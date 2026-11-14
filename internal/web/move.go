// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"net/http"
)

type moveReq struct {
	Target string   `json:"target"`
	Paths  []string `json:"paths"`
}

func (r *moveReq) validate() string {
	if r.Target == "" {
		return "缺少目标目录"
	}
	if len(r.Paths) == 0 {
		return "缺少文件列表"
	}
	return ""
}

// handleMovePreview 移动预检（不产生变更）。
func (s *Server) handleMovePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var body moveReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if msg := body.validate(); msg != "" {
		writeJSON(w, 400, map[string]any{"error": msg})
		return
	}
	writeJSON(w, 200, s.e.MovePreview(r.Context(), body.Target, body.Paths))
}

// handleMove 执行批量移动（同卷 os.Rename，冲突追加不覆盖，audit 可撤销）。
func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	// v3.10.8（P1-1）：扫描进行中移动文件，收尾会把旧路径写回 active
	if s.scanBusy(w) {
		return
	}
	var body moveReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if msg := body.validate(); msg != "" {
		writeJSON(w, 400, map[string]any{"error": msg})
		return
	}
	writeJSON(w, 200, s.e.MoveFiles(r.Context(), body.Target, body.Paths))
}

// handleMoveUndo 撤销一次移动任务（审计逆序 rename 回原位）。
func (s *Server) handleMoveUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	// v3.10.8（P1-1）：撤销移动与扫描并发会让索引与磁盘错位
	if s.scanBusy(w) {
		return
	}
	var body struct {
		TaskID int64 `json:"task_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TaskID <= 0 {
		writeJSON(w, 400, map[string]any{"error": "缺少任务号"})
		return
	}
	done, failed, err := s.e.UndoMove(r.Context(), body.TaskID)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"done": done, "failed": failed})
}

// moveDirReq 是目录级移动的请求体（v3.14.0）。
// src 是源目录、dst 是目标完整路径（含目录名），与 mv src dst 语义一致。
type moveDirReq struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

func (r *moveDirReq) validate() string {
	if r.Src == "" {
		return "缺少源目录"
	}
	if r.Dst == "" {
		return "缺少目标位置"
	}
	return ""
}

// handleMoveDirPreview 目录移动预检（不产生变更）。
func (s *Server) handleMoveDirPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var body moveDirReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if msg := body.validate(); msg != "" {
		writeJSON(w, 400, map[string]any{"error": msg})
		return
	}
	writeJSON(w, 200, s.e.MoveDirPreview(r.Context(), body.Src, body.Dst))
}

// handleMoveDir 执行目录移动（同卷 rename，索引按前缀改写，审计可撤销）。
func (s *Server) handleMoveDir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	// 目录移动改写大量索引行，扫描进行中执行会让收尾把旧路径写回 active
	if s.scanBusy(w) {
		return
	}
	var body moveDirReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if msg := body.validate(); msg != "" {
		writeJSON(w, 400, map[string]any{"error": msg})
		return
	}
	writeJSON(w, 200, s.e.MoveDir(r.Context(), body.Src, body.Dst))
}

// handleMoveDirUndo 撤销一次目录移动（rename 回原位 + 索引前缀改回）。
func (s *Server) handleMoveDirUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	if s.scanBusy(w) {
		return
	}
	var body struct {
		TaskID int64 `json:"task_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TaskID <= 0 {
		writeJSON(w, 400, map[string]any{"error": "缺少任务号"})
		return
	}
	done, failed, err := s.e.UndoMoveDir(r.Context(), body.TaskID)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"done": done, "failed": failed})
}
