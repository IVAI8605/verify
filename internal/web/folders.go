// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"
	"strings"
)

// handleFolderStats 返回扫描目录下已索引文件数与总大小，供概览页悬浮卡使用（v2.2.0）。
func (s *Server) handleFolderStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	allowed := p != "" && s.e.ValidDirPath(p)
	if !allowed && p != "" {
		// 概览页悬浮的是存储卷前缀（如 /vol6），它可能只是扫描根的上层目录。
		for _, root := range s.e.Volumes() {
			if volumePrefix(root) == p {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "目录不在扫描范围内"})
		return
	}
	files, bytes, err := s.st.PathStats(r.Context(), p)
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, folderStatsResponse{Path: p, Files: files, Bytes: bytes})
}
