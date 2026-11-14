// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件回归 v3.26.0 F1：GET /api/trash/list 必须是纯只读，旧接口的清理行为不能回退。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"verify/internal/store"
)

// getTrashList 直接调用 handleTrashList，跳过鉴权中间件（只验响应契约）。
func getTrashList(t *testing.T, s *Server, target string) (int, map[string]json.RawMessage) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleTrashList(rec, httptest.NewRequest(http.MethodGet, target, nil))
	var body map[string]json.RawMessage
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应不是合法 JSON：%v（%s）", err, rec.Body.String())
		}
	}
	return rec.Code, body
}

// countQuarantine 直接数库里的隔离记录，用来判断接口有没有偷偷清理幽灵行。
func countQuarantine(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM quarantine`).Scan(&n); err != nil {
		t.Fatalf("统计隔离记录失败：%v", err)
	}
	return n
}

// seedGhostTrash 插入一条隔离文件根本不存在的「幽灵行」，不创建任何文件。
func seedGhostTrash(t *testing.T, st *store.Store, root string) {
	t.Helper()
	missing := filepath.Join(root, ".verify-trash", "ghost.mkv")
	if _, err := st.DB().ExecContext(context.Background(),
		`INSERT INTO quarantine(orig_path,trash_path,volume,size,hash,deleted_at,reason)
		 VALUES(?,?,?,?,?,?,?)`,
		filepath.Join(root, "ghost-orig.mkv"), missing, root, 8, "", 1700099999, "duplicate"); err != nil {
		t.Fatalf("插入幽灵隔离记录失败：%v", err)
	}
}

// TestTrashListIsReadOnly 回归 F1 本体：只读清单必须把幽灵行原样返回，且库里一行不少。
func TestTrashListIsReadOnly(t *testing.T) {
	s, st, root := newTrashAPIServer(t)
	seedTrashAPI(t, st, root, 2)
	seedGhostTrash(t, st, root)
	if got := countQuarantine(t, st); got != 3 {
		t.Fatalf("基线应有 3 条隔离记录，实际 %d", got)
	}

	code, body := getTrashList(t, s, "/api/trash/list")
	if code != http.StatusOK {
		t.Fatalf("只读清单应返回 200，实际 %d", code)
	}
	var items []store.TrashRow
	if err := json.Unmarshal(body["items"], &items); err != nil {
		t.Fatalf("items 必须是数组：%v", err)
	}
	if len(items) != 3 {
		t.Fatalf("幽灵行也应原样返回，期望 3 条，实际 %d", len(items))
	}
	if got := countQuarantine(t, st); got != 3 {
		t.Fatalf("只读清单不得清理幽灵行，库内应仍为 3 条，实际 %d", got)
	}

	// 分页路径同样只读：本页 2 条、has_more=true，库内仍 3 条。
	code, body = getTrashList(t, s, "/api/trash/list?limit=2")
	if code != http.StatusOK {
		t.Fatalf("分页只读清单应返回 200，实际 %d", code)
	}
	if err := json.Unmarshal(body["items"], &items); err != nil {
		t.Fatalf("分页 items 必须是数组：%v", err)
	}
	if len(items) != 2 || string(body["has_more"]) != "true" {
		t.Fatalf("分页应返回 2 条且 has_more=true，实际 %d / %s", len(items), body["has_more"])
	}
	if got := countQuarantine(t, st); got != 3 {
		t.Fatalf("分页只读清单不得清理幽灵行，库内应仍为 3 条，实际 %d", got)
	}
}

// TestTrashLegacyStillHeals 旧 GET /api/trash 的清理行为不能因为新增只读接口而回退。
func TestTrashLegacyStillHeals(t *testing.T) {
	s, st, root := newTrashAPIServer(t)
	seedTrashAPI(t, st, root, 2)
	seedGhostTrash(t, st, root)

	code, body := getTrash(t, s, "/api/trash")
	if code != http.StatusOK {
		t.Fatalf("旧接口应返回 200，实际 %d", code)
	}
	var items []store.TrashRow
	if err := json.Unmarshal(body["items"], &items); err != nil {
		t.Fatalf("items 必须是数组：%v", err)
	}
	if len(items) != 2 {
		t.Fatalf("旧接口应清理幽灵行后返回 2 条，实际 %d", len(items))
	}
	if got := countQuarantine(t, st); got != 2 {
		t.Fatalf("旧接口应删掉幽灵行，库内应为 2 条，实际 %d", got)
	}
}

// TestTrashListRejectsPost 只读接口不接受写方法，避免被误当成移入隔离区的入口。
func TestTrashListRejectsPost(t *testing.T) {
	s, _, _ := newTrashAPIServer(t)
	rec := httptest.NewRecorder()
	s.handleTrashList(rec, httptest.NewRequest(http.MethodPost, "/api/trash/list", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 应返回 405，实际 %d", rec.Code)
	}
}
