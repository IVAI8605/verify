// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件是 v3.5.0 重命名方法预设的接口（设计文档 §6.3）。
// 预设只保存方法 JSON，不保存文件列表；执行时由前端把当前清单发来。

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"verify/internal/engine"
	"verify/internal/store"
)

// handleRenamePresets GET/POST /api/rename/presets。
func (s *Server) handleRenamePresets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.st.ListRenamePresets(r.Context())
		if err != nil {
			writeJSON(w, 500, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, 200, renamePresetsResponse{Presets: list})
	case http.MethodPost:
		var p store.RenamePreset
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
			writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
			return
		}
		if msg := validateRenamePreset(&p); msg != "" {
			writeJSON(w, 400, errorResponse{Error: msg})
			return
		}
		if err := s.st.SaveRenamePreset(r.Context(), &p); err != nil {
			writeJSON(w, 500, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, 200, renamePresetResponse{Preset: p})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
	}
}

// handleRenamePresetByID PUT/DELETE /api/rename/presets/{id}。
func (s *Server) handleRenamePresetByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/rename/presets/"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 400, errorResponse{Error: "缺少或非法的预设 id"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		var p store.RenamePreset
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
			writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
			return
		}
		p.ID = id
		if msg := validateRenamePreset(&p); msg != "" {
			writeJSON(w, 400, errorResponse{Error: msg})
			return
		}
		if err := s.st.SaveRenamePreset(r.Context(), &p); err != nil {
			writeJSON(w, 500, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, 200, renamePresetResponse{Preset: p})
	case http.MethodDelete:
		if err := s.st.DeleteRenamePreset(r.Context(), id); err != nil {
			writeJSON(w, 500, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, 200, okResponse{OK: true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
	}
}

// validateRenamePreset 校验名称与方法 JSON；方法数量上限 50。
func validateRenamePreset(p *store.RenamePreset) string {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return "预设名称不能为空"
	}
	if len([]rune(p.Name)) > 64 {
		return "预设名称最多 64 个字符"
	}
	var methods []engine.RenameMethod
	if err := json.Unmarshal([]byte(p.Methods), &methods); err != nil {
		return "方法列表不是合法的 JSON 数组"
	}
	if len(methods) == 0 {
		return "至少需要一个方法"
	}
	if len(methods) > 50 {
		return "方法数量最多 50 个"
	}
	return ""
}
