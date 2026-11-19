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
// 卷正常、批次目录还在（用户只是删了其中一个文件）时，必须允许清理幽灵行。
func TestRowRemovableVolumeTrusted(t *testing.T) {
	vol := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vol, ".verify-trash"), 0o750); err != nil {
		t.Fatal(err)
	}
	// 日期/批次目录仍在——对应「用户删了隔离区里的单个文件」这一真实场景。
	if err := os.MkdirAll(filepath.Join(vol, ".verify-trash", "2026-11-10"), 0o750); err != nil {
		t.Fatal(err)
	}
	trashPath := filepath.Join(vol, ".verify-trash", "2026-11-10", "a.mkv")
	if !RowRemovable(trashPath, []string{vol}) {
		t.Fatal("卷可信、批次目录还在且文件已不存在时应清掉幽灵行")
	}
}

// TestRowRemovableBatchDirRenamed 回归 v4.1.2：日期目录被改名/搬走、但文件还在
// 新目录里时，精确路径 Lstat 也是 ENOENT，绝不能把整批隔离记录当幽灵行删掉。
func TestRowRemovableBatchDirRenamed(t *testing.T) {
	vol := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vol, ".verify-trash"), 0o750); err != nil {
		t.Fatal(err)
	}
	// 模拟批次目录被改名成 2026-11-10-renamed，文件完好无损地待在里面。
	renamed := filepath.Join(vol, ".verify-trash", "2026-11-10-renamed")
	if err := os.MkdirAll(renamed, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(renamed, "a.mkv"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	// 记录里仍写着旧路径；旧路径不存在、卷根也在，但批次目录整体消失 → 必须保留。
	oldPath := filepath.Join(vol, ".verify-trash", "2026-11-10", "a.mkv")
	if RowRemovable(oldPath, []string{vol}) {
		t.Fatal("批次目录被改名时必须保留记录，否则文件变成无法恢复的孤儿")
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
