// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

// v3.3.0：通用 meta 读写与偏好全量读取，供「偏好反哺」聚合使用。

import (
	"context"
	"database/sql"
	"errors"
)

// GetMeta 读取 meta 表的一个键；不存在返回 ("", false, nil)。
func (s *Store) GetMeta(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return v, true, nil
}

// SetMeta 写入/覆盖 meta 键值。
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ListUserPref 返回最近的偏好记录（最多 limit 条），供权重聚合。
func (s *Store) ListUserPref(ctx context.Context, limit int) ([]UserPrefEntry, error) {
	if limit < 1 || limit > 100000 {
		limit = 10000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT group_key,suggested,chosen FROM user_pref ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []UserPrefEntry{}
	for rows.Next() {
		var e UserPrefEntry
		if err := rows.Scan(&e.GroupKey, &e.Suggested, &e.Chosen); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
