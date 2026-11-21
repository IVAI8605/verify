// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"fmt"
	"testing"
)

// TestNameGroupsMinSizeFiltersMembers 锁定「组头筛选条件 = 成员筛选条件」。
//
// 组头 WHERE 是 `status='active' AND size>=?`（最小体积），但取成员时此前只拼了
// rootCondition + filterCondition，没有 size>=?。于是最小体积设 150 时，100 字节的
// 同名文件仍会出现在成员列表里，而 RecomputeStats 又按含它的集合重算
// Count/TotalSize/Waste——界面数字与用户设的筛选条件互相矛盾。
func TestNameGroupsMinSizeFiltersMembers(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	// 三个同名文件，体积 100 / 200 / 300 字节；最小体积设 150。
	for i, size := range []int64{100, 200, 300} {
		name := "clip.mp4"
		if _, err := s.UpsertOne(ctx, FileInfo{
			Path:   fmt.Sprintf("/vol1/media/d%d/%s", i, name),
			Volume: "/vol1",
			Name:   name,
			Size:   size,
			Mtime:  1000,
			Ctime:  1000,
			Ext:    "mp4",
		}, 1); err != nil {
			t.Fatalf("插入文件失败：%v", err)
		}
	}

	groups, total, err := s.NameGroups(ctx, GroupQuery{MinSize: 150, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("同名分组失败：%v", err)
	}
	if total != 1 {
		t.Fatalf("期望 1 组（组头按最小体积过滤），实际 %d", total)
	}
	if len(groups) != 1 {
		t.Fatalf("期望返回 1 组，实际 %d", len(groups))
	}
	for _, f := range groups[0].Files {
		if f.Size < 150 {
			t.Fatalf("成员 %s（%d 字节）低于最小体积 150，不应出现在结果里", f.Path, f.Size)
		}
	}
	if len(groups[0].Files) != 2 {
		t.Fatalf("期望 2 个成员（200/300），实际 %d", len(groups[0].Files))
	}
}
