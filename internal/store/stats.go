// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"strings"
)

// PathStats 返回指定目录（含其子目录）下已索引文件的个数与总大小（v2.2.0 悬浮详情）。
func (s *Store) PathStats(ctx context.Context, dir string) (int64, int64, error) {
	dir = strings.TrimRight(strings.TrimSpace(dir), "/")
	if dir == "" {
		dir = "/"
	}
	prefix := dir + "/"
	if dir == "/" {
		prefix = "/"
	}
	var files, bytes int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(size),0) FROM files
		 WHERE status='active' AND (path = ? OR substr(path, 1, ?) = ?)`,
		dir, len(prefix), prefix).Scan(&files, &bytes)
	return files, bytes, err
}
