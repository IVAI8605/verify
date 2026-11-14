// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件是 v3.2.0 预设清理计划的接口（设计文档 §3）。
// 计划保存只做参数校验；真正执行走 engine.RunCleanupPlan，只移入隔离区。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"verify/internal/config"
	"verify/internal/engine"
	"verify/internal/store"
)

// cleanupSummary 返回最近一次执行过清理计划的摘要，供概览页展示。
// 没有任何计划执行过时返回 nil，前端不显示这一行。
func (s *Server) cleanupSummary(ctx context.Context) map[string]any {
	plans, err := s.st.ListCleanupPlans(ctx)
	if err != nil {
		return nil
	}
	var latest store.CleanupPlan
	for _, p := range plans {
		if p.LastRun > latest.LastRun {
			latest = p
		}
	}
	if latest.LastRun == 0 {
		return nil
	}
	return map[string]any{
		"name":        latest.Name,
		"last_run":    latest.LastRun,
		"last_result": latest.LastResult,
		"last_error":  latest.LastError,
	}
}

// cleanupModes / cleanupStrategies 是允许的枚举值，避免把任意字符串存库。
var cleanupModes = map[string]bool{"all": true, "exact": true, "name": true, "video": true, "audio": true}
var cleanupStrategies = map[string]bool{"modify_newest": true, "largest_size": true, "best_codec": true}

// handleCleanupPlans 列出或保存计划。
// GET  /api/cleanup/plans
// POST /api/cleanup/plans   body: store.CleanupPlan（id=0 新建）
func (s *Server) handleCleanupPlans(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		plans, err := s.st.ListCleanupPlans(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"plans": plans})
	case http.MethodPost:
		var p store.CleanupPlan
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeJSON(w, 400, map[string]any{"error": friendlyDecodeError(err)})
			return
		}
		if msg := validateCleanupPlan(&p); msg != "" {
			writeJSON(w, 400, map[string]any{"error": msg})
			return
		}
		if err := s.st.SaveCleanupPlan(r.Context(), &p); err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"plan": p})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
	}
}

// handleCleanupDelete 删除计划。POST /api/cleanup/plans/delete {"id":N}
func (s *Server) handleCleanupDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID <= 0 {
		writeJSON(w, 400, map[string]any{"error": "id 无效"})
		return
	}
	if err := s.st.DeleteCleanupPlan(r.Context(), body.ID); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleCleanupRun 立即执行一条计划（手动触发，不受 enabled 限制）。
// POST /api/cleanup/run {"id":N}
func (s *Server) handleCleanupRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID <= 0 {
		writeJSON(w, 400, map[string]any{"error": "id 无效"})
		return
	}
	res, err := s.e.RunCleanupPlan(r.Context(), body.ID)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, res)
}

// validateCleanupPlan 校验并规范化计划字段，返回中文错误（空串表示通过）。
func validateCleanupPlan(p *store.CleanupPlan) string {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return "计划名称不能为空"
	}
	if len([]rune(p.Name)) > 64 {
		return "计划名称最多 64 个字符"
	}
	if err := engine.ValidateCleanupSchedule(p.Schedule); err != nil {
		return err.Error()
	}
	if p.MatchMode == "" {
		p.MatchMode = "all"
	}
	if !cleanupModes[p.MatchMode] {
		return "匹配模式只能是 all / exact / name / video / audio"
	}
	// v3.9.0：计划可携带搜索规则；格式非法直接拦下，避免执行时才发现。
	if strings.TrimSpace(p.RulesJSON) != "" {
		var r config.SearchRules
		if err := json.Unmarshal([]byte(p.RulesJSON), &r); err != nil {
			return "搜索规则格式错误"
		}
		r = *config.NormalizeSearchRules(&r)
		if err := config.ValidateSearchRules(&r); err != nil {
			return err.Error()
		}
		b, _ := json.Marshal(r)
		p.RulesJSON = string(b)
	}
	if p.RetainStrategy == "" {
		p.RetainStrategy = "modify_newest"
	}
	if !cleanupStrategies[p.RetainStrategy] {
		return "保留策略只能是 modify_newest / largest_size / best_codec"
	}
	if p.MinSize < 0 {
		p.MinSize = 0
	}
	if p.MinCount < 0 {
		p.MinCount = 0
	}
	if p.MinCount > 1000 {
		return "组内最少文件数最多 1000"
	}
	if p.MaxKeepGB < 0 {
		p.MaxKeepGB = 0
	}
	// v3.17.3（F-06）：上限防止 int64 乘法溢出（GB→字节乘 2^30），
	// 溢出成负数后容量上限会被当成「不限」，计划一次移走超出预期的文件。
	if p.MaxKeepGB > 1<<20 {
		return "单次容量上限最多 1048576 GB"
	}
	p.Extensions = strings.TrimSpace(strings.ReplaceAll(p.Extensions, " ", ""))
	return ""
}
