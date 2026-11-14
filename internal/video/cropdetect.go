// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package video

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

// F6 黑边检测（v1.39 引入）：ffmpeg cropdetect 在片头/片中/片尾多点采样，
// 取众数有效区尺寸 → 长宽比归类横屏/竖屏/方形。只打标签，不转码（用户决策）。

var cropRe = regexp.MustCompile(`crop=(\d+):(\d+)`)

// CropResult 有效区尺寸（像素）。
type CropResult struct {
	W, H int
}

// CropDetect 对视频做黑边检测：三个采样点各跑一小段 cropdetect，
// 取出现最多的 crop=w:h（众数防片头黑场/片尾字幕误判）。
func (r *Runner) CropDetect(ctx context.Context, path string, duration float64) (*CropResult, error) {
	if duration <= 0 {
		pi, err := r.ProbeInfo(ctx, path)
		if err != nil {
			return nil, err
		}
		duration = pi.Duration
	}
	if duration <= 0 {
		return nil, errNoDuration()
	}
	type key struct{ w, h int }
	counter := map[key]int{}
	for _, frac := range []float64{0.05, 0.5, 0.95} {
		at := duration * frac
		if at < 0.5 {
			at = 0.5
		}
		w, h, err := r.cropAt(ctx, path, at)
		if err != nil {
			continue // 单点失败不致命
		}
		counter[key{w, h}]++
	}
	if len(counter) == 0 {
		return nil, errCropFailed()
	}
	best := key{}
	bestN := -1
	for k, n := range counter {
		if n > bestN || (n == bestN && k.w*k.h > best.w*best.h) {
			best, bestN = k, n
		}
	}
	return &CropResult{W: best.w, H: best.h}, nil
}

// cropAt 单点采样：抽 3 帧 cropdetect，从 stderr 解析最后的 crop= 值。
func (r *Runner) cropAt(ctx context.Context, path string, at float64) (int, int, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"-v", "info", "-nostdin", "-ss", strconv.FormatFloat(at, 'f', 2, 64),
		"-i", path, "-frames:v", "3",
		"-vf", "cropdetect=24:16:0", "-f", "null", "-"}
	out, err := r.runCapture(cctx, args)
	if err != nil {
		return 0, 0, err
	}
	w, h := 0, 0
	for _, m := range cropRe.FindAllStringSubmatch(string(out), -1) {
		w, _ = strconv.Atoi(m[1])
		h, _ = strconv.Atoi(m[2])
	}
	if w <= 0 || h <= 0 {
		return 0, 0, errCropFailed()
	}
	return w, h, nil
}

// ClassifyCrop 按有效区宽高比归类：>1.05 横屏、<0.95 竖屏、之间方形。
func ClassifyCrop(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	ratio := float64(w) / float64(h)
	switch {
	case ratio > 1.05:
		return "landscape"
	case ratio < 0.95:
		return "portrait"
	default:
		return "square"
	}
}

// CropLabelClass 中文标签。
func CropLabelClass(label string) string {
	switch label {
	case "landscape":
		return "横屏"
	case "portrait":
		return "竖屏"
	case "square":
		return "方形"
	}
	return ""
}

// runCapture 跑 ffmpeg 并捕获 stderr（cropdetect 结果打在 info 日志流）。
func (r *Runner) runCapture(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.FFmpeg, args...) // #nosec G204 -- 可执行文件为探测到的 ffmpeg 绝对路径，参数为常量
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stderr.Bytes(), nil
}

func errNoDuration() error { return &cropErr{"无法获取视频时长"} }
func errCropFailed() error { return &cropErr{"cropdetect 未输出有效区域"} }

type cropErr struct{ msg string }

func (e *cropErr) Error() string { return e.msg }
