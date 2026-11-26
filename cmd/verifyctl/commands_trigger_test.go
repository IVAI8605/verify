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

// TestTriggerDryRunNoRequest 验证 dry-run 只回 would_do，绝不发 HTTP 请求。
func TestTriggerDryRunNoRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	out, err := runTriggerCommand(context.Background(), c, Command{Path: "/api/scan", SideEffect: "trigger"}, map[string]string{"dry_run": "true"})
	if err != nil {
		t.Fatalf("dry-run 不应报错：%v", err)
	}
	if called {
		t.Fatal("dry-run 不应发请求")
	}
	if !strings.Contains(string(out), "would_do") {
		t.Fatalf("dry-run 回包缺 would_do：%s", out)
	}
}

// TestTriggerDefaultIsDryRun 验证不带 --yes 时默认按 dry-run 处理（commit：dry-run default）。
func TestTriggerDefaultIsDryRun(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	out, err := runTriggerCommand(context.Background(), c, Command{Name: "scan stop", Path: "/api/scan/stop", Method: "POST", SideEffect: "trigger"}, nil)
	if err != nil {
		t.Fatalf("默认 dry-run 不应报错：%v", err)
	}
	if called {
		t.Fatal("未带 --yes 时不应发请求")
	}
	if !strings.Contains(string(out), "would_do") {
		t.Fatalf("默认 dry-run 回包缺 would_do：%s", out)
	}
}

// TestTriggerWouldDoShape 验证 would_do 里带命令/method/path/params，且控制参数不外泄。
func TestTriggerWouldDoShape(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "t")
	cmd := Command{Name: "scan", Path: "/api/scan", Method: "POST", SideEffect: "trigger"}
	out, err := runTriggerCommand(context.Background(), c, cmd, map[string]string{
		"dry_run": "true", "yes": "true", "pretty": "true", "mode": "quick", "confirm_token": "secret",
	})
	if err != nil {
		t.Fatalf("dry-run 不应报错：%v", err)
	}
	var got struct {
		DryRun  bool `json:"dry_run"`
		WouldDo struct {
			Command string            `json:"command"`
			Method  string            `json:"method"`
			Path    string            `json:"path"`
			Params  map[string]string `json:"params"`
		} `json:"would_do"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("would_do 不是合法 JSON：%v（%s）", err, out)
	}
	if !got.DryRun {
		t.Fatal("dry_run 应为 true")
	}
	if got.WouldDo.Command != "scan" || got.WouldDo.Method != "POST" || got.WouldDo.Path != "/api/scan" {
		t.Fatalf("would_do 元信息不对：%+v", got.WouldDo)
	}
	if got.WouldDo.Params["mode"] != "quick" {
		t.Fatalf("would_do 应含业务参数 mode：%+v", got.WouldDo.Params)
	}
	for _, control := range []string{"dry_run", "yes", "pretty", "confirm_token"} {
		if _, ok := got.WouldDo.Params[control]; ok {
			t.Fatalf("控制参数 %s 不应进入 would_do.params", control)
		}
	}
	if strings.Contains(string(out), "secret") {
		t.Fatal("confirm_token 不得出现在输出里")
	}
}

// TestTriggerAllTriggerCommandsWired 验证所有 trigger 命令都接入 Run，
// 而 write/destructive 仍保持 nil（留给后续任务）。
func TestTriggerAllTriggerCommandsWired(t *testing.T) {
	triggers := 0
	for _, cmd := range Commands {
		switch cmd.SideEffect {
		case "trigger":
			triggers++
			if cmd.Run == nil {
				t.Fatalf("trigger 命令 %s 的 Run 未接入", cmd.Name)
			}
		case "write", "destructive":
			if cmd.Run != nil {
				t.Fatalf("副作用命令 %s 不应在本任务接入 Run", cmd.Name)
			}
		}
	}
	if triggers == 0 {
		t.Fatal("命令表里没有 trigger 命令")
	}
}

// TestTriggerYesExecutes 验证 --yes 时才真正发请求，路径/方法/参数正确，并且原样透传响应。
func TestTriggerYesExecutes(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"ok":true,"task_id":"t-42"}`))
	}))
	defer srv.Close()

	cmd, ok := findCommand("scan")
	if !ok {
		t.Fatal("命令表应有 scan")
	}
	c := NewClient(srv.URL, "t")
	out, err := runTriggerCommand(context.Background(), c, cmd, map[string]string{"yes": "true", "mode": "quick"})
	if err != nil {
		t.Fatalf("--yes 执行失败：%v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/scan" {
		t.Fatalf("请求应为 POST /api/scan，实际 %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(string(gotBody), `"mode":"quick"`) {
		t.Fatalf("请求体缺业务参数：%s", gotBody)
	}
	if strings.Contains(string(gotBody), "yes") {
		t.Fatalf("控制参数 yes 不应发给服务端：%s", gotBody)
	}
	if !strings.Contains(string(out), "t-42") {
		t.Fatalf("响应未原样透传：%s", out)
	}
}

// TestTriggerCLIRoundTrip 验证从 run() 入口进来时，默认 dry-run 不发请求，
// 带 --yes 才真正调用服务端，且响应进信封、令牌不回显。
func TestTriggerCLIRoundTrip(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Header.Get("Authorization") != "Bearer cli-tok" {
			t.Error("Bearer 令牌未随请求发出")
		}
		w.Write([]byte(`{"ok":true,"task_id":"t-7"}`))
	}))
	defer srv.Close()

	const tok = "cli-tok"
	var dryBuf bytes.Buffer
	if code := run([]string{"--url", srv.URL, "--token", tok, "scan"}, &dryBuf); code != 0 {
		t.Fatalf("默认 dry-run 退出码=%d，输出=%q", code, dryBuf.String())
	}
	if called {
		t.Fatal("默认 dry-run 不应发请求")
	}
	if !strings.Contains(dryBuf.String(), "would_do") || strings.Contains(dryBuf.String(), tok) {
		t.Fatalf("dry-run 输出异常：%q", dryBuf.String())
	}

	var yesBuf bytes.Buffer
	if code := run([]string{"--url", srv.URL, "--token", tok, "scan", "--yes"}, &yesBuf); code != 0 {
		t.Fatalf("--yes 退出码=%d，输出=%q", code, yesBuf.String())
	}
	if !called {
		t.Fatal("--yes 应发请求")
	}
	var env struct {
		OK      bool            `json:"ok"`
		Command string          `json:"command"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(yesBuf.Bytes(), &env); err != nil {
		t.Fatalf("输出不是合法信封：%v（%q）", err, yesBuf.String())
	}
	if !env.OK || env.Command != "scan" || !strings.Contains(string(env.Data), "t-7") {
		t.Fatalf("信封内容不对：%q", yesBuf.String())
	}
	if strings.Contains(yesBuf.String(), tok) {
		t.Fatalf("输出泄漏了令牌明文：%q", yesBuf.String())
	}
}

// TestTriggerErrorMapping 验证 --yes 下的 HTTP 错误映射为带状态码的 commandHTTPError。
func TestTriggerErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"扫描进行中"}`))
	}))
	defer srv.Close()

	cmd, ok := findCommand("scan stop")
	if !ok {
		t.Fatal("命令表应有 scan stop")
	}
	c := NewClient(srv.URL, "t")
	_, err := runTriggerCommand(context.Background(), c, cmd, map[string]string{"yes": "true"})
	if err == nil {
		t.Fatal("409 应返回错误")
	}
	status, detail := httpStatusFromError(err)
	if status != http.StatusConflict {
		t.Fatalf("状态码应为 409，实际 %d", status)
	}
	if detail == nil || detail.Message != "扫描进行中" {
		t.Fatalf("错误文案未原样保留：%+v", detail)
	}
}
