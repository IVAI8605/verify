// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func openHealthStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestHealthResultReasonsRoundTrip 验证多原因列写入与读取一致（v3.15.0）。
// 未提供 Reasons 的旧式调用要能按单值 reason 回退，不改变界面拿到的原因。
func TestHealthResultReasonsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	items := []HealthResult{
		{Path: "/v/a.mkv", Reason: "unknown", Reasons: []string{"unknown", "uppercase"}},
		{Path: "/v/b.jpg", Reason: "mismatch", Reasons: []string{"mismatch"}},
		{Path: "/v/c.tmp", Reason: "temp"},
	}
	if err := s.ReplaceHealthResults(ctx, "ext", items); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListHealthResults(ctx, "ext", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]HealthResult{}
	for _, r := range got {
		byPath[r.Path] = r
	}
	if rs := byPath["/v/a.mkv"].Reasons; len(rs) != 2 || rs[0] != "unknown" || rs[1] != "uppercase" {
		t.Fatalf("多原因应原样取回，实际 %+v", rs)
	}
	if rs := byPath["/v/c.tmp"].Reasons; len(rs) != 1 || rs[0] != "temp" {
		t.Fatalf("未提供 Reasons 时应回退成 reason，实际 %+v", rs)
	}
	if n, err := s.CountHealthByReason(ctx, "ext", "unknown"); err != nil || n != 1 {
		t.Fatalf("unknown 计数应为 1，实际 %d（err=%v）", n, err)
	}
}

// TestReplaceHealthResults 验证同一 kind 是整体替换，不会残留上一轮条目。
func TestReplaceHealthResults(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	first := []HealthResult{
		{Path: "/v/a.mp4", Reason: "invalid_data", Size: 100},
		{Path: "/v/b.mp4", Reason: "no_moov", Size: 200},
	}
	if err := s.ReplaceHealthResults(ctx, "broken", first); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountHealthResults(ctx, "broken"); err != nil || n != 2 {
		t.Fatalf("期望 2 条，实际 %d（err=%v）", n, err)
	}

	second := []HealthResult{{Path: "/v/c.mp4", Reason: "timeout", Size: 300}}
	if err := s.ReplaceHealthResults(ctx, "broken", second); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListHealthResults(ctx, "broken", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "/v/c.mp4" {
		t.Fatalf("旧结果应被清空，实际 %+v", got)
	}
}

// TestHealthResultsIsolatedByKind 验证四类结果互不干扰。
func TestHealthResultsIsolatedByKind(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	if err := s.ReplaceHealthResults(ctx, "empty", []HealthResult{{Path: "/v/zero.bin", Reason: "zero_size"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceHealthResults(ctx, "ext", []HealthResult{{Path: "/v/a.jpg", Reason: "mismatch", Suggest: "png"}}); err != nil {
		t.Fatal(err)
	}

	counts, err := s.HealthCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["empty"] != 1 || counts["ext"] != 1 {
		t.Fatalf("分类计数错误：%+v", counts)
	}
	if counts["broken"] != 0 {
		t.Fatalf("未检测的类型应为 0：%+v", counts)
	}
}

// TestListHealthResultsOrder 验证按体积降序返回，大文件优先被用户看到。
func TestListHealthResultsOrder(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	items := []HealthResult{
		{Path: "/v/small.mp4", Size: 1},
		{Path: "/v/big.mp4", Size: 1000},
		{Path: "/v/mid.mp4", Size: 100},
	}
	if err := s.ReplaceHealthResults(ctx, "broken", items); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListHealthResults(ctx, "broken", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Path != "/v/big.mp4" {
		t.Fatalf("排序错误：%+v", got)
	}
}

// TestHealthTaskRoundTrip 验证任务摘要能写入并读回最近一条。
func TestHealthTaskRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	if task, err := s.LastHealthTask(ctx, "empty"); err != nil || task != nil {
		t.Fatalf("首次查询应返回 nil，实际 %+v（err=%v）", task, err)
	}

	id, err := s.SaveHealthTask(ctx, HealthTask{Kind: "empty", Status: "done", Total: 10, Processed: 10, Errors: 1, StartedAt: 100, FinishedAt: 200})
	if err != nil || id == 0 {
		t.Fatalf("写入任务失败：id=%d err=%v", id, err)
	}
	task, err := s.LastHealthTask(ctx, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if task == nil || task.Total != 10 || task.Errors != 1 || task.Status != "done" {
		t.Fatalf("回读内容不符：%+v", task)
	}
}

// TestAuditByTask 验证审计记录可按任务取回，用于撤销。
func TestAuditByTask(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	taskID, err := s.SaveHealthTask(ctx, HealthTask{Kind: "name", Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	entries := []AuditEntry{
		{TaskID: taskID, Operation: "rename", OriginalPath: "/v/a.txt", NewPath: "/v/a_1.txt"},
		{TaskID: taskID, Operation: "rename", OriginalPath: "/v/b.txt", NewPath: "/v/b_1.txt"},
		{TaskID: 9999, Operation: "rename", OriginalPath: "/v/other.txt", NewPath: "/v/other_1.txt"},
	}
	for _, e := range entries {
		if err := s.AddAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListAuditByTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应只返回本任务的 2 条，实际 %d", len(got))
	}
	if got[0].OriginalPath != "/v/a.txt" || got[1].NewPath != "/v/b_1.txt" {
		t.Fatalf("顺序或内容不符：%+v", got)
	}

	latest, err := s.LastAuditTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest != 9999 {
		t.Fatalf("最近任务号应为 9999，实际 %d", latest)
	}
}

// TestClearAndAppendHealthResults 验证边扫边写的两个原语：
// 清空后从零开始，追加按批累加，重复路径覆盖而不是报错。
func TestClearAndAppendHealthResults(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	if err := s.ReplaceHealthResults(ctx, "ext", []HealthResult{{Path: "/v/old.jpg", Reason: "mismatch"}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountHealthResults(ctx, "ext"); n != 1 {
		t.Fatalf("预置 1 条，实际 %d", n)
	}

	if err := s.ClearHealthResults(ctx, "ext"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountHealthResults(ctx, "ext"); n != 0 {
		t.Fatalf("清空后应为 0，实际 %d", n)
	}

	if err := s.AppendHealthResults(ctx, "ext", []HealthResult{{Path: "/v/a.jpg", Reason: "mismatch"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendHealthResults(ctx, "ext", []HealthResult{{Path: "/v/b.jpg", Reason: "uppercase"}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountHealthResults(ctx, "ext"); n != 2 {
		t.Fatalf("两批追加后应为 2 条，实际 %d", n)
	}

	// 同一路径再次追加应覆盖，不应触发唯一约束错误
	if err := s.AppendHealthResults(ctx, "ext", []HealthResult{{Path: "/v/a.jpg", Reason: "multi_ext"}}); err != nil {
		t.Fatalf("重复路径应覆盖而不是报错: %v", err)
	}
	items, err := s.ListHealthResults(ctx, "ext", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		if it.Path == "/v/a.jpg" {
			found = true
			if it.Reason != "multi_ext" {
				t.Fatalf("覆盖后的原因应为 multi_ext，实际 %q", it.Reason)
			}
		}
	}
	if !found {
		t.Fatal("覆盖后应仍只有一条 a.jpg 记录")
	}
	// 清空其它类型不应受影响
	if n, _ := s.CountHealthResults(ctx, "empty"); n != 0 {
		t.Fatalf("其它类型应保持 0，实际 %d", n)
	}
}

// TestCountsCache 验证状态聚合的缓存契约：
// TTL 内返回旧值；InvalidateCounts 之后立即反映新数据。
// 引擎侧约定：任何写路径都要调用 InvalidateCounts（bumpVersion 已统一接入）。
func TestCountsCache(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	c1, err := s.Counts(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Files != 0 {
		t.Fatalf("空库应为 0 个文件，实际 %d", c1.Files)
	}

	for _, f := range []FileInfo{
		{Path: "/v/a.mp4", Volume: "/v", Name: "a.mp4", Size: 10, Ext: "mp4"},
		{Path: "/v/b.mp4", Volume: "/v", Name: "b.mp4", Size: 20, Ext: "mp4"},
	} {
		if _, err := s.UpsertOne(ctx, f, 1); err != nil {
			t.Fatal(err)
		}
	}

	// TTL 内不应重新查库：返回的仍是旧值
	c2, err := s.Counts(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Files != 0 {
		t.Fatalf("缓存未失效前应仍是 0 个文件，实际 %d", c2.Files)
	}

	s.InvalidateCounts()
	c3, err := s.Counts(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c3.Files != 2 {
		t.Fatalf("失效后应为 2 个文件，实际 %d", c3.Files)
	}
}

// TestCountsFrozenKeepsCache 验证扫描期间的冻结语义（v3.10.11，第七轮 P2-1）：
// 冻结后写路径的 InvalidateCounts 不再清缓存，Counts 返回冻结前的值；
// 解冻时强制失效一次，下一次立刻拿到新数字。
func TestCountsFrozenKeepsCache(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	if _, err := s.Counts(ctx, 0); err != nil { // 先把缓存建起来
		t.Fatal(err)
	}
	s.SetCountsFrozen(true)
	if _, err := s.UpsertOne(ctx, FileInfo{Path: "/v/a.mp4", Name: "a.mp4", Size: 10, Ext: "mp4"}, 1); err != nil {
		t.Fatal(err)
	}
	s.InvalidateCounts() // 扫描写路径会持续调用，冻结时必须无效果
	c, err := s.Counts(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Files != 0 {
		t.Fatalf("冻结期间应返回旧值 0，实际 %d", c.Files)
	}

	s.SetCountsFrozen(false)
	c, err = s.Counts(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Files != 1 {
		t.Fatalf("解冻后应重新聚合为 1，实际 %d", c.Files)
	}
}

// TestCheckpointWAL 验证收尾语句在当前驱动下可执行（v3.10.11，第七轮 P2-4）。
// PRAGMA wal_checkpoint 返回三列，误用 Exec 会拿不到 busy 标志。
func TestCheckpointWAL(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	if _, err := s.UpsertOne(ctx, FileInfo{Path: "/v/c.mp4", Name: "c.mp4", Size: 30, Ext: "mp4"}, 1); err != nil {
		t.Fatal(err)
	}
	busy, err := s.CheckpointWAL(ctx)
	if err != nil {
		t.Fatalf("收尾 WAL 失败：%v", err)
	}
	if busy {
		t.Fatal("单连接测试库不该出现 busy")
	}
}

// TestMigrateFingerprintDimGuard 验证维度未变时不再做全表 UPDATE，且换维度时
// 片头失败标记一并清掉（v3.10.11，第六轮 P2-4）。
func TestMigrateFingerprintDimGuard(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	if _, err := s.UpsertOne(ctx, FileInfo{Path: "/v/a.mp4", Name: "a.mp4", Size: 10, Ext: "mp4"}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE files SET audio=?, intro_audio=?, intro_at=99, intro_error='旧失败'
		WHERE path='/v/a.mp4'`, make([]byte, 10), make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateFingerprintDim(ctx, 100); err != nil {
		t.Fatal(err)
	}
	// 再塞一条旧维度数据：维度没变，守卫应直接返回，不碰它。
	if _, err := s.db.ExecContext(ctx, `UPDATE files SET audio=? WHERE path='/v/a.mp4'`, make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateFingerprintDim(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var audioLen int
	if err := s.db.QueryRowContext(ctx, `SELECT length(audio) FROM files WHERE path='/v/a.mp4'`).Scan(&audioLen); err != nil {
		t.Fatal(err)
	}
	if audioLen != 10 {
		t.Fatalf("维度未变不应清理指纹，实际长度 %d", audioLen)
	}

	// 换维度后：指纹、片头指纹、intro_at、intro_error 都应归零。
	if _, err := s.db.ExecContext(ctx, `UPDATE files SET intro_audio=?, intro_at=99, intro_error='旧失败'
		WHERE path='/v/a.mp4'`, make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateFingerprintDim(ctx, 200); err != nil {
		t.Fatal(err)
	}
	var audioNull, introNull, introAt, introErr bool
	if err := s.db.QueryRowContext(ctx, `SELECT audio IS NULL, intro_audio IS NULL, intro_at=0,
		COALESCE(intro_error,'')='' FROM files WHERE path='/v/a.mp4'`).
		Scan(&audioNull, &introNull, &introAt, &introErr); err != nil {
		t.Fatal(err)
	}
	if !audioNull || !introNull || !introAt || !introErr {
		t.Fatalf("换维度后应清空指纹与片头重试标记：audio=%v intro=%v at=%v err=%v",
			audioNull, introNull, introAt, introErr)
	}
}

// TestGroupQueryFilters 验证筛选增强的各维度与叠加：
// 大小上限、多扩展名、修改时间区间。三个组各两份文件，逐条件断言命中数。
func TestGroupQueryFilters(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	now := time.Now()
	// 三组：big(mp4,900,24h前) / small(jpg,100,1h前) / mid(png,500,2h前)，每组两份
	rows := []FileInfo{
		{Path: "/v/big.mp4", Name: "big.mp4", Size: 900, Mtime: now.Add(-24 * time.Hour).UnixNano(), Ext: "mp4"},
		{Path: "/v/big2.mp4", Name: "big2.mp4", Size: 900, Mtime: now.Add(-24 * time.Hour).UnixNano(), Ext: "mp4"},
		{Path: "/v/small.jpg", Name: "small.jpg", Size: 100, Mtime: now.Add(-1 * time.Hour).UnixNano(), Ext: "jpg"},
		{Path: "/v/small2.jpg", Name: "small2.jpg", Size: 100, Mtime: now.Add(-1 * time.Hour).UnixNano(), Ext: "jpg"},
		{Path: "/v/mid.png", Name: "mid.png", Size: 500, Mtime: now.Add(-2 * time.Hour).UnixNano(), Ext: "png"},
		{Path: "/v/mid2.png", Name: "mid2.png", Size: 500, Mtime: now.Add(-2 * time.Hour).UnixNano(), Ext: "png"},
	}
	ids := map[string]int64{}
	for _, f := range rows {
		id, err := s.UpsertOne(ctx, f, 1)
		if err != nil {
			t.Fatal(err)
		}
		ids[f.Path] = id
	}
	// 每组一个哈希：h1/h2/h3
	pairs := []struct {
		path, hash string
	}{
		{"/v/big.mp4", "h1"}, {"/v/big2.mp4", "h1"},
		{"/v/small.jpg", "h2"}, {"/v/small2.jpg", "h2"},
		{"/v/mid.png", "h3"}, {"/v/mid2.png", "h3"},
	}
	for _, p := range pairs {
		if err := s.SetQuick(ctx, ids[p.path], p.hash); err != nil {
			t.Fatal(err)
		}
		if err := s.SetHash(ctx, ids[p.path], p.hash); err != nil {
			t.Fatal(err)
		}
	}

	count := func(q GroupQuery) int64 {
		_, total, err := s.ExactGroups(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return total
	}

	if n := count(GroupQuery{MinSize: 1}); n != 3 {
		t.Fatalf("无过滤应 3 组，实际 %d", n)
	}
	if n := count(GroupQuery{MinSize: 1, MaxSize: 600}); n != 2 {
		t.Fatalf("MaxSize=600 应剩 small/mid 2 组，实际 %d", n)
	}
	if n := count(GroupQuery{MinSize: 1, Exts: []string{"mp4", "png"}}); n != 2 {
		t.Fatalf("Exts=[mp4,png] 应剩 big/mid 2 组，实际 %d", n)
	}
	if n := count(GroupQuery{MinSize: 1, Exts: []string{".JPG"}}); n != 1 {
		t.Fatalf("Exts 大小写与点号应被归一，应剩 small 1 组，实际 %d", n)
	}
	since := now.Add(-3 * time.Hour).UnixNano()
	if n := count(GroupQuery{MinSize: 1, Since: since}); n != 2 {
		t.Fatalf("Since=3h 前应剩 small/mid 2 组，实际 %d", n)
	}
	until := now.Add(-12 * time.Hour).UnixNano()
	if n := count(GroupQuery{MinSize: 1, Until: until}); n != 1 {
		t.Fatalf("Until=12h 前应剩 big 1 组，实际 %d", n)
	}
	// 叠加：3 小时内 + 仅 jpg → 只剩 small 组
	if n := count(GroupQuery{MinSize: 1, Since: since, Exts: []string{"jpg"}}); n != 1 {
		t.Fatalf("叠加过滤应只剩 small 1 组，实际 %d", n)
	}
	// 叠加：大小上限 + mp4 → big 组仍命中（时间不限制）
	if n := count(GroupQuery{MinSize: 1, MaxSize: 600, Exts: []string{"mp4"}}); n != 0 {
		t.Fatalf("600KB 上限内没有 mp4，应 0 组，实际 %d", n)
	}
}

// TestDropHealthByPaths 回归 v3.10.2：批量改名后要按路径一次清掉多类结论。
func TestDropHealthByPaths(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	if err := s.ReplaceHealthResults(ctx, "ext", []HealthResult{
		{Path: "/v/a.jpg"}, {Path: "/v/keep.jpg"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceHealthResults(ctx, "name", []HealthResult{{Path: "/v/a.jpg"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DropHealthByPaths(ctx, []string{"/v/a.jpg"}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountHealthResults(ctx, "name"); err != nil || n != 0 {
		t.Fatalf("name 类里的旧路径应被删除，实际 %d（err=%v）", n, err)
	}
	if n, err := s.CountHealthResults(ctx, "ext"); err != nil || n != 1 {
		t.Fatalf("ext 类应只剩未命中的 1 条，实际 %d（err=%v）", n, err)
	}
}

// TestDeleteHealthResultsByPaths 回归第五轮 P2-8：空目录清理后只删 empty_dir 结论，
// 同路径的其它检测结论（例如文件名类）必须保留。
func TestDeleteHealthResultsByPaths(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	if err := s.ReplaceHealthResults(ctx, "empty_dir", []HealthResult{
		{Path: "/v/old"}, {Path: "/v/other"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceHealthResults(ctx, "name", []HealthResult{{Path: "/v/old"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHealthResultsByPaths(ctx, "empty_dir", []string{"/v/old"}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountHealthResults(ctx, "empty_dir"); err != nil || n != 1 {
		t.Fatalf("empty_dir 应只剩 /v/other，实际 %d（err=%v）", n, err)
	}
	if n, err := s.CountHealthResults(ctx, "name"); err != nil || n != 1 {
		t.Fatalf("其它 kind 不应被误删，实际 %d（err=%v）", n, err)
	}
}

// TestListHealthResultsLimitClamp 验证 v3.10.2 的 limit 语义：
// 超过硬上限时截断到上限，而不是像旧实现那样回退成 500 条。
func TestListHealthResultsLimitClamp(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	const total = maxHealthPageLimit + 50
	rows := make([]HealthResult, 0, total)
	for i := 0; i < total; i++ {
		rows = append(rows, HealthResult{Path: fmt.Sprintf("/v/%05d.txt", i)})
	}
	if err := s.ReplaceHealthResults(ctx, "temp", rows); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListHealthResults(ctx, "temp", 5000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5000 {
		t.Fatalf("limit=5000 应返回 5000 行，实际 %d", len(got))
	}

	got, err = s.ListHealthResults(ctx, "temp", maxHealthPageLimit+1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxHealthPageLimit {
		t.Fatalf("超过硬上限应截断到 %d 行，实际 %d", maxHealthPageLimit, len(got))
	}
}

// TestNewAuditTaskIDConcurrentUnique 回归 v3.10.4 必修 2：
// 旧实现用 SELECT MAX(task_id)+1 且不在事务里，两个批量操作并发时会拿到同一个号，
// 之后撤销一条会把另一条一起回放。改为 meta 表原子自增后必须保证并发取号不重复。
func TestNewAuditTaskIDConcurrentUnique(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	const n = 16
	ids := make([]int64, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := s.NewAuditTaskID(ctx)
			if err != nil {
				errs <- err
				return
			}
			ids[i] = id
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发取号失败: %v", err)
	}

	seen := make(map[int64]bool, n)
	for _, id := range ids {
		if id <= 0 {
			t.Fatalf("任务号应为正整数，实际 %d", id)
		}
		if seen[id] {
			t.Fatalf("并发取号出现重复: %d（%v）", id, ids)
		}
		seen[id] = true
	}
}
