// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStatusUnauthHidesScanPaths 锁定未登录 /api/status 的字段收敛。
//
// 这个分支以前还会把 engine.State() 整个下发，其中 Current / Message / LastError
// 会带正在扫描的卷内绝对路径；局域网里未授权访客可以持续读到真实目录与文件名。
func TestStatusUnauthHidesScanPaths(t *testing.T) {
	s, _, _ := newTrashAPIServer(t)
	cfg := s.cfg.Get()
	cfg.AccessToken = "test-token"
	if err := s.cfg.Replace(cfg); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://nas:8898/api/status", nil)
	rec := httptest.NewRecorder()
	s.handleStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}

	var out struct {
		State map[string]any `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.State) != 2 {
		t.Fatalf("未登录 state 应只有 2 个键，实际 %v", out.State)
	}
	for _, k := range []string{"running", "phase"} {
		if _, ok := out.State[k]; !ok {
			t.Errorf("缺少键 %q，实际 %v", k, out.State)
		}
	}
	for _, k := range []string{"current", "message", "last_error", "types"} {
		if _, ok := out.State[k]; ok {
			t.Errorf("未登录不应下发 %q", k)
		}
	}
}

// TestFileSearchEmptyQuerySameShape 锁定 /api/search 空关键词分支的键集合。
//
// 打开搜索页的默认请求（q 为空）以前只回 total/items/ready，
// 有关键词时才回 files/next_cursor 等键；按 files 取值的脚本拿到 undefined。
func TestFileSearchEmptyQuerySameShape(t *testing.T) {
	s, _, _ := newTrashAPIServer(t)
	req := httptest.NewRequest(http.MethodGet, "http://nas:8898/api/search?q=", nil)
	rec := httptest.NewRecorder()
	s.handleFileSearch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"total", "took_ms", "truncated", "degraded", "ready", "items", "next_cursor", "files"} {
		if _, ok := got[k]; !ok {
			t.Errorf("空关键词响应缺少键 %q，实际键: %v", k, got)
		}
	}
	for _, k := range []string{"items", "files"} {
		v, ok := got[k].([]any)
		if !ok || v == nil {
			t.Errorf("%s 必须是数组而不是 null", k)
		}
	}
}
