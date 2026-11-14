// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"testing"
	"time"
)

// TestFeedJobsStopsOnCancel 回归 v3.17.4 F-04：
// worker 在 ctx 取消后直接 return，不再接收任务；feeder 必须跟着退出，
// 否则无缓冲通道的发送会永久阻塞，HTTP handler 的 goroutine 随之泄漏。
func TestFeedJobsStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch := make(chan TrimJob) // 无缓冲且故意没有接收者
	done := make(chan struct{})
	go func() {
		feedJobs(ctx, ch, []TrimJob{{Path: "a"}, {Path: "b"}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 feeder 仍阻塞，会导致 handler goroutine 泄漏")
	}
}
