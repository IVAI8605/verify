// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// v3.21.0 两阶段改名：先建服务端计划，再按 2000 条一片执行。
//
// 为什么要两阶段：40 万条清单如果一次性上传并一次性执行，请求体、WAL、内存
// 三处都会爆。计划表把「算好新名 + 全局解决撞名」与「逐个 rename」拆开，
// 每片都是独立的小事务，中断后可以从剩下的 pending 行继续。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"verify/internal/applog"
	"verify/internal/engine"
	"verify/internal/store"
)

// renamePlanBodyLimit 是计划上传的请求体上限。
// 40 万条路径约 32MB，留一倍余量；走搜索凭据时请求体只有几百字节。
const renamePlanBodyLimit = 64 << 20

// renamePlanMaxPaths 是单次计划的最大条目数，防止误点全选把库拖垮。
const renamePlanMaxPaths = 500000

// errReplacementChar 由流式护栏返回，处理函数翻译成用户能看懂的 400。
var errReplacementChar = errors.New("路径包含无法识别的文件名字符，请从文件列表重新选择")

type renamePlanReq struct {
	Files          []renameFile          `json:"files"`
	Methods        []engine.RenameMethod `json:"methods"`
	SelectionToken string                `json:"selection_token"`
	Excludes       []string              `json:"excludes"`
}

// handleRenamePlan POST /api/rename/plan — 生成改名计划并落库（v3.21.0）。
// 入参二选一：显式 files 清单，或 v3.20 的搜索全选凭据 selection_token。
func (s *Server) handleRenamePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var req renamePlanReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, errReplacementChar) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": errReplacementChar.Error()})
			return
		}
		// v3.25.0（P2-2）：超过 64MiB 时 MaxBytesReader 报的是
		// http.MaxBytesError，旧实现统一按 400「字段类型不对」返回，调用方以为数据格式有问题。
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "改名计划请求体过大，请分多批提交"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if len(req.Methods) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少改名方法"})
		return
	}
	if s.scanBusy(w) {
		return
	}
	var paths []string
	source := "explicit"
	if strings.TrimSpace(req.SelectionToken) != "" {
		var err error
		paths, err = s.resolveSelectionPaths(r.Context(), req.SelectionToken, req.Excludes)
		if err != nil {
			writeJSON(w, http.StatusGone, map[string]any{"error": err.Error()})
			return
		}
		source = "search"
	}
	// 显式清单与全选凭据可以同时存在：搜索页加了全选，下面又补了几条手选的。
	seen := make(map[string]bool, len(paths)+len(req.Files))
	merged := paths[:0]
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			merged = append(merged, p)
		}
	}
	for _, f := range req.Files {
		if p := strings.TrimSpace(f.Path); p != "" && !seen[p] {
			seen[p] = true
			merged = append(merged, p)
		}
	}
	paths = merged
	if len(paths) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "没有要改名的文件"})
		return
	}
	if len(paths) > renamePlanMaxPaths {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "一次最多处理 50 万个文件，请收窄筛选后再试"})
		return
	}

	plan := s.e.BuildRenamePlan(r.Context(), paths, req.Methods)
	planID, err := s.st.CreateRenamePlan(r.Context(), len(plan))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "创建改名计划失败：" + err.Error()})
		return
	}
	rows := make([]store.RenamePlanItemRow, 0, len(plan))
	conflicts, errCount := 0, 0
	for i, it := range plan {
		state := "pending"
		if it.Error != "" {
			state = "failed"
			errCount++
		}
		if it.HadCollision {
			conflicts++
		}
		rows = append(rows, store.RenamePlanItemRow{
			Idx: i, OldPath: it.OldPath, OldName: it.OldName, NewName: it.NewName,
			Size: it.Size, Mtime: it.Mtime, State: state, Error: it.Error,
		})
	}
	if err := s.st.AddRenamePlanItems(r.Context(), planID, 0, rows); err != nil {
		// v3.25.0（P2-6）：明细没写进去，这份头就是 total>0 但一条明细都没有的孤儿。
		// 顺手清掉，失败只告警——真正的错误下面已经返回给调用方了。
		if derr := s.st.DiscardRenamePlan(context.WithoutCancel(r.Context()), planID); derr != nil {
			applog.Warn("清理未写入明细的改名计划失败：plan=%d（%v）", planID, derr)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "保存改名计划失败：" + err.Error()})
		return
	}
	// 预览只回前 200 条：40 万条全回会把响应撑大，界面也看不过来。
	n := len(plan)
	if n > 200 {
		n = 200
	}
	samples := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		it := plan[i]
		samples = append(samples, map[string]any{
			"path": it.OldPath, "orig": it.OldName, "target": it.NewName, "error": it.Error,
		})
	}
	writeJSON(w, 200, map[string]any{
		"plan_id":   planID,
		"total":     len(plan),
		"conflicts": conflicts,
		"errors":    errCount,
		"source":    source,
		"samples":   samples,
	})
}

type renameChunkReq struct {
	PlanID int64 `json:"plan_id"`
	Limit  int   `json:"limit"`
}

// handleRenameExecuteChunk POST /api/rename/execute-chunk — 执行下一片（v3.21.0）。
// 服务端按 pending 顺序取一片，执行完回填状态；重复调用即可续跑。
func (s *Server) handleRenameExecuteChunk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var req renameChunkReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if req.PlanID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少计划 ID"})
		return
	}
	ok, err := s.st.RenamePlanExists(r.Context(), req.PlanID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "改名计划不存在或已清理，请重新生成"})
		return
	}
	// v3.22.0：取消只在片边界生效。置过取消标记后，本片（若已在跑）会跑完，
	// 下一次分片请求直接返回「已取消」，剩余 pending 保留可续跑。
	if s.renameCancel.take(req.PlanID) {
		total, done, failed, pending, _, _ := s.st.RenamePlanStatus(r.Context(), req.PlanID)
		writeJSON(w, 200, map[string]any{
			"cancelled": true, "done": 0, "failed": 0, "remaining": pending,
			"total": total, "total_done": done, "total_failed": failed,
		})
		return
	}
	rows, err := s.st.NextRenamePlanItems(r.Context(), req.PlanID, req.Limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if len(rows) == 0 {
		total, done, failed, pending, status, serr := s.st.RenamePlanStatus(r.Context(), req.PlanID)
		if serr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": serr.Error()})
			return
		}
		if pending == 0 && status != "done" {
			_ = s.st.SetRenamePlanStatus(r.Context(), req.PlanID, "done")
		}
		writeJSON(w, 200, map[string]any{
			"done": 0, "failed": 0, "remaining": pending,
			"total": total, "total_done": done, "total_failed": failed,
		})
		return
	}
	if s.scanBusy(w) {
		return
	}
	chunk := make([]engine.RenamePlanItem, 0, len(rows))
	for _, row := range rows {
		chunk = append(chunk, engine.RenamePlanItem{
			OldPath: row.OldPath,
			OldName: row.OldName,
			NewName: row.NewName,
			NewPath: filepath.Join(filepath.Dir(row.OldPath), row.NewName),
			Size:    row.Size,
			Mtime:   row.Mtime,
		})
	}
	res, err := s.e.ExecuteRenamePlan(r.Context(), chunk, nil, 0)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// v3.22.0：每批失败明细单独落盘，不依赖界面导出，也方便事后对账。
	s.writeRenameFailureCSV(res.BatchID, res.Items)
	results, skipped := renameChunkResults(rows, res.Items, res.BatchID)
	if err := s.st.MarkRenamePlanResults(r.Context(), req.PlanID, results); err != nil {
		// 文件已经改完，回填失败只告警；重复执行会因「目标不存在」被逐条标记失败，不会重复改名。
		writeJSON(w, 200, map[string]any{
			"batch_id": res.BatchID, "done": res.Done, "failed": res.Failed,
			"remaining": skipped, "warning": "执行结果回填失败：" + err.Error(),
			"items": res.Items, "reconcile": res.Reconcile,
		})
		return
	}
	total, done, failed, pending, status, _ := s.st.RenamePlanStatus(r.Context(), req.PlanID)
	switch {
	case skipped > 0 && pending > 0:
		// 取消发生在片中间：本片只改了一部分，剩余 pending 保留可续跑，
		// 计划头标 partial，避免界面把它当成全部完成。
		if status != "partial" {
			_ = s.st.SetRenamePlanStatus(r.Context(), req.PlanID, "partial")
		}
	case pending == 0 && status != "done":
		_ = s.st.SetRenamePlanStatus(r.Context(), req.PlanID, "done")
	}
	writeJSON(w, 200, map[string]any{
		"batch_id": res.BatchID, "done": res.Done, "failed": res.Failed,
		"remaining": pending, "total": total, "total_done": done, "total_failed": failed,
		"items": res.Items, "reconcile": res.Reconcile,
	})
}

// renameChunkResults 把引擎逐项结果映射成计划表回填行（v3.25.0）。
//
// 引擎在 ctx 取消时不会给没来得及处理的条目填 Error，NewPath 也是空。
// 旧实现把它们一律按 done 回填，计划头随即被标完成、pending 归零：
// 界面显示「已完成」，实际最多 2000 个文件没动，而且再没有可续跑的 pending 行。
// 这里把「没执行」单独识别为 pending，只有确实改成功才记 done。
func renameChunkResults(rows []store.RenamePlanItemRow, items []store.RenameBatchItem, batchID int64) (out []store.RenamePlanResult, skipped int) {
	out = make([]store.RenamePlanResult, 0, len(rows))
	for i, row := range rows {
		state, msg := "done", ""
		switch {
		case i >= len(items):
			state, msg = "pending", "任务已取消，未执行"
		case items[i].Error != "":
			state, msg = "failed", items[i].Error
		case items[i].NewPath == "":
			state, msg = "pending", "任务已取消，未执行"
		}
		if state == "pending" {
			skipped++
		}
		out = append(out, store.RenamePlanResult{Idx: row.Idx, State: state, Error: msg, BatchID: batchID})
	}
	return out, skipped
}

// handleRenamePlanStatus GET /api/rename/plan/status?plan_id= — 查询计划进度（v3.21.0）。
// 界面刷新后用它决定是否还有未执行完的片可以续跑。
func (s *Server) handleRenamePlanStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("plan_id")), 10, 64)
	if id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "无效的计划 ID"})
		return
	}
	total, done, failed, pending, status, err := s.st.RenamePlanStatus(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "改名计划不存在或已清理"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"plan_id": id, "total": total, "done": done, "failed": failed,
		"pending": pending, "status": status,
	})
}

// ---------- v3.22.0：取消 / 失败明细 / 最近批次 / 多批撤销 ----------

// renameCancelStore 记录用户请求取消的计划。
// 只活在内存里：重启后没有正在跑的分片，取消标记本来也没有意义。
type renameCancelStore struct {
	mu sync.Mutex
	m  map[int64]bool
}

func newRenameCancelStore() *renameCancelStore {
	return &renameCancelStore{m: map[int64]bool{}}
}

func (c *renameCancelStore) set(planID int64, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if on {
		c.m[planID] = true
	} else {
		delete(c.m, planID)
	}
}

// take 读取并清除取消标记；续跑时第一次分片请求就会把它消耗掉。
func (c *renameCancelStore) take(planID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	on := c.m[planID]
	delete(c.m, planID)
	return on
}

type renameCancelReq struct {
	PlanID int64 `json:"plan_id"`
	Clear  bool  `json:"clear"`
}

// handleRenameCancel POST /api/rename/cancel — 请求取消或清除取消标记（v3.22.0）。
func (s *Server) handleRenameCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var req renameCancelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if req.PlanID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少计划 ID"})
		return
	}
	s.renameCancel.set(req.PlanID, !req.Clear)
	writeJSON(w, 200, map[string]any{"ok": true, "plan_id": req.PlanID, "cancel": !req.Clear})
}

type renameUndoBatchReq struct {
	BatchIDs []int64 `json:"batch_ids"`
}

// handleRenameUndoBatch POST /api/rename/undo-batch — 逆序撤销多个批次（v3.22.0）。
// 后改的先撤：同一文件被两次改名时，顺序反了会互相覆盖。
func (s *Server) handleRenameUndoBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var req renameUndoBatchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if len(req.BatchIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "没有要撤销的批次"})
		return
	}
	if len(req.BatchIDs) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "一次最多撤销 200 个批次"})
		return
	}
	type batchResult struct {
		BatchID int64  `json:"batch_id"`
		Done    int    `json:"done"`
		Failed  int    `json:"failed"`
		Status  string `json:"status"`
		Error   string `json:"error,omitempty"`
	}
	done, failed := 0, 0
	var items []engine.HealthOpItem
	batches := make([]batchResult, 0, len(req.BatchIDs))
	for i := len(req.BatchIDs) - 1; i >= 0; i-- {
		id := req.BatchIDs[i]
		d, f, status, failures, err := s.e.UndoRenameBatch(r.Context(), id)
		if err != nil {
			batches = append(batches, batchResult{BatchID: id, Status: "error", Error: err.Error()})
			failed++
			continue
		}
		done += d
		failed += f
		items = append(items, failures...)
		batches = append(batches, batchResult{BatchID: id, Done: d, Failed: f, Status: status})
	}
	writeJSON(w, 200, map[string]any{"done": done, "failed": failed, "items": items, "batches": batches})
}

// handleRecentRenameBatches GET /api/rename/batches/recent — 最近批次（v3.22.0）。
// 页面刷新或关掉再进时，用它恢复撤销入口，不依赖浏览器内存里的 batch_id。
func (s *Server) handleRecentRenameBatches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	list, err := s.st.RecentRenameBatches(r.Context(), int(intParam(r, "limit", 20, 1, 100)))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"batches": list})
}

// writeRenameFailureCSV 把一批失败明细写成 CSV（v3.22.0）。
// 单独命名（rename-failures-<batch>.csv），不参与 verify.log 的轮转统计。
func (s *Server) writeRenameFailureCSV(batchID int64, items []store.RenameBatchItem) {
	if batchID <= 0 {
		return
	}
	failed := make([]store.RenameBatchItem, 0, len(items))
	for _, it := range items {
		if it.Error != "" {
			failed = append(failed, it)
		}
	}
	if len(failed) == 0 {
		return
	}
	dir := strings.TrimSpace(s.cfg.Get().LogDir)
	if dir == "" {
		dir = filepath.Join(s.dataDir, "logs")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		applog.Warn("创建改名失败明细目录失败：%v", err)
		return
	}
	var b strings.Builder
	b.WriteString("\ufeff路径,目标名,原因,时间\r\n")
	now := time.Now().Format("2006-01-02 15:04:05")
	for _, it := range failed {
		b.WriteString(csvEscape(it.Path) + "," + csvEscape(it.Target) + "," + csvEscape(it.Error) + "," + csvEscape(now) + "\r\n")
	}
	path := filepath.Join(dir, fmt.Sprintf("rename-failures-%d.csv", batchID))
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		applog.Warn("写入改名失败明细失败（%s）：%v", path, err)
		return
	}
	applog.Info("改名批次 %d 有 %d 条失败，明细已写入 %s", batchID, len(failed), path)
}

// csvEscape 按 RFC 4180 转义 CSV 单元格。
func csvEscape(v string) string {
	if strings.ContainsAny(v, ",\"\r\n") {
		return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
	}
	return v
}
