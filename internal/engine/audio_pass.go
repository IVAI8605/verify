// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"verify/internal/apperr"
	"verify/internal/applog"
	"verify/internal/config"
	"verify/internal/store"
)

// RunAudioPass 对纯音频文件计算频谱指纹（设计文档 8.1）。
// 与视频 pass 同构：候选分批 + worker 池 + 批量落库；每批前 DiskMonitor 让路。
// 音频短于约 0.5 秒或静音 → 记 video_error（字段复用），与视频失败同进失败列表。
func (e *Engine) RunAudioPass(ctx context.Context, cfg *config.Config) {
	if !cfg.AudioEnabled {
		return
	}
	e.RunnerReload()
	if !e.RunnerSnapshot().Available() {
		e.updateState(func(s *State) { s.Message = "未找到 ffmpeg/ffprobe，已跳过音频分析" })
		e.publishProgress(true)
		return
	}
	// v1.42.0：统计失败必须暴露。此前 `total, _ :=` 把 SQL 错误吞掉，
	// 重复音频功能静默跳过且界面上看不出任何异常（BUG-02）。
	total, err := e.st.CountAudioCandidates(ctx)
	if err != nil {
		applog.Error("统计待分析音频失败：%s", apperr.Format(err))
		e.fail(fmt.Errorf("统计待分析音频失败：%s", apperr.Format(err)))
		return
	}
	if total == 0 {
		return
	}
	e.updateState(func(s *State) {
		s.Phase = "audio"
		s.Total = total
		s.Done = 0
		s.Message = "正在分析音频指纹…"
	})
	e.publishProgress(true)

	var done int64
	var mu sync.Mutex

	for {
		if ctx.Err() != nil {
			break
		}
		cands, err := e.st.AudioCandidates(ctx, 40)
		if err != nil {
			e.fail(err)
			return
		}
		if len(cands) == 0 {
			break
		}
		if e.diskBusy.Load() {
			e.updateState(func(s *State) { s.Message = "磁盘占用高，音频分析让路…" })
			e.publishProgress(true)
			select {
			case <-ctx.Done():
				return
			case <-time.After(imgBatchWait):
			}
			continue
		}

		workers := cfg.VideoWorkers
		if workers < 1 || workers > 4 {
			workers = 2
		}
		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup
		batch := make([]store.AudioResult, 0, len(cands))
		var bmu sync.Mutex
	outerAudio:
		for _, f := range cands {
			if ctx.Err() != nil {
				break
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				break outerAudio
			}
			wg.Add(1)
			go func(f store.FileRow) {
				defer wg.Done()
				defer func() { <-sem }()
				runner := e.RunnerSnapshot()
				audio, aerr := runner.FingerprintAudio(ctx, f.Path, 0, audioWindow(cfg))
				// v3.10.10（P2-7）：分组要按时长拦掉「恒定频谱但不同时长」的误组，
				// 纯音频此前从未写过 duration。探测失败不影响指纹落库，只是不参与时长约束。
				var dur float64
				if info, perr := runner.ProbeInfo(ctx, f.Path); perr == nil {
					dur = info.Duration
				}
				mu.Lock()
				done++
				e.updateState(func(s *State) { s.Done = done })
				mu.Unlock()
				res := store.AudioResult{ID: f.ID, Duration: dur}
				switch {
				case aerr != nil:
					res.Err = "无法解析音频：" + aerr.Error()
				case audio == nil:
					res.Err = "音频过短或静音，无法计算指纹"
				default:
					res.Audio = audio
				}
				bmu.Lock()
				batch = append(batch, res)
				bmu.Unlock()
			}(f)
		}
		wg.Wait()
		if len(batch) > 0 {
			if err := e.st.SetAudioBatch(ctx, batch); err != nil {
				e.fail(err)
				return
			}
		}
	}
	// 取消时不要把进度补到 total：那会让人以为这一阶段正常跑完了。
	if ctx.Err() != nil {
		return
	}
	// v1.42.0：音频 pass 修复后第一次真正运行，结束时补齐进度并通知结果页刷新，
	// 否则阶段条会停在最后一次批次的 done 上（此前功能静默跳过，没人发现）。
	e.updateState(func(s *State) { s.Done = total })
	e.bumpVersion()
	e.publishProgress(true)
	applog.Info("音频指纹分析完成：%d 个文件", total)
}

// audioWindow 返回纯音频比较时长（v2.3.0）：0 表示全片；已由 config 归一为 15/120/0。
func audioWindow(cfg *config.Config) int {
	return cfg.AudioWindowSeconds
}
