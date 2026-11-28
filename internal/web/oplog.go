// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"verify/internal/applog"
	"verify/internal/config"
	"verify/internal/oplog"
	"verify/internal/store"
)

// 鉴权失败按来源 IP 做 60 秒节流：/api/status 每 15 秒轮询一次，
// 令牌失效时如果每次都记一行，操作记录会被同一件事刷满。
var (
	opDeniedMu sync.Mutex
	opDeniedAt = map[string]time.Time{}
)

// recordOp 写一条操作记录。普通操作审计失败只记警告，不阻断主流程（fail-open）；
// 不可逆删除的 fail-closed 在 internal/engine/trash.go 里单独处理。
func (s *Server) recordOp(r *http.Request, e oplog.Entry) {
	e.FromContext(r.Context())
	limit := s.cfg.Get().OpLogDetailLimit
	if err := s.st.InsertOp(r.Context(), e, limit); err != nil {
		applog.Warn("写入操作记录失败（%s）：%v", e.Action, err)
	}
}

// recordDenied 记录一次「被拒绝的请求」，同一 IP 一分钟内只记一条。
// v3.17.4（F-21）：reason 说明拒绝原因（401 无令牌、403 跨站 / 缺请求标记），
// 写进操作记录后能把「谁在试什么」和「为什么被拦」对上，不再只有一句「无有效令牌」。
func (s *Server) recordDenied(r *http.Request, reason string) {
	ip := oplog.ClientIP(r.RemoteAddr)
	opDeniedMu.Lock()
	if last, ok := opDeniedAt[ip]; ok && time.Since(last) < time.Minute {
		opDeniedMu.Unlock()
		return
	}
	if len(opDeniedAt) > 1000 { // 防御性清理，正常局域网场景远达不到
		opDeniedAt = map[string]time.Time{}
	}
	opDeniedAt[ip] = time.Now()
	opDeniedMu.Unlock()

	e := oplog.New("auth_denied")
	e.Actor = ip
	e.Result = oplog.ResultFail
	e.Summary = reason + "：" + r.Method + " " + oplog.SafePath(r.URL.Path)
	s.recordOp(r, e)
}

// opSkipPaths 里的接口不产生中间件汇总行：要么高频只读、要么是 SSE 长连接，
// 要么已经由业务层写了更详细的记录（隔离区四操作、登录、设置变更）。
var opSkipPaths = map[string]bool{
	"/":                  true,
	"/favicon.ico":       true,
	"/api/status":        true,
	"/api/events":        true,
	"/api/raw":           true,
	"/api/thumb":         true,
	"/api/oplog":         true,
	"/api/oplog/export":  true,
	"/api/oplog/clear":   true,
	"/api/trash/export":  true,
	"/api/login":         true,
	"/api/logout":        true,
	"/api/trash":         true,
	"/api/trash/restore": true,
	"/api/trash/delete":  true,
	"/api/trash/empty":   true,
	"/api/settings":      true,
}

// opActionMap 给常见写接口一个稳定的中文动作名；没列出的按路径自动生成。
var opActionMap = map[string]string{
	"/api/scan":                 "scan_start",
	"/api/scan/stop":            "scan_stop",
	"/api/tasks/pause":          "tasks_pause",
	"/api/tasks/resume":         "tasks_resume",
	"/api/health/scan":          "health_scan",
	"/api/health/cancel":        "health_cancel",
	"/api/health/rename":        "health_rename",
	"/api/health/fix-encoding":  "health_fix_encoding",
	"/api/health/trash":         "health_trash",
	"/api/health/undo":          "health_undo",
	"/api/move":                 "move",
	"/api/move/dir":             "move_dir",
	"/api/move/dir/undo":        "move_dir_undo",
	"/api/move/undo":            "move_undo",
	"/api/relink":               "relink",
	"/api/transcode":            "transcode",
	"/api/transcode/cancel":     "transcode_cancel",
	"/api/trim":                 "trim",
	"/api/rename/execute":       "rename_execute",
	"/api/folder_rename":        "folder_rename",
	"/api/groups/rename":        "group_rename",
	"/api/file/rename":          "file_rename",
	"/api/fanjian":              "fanjian",
	"/api/emptydir/clean":       "emptydir_clean",
	"/api/cache/clear":          "cache_clear",
	"/api/cleanup/run":          "cleanup_run",
	"/api/cleanup/plans/delete": "cleanup_plan_delete",
	"/api/export":               "export",
	"/api/report":               "report",
	"/api/import":               "import",
	"/api/sync/import":          "sync_import",
	"/api/sync/export":          "sync_export",
	"/api/fs/mkdir":             "fs_mkdir",
	"/api/search/rebuild":       "search_rebuild",
	"/api/video/reanalyze":      "video_reanalyze",
	"/api/intro/reanalyze":      "intro_reanalyze",
	"/api/video_tags/recompute": "video_tags_recompute",
	"/api/retain/pref/clear":    "retain_pref_clear",
	"/api/retain/pref/apply":    "retain_pref_apply",
	"/api/trash/export":         "trash_export",
	"/api/oplog/export":         "oplog_export",
	"/api/oplog/clear":          "oplog_clear",
}

func opAction(method, path string) string {
	if a, ok := opActionMap[path]; ok {
		return a
	}
	name := strings.ReplaceAll(strings.TrimPrefix(path, "/api/"), "/", "_")
	return "api_" + strings.ToLower(method) + "_" + name
}

// statusRecorder 只捕获状态码，不改变原有写响应行为。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusRecorder) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// opLogMiddleware 兜底记录所有写请求：谁、何时、哪个接口、成功失败、耗时、
// 以及请求体里的 paths（对哪些文件）。业务层另有更细记录的接口在 opSkipPaths 里跳过。
func (s *Server) opLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := oplog.WithActor(r.Context(), oplog.ClientIP(r.RemoteAddr))
		ctx = oplog.WithReqID(ctx, oplog.NewReqID())
		r = r.WithContext(ctx)
		if !s.shouldLogRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		details, count := requestDetails(r)
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r)
		code := rec.statusCode()
		// v3.10.10：401/403 已由 recordDenied 单独记录（自带按 IP 节流）。
		// 中间件再记一行会让「令牌错误」这一件事把操作记录刷满，且两行语义重复。
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			return
		}
		e := oplog.New(opAction(r.Method, r.URL.Path))
		e.Summary = fmt.Sprintf("%s %s → %d", r.Method, oplog.SafePath(r.URL.Path), code)
		e.TargetCount = count
		e.Details = details
		e.DurationMS = time.Since(start).Milliseconds()
		if code >= 400 {
			e.Result = oplog.ResultFail
			e.Error = fmt.Sprintf("HTTP %d", code)
		}
		s.recordOp(r, e)
	})
}

func (s *Server) shouldLogRequest(r *http.Request) bool {
	cfg := s.cfg.Get()
	if !cfg.OpLogEnabled {
		return false
	}
	path := r.URL.Path
	if opSkipPaths[path] || strings.HasPrefix(path, "/ui/") {
		return false
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return cfg.OpLogReads
	}
	return true
}

// requestDetails 只读小 JSON 请求体里的 paths / ids，绝不记录令牌、Cookie 或完整请求体。
// 上传类请求（multipart、大 body、chunked）一律跳过，避免破坏原请求。
func requestDetails(r *http.Request) ([]oplog.Detail, int) {
	if r.Body == nil || r.ContentLength < 0 || r.ContentLength > 1<<20 {
		return nil, 0
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return nil, 0
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, 0
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var payload struct {
		Paths []string `json:"paths"`
		IDs   []int64  `json:"ids"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil, 0
	}
	details := make([]oplog.Detail, 0, len(payload.Paths))
	for _, p := range payload.Paths {
		details = append(details, oplog.Detail{Path: p})
	}
	return details, len(payload.Paths) + len(payload.IDs)
}

// opSensitiveFields 的值绝不写进操作记录，只记「已修改」。
var opSensitiveFields = map[string]bool{
	"access_token":     true,
	"password_hash":    true,
	"current_password": true,
	"new_password":     true,
	"ai_endpoint":      true,
}

func configToMap(c *config.Config) map[string]any {
	// #nosec G117 -- 只在本进程内转成 map 做字段比对，敏感字段在 configDiffDetails 里只记「已修改」，不写值、不落盘
	b, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// configDiffDetails 逐字段比较保存前后的配置，只返回真正变化的项。
func configDiffDetails(old, cur *config.Config) []oplog.Detail {
	om, cm := configToMap(old), configToMap(cur)
	if om == nil || cm == nil {
		return nil
	}
	keys := make([]string, 0, len(cm))
	for k := range cm {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]oplog.Detail, 0, 4)
	for _, k := range keys {
		if reflect.DeepEqual(om[k], cm[k]) {
			continue
		}
		d := oplog.Detail{Field: k}
		if opSensitiveFields[k] {
			d.Old, d.New = "（已隐藏）", "（已隐藏）"
		} else {
			d.Old = clipText(fmt.Sprint(om[k]), 200)
			d.New = clipText(fmt.Sprint(cm[k]), 200)
		}
		out = append(out, d)
	}
	return out
}

func opQueryFromRequest(r *http.Request) store.OpQuery {
	q := store.OpQuery{
		Action:  r.URL.Query().Get("action"),
		Result:  r.URL.Query().Get("result"),
		Keyword: r.URL.Query().Get("keyword"),
	}
	q.Since, _ = strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	q.Until, _ = strconv.ParseInt(r.URL.Query().Get("until"), 10, 64)
	q.Page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	q.PageSize, _ = strconv.Atoi(r.URL.Query().Get("page_size"))
	// v3.10.11（第七轮 P2-3）：before_id 非 0 时走 keyset 翻页，避免深分页 OFFSET 线性扫描。
	q.BeforeID, _ = strconv.ParseInt(r.URL.Query().Get("before_id"), 10, 64)
	if q.BeforeID < 0 {
		q.BeforeID = 0
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 || q.PageSize > 200 {
		q.PageSize = 50
	}
	return q
}

// handleOpLog 是操作记录的只读查询接口。
func (s *Server) handleOpLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	q := opQueryFromRequest(r)
	items, total, err := s.st.ListOps(r.Context(), q)
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	resp := oplogListResponse{
		Items: items, Total: total, Page: q.Page, PageSize: q.PageSize,
	}
	// 给满页时返回下一页游标：前端拿 before_id 再请求即可跳过 OFFSET。
	if len(items) == q.PageSize {
		resp.NextBeforeID = items[len(items)-1].ID
	}
	writeJSON(w, 200, resp)
}

// handleOpLogExport 把当前筛选结果导出为 CSV（带 BOM，Excel 直接可开）。
func (s *Server) handleOpLogExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	q := opQueryFromRequest(r)
	q.All = true

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="verify-oplog.csv"`)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"时间", "来源 IP", "动作", "结果", "摘要", "条数", "明细", "错误", "耗时(ms)"})
	// v3.10.8（P2-10）：边查边写，不把整张 op_log 读进内存。
	n, walkErr := s.st.WalkOps(r.Context(), q, func(row store.OpRow) bool {
		werr := cw.Write([]string{
			time.UnixMilli(row.TS).Format("2006-01-02 15:04:05"),
			row.Actor, csvSafe(row.Action), csvSafe(row.Result), csvSafe(row.Summary),
			strconv.Itoa(row.TargetCount), csvSafe(row.DetailJSON), csvSafe(row.Error),
			strconv.FormatInt(row.DurationMS, 10),
		})
		return werr == nil
	})
	cw.Flush()
	if ferr := cw.Error(); walkErr == nil && ferr != nil {
		// v3.10.8：客户端断开/写盘失败时 csv.Writer 会停在这里，必须据此记为部分成功，
		// 否则审计里会留下一条「导出成功但文件不全」的记录。
		walkErr = ferr
	}
	e := oplog.New("oplog_export")
	e.Summary = fmt.Sprintf("导出操作记录 %d 条", n)
	e.TargetCount = n
	if walkErr != nil {
		e.Result = oplog.ResultPartial
		e.Error = walkErr.Error()
		applog.Warn("导出操作记录中断：%v", walkErr)
	}
	s.recordOp(r, e)
}

// csvSafe 防止以 = + - @ 或制表符/回车开头的单元格在 Excel 里被当公式执行。
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// handleOpLogClear 清理指定时间之前的记录，必须二次确认；清理动作本身先留痕。
func (s *Server) handleOpLogClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Before  int64 `json:"before_ts"`
		Confirm bool  `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	now := time.Now().UnixMilli()
	if !body.Confirm || body.Before <= 0 || body.Before > now {
		writeJSON(w, 400, errorResponse{Error: "需要确认，且清理时间不能是未来"})
		return
	}
	e := oplog.New("oplog_clear")
	e.Summary = "清理 " + time.UnixMilli(body.Before).Format("2006-01-02 15:04:05") + " 之前的操作记录"
	// v3.10.9：清空审计是不可逆操作，审计自身写不进去就拒绝执行（与清空隔离区同口径）。
	// 此前走 recordOp 的 fail-open，数据库写失败只告警仍照删，事后无法回答「谁清了审计」。
	e.FromContext(r.Context())
	if err := s.st.InsertOp(r.Context(), e, s.cfg.Get().OpLogDetailLimit); err != nil {
		applog.Warn("清理操作记录前写入审计失败，已拒绝执行：%v", err)
		writeJSON(w, 500, errorResponse{Error: "操作记录写入失败，已取消清理，请查看服务端日志"})
		return
	}
	n, err := s.st.PurgeOpsBefore(r.Context(), body.Before)
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: publicErrorText(err.Error())})
		return
	}
	writeJSON(w, 200, okRemovedResponse{OK: true, Removed: n})
}
