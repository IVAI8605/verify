// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"verify/internal/config"
	"verify/internal/store"
	"verify/internal/webauth"
)

// TestSameSiteRequest 覆盖 SEC-1 的来源判定：同源放行，同站跨端口与跨站拒绝，
// 没有浏览器来源信息的客户端（curl / 本机脚本）放行。
func TestSameSiteRequest(t *testing.T) {
	cases := []struct {
		name string
		hdr  map[string]string
		want bool
	}{
		{"浏览器同源", map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"浏览器直接访问", map[string]string{"Sec-Fetch-Site": "none"}, true},
		{"浏览器同站跨端口", map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		{"浏览器跨站", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"非浏览器无来源头", nil, true},
		{"Origin 同源", map[string]string{"Origin": "http://nas:8898"}, true},
		{"Origin 其他端口", map[string]string{"Origin": "http://nas:8096"}, false},
		{"Referer 同源", map[string]string{"Referer": "http://nas:8898/index.html"}, true},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "http://nas:8898/api/scan", nil)
		for k, v := range c.hdr {
			req.Header.Set(k, v)
		}
		if got := sameSiteRequest(req); got != c.want {
			t.Fatalf("%s：期望 %v，实际 %v", c.name, c.want, got)
		}
	}
}

// TestCSRFGuard 验证中间件对写请求的拦截行为，以及只读请求与安全头。
func TestCSRFGuard(t *testing.T) {
	s := &Server{}
	var hit bool
	h := s.csrfGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	send := func(method, site string) *httptest.ResponseRecorder {
		hit = false
		req := httptest.NewRequest(method, "http://nas:8898/api/scan", nil)
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := send(http.MethodPost, "same-site"); rec.Code != http.StatusForbidden || hit {
		t.Fatalf("同站跨端口 POST 应被拒绝，实际 %d（已放行=%v）", rec.Code, hit)
	}
	if rec := send(http.MethodPost, "same-origin"); rec.Code != http.StatusOK || !hit {
		t.Fatalf("同源 POST 应放行，实际 %d（已放行=%v）", rec.Code, hit)
	}
	if rec := send(http.MethodPost, ""); rec.Code != http.StatusOK || !hit {
		t.Fatalf("无来源头的脚本 POST 应放行，实际 %d", rec.Code)
	}
	// 只读请求不参与 CSRF 判定，且所有响应都带安全头
	rec := send(http.MethodGet, "cross-site")
	if rec.Code != http.StatusOK || !hit {
		t.Fatalf("GET 不应被拦截，实际 %d", rec.Code)
	}
	// v3.10.9：入口已改为 type=url 独立网页，不再需要被 iframe 承载，
	// 框架头收紧回 DENY + frame-ancestors 'self'，任何页面都不能嵌入本应用。
	rec = send("POST", "same-origin")
	if rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("响应缺少安全头：%v", rec.Header())
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'self';") {
		t.Fatalf("frame-ancestors 应只允许同源：%s", csp)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); strings.Contains(csp, ":*") {
		t.Fatalf("frame-ancestors 不应再放行同主机任意端口：%s", csp)
	}
}

// TestCSRFCookieSessionRequiresHeader 守住 v3.10.6 的回归面：
// 带令牌 + Cookie 登录时，绕过 postJSON 的写请求缺 X-Requested-With 会一律 403。
// v3.10.7 把补头下沉到 api() 之后，这些路径必须能放行到业务层。
func TestCSRFCookieSessionRequiresHeader(t *testing.T) {
	dir := t.TempDir()
	m, err := config.Load(dir)
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}
	cfg := m.Get()
	cfg.AccessToken = "test-token"
	if err := m.Replace(cfg); err != nil {
		t.Fatalf("写入配置失败：%v", err)
	}
	// recordDenied 第一次拒绝会写审计；st 不能为空，否则本用例单独跑会 panic
	// （完整套件里靠前面用例占住节流窗口才没暴露）。
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Server{cfg: m, st: st}
	var hit bool
	h := s.csrfGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	cookie := &http.Cookie{Name: cookieName, Value: webauth.CookieValue("test-token")}
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/export"},
		{http.MethodPost, "/api/report"},
		{http.MethodPost, "/api/oplog/export"},
		{http.MethodPost, "/api/import"},
		{http.MethodPost, "/api/fs/roots"},
		{http.MethodDelete, "/api/import/sessions?id=1"},
		{http.MethodDelete, "/api/video_tags?id=1"},
		{http.MethodDelete, "/api/sync/peers?machine=nas2"},
		{http.MethodDelete, "/api/rename/presets/1"},
	}
	for _, c := range cases {
		hit = false
		req := httptest.NewRequest(c.method, "http://nas:8898"+c.path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || hit {
			t.Fatalf("%s %s 缺 X-Requested-With 应 403，实际 %d（已放行=%v）", c.method, c.path, rec.Code, hit)
		}

		hit = false
		req = httptest.NewRequest(c.method, "http://nas:8898"+c.path, nil)
		req.AddCookie(cookie)
		req.Header.Set("X-Requested-With", "verify")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !hit {
			t.Fatalf("%s %s 带 X-Requested-With 应放行，实际 %d（已放行=%v）", c.method, c.path, rec.Code, hit)
		}
	}
}

// TestHandleScanRequiresExplicitMode 守住 v3.10.10 的回归面：空 body 或非法 mode
// 不能被当成全量扫描启动。这里用空 Server，命中 400/405 时不会触碰 engine。
func TestHandleScanRequiresExplicitMode(t *testing.T) {
	s := &Server{}
	cases := []struct {
		name   string
		method string
		body   string
		want   int
	}{
		{"GET 不启动扫描", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"空 body", http.MethodPost, "", http.StatusBadRequest},
		{"只有无关字段", http.MethodPost, `{"foo":1}`, http.StatusBadRequest},
		{"未知 mode", http.MethodPost, `{"mode":"deep"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://nas:8898/api/scan", strings.NewReader(c.body))
		rec := httptest.NewRecorder()
		s.handleScan(rec, req)
		if rec.Code != c.want {
			t.Fatalf("%s：期望 %d，实际 %d（%s）", c.name, c.want, rec.Code, rec.Body.String())
		}
	}
}

// TestRawInlineTypesRejectsActiveContent 验证 SEC-2 的白名单不给可执行类型内联渲染的机会。
func TestRawInlineTypesRejectsActiveContent(t *testing.T) {
	for _, ext := range []string{".html", ".htm", ".svg", ".js", ".mjs", ".xml", ".xhtml", ".pdf"} {
		if _, ok := rawInlineTypes[ext]; ok {
			t.Fatalf("%s 不应允许内联打开", ext)
		}
	}
	for _, ext := range []string{".mp4", ".mkv", ".jpg", ".srt"} {
		if _, ok := rawInlineTypes[ext]; !ok {
			t.Fatalf("%s 应允许内联播放/预览", ext)
		}
	}
}
