// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"verify/internal/health"
	"verify/internal/store"
)

// pngHead 是一段足以被嗅探识别为 PNG 的文件头。
var pngHead = append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, 64)...)

// headMP4ForEngine 是一段足以被 Sniff 识别为 mp4 的文件头。
func headMP4ForEngine() []byte {
	return append([]byte{0x00, 0x00, 0x00, 0x18}, []byte("ftypisom")...)
}

// useScanRoot 把临时目录登记为受管卷，让 VolumeOf / ValidPath 生效。
func useScanRoot(t *testing.T, root string) {
	t.Helper()
	t.Setenv("DUP_CLEAN_SCAN_ROOTS", root)
}

// TestRenameHealthFilesRejectsUnknownExt 内容认不出时不允许改名（v3.15.0）。
// 不确定就不猜目标扩展名：文件必须保持原样，失败原因要说明「无法识别」。
func TestRenameHealthFilesRejectsUnknownExt(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)

	src := filepath.Join(root, "movie.mkv")
	if err := os.WriteFile(src, []byte("not a real mkv"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.RenameHealthFiles(context.Background(), health.KindExt, []string{src}, health.NameOptions{}, false)
	if res.Done != 0 || res.Failed != 1 {
		t.Fatalf("内容认不出时应拒绝改名，实际 %+v", res)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("原文件必须保持原样")
	}
	if len(res.Items) != 1 || !strings.Contains(res.Items[0].Error, "无法识别") {
		t.Fatalf("失败原因应说明内容无法识别，实际 %+v", res.Items)
	}
}

// TestDryRunHealthRename 回归 2026-11-25 建议 8：dry-run 只给计划不落盘，
// 每条带目标扩展名与风险分类，供脚本先全量预演。
func TestDryRunHealthRename(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)

	// 大写扩展名 → 计划转小写
	upper := filepath.Join(root, "MOVIE.MP4")
	if err := os.WriteFile(upper, headMP4ForEngine(), 0o600); err != nil {
		t.Fatal(err)
	}
	// 内容认不出 → 风险提示
	unknown := filepath.Join(root, "movie.mkv")
	if err := os.WriteFile(unknown, []byte("totally unknown bytes here"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.DryRunHealthRename(context.Background(), health.KindExt,
		[]string{upper, unknown}, health.NameOptions{}, false)
	if len(res.Items) != 2 {
		t.Fatalf("dry-run 应逐条返回，实际 %d 条：%+v", len(res.Items), res.Items)
	}
	if res.Done != 0 || res.Failed != 0 {
		t.Fatalf("dry-run 不应执行任何改动，实际 done=%d failed=%d", res.Done, res.Failed)
	}
	// 原文件都还在，内容没变
	for _, p := range []string{upper, unknown} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("dry-run 不得改磁盘：%s 不见了", p)
		}
	}
	// 能识别出一条有计划、一条有风险
	var hasPlan, hasRisk bool
	for _, it := range res.Items {
		if it.NewPath != "" {
			hasPlan = true
		}
		if it.Error != "" {
			hasRisk = true
		}
	}
	if !hasPlan || !hasRisk {
		t.Fatalf("dry-run 应同时给出计划与风险，实际 %+v", res.Items)
	}
}

// TestClassifyRenameFailure 回归 2026-11-25 建议 7：源路径已不存在（父目录被前一批改名）
// 要单列 stale_path，不能和普通 failed 混在一起让脚本误读成"没改成"。
func TestClassifyRenameFailure(t *testing.T) {
	root := t.TempDir()
	exists := filepath.Join(root, "exists.txt")
	if err := os.WriteFile(exists, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Lstat(filepath.Join(root, "gone.txt"))
	if statErr == nil {
		t.Fatal("测试前提：gone.txt 应不存在")
	}
	if got := classifyRenameFailure(filepath.Join(root, "gone.txt"), statErr); got != "stale_path" {
		t.Fatalf("源路径不存在应判 stale_path，实际 %q", got)
	}
	if got := classifyRenameFailure(exists, nil); got != "" {
		t.Fatalf("源路径存在应判普通失败，实际 %q", got)
	}
}

// TestUndoHealthTaskIdempotent 回归 2026-11-25 建议 9：重复撤销同一 task_id
// 应返回「已撤销」而不是逐条失败，方便脚本重试。
func TestUndoHealthTaskIdempotent(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)

	src := filepath.Join(root, "MOVIE.MP4")
	if err := os.WriteFile(src, headMP4ForEngine(), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.RenameHealthFiles(context.Background(), health.KindExt, []string{src}, health.NameOptions{}, false)
	if res.Done != 1 {
		t.Fatalf("准备阶段大写扩展名改名应成功：%+v", res)
	}
	// 第一次撤销
	if _, err := e.UndoHealthTask(context.Background(), res.TaskID); err != nil {
		t.Fatalf("首次撤销失败：%v", err)
	}
	// 第二次撤销：应幂等返回，不报失败
	again, err := e.UndoHealthTask(context.Background(), res.TaskID)
	if err != nil {
		t.Fatalf("重复撤销不应报错：%v", err)
	}
	if again.Failed != 0 {
		t.Fatalf("重复撤销应视为已撤销，不应失败：%+v", again.Items)
	}
	var allUndone bool
	for _, it := range again.Items {
		if it.Class != "already_undone" {
			t.Fatalf("重复撤销的条目应标 already_undone，实际 %q", it.Class)
		}
		allUndone = true
	}
	if !allUndone || len(again.Items) == 0 {
		t.Fatal("应至少有一条 already_undone 结果")
	}
}

// TestRenameHealthFilesForceUnknown 回归 2026-11-25 建议 4：unknown 类默认拒绝，
// 但调用方用外部工具（ffprobe）实锤后可用 force + 目标扩展名显式执行，且仍可撤销。
func TestRenameHealthFilesForceUnknown(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)

	src := filepath.Join(root, "movie.mkv")
	// 前导乱码但 188/376 是 TS 同步字——现有 Sniff 已能识别为 mpegts，
	// 这里用真正认不出的内容来验证 force 通道。
	if err := os.WriteFile(src, []byte("totally unknown bytes here"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 不带 force 仍拒绝
	res := e.RenameHealthFiles(context.Background(), health.KindExt, []string{src}, health.NameOptions{}, false)
	if res.Done != 0 || res.Failed != 1 {
		t.Fatalf("无 force 时 unknown 应拒绝，实际 %+v", res)
	}
	// force + 合法扩展名 → 改名成功
	res = e.RenameHealthFiles(context.Background(), health.KindExt, []string{src}, health.NameOptions{}, false,
		HealthRenameExtra{Force: true, ForceExt: "ts"})
	if res.Done != 1 || res.Failed != 0 {
		t.Fatalf("force 后 unknown 应改名成功，实际 %+v items=%+v", res, res.Items)
	}
	want := filepath.Join(root, "movie.ts")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("应改名为 %s：%v", want, err)
	}
	// 撤销链仍完整
	if _, err := e.UndoHealthTask(context.Background(), res.TaskID); err != nil {
		t.Fatalf("force 改名应可撤销：%v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("撤销后原文件应恢复")
	}
}

// TestRenameHealthFilesForceExtInvalid 非法目标扩展名即使 force 也要拒绝。
func TestRenameHealthFilesForceExtInvalid(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)

	src := filepath.Join(root, "movie.mkv")
	if err := os.WriteFile(src, []byte("totally unknown bytes here"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.RenameHealthFiles(context.Background(), health.KindExt, []string{src}, health.NameOptions{}, false,
		HealthRenameExtra{Force: true, ForceExt: "../evil"})
	if res.Done != 0 || res.Failed != 1 {
		t.Fatalf("非法 force 扩展名应拒绝，实际 %+v", res)
	}
	if len(res.Items) == 0 || !strings.Contains(res.Items[0].Error, "目标扩展名") {
		t.Fatalf("失败原因应说明目标扩展名非法，实际 %+v", res.Items)
	}
}

// TestRenameHealthFilesAndUndo 覆盖改名与撤销的完整往返。
func TestRenameHealthFilesAndUndo(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)

	src := filepath.Join(root, "photo.jpg")
	if err := os.WriteFile(src, pngHead, 0o600); err != nil {
		t.Fatal(err)
	}

	res := e.RenameHealthFiles(context.Background(), health.KindExt, []string{src}, health.NameOptions{}, false)
	if res.Done != 1 || res.Failed != 0 {
		t.Fatalf("改名应成功 1 个，实际 %+v", res)
	}
	dst := filepath.Join(root, "photo.png")
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("目标文件不存在: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("原文件应已改名")
	}

	entries, err := st.ListAuditByTask(context.Background(), res.TaskID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("应有 1 条审计，实际 %d（err=%v）", len(entries), err)
	}
	if entries[0].Operation != "health_rename" || entries[0].NewPath != dst {
		t.Fatalf("审计内容不符: %+v", entries[0])
	}

	undo, err := e.UndoHealthTask(context.Background(), res.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if undo.Done != 1 {
		t.Fatalf("撤销应还原 1 个，实际 %+v", undo)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("原文件应被还原: %v", err)
	}
}

// TestRenameSkipsWhenNoChange 验证合规文件不会被改名，也不计入失败。
func TestRenameSkipsWhenNoChange(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)

	src := filepath.Join(root, "clean.png")
	if err := os.WriteFile(src, pngHead, 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.RenameHealthFiles(context.Background(), health.KindExt, []string{src}, health.NameOptions{}, false)
	if res.Done != 0 || res.Failed != 0 {
		t.Fatalf("无需改动时不应计入成功或失败，实际 %+v", res)
	}
}

// TestRenameRejectsOutsideRoot 验证卷外路径被拒绝。
func TestRenameRejectsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)

	outside := filepath.Join(t.TempDir(), "x.jpg")
	if err := os.WriteFile(outside, pngHead, 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.RenameHealthFiles(context.Background(), health.KindExt, []string{outside}, health.NameOptions{}, false)
	if res.Failed != 1 {
		t.Fatalf("卷外路径应被拒绝，实际 %+v", res)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("被拒绝的文件不应被移动")
	}
}

// TestTrashHealthFile 覆盖移入隔离区：文件离开原位并出现在 quarantine 表。
// 该用例同时验证零字节文件——它不在查重索引里，必须仍然可清理。
func TestTrashHealthFile(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)

	src := filepath.Join(root, "empty.bin")
	if err := os.WriteFile(src, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	res := e.TrashHealthFiles(context.Background(), []string{src})
	if res.Done != 1 || res.Failed != 0 {
		t.Fatalf("移入隔离区应成功 1 个，实际 %+v", res)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("原文件应已移走")
	}
	rows, err := st.ListTrash(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].OrigPath != src {
		t.Fatalf("隔离区记录不符: %+v", rows)
	}
	// 文件应落在 .verify-trash 下，而不是被直接删除
	if !strings.Contains(rows[0].TrashPath, ".verify-trash") {
		t.Fatalf("隔离路径不符: %q", rows[0].TrashPath)
	}
	if _, err := os.Stat(rows[0].TrashPath); err != nil {
		t.Fatalf("隔离区内应存在该文件: %v", err)
	}
}

// TestRenameOnlyUppercase 验证"只转小写"不会碰真正内容不符的文件。
func TestRenameOnlyUppercase(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)

	upper := filepath.Join(root, "MOVIE.MP4")
	if err := os.WriteFile(upper, []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, 0o600); err != nil {
		t.Fatal(err)
	}
	// 内容是 PNG 但扩展名是 .jpg：属于真正的内容不符，不该被"只转小写"处理
	mismatch := filepath.Join(root, "photo.jpg")
	if err := os.WriteFile(mismatch, pngHead, 0o600); err != nil {
		t.Fatal(err)
	}

	res := e.RenameHealthFiles(context.Background(), health.KindExt, []string{upper, mismatch}, health.NameOptions{}, true)
	if res.Done != 1 {
		t.Fatalf("应只处理 1 个，实际 %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "MOVIE.mp4")); err != nil {
		t.Fatal("大写扩展名应被转小写")
	}
	if _, err := os.Stat(mismatch); err != nil {
		t.Fatal("真正内容不符的文件不应被这次操作改动")
	}
}

// TestRenameHealthFiles_IndexSync 回归 v3.10.0 SYNC-01 / SYNC-03：
// 健康页改名只动磁盘、不写回索引是老问题（关掉实时监控就会暴露），
// 且同一路径可能同时命中多类检测，改名后旧路径的健康结论必须整体清掉。
func TestRenameHealthFiles_IndexSync(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()

	src := filepath.Join(root, "photo.jpg")
	if err := os.WriteFile(src, pngHead, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertOne(ctx, store.FileInfo{
		Path: src, Volume: root, Name: "photo.jpg", Size: info.Size(),
		Mtime: info.ModTime().UnixNano(), Ext: "jpg",
	}, 1); err != nil {
		t.Fatal(err)
	}
	// 同一文件同时登记两类健康结论，验证按路径清空而不是按类清空
	for _, kind := range []string{"ext", "name"} {
		if err := st.ReplaceHealthResults(ctx, kind, []store.HealthResult{
			{Kind: kind, Path: src, Reason: "测试"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	res := e.RenameHealthFiles(ctx, health.KindExt, []string{src}, health.NameOptions{}, false)
	if res.Done != 1 {
		t.Fatalf("改名应成功 1 个，实际 %+v", res)
	}
	dst := filepath.Join(root, "photo.png")
	if row, err := st.GetFileByPath(ctx, src); err != nil || row != nil {
		t.Fatalf("旧路径不应留在索引里: row=%+v err=%v", row, err)
	}
	row, err := st.GetFileByPath(ctx, dst)
	if err != nil || row == nil {
		t.Fatalf("新路径应已进索引: row=%+v err=%v", row, err)
	}
	if row.Name != "photo.png" || row.Ext != "png" {
		t.Fatalf("name/ext 未随扩展名更新: name=%q ext=%q", row.Name, row.Ext)
	}
	for _, kind := range []string{"ext", "name"} {
		rows, err := st.ListHealthResults(ctx, kind, 500, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.Path == src {
				t.Fatalf("%s 类仍挂着旧路径死行: %+v", kind, r)
			}
		}
	}
}

// TestUndoHealthTask_IndexSync 回归 v3.10.0 SYNC-02：撤销改名后索引要回到原名。
func TestUndoHealthTask_IndexSync(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()

	src := filepath.Join(root, "photo.jpg")
	if err := os.WriteFile(src, pngHead, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertOne(ctx, store.FileInfo{
		Path: src, Volume: root, Name: "photo.jpg", Size: info.Size(),
		Mtime: info.ModTime().UnixNano(), Ext: "jpg",
	}, 1); err != nil {
		t.Fatal(err)
	}

	res := e.RenameHealthFiles(ctx, health.KindExt, []string{src}, health.NameOptions{}, false)
	if res.Done != 1 {
		t.Fatalf("改名应成功 1 个，实际 %+v", res)
	}
	dst := filepath.Join(root, "photo.png")
	if _, err := e.UndoHealthTask(ctx, res.TaskID); err != nil {
		t.Fatal(err)
	}
	if row, err := st.GetFileByPath(ctx, dst); err != nil || row != nil {
		t.Fatalf("撤销后改后路径不应留在索引里: row=%+v err=%v", row, err)
	}
	row, err := st.GetFileByPath(ctx, src)
	if err != nil || row == nil {
		t.Fatalf("撤销后原路径应回到索引: row=%+v err=%v", row, err)
	}
	if row.Name != "photo.jpg" || row.Ext != "jpg" {
		t.Fatalf("撤销后 name/ext 未还原: name=%q ext=%q", row.Name, row.Ext)
	}
}

// TestRenameHealthFilesSameTargetInOneBatch 回归 v3.10.2 的并发改造：
// 同一批里两个文件算出同一个目标名时，必须串行加后缀，不能因为并发 rename 互相覆盖。
func TestRenameHealthFilesSameTargetInOneBatch(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)

	a := filepath.Join(root, "photo.JPG")
	b := filepath.Join(root, "photo.jpeg")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, pngHead, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	res := e.RenameHealthFiles(context.Background(), health.KindExt, []string{a, b}, health.NameOptions{}, false)
	if res.Done != 2 || res.Failed != 0 {
		t.Fatalf("两个文件都应改名成功，实际 %+v", res)
	}
	// 两个目标文件必须都存在：任何一个被覆盖都说明并发 rename 冲掉了数据。
	names := []string{"photo.png", "photo_1.png"}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("缺少 %s，同批改名可能互相覆盖：%v", name, err)
		}
	}
}

// TestIsProtected 验证保护目录判定：用户配置的目录、隔离区自身、前缀子路径。
func TestIsProtected(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)

	keep := filepath.Join(root, "keep")
	cfg := e.cfg.Get()
	cfg.ProtectPaths = []string{keep}
	if err := e.cfg.Replace(cfg); err != nil {
		t.Fatal(err)
	}

	cases := map[string]bool{
		filepath.Join(keep, "a.txt"): true,
		keep:                         true,
		filepath.Join(root, "keepx"): false, // 前缀相同但不是同一目录
		filepath.Join(root, "o.txt"): false,
	}
	for path, want := range cases {
		if got := e.IsProtected(path); got != want {
			t.Fatalf("IsProtected(%q) = %v，期望 %v", path, got, want)
		}
	}
	// 隔离区永远受保护
	if !e.IsProtected(filepath.Join(root, ".verify-trash", "x.mp4")) {
		t.Fatal("隔离区路径应永远受保护")
	}
}

// TestTrashSkipsProtected 验证保护目录内的文件不会被移入隔离区，同批普通文件不受影响。
func TestTrashSkipsProtected(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)

	keep := filepath.Join(root, "keep")
	if err := os.MkdirAll(keep, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg.Get()
	cfg.ProtectPaths = []string{keep}
	if err := e.cfg.Replace(cfg); err != nil {
		t.Fatal(err)
	}

	// 两个文件都必须在 files 索引里（TrashFiles 的既有校验）
	prot := filepath.Join(keep, "p.txt")
	norm := filepath.Join(root, "n.txt")
	for _, p := range []string{prot, norm} {
		if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(p)
		if _, err := st.UpsertOne(context.Background(), store.FileInfo{
			Path: p, Volume: root, Name: filepath.Base(p), Size: info.Size(),
			Mtime: info.ModTime().UnixNano(), Ext: "txt",
		}, 1); err != nil {
			t.Fatal(err)
		}
	}

	res, err := e.TrashFiles(context.Background(), []string{prot, norm}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Moved) != 1 || res.Moved[0] != norm {
		t.Fatalf("应只移走普通文件，实际 %+v", res)
	}
	if len(res.Failed) != 1 || res.Failed[0] != prot {
		t.Fatalf("保护文件应被拦下，实际 %+v", res)
	}
	if _, err := os.Stat(prot); err != nil {
		t.Fatal("保护文件必须原地不动")
	}
}

// TestTrashHealthFilesProtectsProtectedDir 回归 BUG-03（v1.42.0）：
// 健康页临时文件清理此前只校验卷/存在性/mtime，没有走保护目录判定，
// 导致保护目录里的 .tmp/.bak 也能被移入隔离区。
func TestTrashHealthFilesProtectsProtectedDir(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)

	keep := filepath.Join(root, "keep")
	if err := os.MkdirAll(keep, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg.Get()
	cfg.ProtectPaths = []string{keep}
	if err := e.cfg.Replace(cfg); err != nil {
		t.Fatal(err)
	}

	p := filepath.Join(keep, "p.tmp")
	if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := e.TrashHealthFiles(context.Background(), []string{p})
	if res.Done != 0 || res.Failed != 1 {
		t.Fatalf("保护目录内文件应被拒绝，实际 %+v", res)
	}
	if len(res.Items) != 1 || !strings.Contains(res.Items[0].Error, "受保护") {
		t.Fatalf("失败原因应说明保护目录，实际 %+v", res.Items)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("保护目录内文件必须原地不动")
	}
}
