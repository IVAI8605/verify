// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"verify/internal/store"
)

// prepareTrashFile 造一个真实的隔离区文件与对应的业务行。
func prepareTrashFile(t *testing.T, st *store.Store, root string) (string, string) {
	t.Helper()
	trashDir := filepath.Join(root, ".verify-trash")
	if err := os.MkdirAll(trashDir, 0o750); err != nil {
		t.Fatal(err)
	}
	trashPath := filepath.Join(trashDir, "a.mkv")
	if err := os.WriteFile(trashPath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := filepath.Join(root, "a.mkv")
	if err := st.AddTrash(context.Background(), orig, trashPath, root, "hash-a", "", "duplicate", 4); err != nil {
		t.Fatal(err)
	}
	return orig, trashPath
}

func trashIDs(t *testing.T, st *store.Store) []int64 {
	t.Helper()
	items, err := st.ListTrash(context.Background())
	if err != nil || len(items) == 0 {
		t.Fatalf("隔离区应有记录：len=%d err=%v", len(items), err)
	}
	ids := make([]int64, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

// TestDeletePermanentRecordsAuditBeforeRemoving 是阶段 A 的核心回环：
// 文件与业务行都消失后，审计行仍能回答删过什么。
func TestDeletePermanentRecordsAuditBeforeRemoving(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()
	orig, trashPath := prepareTrashFile(t, st, root)

	res := e.DeletePermanent(ctx, trashIDs(t, st))
	if len(res.Moved) != 1 || len(res.Failed) != 0 {
		t.Fatalf("应成功删除 1 个：%+v", res)
	}
	if _, err := os.Stat(trashPath); !os.IsNotExist(err) {
		t.Fatal("隔离区文件应已删除")
	}
	left, err := st.ListTrash(ctx)
	if err != nil || len(left) != 0 {
		t.Fatalf("隔离区业务行应已清空：len=%d err=%v", len(left), err)
	}
	rows, total, err := st.ListOps(ctx, store.OpQuery{Action: "trash_delete", Page: 1, PageSize: 10})
	if err != nil || total != 1 {
		t.Fatalf("应留下 1 条审计：total=%d err=%v", total, err)
	}
	if !strings.Contains(rows[0].DetailJSON, orig) || rows[0].TargetCount != 1 {
		t.Fatalf("审计明细缺原路径或条数不对：%+v", rows[0])
	}
}

// TestDeletePermanentFailClosed 模拟审计写入失败（把表删掉）：
// 不可逆操作必须一个文件都不删，业务行也保留。
func TestDeletePermanentFailClosed(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()
	_, trashPath := prepareTrashFile(t, st, root)
	if _, err := st.DB().Exec("DROP TABLE op_log"); err != nil {
		t.Fatal(err)
	}

	res := e.DeletePermanent(ctx, trashIDs(t, st))
	if len(res.Moved) != 0 || len(res.Failed) != 1 {
		t.Fatalf("审计失败时应全部拒绝：%+v", res)
	}
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("文件不应被删除：%v", err)
	}
	left, err := st.ListTrash(ctx)
	if err != nil || len(left) != 1 {
		t.Fatalf("业务行应保留：len=%d err=%v", len(left), err)
	}
}

// TestEmptyTrashFailClosed 同样验证清空隔离区的 fail-closed。
func TestEmptyTrashFailClosed(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()
	_, trashPath := prepareTrashFile(t, st, root)
	if _, err := st.DB().Exec("DROP TABLE op_log"); err != nil {
		t.Fatal(err)
	}

	n, errs := e.EmptyTrash(ctx)
	if n != 0 || len(errs) == 0 {
		t.Fatalf("审计失败时应拒绝清空：n=%d errs=%v", n, errs)
	}
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("文件不应被删除：%v", err)
	}
}

// TestEmptyTrashOneAuditRow 验证批量清空只写一行汇总、明细按上限截断。
func TestEmptyTrashOneAuditRow(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()
	trashDir := filepath.Join(root, ".verify-trash")
	if err := os.MkdirAll(trashDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		p := filepath.Join(trashDir, "f"+string(rune('a'+i))+".mkv")
		if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := st.AddTrash(ctx, filepath.Join(root, "f.mkv"), p, root, "", "", "duplicate", 4); err != nil {
			t.Fatal(err)
		}
	}
	n, errs := e.EmptyTrash(ctx)
	if n != 5 || len(errs) != 0 {
		t.Fatalf("应清空 5 个：n=%d errs=%v", n, errs)
	}
	rows, total, err := st.ListOps(ctx, store.OpQuery{Action: "trash_empty", Page: 1, PageSize: 10})
	if err != nil || total != 1 || rows[0].TargetCount != 5 {
		t.Fatalf("批量清空应只写 1 行汇总：total=%d rows=%+v err=%v", total, rows, err)
	}
}
