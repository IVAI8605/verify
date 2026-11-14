// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package video

import (
	"strings"
	"verify/internal/applog"
)

// friendlyFFmpegError 返回“中文描述（英文原错误）”格式，既让用户看懂，又保留排查线索。
// 完整 stderr 仍由 logFFmpegStderr 写入日志。
func friendlyFFmpegError(stderr string) string {
	cn := chineseFFmpegReason(stderr)
	raw := firstErrorLine(stderr)
	if raw == "" {
		return cn
	}
	return cn + "（" + truncate(raw, 160) + "）"
}

func chineseFFmpegReason(stderr string) string {
	s := strings.ToLower(stderr)
	switch {
	case strings.TrimSpace(s) == "":
		return "视频解码失败"
	case strings.Contains(s, "no such file or directory"):
		return "文件不存在或已被移动"
	case strings.Contains(s, "permission denied"):
		return "没有读取权限"
	case strings.Contains(s, "moov atom not found"):
		return "视频文件不完整（缺少索引）"
	case strings.Contains(s, "invalid data found"):
		return "文件损坏或格式不支持"
	case strings.Contains(s, "does not contain any stream"):
		return "文件中没有可用的视频流"
	case strings.Contains(s, "hwaccel") || strings.Contains(s, "qsv") ||
		strings.Contains(s, "vaapi") || strings.Contains(s, "cuda") ||
		strings.Contains(s, "d3d11") || strings.Contains(s, "render"):
		return "硬件解码初始化失败，已回退软解"
	case strings.Contains(s, "timed out") || strings.Contains(s, "timeout"):
		return "视频读取超时"
	default:
		return "视频解码失败"
	}
}

func firstErrorLine(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// logFFmpegStderr 只把原始英文输出留在日志里，方便排查编码/驱动问题。
func logFFmpegStderr(action, path, stderr string) {
	raw := strings.TrimSpace(stderr)
	if raw == "" {
		return
	}
	applog.Warn("%s失败：%s，ffmpeg 原始输出：%s", action, path, truncate(raw, 500))
}
