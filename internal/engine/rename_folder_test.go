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

// TestUndoFolderRenameRefusesOccupiedTarget 覆盖 P0：撤销「按文件夹名重命名」时，
// 原路径已被别的文件占用必须拒绝。Linux 的 rename 会静默覆盖目标，
// 被覆盖的文件既不进隔离区也没有副本，这条路径之前漏了存在性检查。
func TestUndoFolderRenameRefusesOccupiedTarget(t *testing.T) {
	e, _ := newHealthTestEngine(t)
	ctx := context.Background()
	dir := t.TempDir()

	newPath := filepath.Join(dir, "folder_001.mkv")
	origPath := filepath.Join(dir, "folder.mkv")
	if err := os.WriteFile(newPath, []byte("renamed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(origPath, []byte("occupied"), 0o644); err != nil {
		t.Fatal(err)
	}
	taskID, err := e.st.NewAuditTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddAudit(ctx, store.AuditEntry{
		TaskID: taskID, Operation: "folder_rename", OriginalPath: origPath, NewPath: newPath,
	}); err != nil {
		t.Fatal(err)
	}

	done, failed, uerr := e.UndoFolderRename(ctx, taskID)
	if uerr != nil || done != 0 || failed != 1 {
		t.Fatalf("原路径被占用时应拒绝撤销：done=%d failed=%d err=%v", done, failed, uerr)
	}
	if b, _ := os.ReadFile(origPath); string(b) != "occupied" {
		t.Fatalf("原路径内容被覆盖：%q", string(b))
	}
	if _, err := os.Lstat(newPath); err != nil {
		t.Fatal("改名后的文件不应被移动或删除")
	}
}

// TestClassifyStem A/B 类判定。
func TestClassifyStem(t *testing.T) {
	cases := []struct {
		stem string
		want string
	}{
		{"IMG_1234", "serial"},
		{"DSC_0001", "serial"},
		{"dji_0007", "serial"},
		{"001", "serial"},
		{"20230101", "serial"},
		{"P1010261", "serial"},
		{"沙丘", "keep"},
		{"holiday", "keep"},
		{"第01集", "keep"},
		{"movie 2023", "keep"},
		{"", "keep"},
	}
	for _, c := range cases {
		if got := ClassifyStem(c.stem); got != c.want {
			t.Fatalf("ClassifyStem(%q)=%q，期望 %q", c.stem, got, c.want)
		}
	}
}

// TestLastDigits。
func TestLastDigits(t *testing.T) {
	if lastDigits("IMG_1234") != "1234" {
		t.Fatal("IMG_1234 应提取 1234")
	}
	if lastDigits("001") != "001" {
		t.Fatal("001 应提取 001")
	}
	if lastDigits("holiday") != "" {
		t.Fatal("无数字应为空")
	}
}

// TestFolderRenamePlan 计划与撞名处理（纯文件系统，POSIX 语义）。
func TestFolderRenamePlan(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	// 不建目录，直接构造：folderRenamePlan 依赖 ReadDir；用 t.TempDir 验证 B 类序号与撞名
	// Linux 语义在 CI 验证；Windows 下路径函数可用，ReadDir 也可用。
	dir := t.TempDir()
	for _, n := range []string{"IMG_1234.jpg", "DSC_0001.jpg", "holiday.jpg", "001.png"} {
		if err := osWrite(dir, n); err != nil {
			t.Fatal(err)
		}
	}
	p, err := folderRenamePlan(dir, FolderRenameOptions{})
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Base(dir)
	got := map[string]string{}
	for _, it := range p.Items {
		got[it.Old] = it.New
	}
	if got["IMG_1234.jpg"] != folder+"_1234.jpg" {
		t.Fatalf("B 类应替换为 <文件夹名>_<序号>.<ext>，实际 %q", got["IMG_1234.jpg"])
	}
	if got["001.png"] != folder+"_001.png" {
		t.Fatalf("纯数字应保留序号补零，实际 %q", got["001.png"])
	}
	if got["holiday.jpg"] != "holiday.jpg" {
		t.Fatalf("A 类默认不动，实际 %q", got["holiday.jpg"])
	}
	if p.RenameCount != 3 {
		t.Fatalf("RenameCount=%d，期望 3", p.RenameCount)
	}
	// A 类加前缀
	p2, err := folderRenamePlan(dir, FolderRenameOptions{PrefixA: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range p2.Items {
		if it.Old == "holiday.jpg" && it.New != folder+" - holiday.jpg" {
			t.Fatalf("A 类前缀格式错误：%q", it.New)
		}
	}
}

func osWrite(dir, name string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600)
}
