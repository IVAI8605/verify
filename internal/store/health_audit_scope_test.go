// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestLastAuditTaskIDScopedToHealth 回归 v4.1.2：LastAuditTaskID 取全局 MAX，
// 刷新健康页后会拿到别处（trash/组改名/规则引擎）的更大任务号，导致撤销错对象或 done=0。
func TestLastAuditTaskIDScopedToHealth(t *testing.T) {
	s := openHealthStore(t)
	ctx := context.Background()
	// 健康页改名，taskID=10
	if err := s.AddAudit(ctx, AuditEntry{TaskID: 10, Operation: "health_rename", OriginalPath: "/v/a", NewPath: "/v/b"}); err != nil {
		t.Fatal(err)
	}
	// 别处产生更大的任务号（trash / 组内 rename）
	if err := s.AddAudit(ctx, AuditEntry{TaskID: 20, Operation: "trash_add", OriginalPath: "/v/c", NewPath: ""}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAudit(ctx, AuditEntry{TaskID: 30, Operation: "rename", OriginalPath: "/v/d", NewPath: "/v/e"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.LastAuditTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 10 {
		t.Fatalf("应只返回健康页可撤销的任务 10，实际 %d", got)
	}
}

// TestLastAuditTaskIDFanjian 转简体（fanjian）也算健康页可撤销。
func TestLastAuditTaskIDFanjian(t *testing.T) {
	s := openHealthStore(t)
	ctx := context.Background()
	if err := s.AddAudit(ctx, AuditEntry{TaskID: 7, Operation: "fanjian", OriginalPath: "/v/繁", NewPath: "/v/简"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LastAuditTaskID(ctx)
	if err != nil || got != 7 {
		t.Fatalf("fanjian 任务应可撤销，实际 got=%d err=%v", got, err)
	}
}
