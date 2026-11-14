// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"net/http"
	"strconv"

	"verify/internal/engine"
	"verify/internal/store"
)

// handleTrim 批量剪除片头（v1.16）：POST /api/trim {jobs:[{path,group_key,offset,mode,replace,duration}]}。
func (s *Server) handleTrim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	// v3.10.8（P1-1）：剪除会生成新文件并隔离原文件，扫描进行中执行会让索引与磁盘错位
	if s.scanBusy(w) {
		return
	}
	var body struct {
		Jobs []engine.TrimJob `json:"jobs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if len(body.Jobs) == 0 {
		writeJSON(w, 400, map[string]any{"error": "缺少任务"})
		return
	}
	for i := range body.Jobs {
		if body.Jobs[i].Mode != "precise" {
			body.Jobs[i].Mode = "fast"
		}
	}
	writeJSON(w, 200, s.e.TrimIntros(r.Context(), body.Jobs))
}

// handleTrimLog 剪除记录：GET /api/trim/log?limit=100。
func (s *Server) handleTrimLog(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	log, err := s.st.ListTrimLog(r.Context(), limit)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	if log == nil {
		log = []store.TrimLogEntry{}
	}
	writeJSON(w, 200, map[string]any{"log": log})
}

// handleTrimProbe 边界探测（v1.16 引入 16.6）：POST /api/trim/probe {group_key, paths, durations, threshold}。
// 结果按组缓存，重复打开面板不重算。
func (s *Server) handleTrimProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var body struct {
		GroupKey  string    `json:"group_key"`
		Paths     []string  `json:"paths"`
		Durations []float64 `json:"durations"`
		Threshold float64   `json:"threshold"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if body.GroupKey == "" || len(body.Paths) < 2 {
		writeJSON(w, 400, map[string]any{"error": "缺少分组或成员不足"})
		return
	}
	if sec, st, ok := s.st.GetTrimProbe(r.Context(), body.GroupKey); ok && st == "ok" {
		writeJSON(w, 200, map[string]any{"boundary": sec, "status": st, "cached": true})
		return
	}
	if body.Threshold <= 0 {
		body.Threshold = s.cfg.Get().AudioThreshold
	}
	sec, st := s.e.TrimProbeBoundary(r.Context(), body.Paths, body.Durations, body.Threshold)
	_ = s.st.SaveTrimProbe(r.Context(), body.GroupKey, sec, st)
	writeJSON(w, 200, map[string]any{"boundary": sec, "status": st, "cached": false})
}
