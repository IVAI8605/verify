// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"strings"
	"testing"
)

// TestActivePartialIndexes 守住 v3.10.10（第七轮 P1-1）的两条部分索引：
// 精确组 / 同名组翻页都必须命中它们，且不再为 GROUP BY 建临时 B-tree。
// 查询语句与 exact.go 的 GROUP BY 子句保持一致，改那边时这里会先失败。
func TestActivePartialIndexes(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	defer func() { _ = s.Close() }()

	cases := []struct {
		name  string
		index string
		sql   string
	}{
		{
			name:  "精确组按 hash 分组",
			index: "idx_files_hash_active",
			sql: `SELECT hash, COUNT(*), SUM(size) FROM files INDEXED BY idx_files_hash_active
				WHERE status='active' AND hash IS NOT NULL AND hash<>'' AND hash NOT LIKE 'ERR%' AND size>=?
				GROUP BY hash HAVING COUNT(*)>1 ORDER BY SUM(size) DESC LIMIT ? OFFSET ?`,
		},
		{
			name:  "同名组按 lower(name) 分组",
			index: "idx_files_lower_name_active",
			sql: `SELECT lower(name), COUNT(*), SUM(size) FROM files INDEXED BY idx_files_lower_name_active
				WHERE status='active' AND size>=?
				GROUP BY lower(name) HAVING COUNT(*)>1 ORDER BY SUM(size) DESC LIMIT ? OFFSET ?`,
		},
	}
	for _, c := range cases {
		rows, err := s.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+c.sql, int64(0), 20, 0)
		if err != nil {
			t.Fatalf("%s：取查询计划失败：%v", c.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notUsed int
			var detail string
			if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
				_ = rows.Close()
				t.Fatalf("%s：读取查询计划失败：%v", c.name, err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("%s：读取查询计划失败：%v", c.name, err)
		}
		joined := strings.Join(plan, "\n")
		if !strings.Contains(joined, c.index) {
			t.Fatalf("%s：未命中部分索引 %s，实际计划：\n%s", c.name, c.index, joined)
		}
		if strings.Contains(joined, "TEMP B-TREE FOR GROUP BY") {
			t.Fatalf("%s：仍在为分组建临时 B-tree，实际计划：\n%s", c.name, joined)
		}
	}
}
