// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestMoveFilesKeepsFileTagsAndID 锁定「只换位置不改名」不能删行重建。
//
// 移动此前走 dropIndexPath + UpdatePath：DeleteByPath 在同一个事务里连
// file_tags、probe_cache 一起删，UpdatePath 又按新路径建一条新 id 的行——挂在
// files.id 上的用户打标与音视频指纹全部丢失，文件会被当成新文件重新排队分析。
// store.UpdatePathsBatch / UpdateProbePathsBatch 正是为这个场景写的。
func TestMoveFilesKeepsFileTagsAndID(t *testing.T) {
	ctx := context.Background()
	e, st := newHealthTestEngine(t)
	vol := t.TempDir()
	t.Setenv("DUP_CLEAN_SCAN_ROOTS", vol)

	srcDir := filepath.Join(vol, "src")
	dstDir := filepath.Join(vol, "dst")
	for _, d := range []string{srcDir, dstDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(srcDir, "clip.mp4")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 直接插入索引行与附属数据，避免依赖扫描。file_tags 挂在 files.id 上。
	db := st.DB()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO files(path,volume,name,size,mtime,ext) VALUES(?,?,?,?,?,?)`,
		src, vol, "clip.mp4", 1, 0, ".mp4"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM files WHERE path=?`, src).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO video_tags(id,name) VALUES(1,'测试标签')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO file_tags(file_id,tag_id,value) VALUES(?,1,'剧A')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO probe_cache(path,status) VALUES(?,'ok')`, src); err != nil {
		t.Fatal(err)
	}

	res := e.MoveFiles(ctx, dstDir, []string{src})
	if len(res.Done) != 1 || len(res.Failed) != 0 {
		t.Fatalf("移动应成功：%+v", res)
	}
	newPath := res.Done[0].To
	if want := filepath.Join(dstDir, "clip.mp4"); newPath != want {
		t.Fatalf("目标路径 = %s，期望 %s", newPath, want)
	}

	// 新路径必须还是同一行（id 不变），标签与探测缓存不能被删掉重建。
	var newID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM files WHERE path=?`, newPath).Scan(&newID); err != nil {
		t.Fatalf("新路径应仍在索引里：%v", err)
	}
	if newID != id {
		t.Fatalf("移动后 files.id 变了（%d → %d），挂在 id 上的标签会一起丢", id, newID)
	}
	var tags int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_tags WHERE file_id=?`, newID).Scan(&tags); err != nil {
		t.Fatal(err)
	}
	if tags != 1 {
		t.Fatalf("用户打标应保留 1 条，实际 %d", tags)
	}
	// 探测缓存按 path 存主键，必须跟着文件走到新路径。
	var probe int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_cache WHERE path=?`, newPath).Scan(&probe); err != nil {
		t.Fatal(err)
	}
	if probe != 1 {
		t.Fatalf("探测缓存应改写到新路径，实际 %d 条", probe)
	}
	// 旧路径不能留行，也不能留探测缓存。
	var oldRows, oldProbe int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE path=?`, src).Scan(&oldRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_cache WHERE path=?`, src).Scan(&oldProbe); err != nil {
		t.Fatal(err)
	}
	if oldRows != 0 || oldProbe != 0 {
		t.Fatalf("旧路径应无残留：files=%d probe_cache=%d", oldRows, oldProbe)
	}
}
