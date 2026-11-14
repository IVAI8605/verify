// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import "testing"

// TestRetainSuggest 完整性优先 + 质量优先。
func TestRetainSuggest(t *testing.T) {
	files := []RetainFile{
		{Path: "/vol1/broken.mkv", Name: "broken.mkv", Size: 100, Broken: true, Mtime: 1700000000},
		{Path: "/vol1/good.mkv", Name: "good.mkv", Size: 200, Width: 1920, Height: 1080, BitRate: 8000000, Duration: 90, Mtime: 1700000000},
	}
	sug := RetainSuggest(files)
	if sug.Retain != "/vol1/good.mkv" {
		t.Fatalf("损坏文件不应被建议保留：%s", sug.Retain)
	}
	if sug.Confidence != "high" {
		t.Fatalf("完整性差异应 high，实际 %s", sug.Confidence)
	}

	// 质量信号主导：高分辨率 vs 路径更整洁
	files2 := []RetainFile{
		{Path: "/vol1/a/b/c/movie-480p.mkv", Name: "movie-480p.mkv", Width: 640, Height: 480, BitRate: 1000000, Mtime: 1700000000},
		{Path: "/vol1/movie-1080p.mkv", Name: "movie-1080p.mkv", Width: 1920, Height: 1080, BitRate: 8000000, Mtime: 1700000000},
	}
	sug2 := RetainSuggest(files2)
	if sug2.Retain != "/vol1/movie-1080p.mkv" {
		t.Fatalf("质量优先应保留 1080p：%s（%s）", sug2.Retain, sug2.Reason)
	}

	// 得分接近 → low 置信
	files3 := []RetainFile{
		{Path: "/vol1/x1.mkv", Name: "x1.mkv", Width: 1280, Height: 720, BitRate: 4000000, Mtime: 1700000000},
		{Path: "/vol1/x2.mkv", Name: "x2.mkv", Width: 1280, Height: 720, BitRate: 4000000, Mtime: 1700000001},
	}
	sug3 := RetainSuggest(files3)
	if sug3.Confidence != "low" {
		t.Fatalf("同质文件应 low 置信，实际 %s（%v）", sug3.Confidence, sug3.Scores)
	}
	if len(sug3.Alternates) == 0 {
		t.Fatal("low 置信应给出 alternates")
	}
}
