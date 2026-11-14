// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
	"verify/internal/config"
	"verify/internal/health"
	"verify/internal/store"
)

// newHealthTestEngine 构造最小可用的引擎：只要 store、config 与事件总线。
func newHealthTestEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return New(st, cfg, NewBus(), dir), st
}

// waitHealth 轮询等待检测结束，避免每个用例都写一遍超时逻辑。
func waitHealth(t *testing.T, e *Engine) HealthProgress {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if p := e.HealthState(); p.Status != HealthRunning {
			return p
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("检测超时未结束，当前状态 %+v", e.HealthState())
	return HealthProgress{}
}

// TestHealthScanEmptyFiles 覆盖零字节检测的完整链路：遍历、判定、落库。
func TestHealthScanEmptyFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "empty.bin"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "full.bin"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	e, st := newHealthTestEngine(t)
	if err := e.StartHealth(health.KindEmpty, HealthOptions{Roots: []string{root}, Workers: 2}); err != nil {
		t.Fatal(err)
	}
	p := waitHealth(t, e)
	if p.Status != HealthDone {
		t.Fatalf("期望 done，实际 %+v", p)
	}
	if p.Total != 2 || p.Processed != 2 {
		t.Fatalf("处理数量不符：%+v", p)
	}

	items, err := st.ListHealthResults(context.Background(), string(health.KindEmpty), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || filepath.Base(items[0].Path) != "empty.bin" {
		t.Fatalf("应只命中 empty.bin，实际 %+v", items)
	}
	if items[0].Reason != health.ReasonZeroSize {
		t.Fatalf("原因应为 zero_size，实际 %q", items[0].Reason)
	}
}

// TestHealthScanExcludedDir 验证排除目录不会被扫进结果。
func TestHealthScanExcludedDir(t *testing.T) {
	root := t.TempDir()
	excluded := filepath.Join(root, "skipme")
	if err := os.MkdirAll(excluded, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(excluded, "empty.bin"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	e, st := newHealthTestEngine(t)
	cfg := e.cfg.Get()
	cfg.ExcludePaths = []string{excluded}
	if err := e.cfg.Replace(cfg); err != nil {
		t.Fatal(err)
	}

	if err := e.StartHealth(health.KindEmpty, HealthOptions{Roots: []string{root}, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	if p := waitHealth(t, e); p.Status != HealthDone {
		t.Fatalf("期望 done，实际 %+v", p)
	}
	if n, err := st.CountHealthResults(context.Background(), string(health.KindEmpty)); err != nil || n != 0 {
		t.Fatalf("排除目录内的文件不应命中，实际 %d（err=%v）", n, err)
	}
}

// TestHealthScanConcurrentGuard 验证同一时刻只允许一个检测任务。
// 直接构造"运行中"状态，避免依赖扫描速度导致用例不稳定。
func TestHealthScanConcurrentGuard(t *testing.T) {
	e, _ := newHealthTestEngine(t)

	e.hs.mu.Lock()
	e.hs.progress = HealthProgress{Kind: string(health.KindEmpty), Status: HealthRunning}
	e.hs.mu.Unlock()

	if err := e.StartHealth(health.KindName, HealthOptions{}); err == nil {
		t.Fatal("已有任务在跑时，第二次启动应被拒绝")
	}

	e.hs.mu.Lock()
	e.hs.progress = HealthProgress{}
	e.hs.mu.Unlock()
	if e.HealthRunning() {
		t.Fatal("重置后不应仍处于运行中")
	}
}

// TestHealthScanInvalidKind 验证未知类型直接报错，不会启动空任务。
func TestHealthScanInvalidKind(t *testing.T) {
	e, _ := newHealthTestEngine(t)
	if err := e.StartHealth(health.Kind("nope"), HealthOptions{}); err == nil {
		t.Fatal("未知类型应返回错误")
	}
}

// TestHealthScanAllKinds 覆盖 v3.9.3 的“一次扫描全部类型”：
// 同一轮遍历里应同时产出空文件、文件名不规范与空目录三类结果。
func TestHealthScanAllKinds(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "empty.bin"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, " leading.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "emptydir"), 0o750); err != nil {
		t.Fatal(err)
	}

	e, st := newHealthTestEngine(t)
	if err := e.StartHealth(health.KindAll, HealthOptions{Roots: []string{root}, Workers: 2}); err != nil {
		t.Fatal(err)
	}
	p := waitHealth(t, e)
	if p.Status != HealthDone {
		t.Fatalf("期望 done，实际 %+v", p)
	}
	if p.Kind != string(health.KindAll) {
		t.Fatalf("进度类型应为 all，实际 %q", p.Kind)
	}
	// 2 个文件 + 1 个空目录
	if p.Total != 3 || p.Processed != 3 {
		t.Fatalf("处理数量不符：%+v", p)
	}

	ctx := context.Background()
	if items, err := st.ListHealthResults(ctx, string(health.KindEmpty), 10, 0); err != nil || len(items) != 1 {
		t.Fatalf("空文件应命中 1 个，实际 %d（err=%v）", len(items), err)
	}
	if items, err := st.ListHealthResults(ctx, string(health.KindName), 10, 0); err != nil || len(items) != 1 {
		t.Fatalf("文件名不规范应命中 1 个，实际 %d（err=%v）", len(items), err)
	}
	if items, err := st.ListHealthResults(ctx, string(health.KindEmptyDir), 10, 0); err != nil || len(items) != 1 {
		t.Fatalf("空目录应命中 1 个，实际 %d（err=%v）", len(items), err)
	}
}
