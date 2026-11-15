// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"testing"
	"time"

	"verify/internal/store"
)

func TestParseCleanupSchedule(t *testing.T) {
	cases := []struct {
		in   string
		kind string
	}{
		{"daily 03:00", "daily"},
		{"weekly fri 05:00", "weekly"},
		{"interval 86400", "interval"},
		{"DAILY 03:00", "daily"},
	}
	for _, c := range cases {
		sc, err := parseCleanupSchedule(c.in)
		if err != nil {
			t.Fatalf("parse(%q) 失败：%v", c.in, err)
		}
		if sc.kind != c.kind {
			t.Fatalf("parse(%q).kind = %s，want %s", c.in, sc.kind, c.kind)
		}
	}
	for _, bad := range []string{"", "daily 25:00", "weekly foo 05:00", "interval 30", "unknown"} {
		if _, err := parseCleanupSchedule(bad); err == nil {
			t.Fatalf("parse(%q) 应失败但通过了", bad)
		}
	}
}

func TestPlanDueDaily(t *testing.T) {
	// 2026-11-06 03:00 到点；上次运行在 11-05 03:10，则今天应再跑。
	now := time.Date(2026, 11, 6, 3, 30, 0, 0, time.Local)
	last := time.Date(2026, 11, 5, 3, 10, 0, 0, time.Local).Unix()
	p := store.CleanupPlan{Schedule: "daily 03:00", LastRun: last}
	if !planDue(p, now) {
		t.Fatal("daily 计划应到点")
	}
	// 已经跑过今天 03:00 之后则不应重复。
	p.LastRun = time.Date(2026, 11, 6, 3, 1, 0, 0, time.Local).Unix()
	if planDue(p, now) {
		t.Fatal("daily 计划今天已跑过，不应再次执行")
	}
}

func TestPlanDueWeekly(t *testing.T) {
	// 2026-11-06 是周五；05:00 到点判定。
	now := time.Date(2026, 11, 6, 5, 30, 0, 0, time.Local)
	p := store.CleanupPlan{Schedule: "weekly fri 05:00", LastRun: 0}
	if !planDue(p, now) {
		t.Fatal("weekly 计划首次应到点")
	}
	occ := mostRecentWeekly(now, cleanupSchedule{weekday: time.Friday, hour: 5, minute: 0})
	p.LastRun = occ.Unix()
	if planDue(p, now) {
		t.Fatal("weekly 计划本周已跑过，不应再次执行")
	}
}

func TestPlanDueInterval(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)
	p := store.CleanupPlan{Schedule: "interval 3600", LastRun: now.Add(-20 * time.Minute).Unix()}
	if planDue(p, now) {
		t.Fatal("间隔未到不应执行")
	}
	p.LastRun = now.Add(-2 * time.Hour).Unix()
	if !planDue(p, now) {
		t.Fatal("间隔已到应执行")
	}
}

func TestPickCleanupKeep(t *testing.T) {
	files := []store.FileRow{
		{Path: "/a", Size: 100, Mtime: 3},
		{Path: "/b", Size: 300, Mtime: 1},
		{Path: "/c", Size: 200, Mtime: 2},
	}
	if i, _ := pickCleanupKeep(files, "modify_newest"); i != 0 {
		t.Fatalf("modify_newest 应保留 /a，得到下标 %d", i)
	}
	if i, _ := pickCleanupKeep(files, "largest_size"); i != 1 {
		t.Fatalf("largest_size 应保留 /b，得到下标 %d", i)
	}
}

// TestCleanupExtSet 扩展名集合解析：去点、去空白、转小写、忽略空项、去重。
func TestCleanupExtSet(t *testing.T) {
	got := cleanupExtSet(" .MP4 , mkv,, .mkv , ,AVI")
	want := map[string]bool{"mp4": true, "mkv": true, "avi": true}
	if len(got) != len(want) {
		t.Fatalf("解析结果 %v，期望 %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("缺少扩展名 %q，实际 %v", k, got)
		}
	}
	if len(cleanupExtSet("")) != 0 {
		t.Fatal("空串应得到空集合")
	}
}
