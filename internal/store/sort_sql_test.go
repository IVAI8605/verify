// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import "testing"

// TestGroupSQLSortWhitelist 锁死组级排序 SQL 白名单映射：
// 已知键映射到受控列表达式，非白名单键（含注入串）一律回退到 waste，
// 保证外部 sort 参数永远不会拼进 SQL 字符串（v1.14.2 §4.2 的安全前提）。
func TestGroupSQLSortWhitelist(t *testing.T) {
	waste := "(SUM(" + occupExpr + ")-MAX(" + occupExpr + "))"
	cases := []struct {
		sortKey string
		order   string
		want    string
	}{
		{"total_size", "desc", "SUM(" + occupExpr + ") DESC"},
		{"count", "asc", "COUNT(*) ASC"},
		{"max_size", "desc", "MAX(" + occupExpr + ") DESC"},
		{"max_mtime", "asc", "MAX(mtime) ASC"},
		{"min_mtime", "desc", "MIN(mtime) DESC"},
		{"name", "desc", "MAX(name) DESC"},
		{"waste", "desc", waste + " DESC"},
		{"path", "asc", waste + " ASC"},         // 非 SQL 键回退 waste
		{"' OR 1=1--", "desc", waste + " DESC"}, // 注入串也回退
		{"", "", waste + " DESC"},               // 空键 + 默认方向
	}
	for _, c := range cases {
		if got := groupSQLSort(c.sortKey, c.order); got != c.want {
			t.Fatalf("groupSQLSort(%q,%q)=%q，期望 %q", c.sortKey, c.order, got, c.want)
		}
	}
}
