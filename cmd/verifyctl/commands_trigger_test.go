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
	out, err := runTriggerCommand(context.Background(), c, Command{Path: "/api/scan", SideEffect: "trigger"}, map[string]string{"__cli_dry_run": "true"})
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

// TestTriggerWouldDoShape 验证 would_do 里带命令/method/path/params，控制参数不外泄，
// 业务 dry_run 则照常显示。
func TestTriggerWouldDoShape(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "t")
	cmd := Command{Name: "scan", Path: "/api/scan", Method: "POST", SideEffect: "trigger"}
	out, err := runTriggerCommand(context.Background(), c, cmd, map[string]string{
		"__cli_dry_run": "true", "__cli_yes": "true", "pretty": "true", "mode": "quick", "confirm_token": "secret", "dry_run": "true",
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
	if got.WouldDo.Params["dry_run"] != "true" {
		t.Fatalf("业务 dry_run 应在 would_do.params 里显示原值：%+v", got.WouldDo.Params)
	}
	for _, control := range []string{"__cli_dry_run", "__cli_yes", "pretty", "confirm_token"} {
		if _, ok := got.WouldDo.Params[control]; ok {
			t.Fatalf("控制参数 %s 不应进入 would_do.params", control)
		}
	}
	if strings.Contains(string(out), "secret") || strings.Contains(string(out), "__cli_") {
		t.Fatal("confirm_token 与内部保留 key 不得出现在输出里")
	}
}

// TestTriggerAllTriggerCommandsWired 验证所有 trigger 命令都接入 Run，
// destructive 仍保持 nil（留给 Task 9；write 已由 Task 8 接入）。
func TestTriggerAllTriggerCommandsWired(t *testing.T) {
	triggers := 0
	for _, cmd := range Commands {
		switch cmd.SideEffect {
		case "trigger":
			triggers++
			if cmd.Run == nil {
				t.Fatalf("trigger 命令 %s 的 Run 未接入", cmd.Name)
			}
		case "destructive":
			if cmd.Run != nil {
				t.Fatalf("破坏性命令 %s 不应在本任务接入 Run", cmd.Name)
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
	out, err := runTriggerCommand(context.Background(), c, cmd, map[string]string{"__cli_yes": "true", "mode": "quick"})
	if err != nil {
		t.Fatalf("--yes 执行失败：%v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/scan" {
		t.Fatalf("请求应为 POST /api/scan，实际 %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(string(gotBody), `"mode":"quick"`) {
		t.Fatalf("请求体缺业务参数：%s", gotBody)
	}
	if strings.Contains(string(gotBody), "__cli_") {
		t.Fatalf("内部保留 key 不应发给服务端：%s", gotBody)
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

// TestTriggerBusinessDryRunPassThrough 修复本地 dry-run 开关与服务端业务参数 dry_run 撞 key：
// 带 --yes 且不带本地 --dry-run 时，业务 dry_run=true 必须原样进 HTTP 请求体；
// 带本地 --dry-run 时才回 would_do，且回包能看到业务 dry_run 原值。
func TestTriggerBusinessDryRunPassThrough(t *testing.T) {
	var gotBody string
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if strings.Contains(gotBody, "__cli_") {
			t.Errorf("内部保留 key 泄漏进请求体：%s", gotBody)
		}
		w.Write([]byte(`{"ok":true,"dry_run":true}`))
	}))
	defer srv.Close()

	cmd, ok := findCommand("index repair")
	if !ok {
		t.Fatal("命令表应有 index repair")
	}
	c := NewClient(srv.URL, "t")

	// 服务端预演：业务 dry_run=true + 本地 --yes，必须真的发请求。
	out, err := runTriggerCommand(context.Background(), c, cmd, map[string]string{
		"__cli_yes": "true", "dry_run": "true",
	})
	if err != nil {
		t.Fatalf("业务 dry_run 执行失败：%v", err)
	}
	if !called {
		t.Fatal("业务 dry_run=true 被本地开关拦截，未发请求")
	}
	if strings.Contains(string(out), "would_do") {
		t.Fatalf("业务 dry_run 不应触发本地 would_do：%s", out)
	}
	if !strings.Contains(gotBody, `"dry_run":"true"`) && !strings.Contains(gotBody, `"dry_run":true`) {
		t.Fatalf("请求体缺业务 dry_run=true：%s", gotBody)
	}

	// 从 CLI 入口再走一遍，确认 --dry_run=true 映射到业务参数而非本地开关。
	called = false
	gotBody = ""
	var buf bytes.Buffer
	if code := run([]string{"--url", srv.URL, "--token", "t", "index repair", "--yes", "--dry_run=true"}, &buf); code != 0 {
		t.Fatalf("CLI 传业务 dry_run 退出码=%d，输出=%q", code, buf.String())
	}
	if !called || !strings.Contains(gotBody, "dry_run") || !strings.Contains(gotBody, "true") {
		t.Fatalf("CLI 未透传业务 dry_run：called=%v body=%q", called, gotBody)
	}

	// 本地 --dry-run 优先：不发请求，但 would_do 里能看到业务 dry_run 原值。
	called = false
	buf.Reset()
	if code := run([]string{"--url", srv.URL, "--token", "t", "index repair", "--dry-run", "--yes", "--dry_run=true"}, &buf); code != 0 {
		t.Fatalf("本地 dry-run 退出码=%d，输出=%q", code, buf.String())
	}
	if called {
		t.Fatal("本地 --dry-run 不应发请求")
	}
	if !strings.Contains(buf.String(), `"dry_run":"true"`) {
		t.Fatalf("would_do 未显示业务 dry_run 原值：%q", buf.String())
	}
	if strings.Contains(buf.String(), "__cli_") {
		t.Fatalf("would_do 泄漏内部保留 key：%q", buf.String())
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
	_, err := runTriggerCommand(context.Background(), c, cmd, map[string]string{"__cli_yes": "true"})
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

// TestBoolParamsEncodedAsBool 验证服务端解码为 bool 的注册表参数发出的是 JSON 布尔，
// 而非字符串（字符串会让 index repair 真机 400、health scan 静默变 false）。
func TestBoolParamsEncodedAsBool(t *testing.T) {
	cases := []struct {
		command string
		params  map[string]string
	}{
		{"index repair", map[string]string{"dry_run": "true"}},
		{"video reanalyze", map[string]string{"all": "true"}},
		{"health scan", map[string]string{"restrict_charset": "true", "non_cjk": "true", "force": "true"}},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			cmd, ok := findCommand(tc.command)
			if !ok {
				t.Fatalf("命令表应有 %s", tc.command)
			}
			// 先钉住注册表类型，再钉住线上字节，防止两处各自漂移。
			types := make(map[string]string, len(cmd.Params))
			for _, p := range cmd.Params {
				types[p.Name] = p.Type
			}
			params := map[string]string{"__cli_yes": "true"}
			for k, v := range tc.params {
				if types[k] != "bool" {
					t.Fatalf("%s 的 %s 应为 bool，实际 %s", tc.command, k, types[k])
				}
				params[k] = v
			}

			var body string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				body = string(b)
				w.Write([]byte(`{"ok":true}`))
			}))
			defer srv.Close()

			c := NewClient(srv.URL, "t")
			if _, err := runTriggerCommand(context.Background(), c, cmd, params); err != nil {
				t.Fatalf("执行失败：%v", err)
			}
			for k := range tc.params {
				if !strings.Contains(body, `"`+k+`":true`) {
					t.Fatalf("%s 应发真布尔 %s:true，实际请求体=%s", tc.command, k, body)
				}
				if strings.Contains(body, `"`+k+`":"true"`) {
					t.Fatalf("%s 把 %s 发成了字符串，实际请求体=%s", tc.command, k, body)
				}
			}
		})
	}
}
