// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestScanSkipDeleteMissingOnUntrustedVolume 覆盖 v3.25.2 的扫描信任门禁：
// 卷不可信（模拟已卸载、挂载点仍是空目录）时，扫描收尾的 DeleteMissing 必须不执行，
// 旧索引与用户标签都要留着；对照组（卷可信）必须清掉，证明门禁是唯一差异来源。
//
// 「卷已卸载」在临时目录里造不出来，因此用 volumeTrustFn 桩模拟；这也是该桩存在的原因。
func TestScanSkipDeleteMissingOnUntrustedVolume(t *testing.T) {
	// run 跑一次完整扫描，返回幽灵行与它标签的剩余数量。
	// missingVol=true 时模拟「配置里声明了第二个卷，但卷目录整个消失」（v3.25.2 P2-4）。
	run := func(t *testing.T, untrusted, missingVol bool) (ghostLeft, tagLeft int) {
		root := t.TempDir()
		if missingVol {
			// 声明两个卷：一个真实存在（本次会扫到），一个目录不存在。
			gone := filepath.Join(t.TempDir(), "vol-gone")
			t.Setenv("DUP_CLEAN_SCAN_ROOTS", root+string(os.PathListSeparator)+gone)
		} else {
			useScanRoot(t, root)
		}
		e, st := newHealthTestEngine(t)
		ctx := t.Context()

		if err := os.WriteFile(filepath.Join(root, "keep.bin"), bytes.Repeat([]byte("k"), 16), 0o600); err != nil {
			t.Fatalf("写测试文件失败：%v", err)
		}
		// 代数预置为 5：本次扫描 gen=6，收尾会删 last_seen<5 的行，幽灵行的 1 正好命中。
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO meta(key,value) VALUES('generation','5')
			 ON CONFLICT(key) DO UPDATE SET value='5'`); err != nil {
			t.Fatalf("预置扫描代数失败：%v", err)
		}
		ghost := filepath.Join(root, "ghost.bin")
		res, err := st.DB().ExecContext(ctx,
			`INSERT INTO files(path,volume,name,size,mtime,ctime,nlink,inode,dev,ext,status,last_seen)
			 VALUES(?,?,?,?,0,0,1,0,0,'bin','active',1)`, ghost, root, "ghost.bin", 8)
		if err != nil {
			t.Fatalf("插入幽灵索引失败：%v", err)
		}
		fileID, _ := res.LastInsertId()
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO file_tags(file_id,tag_id,value) VALUES(?,1,'重要')`, fileID); err != nil {
			t.Fatalf("插入用户标签失败：%v", err)
		}

		if untrusted {
			old := volumeTrustFn
			volumeTrustFn = func(string) (bool, string) { return false, "模拟卷已卸载" }
			t.Cleanup(func() { volumeTrustFn = old })
		}

		if err := e.StartScanMode(ScanModeFull); err != nil {
			t.Fatalf("启动扫描失败：%v", err)
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) && e.IsRunning() {
			time.Sleep(20 * time.Millisecond)
		}
		if e.IsRunning() {
			t.Fatal("扫描超时未结束")
		}

		if err := st.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM files WHERE path=?`, ghost).Scan(&ghostLeft); err != nil {
			t.Fatalf("查询幽灵行失败：%v", err)
		}
		if err := st.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM file_tags WHERE file_id=?`, fileID).Scan(&tagLeft); err != nil {
			t.Fatalf("查询标签失败：%v", err)
		}
		return ghostLeft, tagLeft
	}

	t.Run("卷不可信时保留索引与标签", func(t *testing.T) {
		left, tags := run(t, true, false)
		if left != 1 || tags != 1 {
			t.Fatalf("卷不可信时不应清理（等价于 removed==0）：files=%d tags=%d", left, tags)
		}
	})

	t.Run("卷目录整个消失时保留索引与标签", func(t *testing.T) {
		left, tags := run(t, false, true)
		if left != 1 || tags != 1 {
			t.Fatalf("声明卷不可访问时不应清理：files=%d tags=%d", left, tags)
		}
	})

	t.Run("卷可信时按旧逻辑清理", func(t *testing.T) {
		left, tags := run(t, false, false)
		if left != 0 || tags != 0 {
			t.Fatalf("卷可信时应清掉失效索引与标签：files=%d tags=%d", left, tags)
		}
	})
}
