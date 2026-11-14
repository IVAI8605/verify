// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 本文件回归 P3-2：剪除 / 转码 / 空目录清理的落位与回滚不得退回裸 os.Rename。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// TestPlacementPathsUseNoOverwriteRename 锁定落位与回滚文件的调用点。
// 「检查目标不存在 → 真正改名」之间的抢占窗口无法在单测里稳定复现，
// 因此用语法树扫描锁定调用点，而不是靠运气触发竞态。
//
// relink.go 故意排除：那里的 os.Rename 是「硬链接顶替原文件」的原子替换，
// 目标必须存在，换成 renameNoOverwrite 反而会让功能失效。
func TestPlacementPathsUseNoOverwriteRename(t *testing.T) {
	files := []string{"trim.go", "transcode.go", "emptydir.go", "trash.go", "health_ops.go", "group_rename.go"}
	for _, name := range files {
		bare, guarded := scanRenameCalls(t, name)
		if len(bare) > 0 {
			t.Fatalf("%s 仍有裸 os.Rename 调用点：%v", name, bare)
		}
		if guarded == 0 {
			t.Fatalf("%s 未找到 renameNoOverwrite 调用，落位可能被改回裸改名", name)
		}
	}
}

// scanRenameCalls 统计一个源文件里的裸 os.Rename 调用位置与 renameNoOverwrite 调用数。
func scanRenameCalls(t *testing.T, name string) (bare []string, guarded int) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", name, err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if id, ok := fn.X.(*ast.Ident); ok && id.Name == "os" && fn.Sel.Name == "Rename" {
				bare = append(bare, fset.Position(call.Pos()).String())
			}
		case *ast.Ident:
			if fn.Name == "renameNoOverwrite" {
				guarded++
			}
		}
		return true
	})
	return bare, guarded
}

// TestMoveFileKeepsExistingTarget 覆盖隔离 / 恢复共用的搬运入口：
// 目标已存在时必须拒绝，源文件与目标都原样保留。
func TestMoveFileKeepsExistingTarget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte("SRC"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("VICTIM"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := moveFile(src, dst); err == nil {
		t.Fatal("目标已存在时 moveFile 必须失败，不能覆盖")
	}
	assertContent(t, src, "SRC")
	assertContent(t, dst, "VICTIM")
}

// TestRenameNoOverwriteSamePath 锁定「源与目标相同按成功处理」：转码在非 .mp4 源时
// finalPath 与产物同路径，这里若报冲突会把正常转码误判成替换失败。
func TestRenameNoOverwriteSamePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "same.mp4")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renameNoOverwrite(p, p); err != nil {
		t.Fatalf("同路径改名应成功（no-op），实际 %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("文件不应消失：%v", err)
	}
}

// TestRenameNoOverwriteKeepsEmptyDirTarget 覆盖空目录清理的落位：目标目录即使是空目录
// 也不得替换——裸 os.Rename 会直接顶掉空目录，外部的空目录就此消失。
func TestRenameNoOverwriteKeepsEmptyDirTarget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src-dir")
	dst := filepath.Join(dir, "dst-dir")
	for _, d := range []string{src, dst} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(src, "a.txt")
	if err := os.WriteFile(marker, []byte("SRC"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := renameNoOverwrite(src, dst); err == nil {
		t.Fatal("目标目录已存在时必须失败，不能替换")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("源目录不应被移走：%v", err)
	}
}
