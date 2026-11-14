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

// newRecoverTestEngine 手工组装 Engine，不调用 New：
// New 会异步启动 startupRecover，与用例里显式触发的恢复争抢同一批数据，
// 断言会随机失败。这里只保留恢复流程用到的字段。
func newRecoverTestEngine(t *testing.T, ctx context.Context) (*Engine, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{st: st, cfg: cfg, Bus: NewBus(), baseCtx: ctx, baseCancel: func() {}}, st
}

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRecoverRenameIntentsRepairsOrphans 覆盖 P1-03：改名成功但审计/索引没写完的文件，
// 启动恢复凭 pending 意图行 + 磁盘现状补记录；没改的文件标 failed；
// 新旧路径都在的行标 ambiguous，恢复过程不碰。
func TestRecoverRenameIntentsRepairsOrphans(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newRecoverTestEngine(t, ctx)

	// 已改名：旧路径不存在、新路径存在，索引还停在旧路径。
	oldPath := filepath.Join(root, "old.bin")
	newPath := filepath.Join(root, "new.bin")
	touch(t, newPath)
	// 未改动：旧路径还在，目标不存在。
	oldKeep := filepath.Join(root, "keep.bin")
	newKeep := filepath.Join(root, "keep_new.bin")
	touch(t, oldKeep)
	// 状态不明：新旧都在。
	oldBoth := filepath.Join(root, "both.bin")
	newBoth := filepath.Join(root, "both_new.bin")
	touch(t, oldBoth)
	touch(t, newBoth)

	if _, err := st.DB().Exec(`INSERT INTO files(path,name,size,status) VALUES(?,?,1,'active')`, oldPath, "old.bin"); err != nil {
		t.Fatal(err)
	}
	taskID := int64(9001)
	batchID, err := st.CreateRenameBatchRunning(ctx, 0, 3, "[]", taskID)
	if err != nil {
		t.Fatal(err)
	}
	pairs := [][2]string{{oldPath, newPath}, {oldKeep, newKeep}, {oldBoth, newBoth}}
	if err := st.AddRenameIntents(ctx, batchID, taskID, pairs); err != nil {
		t.Fatal(err)
	}

	res, err := e.RecoverRenameIntents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Batches != 1 || res.Repaired != 1 || res.Failed != 1 || res.Ambiguous != 1 {
		t.Fatalf("恢复汇总不对：%+v（期望 repaired=1 failed=1 ambiguous=1）", res)
	}
	// 索引应已从旧路径迁到新路径（文件名同刷）。
	var name string
	if err := st.DB().QueryRow(`SELECT name FROM files WHERE path=?`, newPath).Scan(&name); err != nil {
		t.Fatalf("新路径索引行不存在：%v", err)
	}
	if name != "new.bin" {
		t.Fatalf("索引 name 未刷新：%s", name)
	}
	var oldCount int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM files WHERE path=?`, oldPath).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 {
		t.Fatalf("旧路径索引行应已迁走，实际还剩 %d 条", oldCount)
	}
	// 审计必须补上，否则这批在界面上仍然撤销不了。
	var audits int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE task_id=? AND operation='rename'`, taskID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("期望补写 1 条改名审计，实际 %d 条", audits)
	}
	// v3.10.13（P1）：批次必须当场落终态——还有 ambiguous 行时落 repaired_partial，
	// failed_count 记录待人工确认的条数，不再停在 running 等用户去点「撤销」。
	var status string
	var failedCount int
	if err := st.DB().QueryRow(`SELECT status, failed_count FROM rename_batches WHERE id=?`, batchID).Scan(&status, &failedCount); err != nil {
		t.Fatal(err)
	}
	if status != "repaired_partial" || failedCount != 1 {
		t.Fatalf("批次终态不对：status=%s failed=%d（期望 repaired_partial / 1）", status, failedCount)
	}
	var leftover int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM rename_intents WHERE batch_id=?`, batchID).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Fatalf("批次收尾后意图行应已清理，实际剩 %d 条", leftover)
	}
	// 恢复动作要留痕。
	var ops int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM op_log WHERE action='rename_recover'`).Scan(&ops); err != nil {
		t.Fatal(err)
	}
	if ops != 1 {
		t.Fatalf("期望 1 条 rename_recover 操作记录，实际 %d 条", ops)
	}

	// 再次恢复不应产生任何新动作（幂等）。
	res2, err := e.RecoverRenameIntents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Repaired != 0 || res2.Failed != 0 || res2.Ambiguous != 0 {
		t.Fatalf("第二次恢复不应再处理已定论的意图：%+v", res2)
	}
}

// TestRecoverRenameIntentsClosesEmptyBatch 覆盖「写了意图但一条都没改」的崩溃窗口：
// 批次应收尾为 failed，不再长期挂在界面提示条上。
func TestRecoverRenameIntentsClosesEmptyBatch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newRecoverTestEngine(t, ctx)

	oldPath := filepath.Join(root, "a.bin")
	touch(t, oldPath)
	batchID, err := st.CreateRenameBatchRunning(ctx, 0, 1, "[]", 9002)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddRenameIntents(ctx, batchID, 9002, [][2]string{{oldPath, filepath.Join(root, "b.bin")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RecoverRenameIntents(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := st.DB().QueryRow(`SELECT status FROM rename_batches WHERE id=?`, batchID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("一条都没改的批次应收尾为 failed，实际 %s", status)
	}
}

// TestRecoverRenameIntentsMarksRepaired 覆盖「全部补好、没有歧义」的批次：
// 落 repaired 终态，界面不再把它当成还需要撤销的 running 批次。
func TestRecoverRenameIntentsMarksRepaired(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newRecoverTestEngine(t, ctx)

	oldPath := filepath.Join(root, "a.bin")
	newPath := filepath.Join(root, "b.bin")
	touch(t, newPath)
	batchID, err := st.CreateRenameBatchRunning(ctx, 0, 1, "[]", 9003)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddRenameIntents(ctx, batchID, 9003, [][2]string{{oldPath, newPath}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RecoverRenameIntents(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := st.DB().QueryRow(`SELECT status FROM rename_batches WHERE id=?`, batchID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "repaired" {
		t.Fatalf("补好且无歧义的批次应收尾为 repaired，实际 %s", status)
	}
}
