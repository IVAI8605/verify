// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"verify/internal/applog"
	"verify/internal/search"
	"verify/internal/store"
)

// searchQuery 把 URL 参数转成 search.Query（v2.10.0）。
func searchQuery(r *http.Request) search.Query {
	q := r.URL.Query()
	qp := search.Query{
		Text:          strings.TrimSpace(q.Get("q")),
		Mode:          strings.TrimSpace(q.Get("mode")),
		CaseSensitive: boolParam(r, "case", false),
		MinSize:       intParam(r, "min_size", 0, 0, 1<<50),
		MaxSize:       intParam(r, "max_size", 0, 0, 1<<50),
		Dir:           strings.TrimSpace(q.Get("dir")),
		Sort:          strings.TrimSpace(q.Get("sort")),
		Order:         strings.TrimSpace(q.Get("order")),
		Offset:        int(intParam(r, "offset", 0, 0, 100000000)),
		Limit:         int(intParam(r, "limit", 200, 1, 2000)),
		// v3.20.0：滚动加载用 keyset 游标；count_only 只要总数。
		Cursor:    strings.TrimSpace(q.Get("cursor")),
		CountOnly: boolParam(r, "count_only", false),
	}
	// 时间参数用毫秒（与其它接口一致），内部转纳秒
	qp.Since = intParam(r, "since_ms", 0, 0, 1<<40) * int64(time.Millisecond)
	qp.Until = intParam(r, "until_ms", 0, 0, 1<<40) * int64(time.Millisecond)
	for _, e := range strings.Split(q.Get("ext"), ",") {
		if e = strings.TrimSpace(e); e != "" {
			qp.Exts = append(qp.Exts, e)
		}
	}
	if qp.Sort == "" {
		qp.Sort = "name"
	}
	if qp.Order == "" {
		qp.Order = "asc"
	}
	return qp
}

// handleFileSearch GET /api/search — 文件名/路径搜索（v2.10.0）。
// 索引就绪时走内存索引；未就绪或超上限降级时回退 SQL LIKE，并在响应里标注。
func (s *Server) handleFileSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	qp := searchQuery(r)
	if qp.Text == "" {
		writeJSON(w, 200, map[string]any{"total": 0, "items": []search.Item{}, "ready": s.searchIndex.Ready()})
		return
	}
	// 目录限定必须是受管扫描目录，避免拿搜索接口探测任意路径
	if qp.Dir != "" {
		cfg := s.cfg.Get()
		if !pathUnderRoots(qp.Dir, cfg.Volumes) {
			writeJSON(w, 400, map[string]any{"error": "目录不在扫描范围内"})
			return
		}
	}
	// 扫描后文件数可能变化，这里做一次轻量检查，必要时后台重建（旧索引继续服务）
	s.searchIndex.RebuildIfStale(r.Context(), s.st.DB())

	res := s.searchIndex.Query(qp)
	notice := ""
	if !s.searchIndex.Ready() {
		// 大库（超过内存上限）或索引仍在构建：回退 SQL 只支持子串，语义会放宽
		res = s.searchSQLFallback(r.Context(), qp)
		notice = "索引未就绪或超出内存上限，已回退数据库模糊匹配（仅支持子串）"
	}

	body := map[string]any{
		"total":     res.Total,
		"took_ms":   res.Took.Milliseconds(),
		"truncated": res.Truncated,
		"degraded":  res.Degraded || notice != "",
		"ready":     s.searchIndex.Ready(),
		"items":     res.Items,
		// v3.20.0：下一页游标；空串表示已经到底。
		"next_cursor": res.NextCursor,
		// files 保留旧 /api/search 的字段名，兼容已有脚本调用
		"files": res.Items,
	}
	if notice != "" {
		body["notice"] = notice
	}
	writeJSON(w, 200, body)
}

// searchSQLFallback 在索引不可用时直接查 files 表（只支持子串）。
func (s *Server) searchSQLFallback(ctx context.Context, qp search.Query) search.Result {
	start := time.Now()
	kw := "%" + store.EscapeLike(strings.ToLower(qp.Text)) + "%"
	// v3.17.4（F-12）：关键字里的 % 与 _ 是普通字符，转义后再当子串匹配。
	where := `status='active' AND (lower(name) LIKE ? ESCAPE '\' OR lower(path) LIKE ? ESCAPE '\')`
	args := []any{kw, kw}
	if len(qp.Exts) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(qp.Exts)), ",")
		where += ` AND lower(ext) IN (` + ph + `)`
		for _, e := range qp.Exts {
			args = append(args, strings.ToLower(strings.TrimPrefix(e, ".")))
		}
	}
	if qp.MinSize > 0 {
		where += ` AND size>=?`
		args = append(args, qp.MinSize)
	}
	if qp.MaxSize > 0 {
		where += ` AND size<=?`
		args = append(args, qp.MaxSize)
	}
	if qp.Since > 0 {
		where += ` AND mtime>=?`
		args = append(args, qp.Since)
	}
	if qp.Until > 0 {
		where += ` AND mtime<?`
		args = append(args, qp.Until)
	}

	var total int
	if err := s.st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE `+where, args...).Scan(&total); err != nil {
		return search.Result{Degraded: true, Took: time.Since(start)}
	}
	if qp.CountOnly {
		return search.Result{Total: total, Degraded: true, Took: time.Since(start)}
	}
	// 降级模式没有内存排序，游标用 "o:<offset>" 表示下次从哪里继续（v3.20.0）。
	off := qp.Offset
	if strings.HasPrefix(qp.Cursor, "o:") {
		if n, err := strconv.Atoi(strings.TrimPrefix(qp.Cursor, "o:")); err == nil && n >= 0 {
			off = n
		}
	}
	// #nosec G202 -- WHERE 子串只由固定片段与占位符拼接，值全部走参数绑定
	rows, err := s.st.DB().QueryContext(ctx, `SELECT id, name, path, ext, size, mtime, volume FROM files WHERE `+where+
		` ORDER BY name LIMIT ? OFFSET ?`, append(args, qp.Limit, off)...)
	if err != nil {
		return search.Result{Degraded: true, Took: time.Since(start)}
	}
	defer func() { _ = rows.Close() }()
	items := make([]search.Item, 0, qp.Limit)
	for rows.Next() {
		var it search.Item
		if err := rows.Scan(&it.ID, &it.Name, &it.Path, &it.Ext, &it.Size, &it.Mtime, &it.Volume); err != nil {
			return search.Result{Degraded: true, Took: time.Since(start)}
		}
		items = append(items, it)
	}
	more := total > off+len(items)
	next := ""
	if more {
		next = "o:" + strconv.Itoa(off+len(items))
	}
	return search.Result{
		Total:      total,
		Items:      items,
		Degraded:   true,
		Truncated:  more,
		NextCursor: next,
		Took:       time.Since(start),
	}
}

// handleSearchStatus GET /api/search/status — 索引条目数、构建状态、降级标记。
func (s *Server) handleSearchStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	st := s.searchIndex.Status()
	writeJSON(w, 200, map[string]any{
		"entries":     st.Entries,
		"built_at":    st.BuiltAt,
		"building":    st.Building,
		"degraded":    st.Degraded,
		"est_bytes":   st.EstBytes,
		"max_entries": st.MaxEntries,
		"ready":       s.searchIndex.Ready(),
	})
}

// handleSearchRebuild POST /api/search/rebuild — 手动重建索引（后台执行）。
func (s *Server) handleSearchRebuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	go func() {
		if _, err := s.searchIndex.Build(context.Background(), s.st.DB()); err != nil {
			applog.Warn("文件搜索索引重建失败：%s", err.Error())
		}
	}()
	writeJSON(w, 200, map[string]any{"ok": true})
}
