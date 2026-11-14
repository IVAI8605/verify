// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package perm

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// skipOnWindows Windows 的 os.Chmod 只支持只读位，无法表达 0600/0750，相关断言没有意义。
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不支持 POSIX 权限位")
	}
}

// TestTightenLoosensNothing 验证只收紧、不放松：已设成 0700 的目录不会被改回 0750。
func TestTightenLoosensNothing(t *testing.T) {
	skipOnWindows(t)
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if Tighten(dir, 0o750) {
		t.Fatal("权限已经严于目标时不应发生修改")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("期望保持 0700，实际 %o", got)
	}
}

// TestTightenDir 验证过宽的目录权限会被收敛到 0750。
func TestTightenDir(t *testing.T) {
	skipOnWindows(t)
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !Tighten(dir, 0o750) {
		t.Fatal("0755 应收紧为 0750")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("期望 0750，实际 %o", got)
	}
}

// TestTightenFile 验证已存在的老文件（例如 0644 的日志与配置）能被收紧到 0600。
func TestTightenFile(t *testing.T) {
	skipOnWindows(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if !Tighten(path, 0o600) {
		t.Fatal("0644 应收紧为 0600")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("期望 0600，实际 %o", got)
	}
}

// TestTightenMissingPath 验证路径不存在时静默返回 false，不影响主流程。
func TestTightenMissingPath(t *testing.T) {
	if Tighten(filepath.Join(t.TempDir(), "nope"), 0o600) {
		t.Fatal("不存在的路径不应返回 true")
	}
}
