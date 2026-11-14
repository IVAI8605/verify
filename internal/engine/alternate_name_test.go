// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 本文件回归 P3-1：恢复时的重名兜底名必须复核存在性，不能返回已被占用的路径。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAlternateNameSkipsExistingNumbered 锁定基本行为：编号名被占用时顺延。
func TestAlternateNameSkipsExistingNumbered(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "clip.mkv")
	if err := os.WriteFile(filepath.Join(dir, "clip (恢复 1).mkv"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := alternateNameAt(orig, time.Unix(0, 1))
	if err != nil {
		t.Fatalf("应能找到可用名：%v", err)
	}
	if want := filepath.Join(dir, "clip (恢复 2).mkv"); got != want {
		t.Fatalf("应顺延到 %s，实际 %s", want, got)
	}
}

// TestAlternateNameFallbackRechecks 回归 P3-1 本体：999 个编号名全部占用后，
// 旧实现直接返回时间戳名、不再 Lstat 复核。这里把该时间戳名也抢先占住，
// 断言返回值必须换一个，且磁盘上确实不存在——否则后续改名会覆盖同名文件。
func TestAlternateNameFallbackRechecks(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "clip.mkv")
	ext := filepath.Ext(orig)
	stem := strings.TrimSuffix(orig, ext)
	for i := 1; i < 1000; i++ {
		if err := os.WriteFile(fmt.Sprintf("%s (恢复 %d)%s", stem, i, ext), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Unix(1763000000, 123456789)
	stamped := fmt.Sprintf("%s (恢复 %d)%s", stem, now.UnixNano(), ext)
	if err := os.WriteFile(stamped, []byte("victim"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := alternateNameAt(orig, now)
	if err != nil {
		t.Fatalf("兜底分支应能给出不冲突的名字：%v", err)
	}
	if got == stamped {
		t.Fatal("返回了已被占用时间戳兜底名，后续改名会覆盖该文件")
	}
	if _, lerr := os.Lstat(got); !os.IsNotExist(lerr) {
		t.Fatalf("返回名 %s 在磁盘上已存在（err=%v）", got, lerr)
	}
}
