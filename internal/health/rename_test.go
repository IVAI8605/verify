// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"path/filepath"
	"testing"
)

// TestBuildTargetExt 验证扩展名修正只替换最后一段扩展名。
func TestBuildTargetExt(t *testing.T) {
	cases := []struct {
		name    string
		suggest string
		want    string
		ok      bool
	}{
		{"photo.jpg", "png", "photo.png", true},
		{"VIDEO.MP4", "mp4", "VIDEO.mp4", true},
		{"video.mp4.avi", "mp4", "video.mp4.mp4", true},
		{"noext", "png", "noext.png", true},
		{"already.png", "png", "", false},
	}
	for _, c := range cases {
		got, ok := BuildTarget(KindExt, c.name, c.suggest, NameOptions{})
		if ok != c.ok || got != c.want {
			t.Fatalf("BuildTarget(%q→%q) = (%q,%v)，期望 (%q,%v)", c.name, c.suggest, got, ok, c.want, c.ok)
		}
	}
}

// TestBuildTargetExtWithoutSuggestion 验证无建议时不做任何改动。
func TestBuildTargetExtWithoutSuggestion(t *testing.T) {
	if got, ok := BuildTarget(KindExt, "a.bin", "", NameOptions{}); ok || got != "" {
		t.Fatalf("无建议扩展名时不应生成目标名，实际 (%q,%v)", got, ok)
	}
}

// TestBuildTargetName 验证文件名规范化作为目标名。
func TestBuildTargetName(t *testing.T) {
	got, ok := BuildTarget(KindName, " 报告___终稿 .PDF", "", NameOptions{})
	if !ok {
		t.Fatal("应生成目标名")
	}
	if got != "报告_终稿.pdf" {
		t.Fatalf("目标名 %q", got)
	}
}

// TestBuildTargetNameNoChange 验证无需改动时不返回目标名。
func TestBuildTargetNameNoChange(t *testing.T) {
	if _, ok := BuildTarget(KindName, "正常文件名.txt", "", NameOptions{}); ok {
		t.Fatal("合规文件名不应产生改动")
	}
}

// TestBuildTargetOtherKinds 验证空文件/损坏视频不做重命名。
func TestBuildTargetOtherKinds(t *testing.T) {
	for _, k := range []Kind{KindEmpty, KindBroken} {
		if _, ok := BuildTarget(k, "a.mp4", "mp4", NameOptions{}); ok {
			t.Fatalf("%s 不应支持重命名", k)
		}
	}
}

// TestUniquePath 验证目标存在时追加 _1、_2，绝不覆盖。
func TestUniquePath(t *testing.T) {
	dir := "/vol1/media"
	taken := map[string]bool{
		filepath.Join(dir, "a.png"):   true,
		filepath.Join(dir, "a_1.png"): true,
	}
	got := UniquePath(dir, "a.png", func(p string) bool { return taken[p] })
	if filepath.Base(got) != "a_2.png" {
		t.Fatalf("应追加到 a_2.png，实际 %q", filepath.Base(got))
	}
}

// TestUniquePathFree 验证目标空闲时原样返回。
func TestUniquePathFree(t *testing.T) {
	dir := "/vol1/media"
	got := UniquePath(dir, "b.png", func(string) bool { return false })
	if got != filepath.Join(dir, "b.png") {
		t.Fatalf("目标空闲时应原样返回，实际 %q", got)
	}
}

// TestUniquePathWithoutExt 验证无扩展名文件也能正确追加后缀。
func TestUniquePathWithoutExt(t *testing.T) {
	dir := "/vol1/media"
	taken := map[string]bool{filepath.Join(dir, "README"): true}
	got := UniquePath(dir, "README", func(p string) bool { return taken[p] })
	if filepath.Base(got) != "README_1" {
		t.Fatalf("应为 README_1，实际 %q", filepath.Base(got))
	}
}

// TestItoa 验证极简整数转换覆盖边界。
func TestItoa(t *testing.T) {
	cases := map[int]string{0: "0", 1: "1", 9: "9", 10: "10", 99: "99", 999: "999"}
	for n, want := range cases {
		if got := itoa(n); got != want {
			t.Fatalf("itoa(%d) = %q，期望 %q", n, got, want)
		}
	}
}
