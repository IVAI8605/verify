// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"verify/internal/config"
	"verify/internal/engine"
	"verify/internal/search"
	"verify/internal/store"
)

// newTrashAPIServer 组装一个能直连 handleTrash 的最小服务端（v3.25.2）。
// 隔离文件必须真实存在，否则 HealTrashRows 会按幽灵行清理，用例就拿不到行。
func newTrashAPIServer(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("VERIFY_SCAN_ROOTS", root)
	m, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := engine.New(st, m, engine.NewBus(), root)
	return &Server{e: e, cfg: m, st: st, searchIndex: search.New(0), searchSel: newSelectionStore(), renameCancel: newRenameCancelStore()}, st, root
}

// seedTrashAPI 插入 n 条隔离记录，并各自落一个真实隔离文件。
func seedTrashAPI(t *testing.T, st *store.Store, root string, n int) {
	t.Helper()
	trashDir := filepath.Join(root, ".verify-trash")
	if err := os.MkdirAll(trashDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		p := filepath.Join(trashDir, fmt.Sprintf("f-%d.mkv", i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().ExecContext(context.Background(),
			`INSERT INTO quarantine(orig_path,trash_path,volume,size,hash,deleted_at,reason)
			 VALUES(?,?,?,?,?,?,?)`,
			fmt.Sprintf("%s/m%03d.mkv", root, i), p, root, 8, "", 1700000000+i, "duplicate"); err != nil {
			t.Fatalf("插入隔离记录失败：%v", err)
		}
	}
}

// getTrash 直接调用 handleTrash，跳过鉴权中间件（只验响应契约）。
func getTrash(t *testing.T, s *Server, target string) (int, map[string]json.RawMessage) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleTrash(rec, httptest.NewRequest(http.MethodGet, target, nil))
	var body map[string]json.RawMessage
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应不是合法 JSON：%v（%s）", err, rec.Body.String())
		}
	}
	return rec.Code, body
}

// TestTrashLegacyResponseKeepsItems v3.25.2 向后兼容断言：
// 不传分页参数时 items 仍是完整数组，新增字段只是追加，老调用方不受影响。
func TestTrashLegacyResponseKeepsItems(t *testing.T) {
	s, st, root := newTrashAPIServer(t)
	seedTrashAPI(t, st, root, 3)

	code, body := getTrash(t, s, "/api/trash")
	if code != http.StatusOK {
		t.Fatalf("老路径应返回 200，实际 %d", code)
	}
	var items []store.TrashRow
	if err := json.Unmarshal(body["items"], &items); err != nil {
		t.Fatalf("items 必须是数组：%v", err)
	}
	if len(items) != 3 {
		t.Fatalf("老路径应返回全部 3 行，实际 %d", len(items))
	}
	for _, k := range []string{"total", "returned", "has_more", "next_cursor"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("响应缺少追加字段 %s", k)
		}
	}
	if string(body["has_more"]) != "false" || string(body["next_cursor"]) != `""` {
		t.Fatalf("老路径 has_more/next_cursor 应为 false/空，实际 %s / %s", body["has_more"], body["next_cursor"])
	}
}

// TestTrashPagedResponse 分页响应：本页条数、是否还有更多、游标可用且翻页不重复。
func TestTrashPagedResponse(t *testing.T) {
	s, st, root := newTrashAPIServer(t)
	seedTrashAPI(t, st, root, 3)

	code, body := getTrash(t, s, "/api/trash?limit=2")
	if code != http.StatusOK {
		t.Fatalf("分页应返回 200，实际 %d", code)
	}
	var page1 []store.TrashRow
	if err := json.Unmarshal(body["items"], &page1); err != nil || len(page1) != 2 {
		t.Fatalf("第一页应有 2 行：err=%v len=%d", err, len(page1))
	}
	var cursor string
	_ = json.Unmarshal(body["next_cursor"], &cursor)
	if cursor == "" || string(body["has_more"]) != "true" {
		t.Fatalf("第一页应带游标且 has_more=true：cursor=%q has_more=%s", cursor, body["has_more"])
	}

	code2, body2 := getTrash(t, s, "/api/trash?limit=2&cursor="+cursor)
	if code2 != http.StatusOK {
		t.Fatalf("第二页应返回 200，实际 %d", code2)
	}
	var page2 []store.TrashRow
	if err := json.Unmarshal(body2["items"], &page2); err != nil || len(page2) != 1 {
		t.Fatalf("第二页应有 1 行：err=%v len=%d", err, len(page2))
	}
	if page2[0].ID == page1[0].ID || page2[0].ID == page1[1].ID {
		t.Fatalf("翻页出现重复行：page1=%d,%d page2=%d", page1[0].ID, page1[1].ID, page2[0].ID)
	}
	if string(body2["has_more"]) != "false" {
		t.Fatalf("第二页应到底，实际 has_more=%s", body2["has_more"])
	}
}

// TestTrashBadPagingParams 参数错误必须回 400 且带中文提示，不能落到 500。
func TestTrashBadPagingParams(t *testing.T) {
	s, st, root := newTrashAPIServer(t)
	seedTrashAPI(t, st, root, 1)
	for _, target := range []string{"/api/trash?limit=abc", "/api/trash?limit=0", "/api/trash?limit=501", "/api/trash?cursor=bad"} {
		code, _ := getTrash(t, s, target)
		if code != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400，实际 %d", target, code)
		}
	}
}
