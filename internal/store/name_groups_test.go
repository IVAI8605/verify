// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"fmt"
	"testing"
)

// TestNameGroupsNonASCIIKeyConsistency 回归 P1-2（v3.10.10）：
// 组头键来自 SQL 的 lower(name)，组内文件列表曾用 Go 的 strings.ToLower 重建。
// SQLite 只折叠 ASCII、Go 做 Unicode 折叠，名字含 Ä / 全角Ａ / 非法 UTF-8 时
// 两边键不一致，表现为「组头有计数、展开后文件列表为空」。
func TestNameGroupsNonASCIIKeyConsistency(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	names := []string{"Ä.txt", "全角Ａ.txt", "Б.txt", "bad\xff.txt"}
	for i, name := range names {
		for j := 0; j < 2; j++ {
			path := fmt.Sprintf("/v%d/%d/%s", j, i, name)
			if _, err := s.UpsertOne(ctx, FileInfo{
				Path: path, Volume: "/v", Name: name, Ext: "txt", Size: 10, Mtime: 1, Ctime: 1,
			}, 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	groups, _, err := s.NameGroups(ctx, GroupQuery{MinSize: 1, Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != len(names) {
		t.Fatalf("同名组应为 %d 个，实际 %d", len(names), len(groups))
	}
	for _, g := range groups {
		if len(g.Files) != g.Count || len(g.Files) == 0 {
			t.Fatalf("组 %q 计数 %d，文件列表却 %d 条", g.Name, g.Count, len(g.Files))
		}
	}
}

// TestNameGroupsSkipTotal 翻页跳过 COUNT（v3.10.10，第七轮 P1-2）：
// 第一页算总数，第二页返回 -1，由前端沿用第一页的值。
func TestNameGroupsSkipTotal(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("dup%d.txt", i)
		for j := 0; j < 2; j++ {
			path := fmt.Sprintf("/v%d/%d/%s", j, i, name)
			if _, err := s.UpsertOne(ctx, FileInfo{
				Path: path, Volume: "/v", Name: name, Ext: "txt", Size: 10, Mtime: 1, Ctime: 1,
			}, 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	first, total, err := s.NameGroups(ctx, GroupQuery{MinSize: 1, Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 || len(first) != 2 {
		t.Fatalf("第一页应返回 2 组 / 共 6 组，实际 %d / %d", len(first), total)
	}
	second, skipped, err := s.NameGroups(ctx, GroupQuery{MinSize: 1, Page: 2, PageSize: 2, SkipTotal: true})
	if err != nil {
		t.Fatal(err)
	}
	if skipped != -1 {
		t.Fatalf("跳过 COUNT 时应返回 total=-1，实际 %d", skipped)
	}
	if len(second) != 2 || second[0].Key == first[0].Key {
		t.Fatalf("第二页应返回不同的 2 组，实际 %d 组", len(second))
	}
}
