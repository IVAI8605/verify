// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

// 本文件是 v3.21.0 两阶段改名计划表的回归：分批写入、按待执行顺序取片、
// 回填后进度正确，覆盖 1000 条分片边界。

import (
	"context"
	"testing"
	"time"
)

func TestRenamePlanChunksAndStatus(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	const total = 2500
	planID, err := s.CreateRenamePlan(ctx, total)
	if err != nil {
		t.Fatal(err)
	}
	items := make([]RenamePlanItemRow, 0, total)
	for i := 0; i < total; i++ {
		items = append(items, RenamePlanItemRow{
			OldPath: "/v/a.txt", OldName: "a.txt", NewName: "b.txt",
		})
	}
	// 显式跨过 1000 条分片边界，确认能全部落库。
	if err := s.AddRenamePlanItems(ctx, planID, 0, items); err != nil {
		t.Fatal(err)
	}
	tot, done, failed, pending, status, err := s.RenamePlanStatus(ctx, planID)
	if err != nil || tot != total || done != 0 || failed != 0 || pending != total || status != "planned" {
		t.Fatalf("初始状态不正确：total=%d done=%d failed=%d pending=%d status=%s err=%v", tot, done, failed, pending, status, err)
	}

	first, err := s.NextRenamePlanItems(ctx, planID, 1200)
	if err != nil || len(first) != 1200 {
		t.Fatalf("第一片应取 1200 条，实际 %d（err=%v）", len(first), err)
	}
	if first[0].Idx != 0 || first[1199].Idx != 1199 {
		t.Fatalf("待执行顺序应按 idx 升序：首 %d 末 %d", first[0].Idx, first[1199].Idx)
	}
	results := make([]RenamePlanResult, 0, len(first))
	for i, it := range first {
		state, msg := "done", ""
		if i == 0 {
			state, msg = "failed", "目标已存在"
		}
		results = append(results, RenamePlanResult{Idx: it.Idx, State: state, Error: msg, BatchID: 7})
	}
	if err := s.MarkRenamePlanResults(ctx, planID, results); err != nil {
		t.Fatal(err)
	}
	_, done, failed, pending, _, err = s.RenamePlanStatus(ctx, planID)
	if err != nil || done != 1199 || failed != 1 || pending != total-1200 {
		t.Fatalf("回填后进度不正确：done=%d failed=%d pending=%d err=%v", done, failed, pending, err)
	}

	rest, err := s.NextRenamePlanItems(ctx, planID, 5000)
	if err != nil || len(rest) != total-1200 {
		t.Fatalf("剩余应取 %d 条，实际 %d（err=%v）", total-1200, len(rest), err)
	}
	for _, it := range rest {
		if err := s.MarkRenamePlanResults(ctx, planID, []RenamePlanResult{{Idx: it.Idx, State: "done"}}); err != nil {
			t.Fatal(err)
		}
	}
	_, _, _, pending, _, err = s.RenamePlanStatus(ctx, planID)
	if err != nil || pending != 0 {
		t.Fatalf("全部回填后 pending 应为 0，实际 %d（err=%v）", pending, err)
	}
	if ok, err := s.RenamePlanExists(ctx, planID); err != nil || !ok {
		t.Fatalf("计划应存在：ok=%v err=%v", ok, err)
	}
}

// TestRenamePlanPurgeKeepsPendingPlan 覆盖 v3.25.1 的清理规则：
// 旧实现按 created_at 一刀切，用户暂停超过 7 天再回来时，
// 一次新建计划就会把仍有 pending 的可续跑计划连头带明细删光，
// 续跑接口随即报「计划不存在」。现在只清理没有 pending 明细的计划。
func TestRenamePlanPurgeKeepsPendingPlan(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	old := time.Now().AddDate(0, 0, -8).Unix()

	pendingPlan, err := s.CreateRenamePlan(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddRenamePlanItems(ctx, pendingPlan, 0, []RenamePlanItemRow{
		{Idx: 0, OldPath: "/v/a.txt", OldName: "a.txt", NewName: "a_1.txt"},
		{Idx: 1, OldPath: "/v/b.txt", OldName: "b.txt", NewName: "b_1.txt"},
	}); err != nil {
		t.Fatal(err)
	}

	donePlan, err := s.CreateRenamePlan(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddRenamePlanItems(ctx, donePlan, 0, []RenamePlanItemRow{
		{Idx: 0, OldPath: "/v/c.txt", OldName: "c.txt", NewName: "c_1.txt"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRenamePlanResults(ctx, donePlan, []RenamePlanResult{{Idx: 0, State: "done"}}); err != nil {
		t.Fatal(err)
	}

	// 把两份计划都改成 8 天前，模拟用户暂停很久后回来。
	for _, id := range []int64{pendingPlan, donePlan} {
		if _, err := s.db.ExecContext(ctx, `UPDATE rename_plans SET created_at=? WHERE id=?`, old, id); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.CreateRenamePlan(ctx, 1); err != nil {
		t.Fatal(err)
	}

	if ok, err := s.RenamePlanExists(ctx, pendingPlan); err != nil || !ok {
		t.Fatalf("仍有 pending 的旧计划不应被清理：ok=%v err=%v", ok, err)
	}
	if ok, _ := s.RenamePlanExists(ctx, donePlan); ok {
		t.Fatal("已跑完的旧计划应被清理")
	}
	if items, err := s.NextRenamePlanItems(ctx, pendingPlan, 10); err != nil || len(items) != 2 {
		t.Fatalf("续跑应仍能取到 2 条 pending：%d err=%v", len(items), err)
	}
}
