// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

// 本文件是 v3.2.0 的哈希批量写入（设计文档 §10.1）。
// 扫描阶段此前每算完一个文件就单独 UPDATE 一次，18 万文件约 18 万次事务；
// 批量接口把 300 条合并成一个事务，语义与单条 SetQuick/SetHash 完全一致，
// 单条接口继续保留给监控与单文件修复使用。

import (
	"context"
)

// HashUpdate 是一条待写入的哈希值；Value="ERR" 表示计算失败（与原语义一致）。
//
// v3.10.4：Size/Mtime 是算哈希时读到的大小与修改时间，写入时一起作为
// 乐观条件。文件在「选中」到「落库」之间被改动时，UpsertBatch 已把
// quick/hash 置 NULL，这里若不带条件就会把旧内容的哈希写到新大小上，
// 之后候选查询再也不会重算，可能据此误删。
type HashUpdate struct {
	ID    int64
	Size  int64
	Mtime int64
	// Ctime 参与乐观条件（v3.10.10，P2-5）：内容替换后 mtime 可被回拨，ctime 不能。
	// 0 表示调用方未采集，此条只有 size+mtime 生效。
	Ctime int64
	Value string
}

// SetQuickBatch 批量写入预哈希值。
func (s *Store) SetQuickBatch(ctx context.Context, updates []HashUpdate) error {
	return s.setHashBatch(ctx, `UPDATE files SET quick=? WHERE id=? AND size=? AND mtime=? AND (ctime=0 OR ?=0 OR ctime=?)`, updates)
}

// SetHashBatch 批量写入全量哈希值。
func (s *Store) SetHashBatch(ctx context.Context, updates []HashUpdate) error {
	return s.setHashBatch(ctx, `UPDATE files SET hash=? WHERE id=? AND size=? AND mtime=? AND (ctime=0 OR ?=0 OR ctime=?)`, updates)
}

// setHashBatch 是两种批量写入的共用实现：单事务 + 预编译语句 + 一次提交。
// 条件不命中的行不报错、也不写值，下次扫描会按 quick/hash IS NULL 重新选中。
func (s *Store) setHashBatch(ctx context.Context, sqlText string, updates []HashUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, sqlText) // #nosec G202 -- SQL 语句来自本文件内部常量
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, u := range updates {
		if _, err := stmt.ExecContext(ctx, u.Value, u.ID, u.Size, u.Mtime, u.Ctime, u.Ctime); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// v3.17.4（F-05）：批量写哈希同样是文件数据变更，提交成功后让搜索快照过期。
	// 扫描一轮会触发多次，atomic 计数开销可忽略；不 bump 会让快照在整个扫描期间
	// 一直「看起来是新的」，前端继续展示旧的查重结果。
	s.dataVersion.Add(1)
	return nil
}
