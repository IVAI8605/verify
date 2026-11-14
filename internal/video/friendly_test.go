// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package video

import (
	"strings"
	"testing"
)

// TestFriendlyFFmpegErrorKeepsOriginal 确认中文为主、括号内保留英文原文。
func TestFriendlyFFmpegErrorKeepsOriginal(t *testing.T) {
	cases := []struct {
		stderr string
		cn     string
		raw    string
	}{
		{"No such file or directory", "文件不存在或已被移动", "No such file or directory"},
		{"Invalid data found when processing input", "文件损坏或格式不支持", "Invalid data found"},
		{"Error initializing qsv device", "硬件解码初始化失败，已回退软解", "Error initializing qsv device"},
	}
	for _, c := range cases {
		got := friendlyFFmpegError(c.stderr)
		if !strings.HasPrefix(got, c.cn+"（") || !strings.Contains(got, c.raw) || !strings.HasSuffix(got, "）") {
			t.Fatalf("格式错误: got=%q", got)
		}
	}
	if got := friendlyFFmpegError("   \n"); got != "视频解码失败" {
		t.Fatalf("空 stderr 不应加括号: %q", got)
	}
}
