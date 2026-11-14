// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

// 本文件是 v3.5.0 重命名规则引擎的存储层（设计文档 §6）：
// 预设 CRUD、批量改名批记录、审计批量写入与 files 路径批量同步。

import (
	"context"
	"time"
)

// RenamePreset 是一套可保存/加载的重命名方法列表（Methods 为 JSON 数组字符串）。
type RenamePreset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Methods     string `json:"methods"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// ListRenamePresets 返回全部预设（按名称排序）。
func (s *Store) ListRenamePresets(ctx context.Context) ([]RenamePreset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,description,methods,created_at,updated_at
		FROM rename_presets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []RenamePreset{}
	for rows.Next() {
		var p RenamePreset
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Methods, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetRenamePreset 读取单个预设；不存在返回 (nil, nil)。
func (s *Store) GetRenamePreset(ctx context.Context, id int64) (*RenamePreset, error) {
	var p RenamePreset
	err := s.db.QueryRowContext(ctx, `SELECT id,name,description,methods,created_at,updated_at
		FROM rename_presets WHERE id=?`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.Methods, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, nil
	}
	return &p, nil
}

// SaveRenamePreset 新建或更新预设（ID=0 新建）。
func (s *Store) SaveRenamePreset(ctx context.Context, p *RenamePreset) error {
	now := time.Now().Unix()
	if p.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO rename_presets(name,description,methods,created_at,updated_at)
			VALUES(?,?,?,?,?)`, p.Name, p.Description, p.Methods, now, now)
		if err != nil {
			return err
		}
		p.ID, _ = res.LastInsertId()
		p.CreatedAt, p.UpdatedAt = now, now
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE rename_presets SET name=?,description=?,methods=?,updated_at=? WHERE id=?`,
		p.Name, p.Description, p.Methods, now, p.ID)
	p.UpdatedAt = now
	return err
}

// DeleteRenamePreset 删除预设。
func (s *Store) DeleteRenamePreset(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM rename_presets WHERE id=?`, id)
	return err
}

// CreateRenameBatchRunning 在执行前先落一条 running 批次行（v3.10.10 第五轮 P1-2）。
//
// 规则引擎一次可改十几万个文件，原先等全部执行完才写批次：进程中途崩溃或断电时，
// 磁盘上已经改名的文件找不到 batch_id，前端「撤销本批」按钮也就没有入口。
// 先把批次行与 task_id 落库，明细留到结束时补齐，保证任何时刻都能顺着 task_id
// 回到 audit_log 回放这批改名。
func (s *Store) CreateRenameBatchRunning(ctx context.Context, presetID int64, filesCount int, methods string, taskID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO rename_batches(created_at,status,items_json,preset_id,files_count,methods,task_id)
		VALUES(?,?,?,?,?,?,?)`, time.Now().Unix(), "running", "[]", presetID, filesCount, methods, taskID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishRenameBatch 执行结束后补齐批次明细与最终状态（normal 结束或失败）。
// v3.10.12（P1-03）：意图行与批次状态在同一事务里收尾，正常批次不再需要恢复，
// 顺手删掉意图行，避免大批量改名让 rename_intents 无限增长。
func (s *Store) FinishRenameBatch(ctx context.Context, id int64, itemsJSON, status string) error {
	if status == "" {
		status = "done"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE rename_batches SET items_json=?, status=? WHERE id=?`, itemsJSON, status, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rename_intents WHERE batch_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// AddAuditBatch 批量写入审计记录（单事务），供 18 万级改名按小批次 flush。
func (s *Store) AddAuditBatch(ctx context.Context, entries []AuditEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO audit_log(task_id,operation,original_path,new_path,timestamp) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, e := range entries {
		at := e.Timestamp
		if at == 0 {
			at = now
		}
		if _, err := stmt.ExecContext(ctx, e.TaskID, e.Operation, e.OriginalPath, e.NewPath, at); err != nil {
			_ = stmt.Close()
			return err
		}
	}
	_ = stmt.Close()
	return tx.Commit()
}

// UpdatePathsBatch 批量把 files 表旧路径改成新路径（单事务）。
func (s *Store) UpdatePathsBatch(ctx context.Context, pairs [][2]string) error {
	if len(pairs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `UPDATE files SET path=? WHERE path=?`)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if _, err := stmt.ExecContext(ctx, p[1], p[0]); err != nil {
			_ = stmt.Close()
			return err
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		return err
	}
	// v3.8.0：路径变化会让搜索结果过期。
	s.dataVersion.Add(1)
	return nil
}
