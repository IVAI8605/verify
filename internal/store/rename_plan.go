// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

// 本文件是 v3.21.0 两阶段改名的计划表持久化层。
//
// 为什么单独建表：40 万条改名明细如果塞进 rename_batches.items_json，
// 会变成 60–100MB 的单行 TEXT，写入与撤销都要整包 marshal/unmarshal。
// 逐行存后，执行按 2000 条一片取，中断可续跑，WAL 也不会被单事务顶满。

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RenamePlanItemRow 是计划表里的一行。
type RenamePlanItemRow struct {
	Idx     int
	OldPath string
	OldName string
	NewName string
	// Size / Mtime 是计划生成时的文件身份快照（v3.25.0 P1-2）。
	// 旧库补列后为 0，执行时跳过复核。
	Size    int64
	Mtime   int64
	State   string // pending / done / failed
	Error   string
	BatchID int64
}

// RenamePlanResult 是一片执行后的单行结果。
type RenamePlanResult struct {
	Idx     int
	State   string
	Error   string
	BatchID int64
}

// renamePlanKeepDays 是计划表的保留天数。清理发生在新建计划时，
// 不影响正在执行或需要续跑的计划。
const renamePlanKeepDays = 7

// CreateRenamePlan 建一份计划头，返回 plan_id，并顺手清理过旧计划。
func (s *Store) CreateRenamePlan(ctx context.Context, total int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -renamePlanKeepDays).Unix()
	s.purgeStaleRenamePlans(ctx, cutoff)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO rename_plans(created_at, total, status) VALUES(?,?,'planned')`,
		time.Now().Unix(), total)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// purgeStaleRenamePlans 删除早于 cutoff 的旧计划。
//
// v3.25.1：只清理「已经没有 pending 明细」的计划。旧实现按 created_at 一刀切，
// 用户暂停超过 7 天的计划会被下一次新建计划连头带明细删掉，续跑接口随即报
// 「计划不存在」，与上面的保留语义（不影响需要续跑的计划）相反。
// 先按条件选出候选 ID 再逐条删除，避免「先删明细」破坏 NOT EXISTS 判定。
func (s *Store) purgeStaleRenamePlans(ctx context.Context, cutoff int64) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id FROM rename_plans p
		  WHERE p.created_at < ?
		    AND NOT EXISTS (SELECT 1 FROM rename_plan_items i
		                     WHERE i.plan_id = p.id AND i.state = 'pending')`, cutoff)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	_ = rows.Close()
	if len(ids) == 0 {
		return
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range ids {
		_, _ = tx.ExecContext(ctx, `DELETE FROM rename_plan_items WHERE plan_id=?`, id)
		_, _ = tx.ExecContext(ctx, `DELETE FROM rename_plans WHERE id=?`, id)
	}
	_ = tx.Commit()
}

// AddRenamePlanItems 分批写入计划明细；每 1000 条一个事务。
func (s *Store) AddRenamePlanItems(ctx context.Context, planID int64, startIdx int, items []RenamePlanItemRow) error {
	if planID <= 0 || len(items) == 0 {
		return nil
	}
	const chunk = 1000
	for start := 0; start < len(items); start += chunk {
		end := start + chunk
		if end > len(items) {
			end = len(items)
		}
		if err := s.addRenamePlanChunk(ctx, planID, startIdx+start, items[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) addRenamePlanChunk(ctx context.Context, planID int64, startIdx int, items []RenamePlanItemRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR REPLACE INTO rename_plan_items(plan_id, idx, old_path, old_name, new_name, size, mtime, state, error)
		 VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for i, it := range items {
		state := it.State
		if state == "" {
			state = "pending"
		}
		if _, err := stmt.ExecContext(ctx, planID, startIdx+i, it.OldPath, it.OldName, it.NewName, it.Size, it.Mtime, state, it.Error); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NextRenamePlanItems 取下一片待执行明细（按 idx 顺序）。
func (s *Store) NextRenamePlanItems(ctx context.Context, planID int64, limit int) ([]RenamePlanItemRow, error) {
	if limit <= 0 {
		limit = 2000
	}
	if limit > 5000 {
		limit = 5000
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT idx, old_path, old_name, new_name, size, mtime FROM rename_plan_items
		 WHERE plan_id=? AND state='pending' ORDER BY idx LIMIT ?`, planID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]RenamePlanItemRow, 0, limit)
	for rows.Next() {
		var it RenamePlanItemRow
		if err := rows.Scan(&it.Idx, &it.OldPath, &it.OldName, &it.NewName, &it.Size, &it.Mtime); err != nil {
			return nil, err
		}
		it.State = "pending"
		out = append(out, it)
	}
	return out, rows.Err()
}

// MarkRenamePlanResults 回填一片执行结果；每 1000 条一个事务。
func (s *Store) MarkRenamePlanResults(ctx context.Context, planID int64, results []RenamePlanResult) error {
	if planID <= 0 || len(results) == 0 {
		return nil
	}
	const chunk = 1000
	for start := 0; start < len(results); start += chunk {
		end := start + chunk
		if end > len(results) {
			end = len(results)
		}
		if err := s.markRenamePlanChunk(ctx, planID, results[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) markRenamePlanChunk(ctx context.Context, planID int64, results []RenamePlanResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`UPDATE rename_plan_items SET state=?, error=?, real_batch_id=? WHERE plan_id=? AND idx=?`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range results {
		state := r.State
		if state == "" {
			state = "done"
		}
		if _, err := stmt.ExecContext(ctx, state, r.Error, r.BatchID, planID, r.Idx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RenamePlanStatus 返回计划进度；total 取自计划头，避免明细被清理后归零。
func (s *Store) RenamePlanStatus(ctx context.Context, planID int64) (total, done, failed, pending int, status string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT total, status FROM rename_plans WHERE id=?`, planID).Scan(&total, &status)
	if err != nil {
		return
	}
	err = s.db.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(CASE WHEN state='done' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN state='failed' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN state='pending' THEN 1 ELSE 0 END),0)
		FROM rename_plan_items WHERE plan_id=?`, planID).Scan(&done, &failed, &pending)
	return
}

// SetRenamePlanStatus 更新计划头状态。
func (s *Store) SetRenamePlanStatus(ctx context.Context, planID int64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rename_plans SET status=? WHERE id=?`, status, planID)
	return err
}

// DiscardRenamePlan 删除刚建好但明细写入失败的计划（v3.25.0 P2-6）。
// 只动这一份计划的明细与头，避免留下 total>0、明细为 0 的孤儿计划。
func (s *Store) DiscardRenamePlan(ctx context.Context, planID int64) error {
	if planID <= 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM rename_plan_items WHERE plan_id=?`, planID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rename_plans WHERE id=?`, planID); err != nil {
		return err
	}
	return tx.Commit()
}

// RenamePlanExists 判断计划是否存在（执行分片前做一次快速校验）。
func (s *Store) RenamePlanExists(ctx context.Context, planID int64) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM rename_plans WHERE id=?`, planID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
