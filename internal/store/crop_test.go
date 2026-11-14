// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestCropCandidatesPassesLimit 回归 BUG-04（v1.42.0）：
// SQL 写了 LIMIT ? 却没把 limit 传进 QueryContext，modernc SQLite 报
// "missing argument with index 1"，竖横屏识别自 v1.39 起 100% 失败。
func TestCropCandidatesPassesLimit(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	files := []struct{ path, ext string }{
		{"/v/a.mp4", "mp4"},
		{"/v/b.mkv", "mkv"},
		{"/v/c.avi", "avi"},
		{"/v/d.txt", "txt"},
	}
	for _, f := range files {
		if _, err := s.UpsertOne(ctx, FileInfo{
			Path: f.path, Volume: "/v", Name: f.path, Ext: f.ext,
			Size: 10, Mtime: 1, Ctime: 1,
		}, 1); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.CropCandidates(ctx, 2)
	if err != nil {
		t.Fatalf("CropCandidates 不应报错（BUG-04 回潮）：%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit=2 应返回 2 条，实际 %d", len(got))
	}

	// limit=0 走兜底，不应 panic。
	if _, err := s.CropCandidates(ctx, 0); err != nil {
		t.Fatalf("limit=0 应使用兜底值而不是报错：%v", err)
	}
}

// TestCropCandidatesSkipsLabeled 回归 v3.19.0：已打标文件不再返回，
// CountCropCandidates 与待检测列表保持同一口径，前端才能判断进度与收工。
func TestCropCandidatesSkipsLabeled(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	for _, f := range []struct{ path, ext string }{
		{"/v/a.mp4", "mp4"},
		{"/v/b.mkv", "mkv"},
		{"/v/c.txt", "txt"},
	} {
		if _, err := s.UpsertOne(ctx, FileInfo{
			Path: f.path, Volume: "/v", Name: f.path, Ext: f.ext,
			Size: 10, Mtime: 1, Ctime: 1,
		}, 1); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.CountCropCandidates(ctx)
	if err != nil || n != 2 {
		t.Fatalf("待检测视频应为 2，实际 %d（err=%v）", n, err)
	}
	got, err := s.CropCandidates(ctx, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("应返回 2 个待检测视频，实际 %d（err=%v）", len(got), err)
	}
	if err := s.SetCropLabels(ctx, []CropEntry{{ID: got[0].ID, W: 1920, H: 1080}}); err != nil {
		t.Fatal(err)
	}
	if n, err = s.CountCropCandidates(ctx); err != nil || n != 1 {
		t.Fatalf("打标后应剩 1 个，实际 %d（err=%v）", n, err)
	}
	again, err := s.CropCandidates(ctx, 10)
	if err != nil || len(again) != 1 || again[0].ID == got[0].ID {
		t.Fatalf("已打标文件不应再出现：%+v（err=%v）", again, err)
	}
}
