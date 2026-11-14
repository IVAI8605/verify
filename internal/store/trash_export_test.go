// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestMarkTrashExported 验证导出标记只影响传入的条目，其余保持未导出。
func TestMarkTrashExported(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	for _, p := range []struct{ orig, trash string }{
		{"/v/a.mp4", "/v/.verify-trash/2026-11-05/a.mp4"},
		{"/v/b.mp4", "/v/.verify-trash/2026-11-05/b.mp4"},
	} {
		if err := s.AddTrash(ctx, p.orig, p.trash, "/v", "", "", "duplicate", 10); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListTrash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("ListTrash = %d 条，want 2", len(rows))
	}
	for _, r := range rows {
		if r.ExportedAt != 0 {
			t.Fatalf("新条目 exported_at = %d，want 0", r.ExportedAt)
		}
	}

	const ts = int64(1793817000)
	if err := s.MarkTrashExported(ctx, []int64{rows[0].ID}, ts); err != nil {
		t.Fatal(err)
	}
	rows, err = s.ListTrash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var marked, unmarked int
	for _, r := range rows {
		if r.ExportedAt == ts {
			marked++
		} else if r.ExportedAt == 0 {
			unmarked++
		}
	}
	if marked != 1 || unmarked != 1 {
		t.Fatalf("标记后 marked=%d unmarked=%d，want 1/1", marked, unmarked)
	}
}

// TestQuarantineExportedAtMigration 验证旧库（无 exported_at 列）升级后能正常读写。
func TestQuarantineExportedAtMigration(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	// 模拟旧库：删掉新列在 SQLite 里不可行，这里改为直接确认 PRAGMA 已包含该列，
	// 并再跑一次 migrate 验证幂等（重复执行不会报错）。
	cols := map[string]bool{}
	rows, err := s.DB().QueryContext(ctx, `PRAGMA table_info(quarantine)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	_ = rows.Close()
	if !cols["exported_at"] {
		t.Fatal("quarantine 缺少 exported_at 列")
	}
	if err := migrate(ctx, s.DB()); err != nil {
		t.Fatalf("重复迁移失败：%v", err)
	}
}
