// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestExcludedByPath 验证排除目录按完整路径前缀匹配，而不是把相似目录名也算进去。
func TestExcludedByPath(t *testing.T) {
	cases := []struct {
		path  string
		roots []string
		want  bool
	}{
		{"/vol1/media/a.mkv", []string{"/vol1/media"}, true},
		{"/vol1/media", []string{"/vol1/media/"}, true},
		{"/vol1/media/sub/a.mkv", []string{"/vol1/media/"}, true},
		{"/vol1/mediax/a.mkv", []string{"/vol1/media"}, false},
		{"/vol1/media/a.mkv", nil, false},
		{"/vol1/media/a.mkv", []string{" "}, false},
	}
	for _, c := range cases {
		if got := excludedByPath(c.path, c.roots); got != c.want {
			t.Fatalf("excludedByPath(%q, %v) = %v，期望 %v", c.path, c.roots, got, c.want)
		}
	}
}

// TestVolumeOfPure 卷前缀匹配的目录边界（media 不得误配 media2）。
func TestVolumeOfPure(t *testing.T) {
	vols := []string{"/vol1", "/vol1/media"}
	if got := volumeOf("/vol1/media2/x.jpg", vols); got != "/vol1" {
		t.Fatalf("media2 应落到宽泛卷 /vol1，实际 %q", got)
	}
	if got := volumeOf("/vol1/media/x.jpg", vols); got != "/vol1/media" {
		t.Fatalf("最长前缀优先，实际 %q", got)
	}
}

// TestResolveManagedPath 路径穿越双向量回归（审查发现 #1，2026-11-01 真机确认可利用）：
// 1) `..` 遍历：原始串带受管卷前缀但 Clean 后逃逸；
// 2) 软链接：卷内软链接指向卷外文件。
// 两者都必须被 resolveManagedPath 拒绝。
func TestResolveManagedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("软链接与 POSIX 路径语义需 CI(linux) 验证")
	}
	root := t.TempDir()    // 受管卷
	outside := t.TempDir() // 卷外

	if err := os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "deep", "f.txt"), []byte("in"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("out"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 卷内软链接指向卷外文件（软链接逃逸向量）
	if err := os.Symlink(secret, filepath.Join(root, "link_out.txt")); err != nil {
		t.Fatal(err)
	}
	// 卷内软链接指向卷内文件（合法场景，应放行）
	if err := os.Symlink(filepath.Join(root, "sub", "deep", "f.txt"), filepath.Join(root, "link_in.txt")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"卷内普通文件", filepath.Join(root, "sub", "deep", "f.txt"), true},
		{"卷内不存在的文件", filepath.Join(root, "missing.txt"), false},
		{"原始串在卷外", filepath.Join(outside, "secret.txt"), false},
		{".. 遍历逃逸（原始串带卷前缀）", filepath.Join(root, "sub", "deep", "..", "..", "..", filepath.Base(outside), "secret.txt"), false},
		{"多重 .. 逃逸到根外", root + "/sub/deep/../../../../etc/passwd", false},
		{".. 消解后仍在卷内且存在", filepath.Join(root, "sub", "deep", "..", "..", "sub", "deep", "f.txt"), true},
		{"软链接指向卷外", filepath.Join(root, "link_out.txt"), false},
		{"软链接指向卷内", filepath.Join(root, "link_in.txt"), true},
		{"软链接指向不存在目标", filepath.Join(root, "dead"), false},
		{"空路径", "", false},
	}
	for _, c := range cases {
		got, ok := resolveManagedPath(c.path, []string{root})
		if ok != c.want {
			t.Errorf("%s: resolveManagedPath(%q) 允许=%v，期望 %v", c.name, c.path, ok, c.want)
		}
		if ok && got == "" {
			t.Errorf("%s: 允许时应返回解析路径", c.name)
		}
	}
	// 逃逸向量的解析结果绝不能落在卷外
	for _, p := range []string{
		filepath.Join(root, "sub", "deep", "..", "..", "..", filepath.Base(outside), "secret.txt"),
		filepath.Join(root, "link_out.txt"),
	} {
		if resolved, ok := resolveManagedPath(p, []string{root}); ok && !within(resolved, root) {
			t.Errorf("路径 %q 解析到卷外 %q 却被放行", p, resolved)
		}
	}
}

// within 判断 resolved 是否位于 root 内（目录边界）。
func within(resolved, root string) bool {
	return resolved == root || filepath.HasPrefix(resolved, root+string(filepath.Separator))
}
