// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"

	"verify/internal/applog"
	"verify/internal/store"
	"verify/internal/taskguard"
)

// CropLabelBatch 对最多 limit 个视频跑 cropdetect 并落库标签（横屏/竖屏/方形）。
// 单文件失败记日志继续；返回成功打标的数量与剩余待检测数量（v3.19.0）。
// 剩余数让前端能画出准确进度并判断何时收工，不必靠「再点一次试试」。
func (e *Engine) CropLabelBatch(ctx context.Context, limit int) (int, int, error) {
	// v3.18.0：cropdetect 吃解码资源，与扫描 / 转码 / 健康检测互斥。
	doneGuard, gerr := e.acquireTask(taskguard.KindCrop, "", taskguard.GroupFFmpeg,
		taskguard.ResCPU, taskguard.ResDiskIO, taskguard.ResFFmpeg)
	if gerr != nil {
		return 0, 0, gerr
	}
	defer doneGuard("done")
	// v3.19.0：接入 ffmpegGate，与损坏探测错峰，避免两边同时抢解码器导致探测超时。
	// 优先级沿用闸门约定：ffmpeg 类任务优先，损坏探测让路。
	if !e.acquireFFmpegTask(ctx) {
		return 0, 0, ctx.Err()
	}
	defer e.releaseFFmpegTask()
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cands, err := e.st.CropCandidates(ctx, limit)
	if err != nil {
		return 0, 0, err
	}
	var entries []store.CropEntry
	labeled := 0
	for _, c := range cands {
		if ctx.Err() != nil {
			break
		}
		cr, err := e.RunnerSnapshot().CropDetect(ctx, c.Path, 0)
		if err != nil {
			applog.Warn("黑边检测失败（%s）：%v", c.Path, err)
			continue
		}
		entries = append(entries, store.CropEntry{ID: c.ID, W: cr.W, H: cr.H})
		labeled++
	}
	if len(entries) > 0 {
		if err := e.st.SetCropLabels(ctx, entries); err != nil {
			return labeled, 0, err
		}
	}
	// 剩余数在打标之后统计：前端拿到的「已处理 + 剩余」才是完整的进度分母。
	remaining, err := e.st.CountCropCandidates(ctx)
	if err != nil {
		return labeled, 0, err
	}
	return labeled, remaining, nil
}

// v3.9.10：原 CropLabel 已删除——它只是 video.CropLabelClass 的同名转发，
// 全项目零调用，前端取标签直接走 video 包即可。
