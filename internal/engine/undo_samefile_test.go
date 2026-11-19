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

// TestUndoHealthTaskSameFileAllowed 回归 v4.1.2：大小写不敏感卷上「只改大小写」
// 的改名撤销时，原路径与新路径是同一 inode。旧逻辑见 OriginalPath 存在就判「已占用」
// 直接失败，撤销在那类卷上永远失效。用硬链接模拟同 inode，锁住 SameFile 放行。
func TestUndoHealthTaskSameFileAllowed(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()

	newPath := filepath.Join(root, "photo.jpg")
	if err := os.WriteFile(newPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 硬链接：orig 与 new 指向同一 inode（等价大小写卷上的同名文件）。
	origPath := filepath.Join(root, "PHOTO.JPG")
	if err := os.Link(newPath, origPath); err != nil {
		t.Skipf("测试环境不支持硬链接：%v", err)
	}

	taskID, err := st.NewAuditTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddAudit(ctx, store.AuditEntry{
		TaskID: taskID, Operation: "rename", OriginalPath: origPath, NewPath: newPath,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := e.UndoHealthTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 || res.Done != 1 {
		t.Fatalf("同 inode 的撤销应成功，实际 %+v", res)
	}
	// 核心断言：不再误判「原文件名已被占用」。硬链接是同一 inode，
	// renameNoOverwrite 按 SameFile 放行后两个路径都指向同一份数据，内容不能丢。
	data, err := os.ReadFile(origPath)
	if err != nil || string(data) != "x" {
		t.Fatalf("撤销后原路径内容应完整：data=%q err=%v", data, err)
	}
}
