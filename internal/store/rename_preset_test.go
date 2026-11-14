// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestRenamePresetCRUD 验证预设创建、更新、列表与删除。
func TestRenamePresetCRUD(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	p := RenamePreset{Name: "清理分辨率", Description: "去掉 1080p", Methods: `[{"type":"remove_pattern","pattern":"_1080p"}]`}
	if err := s.SaveRenamePreset(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if p.ID == 0 {
		t.Fatal("未回填 ID")
	}
	list, err := s.ListRenamePresets(ctx)
	if err != nil || len(list) != 1 || list[0].Name != p.Name {
		t.Fatalf("ListRenamePresets = %+v, err=%v", list, err)
	}
	p.Name = "改名后的预设"
	if err := s.SaveRenamePreset(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetRenamePreset(ctx, p.ID); got == nil || got.Name != "改名后的预设" {
		t.Fatalf("更新未生效：%+v", got)
	}
	if err := s.DeleteRenamePreset(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetRenamePreset(ctx, p.ID); got != nil {
		t.Fatal("删除后仍能读到")
	}
}

// TestRenameBatchRunningLifecycle 覆盖 v3.10.10 第五轮 P1-2 的两阶段写入：
// 开局落 running 批次（带任务号），结束后补明细与最终状态；崩溃时凭任务号仍能撤销。
func TestRenameBatchRunningLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	id, err := s.CreateRenameBatchRunning(ctx, 7, 2, `[{"type":"trim"}]`, 99)
	if err != nil || id == 0 {
		t.Fatalf("CreateRenameBatchRunning = %d, err=%v", id, err)
	}
	status, raw, taskID, err := s.GetRenameBatchMeta(ctx, id)
	if err != nil || status != "running" || raw != "[]" || taskID != 99 {
		t.Fatalf("开局批次不对：status=%s raw=%s task=%d err=%v", status, raw, taskID, err)
	}
	if list, err := s.ListRunningRenameBatches(ctx, 5); err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("未完成批次列表不对：%+v err=%v", list, err)
	}
	if err := s.FinishRenameBatch(ctx, id, `[{"path":"/v/a.mp4"}]`, "done"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListRunningRenameBatches(ctx, 5); len(list) != 0 {
		t.Fatalf("收尾后不应再有未完成批次：%+v", list)
	}
	status, raw, taskID, _ = s.GetRenameBatchMeta(ctx, id)
	if status != "done" || raw != `[{"path":"/v/a.mp4"}]` || taskID != 99 {
		t.Fatalf("收尾批次不对：status=%s raw=%s task=%d", status, raw, taskID)
	}
}

// TestRenameBatchFullAndPathUpdate 验证批次记录与 files 路径批量更新。
func TestRenameBatchFullAndPathUpdate(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	if _, err := s.UpsertOne(ctx, FileInfo{Path: "/v/a.mp4", Volume: "/v", Name: "a.mp4", Ext: "mp4", Size: 1}, 1); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateRenameBatchRunning(ctx, 7, 1, `[{"type":"trim"}]`, 99)
	if err != nil || id == 0 {
		t.Fatalf("CreateRenameBatchRunning = %d, err=%v", id, err)
	}
	if err := s.FinishRenameBatch(ctx, id, `[{"path":"/v/a.mp4"}]`, "done"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePathsBatch(ctx, [][2]string{{"/v/a.mp4", "/v/b.mp4"}}); err != nil {
		t.Fatal(err)
	}
	if row, _ := s.GetFileByPath(ctx, "/v/b.mp4"); row == nil {
		t.Fatal("路径未更新")
	}
	if row, _ := s.GetFileByPath(ctx, "/v/a.mp4"); row != nil {
		t.Fatal("旧路径仍存在")
	}
	if err := s.AddAuditBatch(ctx, []AuditEntry{{TaskID: 99, Operation: "rename", OriginalPath: "/v/a.mp4", NewPath: "/v/b.mp4"}}); err != nil {
		t.Fatal(err)
	}
}
