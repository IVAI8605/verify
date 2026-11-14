// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"testing"
)

// TestRetainWeightsFlipSuggestion 验证权重变化确实会改变保留建议：
// 默认权重偏向画质，路径优先权重会改为保留路径更浅的那份。
func TestRetainWeightsFlipSuggestion(t *testing.T) {
	files := []RetainFile{
		{Path: "/vol6/a/b/c/d/e/f.mp4", Name: "高清.mp4", Width: 1920, Height: 1080, BitRate: 5_000_000, Mtime: 1},
		{Path: "/vol6/b.mp4", Name: "普通.mp4", Width: 640, Height: 360, BitRate: 500_000, Mtime: 1},
	}
	defer func() {
		retainLearn.mu.Lock()
		retainLearn.enabled = false
		retainLearn.weights = RetainWeights{}
		retainLearn.mu.Unlock()
	}()

	quality := RetainSuggest(files)
	retainLearn.mu.Lock()
	retainLearn.enabled = true
	retainLearn.weights = RetainWeights{Quality: 0.15, Integrity: 0.30, PathTidy: 0.40, NameQ: 0.10, Fresh: 0.05}
	retainLearn.mu.Unlock()
	pathFirst := RetainSuggest(files)

	if quality.Retain == pathFirst.Retain {
		t.Fatalf("权重切换后建议未变化，都是 %s", quality.Retain)
	}
	if quality.Retain != files[0].Path {
		t.Fatalf("默认权重应保留画质更好的 %s，得到 %s", files[0].Path, quality.Retain)
	}
	if pathFirst.Retain != files[1].Path {
		t.Fatalf("路径优先权重应保留路径更浅的 %s，得到 %s", files[1].Path, pathFirst.Retain)
	}
}

// TestDefaultRetainWeights 默认权重总和为 1，且与全局开关一致。
func TestDefaultRetainWeights(t *testing.T) {
	w := defaultRetainWeights()
	sum := w.Quality + w.Integrity + w.PathTidy + w.NameQ + w.Fresh
	if sum < 0.99 || sum > 1.01 {
		t.Fatalf("默认权重总和 = %.3f，want 1", sum)
	}
	if w.Quality != 0.45 || w.PathTidy != 0.10 {
		t.Fatalf("质量优先默认权重 = %.2f/%.2f，want 0.45/0.10", w.Quality, w.PathTidy)
	}
}

// TestRetainPrefix 前缀取前两段，便于展示。
func TestRetainPrefix(t *testing.T) {
	if got := retainPrefix("/vol6/1000/media/sub/x.mp4"); got != "/vol6/1000" {
		t.Fatalf("retainPrefix = %s，want /vol6/1000", got)
	}
	if got := retainPrefix(""); got != "" {
		t.Fatalf("空路径前缀 = %q，want 空", got)
	}
}
