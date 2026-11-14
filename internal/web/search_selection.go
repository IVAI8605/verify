// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"verify/internal/search"
)

// searchSelection 是一次「全选全部结果」的服务端凭据（v3.20.0）。
// 只存查询条件与计数，不物化几十万条路径；需要具体路径时按偏移重新查询。
type searchSelection struct {
	q        search.Query
	count    int
	builtAt  int64 // 建立凭据时的索引构建时间，索引重建后凭据立即失效
	dataVer  int64 // 建立凭据时的数据版本，扫描写入后凭据立即失效
	created  time.Time
	lastUsed time.Time
}

// selectionStore 是凭据表：5 分钟不访问即过期，创建时顺手清理旧项。
// 不落盘——页面关闭或服务重启后，旧选择本就应作废。
type selectionStore struct {
	mu  sync.Mutex
	m   map[string]*searchSelection
	ttl time.Duration
}

func newSelectionStore() *selectionStore {
	return &selectionStore{m: map[string]*searchSelection{}, ttl: 5 * time.Minute}
}

// put 登记一份凭据并返回随机令牌；rand 不可用时返回空串由调用方报错。
func (ss *selectionStore) put(q search.Query, count int, builtAt, dataVer int64) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	token := hex.EncodeToString(b)
	now := time.Now()
	ss.mu.Lock()
	for k, v := range ss.m {
		if now.Sub(v.lastUsed) > ss.ttl {
			delete(ss.m, k)
		}
	}
	ss.m[token] = &searchSelection{q: q, count: count, builtAt: builtAt, dataVer: dataVer, created: now, lastUsed: now}
	ss.mu.Unlock()
	return token
}

func (ss *selectionStore) get(token string) (*searchSelection, bool) {
	now := time.Now()
	ss.mu.Lock()
	defer ss.mu.Unlock()
	v, ok := ss.m[token]
	if !ok {
		return nil, false
	}
	if now.Sub(v.lastUsed) > ss.ttl {
		delete(ss.m, token)
		return nil, false
	}
	v.lastUsed = now
	return v, true
}

// searchSelectionBody 与 /api/search 的查询参数同义，只是改成 JSON 传输；
// 另外承载解析阶段需要的 token / offset / excludes。
type searchSelectionBody struct {
	Q        string   `json:"q"`
	Mode     string   `json:"mode"`
	Case     bool     `json:"case"`
	Ext      string   `json:"ext"`
	MinSize  int64    `json:"min_size"`
	MaxSize  int64    `json:"max_size"`
	SinceMS  int64    `json:"since_ms"`
	UntilMS  int64    `json:"until_ms"`
	Dir      string   `json:"dir"`
	Sort     string   `json:"sort"`
	Order    string   `json:"order"`
	Token    string   `json:"token"`
	Offset   int      `json:"offset"`
	Limit    int      `json:"limit"`
	Excludes []string `json:"excludes"`
}

func (b *searchSelectionBody) query() search.Query {
	q := search.Query{
		Text:          strings.TrimSpace(b.Q),
		Mode:          strings.TrimSpace(b.Mode),
		CaseSensitive: b.Case,
		MinSize:       maxInt64(0, b.MinSize),
		MaxSize:       maxInt64(0, b.MaxSize),
		Dir:           strings.TrimSpace(b.Dir),
		Sort:          strings.TrimSpace(b.Sort),
		Order:         strings.TrimSpace(b.Order),
	}
	q.Since = maxInt64(0, b.SinceMS) * int64(time.Millisecond)
	q.Until = maxInt64(0, b.UntilMS) * int64(time.Millisecond)
	for _, e := range strings.Split(b.Ext, ",") {
		if e = strings.TrimSpace(e); e != "" {
			q.Exts = append(q.Exts, e)
		}
	}
	if q.Sort == "" {
		q.Sort = "name"
	}
	if q.Order == "" {
		q.Order = "asc"
	}
	return q
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// resolveSelectionPaths 按凭据在服务端取回全部路径（v3.21.0）。
// 只给改名计划这类「需要完整清单但不想让浏览器上传」的内部调用使用；
// 分批查询避免 40 万条一次性组装成大响应。
func (s *Server) resolveSelectionPaths(ctx context.Context, token string, excludes []string) ([]string, error) {
	sel, ok := s.searchSel.get(strings.TrimSpace(token))
	if !ok {
		return nil, errors.New("选择已过期，请重新全选")
	}
	if sel.builtAt != s.searchIndex.Status().BuiltAt || sel.dataVer != s.st.DataVersion() || !s.searchIndex.Ready() {
		return nil, errors.New("结果已变化，请重新全选")
	}
	q := sel.q
	q.CountOnly = false
	q.Cursor = ""
	q.Offset = 0
	q.Limit = 5000
	if len(excludes) > 0 {
		q.ExcludePaths = make(map[string]bool, len(excludes))
		for _, p := range excludes {
			if p != "" {
				q.ExcludePaths[p] = true
			}
		}
	}
	var out []string
	for {
		res := s.searchIndex.Query(q)
		for _, it := range res.Items {
			out = append(out, it.Path)
		}
		if len(res.Items) == 0 || q.Offset+len(res.Items) >= res.Total {
			break
		}
		q.Offset += len(res.Items)
		if len(out) > renamePlanMaxPaths {
			return nil, errors.New("选择条目过多，请收窄筛选后再试")
		}
	}
	return out, nil
}

// handleSearchSelection POST /api/search/selection — 创建「全选全部结果」凭据（v3.20.0）。
// 只回总数，不回路径；路径由 /api/search/selection/items 分批取。
func (s *Server) handleSearchSelection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var body searchSelectionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式错误"})
		return
	}
	q := body.query()
	if q.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "关键词为空"})
		return
	}
	if q.Dir != "" && !pathUnderRoots(q.Dir, s.cfg.Get().Volumes) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "目录不在扫描范围内"})
		return
	}
	s.searchIndex.RebuildIfStale(r.Context(), s.st.DB())
	if !s.searchIndex.Ready() {
		// 降级模式下没有稳定结果集，全选凭据无从谈起，让前端退回「只选已加载页」。
		writeJSON(w, http.StatusConflict, map[string]any{"error": "索引未就绪，暂不支持全选全部结果"})
		return
	}
	q.CountOnly = true
	res := s.searchIndex.Query(q)
	builtAt := s.searchIndex.Status().BuiltAt
	dataVer := s.st.DataVersion()
	token := s.searchSel.put(q, res.Total, builtAt, dataVer)
	if token == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "生成选择凭据失败"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"token":          token,
		"count":          res.Total,
		"index_built_at": builtAt,
		"data_version":   dataVer,
	})
}

// handleSearchSelectionItems POST /api/search/selection/items — 按凭据分批取路径（v3.20.0）。
// excludes 是「全选后逐条取消」的少数路径，服务端在过滤阶段剔除，总数与之保持同一口径。
func (s *Server) handleSearchSelectionItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var body searchSelectionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式错误"})
		return
	}
	sel, ok := s.searchSel.get(strings.TrimSpace(body.Token))
	if !ok {
		writeJSON(w, http.StatusGone, map[string]any{"error": "选择已过期，请重新全选"})
		return
	}
	// 索引重建或数据变化后，旧凭据指向的结果集已经不是用户看到的那一批。
	if sel.builtAt != s.searchIndex.Status().BuiltAt || sel.dataVer != s.st.DataVersion() || !s.searchIndex.Ready() {
		writeJSON(w, http.StatusGone, map[string]any{"error": "结果已变化，请重新全选"})
		return
	}
	q := sel.q
	q.CountOnly = false
	q.Cursor = ""
	q.Offset = body.Offset
	if q.Offset < 0 {
		q.Offset = 0
	}
	limit := body.Limit
	if limit <= 0 {
		limit = 2000
	}
	if limit > 5000 {
		limit = 5000
	}
	q.Limit = limit
	if len(body.Excludes) > 0 {
		q.ExcludePaths = make(map[string]bool, len(body.Excludes))
		for _, p := range body.Excludes {
			if p != "" {
				q.ExcludePaths[p] = true
			}
		}
	}
	res := s.searchIndex.Query(q)
	writeJSON(w, 200, map[string]any{
		"total":       res.Total,
		"items":       res.Items,
		"next_offset": q.Offset + len(res.Items),
		"took_ms":     res.Took.Milliseconds(),
	})
}
