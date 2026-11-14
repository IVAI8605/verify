// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import "testing"

// TestPathUnderAnyCleansRelativeSegments 验证保护/排除目录前缀匹配会折叠 ./..。
// v3.10.9：配置里 `/vol1/protect/../keep` 这类手输路径若不折叠，
// 与扫描产物的 `/vol1/keep/...` 永远匹配不上，保护会静默失效。
func TestPathUnderAnyCleansRelativeSegments(t *testing.T) {
	roots := []string{"/vol1/protect/../keep", "/vol1/media", "", "   "}
	cases := map[string]bool{
		"/vol1/keep/a.mkv":  true,
		"/vol1/keep":        true,
		"/vol1/keep2/a.mkv": false,
		"/vol1/media/x":     true,
		"/vol2/keep/a.mkv":  false,
	}
	for p, want := range cases {
		if got := pathUnderAny(p, roots); got != want {
			t.Fatalf("pathUnderAny(%q) = %v，期望 %v", p, got, want)
		}
	}
}
