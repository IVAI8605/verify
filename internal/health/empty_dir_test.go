// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestFindEmptyDirs 验收（设计文档 10.2）：
// mkdir -p a/b/c 全空 → a、b、c 三条都报；b 放一个文件 → 只报 c；
// 保护/隔离区目录跳过且不拖累父目录。
func TestFindEmptyDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX 目录语义需 CI(linux) 验证")
	}
	root := t.TempDir()
	mk := func(rel string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mk("a/b/c")    // 全空树
	mk("x/y")      // 空树
	mk("busy/sub") // busy 放了文件
	if err := os.WriteFile(filepath.Join(root, "busy", "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	protected := mk("prot/inner") // 保护目录（skip 返回 true）
	if err := os.WriteFile(filepath.Join(root, "prot", ".keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = protected

	skip := func(p string) bool {
		return p == filepath.Join(root, "prot") || p == filepath.Join(root, "prot", "inner")
	}
	dirs, err := FindEmptyDirs(context.Background(), []string{root}, skip)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range dirs {
		got[d] = true
	}
	// a/b/c 全空：三条都报
	for _, rel := range []string{"a", filepath.Join("a", "b"), filepath.Join("a", "b", "c")} {
		if !got[filepath.Join(root, rel)] {
			t.Errorf("空树 %s 应命中，实际命中 %v", rel, dirs)
		}
	}
	// x/y 两条
	if !got[filepath.Join(root, "x")] || !got[filepath.Join(root, "x", "y")] {
		t.Errorf("x/y 应命中，实际 %v", dirs)
	}
	// busy 有文件不报；prot 被跳过不报，且 prot/.keep 使 prot 非空（不受 skip 影响）
	for _, rel := range []string{"busy", "prot", filepath.Join("prot", "inner")} {
		if got[filepath.Join(root, rel)] {
			t.Errorf("%s 不应命中", rel)
		}
	}
}
