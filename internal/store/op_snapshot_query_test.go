// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestListOpSnapshots 覆盖 v3.11.0 的历史快照只读接口：
// 分页、result 筛选与对账分类计数必须一致，且不能因为查询改动触发任何写操作。
func TestListOpSnapshots(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	defer func() { _ = s.Close() }()

	runID, err := s.CreateOpSnapshot(ctx, "rename")
	if err != nil {
		t.Fatalf("建快照失败：%v", err)
	}
	if err := s.AddOpSnapshotItems(ctx, runID, []OpSnapshotItem{
		{Path: "/vol1/a.mkv", Size: 10},
		{Path: "/vol1/b.mkv", Size: 20},
	}); err != nil {
		t.Fatalf("写快照条目失败：%v", err)
	}
	if err := s.FinishOpSnapshot(ctx, runID, []OpSnapshotResult{
		{Path: "/vol1/a.mkv", Expect: "renamed", FinalPath: "/vol1/a2.mkv", State: "ok"},
		{Path: "/vol1/b.mkv", Expect: "renamed", FinalPath: "/vol1/b2.mkv", State: "path_mismatch", Detail: "目标路径不符"},
	}, "测试"); err != nil {
		t.Fatalf("回填对账失败：%v", err)
	}

	// 再造一条未完成快照（模拟崩溃）：mismatched 仍是 0，但 finished_at=0。
	if _, err := s.CreateOpSnapshot(ctx, "trash"); err != nil {
		t.Fatalf("建未完成快照失败：%v", err)
	}

	total, list, err := s.ListOpSnapshots(ctx, 1, 20, "", "all", "", 200)
	if err != nil {
		t.Fatalf("查询快照失败：%v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("期望 2 条快照，实际 total=%d len=%d", total, len(list))
	}
	unfinished := 0
	for _, it := range list {
		if it.FinishedAt == 0 {
			unfinished++
		}
	}
	if unfinished != 1 {
		t.Fatalf("应能识别 1 条未完成快照，实际 %d", unfinished)
	}
	var done OpSnapshotSummary
	for _, it := range list {
		if it.ID == runID {
			done = it
		}
	}
	if done.Diff.PathMismatch != 1 {
		t.Fatalf("期望 path_mismatch=1，实际 %+v", done.Diff)
	}
	if done.StartedAt == 0 || done.FinishedAt == 0 || len(done.Items) != 2 {
		t.Fatalf("快照时间或明细缺失：%+v", done)
	}

	total, list, err = s.ListOpSnapshots(ctx, 1, 20, "", "mismatch", "b2", 200)
	if err != nil {
		t.Fatalf("按差异与关键字查询失败：%v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("关键字应命中该快照，实际 total=%d", total)
	}

	total, _, err = s.ListOpSnapshots(ctx, 1, 20, "", "ok", "", 200)
	if err != nil {
		t.Fatalf("按 result=ok 查询失败：%v", err)
	}
	if total != 0 {
		t.Fatalf("存在差异项时 result=ok 应为 0，实际 %d", total)
	}
}
