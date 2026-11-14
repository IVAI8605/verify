// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeSnapFile 造一个真实文件，供快照/对账用例使用。
func writeSnapFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestOpSnapshotStillExists 覆盖「预期不存在的还在」：操作没生效时必须报差异。
func TestOpSnapshotStillExists(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()

	p := filepath.Join(root, "a.txt")
	writeSnapFile(t, p, "hello")

	snap := e.beginOpSnapshot(ctx, "test", []string{p})
	// 故意不动文件：预期「应已不存在」，实际仍在。
	rep := e.finishOpSnapshot(snap)

	if rep.Checked != 1 || rep.Mismatched != 1 {
		t.Fatalf("计数不对：checked=%d mismatched=%d，期望 1/1", rep.Checked, rep.Mismatched)
	}
	if len(rep.Items) != 1 || rep.Items[0].State != opStateStillExists {
		t.Fatalf("差异状态不对：%+v", rep.Items)
	}
	if rep.Advice == "" {
		t.Fatal("有差异时必须给出下一步建议")
	}
	// 对账结果要落库，断点后仍能复盘。
	_, _, finishedAt, checked, mismatched, note, err := st.GetOpSnapshotRun(ctx, rep.SnapshotID)
	if err != nil {
		t.Fatalf("读取快照失败：%v", err)
	}
	if finishedAt == 0 || checked != 1 || mismatched != 1 || note == "" {
		t.Fatalf("快照落库不完整：finished=%d checked=%d mismatched=%d note=%q", finishedAt, checked, mismatched, note)
	}
}

// TestOpSnapshotRenameChecks 覆盖改名类对账：mtime 不变算通过，大小变了要报差异。
func TestOpSnapshotRenameChecks(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)
	ctx := context.Background()

	// 场景一：正常改名，大小与 mtime 都不变 → 一致。
	p1 := filepath.Join(root, "b.txt")
	writeSnapFile(t, p1, "data")
	snap1 := e.beginOpSnapshot(ctx, "test", []string{p1})
	dst1 := filepath.Join(root, "b-new.txt")
	if err := os.Rename(p1, dst1); err != nil {
		t.Fatal(err)
	}
	snap1.MarkRenamed(p1, dst1)
	if rep := e.finishOpSnapshot(snap1); rep.Mismatched != 0 {
		t.Fatalf("正常改名不应报差异：%+v", rep.Items)
	}

	// 场景二：改名后内容被追加，大小与快照不一致 → 必须报差异。
	p2 := filepath.Join(root, "c.txt")
	writeSnapFile(t, p2, "data")
	snap2 := e.beginOpSnapshot(ctx, "test", []string{p2})
	dst2 := filepath.Join(root, "c-new.txt")
	if err := os.Rename(p2, dst2); err != nil {
		t.Fatal(err)
	}
	writeSnapFile(t, dst2, "data-and-more")
	snap2.MarkRenamed(p2, dst2)
	rep := e.finishOpSnapshot(snap2)
	if rep.Mismatched != 1 || rep.Items[0].State != opStateChanged {
		t.Fatalf("大小变化应报 changed：%+v", rep.Items)
	}
}

// TestOpSnapshotSkipNotCounted 验证「没动手」的条目不算差异，避免对账误报。
func TestOpSnapshotSkipNotCounted(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)

	p := filepath.Join(root, "d.txt")
	writeSnapFile(t, p, "x")
	snap := e.beginOpSnapshot(context.Background(), "test", []string{p})
	snap.Skip(p, "预检失败，未执行")

	rep := e.finishOpSnapshot(snap)
	if rep.Checked != 0 || rep.Mismatched != 0 {
		t.Fatalf("跳过项不应计数：checked=%d mismatched=%d", rep.Checked, rep.Mismatched)
	}
}

// TestReconcileUnfinishedSnapshots 未完成快照只读对账（v3.10.10，第五轮 P1-1）：
// 原路径仍在记 still_exists；原路径消失且无 final_path 记 missing；全程不动文件。
func TestReconcileUnfinishedSnapshots(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)
	ctx := context.Background()

	keep := filepath.Join(root, "keep.mkv")
	gone := filepath.Join(root, "gone.mkv")
	writeSnapFile(t, keep, "x")
	writeSnapFile(t, gone, "y")
	// 只开快照不 finish，模拟进程在操作中途崩溃
	e.beginOpSnapshot(ctx, "test", []string{keep, gone})
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	runs, err := e.ReconcileUnfinishedSnapshots(ctx, 5, 100)
	if err != nil {
		t.Fatalf("只读对账失败：%v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("应识别出 1 次未完成 run，实际 %d", len(runs))
	}
	r := runs[0]
	if r.Checked != 2 || r.StillExists != 1 || r.Missing != 1 || len(r.Problems) != 2 {
		t.Fatalf("对账计数不对：checked=%d still=%d missing=%d problems=%d", r.Checked, r.StillExists, r.Missing, len(r.Problems))
	}
	// 只读：原路径文件必须保持原样
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("对账不应改动文件：%v", err)
	}
}
