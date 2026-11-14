// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import "context"

// FuzzyCandidates 全量活动文件（模糊文件名分组用，v1.34）。
// v3.10.10（第七轮 P1-3）：不再 ORDER BY path。调用方按名字键重新分组并自行排序，
// 排序对业务没有意义；去掉后走 idx_files_status 顺序输出，省掉 168k 行的临时 B-tree。
func (s *Store) FuzzyCandidates(ctx context.Context) ([]FileRow, error) {
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files WHERE status='active'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]FileRow, 0, 4096)
	for rows.Next() {
		f, err := s.scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}
