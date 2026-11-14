// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestGroupQueryRoots 验证目录范围同时作用于分组计数和组内文件，且路径里的 LIKE 通配符会被转义。
func TestGroupQueryRoots(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	rows := []struct {
		path string
		name string
		hash string
		size int64
	}{
		{"/vol1/media/a.mkv", "a.mkv", "h1", 100},
		{"/vol1/media/sub/b.mkv", "b.mkv", "h1", 100},
		{"/vol1/other/c.mkv", "c.mkv", "h1", 100},
		{"/vol2/media/d.mkv", "d.mkv", "h2", 50},
		{"/vol2/media/e.mkv", "e.mkv", "h2", 50},
		{"/vol1/a_b/x1.bin", "x1.bin", "h3", 10},
		{"/vol1/a_b/x2.bin", "x2.bin", "h3", 10},
		{"/vol1/aXb/y1.bin", "y1.bin", "h3", 10},
		{"/vol1/aXb/y2.bin", "y2.bin", "h3", 10},
		{"/vol1/p%/q1.bin", "q1.bin", "h4", 10},
		{"/vol1/p%/q2.bin", "q2.bin", "h4", 10},
		{"/vol1/pZ/r1.bin", "r1.bin", "h4", 10},
		{"/vol1/pZ/r2.bin", "r2.bin", "h4", 10},
		{"/vol1/media/same.mkv", "same.mkv", "h5", 30},
		{"/vol1/media/sub/same.mkv", "same.mkv", "h6", 30},
		{"/vol1/other/same.mkv", "same.mkv", "h7", 30},
	}
	for _, r := range rows {
		if _, err := s.DB().Exec(
			`INSERT INTO files(path,name,size,quick,hash,status) VALUES(?,?,?,?,?, 'active')`,
			r.path, r.name, r.size, "q", r.hash,
		); err != nil {
			t.Fatal(err)
		}
	}

	// 选中 /vol1/media 时，只应统计并返回该目录及其子目录下的同哈希文件。
	groups, total, err := s.ExactGroups(context.Background(), GroupQuery{MinSize: 0, Roots: []string{"/vol1/media/"}, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(groups) != 1 {
		t.Fatalf("期望 1 个分组，实际 total=%d groups=%d", total, len(groups))
	}
	if len(groups[0].Files) != 2 {
		t.Fatalf("期望组内 2 个文件，实际 %d", len(groups[0].Files))
	}
	for _, f := range groups[0].Files {
		if f.Path != "/vol1/media/a.mkv" && f.Path != "/vol1/media/sub/b.mkv" {
			t.Fatalf("返回了范围外的文件：%s", f.Path)
		}
	}

	// 下划线是 LIKE 单字符通配符，必须按字面匹配 /vol1/a_b。
	groups, total, err = s.ExactGroups(context.Background(), GroupQuery{MinSize: 0, Roots: []string{"/vol1/a_b"}, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(groups) != 1 || len(groups[0].Files) != 2 {
		t.Fatalf("下划线转义失败：total=%d groups=%d", total, len(groups))
	}

	// 百分号是 LIKE 多字符通配符，不能把 /vol1/pZ 也算进来。
	groups, total, err = s.ExactGroups(context.Background(), GroupQuery{MinSize: 0, Roots: []string{"/vol1/p%"}, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(groups) != 1 || len(groups[0].Files) != 2 {
		t.Fatalf("百分号转义失败：total=%d groups=%d", total, len(groups))
	}

	ngroups, ntotal, err := s.NameGroups(context.Background(), GroupQuery{MinSize: 0, Roots: []string{"/vol1/media"}, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if ntotal != 1 || len(ngroups) != 1 || len(ngroups[0].Files) != 2 {
		t.Fatalf("同名分组范围过滤失败：total=%d groups=%d", ntotal, len(ngroups))
	}
}
