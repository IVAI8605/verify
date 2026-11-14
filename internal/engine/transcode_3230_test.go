// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 本文件是 v3.23.0 转码正确性的回归：入队前确认视频容器、残留只扫视频目录一层。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"verify/internal/store"
)

func TestTranscodeInputReject(t *testing.T) {
	e, _, _ := newMoveDirEngine(t)
	dir := t.TempDir()
	txt := filepath.Join(dir, "fake.mp4")
	if err := os.WriteFile(txt, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reason := e.transcodeInputReject(txt); reason == "" {
		t.Fatal("文本改名成 .mp4 应被拒绝")
	}
	// MP4 文件头：4 字节长度 + ftyp box。
	mp4 := filepath.Join(dir, "real.mp4")
	head := append([]byte{0, 0, 0, 0x18}, []byte("ftypmp42")...)
	if err := os.WriteFile(mp4, head, 0o600); err != nil {
		t.Fatal(err)
	}
	if reason := e.transcodeInputReject(mp4); reason != "" {
		t.Fatalf("合法 MP4 不应被拒绝：%s", reason)
	}
}

func TestFindTranscodeResidue(t *testing.T) {
	e, st, root := newMoveDirEngine(t)
	ctx := context.Background()
	dir := filepath.Join(root, "videos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(dir, "movie_h265_tmp123.mp4")
	if err := os.WriteFile(oldPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldPath, past, past); err != nil {
		t.Fatal(err)
	}
	// 新鲜残留不处理，源视频文件也不能被当成残留。
	if err := os.WriteFile(filepath.Join(dir, "new_h265_tmp123.mp4"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertOne(ctx, store.FileInfo{
		Path: src, Volume: root, Name: "movie.mkv", Ext: "mkv", Size: 1, Mtime: 1, Ctime: 1,
	}, 1); err != nil {
		t.Fatal(err)
	}

	files, dirs, err := e.FindTranscodeResidue(ctx, 24*time.Hour, 100)
	if err != nil {
		t.Fatal(err)
	}
	if dirs != 1 {
		t.Fatalf("应只扫描 1 个视频目录，实际 %d", dirs)
	}
	if len(files) != 1 || files[0].Path != oldPath {
		t.Fatalf("应只命中 48 小时前的残留，实际 %+v", files)
	}
}

// TestCleanTranscodeResidueMovesUnindexed 回归 v3.25.0 P0-2：
// 残留临时文件从未入库，旧实现复用「必须能在索引里查到」的隔离路径，
// 结果真正的残留 100% 清理失败。现在必须能直接搬进隔离区。
func TestCleanTranscodeResidueMovesUnindexed(t *testing.T) {
	e, _, root := newMoveDirEngine(t)
	ctx := context.Background()
	dir := filepath.Join(root, "videos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	residue := filepath.Join(dir, "movie_h265_tmp999.mp4")
	if err := os.WriteFile(residue, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 普通视频文件不能被这个接口顺手搬走。
	normal := filepath.Join(dir, "keep.mp4")
	if err := os.WriteFile(normal, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := e.CleanTranscodeResidue(ctx, []string{residue, normal})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Moved) != 1 || res.Moved[0] != residue {
		t.Fatalf("残留应被移入隔离区：%+v", res)
	}
	if len(res.Failed) != 1 || res.Failed[0] != normal {
		t.Fatalf("普通文件应被拒绝：%+v", res)
	}
	if _, err := os.Stat(residue); !os.IsNotExist(err) {
		t.Fatalf("残留应已离开原目录：%v", err)
	}
	if _, err := os.Stat(normal); err != nil {
		t.Fatalf("普通文件不应被动：%v", err)
	}
}
