// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package trashvol

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRowRemovableVolumeUnmounted 是隔离记录误删的正例防护：
// 卷根目录还在（卸载后残留的挂载点），但隔离目录不存在，必须保留记录。
func TestRowRemovableVolumeUnmounted(t *testing.T) {
	vol := t.TempDir() // 空目录：模拟「存储卷卸载、挂载点残留」
	trashPath := filepath.Join(vol, ".verify-trash", "2026-11-10", "a.mkv")
	if RowRemovable(trashPath, []string{vol}) {
		t.Fatal("卷不可信时不得删除隔离记录（会丢失恢复线索）")
	}
}

// TestRowRemovableVolumeTrusted 是反例：证明不是「一刀切不清」。
// 卷正常（隔离目录存在）且文件确实不在时，必须允许清理幽灵行。
func TestRowRemovableVolumeTrusted(t *testing.T) {
	vol := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vol, ".verify-trash"), 0o750); err != nil {
		t.Fatal(err)
	}
	trashPath := filepath.Join(vol, ".verify-trash", "2026-11-10", "a.mkv")
	if !RowRemovable(trashPath, []string{vol}) {
		t.Fatal("卷可信且文件已不存在时应清掉幽灵行")
	}
}

// TestRowRemovableMisc 覆盖其余保留分支，避免判定被简化成只查隔离目录。
func TestRowRemovableMisc(t *testing.T) {
	vol := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vol, ".verify-trash"), 0o750); err != nil {
		t.Fatal(err)
	}

	// 1) 隔离文件仍在 → 保留。
	alive := filepath.Join(vol, ".verify-trash", "a.mkv")
	if err := os.WriteFile(alive, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if RowRemovable(alive, []string{vol}) {
		t.Fatal("隔离文件还在时不得删记录")
	}

	// 2) 路径不在任何卷内 → 保留。
	if RowRemovable("/other/.verify-trash/a.mkv", []string{vol}) {
		t.Fatal("卷内定位不到时应保留")
	}

	// 3) 空路径 → 保留。
	if RowRemovable("", []string{vol}) {
		t.Fatal("空路径应保留")
	}

	// 4) 卷前缀相似（/vol1 与 /vol10）不能误匹配。
	vol1 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vol1, ".verify-trash"), 0o750); err != nil {
		t.Fatal(err)
	}
	near := vol1 + "0/.verify-trash/a.mkv"
	if RowRemovable(near, []string{vol1}) {
		t.Fatal("前缀相似的卷不得误匹配")
	}
}
