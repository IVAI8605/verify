// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 本文件是 v3.17.5 的恢复路径回归（F-16）：目标父目录不存在时，
// 必须上溯到第一个存在的祖先再判卷前缀，不能因为 EvalSymlinks 失败就放行。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveManagedParent(t *testing.T) {
	secTestSkipOnWindows(t)
	root := t.TempDir()
	outside := t.TempDir()
	e := testSecEngine(t, root)

	// 卷内尚不存在的子目录：放行，并把尾部拼回去
	got, ok := e.resolveManagedParent(filepath.Join(root, "new", "deep"))
	if !ok {
		t.Fatal("卷内不存在的子目录应放行")
	}
	if !strings.HasSuffix(got, filepath.Join("new", "deep")) {
		t.Fatalf("拼接结果 %q 应以 new/deep 结尾", got)
	}

	// 已存在的普通目录：放行
	if _, ok := e.resolveManagedParent(root); !ok {
		t.Fatal("卷根本身应放行")
	}

	// 卷内软链接指向卷外：拒绝
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.resolveManagedParent(filepath.Join(link, "sub")); ok {
		t.Fatal("软链接指向卷外时应拒绝")
	}
}
