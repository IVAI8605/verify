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

// TestUnderRootsEmptyMeansAll 守住 underRoots 的关键语义：空 roots=true，
// 且非空时与 pathUnderAny 一致（含目录本身、排除同前缀兄弟目录）。
func TestUnderRootsEmptyMeansAll(t *testing.T) {
	if !underRoots("/any/path.mkv", nil) {
		t.Fatal("空 roots 应视为不限（true）")
	}
	if underRoots("/any/path.mkv", []string{"", "   "}) {
		t.Fatal("len(roots)>0 但全是空白项时应无匹配（false），与原语义一致")
	}
	roots := []string{"/vol1/keep", "/vol5/1000/x"}
	cases := map[string]bool{
		"/vol1/keep":         true,
		"/vol1/keep/a.mkv":   true,
		"/vol1/keep2/a.mkv":  false,
		"/vol5/1000/x/b.mp4": true,
		"/vol2/keep/a.mkv":   false,
	}
	for p, want := range cases {
		if got := underRoots(p, roots); got != want {
			t.Fatalf("underRoots(%q)=%v，期望 %v", p, got, want)
		}
	}
}
