// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestRewriteHealthDirPrefixWithResidualTargetRow 锁定「目标前缀下有残留行时，
// 健康结论仍要能整体搬到新目录」。
//
// health_results 有 UNIQUE(kind,path)。目录移动后 newDir 是刚 rename 过来的，
// 正常情况下其下不该有行；但旧路径留下的幽灵结论（本项目反复处理的场景）会让
// 整条 UPDATE 回滚并报 UNIQUE constraint failed，结果是一条都搬不过去、
// 全部留在旧路径上——目录已搬走，健康页却还列着搬走前的文件。
func TestRewriteHealthDirPrefixWithResidualTargetRow(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	old := filepath.Join("/", "vol1", "d1")
	newDir := filepath.Join("/", "vol1", "d2")

	if err := s.AppendHealthResults(ctx, "name", []HealthResult{
		{Path: filepath.Join(old, "a.mkv"), Reason: "space", Size: 1},
		// 目标前缀下的残留行：与搬过来的那一行同 kind、同路径，必然撞唯一约束。
		{Path: filepath.Join(newDir, "a.mkv"), Reason: "space", Size: 1},
		{Path: filepath.Join("/", "vol1", "outside.mkv"), Reason: "space", Size: 1},
	}); err != nil {
		t.Fatalf("写入健康结论失败：%v", err)
	}

	n, err := s.RewriteHealthDirPrefix(ctx, old, newDir)
	if err != nil {
		t.Fatalf("目标前缀有残留时不应整条回滚：%v", err)
	}
	if n != 1 {
		t.Fatalf("期望改写 1 条（旧目录下那条），实际 %d", n)
	}

	var oldLeft int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM health_results WHERE path = ? OR substr(path, 1, length(?)) = ?`,
		filepath.Join(old, "a.mkv"), old+string(filepath.Separator), old+string(filepath.Separator)).Scan(&oldLeft); err != nil {
		t.Fatalf("统计旧路径失败：%v", err)
	}
	if oldLeft != 0 {
		t.Fatalf("旧路径下不应再有结论，实际 %d 条", oldLeft)
	}

	var newCount int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM health_results WHERE path = ?`, filepath.Join(newDir, "a.mkv")).Scan(&newCount); err != nil {
		t.Fatalf("统计新路径失败：%v", err)
	}
	if newCount != 1 {
		t.Fatalf("新路径应有 1 条结论，实际 %d", newCount)
	}

	// 目录外的行不能被误伤。
	var outside int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM health_results WHERE path = ?`, filepath.Join("/", "vol1", "outside.mkv")).Scan(&outside); err != nil {
		t.Fatalf("统计目录外结论失败：%v", err)
	}
	if outside != 1 {
		t.Fatalf("目录外的结论不应被改动，实际 %d 条", outside)
	}
}
