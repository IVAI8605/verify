// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// seedDirFiles 在 files 表插入三条测试行：目录下 1 个、子目录 1 个、目录外 1 个。
func seedDirFiles(t *testing.T, s *Store, dir string) {
	t.Helper()
	ctx := context.Background()
	for _, p := range []string{
		filepath.Join(dir, "a.mkv"),
		filepath.Join(dir, "sub", "b.mkv"),
		filepath.Join(filepath.Dir(dir), "outside.mkv"),
	} {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO files(path,name,ext,size,mtime,ctime,dev,inode,status) VALUES(?,?,?,?,?,0,0,0,'active')`,
			p, filepath.Base(p), "mkv", 100, 1700000000000000000); err != nil {
			t.Fatalf("插入测试行失败（%s）：%v", p, err)
		}
	}
}

// TestRewriteDirPrefix 覆盖 v3.14.0 目录前缀改写：
// 目录本身、子目录、目录外三类行只有前两类被改写；LIKE 通配符（%/_）在路径里不误伤。
func TestRewriteDirPrefix(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	old := filepath.Join("/", "vol1", "media", "老剧")
	seedDirFiles(t, s, old)

	sum, err := s.RewriteDirPrefix(ctx, old, filepath.Join("/", "vol1", "media", "新剧"))
	if err != nil {
		t.Fatalf("前缀改写失败：%v", err)
	}
	if sum.Files != 2 {
		t.Fatalf("期望改写 2 行（目录内 1 + 子目录 1），实际 %d", sum.Files)
	}
	for _, p := range []string{
		filepath.Join("/", "vol1", "media", "新剧", "a.mkv"),
		filepath.Join("/", "vol1", "media", "新剧", "sub", "b.mkv"),
	} {
		if row, err := s.GetFileByPath(ctx, p); err != nil || row == nil {
			t.Fatalf("改写后应存在 %s（err=%v row=%v）", p, err, row)
		}
	}
	// 目录外行不受影响
	if row, err := s.GetFileByPath(ctx, filepath.Join("/", "vol1", "media", "outside.mkv")); err != nil || row == nil {
		t.Fatalf("目录外行不应被动：%v %v", err, row)
	}
	// 旧路径行应已消失
	if row, _ := s.GetFileByPath(ctx, filepath.Join(old, "a.mkv")); row != nil {
		t.Fatal("旧路径行应被改写掉")
	}
}

// TestRewriteDirPrefixLikeWildcards 路径含 % 或 _ 时 LIKE 不能把它们当通配符：
// 改写「a_b」目录不应波及「axb」目录。
func TestRewriteDirPrefixLikeWildcards(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	base := filepath.Join("/", "vol1")
	src := filepath.Join(base, "a_b")
	neighbor := filepath.Join(base, "axb")
	for _, p := range []string{
		filepath.Join(src, "in.mkv"),
		filepath.Join(neighbor, "in.mkv"),
	} {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO files(path,name,ext,size,mtime,ctime,dev,inode,status) VALUES(?,?,?,?,?,0,0,0,'active')`,
			p, filepath.Base(p), "mkv", 1, 1); err != nil {
			t.Fatalf("插入失败：%v", err)
		}
	}
	dst := filepath.Join(base, "renamed")
	if _, err := s.RewriteDirPrefix(ctx, src, dst); err != nil {
		t.Fatalf("改写失败：%v", err)
	}
	// a_b 下的行搬走
	if row, _ := s.GetFileByPath(ctx, filepath.Join(src, "in.mkv")); row != nil {
		t.Fatal("a_b 下的行应已改写")
	}
	// axb 下的行必须原样保留（_ 是 LIKE 通配符）
	if row, err := s.GetFileByPath(ctx, filepath.Join(neighbor, "in.mkv")); err != nil || row == nil {
		t.Fatalf("axb 邻居目录不应被波及：%v %v", err, row)
	}
}

// TestRewriteHealthDirPrefix 健康结论跟随目录改写。
func TestRewriteHealthDirPrefix(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	old := filepath.Join("/", "vol1", "d1")
	if err := s.AppendHealthResults(ctx, "name", []HealthResult{
		{Path: filepath.Join(old, "a.mkv"), Reason: "space", Size: 1},
		{Path: filepath.Join(old, "s", "b.mkv"), Reason: "space", Size: 1},
		{Path: filepath.Join("/", "vol1", "c.mkv"), Reason: "space", Size: 1},
	}); err != nil {
		t.Fatalf("写入健康结论失败：%v", err)
	}
	n, err := s.RewriteHealthDirPrefix(ctx, old, filepath.Join("/", "vol1", "d2"))
	if err != nil {
		t.Fatalf("健康前缀改写失败：%v", err)
	}
	if n != 2 {
		t.Fatalf("期望改写 2 条健康结论，实际 %d", n)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM health_results`).Scan(&total); err != nil || total != 3 {
		t.Fatalf("健康结论总数不应变：%v %d", err, total)
	}
	// 新目录前缀下应有 2 条，且新路径必须带分隔符（d2/a.mkv，不是 d2a.mkv）
	var underNew int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM health_results WHERE path LIKE ?`, filepath.Join("/", "vol1", "d2")+`%`).Scan(&underNew); err != nil || underNew != 2 {
		t.Fatalf("d2 前缀下应有 2 条，实际 %d（err=%v）", underNew, err)
	}
	var onePath string
	if err := s.db.QueryRowContext(ctx,
		`SELECT path FROM health_results WHERE path LIKE ? ESCAPE '\' LIMIT 1`,
		EscapeLike(filepath.Join("/", "vol1", "d2")+string(filepath.Separator))+`%`).Scan(&onePath); err != nil {
		t.Fatalf("查新路径失败：%v", err)
	} else if !strings.HasPrefix(onePath, filepath.Join("/", "vol1", "d2")+string(filepath.Separator)) {
		t.Fatalf("新路径应带分隔符：%q", onePath)
	}
}

// TestCountUnderPrefix 统计口径与 ListPathsUnderPrefix 一致（含子目录、不含目录外）。
func TestCountUnderPrefix(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	dir := filepath.Join("/", "vol1", "count")
	seedDirFiles(t, s, dir)
	n, err := s.CountUnderPrefix(ctx, dir)
	if err != nil {
		t.Fatalf("统计失败：%v", err)
	}
	if n != 2 {
		t.Fatalf("期望 2 行，实际 %d", n)
	}
}

// TestDirPrefixCaseSensitive 覆盖同级目录只差 ASCII 大小写的场景（v3.25.2 回归）。
// SQLite 的 LIKE 对 ASCII 默认不区分大小写，旧实现移动 /vol1/Video 时会连
// /vol1/video 的索引与健康结论一起改写走，导致该目录的文件从界面消失。
func TestDirPrefixCaseSensitive(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	upper := filepath.Join("/", "vol1", "Video")
	lower := filepath.Join("/", "vol1", "video")
	for _, dir := range []string{upper, lower} {
		p := filepath.Join(dir, "a.mkv")
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO files(path,name,ext,size,mtime,ctime,dev,inode,status) VALUES(?,?,?,?,?,0,0,0,'active')`,
			p, "a.mkv", "mkv", 100, 1700000000000000000); err != nil {
			t.Fatalf("插入测试行失败（%s）：%v", p, err)
		}
	}
	n, err := s.CountUnderPrefix(ctx, upper)
	if err != nil || n != 1 {
		t.Fatalf("CountUnderPrefix 期望 1，实际 %d（err=%v）", n, err)
	}
	sum, err := s.RewriteDirPrefix(ctx, upper, filepath.Join("/", "vol1", "Moved"))
	if err != nil || sum.Files != 1 {
		t.Fatalf("期望只改写 1 行，实际 %d（err=%v）", sum.Files, err)
	}
	if row, err := s.GetFileByPath(ctx, filepath.Join(lower, "a.mkv")); err != nil || row == nil {
		t.Fatalf("小写目录的索引不应被改写：err=%v row=%v", err, row)
	}
}
