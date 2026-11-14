// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"verify/internal/store"
)

// TestFitTrashNameClampsToNameMax 覆盖 v3.10.8 的超长文件名修复：
// 隔离区文件名必须截断到 NAME_MAX 以内，同时保留扩展名、不切断 UTF-8 字符。
func TestFitTrashNameClampsToNameMax(t *testing.T) {
	long := strings.Repeat("长", 200) + ".mkv" // 600+ 字节，远超 255
	got := fitTrashName(long, "-12")
	if len(got) > maxTrashNameBytes {
		t.Fatalf("截断后仍超长：%d 字节", len(got))
	}
	if !strings.HasSuffix(got, ".mkv") {
		t.Fatalf("扩展名丢失：%q", got[len(got)-8:])
	}
	if !utf8.ValidString(got) {
		t.Fatalf("截断点落在 UTF-8 字符中间：%q", got)
	}
	if short := "a.mkv"; fitTrashName(short, "") != short {
		t.Fatalf("短文件名不应被改动：%q", fitTrashName(short, ""))
	}
}

// TestBuildTrashPathMkdirErrorChinese 覆盖 P2-02：隔离目录建不出来时，
// 错误必须带中文前缀，不能把英文 mkdir 原文直接抛给界面。
func TestBuildTrashPathMkdirErrorChinese(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, _ := newHealthTestEngine(t)
	_, err := e.buildTrashPath(blocker, filepath.Join(blocker, "a.bin"), time.Now())
	if err == nil {
		t.Fatal("隔离目录建在普通文件下，期望失败")
	}
	if !strings.Contains(err.Error(), "创建隔离目录失败") {
		t.Fatalf("错误文案缺少中文前缀：%v", err)
	}
}

// TestCheckTrashRowPath 覆盖 v3.7.1 新增的隔离区路径复核：
// 永久删除/清空/恢复都靠它把「历史脏记录」挡在动手之前，判定失败宁可拒绝。
func TestCheckTrashRowPath(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)

	// 受管卷内的正常隔离区文件
	trashDir := filepath.Join(root, ".verify-trash", "2026-11-05")
	if err := os.MkdirAll(trashDir, 0o750); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(trashDir, "a.mkv")
	if err := os.WriteFile(good, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 卷内但不在隔离区目录里的普通文件（例如记录被改坏成指向原始媒体）
	outside := filepath.Join(root, "real-video.mkv")
	if err := os.WriteFile(outside, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 受管卷之外的路径：任何情况下都不允许动
	offVol := filepath.Join(t.TempDir(), ".verify-trash", "x.mkv")
	if err := os.MkdirAll(filepath.Dir(offVol), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(offVol, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		trashPath string
		wantPass  bool
	}{
		{"隔离区内正常文件", good, true},
		{"隔离区内已丢失文件应放行（仅清记录）", filepath.Join(trashDir, "missing.mkv"), true},
		{"卷内非隔离区路径应拒绝", outside, false},
		{"卷外路径应拒绝", offVol, false},
		{"穿越到卷外的隔离区路径应拒绝", filepath.Join(root, ".verify-trash", "..", "..", "evil.mkv"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason := e.checkTrashRowPath(store.TrashRow{OrigPath: c.trashPath, TrashPath: c.trashPath, Volume: root})
			if c.wantPass && reason != "" {
				t.Fatalf("应通过复核，实际被拒：%s", reason)
			}
			if !c.wantPass && reason == "" {
				t.Fatal("应拒绝，却通过了复核")
			}
		})
	}
}

// TestRemovableReason 校验预览阶段的父目录可写探测：
// 目标是让「预览显示可执行」与「执行真的能删」用同一把尺子（审查报告 P-01）。
func TestRemovableReason(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.mkv")
	if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reason := removableReason(p); reason != "" {
		t.Fatalf("可写目录下的文件应判定为可删，实际：%s", reason)
	}
	// 文件不存在时只探测父目录权限，不应误报（删除允许仅清理记录）
	if reason := removableReason(filepath.Join(dir, "gone.mkv")); reason != "" {
		t.Fatalf("探测对象是父目录权限，文件不存在也应通过：%s", reason)
	}
	if reason := removableReason(filepath.Join(dir, "no-such-dir", "a.mkv")); reason == "" {
		t.Fatal("父目录不存在应判定为不可删")
	}
}
