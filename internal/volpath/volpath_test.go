// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package volpath

import "testing"

// TestIsVolRoot 验证 /vol 前缀按卷名边界判断（沿用 P2-15 的口径）。
func TestIsVolRoot(t *testing.T) {
	cases := map[string]bool{
		"/vol1":         true,
		"/vol12/1000":   true,
		"/volumes":      false,
		"/vol":          false,
		"/vol1abc":      false,
		"/vol1/../etc":  true, // 本函数只判前缀；配置校验会先 Clean 消解 ..
		"/tmp/vol1":     false,
		"relative/vol1": false,
		"/vol1/.verify": true,
		"":              false,
	}
	for p, want := range cases {
		if got := IsVolRoot(p); got != want {
			t.Fatalf("IsVolRoot(%q) = %v，期望 %v", p, got, want)
		}
	}
}

// TestIsCleanVolRoot 验证带 ".." 与非卷根路径在规范形态下被拒（F-02）。
func TestIsCleanVolRoot(t *testing.T) {
	cases := map[string]bool{
		"/vol1/1000/media":    true,
		"  /vol1/1000/media ": true,
		"/vol1/../etc":        false,
		"/vol1/../../etc":     false,
		"/etc":                false,
		"relative":            false,
	}
	for p, want := range cases {
		if got := IsCleanVolRoot(p); got != want {
			t.Fatalf("IsCleanVolRoot(%q) = %v，期望 %v", p, got, want)
		}
	}
}
