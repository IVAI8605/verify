// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCopyFileVerified 覆盖跨卷移动的核心：复制+校验+删源。
func TestCopyFileVerified(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.bin")
	payload := []byte("hello verify 4.2.0")
	if err := os.WriteFile(src, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	// 该工作区的挂载会把新建文件强制成 0700，显式 chmod 才能测到「保留权限」。
	if err := os.Chmod(src, 0o640); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(1700000000, 0)
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "b.bin")
	if err := copyFileVerified(src, dst, true); err != nil {
		t.Fatalf("复制失败：%v", err)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Fatalf("removeSrc 后源文件应已删除，实际 err=%v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("目标内容不一致：%q err=%v", got, err)
	}
	fi, _ := os.Stat(dst)
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("权限应保留 0640，实际 %o", fi.Mode().Perm())
	}
	if !fi.ModTime().Equal(mtime) {
		t.Fatalf("修改时间应保留 %v，实际 %v", mtime, fi.ModTime())
	}
}

// TestCopyFileVerifiedNoRemove 保留源（目录树复制用）。
func TestCopyFileVerifiedNoRemove(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "b.txt")
	if err := copyFileVerified(src, dst, false); err != nil {
		t.Fatalf("复制失败：%v", err)
	}
	if _, err := os.Lstat(src); err != nil {
		t.Fatalf("removeSrc=false 时源文件应保留：%v", err)
	}
}

// TestCopyFileVerifiedNoOverwrite 目标已存在时必须失败，绝不覆盖。
func TestCopyFileVerifiedNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	dst := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(src, []byte("src"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFileVerified(src, dst, true); err == nil {
		t.Fatalf("目标已存在时应失败")
	}
	data, _ := os.ReadFile(dst)
	if string(data) != "keep" {
		t.Fatalf("目标文件被覆盖：%q", data)
	}
	if _, err := os.Lstat(src); err != nil {
		t.Fatalf("失败时源文件必须保留：%v", err)
	}
}

// TestCopyTreeVerified 目录树复制+删源。
func TestCopyTreeVerified(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("bb"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "tree2")
	if err := copyTreeVerified(src, dst); err != nil {
		t.Fatalf("目录复制失败：%v", err)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Fatalf("源目录应已删除")
	}
	if data, err := os.ReadFile(filepath.Join(dst, "sub", "b.txt")); err != nil || string(data) != "bb" {
		t.Fatalf("子文件内容不一致：%q err=%v", data, err)
	}
}

// TestMoveFileAcrossSameVolume 同卷仍走 rename（源消失、目标出现）。
func TestMoveFileAcrossSameVolume(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	dst := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := moveFileAcross(src, dst); err != nil {
		t.Fatalf("同卷移动失败：%v", err)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Fatalf("源应不存在")
	}
	if _, err := os.Lstat(dst); err != nil {
		t.Fatalf("目标应存在：%v", err)
	}
}
