// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestCleanupPlanCRUD 验证计划的创建、更新、列表与删除。
func TestCleanupPlanCRUD(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	p := CleanupPlan{
		Name: "每周清理重复视频", Enabled: true, Schedule: "weekly fri 05:00",
		MatchMode: "video", MinSize: 0, MinCount: 2, Extensions: "mp4,mkv",
		RetainStrategy: "modify_newest", MaxKeepGB: 10,
	}
	if err := s.SaveCleanupPlan(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if p.ID == 0 {
		t.Fatal("新建计划未回填 ID")
	}
	plans, err := s.ListCleanupPlans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].Name != p.Name || !plans[0].Enabled {
		t.Fatalf("ListCleanupPlans = %+v", plans)
	}

	p.Name = "改名后的计划"
	p.Enabled = false
	if err := s.SaveCleanupPlan(ctx, &p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCleanupPlan(ctx, p.ID)
	if err != nil || got == nil {
		t.Fatalf("GetCleanupPlan = %+v, err=%v", got, err)
	}
	if got.Name != "改名后的计划" || got.Enabled {
		t.Fatalf("更新未生效：%+v", got)
	}

	if err := s.UpdateCleanupPlanResult(ctx, p.ID, 123, "移入 2 个文件", ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetCleanupPlan(ctx, p.ID)
	if got.LastRun != 123 || got.LastResult != "移入 2 个文件" {
		t.Fatalf("结果未写入：%+v", got)
	}

	if err := s.DeleteCleanupPlan(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetCleanupPlan(ctx, p.ID); got != nil {
		t.Fatal("删除后仍能读到计划")
	}
}
