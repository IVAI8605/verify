// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"testing"
	"time"
)

// waitGateCond 轮询等待门状态满足条件，避免测试里写死 sleep 时长。
func waitGateCond(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("等待门状态超时")
}

// TestFFmpegGateMutualExclusion 覆盖 v3.12.0 决策 B：一方在跑时另一方必须等待，释放后立刻能进。
func TestFFmpegGateMutualExclusion(t *testing.T) {
	g := newFFmpegGate()
	ctx := context.Background()

	if !g.acquire(ctx, true) {
		t.Fatal("空门应能进入 ffmpeg 任务")
	}
	entered := make(chan bool, 1)
	go func() { entered <- g.acquire(ctx, false) }()
	select {
	case ok := <-entered:
		t.Fatalf("ffmpeg 在跑时健康探测不应进入，ok=%v", ok)
	case <-time.After(30 * time.Millisecond):
	}
	g.release(true)
	select {
	case ok := <-entered:
		if !ok {
			t.Fatal("ffmpeg 释放后健康探测应进入")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ffmpeg 释放后健康探测仍在等")
	}
	g.release(false)

	// 反向：健康探测在跑时 ffmpeg 等待
	if !g.acquire(ctx, false) {
		t.Fatal("空门应能进入健康探测")
	}
	ff := make(chan bool, 1)
	go func() { ff <- g.acquire(ctx, true) }()
	select {
	case ok := <-ff:
		t.Fatalf("健康探测在跑时 ffmpeg 不应进入，ok=%v", ok)
	case <-time.After(30 * time.Millisecond):
	}
	g.release(false)
	select {
	case ok := <-ff:
		if !ok {
			t.Fatal("健康释放后 ffmpeg 应进入")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("健康释放后 ffmpeg 仍在等")
	}
	g.release(true)
}

// TestFFmpegGateHealthYields 覆盖优先级：ffmpeg 有等待者时，新健康探测不再抢入口，
// 否则多路探测 worker 会反复抢回门，把用户发起的视频分析无限期压住。
func TestFFmpegGateHealthYields(t *testing.T) {
	g := newFFmpegGate()
	ctx := context.Background()
	if !g.acquire(ctx, false) {
		t.Fatal("健康探测应进入")
	}

	ff := make(chan bool, 1)
	go func() { ff <- g.acquire(ctx, true) }()
	waitGateCond(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.waitFF == 1
	})

	second := make(chan bool, 1)
	go func() { second <- g.acquire(ctx, false) }()
	waitGateCond(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.waitH == 1
	})

	g.release(false) // 现有健康探测结束，应让给 ffmpeg
	select {
	case ok := <-ff:
		if !ok {
			t.Fatal("ffmpeg 应进入")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ffmpeg 未在健康探测释放后进入")
	}
	select {
	case ok := <-second:
		t.Fatalf("有 ffmpeg 等待时新健康探测不应进入，ok=%v", ok)
	case <-time.After(30 * time.Millisecond):
	}
	g.release(true)
	select {
	case ok := <-second:
		if !ok {
			t.Fatal("ffmpeg 释放后健康探测应进入")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ffmpeg 释放后健康探测仍在等")
	}
	g.release(false)
}

// TestFFmpegGateCancel 验证 ctx 取消时等待者退出并返回 false，且不会留下假忙状态。
func TestFFmpegGateCancel(t *testing.T) {
	g := newFFmpegGate()
	if !g.acquire(context.Background(), true) {
		t.Fatal("应能进入")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if g.acquire(ctx, false) {
		t.Fatal("ctx 到期应返回 false")
	}
	if !g.ffmpegBusy() {
		t.Fatal("ffmpeg 未释放前应报告忙")
	}
	g.release(true)
	if g.ffmpegBusy() {
		t.Fatal("释放后不应再报告忙")
	}
}
