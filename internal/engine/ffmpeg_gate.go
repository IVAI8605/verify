// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"sync"
)

// ffmpegGate 让「ffmpeg 类任务」与「损坏探测」互斥调度（v3.12.0）。
//
// 背景：heavySem 只限制总并发，管不住「已经跑起来的 ffmpeg 吃满 CPU」。
// 视频/片头/裁剪/转码任一在跑时，同机再起 ffmpeg 探测会被抢 CPU 而超时，
// 好文件因此被记成「疑似损坏」。这里的做法是让两方不同时开工：
// 一方在跑时，另一方在入口等待，而不是拿到槽位后再被饿到超时。
//
// 优先级：ffmpeg 类任务优先。损坏探测一旦发现有人在等 ffmpeg，就不再抢新文件，
// 避免「探测 worker 多、反复抢回入口」把用户发起的视频分析无限期压住。
type ffmpegGate struct {
	mu      sync.Mutex
	changed chan struct{} // 状态变化时关闭并重建，用来唤醒所有等待者
	ffmpeg  int           // 正在运行的 ffmpeg 类任务数
	health  int           // 正在运行的损坏探测数
	waitFF  int           // 等待进入的 ffmpeg 类任务数
	waitH   int           // 等待进入的损坏探测数
}

func newFFmpegGate() *ffmpegGate {
	return &ffmpegGate{changed: make(chan struct{})}
}

// notifyLocked 唤醒所有等待者；调用方必须持有 mu。
func (g *ffmpegGate) notifyLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

// acquire 申请进入；isFF 为 true 表示 ffmpeg 类任务。
// 返回 false 表示 ctx 已取消。
func (g *ffmpegGate) acquire(ctx context.Context, isFF bool) bool {
	g.mu.Lock()
	if isFF {
		g.waitFF++
	} else {
		g.waitH++
	}
	for {
		ok := false
		if isFF {
			ok = g.health == 0
		} else {
			// 有 ffmpeg 任务在等时，损坏探测让路，避免两边长期交替抢占。
			ok = g.ffmpeg == 0 && g.waitFF == 0
		}
		if ok {
			if isFF {
				g.waitFF--
				g.ffmpeg++
			} else {
				g.waitH--
				g.health++
			}
			g.mu.Unlock()
			return true
		}
		ch := g.changed
		g.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			g.mu.Lock()
			if isFF {
				g.waitFF--
			} else {
				g.waitH--
			}
			// 自己退出等待可能让别人满足条件，必须广播一次。
			g.notifyLocked()
			g.mu.Unlock()
			return false
		}
		g.mu.Lock()
	}
}

// release 退出一个名额；isFF 必须与 acquire 一致。
func (g *ffmpegGate) release(isFF bool) {
	g.mu.Lock()
	if isFF {
		if g.ffmpeg > 0 {
			g.ffmpeg--
		}
	} else if g.health > 0 {
		g.health--
	}
	g.notifyLocked()
	g.mu.Unlock()
}

// ffmpegBusy 报告是否有 ffmpeg 类任务在跑（/api/status 的 ffmpeg_running）。
func (g *ffmpegGate) ffmpegBusy() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ffmpeg > 0
}

// acquireFFmpegTask 供视频/片头/裁剪/转码在调 ffmpeg 前调用。
func (e *Engine) acquireFFmpegTask(ctx context.Context) bool {
	if e.ffGate == nil {
		return true
	}
	return e.ffGate.acquire(ctx, true)
}

func (e *Engine) releaseFFmpegTask() {
	if e.ffGate == nil {
		return
	}
	e.ffGate.release(true)
}

// acquireHealthProbe 供损坏探测在调 ffmpeg 前调用。
func (e *Engine) acquireHealthProbe(ctx context.Context) bool {
	if e.ffGate == nil {
		return true
	}
	return e.ffGate.acquire(ctx, false)
}

func (e *Engine) releaseHealthProbe() {
	if e.ffGate == nil {
		return
	}
	e.ffGate.release(false)
}

// FFmpegBusy 报告是否有 ffmpeg 类任务正在运行，供状态接口与界面二次确认使用。
func (e *Engine) FFmpegBusy() bool {
	if e.ffGate == nil {
		return false
	}
	return e.ffGate.ffmpegBusy()
}

// ThumbGate 为缩略图生成申请资源名额（v3.25.1）。
//
// 结果页滚动会连续触发几十个 /api/thumb，每次都是一次 ffmpeg 解码。
// 旧实现完全绕过 heavySem 与 ffmpegGate：既和转码/视频分析抢 CPU，
// 又让同时开始的损坏探测看不到「有 ffmpeg 在跑」，被饿到超时后把好文件记成疑似损坏。
// 返回的释放函数必须由调用方 defer 执行；ok=false 表示等待期间 ctx 已结束。
func (e *Engine) ThumbGate(ctx context.Context) (release func(), ok bool) {
	if !e.acquireHeavy(ctx) {
		return func() {}, false
	}
	if !e.acquireFFmpegTask(ctx) {
		e.releaseHeavy()
		return func() {}, false
	}
	return func() {
		e.releaseFFmpegTask()
		e.releaseHeavy()
	}, true
}
