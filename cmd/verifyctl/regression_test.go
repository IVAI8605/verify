package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runOut 执行一次 run() 并返回输出与退出码（不强制 0，供回归用例断言）。
func runOut(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var buf bytes.Buffer
	code := run(args, &buf)
	return buf.String(), code
}

// dataEnvelope 是解析信封 data 字段的辅助结构（data 是任意 JSON）。
type dataEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *ErrDetail      `json:"error"`
}

// TestRenameUndoUsesPathParam 回归 C1：rename undo 必须声明 id 且按路径参数拼接，
// 否则请求打到 /api/rename/undo/ 永远 400（handler 从 URL 尾段取 batch_id）。
func TestRenameUndoUsesPathParam(t *testing.T) {
	cmd, ok := findCommand("rename undo")
	if !ok {
		t.Fatal("注册表缺少 rename undo")
	}
	if len(cmd.PathParams) != 1 || cmd.PathParams[0] != "id" {
		t.Fatalf("rename undo PathParams 应为 [id]，实际 %v", cmd.PathParams)
	}
	found := false
	for _, p := range cmd.Params {
		if p.Name == "id" && p.Type == "int" && p.Required {
			found = true
		}
	}
	if !found {
		t.Fatalf("rename undo 应声明必填 int 参数 id，实际 %+v", cmd.Params)
	}

	// dry-run 的 would_do.path 应已代入路径参数（验证 effectivePath + parseCommandParams）。
	out, code := runOut(t, "rename", "undo", "--id", "7", "--dry-run")
	if code != 0 {
		t.Fatalf("dry-run 退出码=%d，期望 0；输出=%s", code, out)
	}
	var env dataEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("dry-run 输出非 JSON：%v（%s）", err, out)
	}
	var dry struct {
		WouldDo struct {
			Path string `json:"path"`
		} `json:"would_do"`
	}
	_ = json.Unmarshal(env.Data, &dry)
	if dry.WouldDo.Path != "/api/rename/undo/7" {
		t.Fatalf("would_do.path=%q，期望 /api/rename/undo/7", dry.WouldDo.Path)
	}

	// 真实请求应打到 /api/rename/undo/7，且 id 不从 body 重复发送。
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method=%s，期望 POST", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"id"`) {
			t.Errorf("id 属路径参数，不应进 body：%s", body)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "")
	if _, status, err := c.Do(context.Background(), cmd, map[string]string{"id": "7"}); err != nil || status != 200 {
		t.Fatalf("Do 失败：status=%d err=%v", status, err)
	}
	if gotPath != "/api/rename/undo/7" {
		t.Fatalf("请求路径=%q，期望 /api/rename/undo/7", gotPath)
	}
}

// TestDangerInjectsConfirm 回归 C3：destructive 命令声明 confirm 时必须自动注入 confirm=true，
// 否则 trash empty/delete、oplog clear 这类「服务端强制校验 confirm」的接口永远 400。
// 用一个真校验 confirm 的 handler 端到端跑自然流程：dry-run 取令牌 → --yes + 令牌执行。
func TestDangerInjectsConfirm(t *testing.T) {
	var sawConfirm bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Confirm bool `json:"confirm"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sawConfirm = body.Confirm
		if !body.Confirm {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"清空隔离区不可恢复，需显式确认（confirm=true）"}`))
			return
		}
		_, _ = w.Write([]byte(`{"removed":0,"errors":[]}`))
	}))
	defer srv.Close()

	// 1) dry-run 取确认令牌（不应发请求，sawConfirm 仍 false）。
	dry, code := runOut(t, "--url", srv.URL, "trash", "empty", "--dry-run")
	if code != 0 {
		t.Fatalf("dry-run 退出码=%d，期望 0；输出=%s", code, dry)
	}
	var env dataEnvelope
	if err := json.Unmarshal([]byte(dry), &env); err != nil {
		t.Fatalf("dry-run 非 JSON：%v（%s）", err, dry)
	}
	var d struct {
		ConfirmToken string `json:"confirm_token"`
	}
	_ = json.Unmarshal(env.Data, &d)
	if d.ConfirmToken == "" {
		t.Fatalf("dry-run 缺 confirm_token：%s", dry)
	}
	if sawConfirm {
		t.Fatal("dry-run 不应发请求")
	}

	// 2) --yes + 令牌执行：CLI 应自动注入 confirm=true，服务端通过。
	out, code := runOut(t, "--url", srv.URL, "trash", "empty", "--yes", "--confirm-token", d.ConfirmToken)
	if code != 0 {
		t.Fatalf("执行退出码=%d（旧 bug 会因 {} 被 400 而退 5）；输出=%s", code, out)
	}
	if !sawConfirm {
		t.Fatal("执行请求体应含 confirm=true，实际服务端未收到")
	}
	if !strings.Contains(out, "removed") {
		t.Fatalf("应透传响应体：%s", out)
	}
}

// TestBinaryResultCommands 回归 C2：CSV/HTML/BOM 导出类必须走原样输出，不能进 JSON 信封。
func TestBinaryResultCommands(t *testing.T) {
	for _, name := range []string{"export", "report", "oplog export"} {
		cmd, ok := findCommand(name)
		if !ok {
			t.Fatalf("缺少命令 %s", name)
		}
		if !binaryResultCommand(cmd) {
			t.Errorf("%s(%s) 应被 binaryResultCommand 判定为原样输出", name, cmd.Path)
		}
	}

	cases := []struct {
		name string
		args []string
		body string
	}{
		{"export", []string{"export", "--yes"}, "a,b\n1,2\n"},
		{"report", []string{"report", "--yes"}, "<html><body>报 告</body></html>"},
		{"oplog export", []string{"oplog", "export", "--yes"}, "\ufefftime,action\n1,scan\n"},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(tc.body))
		}))
		out, code := runOut(t, append([]string{"--url", srv.URL}, tc.args...)...)
		srv.Close()
		if code != 0 {
			t.Fatalf("%s 退出码=%d，期望 0；输出=%s", tc.name, code, out)
		}
		if out != tc.body {
			t.Fatalf("%s 输出应原样为响应体：得到 %q，期望 %q（不得包 JSON 信封）", tc.name, out, tc.body)
		}
	}
}

// TestWriteResultNonJSONFallback 回归 C2 兜底：未列入 binaryResultCommand 的非 JSON 响应
// 也必须原样写出，而不是 json.RawMessage 编码失败退出 1。
func TestWriteResultNonJSONFallback(t *testing.T) {
	var buf bytes.Buffer
	raw := []byte("纯文本不是 JSON\n")
	if err := writeResult(&buf, "status", raw, false, false); err != nil {
		t.Fatalf("writeResult 不应报错：%v", err)
	}
	if buf.String() != string(raw) {
		t.Fatalf("非 JSON 响应应原样输出：得到 %q，期望 %q", buf.String(), string(raw))
	}

	// 合法 JSON 仍应包信封。
	buf.Reset()
	if err := writeResult(&buf, "status", []byte(`{"ok":true}`), false, false); err != nil {
		t.Fatalf("writeResult 报错：%v", err)
	}
	if !strings.Contains(buf.String(), `"data":{"ok":true}`) {
		t.Fatalf("合法 JSON 应包信封：%s", buf.String())
	}
}

// TestEventStream 回归 I1：/api/events 必须流式逐行输出，读到流结束即返回 0，
// 不能走 io.ReadAll 阻塞到超时再失败。
func TestEventStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events" {
			t.Errorf("路径=%s，期望 /api/events", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"a\":1}\n\n"))
		if fl != nil {
			fl.Flush()
		}
		_, _ = w.Write([]byte("data: 2\n\n"))
		if fl != nil {
			fl.Flush()
		}
	}))
	defer srv.Close()

	out, code := runOut(t, "--url", srv.URL, "events")
	if code != 0 {
		t.Fatalf("events 退出码=%d，期望 0；输出=%s", code, out)
	}
	if !strings.Contains(out, `data: {"a":1}`) || !strings.Contains(out, "data: 2") {
		t.Fatalf("应逐行输出 SSE 事件：%s", out)
	}
}
