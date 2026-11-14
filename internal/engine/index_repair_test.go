// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepairIndexRemovesGhosts 覆盖 P1-02：磁盘上已不存在的索引行会被清理，
// 真实文件不受影响；dryRun 只统计不删；不在线卷内的行一律跳过。
func TestRepairIndexRemovesGhosts(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()

	realFile := filepath.Join(root, "real.bin")
	if err := os.WriteFile(realFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ghosts := []string{filepath.Join(root, "gone1.bin"), filepath.Join(root, "gone2.bin")}
	tx, err := st.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range append([]string{realFile}, ghosts...) {
		if _, err := tx.Exec(`INSERT INTO files(path,name,size,status) VALUES(?,?,1,'active')`, p, filepath.Base(p)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	// 模拟离线卷上的索引行：假路径不在任何在线卷内，必须跳过而不是删掉。
	offline := "/vol9/offline/gone.bin"
	if _, err := tx.Exec(`INSERT INTO files(path,name,size,status) VALUES(?,?,1,'active')`, offline, "gone.bin"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO health_results(kind,path,reason) VALUES('broken',?,'invalid_data')`, ghosts[0]); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	dry, err := e.RepairIndex(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || dry.Checked != 4 || dry.Removed != 0 || dry.WouldRemove != 2 || dry.Skipped != 1 {
		t.Fatalf("dryRun 结果不对：%+v（期望 checked=4 removed=0 would_remove=2 skipped=1）", dry)
	}
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("dryRun 不应删行，实际剩 %d 条", n)
	}

	got, err := e.RepairIndex(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Removed != 2 || got.Skipped != 1 {
		t.Fatalf("实际清理结果不对：%+v（期望 removed=2 skipped=1）", got)
	}
	for _, p := range append([]string{realFile, offline}, ghosts...) {
		var cnt int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM files WHERE path=?`, p).Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		want := 1
		if p == ghosts[0] || p == ghosts[1] {
			want = 0
		}
		if cnt != want {
			t.Fatalf("%s 期望 %d 条，实际 %d 条", p, want, cnt)
		}
	}
	var health int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM health_results WHERE path=?`, ghosts[0]).Scan(&health); err != nil {
		t.Fatal(err)
	}
	if health != 0 {
		t.Fatalf("幽灵路径的健康结论应同删，实际剩 %d 条", health)
	}
	// 清理动作必须留审计：结果页与操作记录都要能复盘。
	var ops int
	if err := st.DB().QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM op_log WHERE action='index_repair'`)).Scan(&ops); err != nil {
		t.Fatal(err)
	}
	if ops != 1 {
		t.Fatalf("期望 1 条 index_repair 操作记录，实际 %d 条", ops)
	}
}

// TestRepairIndexSkipsUnmountedVolume 覆盖 v3.10.13 P0：
// 卷不可信（未挂载 / 文件系统不可读）时，该卷下所有 ENOENT 都不能当幽灵行，
// 否则一块离线硬盘的索引会被整卷删光，重扫是小时到天级代价。
func TestRepairIndexSkipsUnmountedVolume(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()
	// 临时目录在真实环境里同设备，无法复现「挂载点还在但卷不可读」，
	// 用探测入口返回不可信来模拟；断言必须一行都不删。
	old := volumeTrustFn
	volumeTrustFn = func(string) (bool, string) { return false, "模拟卷已卸载" }
	t.Cleanup(func() { volumeTrustFn = old })

	ghosts := []string{filepath.Join(root, "g1.bin"), filepath.Join(root, "g2.bin"), filepath.Join(root, "g3.bin")}
	tx, err := st.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ghosts {
		if _, err := tx.Exec(`INSERT INTO files(path,name,size,status) VALUES(?,?,1,'active')`, p, filepath.Base(p)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got, err := e.RepairIndex(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Removed != 0 {
		t.Fatalf("卷不可信时不允许删任何行，实际删了 %d 条", got.Removed)
	}
	if got.Skipped != 3 || len(got.SkippedVolumes) != 1 || got.SkippedVolumes[0].Rows != 3 {
		t.Fatalf("跳过统计不对：%+v", got)
	}
	var left int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM files`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 3 {
		t.Fatalf("卷不可信时索引行应全部保留，实际剩 %d 条", left)
	}
}

// TestRepairIndexSkipsWhenVolumeRootGone 用真实磁盘状态模拟卷根消失：
// 卷根被删除/改名后，它已经不在配置卷列表内，整卷行必须跳过而不是判幽灵。
func TestRepairIndexSkipsWhenVolumeRootGone(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()

	ghosts := []string{filepath.Join(root, "gone1.bin"), filepath.Join(root, "gone2.bin")}
	tx, err := st.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ghosts {
		if _, err := tx.Exec(`INSERT INTO files(path,name,size,status) VALUES(?,?,1,'active')`, p, filepath.Base(p)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 卷根消失 = 硬盘离线 / 挂载点被卸载，此时所有子路径的 ENOENT 都不能作为删除依据。
	if err := os.Rename(root, root+"-offline"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root + "-offline") })

	got, err := e.RepairIndex(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Removed != 0 || got.Skipped != 2 {
		t.Fatalf("卷根消失时应全部跳过，实际 %+v", got)
	}
	var left int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM files`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 2 {
		t.Fatalf("卷根消失时索引行应全部保留，实际剩 %d 条", left)
	}
}

// TestVolumeTrustRejectsMissingRoot 直接验证卷根探测：根不存在时必须判不可信。
func TestVolumeTrustRejectsMissingRoot(t *testing.T) {
	usable, reason := volumeTrust(filepath.Join(t.TempDir(), "not-mounted"))
	if usable || reason == "" {
		t.Fatalf("不存在的卷根应判不可信，实际 usable=%v reason=%q", usable, reason)
	}
}

// TestRepairIndexBlocksScanStart 覆盖 v3.10.13 P1：体检期间不允许启动扫描。
// 扫描收尾会批量 Upsert 索引，与体检的批量删除并发会互相覆盖。
func TestRepairIndexBlocksScanStart(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, _ := newHealthTestEngine(t)
	e.indexRepair.Store(true)
	defer e.indexRepair.Store(false)
	err := e.StartScanMode(ScanModeFull)
	if err == nil || !strings.Contains(err.Error(), "索引体检进行中") {
		t.Fatalf("体检期间应拒绝扫描启动，实际 err=%v", err)
	}
}
