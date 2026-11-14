// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 本文件是 v3.17.3 的改名链路回归：新名清洗（F-03）、旧路径编码（F-09）、
// 三向互斥（F-08）、批量改名后清健康结论（F-01）与序号位数上限（F-07）。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"verify/internal/store"
)

// TestInvalidRenameName 验证新名只能是文件名本身（F-03）。
func TestInvalidRenameName(t *testing.T) {
	for _, n := range []string{"", ".", "..", "a/b.mp4", `a\b.mp4`, "a\x00b.mp4"} {
		if invalidRenameName(n) == "" {
			t.Fatalf("%q 应被判为非法文件名", n)
		}
	}
	for _, n := range []string{"a.mp4", "a..b.mp4", ".hidden"} {
		if msg := invalidRenameName(n); msg != "" {
			t.Fatalf("%q 应合法，实际被拒：%s", n, msg)
		}
	}
}

// TestBuildRenamePlanRejectsSeparator 回归 F-03：规则拼出 "../../x" 时整条跳过，
// 不生成可写到父目录的 NewPath。
func TestBuildRenamePlanRejectsSeparator(t *testing.T) {
	secTestSkipOnWindows(t)
	root := t.TempDir()
	src := filepath.Join(root, "a.mp4")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := testSecEngine(t, root)
	plan := e.BuildRenamePlan(context.Background(), []string{src}, []RenameMethod{
		{Type: "list", Names: []string{"../../x.mp4"}},
	})
	if len(plan) != 1 {
		t.Fatalf("计划条目数应为 1，实际 %d", len(plan))
	}
	if plan[0].Error == "" {
		t.Fatal("含路径分隔符的新名必须整条跳过")
	}
	if plan[0].NewPath != "" {
		t.Fatalf("非法项不应生成 NewPath，实际 %q", plan[0].NewPath)
	}
}

// TestBuildRenamePlanRejectsInvalidUTF8 回归 F-09：非 UTF-8 旧路径在计划阶段就拦下。
func TestBuildRenamePlanRejectsInvalidUTF8(t *testing.T) {
	root := t.TempDir()
	e := testSecEngine(t, root)
	bad := filepath.Join(root, string([]byte{0xff, 0xfe})+"x.mp4")
	plan := e.BuildRenamePlan(context.Background(), []string{bad}, []RenameMethod{
		{Type: "add", Text: "p_", Position: "head"},
	})
	if len(plan) != 1 || plan[0].Error == "" {
		t.Fatalf("非 UTF-8 路径应报错，实际 %+v", plan)
	}
}

// TestExecuteRenamePlanRejectsBusyStates 回归 F-08：索引体检期间拒绝改名。
func TestExecuteRenamePlanRejectsBusyStates(t *testing.T) {
	e, _, root := newMoveDirEngine(t)
	src := filepath.Join(root, "a.mp4")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := []RenamePlanItem{{
		OldPath: src, OldName: "a.mp4",
		NewName: "b.mp4", NewPath: filepath.Join(root, "b.mp4"),
	}}
	e.indexRepair.Store(true)
	defer e.indexRepair.Store(false)
	if _, err := e.ExecuteRenamePlan(context.Background(), plan, nil, 0); err == nil {
		t.Fatal("索引体检进行中应拒绝改名")
	}
}

// TestExecuteRenamePlanDropsOldHealth 回归 F-01：批量改名成功后旧路径的健康结论必须清空，
// 否则健康页继续列出已经不存在的文件。
func TestExecuteRenamePlanDropsOldHealth(t *testing.T) {
	e, st, root := newMoveDirEngine(t)
	ctx := context.Background()
	src := filepath.Join(root, "old.mp4")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceHealthResults(ctx, "ext", []store.HealthResult{{Path: src}}); err != nil {
		t.Fatal(err)
	}
	methods := []RenameMethod{{Type: "add", Text: "new_", Position: "head"}}
	plan := e.BuildRenamePlan(ctx, []string{src}, methods)
	if len(plan) != 1 || plan[0].Error != "" {
		t.Fatalf("计划不应有错误：%+v", plan)
	}
	if _, err := e.ExecuteRenamePlan(ctx, plan, methods, 0); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountHealthResults(ctx, "ext"); err != nil || n != 0 {
		t.Fatalf("改名后旧路径健康结论应清空，实际 %d（err=%v）", n, err)
	}
}

// TestMethodRenumberDigitsCap 回归 F-07：位数在引擎侧兜底钳到 10。
func TestMethodRenumberDigitsCap(t *testing.T) {
	got := applyOneMethod("a", 0, RenameMethod{Type: "renumber", Start: 1, Digits: 1000000, Text: "_"}, nil)
	if len(got) > 32 {
		t.Fatalf("位数应被钳制，实际新名长度 %d（%q）", len(got), got)
	}
}

// TestExecuteRenamePlanRejectsChangedFile 回归 v3.25.0 P1-2：
// 计划生成到执行之间文件被换掉时，执行必须拒绝，不能把没参与计划的新文件改走。
func TestExecuteRenamePlanRejectsChangedFile(t *testing.T) {
	e, _, root := newMoveDirEngine(t)
	ctx := context.Background()
	src := filepath.Join(root, "a.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	methods := []RenameMethod{{Type: "add", Text: "new_", Position: "head"}}
	plan := e.BuildRenamePlan(ctx, []string{src}, methods)
	if len(plan) != 1 || plan[0].Error != "" || plan[0].Size == 0 || plan[0].Mtime == 0 {
		t.Fatalf("计划应带 size/mtime 快照：%+v", plan)
	}
	// 执行前把 old_path 换成另一份内容（模拟用户挪走了旧文件、放了新的进来）。
	if err := os.WriteFile(src, []byte("hello world, changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.ExecuteRenamePlan(ctx, plan, methods, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Error == "" {
		t.Fatalf("文件已变化时应拒绝执行：%+v", res.Items)
	}
	if _, err := os.Stat(filepath.Join(root, "new_a.txt")); err == nil {
		t.Fatal("不应把换过的文件改名")
	}
	if b, err := os.ReadFile(src); err != nil || string(b) != "hello world, changed" {
		t.Fatal("原路径上的新文件不应被动")
	}
}
