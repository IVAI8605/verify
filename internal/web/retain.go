// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"net/http"

	"verify/internal/engine"
	"verify/internal/store"
)

// F4 智能保留策略（v1.40 引入）：无状态打分 API——前端把当前页分组发来，
// 后端结合 health 损坏结论打分返回建议；应用走前端标记通道，永不自动删除。

type retainScoreBody struct {
	Groups []struct {
		Key   string              `json:"key"`
		Files []engine.RetainFile `json:"files"`
	} `json:"groups"`
}

// handleRetainScore POST /api/retain/score。
func (s *Server) handleRetainScore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body retainScoreBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	// v3.9.8 修 F-03：空组文案「分组数量超限」不准确——区分"空数组"和"超限"
	if len(body.Groups) == 0 {
		writeJSON(w, 400, errorResponse{Error: "未选择分组"})
		return
	}
	if len(body.Groups) > 200 {
		writeJSON(w, 400, errorResponse{Error: "分组数量超限（最多 200 组）"})
		return
	}
	broken, err := s.e.BrokenPathsSet(r.Context())
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	type out struct {
		Key        string                  `json:"key"`
		Suggestion engine.RetainSuggestion `json:"suggestion"`
	}
	results := make([]out, 0, len(body.Groups))
	for _, g := range body.Groups {
		files := make([]engine.RetainFile, 0, len(g.Files))
		for _, f := range g.Files {
			f.Broken = broken[f.Path]
			files = append(files, f)
		}
		if len(files) < 2 {
			continue
		}
		results = append(results, out{Key: g.Key, Suggestion: engine.RetainSuggest(files)})
	}
	writeJSON(w, 200, retainSuggestionsResponse{Suggestions: results})
}

// handleRetainPref 偏好记录（F4 二期）：POST /api/retain/pref {entries}。
func (s *Server) handleRetainPref(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Entries []store.UserPrefEntry `json:"entries"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	if len(body.Entries) > 500 {
		writeJSON(w, 400, errorResponse{Error: "条数超限"})
		return
	}
	if err := s.st.RecordUserPref(r.Context(), body.Entries); err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, okResponse{OK: true})
}

// handleRetainPrefStats 学习统计：GET /api/retain/pref/stats。
func (s *Server) handleRetainPrefStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	st, err := s.st.UserPrefStats(r.Context())
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, toUserPrefStatsResp(st))
}

// handleRetainPrefClear 重置学习数据：POST /api/retain/pref/clear。
func (s *Server) handleRetainPrefClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	n, err := s.st.ClearUserPref(r.Context())
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	// v3.3.0：清空后立即按当前开关重算，权重复位到默认值。
	_, _ = s.e.RefreshRetainProfile(r.Context(), s.e.RetainLearnEnabled(), true)
	writeJSON(w, 200, retainClearedResponse{Cleared: n})
}

// handleRetainPrefProfile 返回偏好学习档案：GET /api/retain/pref/profile。
func (s *Server) handleRetainPrefProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	writeJSON(w, 200, toRetainProfileResp(s.e.RetainProfileNow(r.Context())))
}

// handleRetainPrefApply 启用/停用偏好反哺：POST /api/retain/pref/apply {"enable":true}。
func (s *Server) handleRetainPrefApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Enable bool `json:"enable"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	profile, err := s.e.RefreshRetainProfile(r.Context(), body.Enable, true)
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, toRetainProfileResp(profile))
}
