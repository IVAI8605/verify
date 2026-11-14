// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"strings"
	"time"
)

// RenameBatchItem 是一次清单式改名中单个文件的快照（v2.10.0）。
type RenameBatchItem struct {
	Path    string `json:"path"`
	Orig    string `json:"orig"`
	Target  string `json:"target"`
	NewPath string `json:"new_path,omitempty"`
	TaskID  int64  `json:"task_id,omitempty"`
	Error   string `json:"error,omitempty"`
}

// CreateRenameBatch 保存一批改名快照，返回 batch_id。
func (s *Store) CreateRenameBatch(ctx context.Context, itemsJSON string, status string) (int64, error) {
	if status == "" {
		status = "done"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO rename_batches(created_at, status, items_json) VALUES(?,?,?)`,
		time.Now().Unix(), status, itemsJSON)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetRenameBatchMeta 读取批次状态、明细与批次任务号（v3.10.10 第五轮 P1-2）。
//
// 崩溃留下的 running 批次明细可能仍是空数组，但 task_id 在开始时就已落库；
// 撤销入口必须能靠它找到 audit_log 里的整批改名，不能只看 items_json。
func (s *Store) GetRenameBatchMeta(ctx context.Context, id int64) (status, itemsJSON string, taskID int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT status, items_json, task_id FROM rename_batches WHERE id=?`, id).
		Scan(&status, &itemsJSON, &taskID)
	return status, itemsJSON, taskID, err
}

// RunningRenameBatch 是一个没有正常收尾的改名批次（v3.10.10 第五轮 P1-2）。
//
// 崩溃时 items_json 可能都还没补齐，所以 Done 取 audit_log 里该任务已记账的改名条数，
// 不依赖批次明细；PresetName 用于界面直接展示「哪个预设的半批」。
type RunningRenameBatch struct {
	ID         int64  `json:"batch_id"`
	PresetID   int64  `json:"preset_id"`
	PresetName string `json:"preset_name"`
	FilesCount int    `json:"files_count"`
	Done       int    `json:"done"`
	CreatedAt  int64  `json:"created_at"`
	TaskID     int64  `json:"task_id"`
	// v3.10.13：界面要区分「崩溃遗留」与「撤销没干净」两种残项，并显示还差几行，
	// 否则用户刷新页面就再也看不到上次部分撤销的失败项。
	Status string `json:"status"`
	Failed int    `json:"failed"`
	// v3.11.0：新前端按 started_at / total 读取，已发布的 3.10.13 前端仍读
	// created_at / files_count。过渡期两套名字同时发，值完全一致，避免跨版本混用时解析不到。
	StartedAt int64 `json:"started_at"`
	Total     int   `json:"total"`
}

// unfinishedRenameStatuses 是「不算正常收尾」的批次状态。
// running 是崩溃遗留；partial 是分片执行被取消、还有未执行项（v3.22.0）；
// undone_partial / undone_failed 是撤销没撤干净；
// repaired_partial 是启动恢复后仍有新旧路径都找不到、需要人工看一眼的行。
var unfinishedRenameStatuses = []string{"running", "partial", "undone_partial", "undone_failed", "repaired_partial"}

// ListRunningRenameBatches 只返回崩溃遗留的 running 批次（保持旧调用方语义）。
func (s *Store) ListRunningRenameBatches(ctx context.Context, limit int) ([]RunningRenameBatch, error) {
	return s.ListUnfinishedRenameBatches(ctx, limit, false)
}

// ListUnfinishedRenameBatches 返回没有正常收尾的改名批次（v3.10.10 第五轮 P1-2 / v3.10.13）。
//
// includeResidual=false 时只查 running，供启动告警与「是否需要撤销」判断使用；
// =true 时额外带上撤销未收敛与恢复后有歧义的批次，供界面残项提示使用。
func (s *Store) ListUnfinishedRenameBatches(ctx context.Context, limit int, includeResidual bool) ([]RunningRenameBatch, error) {
	if limit <= 0 {
		limit = 5
	}
	where := `b.status='running'`
	if includeResidual {
		where = `b.status IN ('` + strings.Join(unfinishedRenameStatuses, "','") + `')`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT b.id, b.preset_id, COALESCE(p.name,''), b.files_count, b.created_at, b.task_id,
			b.status, b.failed_count,
			(SELECT COUNT(*) FROM audit_log a WHERE a.task_id=b.task_id AND a.operation='rename')
		FROM rename_batches b LEFT JOIN rename_presets p ON p.id=b.preset_id
		WHERE `+where+` ORDER BY b.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RunningRenameBatch
	for rows.Next() {
		var b RunningRenameBatch
		if err := rows.Scan(&b.ID, &b.PresetID, &b.PresetName, &b.FilesCount, &b.CreatedAt, &b.TaskID,
			&b.Status, &b.Failed, &b.Done); err != nil {
			return nil, err
		}
		b.StartedAt = b.CreatedAt
		b.Total = b.FilesCount
		out = append(out, b)
	}
	return out, rows.Err()
}

// FinishRenameBatchState 落批次的终态并清掉意图行（撤销 / 崩溃恢复共用）。
// v3.10.12（P1-03）：批次一旦离开 running，意图行就没有恢复用途，随状态一起清掉。
// v3.10.13：同时写入 failed_count，供未完成批次提示直接显示残项条数。
func (s *Store) FinishRenameBatchState(ctx context.Context, id int64, status string, failed int) error {
	if failed < 0 {
		failed = 0
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE rename_batches SET status=?, failed_count=? WHERE id=?`, status, failed, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rename_intents WHERE batch_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// RecentRenameBatch 是界面「最近批次」用的轻量行（v3.22.0）。
type RecentRenameBatch struct {
	ID         int64  `json:"batch_id"`
	CreatedAt  int64  `json:"created_at"`
	Status     string `json:"status"`
	FilesCount int    `json:"files_count"`
	Failed     int    `json:"failed"`
}

// RecentRenameBatches 返回最近的改名批次。
// 关页面后前端靠它重新拿到撤销入口，不必依赖内存里的 batch_id。
func (s *Store) RecentRenameBatches(ctx context.Context, limit int) ([]RecentRenameBatch, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at, status, files_count, failed_count FROM rename_batches ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]RecentRenameBatch, 0, limit)
	for rows.Next() {
		var b RecentRenameBatch
		if err := rows.Scan(&b.ID, &b.CreatedAt, &b.Status, &b.FilesCount, &b.Failed); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
