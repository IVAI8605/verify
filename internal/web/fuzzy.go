// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"

	"verify/internal/store"
)

// handleFuzzyGroups 模糊文件名分组（v1.34 F3）：GET /api/groups/fuzzy?near=1&page=1。
// near=0 时仅同键精确匹配；仅分组不删。
func (s *Server) handleFuzzyGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	near := r.URL.Query().Get("near") != "0"
	if !matchModeAllows(s.cfg.Get(), "fuzzy") {
		writeModeDisabled(w, "fuzzy")
		return
	}
	groups, err := s.e.FuzzyGroups(r.Context(), near)
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	groups = filterGroupsByRoots(groups, s.cfg.Get().Volumes)
	cfg := s.cfg.Get()
	groups = filterGroupsByQuery(groups, groupQuery(r, cfg))
	if groups == nil {
		groups = []store.Group{}
	}
	groups = sortGroupsQ(groups, r)
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
	writeJSON(w, 200, groupsResponse{Groups: pageGroups, Total: int64(len(groups)), Page: page, PageSize: pageSize})
}
