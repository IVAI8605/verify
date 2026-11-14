// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"

	"verify/internal/store"
)

// handlePhotoGroups 返回相似图片分组（阈值取设置页 img_hamming）。
// 一次性返回全部分组：组数远小于图片数，量级可控。
func (s *Server) handlePhotoGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	cfg := s.cfg.Get()
	if !matchModeAllows(cfg, "photo") {
		writeModeDisabled(w, "photo")
		return
	}
	threshold := int64(cfg.ImgHamming)
	if v := intParam(r, "threshold", threshold, 0, 64); v != threshold {
		threshold = v
	}
	groups, total, err := s.st.PhotoGroups(r.Context(), int(threshold))
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	if groups == nil {
		groups = []store.Group{}
	}
	groups = sortGroupsQ(groups, r)
	groups = s.attachTagsForPage(r, groups)
	writeJSON(w, 200, map[string]any{"groups": groups, "total": total, "threshold": threshold})
}
