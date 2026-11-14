// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestPathStatsCountsSubtree 悬浮详情只统计该目录及其子目录的已索引文件。
func TestPathStatsCountsSubtree(t *testing.T) {
	s := openHealthStore(t)
	ctx := context.Background()
	cases := []struct {
		path, volume string
		size         int64
	}{
		{"/v/a.mp4", "/v", 10},
		{"/v/sub/b.mkv", "/v", 20},
		{"/v2/c.mp4", "/v2", 30},
	}
	for _, c := range cases {
		if _, err := s.UpsertOne(ctx, FileInfo{
			Path: c.path, Volume: c.volume, Name: c.path, Ext: "mp4",
			Size: c.size, Mtime: 1, Ctime: 1,
		}, 1); err != nil {
			t.Fatal(err)
		}
	}

	files, bytes, err := s.PathStats(ctx, "/v/")
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || bytes != 30 {
		t.Fatalf("PathStats(/v) = %d 个 / %d 字节，want 2 / 30", files, bytes)
	}

	files, bytes, err = s.PathStats(ctx, "/v2")
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || bytes != 30 {
		t.Fatalf("PathStats(/v2) = %d 个 / %d 字节，want 1 / 30", files, bytes)
	}
}

// TestCountsExactWasteUsesAlloc 验证可回收空间按实际占用（alloc）计算：
// 稀疏文件逻辑 4GiB 但只占 1MiB，不能再按逻辑大小虚报；旧库 alloc=0 回退 size。
func TestCountsExactWasteUsesAlloc(t *testing.T) {
	s := openHealthStore(t)
	ctx := context.Background()
	rows := []struct {
		path        string
		size, alloc int64
		inode       int64
	}{
		{"/v/sparse-a.img", 4 << 30, 1 << 20, 11},
		{"/v/sparse-b.img", 4 << 30, 1 << 20, 12},
		{"/v/legacy-a.bin", 100, 0, 21},
		{"/v/legacy-b.bin", 100, 0, 22},
	}
	for _, r := range rows {
		if _, err := s.UpsertOne(ctx, FileInfo{
			Path: r.path, Volume: "/v", Name: filepath.Base(r.path),
			Size: r.size, Alloc: r.alloc, Inode: r.inode, Mtime: 1, Ctime: 1,
		}, 1); err != nil {
			t.Fatal(err)
		}
	}
	// UpsertOne 不负责哈希，这里直接标注两组的哈希，让 Counts 的精确查重聚合生效。
	if _, err := s.DB().Exec(`UPDATE files SET hash='h-sparse' WHERE path IN ('/v/sparse-a.img','/v/sparse-b.img')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE files SET hash='h-legacy' WHERE path IN ('/v/legacy-a.bin','/v/legacy-b.bin')`); err != nil {
		t.Fatal(err)
	}

	c, err := s.Counts(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(1<<20) + 100
	if c.ExactWaste != want {
		t.Fatalf("ExactWaste = %d，want %d（稀疏按 alloc，旧行回退 size）", c.ExactWaste, want)
	}
}
