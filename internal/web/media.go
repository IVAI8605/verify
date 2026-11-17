// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"

	"verify/internal/store"
)

// handleAudioGroups 返回纯音频重复分组（阈值取设置页 audio_threshold）。
func (s *Server) handleAudioGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	cfg := s.cfg.Get()
	if !matchModeAllows(cfg, "audio") {
		writeModeDisabled(w, "audio")
		return
	}
	thr := cfg.EffectiveAudioThreshold()
	groups, _, err := s.st.AudioGroups(r.Context(), thr)
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	groups = filterGroupsByRoots(groups, cfg.Volumes)
	groups = filterGroupsByQuery(groups, groupQuery(r, cfg))
	if groups == nil {
		groups = []store.Group{}
	}
	groups = sortGroupsQ(groups, r)
	groups = s.attachTagsForPage(r, groups)
	writeJSON(w, 200, groupsMediaResponse{Groups: groups, Total: int64(len(groups)), Threshold: thr})
}

// handleFolderGroups 返回文件夹整体重复分组（单层目录签名）。
func (s *Server) handleFolderGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	// v2.0.0：关闭「计算重复文件夹」后直接返回，不查库（该页每次要扫两遍全表）。
	cfg := s.cfg.Get()
	if !matchModeAllows(cfg, "folder") {
		writeModeDisabled(w, "folder")
		return
	}
	if !cfg.ComputeDupFolders {
		writeJSON(w, 200, groupsDisabledResponse{Groups: []store.Group{}, Total: 0, Disabled: true})
		return
	}
	groups, total, err := s.st.FolderGroups(r.Context())
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	if groups == nil {
		groups = []store.Group{}
	}
	groups = sortGroupsQ(groups, r)
	groups = s.attachTagsForPage(r, groups)
	writeJSON(w, 200, groupsSimpleResponse{Groups: groups, Total: int64(total)})
}
