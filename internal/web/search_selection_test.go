// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件是 v3.20.0「Everything 式搜索」的回归：游标翻页、全选凭据与失效判定。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"verify/internal/config"
	"verify/internal/search"
	"verify/internal/store"
)

func newSearchServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	m, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	for _, f := range []struct{ path, name, ext string }{
		{"/v/a/movie.mkv", "movie.mkv", "mkv"},
		{"/v/b/movie.mp4", "movie.mp4", "mp4"},
		{"/v/c/note.txt", "note.txt", "txt"},
	} {
		if _, err := st.UpsertOne(ctx, store.FileInfo{
			Path: f.path, Volume: "/v", Name: f.name, Ext: f.ext,
			Size: 10, Mtime: 1, Ctime: 1,
		}, 1); err != nil {
			t.Fatal(err)
		}
	}
	ix := search.New(0)
	if _, err := ix.Build(ctx, st.DB()); err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: m, st: st, searchIndex: ix, searchSel: newSelectionStore()}
}

func postSearchJSON(t *testing.T, h http.HandlerFunc, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	h(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// TestSearchSelectionResolve 验证凭据只回总数、解析分批下发、排除项参与总数。
func TestSearchSelectionResolve(t *testing.T) {
	s := newSearchServer(t)
	rec, out := postSearchJSON(t, s.handleSearchSelection, map[string]any{"q": "movie", "ext": "mkv,mp4"})
	if rec.Code != http.StatusOK {
		t.Fatalf("创建凭据应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatalf("未返回 token：%v", out)
	}
	if got := int(out["count"].(float64)); got != 2 {
		t.Fatalf("命中数应为 2，实际 %d", got)
	}

	rec, out = postSearchJSON(t, s.handleSearchSelectionItems, map[string]any{"token": token, "offset": 0, "limit": 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("解析第一页应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	items, _ := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("limit=1 应返回 1 行，实际 %d", len(items))
	}
	first := items[0].(map[string]any)["path"].(string)

	// 排除第一页那条后，总数与下一页口径都应按排除后的集合计算。
	rec, out = postSearchJSON(t, s.handleSearchSelectionItems, map[string]any{
		"token": token, "offset": 0, "limit": 2000, "excludes": []string{first},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("带排除解析应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if got := int(out["total"].(float64)); got != 1 {
		t.Fatalf("排除 1 条后总数应为 1，实际 %d", got)
	}

	// 数据版本变化后（扫描写入），旧凭据必须失效，提示重新全选。
	if _, err := s.st.UpsertOne(context.Background(), store.FileInfo{
		Path: "/v/d/movie.avi", Volume: "/v", Name: "movie.avi", Ext: "avi",
		Size: 10, Mtime: 1, Ctime: 1,
	}, 1); err != nil {
		t.Fatal(err)
	}
	rec, out = postSearchJSON(t, s.handleSearchSelectionItems, map[string]any{"token": token})
	if rec.Code != http.StatusGone {
		t.Fatalf("数据变化后应 410，实际 %d %s", rec.Code, rec.Body.String())
	}
	if msg, _ := out["error"].(string); msg == "" {
		t.Fatal("410 响应应说明原因")
	}
}

// TestFileSearchCursor 验证 /api/search 返回 next_cursor，且两页不重复。
func TestFileSearchCursor(t *testing.T) {
	s := newSearchServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=movie&limit=1&sort=name&order=asc", nil)
	rec := httptest.NewRecorder()
	s.handleFileSearch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("搜索应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var first map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	cursor, _ := first["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("limit=1 时应返回下一页游标：%v", first)
	}
	items := first["items"].([]any)
	firstPath := items[0].(map[string]any)["path"].(string)

	req = httptest.NewRequest(http.MethodGet, "/api/search?q=movie&limit=1&sort=name&order=asc&cursor="+cursor, nil)
	rec = httptest.NewRecorder()
	s.handleFileSearch(rec, req)
	var second map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	items = second["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("第二页应 1 行，实际 %d", len(items))
	}
	if p := items[0].(map[string]any)["path"].(string); p == firstPath {
		t.Fatalf("第二页不应重复第一页的 %s", p)
	}
}
