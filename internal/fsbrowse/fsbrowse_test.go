// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package fsbrowse

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestListSubdirsStopsAtMax 验证分批读取在条目上限处提前停止并置截断标记（P2-14）。
func TestListSubdirsStopsAtMax(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d"} {
		if err := os.Mkdir(filepath.Join(root, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	names, truncated := listSubdirs(f, 2)
	if len(names) != 2 || !truncated {
		t.Fatalf("期望收集 2 条且标记截断，实际 %v（truncated=%v）", names, truncated)
	}
}

func TestNormalizeRejectsOutside(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "a", "b")
	if _, ok := Normalize([]string{root}, inside); !ok {
		t.Fatalf("根目录内路径应允许: %s", inside)
	}
	if _, ok := Normalize([]string{root}, root); !ok {
		t.Fatalf("根目录本身应允许")
	}
	if _, ok := Normalize([]string{root}, filepath.Join(root, "..", "outside")); ok {
		t.Fatalf("越过根目录的路径应拒绝")
	}
	if _, ok := Normalize([]string{root}, ""); ok {
		t.Fatalf("空路径应拒绝")
	}
}

func TestListAndMkdir(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"b", "a", "c"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// 文件不应出现在目录列表里
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := List([]string{root}, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Entries) != 3 {
		t.Fatalf("只应列出 3 个目录，实际 %d", len(list.Entries))
	}
	names := []string{list.Entries[0].Name, list.Entries[1].Name, list.Entries[2].Name}
	if names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Fatalf("目录应按名称排序: %v", names)
	}
	if list.Parent != "" {
		t.Fatalf("顶层目录不应有父级: %s", list.Parent)
	}

	newDir, err := Mkdir([]string{root}, root, "新文件夹")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Fatalf("新建目录不存在: %v", err)
	}
	if _, err := Mkdir([]string{root}, root, "新文件夹"); err == nil {
		t.Fatal("重复新建应报错")
	}
	for _, bad := range []string{"", "..", "a/b", `a\b`} {
		if _, err := Mkdir([]string{root}, root, bad); err == nil {
			t.Fatalf("非法名称应拒绝: %q", bad)
		}
	}
}

func TestListParentWithinRoots(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	list, err := List([]string{root}, nil, sub)
	if err != nil {
		t.Fatal(err)
	}
	if list.Parent != root {
		t.Fatalf("父目录错误: got=%s want=%s", list.Parent, root)
	}
}

// TestListEntryViewNoVolumeRoots v1.41.1 安全修复：入口视图不再列任何卷根
// （WebDAV 等全局可读挂载会让 readable 探测误判为已授权），
// 只列 shortcuts（用户在应用内添加/浏览过的目录，由调用方并入数据目录兜底）。
func TestListEntryViewNoVolumeRoots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX 权限语义需 CI(linux) 验证")
	}
	root := t.TempDir()
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(locked, 0o750) }()
	shortcut := filepath.Join(root, "user-picked")
	if err := os.Mkdir(shortcut, 0o750); err != nil {
		t.Fatal(err)
	}
	l, err := List([]string{root, locked}, []string{shortcut}, "")
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, e := range l.Entries {
		paths[e.Path] = true
	}
	// 卷根（可读的 root 与不可读的 locked）都不得出现
	if paths[root] || paths[locked] {
		t.Fatalf("卷根不应出现在入口视图，实际 %v", paths)
	}
	if !paths[shortcut] {
		t.Fatalf("用户添加的入口应保留")
	}
	if l.HiddenLocked != 0 {
		t.Fatalf("HiddenLocked 应恒为 0，实际 %d", l.HiddenLocked)
	}

	// v1.41.2：未授权（readable 失败）的配置目录收起并计数，不显示
	lockedSC := filepath.Join(t.TempDir(), "locked-sc")
	if err := os.Mkdir(lockedSC, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(lockedSC, 0o750) }()
	l2, err := List([]string{root}, []string{shortcut, lockedSC}, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range l2.Entries {
		if e.Path == lockedSC {
			t.Fatalf("未授权配置目录不应显示")
		}
		if e.Path == shortcut {
			found = true
			// v3.5.3：入口显示最下级文件夹名，而不是整串路径
			if e.Name != filepath.Base(shortcut) {
				t.Fatalf("入口名称应为 %q，实际 %q", filepath.Base(shortcut), e.Name)
			}
		}
	}
	if !found {
		t.Fatal("可读配置目录应显示")
	}
	if l2.HiddenLocked != 1 {
		t.Fatalf("HiddenLocked = %d，期望 1", l2.HiddenLocked)
	}
}
