// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package cache

import (
	"runtime"
	"testing"
)

func isLinux() bool { return runtime.GOOS == "linux" }

// TestBudgetFromKB 内存预算分档公式（设计文档 §1.3 表格）。
func TestBudgetFromKB(t *testing.T) {
	const GiB = 1024 * 1024
	cases := []struct {
		kb   int64
		want int
	}{
		{4 * GiB, 1024},
		{8 * GiB, 1024},
		{8*GiB + 1, 2048},
		{16 * GiB, 2048},
		{16*GiB + 1, 4096},
		{32 * GiB, 4096}, // 用户的 32G 机器 → 4096（预算非物理占用）
		{32*GiB + 1, 8192},
		{64 * GiB, 8192},
		{64*GiB + 1, 12288},
		{128 * GiB, 12288},
	}
	for _, c := range cases {
		if got := budgetFromKB(c.kb); got != c.want {
			t.Fatalf("budgetFromKB(%d KiB) = %d，期望 %d", c.kb, got, c.want)
		}
	}
}

// TestDetectMediaNonLinux 非 Linux 上必须返回 unknown（不臆造）。
func TestDetectMediaNonLinux(t *testing.T) {
	got := DetectMedia(t.TempDir())
	if got != MediaSSD && got != MediaHDD && got != MediaUnknown {
		t.Fatalf("非法介质值 %q", got)
	}
	if got != MediaUnknown && !isLinux() {
		t.Fatalf("非 Linux 应返回 unknown，实际 %q", got)
	}
}

// TestUnescapeMount 挂载点八进制转义还原。
func TestUnescapeMount(t *testing.T) {
	if got := unescapeMount(`/mnt/data\040disk`); got != `/mnt/data disk` {
		t.Fatalf("unescapeMount = %q", got)
	}
	if got := unescapeMount(`/plain`); got != `/plain` {
		t.Fatalf("unescapeMount = %q", got)
	}
}

// TestWithin 目录边界判断。
func TestWithin(t *testing.T) {
	if !within("/vol1/cache", "/vol1") {
		t.Fatal("子路径应命中")
	}
	if within("/vol1x/cache", "/vol1") {
		t.Fatal("目录边界不得误配（vol1x 不是 vol1 的子路径）")
	}
	if !within("/anything", "/") {
		t.Fatal("根挂载点覆盖一切")
	}
}
