// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件是 v3.17.3 的入口参数回归：容量上限（F-06）、序号位数（F-07）
// 与永久删除的方法/确认校验（F-17）。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"verify/internal/engine"
	"verify/internal/store"
)

// TestValidateRenameDigits 回归 F-07：过大的序号位数必须在入口被拒。
func TestValidateRenameDigits(t *testing.T) {
	cases := []struct {
		name string
		req  renameReq
		want string
	}{
		{"默认", renameReq{}, ""},
		{"边界内", renameReq{Rules: renameRules{Digits: 10}}, ""},
		{"清单式超限", renameReq{Rules: renameRules{Digits: 11}}, "序号位数必须在 1–10 之间"},
		{"规则式超限", renameReq{Methods: []engine.RenameMethod{{Digits: 1000000}}}, "序号位数必须在 1–10 之间"},
		{"负数", renameReq{Rules: renameRules{Digits: -1}}, "序号位数必须在 1–10 之间"},
	}
	for _, c := range cases {
		if got := validateRenameDigits(c.req); got != c.want {
			t.Fatalf("%s：期望 %q，实际 %q", c.name, c.want, got)
		}
	}
}

// TestValidateCleanupPlanMaxKeepGB 回归 F-06：容量上限超过 2^20 GB 直接拒绝，
// 避免 GB→字节的 int64 乘法溢出后把上限当成「不限」。
func TestValidateCleanupPlanMaxKeepGB(t *testing.T) {
	valid := func() *store.CleanupPlan {
		return &store.CleanupPlan{
			Name: "p", Schedule: "daily 03:00", MatchMode: "all",
			RetainStrategy: "modify_newest",
		}
	}
	p := valid()
	p.MaxKeepGB = 1024
	if msg := validateCleanupPlan(p); msg != "" {
		t.Fatalf("正常上限不应被拒：%s", msg)
	}
	p = valid()
	p.MaxKeepGB = 1 << 21
	if msg := validateCleanupPlan(p); msg == "" {
		t.Fatal("超大容量上限应被拒")
	}
}

// TestHandleDeleteRequiresPostAndConfirm 回归 F-17：永久删除只接受 POST，
// 且必须显式 confirm=true（与清空隔离区同口径）。
func TestHandleDeleteRequiresPostAndConfirm(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleDelete(rec, httptest.NewRequest(http.MethodGet, "/api/trash/delete", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应返回 405，实际 %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.handleDelete(rec, httptest.NewRequest(http.MethodPost, "/api/trash/delete", strings.NewReader(`{"ids":[1]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 confirm 应返回 400，实际 %d", rec.Code)
	}
}
