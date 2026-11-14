// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package applog

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestWriteChineseAndSwitch 验证中文日志落盘、带 24 小时时间戳，并且目录可运行时切换。
func TestWriteChineseAndSwitch(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	if err := Init(dir1, dir1); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = Close() }()

	if _, err := Out().Write([]byte("扫描开始：测试中文日志\n")); err != nil {
		t.Fatal(err)
	}
	Flush()
	assertLog(t, filepath.Join(dir1, fileName), "扫描开始：测试中文日志")

	if err := Switch(dir2); err != nil {
		t.Fatal(err)
	}
	if _, err := Out().Write([]byte("日志目录已切换\n")); err != nil {
		t.Fatal(err)
	}
	Flush()
	assertLog(t, filepath.Join(dir2, fileName), "日志目录已切换")

	// 切换后新目录应继续追加，而不是覆盖旧内容
	if err := Switch(dir2); err != nil {
		t.Fatal(err)
	}
	if _, err := Out().Write([]byte("追加一行\n")); err != nil {
		t.Fatal(err)
	}
	Flush()
	data, err := os.ReadFile(filepath.Join(dir2, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "追加一行") != 1 {
		t.Fatalf("日志应追加写入，实际内容:\n%s", data)
	}
}

// TestInitFallsBackToDefault 自定义目录不可用时仍应写默认目录，避免启动后完全没有日志。
func TestInitFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "notadir")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir, bad); err == nil {
		t.Fatal("自定义目录不可用时应返回错误提示")
	}
	defer func() { _ = Close() }()
	if got := Path(); got != filepath.Join(dir, fileName) {
		t.Fatalf("应回退到默认日志目录，实际 %s", got)
	}
	if _, err := Out().Write([]byte("回退日志\n")); err != nil {
		t.Fatal(err)
	}
	Flush()
	assertLog(t, filepath.Join(dir, fileName), "回退日志")
}

func assertLog(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, want) {
		t.Fatalf("日志缺少内容 %q:\n%s", want, text)
	}
	timestamp := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} `)
	if !timestamp.MatchString(text) {
		t.Fatalf("日志缺少 24 小时时间戳:\n%s", text)
	}
}
