// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 回归 v4.0.3：健康探测按物理卷分组分配路数，保证每块盘都在扫。

import (
	"os"
	"path/filepath"
	"testing"

	"verify/internal/health"
)

func TestHealthGroupWorkersEachDiskGetsOne(t *testing.T) {
	// 3 块盘、总路数 4：每盘至少 1 路，余下 1 路给候选最多的组。
	got := healthGroupWorkers([]int{100, 10, 10}, 4)
	if len(got) != 3 {
		t.Fatalf("应返回 3 组，实际 %v", got)
	}
	sum := 0
	for _, n := range got {
		if n < 1 {
			t.Fatalf("每组至少 1 路，实际 %v", got)
		}
		sum += n
	}
	if sum != 4 {
		t.Fatalf("总路数应为 4，实际 %d（%v）", sum, got)
	}
	if got[0] <= got[1] || got[0] <= got[2] {
		t.Fatalf("候选最多的组应拿到余量，实际 %v", got)
	}
}

func TestHealthGroupWorkersMoreDisksThanSlots(t *testing.T) {
	// 盘数多于总路数：宁可超订，也要每盘 1 路（否则有的盘永远不被扫）。
	got := healthGroupWorkers([]int{5, 5, 5, 5, 5, 5, 5, 8}, 4)
	if len(got) != 8 {
		t.Fatalf("应返回 8 组，实际 %v", got)
	}
	for i, n := range got {
		if n != 1 {
			t.Fatalf("第 %d 组应为 1 路，实际 %d（%v）", i, n, got)
		}
	}
}

func TestHealthGroupWorkersZeroCandidates(t *testing.T) {
	// 候选全为 0（空盘）：余下路数均分，避免全部堆到第一组。
	got := healthGroupWorkers([]int{0, 0, 0}, 4)
	if len(got) != 3 {
		t.Fatalf("应返回 3 组，实际 %v", got)
	}
	sum := 0
	for _, n := range got {
		sum += n
	}
	if sum != 4 {
		t.Fatalf("总路数应为 4，实际 %d（%v）", sum, got)
	}
}

func TestCollectHealthCandidateGroupsSplitsByDevice(t *testing.T) {
	// 同一临时目录下建两个子目录：deviceGroups 视其为同盘 → 合并成 1 组。
	// 这里只验证「分组返回且并集等于展平结果」，设备数由环境决定。
	root := t.TempDir()
	for _, d := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, d, "f.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e, _ := newHealthTestEngine(t)
	groups, err := e.collectHealthCandidateGroups(t.Context(), []string{root}, health.KindAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) == 0 {
		t.Fatal("应有分组")
	}
	flat, err := e.collectHealthCandidates(t.Context(), []string{root}, health.KindAll)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	if total != len(flat) {
		t.Fatalf("分组并集 %d 应等于展平 %d", total, len(flat))
	}
}
