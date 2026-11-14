// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 本文件是 v3.22.0 原子改名的回归：目标已存在时必须失败且不能覆盖内容。
// Linux 走 renameat2(RENAME_NOREPLACE)，其它平台走 Lstat + rename，两者语义一致。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenameNoOverwriteKeepsTarget(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "a.txt")
	newPath := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(oldPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := renameNoOverwrite(oldPath, newPath); err == nil {
		t.Fatal("目标已存在时应失败，绝不能覆盖")
	}
	if b, err := os.ReadFile(newPath); err != nil || string(b) != "new" {
		t.Fatalf("目标文件被覆盖：content=%q err=%v", string(b), err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("失败后源文件应保持原样：%v", err)
	}
}

func TestRenameNoOverwriteMoves(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "a.txt")
	newPath := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(oldPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renameNoOverwrite(oldPath, newPath); err != nil {
		t.Fatalf("目标不存在时应改名成功：%v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatal("成功后源文件不应存在")
	}
}
