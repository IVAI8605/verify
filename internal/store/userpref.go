// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import "context"

// UserPrefEntry 一条偏好记录：建议保留 vs 用户实际处理（F4 二期）。
// Chosen 为空表示用户直接采纳建议；非空表示用户改选（被清理的是 Chosen）。
type UserPrefEntry struct {
	GroupKey  string `json:"group_key"`
	Suggested string `json:"suggested"`
	Chosen    string `json:"chosen"`
}

// UserPrefStats 学习统计。
type UserPrefStats struct {
	Total   int64 `json:"total"`
	Adopt   int64 `json:"adopt"`   // 直接采纳
	Changed int64 `json:"changed"` // 改选
}

// v3.9.10：原 CreateUserPrefTable 已删除——user_pref 表由 store.go 的 schema 统一建，
// 这里的重复建表零调用，留着只会让人误以为建表分散在两处。

// RecordUserPref 批量记录偏好。
func (s *Store) RecordUserPref(ctx context.Context, entries []UserPrefEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO user_pref(group_key,suggested,chosen,created_at) VALUES(?,?,?,strftime('%s','now'))`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.GroupKey, e.Suggested, e.Chosen); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UserPrefStats 统计采纳/改选比例（反哺权重的数据基础）。
func (s *Store) UserPrefStats(ctx context.Context) (UserPrefStats, error) {
	var st UserPrefStats
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN chosen='' OR chosen=suggested THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN chosen<>'' AND chosen<>suggested THEN 1 ELSE 0 END),0)
		FROM user_pref`)
	err := row.Scan(&st.Total, &st.Adopt, &st.Changed)
	return st, err
}

// ClearUserPref 重置学习数据。
func (s *Store) ClearUserPref(ctx context.Context) (int64, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM user_pref`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
