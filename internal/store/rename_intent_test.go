// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"fmt"
	"testing"
)

// TestRenameIntentsLifecycle 覆盖意图行的写入、分页、标完成与按批删除。
func TestRenameIntentsLifecycle(t *testing.T) {
	s := openHealthStore(t)
	ctx := context.Background()
	batchID, taskID := int64(7), int64(8)

	pairs := make([][2]string, 0, 1001)
	for i := 0; i < 1001; i++ {
		pairs = append(pairs, [2]string{fmt.Sprintf("/v/old%04d.bin", i), fmt.Sprintf("/v/new%04d.bin", i)})
	}
	if err := s.AddRenameIntents(ctx, batchID, taskID, pairs); err != nil {
		t.Fatal(err)
	}

	// keyset 分页：每页 500，三页取完，顺序与写入一致。
	var after int64
	var got []string
	for {
		page, err := s.ListPendingRenameIntentsAfter(ctx, batchID, after, 500)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, it := range page {
			got = append(got, it.OriginalPath)
		}
		after = page[len(page)-1].ID
	}
	if len(got) != len(pairs) {
		t.Fatalf("期望 %d 条，实际 %d 条", len(pairs), len(got))
	}
	for i := 0; i < len(got); i += 200 {
		if want := pairs[i][0]; got[i] != want {
			t.Fatalf("第 %d 条期望 %s，实际 %s", i, want, got[i])
		}
	}

	// 标完成 600 条后，剩余 pending 应为 401。
	if err := s.MarkRenameIntentsDone(ctx, batchID, pairs[:600]); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPendingRenameIntentsAfter(ctx, batchID, 0, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 401 {
		t.Fatalf("标完成后 pending 期望 401 条，实际 %d 条", len(pending))
	}

	// 按 id 批量改状态。
	ids := make([]int64, 0, len(pending))
	for _, it := range pending {
		ids = append(ids, it.ID)
	}
	if err := s.MarkRenameIntentStatus(ctx, ids, RenameIntentAmbiguous); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM rename_intents WHERE batch_id=? AND status=?`, batchID, RenameIntentAmbiguous).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 401 {
		t.Fatalf("标待确认后期望 401 条，实际 %d 条", n)
	}

	// 批次离开 running 时按批删除，不残留。
	if err := s.FinishRenameBatchState(ctx, batchID, "undone", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM rename_intents WHERE batch_id=?`, batchID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("按批删除后应无剩余，实际 %d 条", n)
	}
}

// TestListUnfinishedRenameBatches 覆盖 v3.10.13：
// 撤销残项与崩溃遗留分开查，每条带 status / failed；只查 running 的老调用方语义不变。
func TestListUnfinishedRenameBatches(t *testing.T) {
	s := openHealthStore(t)
	ctx := context.Background()
	runningID, err := s.CreateRenameBatchRunning(ctx, 0, 3, "[]", 101)
	if err != nil {
		t.Fatal(err)
	}
	partialID, err := s.CreateRenameBatchRunning(ctx, 0, 2, "[]", 102)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRenameBatchState(ctx, partialID, "undone_partial", 1); err != nil {
		t.Fatal(err)
	}

	running, err := s.ListRunningRenameBatches(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 || running[0].ID != runningID || running[0].Status != "running" {
		t.Fatalf("只查 running 时应只回崩溃遗留批次，实际 %+v", running)
	}

	all, err := s.ListUnfinishedRenameBatches(ctx, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("含残项时应回 2 条，实际 %d 条", len(all))
	}
	var sawPartial bool
	for _, b := range all {
		if b.ID == partialID {
			sawPartial = true
			if b.Status != "undone_partial" || b.Failed != 1 {
				t.Fatalf("残项字段不对：%+v", b)
			}
		}
	}
	if !sawPartial {
		t.Fatal("未返回撤销残项批次")
	}
}

// TestAddAuditIfMissing 验证崩溃恢复重复处理同一意图时不会写入第二条审计。
func TestAddAuditIfMissing(t *testing.T) {
	s := openHealthStore(t)
	ctx := context.Background()
	entry := AuditEntry{TaskID: 11, Operation: "rename", OriginalPath: "/v/a.bin", NewPath: "/v/b.bin"}
	if err := s.AddAuditIfMissing(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAuditIfMissing(ctx, entry); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE task_id=11 AND operation='rename'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("重复恢复不应产生重复审计，实际 %d 条", n)
	}
}
