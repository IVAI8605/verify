// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestProbeCacheRoundTrip 验证探测缓存能写能读，且覆盖更新生效。
func TestProbeCacheRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	entries := []ProbeEntry{
		{Path: "/v/a.mp4", Size: 100, Mtime: 111, Status: ProbeOK},
		{Path: "/v/b.mp4", Size: 200, Mtime: 222, Status: ProbeBroken, Reason: "invalid_data", Detail: "解码报数据无效"},
	}
	if err := s.SaveProbeCache(ctx, entries); err != nil {
		t.Fatal(err)
	}

	cache, err := s.ProbeCache(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cache) != 2 {
		t.Fatalf("应有 2 条缓存，实际 %d", len(cache))
	}
	if cache["/v/a.mp4"].Status != ProbeOK {
		t.Fatalf("a.mp4 状态应为 ok，实际 %q", cache["/v/a.mp4"].Status)
	}
	if cache["/v/b.mp4"].Reason != "invalid_data" {
		t.Fatalf("b.mp4 原因不符: %+v", cache["/v/b.mp4"])
	}

	// 文件改动后重新探测：同路径覆盖，不应重复插入
	if err := s.SaveProbeCache(ctx, []ProbeEntry{{Path: "/v/b.mp4", Size: 333, Mtime: 444, Status: ProbeOK}}); err != nil {
		t.Fatal(err)
	}
	cache, _ = s.ProbeCache(ctx)
	if len(cache) != 2 {
		t.Fatalf("覆盖后仍应为 2 条，实际 %d", len(cache))
	}
	if cache["/v/b.mp4"].Status != ProbeOK || cache["/v/b.mp4"].Size != 333 {
		t.Fatalf("覆盖未生效: %+v", cache["/v/b.mp4"])
	}
}

// TestProbeStats 验证覆盖率统计区分健康与异常。
func TestProbeStats(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	if err := s.SaveProbeCache(ctx, []ProbeEntry{
		{Path: "/v/a.mp4", Status: ProbeOK},
		{Path: "/v/b.mp4", Status: ProbeOK},
		{Path: "/v/c.mp4", Status: ProbeBroken},
		{Path: "/v/d.mp4", Status: ProbeTimeout},
	}); err != nil {
		t.Fatal(err)
	}

	total, broken, err := s.ProbeStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || broken != 2 {
		t.Fatalf("统计应为 4/2，实际 %d/%d", total, broken)
	}
}

// TestMigrateProbeAlgo 验证规则版本变化时只作废非 OK 结论，且只作废一次。
func TestMigrateProbeAlgo(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	if err := s.SaveProbeCache(ctx, []ProbeEntry{
		{Path: "/v/ok.mp4", Status: ProbeOK},
		{Path: "/v/broken.mp4", Status: ProbeBroken, Reason: "invalid_data"},
		{Path: "/v/slow.mp4", Status: ProbeTimeout},
	}); err != nil {
		t.Fatal(err)
	}

	n, err := s.MigrateProbeAlgo(ctx, ProbeAlgoVersion)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应清理 2 条非 OK 缓存，实际 %d", n)
	}
	cache, _ := s.ProbeCache(ctx)
	if len(cache) != 1 || cache["/v/ok.mp4"].Status != ProbeOK {
		t.Fatalf("应只保留健康结论: %+v", cache)
	}

	// 同一版本再次启动不应重复清理（否则每次重启都要重解全部坏文件）
	n, err = s.MigrateProbeAlgo(ctx, ProbeAlgoVersion)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("同版本不应再清理，实际 %d", n)
	}
}

// TestPruneProbeCache 验证已删除文件的缓存会被清理。
func TestPruneProbeCache(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	if err := s.SaveProbeCache(ctx, []ProbeEntry{
		{Path: "/v/keep.mp4", Status: ProbeOK},
		{Path: "/v/gone.mp4", Status: ProbeOK},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneProbeCache(ctx, map[string]bool{"/v/keep.mp4": true})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应清理 1 条，实际 %d", n)
	}
	cache, _ := s.ProbeCache(ctx)
	if len(cache) != 1 {
		t.Fatalf("保留项不符: %+v", cache)
	}
	if cache["/v/keep.mp4"].Path != "/v/keep.mp4" {
		t.Fatalf("应保留 keep.mp4: %+v", cache)
	}
}
