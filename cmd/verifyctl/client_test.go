package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDoBearerAndEnvelope 验证 Bearer 鉴权、不使用 ?token=、响应体原样返回与 data 信封包裹。
func TestDoBearerAndEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("鉴权头错误：得到 %q，期望 %q", got, "Bearer test-token")
		}
		if r.URL.Query().Has("token") {
			t.Error("不应使用 ?token= 传递令牌")
		}
		if r.URL.Path != "/api/status" {
			t.Errorf("请求路径错误：%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"items":[]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	body, status, err := c.Do(context.Background(), Command{Path: "/api/status", Method: "GET"}, nil)
	if err != nil {
		t.Fatalf("Do 失败：%v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("状态码错误：%d，期望 200", status)
	}
	if want := `{"ok":true,"items":[]}`; string(body) != want {
		t.Fatalf("响应体被改写：得到 %s，期望 %s", body, want)
	}

	var buf bytes.Buffer
	if err := writeEnvelope(&buf, "status", body, false); err != nil {
		t.Fatalf("writeEnvelope 失败：%v", err)
	}
	if !strings.Contains(buf.String(), `"command":"status"`) {
		t.Errorf("信封缺少命令名：%s", buf.String())
	}
	if !strings.Contains(buf.String(), `"data":{"ok":true,"items":[]}`) {
		t.Errorf("信封缺少 data 包裹：%s", buf.String())
	}
	if strings.Contains(buf.String(), "test-token") {
		t.Error("信封泄漏令牌")
	}
}

// TestDoPasswordBasic 验证密码模式走 HTTP Basic（用户名为空），且不发 Bearer。
func TestDoPasswordBasic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "" || pass != "pw-123" {
			t.Errorf("Basic 鉴权错误：user=%q pass=%q ok=%v", user, pass, ok)
		}
		if got := r.Header.Get("Authorization"); strings.HasPrefix(got, "Bearer ") {
			t.Error("不应同时发送 Bearer")
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	c.Password = "pw-123"
	if _, status, err := c.Do(context.Background(), Command{Path: "/api/status", Method: "GET"}, nil); err != nil || status != http.StatusOK {
		t.Fatalf("Do 失败：status=%d err=%v", status, err)
	}
}

// Test409MapsToExit5 验证 409 按控制器裁定映射到退出码 5，且不产生退出码 7。
func Test409MapsToExit5(t *testing.T) {
	if got := exitCodeForHTTP(409); got != 5 {
		t.Fatalf("409→%d，期望 5", got)
	}
	if got := exitCodeForHTTP(409); got == 7 {
		t.Fatal("409 不得映射为退出码 7")
	}
}

// TestErrorNoTokenLeak 验证错误信封透传中文文案且不回显令牌。
func TestErrorNoTokenLeak(t *testing.T) {
	var buf bytes.Buffer
	if err := writeError(&buf, "status", &ErrDetail{Code: "auth", Message: "鉴权失败"}); err != nil {
		t.Fatalf("writeError 失败：%v", err)
	}
	if strings.Contains(buf.String(), "test-token") {
		t.Fatal("错误输出含令牌")
	}
	if !strings.Contains(buf.String(), "鉴权失败") {
		t.Fatalf("错误文案未透传：%s", buf.String())
	}
}

// TestExitCodeForHTTP 覆盖语义退出码映射表。
func TestExitCodeForHTTP(t *testing.T) {
	cases := []struct {
		status int
		want   int
	}{
		{200, 0}, {201, 0}, {204, 0},
		{401, 3}, {403, 3},
		{400, 5}, {404, 5}, {409, 5}, {410, 5},
		{500, 6}, {503, 6},
		{0, 4},
	}
	for _, tc := range cases {
		if got := exitCodeForHTTP(tc.status); got != tc.want {
			t.Errorf("HTTP %d → %d，期望 %d", tc.status, got, tc.want)
		}
	}
}

// TestWriteEnvelopeRawMode 验证 --raw 模式直接输出原始响应体，不加包裹。
func TestWriteEnvelopeRawMode(t *testing.T) {
	var buf bytes.Buffer
	raw := []byte(`[{"a":1}]`)
	if err := writeEnvelope(&buf, "groups", raw, true); err != nil {
		t.Fatalf("writeEnvelope(raw) 失败：%v", err)
	}
	if buf.String() != string(raw) {
		t.Fatalf("raw 模式被包裹：%s", buf.String())
	}
}

// TestDoPostTypedParams 验证 POST 按 Param.Type 把字符串参数转成正确的 JSON 字段类型。
func TestDoPostTypedParams(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("方法错误：%s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("解析请求体失败：%v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	cmd := Command{Path: "/api/folder_rename", Method: "POST", Params: []Param{
		{Name: "prefix_a", Type: "bool"},
		{Name: "sort_order", Type: "int"},
		{Name: "groups", Type: "json"},
	}}
	if _, _, err := c.Do(context.Background(), cmd, map[string]string{
		"prefix_a":   "true",
		"sort_order": "3",
		"groups":     `[{"id":1}]`,
	}); err != nil {
		t.Fatalf("Do 失败：%v", err)
	}
	if got["prefix_a"] != true {
		t.Errorf("bool 参数未转换：%#v", got["prefix_a"])
	}
	if got["sort_order"] != float64(3) {
		t.Errorf("int 参数未转换：%#v", got["sort_order"])
	}
	if arr, ok := got["groups"].([]any); !ok || len(arr) != 1 {
		t.Errorf("json 参数未内联：%#v", got["groups"])
	}
}

// TestParseErrorDetailVerbatim 验证服务端中文错误原文透传，不做冲突解析或文案改写。
func TestParseErrorDetailVerbatim(t *testing.T) {
	msg := "扫描进行中，请等本次结束再操作"
	e := parseErrorDetail([]byte(`{"error":"` + msg + `"}`))
	if e == nil || e.Message != msg {
		t.Fatalf("错误原文未透传：%#v", e)
	}
	var buf bytes.Buffer
	if err := writeError(&buf, "scan", e); err != nil {
		t.Fatalf("writeError 失败：%v", err)
	}
	if !strings.Contains(buf.String(), msg) {
		t.Fatalf("错误信封缺少原文：%s", buf.String())
	}
	if strings.Contains(buf.String(), "test-token") {
		t.Fatal("错误信封泄漏令牌")
	}
}
