// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"runtime"
)

// heavySlots 返回全进程并发重任务单元的上限（v3.10.10，第七轮 P1-5）。
//
// 扫描 4 路哈希、视频/片头 2 路 ffmpeg（内部还多线程）、健康探测 8 路、改名 4 路
// 各自有 worker 池，最坏叠加 20+ 个任务同时抢 CPU/磁盘。这里给所有重任务
// 一个共享槽位，总量压到 CPU 核数（2–8，NAS 4 核即 4）。
func heavySlots() int {
	n := runtime.NumCPU()
	if n < 2 {
		n = 2
	}
	if n > 8 {
		n = 8
	}
	return n
}

// acquireHeavy 申请一个重任务槽位，返回 false 表示 ctx 已取消。
// 调用方必须在拿到槽位后 defer releaseHeavy。
func (e *Engine) acquireHeavy(ctx context.Context) bool {
	if e.heavySem == nil {
		// 测试里可能直接构造 Engine（不走 New），此时不做限流而不是永久阻塞
		return true
	}
	select {
	case e.heavySem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *Engine) releaseHeavy() {
	if e.heavySem == nil {
		return
	}
	<-e.heavySem
}
