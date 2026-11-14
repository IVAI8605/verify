// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestRenameNoOverwriteKeepsVictim 锁定「目标已存在时绝不覆盖」这条底线（v3.25.2 P3）。
// 裸 os.Rename 会把已有文件静默替换，这里直接断言源与目标都原样保留。
func TestRenameNoOverwriteKeepsVictim(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")
	if err := os.WriteFile(src, []byte("SRC"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("VICTIM"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := renameNoOverwrite(src, dst)
	if err == nil {
		t.Fatal("目标已存在时必须返回失败，不能静默覆盖")
	}
	if !strings.Contains(renameFailText(err), "目标文件已存在") {
		t.Fatalf("应提示目标已存在，实际 %q", renameFailText(err))
	}
	assertContent(t, src, "SRC")
	assertContent(t, dst, "VICTIM")
}

// TestRenameNoOverwriteUnderConcurrentCreator 用另一个 goroutine 抢建同名目标，
// 覆盖「检查目标不存在 → 真正改名」之间的窗口。断言只有两种合法结局：
// 改名成功（目标内容 = 源内容）或改名失败（目标仍是抢占者写的哨兵），
// 绝不允许「失败但目标被换掉」这种静默覆盖。
func TestRenameNoOverwriteUnderConcurrentCreator(t *testing.T) {
	dir := t.TempDir()
	const rounds = 200
	for i := 0; i < rounds; i++ {
		src := filepath.Join(dir, "src.mkv")
		dst := filepath.Join(dir, "dst.mkv")
		if err := os.WriteFile(src, []byte("SRC"), 0o644); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			// O_EXCL 等价于「外部进程在改名落地前恰好创建了同名文件」。
			f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				return // 改名先赢，本轮不构成竞态
			}
			_, _ = f.WriteString("VICTIM")
			_ = f.Close()
		}()

		err := renameNoOverwrite(src, dst)
		wg.Wait()

		if err == nil {
			assertContent(t, dst, "SRC")
		} else {
			assertContent(t, dst, "VICTIM")
			assertContent(t, src, "SRC")
		}
		// 复位到下一轮：把两个名字都清空（仅在测试临时目录内）。
		if rerr := os.Remove(dst); rerr != nil && !os.IsNotExist(rerr) {
			t.Fatal(rerr)
		}
		if rerr := os.Remove(src); rerr != nil && !os.IsNotExist(rerr) {
			t.Fatal(rerr)
		}
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	if string(b) != want {
		t.Fatalf("%s 内容应为 %q，实际 %q（可能被静默覆盖）", path, want, string(b))
	}
}
