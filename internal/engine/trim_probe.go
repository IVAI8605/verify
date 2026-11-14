// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"

	"verify/internal/applog"
	"verify/internal/phash"
	"verify/internal/taskguard"
	"verify/internal/video"
)

// TrimProbeBoundary 二分探测组公共片头边界（v1.16 引入 16.6，P1）。
//
// 原理：公共片头长度 L 是单调边界——窗口起点 ≤ L−窗口长 的探测窗口完全落在
// 片头内（相似），起点 > L 已进正片（失配）。单调边界二分，5 秒窗口收敛后
// 1 秒窗口线性细化，最终精度 1 秒。223 秒片头 ≈ 每成员 12 次探测（线性要 200+）。
//
// 返回 (边界秒, 状态)：状态 ok / no_probe（成员不足）/ too_short（上限不足）/ failed。
// 探测只读，不写用户文件；结果由调用方落 trim_probe 缓存。
func (e *Engine) TrimProbeBoundary(ctx context.Context, paths []string, durations []float64, audioThr float64) (float64, string) {
	if !e.RunnerAvailable() {
		return 0, "failed"
	}
	// v3.25.0（P1-1）：二分探测会对每个成员反复起 ffmpeg，属于 ffmpeg 长任务。
	// 此前没登记闸门，转码/视频分析在跑时叠加上去会把 CPU 吃满。
	done, gerr := e.acquireTask(taskguard.KindTrim, "片头探测", taskguard.GroupFFmpeg, taskguard.ResCPU, taskguard.ResFFmpeg)
	if gerr != nil {
		applog.Warn("片头探测被拒绝：%v", gerr)
		return 0, "failed"
	}
	defer done("done")
	if len(paths) < 2 || len(paths) != len(durations) {
		return 0, "no_probe"
	}
	// 基准 = 时长最短成员（设计文档 16.6.1）
	baseIdx := 0
	for i, d := range durations {
		if d < durations[baseIdx] {
			baseIdx = i
		}
	}
	base := paths[baseIdx]
	high := 300.0
	for _, d := range durations {
		if v := d - 10; v < high {
			high = v
		}
	}
	const win = 5.0
	if high < win {
		return 0, "too_short"
	}
	relaxed := audioThr * 0.9 // 1 秒窗口动态段信息少，阈值放宽 10%（设计文档 16.6 健壮性）

	// simAt 判断 p 在 at 处 win 秒窗口与基准是否相似。
	// 提取失败按失配处理（边界前移，保守）。
	simAt := func(p string, at float64, seconds int, thr float64) bool {
		s1, err1 := e.RunnerSnapshot().ExtractAudio(ctx, base, at, seconds)
		s2, err2 := e.RunnerSnapshot().ExtractAudio(ctx, p, at, seconds)
		if err1 != nil || err2 != nil {
			return false
		}
		f1 := video.AudioFingerprint(s1, 8000)
		f2 := video.AudioFingerprint(s2, 8000)
		if len(f1) == 0 || len(f2) == 0 {
			return false
		}
		return phash.Cosine(f1, f2) >= thr
	}

	boundary := high
	for i, p := range paths {
		if i == baseIdx {
			continue
		}
		// 二分：找最后仍相似的窗口起点
		lo, hi := 0.0, high
		steps := 0
		for hi-lo > win && steps < 12 {
			mid := (lo + hi) / 2
			if simAt(p, mid, int(win), audioThr) {
				lo = mid
			} else {
				hi = mid
			}
			steps++
		}
		// 1 秒线性细化（最多 4 次，设计文档 16.6.3）：连续 2 个窗口失配才计数防噪声
		refined := lo
		mismatches := 0
		for t := lo; t+1 <= lo+win; t += 1 {
			if simAt(p, t, 1, relaxed) {
				refined = t + 1
				mismatches = 0
			} else {
				mismatches++
				if mismatches >= 2 {
					break
				}
			}
		}
		if refined < boundary {
			boundary = refined
		}
		if ctx.Err() != nil {
			return 0, "failed"
		}
	}
	if boundary < 1 {
		return 0, "failed"
	}
	return boundary, "ok"
}
