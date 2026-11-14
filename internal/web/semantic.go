// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"

	"verify/internal/store"
)

// handleSemanticGroups 语义核心键分组（v1.36 F7）：GET /api/groups/semantic?page=1。
// 仅辅助分组展示，不参与删除判定。
func (s *Server) handleSemanticGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	if !matchModeAllows(s.cfg.Get(), "semantic") {
		writeModeDisabled(w, "semantic")
		return
	}
	groups, err := s.e.SemanticGroups(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	groups = filterGroupsByRoots(groups, s.cfg.Get().Volumes)
	cfg := s.cfg.Get()
	groups = filterGroupsByQuery(groups, groupQuery(r, cfg))
	if groups == nil {
		groups = []store.Group{}
	}
	page := int(intParam(r, "page", 1, 1, 100000))
	pageSize := int(intParam(r, "page_size", 20, 1, 200))
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(groups) {
		start = len(groups)
	}
	if end > len(groups) {
		end = len(groups)
	}
	pageGroups := groups[start:end]
	pageGroups = s.attachTagsForPage(r, pageGroups)
	writeJSON(w, 200, map[string]any{"groups": pageGroups, "total": len(groups), "page": page, "page_size": pageSize})
}
