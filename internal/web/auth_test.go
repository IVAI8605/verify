// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"verify/internal/config"
	"verify/internal/store"
	"verify/internal/webauth"
)

// newAuthServer 构造带配置与真实数据库的 Server，够 authed/handleLogin 使用。
// 登录、登出都要写操作记录（v3.7.0），所以 store 必须实例化；engine 仍不参与。
func newAuthServer(t *testing.T, token string) *Server {
	t.Helper()
	dir := t.TempDir()
	m, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := m.Get()
	c.AccessToken = token
	if err := m.Replace(c); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Server{cfg: m, st: st}
}

// TestAuthedCookieIsDerivedValue 是这次改动的核心断言：
// 只有派生值能通过，直接塞明文令牌的 Cookie 必须被拒。
func TestAuthedCookieIsDerivedValue(t *testing.T) {
	const token = "s3cret-token"
	s := newAuthServer(t, token)

	if s.authed(httptest.NewRequest(http.MethodGet, "/api/status", nil)) {
		t.Fatal("未携带任何凭据时应拒绝")
	}

	raw := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	raw.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	if s.authed(raw) {
		t.Fatal("明文令牌 Cookie 不应通过")
	}

	derived := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	derived.AddCookie(&http.Cookie{Name: cookieName, Value: webauth.CookieValue(token)})
	if !s.authed(derived) {
		t.Fatal("派生值 Cookie 应通过")
	}
}

// TestStatusHidesInternalsWhenUnauthenticated 覆盖 /api/status 收敛（v3.10.8）：
// 未登录时不能把数据目录、日志路径、ffmpeg 路径与完整配置下发给局域网访问者。
func TestStatusHidesInternalsWhenUnauthenticated(t *testing.T) {
	s := newAuthServer(t, "s3cret-token")
	rec := httptest.NewRecorder()
	s.handleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	for _, key := range []string{"paths", "config", "ffmpeg", "counts", "volumes", "hash", "cleanup"} {
		if _, ok := body[key]; ok {
			t.Errorf("未登录响应不应包含字段 %q", key)
		}
	}
	if _, ok := body["auth"]; !ok {
		t.Error("未登录响应仍需返回 auth，登录页据此决定是否弹令牌框")
	}
}

// TestAuthedBearerAndQuery 覆盖 SSE 与外部调用两条非 Cookie 入口。
func TestAuthedBearerAndQuery(t *testing.T) {
	const token = "s3cret-token"
	s := newAuthServer(t, token)

	bearer := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	bearer.Header.Set("Authorization", "Bearer "+token)
	if !s.authed(bearer) {
		t.Fatal("正确 Bearer 头应通过")
	}

	wrong := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	wrong.Header.Set("Authorization", "Bearer nope")
	if s.authed(wrong) {
		t.Fatal("错误 Bearer 头不应通过")
	}

	query := httptest.NewRequest(http.MethodGet, "/api/events?token="+token, nil)
	if !s.authed(query) {
		t.Fatal("查询参数令牌应通过")
	}

	// v3.10.8（SEC-6）：查询参数令牌只对 SSE 放行，其它接口不再接受
	other := httptest.NewRequest(http.MethodGet, "/api/status?token="+token, nil)
	if s.authed(other) {
		t.Fatal("非 SSE 接口不应接受查询参数令牌")
	}
}

// TestHandleAuthTokenRequiresMarker 覆盖 SEC-3：令牌接口必须带自定义请求头，
// 跨站的图片/链接/表单导航设不了它，避免令牌被顺手取走。
func TestHandleAuthTokenRequiresMarker(t *testing.T) {
	s := newAuthServer(t, "s3cret-token")

	bare := httptest.NewRecorder()
	s.handleAuthToken(bare, httptest.NewRequest(http.MethodGet, "/api/auth/token", nil))
	if bare.Code != http.StatusForbidden {
		t.Fatalf("缺少请求标记应 403，实际 %d", bare.Code)
	}

	ok := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/token", nil)
	req.Header.Set("X-Requested-With", "verify")
	s.handleAuthToken(ok, req)
	if ok.Code != http.StatusOK {
		t.Fatalf("带请求标记应 200，实际 %d", ok.Code)
	}
}

// TestHandleLogoutRejectsGET 覆盖 SEC-7：GET 也能触发登出会被任意链接利用。
func TestHandleLogoutRejectsGET(t *testing.T) {
	s := newAuthServer(t, "s3cret-token")
	rr := httptest.NewRecorder()
	s.handleLogout(rr, httptest.NewRequest(http.MethodGet, "/api/logout", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 登出应 405，实际 %d", rr.Code)
	}
}

// TestHandleLoginRejectsOversizeBody 覆盖 SEC-5：登录体超限直接 400，不再整段读入。
func TestHandleLoginRejectsOversizeBody(t *testing.T) {
	s := newAuthServer(t, "s3cret-token")
	big := `{"token":"` + strings.Repeat("a", 16*1024) + `"}`
	rr := httptest.NewRecorder()
	s.handleLogin(rr, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(big)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("超大登录体应 400，实际 %d", rr.Code)
	}
}

// TestAuthedWithoutTokenConfigured 覆盖首次使用：没有任何凭据时业务接口必须拒绝，
// 登录页会显示「设置访问密码」，不能像旧版那样把空令牌当成免登录。
func TestAuthedWithoutTokenConfigured(t *testing.T) {
	s := newAuthServer(t, "")
	if s.authed(httptest.NewRequest(http.MethodGet, "/api/status", nil)) {
		t.Fatal("没有访问密码/令牌时不应放行业务接口")
	}
}

func newPasswordServer(t *testing.T, password string) *Server {
	t.Helper()
	s := newAuthServer(t, "")
	hash, err := webauth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	c := s.cfg.Get()
	c.PasswordHash = hash
	if err := s.cfg.Replace(c); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPasswordBasicAuth(t *testing.T) {
	const pw = "correct horse 123"
	s := newPasswordServer(t, pw)

	okReq := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	okReq.SetBasicAuth("", pw)
	if !s.authed(okReq) {
		t.Fatal("正确密码的 HTTP Basic 应通过")
	}

	badReq := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	badReq.SetBasicAuth("", "wrong-pass")
	if s.authed(badReq) {
		t.Fatal("错误密码的 HTTP Basic 不应通过")
	}
}

func TestPasswordLoginSetsDerivedCookie(t *testing.T) {
	const pw = "correct horse 123"
	s := newPasswordServer(t, pw)
	rr := httptest.NewRecorder()
	s.handleLogin(rr, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"`+pw+`"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("正确密码应 200，实际 %d：%s", rr.Code, rr.Body.String())
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value == pw {
		t.Fatalf("应下发派生 Cookie，实际 %+v", cookies)
	}
	if cookies[0].Value != webauth.PasswordCookieValue(s.cfg.Get().PasswordHash) {
		t.Fatal("Cookie 不是密码哈希的派生值")
	}
}

func TestPasswordLoginRejectsWrongPassword(t *testing.T) {
	s := newPasswordServer(t, "correct horse 123")
	rr := httptest.NewRecorder()
	s.handleLogin(rr, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"wrong"}`)))
	if rr.Code != http.StatusUnauthorized || len(rr.Result().Cookies()) != 0 {
		t.Fatalf("错误密码应 401 且不下发 Cookie，实际 %d cookies=%d", rr.Code, len(rr.Result().Cookies()))
	}
}

func TestPasswordSetupOnlyWhenFresh(t *testing.T) {
	s := newAuthServer(t, "")
	rr := httptest.NewRecorder()
	s.handlePasswordSetup(rr, httptest.NewRequest(http.MethodPost, "/api/password/setup",
		strings.NewReader(`{"password":"first-pass-123","confirm":"first-pass-123"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("首次设置应成功，实际 %d：%s", rr.Code, rr.Body.String())
	}
	cfg := s.cfg.Get()
	if cfg.PasswordHash == "" || strings.Contains(cfg.PasswordHash, "first-pass-123") {
		t.Fatalf("配置里应是哈希而不是明文：%q", cfg.PasswordHash)
	}
	if len(rr.Result().Cookies()) != 1 {
		t.Fatal("设置成功后应直接下发登录 Cookie")
	}
	// 第二次设置应被拒绝，不能覆盖已设密码
	rr2 := httptest.NewRecorder()
	s.handlePasswordSetup(rr2, httptest.NewRequest(http.MethodPost, "/api/password/setup",
		strings.NewReader(`{"password":"second-pass-456","confirm":"second-pass-456"}`)))
	if rr2.Code != http.StatusConflict {
		t.Fatalf("重复设置应 409，实际 %d", rr2.Code)
	}
}

func TestPasswordChangeRequiresCurrent(t *testing.T) {
	s := newPasswordServer(t, "old-pass-123")
	rr := httptest.NewRecorder()
	s.handlePasswordChange(rr, httptest.NewRequest(http.MethodPost, "/api/password",
		strings.NewReader(`{"current_password":"wrong","new_password":"new-pass-456","confirm":"new-pass-456"}`)))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("当前密码不对应 401，实际 %d", rr.Code)
	}
	rr2 := httptest.NewRecorder()
	s.handlePasswordChange(rr2, httptest.NewRequest(http.MethodPost, "/api/password",
		strings.NewReader(`{"current_password":"old-pass-123","new_password":"new-pass-456","confirm":"new-pass-456"}`)))
	if rr2.Code != http.StatusOK {
		t.Fatalf("修改密码应成功，实际 %d：%s", rr2.Code, rr2.Body.String())
	}
	if !webauth.VerifyPassword(s.cfg.Get().PasswordHash, "new-pass-456") {
		t.Fatal("新密码未生效")
	}
}

// TestHandleLoginSetsDerivedCookie 验证登录成功后下发的 Cookie 不含明文令牌。
func TestHandleLoginSetsDerivedCookie(t *testing.T) {
	const token = "s3cret-token"
	s := newAuthServer(t, token)

	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"`+token+`"}`))
	rr := httptest.NewRecorder()
	s.handleLogin(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rr.Code)
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("期望下发 1 个 Cookie，实际 %d", len(cookies))
	}
	got := cookies[0].Value
	if got == token {
		t.Fatal("Cookie 保存了明文令牌")
	}
	if got != webauth.CookieValue(token) {
		t.Fatal("Cookie 值不是约定的派生值")
	}
	if !cookies[0].HttpOnly {
		t.Fatal("Cookie 未设置 HttpOnly")
	}
}

// TestHandleLoginRejectsWrongToken 验证错误令牌返回 401 且不下发 Cookie。
func TestHandleLoginRejectsWrongToken(t *testing.T) {
	s := newAuthServer(t, "s3cret-token")

	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"wrong"}`))
	rr := httptest.NewRecorder()
	s.handleLogin(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("期望 401，实际 %d", rr.Code)
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Fatal("登录失败时不应下发 Cookie")
	}
}

// TestHandleLogoutClearsCookie 验证登出把 Cookie 置空并立即过期。
func TestHandleLogoutClearsCookie(t *testing.T) {
	s := newAuthServer(t, "s3cret-token")

	rr := httptest.NewRecorder()
	s.handleLogout(rr, httptest.NewRequest(http.MethodPost, "/api/logout", nil))

	cookies := rr.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value != "" || cookies[0].MaxAge >= 0 {
		t.Fatal("登出应下发空值且立即过期的 Cookie")
	}
}
