// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 本文件回归 v4.0.1 数据安全检查（2026-11-18）的问题 1/2/3/5：
//  1. 永久删除/清空隔离区删除阶段失败时补写结果审计；
//  2. 请求取消时剩余条目按「未处理」计，不误报「索引中不存在」；
//  3. 幽灵隔离行改由启动维护任务在写锁内清理；
//  5. 隔离失败回滚不重建用户已删除的原目录。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"verify/internal/store"
)

// makeStuckTrashPath 造一个「非空目录」当隔离路径：os.Remove 会因目录非空失败，
// 用来稳定模拟「删除阶段的个别失败」，不依赖权限（root 下 chmod 无效）。
func makeStuckTrashPath(t *testing.T, trashDir, name string) string {
	t.Helper()
	p := filepath.Join(trashDir, name)
	if err := os.MkdirAll(filepath.Join(p, "child"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "child", "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func findOpRow(t *testing.T, rows []store.OpRow, needle string) *store.OpRow {
	t.Helper()
	for i := range rows {
		if strings.Contains(rows[i].Summary, needle) {
			return &rows[i]
		}
	}
	return nil
}

// TestDeletePermanentPartialFailureWritesResultAudit 问题1：
// 部分文件删除失败时，除授权行外还要有一条带实际成功/失败数的结果审计。
func TestDeletePermanentPartialFailureWritesResultAudit(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()
	trashDir := filepath.Join(root, ".verify-trash")
	if err := os.MkdirAll(trashDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// 一个正常文件（会被删掉）
	okPath := filepath.Join(trashDir, "ok.mkv")
	if err := os.WriteFile(okPath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTrash(ctx, filepath.Join(root, "ok.mkv"), okPath, root, "", "", "duplicate", 4); err != nil {
		t.Fatal(err)
	}
	// 一个非空目录（os.Remove 失败）
	badPath := makeStuckTrashPath(t, trashDir, "bad.mkv")
	if err := st.AddTrash(ctx, filepath.Join(root, "bad.mkv"), badPath, root, "", "", "duplicate", 4); err != nil {
		t.Fatal(err)
	}

	res := e.DeletePermanent(ctx, trashIDs(t, st))
	if len(res.Moved) != 1 || len(res.Failed) != 1 {
		t.Fatalf("应成功 1、失败 1，实际 %+v", res)
	}
	if _, err := os.Stat(badPath); err != nil {
		t.Fatalf("删除失败的目录应仍存在：%v", err)
	}
	rows, total, err := st.ListOps(ctx, store.OpQuery{Action: "trash_delete", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("部分失败应留下授权行+结果行共 2 条，实际 %d：%+v", total, rows)
	}
	rr := findOpRow(t, rows, "永久删除结果")
	if rr == nil {
		t.Fatalf("缺少结果审计行：%+v", rows)
	}
	if rr.Result != "partial" {
		t.Fatalf("结果审计应为 partial，实际 %q", rr.Result)
	}
	if rr.TargetCount != 2 {
		t.Fatalf("结果审计计划数应为 2，实际 %d", rr.TargetCount)
	}
	if !strings.Contains(rr.DetailJSON, badPath) {
		t.Fatalf("结果审计明细应包含失败路径 %s，实际 %s", badPath, rr.DetailJSON)
	}
}

// TestEmptyTrashPartialFailureWritesResultAudit 问题1（清空路径）。
func TestEmptyTrashPartialFailureWritesResultAudit(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()
	trashDir := filepath.Join(root, ".verify-trash")
	if err := os.MkdirAll(trashDir, 0o750); err != nil {
		t.Fatal(err)
	}
	okPath := filepath.Join(trashDir, "ok.mkv")
	if err := os.WriteFile(okPath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTrash(ctx, filepath.Join(root, "ok.mkv"), okPath, root, "", "", "duplicate", 4); err != nil {
		t.Fatal(err)
	}
	badPath := makeStuckTrashPath(t, trashDir, "bad.mkv")
	if err := st.AddTrash(ctx, filepath.Join(root, "bad.mkv"), badPath, root, "", "", "duplicate", 4); err != nil {
		t.Fatal(err)
	}

	n, errs := e.EmptyTrash(ctx)
	if n != 1 || len(errs) == 0 {
		t.Fatalf("应成功清 1、有失败原因，实际 n=%d errs=%v", n, errs)
	}
	rows, total, err := st.ListOps(ctx, store.OpQuery{Action: "trash_empty", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("部分失败应留下 2 条审计，实际 %d：%+v", total, rows)
	}
	rr := findOpRow(t, rows, "清空隔离区结果")
	if rr == nil || rr.Result != "partial" || rr.TargetCount != 2 {
		t.Fatalf("结果审计不正确：%+v", rr)
	}
}

// TestTrashFilesCanceledReportsUnprocessed 问题2：请求取消后剩余条目按未处理计，不算失败。
func TestTrashFilesCanceledReportsUnprocessed(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	paths := []string{filepath.Join(root, "a.mkv"), filepath.Join(root, "b.mkv")}
	res, err := e.trashFilesLocked(ctx, paths, nil)
	if err != nil {
		t.Fatalf("取消不应返回错误：%v", err)
	}
	if res.Unprocessed != 2 || len(res.Failed) != 0 || len(res.Moved) != 0 {
		t.Fatalf("取消应记未处理 2、失败 0、移动 0，实际 %+v", res)
	}
	if len(res.Errors) == 0 || !strings.Contains(res.Errors[0], "操作已取消") {
		t.Fatalf("应给出「操作已取消」提示，实际 %v", res.Errors)
	}
}

// TestRestoreCanceledReportsUnprocessed 问题2（恢复路径）。
func TestRestoreCanceledReportsUnprocessed(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	prepareTrashFile(t, st, root)
	ids := trashIDs(t, st)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := e.restoreLocked(ctx, ids)
	if res.Unprocessed != len(ids) || len(res.Restored) != 0 || len(res.Failed) != 0 {
		t.Fatalf("取消应记未处理 %d、无恢复无失败，实际 %+v", len(ids), res)
	}
}

// TestRollbackTrashRecordMissingParent 问题5：原父目录已删除时不重建目录，文件留在隔离区。
func TestRollbackTrashRecordMissingParent(t *testing.T) {
	root := t.TempDir()
	e, _ := newHealthTestEngine(t)
	p := filepath.Join(root, "gone", "a.mkv") // 父目录 gone 不存在
	trash := filepath.Join(root, "trash-a.mkv")
	if err := os.WriteFile(trash, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	restored, note := e.rollbackTrashRecord(nil, p, trash, errors.New("boom"))
	if restored {
		t.Fatal("原目录不存在时不应回滚")
	}
	if _, err := os.Stat(filepath.Join(root, "gone")); !os.IsNotExist(err) {
		t.Fatalf("不得重建已删除的目录，实际 err=%v", err)
	}
	if _, err := os.Stat(trash); err != nil {
		t.Fatalf("文件应仍留在隔离区：%v", err)
	}
	if !strings.Contains(note, "原目录已不存在") {
		t.Fatalf("说明应指出原目录已不存在，实际 %q", note)
	}
}

// TestRollbackTrashRecordRestoresWhenParentExists 问题5 正例：父目录在时应正常搬回。
func TestRollbackTrashRecordRestoresWhenParentExists(t *testing.T) {
	root := t.TempDir()
	e, _ := newHealthTestEngine(t)
	parent := filepath.Join(root, "sub")
	if err := os.MkdirAll(parent, 0o750); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(parent, "a.mkv")
	trash := filepath.Join(root, "trash-a.mkv")
	if err := os.WriteFile(trash, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	restored, _ := e.rollbackTrashRecord(nil, p, trash, errors.New("boom"))
	if !restored {
		t.Fatal("父目录存在时应能回滚")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("文件应搬回原路径：%v", err)
	}
	if _, err := os.Stat(trash); !os.IsNotExist(err) {
		t.Fatal("隔离区不应残留副本")
	}
}

// TestHealTrashMaintenanceDeletesGhost 问题3：启动维护任务在写锁内清幽灵行，GET 不再写库。
func TestHealTrashMaintenanceDeletesGhost(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()
	trashDir := filepath.Join(root, ".verify-trash")
	if err := os.MkdirAll(trashDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// 一条真实记录 + 一条幽灵记录
	realPath := filepath.Join(trashDir, "real.mkv")
	if err := os.WriteFile(realPath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTrash(ctx, filepath.Join(root, "real.mkv"), realPath, root, "", "", "duplicate", 4); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTrash(ctx, filepath.Join(root, "ghost.mkv"), filepath.Join(trashDir, "ghost.mkv"), root, "", "", "duplicate", 4); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.ListTrash(ctx); len(rows) != 2 {
		t.Fatalf("基线应有 2 条，实际 %d", len(rows))
	}

	e.HealTrashMaintenance(ctx)
	if rows, _ := st.ListTrash(ctx); len(rows) != 1 {
		t.Fatalf("幽灵行应被清掉、只剩真实 1 条，实际 %d", len(rows))
	}
}
