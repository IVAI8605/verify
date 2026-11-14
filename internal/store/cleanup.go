// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

// 本文件是 v3.2.0 的预设清理计划（设计文档 §3）。
// 计划只描述「什么时候、按什么条件、保留哪一个」，执行时只会把多余文件
// 移入隔离区，绝不永久删除；真正删除仍由用户在隔离区手动确认。

import (
	"context"
	"database/sql"
	"errors"
)

// CleanupPlan 是一条定时清理计划。
type CleanupPlan struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	Schedule       string `json:"schedule"`        // daily HH:MM / weekly fri HH:MM / interval 秒
	MatchMode      string `json:"match_mode"`      // all / exact / name / video / audio
	MinSize        int64  `json:"min_size"`        // 0 = 不限
	MinCount       int    `json:"min_count"`       // 组内最少文件数，0 = 用默认 2
	Extensions     string `json:"extensions"`      // 逗号分隔，空 = 不限
	RetainStrategy string `json:"retain_strategy"` // modify_newest / largest_size / best_codec
	MaxKeepGB      int64  `json:"max_keep_gb"`     // 单次最多移入隔离区的容量上限（GB），0 = 不限
	// RulesJSON v3.9.0：该计划使用的搜索规则（config.SearchRules 的 JSON）。
	// 空表示沿用旧的 match_mode。
	RulesJSON  string `json:"rules_json,omitempty"`
	LastRun    int64  `json:"last_run"`
	LastResult string `json:"last_result"`
	LastError  string `json:"last_error"`
}

// ListCleanupPlans 按 id 升序返回全部计划。
func (s *Store) ListCleanupPlans(ctx context.Context) ([]CleanupPlan, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,enabled,schedule,match_mode,min_size,min_count,
		COALESCE(extensions,''),COALESCE(retain_strategy,''),max_keep_gb,last_run,
		COALESCE(last_result,''),COALESCE(last_error,''),COALESCE(rules_json,'') FROM cleanup_plans ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []CleanupPlan{}
	for rows.Next() {
		var p CleanupPlan
		var enabled int
		if err := rows.Scan(&p.ID, &p.Name, &enabled, &p.Schedule, &p.MatchMode, &p.MinSize, &p.MinCount,
			&p.Extensions, &p.RetainStrategy, &p.MaxKeepGB, &p.LastRun, &p.LastResult, &p.LastError, &p.RulesJSON); err != nil {
			return nil, err
		}
		p.Enabled = enabled != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetCleanupPlan 读取单条计划；不存在返回 (nil, nil)。
func (s *Store) GetCleanupPlan(ctx context.Context, id int64) (*CleanupPlan, error) {
	var p CleanupPlan
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT id,name,enabled,schedule,match_mode,min_size,min_count,
		COALESCE(extensions,''),COALESCE(retain_strategy,''),max_keep_gb,last_run,
		COALESCE(last_result,''),COALESCE(last_error,''),COALESCE(rules_json,'') FROM cleanup_plans WHERE id=?`, id).
		Scan(&p.ID, &p.Name, &enabled, &p.Schedule, &p.MatchMode, &p.MinSize, &p.MinCount,
			&p.Extensions, &p.RetainStrategy, &p.MaxKeepGB, &p.LastRun, &p.LastResult, &p.LastError, &p.RulesJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	p.Enabled = enabled != 0
	return &p, nil
}

// SaveCleanupPlan 新建或更新计划；ID 为 0 时插入并回填新 ID，否则按 ID 更新。
func (s *Store) SaveCleanupPlan(ctx context.Context, p *CleanupPlan) error {
	enabled := 0
	if p.Enabled {
		enabled = 1
	}
	if p.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO cleanup_plans(name,enabled,schedule,match_mode,min_size,
			min_count,extensions,retain_strategy,max_keep_gb,rules_json) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			p.Name, enabled, p.Schedule, p.MatchMode, p.MinSize, p.MinCount, p.Extensions, p.RetainStrategy, p.MaxKeepGB, p.RulesJSON)
		if err != nil {
			return err
		}
		p.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE cleanup_plans SET name=?,enabled=?,schedule=?,match_mode=?,min_size=?,
		min_count=?,extensions=?,retain_strategy=?,max_keep_gb=?,rules_json=? WHERE id=?`,
		p.Name, enabled, p.Schedule, p.MatchMode, p.MinSize, p.MinCount, p.Extensions, p.RetainStrategy, p.MaxKeepGB, p.RulesJSON, p.ID)
	return err
}

// DeleteCleanupPlan 删除计划。
func (s *Store) DeleteCleanupPlan(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cleanup_plans WHERE id=?`, id)
	return err
}

// UpdateCleanupPlanResult 写入一次执行结果（成功与失败都记录）。
func (s *Store) UpdateCleanupPlanResult(ctx context.Context, id, lastRun int64, result, lastErr string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cleanup_plans SET last_run=?,last_result=?,last_error=? WHERE id=?`,
		lastRun, result, lastErr, id)
	return err
}
