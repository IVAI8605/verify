// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"net/http"
	"strconv"

	"verify/internal/store"
)

// handleVideoTags GET /api/video_tags — 返回全部标签，按 sort_order 排序。
func (s *Server) handleVideoTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	tags, err := s.st.GetVideoTags(r.Context())
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	if tags == nil {
		tags = []store.VideoTag{}
	}
	writeJSON(w, 200, tagsResponse{Tags: tags})
}

// handleVideoTagsPost POST /api/video_tags — 创建或更新标签。
// body: { name, match_mode, join_group, sort_order, value_source, value_pattern }
func (s *Server) handleVideoTagsPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Name         string `json:"name"`
		MatchMode    string `json:"match_mode"`
		ValueSource  string `json:"value_source"`
		ValuePattern string `json:"value_pattern"`
		JoinGroup    *bool  `json:"join_group"`
		SortOrder    *int   `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: "请求体格式错误"})
		return
	}
	if body.Name == "" {
		writeJSON(w, 400, errorResponse{Error: "标签名不能为空"})
		return
	}
	if len(body.Name) > 32 {
		writeJSON(w, 400, errorResponse{Error: "标签名过长（最多 32 字符）"})
		return
	}
	jg := true
	if body.JoinGroup != nil {
		jg = *body.JoinGroup
	}
	so := 0
	if body.SortOrder != nil {
		so = *body.SortOrder
	}
	// 取值来源交给 store 校验：正则编译失败要在这里就报错，不能等重算时才失败
	id, err := s.st.UpsertVideoTag(r.Context(), body.Name, body.MatchMode, body.ValueSource, body.ValuePattern, jg, so)
	if err != nil {
		writeJSON(w, 400, errorResponse{Error: err.Error()})
		return
	}
	// 保存后立即按新规则重算，用户不用额外点一次"重算"才能看到效果
	count, cerr := s.st.ComputeFileTags(r.Context(), id)
	if cerr != nil {
		writeJSON(w, 200, tagSavedWarningResponse{ID: id, OK: true, Warning: "标签已保存，但重算失败：" + cerr.Error()})
		return
	}
	writeJSON(w, 200, tagSavedResponse{ID: id, OK: true, Count: count})
}

// handleVideoTagsRecompute POST /api/video_tags/recompute?id=N — 按当前规则重算标签值（v2.7.0）。
func (s *Server) handleVideoTagsRecompute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 400, errorResponse{Error: "缺少或非法的 id 参数"})
		return
	}
	count, err := s.st.ComputeFileTags(r.Context(), id)
	if err != nil {
		status := 500
		if err.Error() == "标签不存在" {
			status = 404
		}
		writeJSON(w, status, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, tagSavedCountResponse{OK: true, Count: count})
}

// handleTagGroups GET /api/groups/tag?tag_id=N — 按标签值聚组（v2.7.0）。
// 只用于辅助查看，不参与删除判定，因此不套匹配模式的开关限制。
func (s *Server) handleTagGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	tagID, err := strconv.ParseInt(r.URL.Query().Get("tag_id"), 10, 64)
	if err != nil || tagID <= 0 {
		writeJSON(w, 400, errorResponse{Error: "缺少或非法的 tag_id 参数"})
		return
	}
	q := groupQuery(r, s.cfg.Get())
	groups, total, err := s.st.TagGroups(r.Context(), tagID, q)
	if err != nil {
		status := 500
		if err.Error() == "标签不存在" {
			status = 404
		}
		writeJSON(w, status, errorResponse{Error: err.Error()})
		return
	}
	if groups == nil {
		groups = []store.Group{}
	}
	groups = s.attachTagsForPage(r, groups)
	writeJSON(w, 200, groupsResponse{Groups: groups, Total: int64(total), Page: q.Page, PageSize: q.PageSize})
}

// handleVideoTagsDelete DELETE /api/video_tags?id=N
func (s *Server) handleVideoTagsDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	idStr := r.URL.Query().Get("id")
	if idStr == "" {
		writeJSON(w, 400, errorResponse{Error: "缺少 id 参数"})
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, 400, errorResponse{Error: "id 参数非法"})
		return
	}
	if err := s.st.DeleteVideoTag(r.Context(), id); err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, okResponse{OK: true})
}
