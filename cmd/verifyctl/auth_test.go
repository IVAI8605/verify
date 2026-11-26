package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveTokenPriority 验证令牌来源优先级：--token > VERIFY_TOKEN > config.json。
func TestResolveTokenPriority(t *testing.T) {
	t.Setenv("VERIFY_TOKEN", "env-tok")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"access_token":"file-tok"}`), 0o600); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}

	got, err := resolveToken("", dir)
	if err != nil {
		t.Fatalf("读取环境变量令牌失败：%v", err)
	}
	if got != "env-tok" {
		t.Fatalf("环境变量优先级错误：得到 %q，期望 %q", got, "env-tok")
	}

	got, err = resolveToken("flag-tok", dir)
	if err != nil {
		t.Fatalf("读取命令行令牌失败：%v", err)
	}
	if got != "flag-tok" {
		t.Fatalf("命令行优先级错误：得到 %q，期望 %q", got, "flag-tok")
	}
}

// TestResolveTokenFromFile 验证无命令行与环境变量时回退到 config.json；缺文件或缺字段返回空串不报错。
func TestResolveTokenFromFile(t *testing.T) {
	t.Setenv("VERIFY_TOKEN", "")
	dir := t.TempDir()

	got, err := resolveToken("", dir)
	if err != nil {
		t.Fatalf("配置文件不存在时不应报错：%v", err)
	}
	if got != "" {
		t.Fatalf("配置文件不存在时应返回空串，得到 %q", got)
	}

	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"min_size":1024}`), 0o600); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}
	got, err = resolveToken("", dir)
	if err != nil {
		t.Fatalf("配置缺字段时不应报错：%v", err)
	}
	if got != "" {
		t.Fatalf("配置缺 access_token 时应返回空串，得到 %q", got)
	}

	if err := os.WriteFile(path, []byte(`{"access_token":"file-tok"}`), 0o600); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}
	got, err = resolveToken("", dir)
	if err != nil {
		t.Fatalf("读取配置令牌失败：%v", err)
	}
	if got != "file-tok" {
		t.Fatalf("配置令牌读取错误：得到 %q，期望 %q", got, "file-tok")
	}
}

// TestDoesNotReadTokenHint 验证不读取 access-token.txt 明文提示文件。
func TestDoesNotReadTokenHint(t *testing.T) {
	t.Setenv("VERIFY_TOKEN", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "access-token.txt"), []byte("hint-tok"), 0o600); err != nil {
		t.Fatalf("写提示文件失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}

	got, err := resolveToken("", dir)
	if err != nil {
		t.Fatalf("不应依赖提示文件：%v", err)
	}
	if got != "" || strings.Contains(got, "hint-tok") {
		t.Fatalf("不得读取提示文件，期望空串，得到 %q", got)
	}
}

// TestAuthTokenRequest 验证鉴权子命令的 GET 路径、Bearer 头与 X-Requested-With 标记。
func TestAuthTokenRequest(t *testing.T) {
	var method, path, requestedWith, auth, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		requestedWith = r.Header.Get("X-Requested-With")
		auth = r.Header.Get("Authorization")
		query = r.URL.RawQuery
		w.Write([]byte(`{"token":"server-tok"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "cli-tok")
	tok, err := c.AuthToken(context.Background())
	if err != nil {
		t.Fatalf("AuthToken 失败：%v", err)
	}
	if tok != "server-tok" {
		t.Fatalf("令牌读取错误：得到 %q，期望 %q", tok, "server-tok")
	}
	if method != http.MethodGet {
		t.Errorf("方法错误：%s", method)
	}
	if path != "/api/auth/token" {
		t.Errorf("路径错误：%s", path)
	}
	if requestedWith != "verify" {
		t.Errorf("缺少 X-Requested-With 标记：%q", requestedWith)
	}
	if auth != "Bearer cli-tok" {
		t.Errorf("鉴权头错误：%q", auth)
	}
	if strings.Contains(query, "token") {
		t.Errorf("不得用查询串传令牌：%q", query)
	}
}

// TestAuthTokenServerError 验证服务端中文错误原文透传且不回显令牌。
func TestAuthTokenServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"缺少请求标记"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "cli-tok")
	_, err := c.AuthToken(context.Background())
	if err == nil {
		t.Fatal("服务端 403 应返回错误")
	}
	if !strings.Contains(err.Error(), "缺少请求标记") {
		t.Fatalf("中文错误原文未透传：%v", err)
	}
	if strings.Contains(err.Error(), "cli-tok") {
		t.Fatalf("错误文案泄漏令牌：%v", err)
	}
}
