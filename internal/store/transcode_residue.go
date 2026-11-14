// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import "context"

// ActiveVideoPaths 返回索引里仍在活动的视频文件路径（v3.23.0）。
//
// 转码残留清理只扫这些文件所在目录的一层，不做全盘 WalkDir：
// 大库几十万文件，递归遍历会把机械盘读满，而临时文件一定和源文件同目录。
func (s *Store) ActiveVideoPaths(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 || limit > 200000 {
		limit = 50000
	}
	// #nosec G202 -- 片段仅由常量 videoExts 生成
	rows, err := s.db.QueryContext(ctx,
		`SELECT path FROM files WHERE status='active' AND ext IN (`+videoExtSQL()+`) ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]string, 0, limit)
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
