// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"verify/internal/store"
	"verify/internal/taskguard"
)

// handleBig 大文件 Top N（设计文档 10.1）：GET /api/big?limit=50&order=big|small。
func (s *Server) handleBig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	smallest := r.URL.Query().Get("order") == "small"
	// v1.39：crop 筛选（landscape/portrait/square），空不过滤
	files, err := s.st.CropFilterFiles(r.Context(), r.URL.Query().Get("crop"), limit, smallest)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	if files == nil {
		files = []store.FileRow{}
	}
	writeJSON(w, 200, map[string]any{"files": files, "limit": limit, "order": map[bool]string{true: "small", false: "big"}[smallest]})
}

// handleVideoCropDetect 批量黑边检测（v1.39 F6）：POST /api/video/cropdetect {limit}。
// 每次处理一批（默认 50），只取未检测的文件；v3.19.0 起同时返回 remaining，
// 前端据此画进度并判断何时收工，不必靠反复点击试探。
func (s *Server) handleVideoCropDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var body struct {
		Limit int `json:"limit"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	labeled, remaining, err := s.e.CropLabelBatch(r.Context(), body.Limit)
	if err != nil {
		// v3.18.0：被任务闸门拒绝属于「稍后再试」的冲突，不是服务端故障。
		var ce *taskguard.ConflictError
		if errors.As(err, &ce) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"labeled": labeled, "remaining": remaining})
}
