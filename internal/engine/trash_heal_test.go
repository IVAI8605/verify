// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestHealTrashRowsKeepsRecordsWhenVolumeUnmounted 覆盖 v3.25.1：
// 「卷根 Stat 成功」不等于「卷真的挂载着」。飞牛存储池或外接盘卸载后，
// 挂载点常残留空目录，此时隔离文件全部 Lstat 不到，旧实现会把整卷记录当幽灵行删掉，
// 界面恢复入口随之消失（文件其实还在盘上，卷回来就成了不可见孤儿）。
// 现在的规则：隔离目录 .verify-trash 不存在 → 卷不可信 → 一律保留记录。
func TestHealTrashRowsKeepsRecordsWhenVolumeUnmounted(t *testing.T) {
	e, st := newHealthTestEngine(t)
	ctx := context.Background()
	vol := t.TempDir()
	// v3.25.2：不能走 cfg.Replace —— 它会做严格校验（扫描根必须在 /vol 下），
	// t.TempDir() 在 CI 上是 /tmp/...，测试会因为环境而非逻辑失败。
	// 改用环境变量注入扫描根，与 Volumes() 的兜底分支同源。
	t.Setenv("DUP_CLEAN_SCAN_ROOTS", vol)

	trashPath := filepath.Join(vol, ".verify-trash", "2026-11-10", "a.mkv")
	if err := st.AddTrash(ctx, filepath.Join(vol, "a.mkv"), trashPath, vol, "", "", "duplicate", 4); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListTrash(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("准备数据失败：rows=%d err=%v", len(rows), err)
	}

	// 此时隔离文件与隔离目录都不存在，等价于「卷没挂上」。
	kept := e.HealTrashRows(ctx, rows)
	if len(kept) != 1 {
		t.Fatalf("卷不可信时必须保留记录，实际保留 %d 条", len(kept))
	}

	// 隔离目录出现后，才允许把「文件确实不在了」的记录清掉。
	if err := os.MkdirAll(filepath.Join(vol, ".verify-trash"), 0o750); err != nil {
		t.Fatal(err)
	}
	rows2, err := st.ListTrash(ctx)
	if err != nil || len(rows2) != 1 {
		t.Fatalf("准备数据失败：rows=%d err=%v", len(rows2), err)
	}
	if kept2 := e.HealTrashRows(ctx, rows2); len(kept2) != 0 {
		t.Fatalf("隔离目录存在时应清掉幽灵行，实际保留 %d 条", len(kept2))
	}
	if rows3, _ := st.ListTrash(ctx); len(rows3) != 0 {
		t.Fatalf("幽灵行应已从库里删除，剩余 %d 条", len(rows3))
	}
}
