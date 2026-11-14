// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestListOpSnapshotsItemsTruncated 覆盖 v3.13.0（UI 审查 P3-5）：
// 明细恰好等于上限且确实只有这么多时不能报截断，超出时才标 truncated。
func TestListOpSnapshotsItemsTruncated(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	// 正好 2 条，itemLimit 也是 2：不应报截断。
	exactID, err := s.CreateOpSnapshot(ctx, "rename")
	if err != nil {
		t.Fatalf("创建快照失败：%v", err)
	}
	if err := s.AddOpSnapshotItems(ctx, exactID, []OpSnapshotItem{
		{Path: "/v/a1.mkv", Size: 10, Mtime: 1},
		{Path: "/v/a2.mkv", Size: 10, Mtime: 1},
	}); err != nil {
		t.Fatalf("写入快照条目失败：%v", err)
	}
	if err := s.FinishOpSnapshot(ctx, exactID, nil, ""); err != nil {
		t.Fatalf("收尾快照失败：%v", err)
	}

	// 3 条，itemLimit=2：应报截断。
	overID, err := s.CreateOpSnapshot(ctx, "rename")
	if err != nil {
		t.Fatalf("创建快照失败：%v", err)
	}
	if err := s.AddOpSnapshotItems(ctx, overID, []OpSnapshotItem{
		{Path: "/v/b1.mkv", Size: 10, Mtime: 1},
		{Path: "/v/b2.mkv", Size: 10, Mtime: 1},
		{Path: "/v/b3.mkv", Size: 10, Mtime: 1},
	}); err != nil {
		t.Fatalf("写入快照条目失败：%v", err)
	}
	if err := s.FinishOpSnapshot(ctx, overID, nil, ""); err != nil {
		t.Fatalf("收尾快照失败：%v", err)
	}

	_, list, err := s.ListOpSnapshots(ctx, 1, 10, "rename", "all", "", 2)
	if err != nil {
		t.Fatalf("查询快照失败：%v", err)
	}
	if len(list) != 2 {
		t.Fatalf("期望 2 个快照，实际 %d", len(list))
	}
	for _, it := range list {
		switch it.ID {
		case exactID:
			if it.ItemsTruncated {
				t.Fatalf("恰好 2 条且上限 2 时不应报截断：%+v", it)
			}
		case overID:
			if !it.ItemsTruncated {
				t.Fatalf("3 条被截成 2 条时必须报截断：%+v", it)
			}
		}
	}
}

// TestOpSnapshotRoundTrip 验证快照条目能落库、对账结果能回填，
// 且 skipped 不计入 checked（跳过项不算差异，避免「没动手」被误报）。
func TestOpSnapshotRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	runID, err := s.CreateOpSnapshot(ctx, "trash")
	if err != nil {
		t.Fatalf("创建快照失败：%v", err)
	}
	items := []OpSnapshotItem{
		{Path: "/v/a.mkv", Size: 100, Mtime: 1700000000000000000, Hash: "abc"},
		{Path: "/v/b.mkv", Size: 200, Mtime: 1700000000000000000},
		{Path: "/v/c.mkv", Size: 300, Mtime: 1700000000000000000},
	}
	if err := s.AddOpSnapshotItems(ctx, runID, items); err != nil {
		t.Fatalf("写入快照条目失败：%v", err)
	}

	results := []OpSnapshotResult{
		{Path: "/v/a.mkv", Size: 100, Mtime: 1700000000000000000, Hash: "abc",
			Expect: "relocated", FinalPath: "/v/.verify-trash/2026-01-01/a.mkv", State: "ok"},
		{Path: "/v/b.mkv", Size: 200, Expect: "absent", State: "still_exists", Detail: "仍然存在"},
		{Path: "/v/c.mkv", Size: 300, Expect: "absent", State: "skipped", Detail: "预检失败"},
	}
	if err := s.FinishOpSnapshot(ctx, runID, results, "有文件仍在原路径"); err != nil {
		t.Fatalf("回填对账结果失败：%v", err)
	}

	kind, _, finishedAt, checked, mismatched, note, err := s.GetOpSnapshotRun(ctx, runID)
	if err != nil {
		t.Fatalf("读取快照汇总失败：%v", err)
	}
	if kind != "trash" || finishedAt == 0 {
		t.Fatalf("快照汇总不对：kind=%q finished_at=%d", kind, finishedAt)
	}
	// 3 条里 1 条 skipped，checked 应为 2，其中 1 条与预期不符。
	if checked != 2 || mismatched != 1 {
		t.Fatalf("计数不对：checked=%d mismatched=%d，期望 2/1", checked, mismatched)
	}
	if note == "" {
		t.Fatal("对账建议没有落库")
	}

	got, err := s.ListOpSnapshotItems(ctx, runID, 10)
	if err != nil {
		t.Fatalf("读取快照条目失败：%v", err)
	}
	if len(got) != 3 {
		t.Fatalf("条目数不对：%d，期望 3", len(got))
	}
	// 差异项排在最前，界面上不用翻页就能看到问题。
	if got[0].Path != "/v/b.mkv" || got[0].State != "still_exists" {
		t.Fatalf("差异项排序不对：首条 %s/%s", got[0].Path, got[0].State)
	}
	if got[1].Path != "/v/a.mkv" || got[1].Hash != "abc" {
		t.Fatalf("一致项应保留快照哈希：%+v", got[1])
	}
	for _, r := range got {
		if r.Path == "/v/c.mkv" && r.Hash != "" {
			t.Fatalf("无哈希条目应留空：%+v", r)
		}
	}
}

// TestFinishUnfinishedOpSnapshotsByKind 验证只结清指定类型的未完成快照（P2-01）。
func TestFinishUnfinishedOpSnapshotsByKind(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	renameID, err := s.CreateOpSnapshot(ctx, "rename_engine")
	if err != nil {
		t.Fatal(err)
	}
	trashID, err := s.CreateOpSnapshot(ctx, "trash")
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.FinishUnfinishedOpSnapshotsByKind(ctx, "rename_engine", "改名批次 #3 已撤销")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应结清 1 个改名快照，实际 %d 个", n)
	}
	_, _, finishedAt, _, _, note, err := s.GetOpSnapshotRun(ctx, renameID)
	if err != nil {
		t.Fatal(err)
	}
	if finishedAt == 0 || note == "" {
		t.Fatalf("改名快照未结清：finished_at=%d note=%q", finishedAt, note)
	}
	// 其他类型的未完成快照不该被顺手关掉。
	_, _, trashFinished, _, _, _, err := s.GetOpSnapshotRun(ctx, trashID)
	if err != nil {
		t.Fatal(err)
	}
	if trashFinished != 0 {
		t.Fatal("隔离快照不应被改名结清逻辑影响")
	}
}

// TestFileHashesByPathsSkipsMissing 验证取哈希只读库、缺失即不返回（快照不去重算）。
func TestFileHashesByPathsSkipsMissing(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	got, err := s.FileHashesByPaths(ctx, []string{"/v/none-1.mkv", "/v/none-2.mkv"})
	if err != nil {
		t.Fatalf("批量取哈希失败：%v", err)
	}
	if len(got) != 0 {
		t.Fatalf("库里没有的文件不应返回哈希，实际 %d 条", len(got))
	}
}
