// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestThumbCachePrune 审查 D-2 回归：磁盘缓存超限时按 mtime 从旧到新淘汰。
func TestThumbCachePrune(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	write("a.jpg", 100, 3*time.Hour) // 最旧，先删
	write("b.jpg", 100, 2*time.Hour)
	write("c.jpg", 100, 1*time.Hour) // 最新，保留
	write("keep.txt", 10, 0)         // 非 jpg 不动

	// 上限 250：删除 a 后 total=200 已达标 → b、c 保留
	thumbCachePrune(dir, 250)

	if _, err := os.Stat(filepath.Join(dir, "a.jpg")); !os.IsNotExist(err) {
		t.Fatal("最旧的 a.jpg 应被淘汰")
	}
	if _, err := os.Stat(filepath.Join(dir, "b.jpg")); err != nil {
		t.Fatal("b.jpg 应保留（总量已达标）")
	}
	if _, err := os.Stat(filepath.Join(dir, "c.jpg")); err != nil {
		t.Fatal("c.jpg 应保留")
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Fatal("非 jpg 文件不应被清理")
	}
	// 不超限时不删
	write("d.jpg", 100, 4*time.Hour)
	thumbCachePrune(dir, 1000)
	for _, n := range []string{"b.jpg", "c.jpg", "d.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("未超限时不应删除 %s", n)
		}
	}
}
