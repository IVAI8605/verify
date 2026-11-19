// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRenameGroupToKeepsFileTagsAndID 回归 v4.1.1：
// 组内统一命名走「删旧行 + UpdatePath 建新行」会丢掉挂在 files.id 上的用户打标、
// 探测缓存，还会让未变内容被重新排队分析。同文件的 RenameOne 早已改用
// RenameFilePathsBatch 只改 path/name/ext，这里锁住 RenameGroupTo 也走同一条路。
func TestRenameGroupToKeepsFileTagsAndID(t *testing.T) {
	ctx := context.Background()
	e, st := newHealthTestEngine(t)
	vol := t.TempDir()
	t.Setenv("DUP_CLEAN_SCAN_ROOTS", vol)

	baseDir := filepath.Join(vol, "base")
	otherDir := filepath.Join(vol, "other")
	for _, d := range []string{baseDir, otherDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	basePath := filepath.Join(baseDir, "movie.mkv")
	target := filepath.Join(otherDir, "clip.mp4")
	for _, p := range []string{basePath, target} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 直接插入索引行与附属数据，避免依赖扫描。file_tags 挂在 files.id 上。
	db := st.DB()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO files(path,volume,name,size,mtime,ext) VALUES(?,?,?,?,?,?)`,
		target, vol, filepath.Base(target), 1, 0, ".mp4"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM files WHERE path=?`, target).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO video_tags(id,name) VALUES(1,'测试标签')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO file_tags(file_id,tag_id,value) VALUES(?,1,'剧A')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO probe_cache(path,status) VALUES(?,'ok')`, target); err != nil {
		t.Fatal(err)
	}
	// 媒体指纹直接挂在 files 行上，重建行会一起丢掉。
	if _, err := db.ExecContext(ctx, `UPDATE files SET img_hash='abc123', duration=12.5 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}

	res := e.RenameGroupTo(ctx, basePath, []string{target})
	if len(res.Done) != 1 || len(res.Failed) != 0 {
		t.Fatalf("改名应成功：%+v", res)
	}
	newPath := res.Done[0].To

	// 新路径必须还是同一行（id 不变），标签与探测缓存不能被删掉重建。
	var newID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM files WHERE path=?`, newPath).Scan(&newID); err != nil {
		t.Fatalf("新路径不在索引中：%v", err)
	}
	if newID != id {
		t.Fatalf("改名的 id 应保持 %d，实际 %d（重建会丢标签/指纹）", id, newID)
	}
	var tag string
	if err := db.QueryRowContext(ctx,
		`SELECT value FROM file_tags WHERE file_id=? AND tag_id=1`, newID).Scan(&tag); err != nil {
		t.Fatalf("改名后用户打标丢失：%v", err)
	}
	if tag != "剧A" {
		t.Fatalf("标签值应为 剧A，实际 %q", tag)
	}
	var imgHash string
	var duration float64
	if err := db.QueryRowContext(ctx, `SELECT img_hash,duration FROM files WHERE id=?`, newID).Scan(&imgHash, &duration); err != nil {
		t.Fatalf("读取媒体指纹失败：%v", err)
	}
	if imgHash != "abc123" || duration != 12.5 {
		t.Fatalf("改名后媒体指纹丢失：img_hash=%q duration=%v", imgHash, duration)
	}
	// RenameFilePathsBatch 只改 files 三列，不碰 probe_cache（与 RenameOne 一致）。
	// 关键是 DeleteByPath 没被调用——否则旧路径的探测缓存会被级联删掉。
	var probeLeft int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_cache WHERE path=?`, target).Scan(&probeLeft); err != nil {
		t.Fatal(err)
	}
	if probeLeft != 1 {
		t.Fatalf("改名不应删除探测缓存（说明误走了 DeleteByPath），剩余 %d 行", probeLeft)
	}
}
