// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

// 本文件是「改名意图」的存储层（v3.10.12 P1-03）。
//
// 规则引擎改名时，磁盘动作与审计/索引写入不可能是一个原子操作。原来的做法是
// 每 100 条 flush 一次，崩溃时最后不到 100 条「磁盘已改名、库里没记录」成为孤儿。
// 现在执行前先把整批 old→new 写成 pending 意图行，成功后标 done：
// 崩溃后启动恢复用 pending 行去对磁盘，改名成功的补写审计与索引，没改的标 failed。

import (
	"context"
	"strings"
	"time"
)

// 意图状态。pending 表示「已记下但还不能确定磁盘改没改」，
// ambiguous 表示新旧路径同时存在或都不存在，必须人工确认，恢复过程不碰这类行。
const (
	RenameIntentPending   = "pending"
	RenameIntentDone      = "done"
	RenameIntentFailed    = "failed"
	RenameIntentAmbiguous = "ambiguous"
)

// RenameIntent 是一条改名意图。
type RenameIntent struct {
	ID           int64  `json:"id"`
	BatchID      int64  `json:"batch_id"`
	TaskID       int64  `json:"task_id"`
	OriginalPath string `json:"original_path"`
	NewPath      string `json:"new_path"`
	Status       string `json:"status"`
}

// AddRenameIntents 在执行任何 rename 之前写入整批意图（v3.10.12 P1-03）。
// 分块提交，避免 18 万行攒成一个大事务把 WAL 顶满。
func (s *Store) AddRenameIntents(ctx context.Context, batchID, taskID int64, pairs [][2]string) error {
	if len(pairs) == 0 {
		return nil
	}
	const chunk = 1000
	now := time.Now().Unix()
	for start := 0; start < len(pairs); start += chunk {
		end := start + chunk
		if end > len(pairs) {
			end = len(pairs)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO rename_intents(batch_id,task_id,original_path,new_path,status,created_at) VALUES(?,?,?,?,?,?)`)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		for _, p := range pairs[start:end] {
			if _, err := stmt.ExecContext(ctx, batchID, taskID, p[0], p[1], RenameIntentPending, now); err != nil {
				_ = stmt.Close()
				_ = tx.Rollback()
				return err
			}
		}
		_ = stmt.Close()
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ListPendingRenameIntentsAfter 按 id 升序取一批 pending 意图（keyset 分页）。
func (s *Store) ListPendingRenameIntentsAfter(ctx context.Context, batchID, afterID int64, limit int) ([]RenameIntent, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,batch_id,task_id,original_path,new_path,status FROM rename_intents
		 WHERE batch_id=? AND status=? AND id>? ORDER BY id LIMIT ?`,
		batchID, RenameIntentPending, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]RenameIntent, 0, limit)
	for rows.Next() {
		var it RenameIntent
		if err := rows.Scan(&it.ID, &it.BatchID, &it.TaskID, &it.OriginalPath, &it.NewPath, &it.Status); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// MarkRenameIntentsDone 把已成功改名并完成落库的意图标为 done。
// 按 (batch_id, original_path) 定位：同一批的旧路径在计划阶段已去重。
func (s *Store) MarkRenameIntentsDone(ctx context.Context, batchID int64, pairs [][2]string) error {
	if len(pairs) == 0 {
		return nil
	}
	const chunk = 400
	for start := 0; start < len(pairs); start += chunk {
		end := start + chunk
		if end > len(pairs) {
			end = len(pairs)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx,
			`UPDATE rename_intents SET status=? WHERE batch_id=? AND original_path=? AND status=?`)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		for _, p := range pairs[start:end] {
			if _, err := stmt.ExecContext(ctx, RenameIntentDone, batchID, p[0], RenameIntentPending); err != nil {
				_ = stmt.Close()
				_ = tx.Rollback()
				return err
			}
		}
		_ = stmt.Close()
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// MarkRenameIntentStatus 按 id 批量改状态，供崩溃恢复逐条落结论。
func (s *Store) MarkRenameIntentStatus(ctx context.Context, ids []int64, status string) error {
	if len(ids) == 0 {
		return nil
	}
	// SQLite 变量上限默认 32766；400 一批留足余量，也避免单事务过大。
	const chunk = 400
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		part := ids[start:end]
		args := make([]any, 0, len(part)+1)
		args = append(args, status)
		for _, id := range part {
			args = append(args, id)
		}
		// 占位符只由切片长度生成，值全部走 args 绑定，不存在注入面；G202 属误报。
		ph := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		// #nosec G202 -- 拼接内容仅为 len(part) 个问号，无外部输入
		if _, err := s.db.ExecContext(ctx, `UPDATE rename_intents SET status=? WHERE id IN (`+ph+`)`, args...); err != nil {
			return err
		}
	}
	return nil
}
