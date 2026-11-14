// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"verify/internal/store"
)

// TestUndoRenameTargetDoesNotOverwrite 回归 v3.10.4 数据安全：
// Linux 的 rename 在目标存在时静默覆盖，撤销改名必须先确认原路径没被占用。
func TestUndoRenameTargetDoesNotOverwrite(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "new.mp4")
	to := filepath.Join(root, "old.mp4")
	if err := os.WriteFile(from, []byte("renamed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, []byte("occupier"), 0o600); err != nil {
		t.Fatal(err)
	}

	if reason := undoRenameTarget(from, to); reason == "" {
		t.Fatal("原路径被占用时应判失败")
	}
	// 两边内容都必须原样保留：不能覆盖，也不能把改后文件挪走。
	if b, err := os.ReadFile(to); err != nil || string(b) != "occupier" {
		t.Fatalf("原路径文件被改动：%q err=%v", b, err)
	}
	if b, err := os.ReadFile(from); err != nil || string(b) != "renamed" {
		t.Fatalf("改后文件不应被移动：%q err=%v", b, err)
	}
}

// TestUndoRenameTargetRestores 覆盖正常还原与源文件已缺失两种分支。
func TestUndoRenameTargetRestores(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "new.mp4")
	to := filepath.Join(root, "old.mp4")
	if err := os.WriteFile(from, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if reason := undoRenameTarget(from, to); reason != "" {
		t.Fatalf("正常还原不应失败：%s", reason)
	}
	if _, err := os.Lstat(to); err != nil {
		t.Fatalf("原路径应已恢复：%v", err)
	}
	if _, err := os.Lstat(from); err == nil {
		t.Fatal("改后路径应已移走")
	}
	if reason := undoRenameTarget(from, to); reason == "" {
		t.Fatal("源文件不存在时应判失败")
	}
}

// TestRenameNoOverwriteKeepsBothFiles 回归 P0-1（v3.10.10）：
// 规则引擎「预览 → 执行」之间目标可能被另一个标签页的批次或外部工具占用。
// 执行阶段必须拒绝，且两边内容一个字节都不能动；目标空闲时才正常改名。
func TestRenameNoOverwriteKeepsBothFiles(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.mp4")
	dst := filepath.Join(root, "第01集.mp4")
	if err := os.WriteFile(src, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("occupier"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := renameNoOverwrite(src, dst); err == nil {
		t.Fatal("目标已存在时应拒绝执行")
	}
	if b, err := os.ReadFile(dst); err != nil || string(b) != "occupier" {
		t.Fatalf("目标文件被覆盖：%q err=%v", b, err)
	}
	if b, err := os.ReadFile(src); err != nil || string(b) != "source" {
		t.Fatalf("源文件不应被移动或改动：%q err=%v", b, err)
	}

	// 目标空闲时行为与普通改名一致。
	free := filepath.Join(root, "第02集.mp4")
	if err := renameNoOverwrite(src, free); err != nil {
		t.Fatalf("目标不存在时应正常改名：%v", err)
	}
	if _, err := os.Lstat(src); err == nil {
		t.Fatal("改名成功后源路径不应存在")
	}
	if b, err := os.ReadFile(free); err != nil || string(b) != "source" {
		t.Fatalf("改名后内容应保持不变：%q err=%v", b, err)
	}
}

// TestUndoRenameBatchConvergesWhenAlreadyRestored 覆盖 v3.10.13 P2：
// 部分撤销重试时，已经改回原名的条目（新路径没了、原路径在）应计成功，
// 批次收敛到 undone，而不是每次都把「早撤好的」记成失败、永远消不掉提示条。
func TestUndoRenameBatchConvergesWhenAlreadyRestored(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()

	oldPath := filepath.Join(root, "old.mp4")
	newPath := filepath.Join(root, "new.mp4")
	if err := os.WriteFile(oldPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	batchID, err := st.CreateRenameBatchRunning(ctx, 0, 1, "[]", 7701)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddRenameIntents(ctx, batchID, 7701, [][2]string{{oldPath, newPath}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddAuditBatch(ctx, []store.AuditEntry{{TaskID: 7701, Operation: "rename", OriginalPath: oldPath, NewPath: newPath}}); err != nil {
		t.Fatal(err)
	}

	done, failed, status, failures, err := e.UndoRenameBatch(ctx, batchID)
	if err != nil {
		t.Fatal(err)
	}
	if done != 1 || failed != 0 || status != "undone" || len(failures) != 0 {
		t.Fatalf("已还原条目应按成功收敛，实际 done=%d failed=%d status=%s failures=%v", done, failed, status, failures)
	}
	// 已完整撤销的批次再撤一次必须直接返回，不覆盖状态。
	if _, _, again, _, err := e.UndoRenameBatch(ctx, batchID); err != nil || again != "undone" {
		t.Fatalf("重复撤销应保持 undone，实际 status=%s err=%v", again, err)
	}
}

// TestUndoBatchStatus 回归 P1-01（v3.10.11）：撤销上万文件时索引更新可能失败，
// 接口不能一边报「全部成功」一边留下错位索引，部分成功必须单独成态。
func TestUndoBatchStatus(t *testing.T) {
	cases := []struct {
		done, failed int
		want         string
	}{
		{13000, 0, "undone"},
		{0, 5, "undone_failed"},
		{12990, 10, "undone_partial"},
		// v3.12.1（第十二轮 P1-1）：空批次不能落成 undone_failed，
		// 没有任何可撤销项时按「全部成功」收尾才不会让界面挂着撤销失败告警。
		{0, 0, "undone"},
	}
	for _, c := range cases {
		if got := undoBatchStatus(c.done, c.failed); got != c.want {
			t.Fatalf("done=%d failed=%d 期望 %s，实际 %s", c.done, c.failed, c.want, got)
		}
	}
}

// TestBuildRenamePlanRejectsOutsideVolume 回归 v3.10.4：
// 规则引擎此前只有保护目录与 Lstat 校验，卷外路径会进入计划并被直接改名。
func TestBuildRenamePlanRejectsOutsideVolume(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	e := testSecEngine(t, root)
	methods := []RenameMethod{{Type: "add", Text: "x_"}}

	out := filepath.Join(outside, "evil.mp4")
	if err := os.WriteFile(out, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := e.BuildRenamePlan(context.Background(), []string{out}, methods)
	if len(plan) != 1 || plan[0].Error == "" {
		t.Fatalf("卷外路径应在计划阶段被拒：%+v", plan)
	}

	in := filepath.Join(root, "ok.mp4")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan = e.BuildRenamePlan(context.Background(), []string{in}, methods)
	if len(plan) != 1 || plan[0].Error != "" {
		t.Fatalf("卷内普通文件不应被误拦：%+v", plan)
	}
}
