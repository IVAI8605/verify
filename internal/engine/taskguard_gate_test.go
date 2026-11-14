// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"verify/internal/taskguard"
)

// holdExclusive 占住磁盘 IO 独占组，模拟「扫描正在跑」（v3.25.2 P3-4 / P3-5）。
func holdExclusive(t *testing.T, e *Engine, kind taskguard.Kind) {
	t.Helper()
	done, err := e.acquireTask(kind, "测试占用", taskguard.GroupIO, taskguard.ResDiskIO)
	if err != nil {
		t.Fatalf("占位失败：%v", err)
	}
	t.Cleanup(func() { done("done") })
}

// TestEmptyDirCleanBlockedByExclusiveTask 验证空目录清理过闸门（P3-4）：
// 被拒时返回冲突原因，且一个目录都不许动。
func TestEmptyDirCleanBlockedByExclusiveTask(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)
	dir := filepath.Join(root, "empty-a")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	holdExclusive(t, e, taskguard.KindScan)

	res := e.EmptyDirClean(context.Background(), []string{dir})
	if res.Conflict == "" {
		t.Fatal("扫描运行时空目录清理应被闸门拒绝")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("被拒绝时不得移走目录：%v", err)
	}
}

// TestTrashOpsBlockedByExclusiveTask 验证隔离区三个写操作都过闸门（P3-5）。
func TestTrashOpsBlockedByExclusiveTask(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()
	prepareTrashFile(t, st, root)
	holdExclusive(t, e, taskguard.KindScan)

	if res := e.Restore(ctx, trashIDs(t, st)); res.Conflict == "" {
		t.Fatal("恢复隔离区应被闸门拒绝")
	}
	if res := e.DeletePermanent(ctx, trashIDs(t, st)); res.Conflict == "" {
		t.Fatal("永久删除应被闸门拒绝")
	}
	if n, errs := e.EmptyTrash(ctx); n != 0 || len(errs) == 0 {
		t.Fatalf("清空隔离区应被拒绝，实际 n=%d errs=%v", n, errs)
	}
	// 三次都被拒，隔离文件必须原封不动。
	if _, err := os.Stat(filepath.Join(root, ".verify-trash", "a.mkv")); err != nil {
		t.Fatalf("被拒绝时不得动隔离文件：%v", err)
	}
}

// TestRestoreLockedBypassesGate 验证内部回滚走的无闸门版本：
// 转码 / 剪除失败回滚时已持 FFmpeg 独占组，若走 Restore 会被自己的闸门拦下。
func TestRestoreLockedBypassesGate(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()
	orig, _ := prepareTrashFile(t, st, root)
	holdExclusive(t, e, taskguard.KindTrim)

	res := e.restoreLocked(ctx, trashIDs(t, st))
	if res.Conflict != "" {
		t.Fatalf("restoreLocked 不应过闸门：%s", res.Conflict)
	}
	if len(res.Restored) != 1 {
		t.Fatalf("应恢复 1 个文件，实际 %+v", res)
	}
	if _, err := os.Stat(orig); err != nil {
		t.Fatalf("原文件未回到原位：%v", err)
	}
}
