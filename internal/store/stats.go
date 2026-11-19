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
	// v4.1.1：SQLite 的 substr 对文本按「字符」计数，而 Go 的 len(prefix) 是「字节」，
	// 中文路径下两者不等（"/vol1/电影/" 是 13 字节 / 9 字符），旧写法会把子目录文件全漏掉。
	// 让 SQLite 用 length(?) 自己按字符量 prefix，口径一致（与 dir_prefix.go 同款）。
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(size),0) FROM files
		 WHERE status='active' AND (path = ? OR substr(path, 1, length(?)) = ?)`,
		dir, prefix, prefix).Scan(&files, &bytes)
	return files, bytes, err
}
