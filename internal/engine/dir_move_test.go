// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"verify/internal/config"
	"verify/internal/store"
)

// newMoveDirEngine 构造带真实数据库与写锁的引擎，扫描卷指向临时目录。
func newMoveDirEngine(t *testing.T) (*Engine, *store.Store, string) {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	// v3.17.3（F-02）：Volumes 只接受 /vol 下的路径，测试的临时目录改走
	// 与生产同源的扫描根环境变量（Volumes 为空时 engine.Volumes() 读它）。
	t.Setenv("VERIFY_SCAN_ROOTS", root)
	return New(st, cfg, NewBus(), root), st, root
}

// TestMoveDirEndToEnd 覆盖 v3.14.0 的主链路：
// 移动目录 → 索引前缀改写 → 撤销 → 索引改回。全程对真实文件系统断言。
func TestMoveDirEndToEnd(t *testing.T) {
	e, st, root := newMoveDirEngine(t)
	ctx := context.Background()

	src := filepath.Join(root, "剧集A")
	sub := filepath.Join(src, "第一季")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	f1 := filepath.Join(src, "a.mkv")
	f2 := filepath.Join(sub, "b.mkv")
	for _, f := range []string{f1, f2} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 两个文件入索引
	for _, f := range []string{f1, f2} {
		e.UpdatePath(f)
	}
	// 一条健康结论挂在其中一个文件上
	if err := st.AppendHealthResults(ctx, "name", []store.HealthResult{
		{Path: f1, Reason: "space", Size: 1},
	}); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(root, "整理", "剧集A")
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		t.Fatal(err)
	}
	res := e.MoveDir(ctx, src, dst)
	if res.Error != "" {
		t.Fatalf("目录移动失败：%s", res.Error)
	}
	if res.Files != 2 {
		t.Fatalf("期望索引改写 2 行，实际 %d", res.Files)
	}
	// 磁盘断言：源消失、目标在
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("源目录应已不存在")
	}
	if _, err := os.Stat(filepath.Join(dst, "第一季", "b.mkv")); err != nil {
		t.Fatalf("目标下应有子目录文件：%v", err)
	}
	// 索引断言：新路径 2 行，旧路径 0 行
	for _, p := range []string{filepath.Join(dst, "a.mkv"), filepath.Join(dst, "第一季", "b.mkv")} {
		if row, err := st.GetFileByPath(ctx, p); err != nil || row == nil {
			t.Fatalf("索引应含新路径 %s：%v %v", p, err, row)
		}
	}
	if row, _ := st.GetFileByPath(ctx, f1); row != nil {
		t.Fatal("旧路径索引行应已改写")
	}
	// 健康结论跟着走
	var n int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM health_results WHERE path=?`, filepath.Join(dst, "a.mkv")).Scan(&n); err != nil || n != 1 {
		t.Fatalf("健康结论应跟随到新路径：%d（err=%v）", n, err)
	}

	// 撤销：目录回原位、索引改回
	done, failed, err := e.UndoMoveDir(ctx, res.TaskID)
	if err != nil || failed != 0 || done != 1 {
		t.Fatalf("撤销失败：done=%d failed=%d err=%v", done, failed, err)
	}
	if _, err := os.Stat(f1); err != nil {
		t.Fatalf("撤销后源文件应回原位：%v", err)
	}
	if row, err := st.GetFileByPath(ctx, f1); err != nil || row == nil {
		t.Fatalf("撤销后索引应有旧路径：%v %v", err, row)
	}
}

// TestMoveDirRejectsIllegalTargets 覆盖预检边界：自身子目录、目标已存在、源不存在。
func TestMoveDirRejectsIllegalTargets(t *testing.T) {
	e, _, root := newMoveDirEngine(t)
	ctx := context.Background()

	src := filepath.Join(root, "dirA")
	if err := os.MkdirAll(src, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "x.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 移进自己的子目录（上级目录不存在时同样必须被拒——拒绝即可，具体文案不锁死）
	res := e.MoveDirPreview(ctx, src, filepath.Join(src, "sub", "dirA"))
	if res.Error == "" {
		t.Fatal("子目录目标应被拒绝，实际放行")
	}
	// 目标已存在同名目录
	if err := os.MkdirAll(filepath.Join(root, "dirB"), 0o750); err != nil {
		t.Fatal(err)
	}
	res = e.MoveDirPreview(ctx, src, filepath.Join(root, "dirB"))
	if res.Error != "目标位置已存在同名目录" {
		t.Fatalf("已存在目标应被拒绝，实际 %q", res.Error)
	}
	// 源不存在
	res = e.MoveDirPreview(ctx, filepath.Join(root, "不存在"), filepath.Join(root, "dirC"))
	if res.Error != "源目录不存在或不是目录" {
		t.Fatalf("不存在的源应被拒绝，实际 %q", res.Error)
	}
}
