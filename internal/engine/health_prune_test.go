// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"verify/internal/health"
	"verify/internal/store"
)

// TestPruneMissingHealth 覆盖 v4.2.0 幽灵行复核：文件不在的结论被清掉，在的保留。
func TestPruneMissingHealth(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()

	alive := filepath.Join(root, "alive.mp4")
	if err := os.WriteFile(alive, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(root, "gone.mp4")
	if err := os.WriteFile(gone, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendHealthResults(ctx, "broken", []store.HealthResult{
		{Path: alive, Reason: "invalid_data", Size: 1},
		{Path: gone, Reason: "invalid_data", Size: 1},
	}); err != nil {
		t.Fatal(err)
	}
	// 外部工具把这个文件搬走了：结论应被复核清掉
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	res, err := e.PruneMissingHealth(ctx, "broken")
	if err != nil {
		t.Fatalf("复核失败：%v", err)
	}
	if res.Removed != 1 {
		t.Fatalf("应清理 1 条，实际 %d（checked=%d skipped=%d）", res.Removed, res.Checked, res.Skipped)
	}
	items, _ := st.ListHealthResults(ctx, "broken", 100, 0)
	if len(items) != 1 || items[0].Path != alive {
		t.Fatalf("剩余结论应只有 %s，实际 %+v", alive, items)
	}
}

// TestPruneMissingHealthSizeChanged 文件被换成大小不同的另一个文件时旧结论也应失效。
func TestPruneMissingHealthSizeChanged(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()
	p := filepath.Join(root, "a.mp4")
	if err := os.WriteFile(p, []byte("newcontent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendHealthResults(ctx, "broken", []store.HealthResult{{Path: p, Reason: "invalid_data", Size: 3}}); err != nil {
		t.Fatal(err)
	}
	res, err := e.PruneMissingHealth(ctx, "broken")
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 1 {
		t.Fatalf("大小变了应清理，实际 removed=%d", res.Removed)
	}
}

// TestIgnoreHealthFiles 覆盖「标为误报」：记录忽略并删除当前结论；文件变了不再忽略。
func TestIgnoreHealthFiles(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()

	p := filepath.Join(root, "fp.mp4")
	if err := os.WriteFile(p, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendHealthResults(ctx, "broken", []store.HealthResult{{Path: p, Reason: "invalid_data", Size: 4}}); err != nil {
		t.Fatal(err)
	}
	res := e.IgnoreHealthFiles(ctx, "broken", []string{p})
	if res.Done != 1 || res.Failed != 0 {
		t.Fatalf("标记误报应成功：%+v", res)
	}
	if items, _ := st.ListHealthResults(ctx, "broken", 100, 0); len(items) != 0 {
		t.Fatalf("标记后健康结论应删除，实际 %+v", items)
	}
	ig, _ := st.ListHealthIgnore(ctx)
	if len(ig) != 1 || ig[0].Path != p {
		t.Fatalf("忽略表应有 1 条，实际 %+v", ig)
	}
	// 文件未变：候选被过滤
	if got := e.filterIgnoredPaths(ctx, []string{p}); len(got) != 0 {
		t.Fatalf("文件未变时应过滤掉，实际 %v", got)
	}
	// 文件变了：忽略失效，重新参与检测
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	if got := e.filterIgnoredPaths(ctx, []string{p}); len(got) != 1 {
		t.Fatalf("文件变了应重新参与检测，实际 %v", got)
	}
	// 取消忽略
	if n, err := e.UnignoreHealth(ctx, []string{p}); err != nil || n != 1 {
		t.Fatalf("取消忽略失败：n=%d err=%v", n, err)
	}
}

// TestRenameAllHealth 覆盖 v4.2.0「按建议一键修正整类」：
// 只处理可修好的条目，跳过「无法确定」的提示项。
func TestRenameAllHealth(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()

	fixable := filepath.Join(root, "photo.mp4")
	if err := os.WriteFile(fixable, pngHead, 0o600); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(root, "weird.mkv")
	if err := os.WriteFile(unknown, []byte("nothing here"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendHealthResults(ctx, "ext", []store.HealthResult{
		{Path: fixable, Reason: "mismatch", Reasons: []string{"mismatch"}, Size: int64(len(pngHead))},
		{Path: unknown, Reason: "unknown", Reasons: []string{"unknown"}, Size: 12},
	}); err != nil {
		t.Fatal(err)
	}
	res := e.RenameAllHealth(ctx, "ext", "", health.NameOptions{}, false)
	if res.Done != 1 || res.Failed != 0 {
		t.Fatalf("应改名 1 个，实际 done=%d failed=%d items=%+v", res.Done, res.Failed, res.Items)
	}
	if _, err := os.Lstat(filepath.Join(root, "photo.png")); err != nil {
		t.Fatalf("photo.mp4 应改成 photo.png：%v", err)
	}
	if _, err := os.Lstat(unknown); err != nil {
		t.Fatalf("unknown 条目不应被处理：%v", err)
	}
}
