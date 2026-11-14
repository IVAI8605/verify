// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"verify/internal/config"
)

// v1.30.1 安全回归：trim / relink 此前只对原始串做卷前缀匹配（VolumeOf），
// `/vol1/x/../..` 形态的 `..` 向量可通过校验读写卷外文件。
// 修复方式：入口统一接 ValidPath 三步防线（Clean+软链接解析+卷前缀重判）。

// testSecEngine 构造只含配置的最小 Engine——本文件覆盖的校验函数均不触数据库。
// v3.17.3（F-02）：Volumes 现在只接受 /vol 下的路径，测试的临时目录改走
// 与生产同源的 VERIFY_SCAN_ROOTS（Volumes 为空时 engine.Volumes() 读它），
// 避免为了测试放宽生产校验。
func testSecEngine(t *testing.T, vol string) *Engine {
	t.Helper()
	mgr, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VERIFY_SCAN_ROOTS", vol)
	return &Engine{cfg: mgr}
}

func secTestSkipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX 路径与软链接语义需 CI(linux) 验证")
	}
}

// TestTrimCheckRejectsTraversal `..` 逃逸路径必须在 trimCheck 被拒绝；
// 卷内普通文件不得被误拦。
func TestTrimCheckRejectsTraversal(t *testing.T) {
	secTestSkipOnWindows(t)
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "victim.mp4"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := testSecEngine(t, root)
	escape := filepath.Join(root, "..", filepath.Base(outside), "victim.mp4")
	if reason := e.trimCheck(TrimJob{Path: escape, Offset: 5}); reason == "" {
		t.Fatal("`..` 逃逸路径应被 trimCheck 拒绝")
	}
	in := filepath.Join(root, "normal.mp4")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reason := e.trimCheck(TrimJob{Path: in, Offset: 5}); reason != "" {
		t.Fatalf("卷内普通文件不应被拦截，实际 %q", reason)
	}
}

// TestRelinkCheckRejectsTraversal `..` 逃逸与真实卷外路径都必须被 relinkCheck 拒绝；
// 同卷同内容配对应通过全部校验。
func TestRelinkCheckRejectsTraversal(t *testing.T) {
	secTestSkipOnWindows(t)
	root := t.TempDir()
	outside := t.TempDir()
	keep := filepath.Join(root, "keep.bin")
	if err := os.WriteFile(keep, []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(outside, "out.bin")
	if err := os.WriteFile(outFile, []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := testSecEngine(t, root)
	escape := filepath.Join(root, "..", filepath.Base(outside), "out.bin")
	if r := e.relinkCheck(keep, escape, true); r == "" {
		t.Fatal("`..` 逃逸目标应被 relinkCheck 拒绝")
	}
	if r := e.relinkCheck(keep, outFile, true); r == "" {
		t.Fatal("真实卷外目标应被 relinkCheck 拒绝")
	}
	inTarget := filepath.Join(root, "target.bin")
	if err := os.WriteFile(inTarget, []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := e.relinkCheck(keep, inTarget, true); r != "" {
		t.Fatalf("同卷同内容配对应通过校验，实际 %q", r)
	}
}
