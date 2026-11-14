// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"verify/internal/engine"
	"verify/internal/store"
)

// renameRules 是清单式改名的最小规则集（v2.10.0）。
// V3.5 会把这里扩展成 methods 数组（链式组合），接口形态保持不变。
type renameRules struct {
	Prefix      string `json:"prefix"`
	Suffix      string `json:"suffix"`
	Start       int    `json:"start"`  // >0 时在扩展名前追加序号
	Digits      int    `json:"digits"` // 序号零填充位数，默认 2
	ReplaceFrom string `json:"replace_from"`
	ReplaceTo   string `json:"replace_to"`
	Case        string `json:"case"` // upper / lower / title
	Trim        bool   `json:"trim"`
}

type renameFile struct {
	Path string `json:"path"`
	Name string `json:"name"` // 留空表示按规则从当前文件名生成
}

type renameReq struct {
	Files    []renameFile          `json:"files"`
	Rules    renameRules           `json:"rules"`
	Methods  []engine.RenameMethod `json:"methods"`
	PresetID int64                 `json:"preset_id"`
}

// applyRenameRules 按最小规则集生成新文件名；扩展名保持不变。
func applyRenameRules(name string, idx int, r renameRules) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if r.ReplaceFrom != "" {
		stem = strings.ReplaceAll(stem, r.ReplaceFrom, r.ReplaceTo)
	}
	switch r.Case {
	case "upper":
		stem = strings.ToUpper(stem)
	case "lower":
		stem = strings.ToLower(stem)
	case "title":
		stem = titleCase(stem)
	}
	if r.Trim {
		stem = strings.TrimSpace(stem)
	}
	stem = r.Prefix + stem + r.Suffix
	if r.Start > 0 {
		digits := r.Digits
		if digits < 1 {
			digits = 2
		}
		// v3.17.3（F-07）：入口已拦 >10，这里兜底，避免其它调用方绕过校验。
		if digits > 10 {
			digits = 10
		}
		stem += fmt.Sprintf("%0*d", digits, r.Start+idx)
	}
	if stem == "" {
		return name
	}
	return stem + ext
}

// titleCase 把每个分隔片段的首字母转大写，其余小写（简易 title case）。
func titleCase(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		switch r {
		case ' ', '-', '_', '.', '·':
			b.WriteRune(r)
			up = true
			continue
		}
		if up {
			b.WriteString(strings.ToUpper(string(r)))
			up = false
		} else {
			b.WriteString(strings.ToLower(string(r)))
		}
	}
	return b.String()
}

// validateRenameDigits 拦截过大的序号位数（v3.17.3 F-07）。
// Digits 决定零填充宽度，1000000 会让单次格式化分配 1MB，上千文件叠加即 OOM；
// 0 表示沿用默认值，视为合法。
func validateRenameDigits(req renameReq) string {
	inRange := func(d int) bool { return d >= 0 && d <= 10 }
	if !inRange(req.Rules.Digits) {
		return "序号位数必须在 1–10 之间"
	}
	for _, m := range req.Methods {
		if !inRange(m.Digits) {
			return "序号位数必须在 1–10 之间"
		}
	}
	return ""
}

// handleRenamePreview POST /api/rename/preview — dry-run 返回每个文件的新名（v2.10.0）。
func (s *Server) handleRenamePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var req renameReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if len(req.Files) == 0 {
		writeJSON(w, 400, map[string]any{"error": "没有要改名的文件"})
		return
	}
	if msg := validateRenameDigits(req); msg != "" {
		writeJSON(w, 400, map[string]any{"error": msg})
		return
	}
	// v3.5.0：带 methods 时走规则引擎的 dry-run（纯字符串计算，不落盘）。
	if len(req.Methods) > 0 {
		paths := make([]string, 0, len(req.Files))
		for _, f := range req.Files {
			paths = append(paths, f.Path)
		}
		plan := s.e.BuildRenamePlan(r.Context(), paths, req.Methods)
		items := make([]store.RenameBatchItem, 0, len(plan))
		for _, p := range plan {
			items = append(items, store.RenameBatchItem{Path: p.OldPath, Orig: p.OldName, Target: p.NewName, NewPath: p.NewPath, Error: p.Error})
		}
		writeJSON(w, 200, map[string]any{"items": items, "count": len(items), "plan": plan})
		return
	}
	items := make([]store.RenameBatchItem, 0, len(req.Files))
	for i, f := range req.Files {
		orig := filepath.Base(f.Path)
		target := f.Name
		if strings.TrimSpace(target) == "" {
			target = applyRenameRules(orig, i, req.Rules)
		}
		items = append(items, store.RenameBatchItem{Path: f.Path, Orig: orig, Target: target})
	}
	writeJSON(w, 200, map[string]any{"items": items, "count": len(items)})
}

// handleRenameExecute POST /api/rename/execute — 执行清单式改名，返回 batch_id（v2.10.0）。
func (s *Server) handleRenameExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var req renameReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if len(req.Files) == 0 {
		writeJSON(w, 400, map[string]any{"error": "没有要改名的文件"})
		return
	}
	if msg := validateRenameDigits(req); msg != "" {
		writeJSON(w, 400, map[string]any{"error": msg})
		return
	}
	if s.scanBusy(w) {
		return
	}
	// v3.5.0：带 methods 时走规则引擎（并发 rename + 批量落库）。
	if len(req.Methods) > 0 {
		paths := make([]string, 0, len(req.Files))
		for _, f := range req.Files {
			paths = append(paths, f.Path)
		}
		plan := s.e.BuildRenamePlan(r.Context(), paths, req.Methods)
		res, err := s.e.ExecuteRenamePlan(r.Context(), plan, req.Methods, req.PresetID)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, res)
		return
	}
	items := make([]store.RenameBatchItem, 0, len(req.Files))
	done, failed := 0, 0
	// v3.10.5：直接指定新名的批量改名不经过 ExecuteRenamePlan，这里单独拍快照并对账。
	origPaths := make([]string, 0, len(req.Files))
	for _, f := range req.Files {
		origPaths = append(origPaths, f.Path)
	}
	snap := s.e.BeginOpSnapshot(r.Context(), "rename_direct", origPaths)
	for i, f := range req.Files {
		orig := filepath.Base(f.Path)
		target := f.Name
		if strings.TrimSpace(target) == "" {
			target = applyRenameRules(orig, i, req.Rules)
		}
		it := store.RenameBatchItem{Path: f.Path, Orig: orig, Target: target}
		if target == orig {
			// 目标与原名相同不算失败，跳过执行
			it.Error = "目标与原名相同，已跳过"
			snap.Skip(f.Path, it.Error)
			items = append(items, it)
			continue
		}
		newPath, taskIDStr, err := s.e.RenameOne(r.Context(), f.Path, target)
		if err != nil {
			it.Error = err.Error()
			snap.Skip(f.Path, err.Error())
			failed++
			items = append(items, it)
			continue
		}
		it.NewPath = newPath
		it.TaskID, _ = strconv.ParseInt(taskIDStr, 10, 64)
		snap.MarkRenamed(f.Path, newPath)
		done++
		items = append(items, it)
	}
	raw, _ := json.Marshal(items)
	status := "done"
	if failed > 0 && done == 0 {
		status = "failed"
	}
	batchID, err := s.st.CreateRenameBatch(r.Context(), string(raw), status)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": "保存改名批失败：" + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"batch_id": batchID, "done": done, "failed": failed, "items": items, "reconcile": s.e.FinishOpSnapshot(snap)})
}

// handleUnfinishedRenameBatches GET /api/rename/batches/unfinished —
// 列出没有正常收尾的改名批次（v3.10.10 第五轮 P1-2 / v3.10.13）。
//
// 响应固定拆成两个数组：batches 只含崩溃遗留的 running 批次（老客户端语义不变），
// residual 含撤销没撤干净（undone_partial / undone_failed）与恢复后有歧义
// （repaired_partial）的批次；每条都带 status 与 failed 字段。
// v3.10.13 之前只查 running，用户刷新页面后部分撤销的残项会整批消失，等于没提醒。
// 只读查询；用户确认后仍走现有 POST /api/rename/undo/{id} 撤销，本接口不动任何文件。
func (s *Server) handleUnfinishedRenameBatches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	all, err := s.st.ListUnfinishedRenameBatches(ctx, 40, true)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": "读取未完成改名批次失败：" + err.Error()})
		return
	}
	batches := make([]store.RunningRenameBatch, 0, len(all))
	residual := make([]store.RunningRenameBatch, 0, len(all))
	for _, b := range all {
		if b.Status == "running" {
			batches = append(batches, b)
		} else {
			residual = append(residual, b)
		}
	}
	writeJSON(w, 200, map[string]any{"batches": batches, "residual": residual})
}

// handleRenameUndo POST /api/rename/undo/{batch_id} — 按批撤销（v2.10.0）。
func (s *Server) handleRenameUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	// v3.10.8（P1-1）：按批撤销改名与扫描并发会让索引与磁盘错位
	if s.scanBusy(w) {
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/rename/undo/"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 400, map[string]any{"error": "缺少或非法的 batch_id"})
		return
	}
	done, failed, status, failures, uerr := s.e.UndoRenameBatch(r.Context(), id)
	if uerr != nil {
		writeJSON(w, 404, map[string]any{"error": uerr.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "done": done, "failed": failed, "status": status, "items": failures})
}
