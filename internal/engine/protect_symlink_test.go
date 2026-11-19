// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProtectedReasonResolvedSymlinkBypass 回归 v4.1.2：
// 卷内软链接指向受保护目录时，原始串判保护会漏、解析后判卷会过，
// 导致受保护文件被移入隔离区/清理。resolved 版本必须按真实路径拦下。
func TestProtectedReasonResolvedSymlinkBypass(t *testing.T) {
	e, _ := newHealthTestEngine(t)
	vol := t.TempDir()
	protectedDir := filepath.Join(vol, "protected")
	if err := os.MkdirAll(protectedDir, 0o750); err != nil {
		t.Fatal(err)
	}
	protectedFile := filepath.Join(protectedDir, "secret.mkv")
	if err := os.WriteFile(protectedFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 卷内软链接 → 受保护目录
	linkDir := filepath.Join(vol, "link")
	if err := os.Symlink(protectedDir, linkDir); err != nil {
		t.Skipf("测试环境不支持软链接：%v", err)
	}
	viaLink := filepath.Join(linkDir, "secret.mkv")

	// 配置保护目录
	c := e.cfg.Get()
	c.ProtectPaths = []string{protectedDir}
	if err := e.cfg.Replace(c); err != nil {
		t.Fatal(err)
	}

	// 原始串判定会漏（这就是原来的 bug）
	if e.protectedReason(viaLink) != "" {
		t.Fatal("前提不成立：原始串判定本应漏掉软链接路径")
	}
	// resolved 判定必须拦下
	if reason := e.protectedReasonResolved(viaLink); reason == "" {
		t.Fatal("通过软链接访问受保护文件必须被拦截")
	}
	// 直连路径本来就该拦，确认没退化
	if e.protectedReasonResolved(protectedFile) == "" {
		t.Fatal("直连保护文件仍应被拦截")
	}
	// 普通文件不误伤
	normal := filepath.Join(vol, "normal.mkv")
	if err := os.WriteFile(normal, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if e.protectedReasonResolved(normal) != "" {
		t.Fatal("非保护文件不应被误拦")
	}
}
