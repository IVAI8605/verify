// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"net/http"
)

// handleGroupRename 组内统一命名（v1.15）：POST /api/groups/rename {base, paths}。
func (s *Server) handleGroupRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Base  string   `json:"base"`
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	if body.Base == "" || len(body.Paths) == 0 {
		writeJSON(w, 400, errorResponse{Error: "缺少标杆或目标"})
		return
	}
	if s.scanBusy(w) {
		return
	}
	writeJSON(w, 200, toRenameResultResp(s.e.RenameGroupTo(r.Context(), body.Base, body.Paths)))
}

// handleFileRename 单文件改名（v1.15）：POST /api/file/rename {path, name}。
func (s *Server) handleFileRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	if s.scanBusy(w) {
		return
	}
	newPath, taskID, err := s.e.RenameOne(r.Context(), body.Path, body.Name)
	if err != nil {
		writeJSON(w, 400, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, pathTaskIDResponse{Path: newPath, TaskID: taskID})
}

// handleGroupRenameUndo 撤销改名任务：POST /api/groups/rename/undo {task_id}。
func (s *Server) handleGroupRenameUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	// v3.10.8（P1-1）：撤销改名与扫描并发会让索引与磁盘错位
	if s.scanBusy(w) {
		return
	}
	var body struct {
		TaskID int64 `json:"task_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TaskID <= 0 {
		writeJSON(w, 400, errorResponse{Error: "缺少任务号"})
		return
	}
	done, failed, failures, err := s.e.UndoGroupRename(r.Context(), body.TaskID)
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, doneFailedItemsResponse{Done: done, Failed: failed, Items: failures})
}
