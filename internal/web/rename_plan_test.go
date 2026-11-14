// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件是 v3.21.0 两阶段改名的端到端回归：建计划 → 分片执行 → 续跑与文件落盘。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"verify/internal/config"
	"verify/internal/engine"
	"verify/internal/search"
	"verify/internal/store"
)

func newRenamePlanServer(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	m, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// 与 engine 测试同款：临时目录不在 /vol 下，走扫描根环境变量让 ValidPath 放行。
	t.Setenv("VERIFY_SCAN_ROOTS", root)
	e := engine.New(st, m, engine.NewBus(), root)
	return &Server{e: e, cfg: m, st: st, searchIndex: search.New(0), searchSel: newSelectionStore(), renameCancel: newRenameCancelStore()}, root
}

func TestRenamePlanAndChunk(t *testing.T) {
	s, root := newRenamePlanServer(t)
	ctx := context.Background()
	files := make([]map[string]string, 0, 3)
	for i := 0; i < 3; i++ {
		p := filepath.Join(root, fmt.Sprintf("a%d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.st.UpsertOne(ctx, store.FileInfo{
			Path: p, Volume: root, Name: filepath.Base(p), Ext: "txt",
			Size: 1, Mtime: 1, Ctime: 1,
		}, 1); err != nil {
			t.Fatal(err)
		}
		files = append(files, map[string]string{"path": p})
	}

	rec, out := postSearchJSON(t, s.handleRenamePlan, map[string]any{
		"files":   files,
		"methods": []map[string]any{{"type": "replace", "pattern": "a", "replacement": "b"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("建计划应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	planID := int64(out["plan_id"].(float64))
	if planID <= 0 || int(out["total"].(float64)) != 3 {
		t.Fatalf("计划不正确：%v", out)
	}

	// 第一片只执行 1 条，验证进度与「剩余可续跑」。
	rec, out = postSearchJSON(t, s.handleRenameExecuteChunk, map[string]any{"plan_id": planID, "limit": 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("执行第一片应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if int(out["done"].(float64)) != 1 || int(out["remaining"].(float64)) != 2 {
		t.Fatalf("第一片结果不正确：%v", out)
	}
	if _, err := os.Stat(filepath.Join(root, "b0.txt")); err != nil {
		t.Fatalf("第一片应已改名：%v", err)
	}

	// 第二片执行剩余全部。
	rec, out = postSearchJSON(t, s.handleRenameExecuteChunk, map[string]any{"plan_id": planID, "limit": 2000})
	if rec.Code != http.StatusOK {
		t.Fatalf("执行第二片应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if int(out["done"].(float64)) != 2 || int(out["remaining"].(float64)) != 0 {
		t.Fatalf("第二片结果不正确：%v", out)
	}
	for i := 0; i < 3; i++ {
		if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("b%d.txt", i))); err != nil {
			t.Fatalf("文件应已改名：%v", err)
		}
	}

	// 计划状态接口应显示全部完成。
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/rename/plan/status?plan_id=%d", planID), nil)
	rw := httptest.NewRecorder()
	s.handleRenamePlanStatus(rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("状态接口应 200，实际 %d %s", rw.Code, rw.Body.String())
	}
}

// TestRenamePlanRejectsBadPlanID 验证分片执行对不存在计划的 404，避免误建空批次。
func TestRenamePlanRejectsBadPlanID(t *testing.T) {
	s, _ := newRenamePlanServer(t)
	rec, _ := postSearchJSON(t, s.handleRenameExecuteChunk, map[string]any{"plan_id": 999999})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的计划应 404，实际 %d %s", rec.Code, rec.Body.String())
	}
}

// TestWriteRenameFailureCSV 验证失败明细按批落盘（v3.22.0）。
func TestWriteRenameFailureCSV(t *testing.T) {
	dir := t.TempDir()
	m, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: m, dataDir: dir}
	s.writeRenameFailureCSV(42, []store.RenameBatchItem{
		{Path: "/v/a.txt", Target: "b.txt", Error: "目标已存在"},
		{Path: "/v/c.txt", Target: "d.txt"},
	})
	path := filepath.Join(dir, "logs", "rename-failures-42.csv")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("失败明细未落盘：%v", err)
	}
	if !strings.Contains(string(b), "目标已存在") {
		t.Fatalf("CSV 内容缺少失败原因：%s", string(b))
	}
	if strings.Contains(string(b), "/v/c.txt") {
		t.Fatalf("成功项不应写入失败明细：%s", string(b))
	}
}

// TestRenamePlanCancelAndUndo 覆盖 v3.22.0：取消只在片边界生效、续跑可用、多批撤销能改回原名。
func TestRenamePlanCancelAndUndo(t *testing.T) {
	s, root := newRenamePlanServer(t)
	ctx := context.Background()
	files := make([]map[string]string, 0, 2)
	for i := 0; i < 2; i++ {
		p := filepath.Join(root, fmt.Sprintf("c%d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.st.UpsertOne(ctx, store.FileInfo{
			Path: p, Volume: root, Name: filepath.Base(p), Ext: "txt",
			Size: 1, Mtime: 1, Ctime: 1,
		}, 1); err != nil {
			t.Fatal(err)
		}
		files = append(files, map[string]string{"path": p})
	}
	rec, out := postSearchJSON(t, s.handleRenamePlan, map[string]any{
		"files":   files,
		"methods": []map[string]any{{"type": "replace", "pattern": "c", "replacement": "d"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("建计划应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	planID := int64(out["plan_id"].(float64))

	// 取消后第一次分片应直接返回 cancelled，且文件保持原名。
	if rec, _ := postSearchJSON(t, s.handleRenameCancel, map[string]any{"plan_id": planID}); rec.Code != http.StatusOK {
		t.Fatalf("取消接口应 200，实际 %d", rec.Code)
	}
	rec, out = postSearchJSON(t, s.handleRenameExecuteChunk, map[string]any{"plan_id": planID, "limit": 2000})
	if rec.Code != http.StatusOK || out["cancelled"] != true {
		t.Fatalf("取消后应返回 cancelled：%d %v", rec.Code, out)
	}
	for i := 0; i < 2; i++ {
		if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("c%d.txt", i))); err != nil {
			t.Fatalf("取消后文件应保持原名：%v", err)
		}
	}

	// 再次执行等于续跑，取消标记已被消耗。
	rec, out = postSearchJSON(t, s.handleRenameExecuteChunk, map[string]any{"plan_id": planID, "limit": 2000})
	if rec.Code != http.StatusOK || int(out["done"].(float64)) != 2 || int(out["remaining"].(float64)) != 0 {
		t.Fatalf("续跑应改完 2 条：%d %v", rec.Code, out)
	}
	batchID := int64(out["batch_id"].(float64))

	// 多批撤销接口应把文件改回原名。
	rec, out = postSearchJSON(t, s.handleRenameUndoBatch, map[string]any{"batch_ids": []int64{batchID}})
	if rec.Code != http.StatusOK {
		t.Fatalf("撤销应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if int(out["done"].(float64)) == 0 {
		t.Fatalf("撤销应至少完成 1 条：%v", out)
	}
	for i := 0; i < 2; i++ {
		if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("c%d.txt", i))); err != nil {
			t.Fatalf("撤销后应恢复原名：%v", err)
		}
	}
}

// TestRenameChunkResultsMarksSkippedPending 回归 v3.25.0 P0-1：
// 执行被取消时引擎不给未执行的条目填 Error/NewPath，回填必须保持 pending，
// 不能记成 done，否则计划头被标完成、pending 归零，用户失去续跑入口。
func TestRenameChunkResultsMarksSkippedPending(t *testing.T) {
	rows := []store.RenamePlanItemRow{{Idx: 0}, {Idx: 1}, {Idx: 2}}
	items := []store.RenameBatchItem{
		{Path: "/vol1/a.txt", NewPath: "/vol1/a2.txt"},
		{}, // 取消后未执行
		{Path: "/vol1/b.txt", Error: "目标已存在"}, // 真失败
	}
	out, skipped := renameChunkResults(rows, items, 7)
	if skipped != 1 {
		t.Fatalf("应识别 1 条未执行，实际 %d", skipped)
	}
	if out[0].State != "done" || out[1].State != "pending" || out[2].State != "failed" {
		t.Fatalf("状态映射不对：%+v", out)
	}
	if out[1].Error == "" {
		t.Fatal("未执行条目应写明原因")
	}

	// 取消发生在片中间时引擎返回的条数可能少于取出的行数，同样按 pending 处理。
	out, skipped = renameChunkResults(rows, items[:1], 8)
	if skipped != 2 || out[2].State != "pending" {
		t.Fatalf("缺失条目应按未执行处理：skipped=%d %+v", skipped, out)
	}
}
