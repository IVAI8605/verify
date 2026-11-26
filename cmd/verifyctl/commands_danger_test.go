package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// confirmTokenFromDryRun 从 dry-run 回包里取出 confirm_token，取不到即测试失败。
func confirmTokenFromDryRun(t *testing.T, dry []byte) string {
	t.Helper()
	var got struct {
		ConfirmToken string `json:"confirm_token"`
	}
	if err := json.Unmarshal(dry, &got); err != nil || got.ConfirmToken == "" {
		t.Fatalf("dry-run 缺 confirm_token：%v（%s）", err, dry)
	}
	return got.ConfirmToken
}

// TestDangerRequiresConfirmToken 是 Task 9 核心红线：
// dry-run 取确认令牌；已带 --yes 但缺令牌、或令牌错误，都不得发请求（本地退出码 2）。
func TestDangerRequiresConfirmToken(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	cmd := Command{Name: "trash empty", Path: "/api/trash/empty", Method: "POST", SideEffect: "destructive"}

	// dry-run → 取 confirm token
	dry, err := runDangerCommand(context.Background(), c, cmd, nil, "")
	if err != nil {
		t.Fatalf("dry-run 不应报错：%v", err)
	}
	var got struct {
		DryRun       bool   `json:"dry_run"`
		ConfirmToken string `json:"confirm_token"`
	}
	if err := json.Unmarshal(dry, &got); err != nil {
		t.Fatalf("dry-run 不是合法 JSON：%v（%s）", err, dry)
	}
	if !got.DryRun || got.ConfirmToken == "" {
		t.Fatalf("dry-run 应带 confirm_token：%s", dry)
	}

	// 缺 token 执行 → 报错且不发请求
	if _, err := runDangerCommand(context.Background(), c, cmd, map[string]string{"__cli_yes": "true"}, ""); err == nil {
		t.Fatal("带 --yes 缺 confirm_token 应报用法错误")
	}
	if called {
		t.Fatal("缺 confirm_token 不应发请求")
	}

	// 错 token 执行 → 报错且不发请求
	if _, err := runDangerCommand(context.Background(), c, cmd, map[string]string{"__cli_yes": "true"}, "deadbeef00000000"); err == nil {
		t.Fatal("错误 confirm_token 应报用法错误")
	}
	if called {
		t.Fatal("错误 confirm_token 不应发请求")
	}
}

// TestDangerExecuteWithValidToken 令牌正确且 --yes 齐全才发请求，控制参数绝不上行。
func TestDangerExecuteWithValidToken(t *testing.T) {
	var gotBody map[string]any
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		_ = dec.Decode(&gotBody)
		w.Write([]byte(`{"ok":true,"deleted":2}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	// 用注册表里的真实命令，确保 confirm 按 bool 类型编码（验证业务参数按声明类型透传）。
	cmd, ok := findCommand("trash empty")
	if !ok {
		t.Fatal("注册表缺少 trash empty")
	}

	dry, err := runDangerCommand(context.Background(), c, cmd, map[string]string{"confirm": "true"}, "")
	if err != nil {
		t.Fatalf("dry-run 失败：%v", err)
	}
	token := confirmTokenFromDryRun(t, dry)

	out, err := runDangerCommand(context.Background(), c, cmd, map[string]string{
		"__cli_yes": "true", "confirm_token": token, "confirm": "true",
	}, token)
	if err != nil {
		t.Fatalf("双门槛齐全不应报错：%v", err)
	}
	if !called {
		t.Fatal("双门槛齐全应发请求")
	}
	if !strings.Contains(string(out), "deleted") {
		t.Fatalf("应透传响应体：%s", out)
	}
	// 控制参数不得上行：confirm_token / __cli_yes 都不在请求体里
	if _, ok := gotBody["confirm_token"]; ok {
		t.Fatalf("confirm_token 不得发给服务端：%v", gotBody)
	}
	if _, ok := gotBody["__cli_yes"]; ok {
		t.Fatalf("__cli_yes 不得发给服务端：%v", gotBody)
	}
	if gotBody["confirm"] != true {
		t.Fatalf("业务参数 confirm 应原样透传：%v", gotBody)
	}
}

// TestDangerTokenExpired 令牌跨过两个时间片后执行必须被拒，不发请求；同片与相邻片仍有效。
func TestDangerTokenExpired(t *testing.T) {
	base := time.Unix(1700000400, 0) // 对齐时间片边界，便于推演
	oldNow := confirmNow
	defer func() { confirmNow = oldNow }()

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	cmd := Command{Name: "oplog clear", Path: "/api/oplog/clear", Method: "POST", SideEffect: "destructive"}

	confirmNow = func() time.Time { return base }
	dry, err := runDangerCommand(context.Background(), c, cmd, nil, "")
	if err != nil {
		t.Fatalf("dry-run 失败：%v", err)
	}
	token := confirmTokenFromDryRun(t, dry)

	// 同片执行：放行
	confirmNow = func() time.Time { return base.Add(300 * time.Second) }
	if _, err := runDangerCommand(context.Background(), c, cmd, map[string]string{"__cli_yes": "true", "confirm_token": token}, token); err != nil {
		t.Fatalf("同片令牌应有效：%v", err)
	}
	// 下一片（600s+）：滑动窗口仍放行
	confirmNow = func() time.Time { return base.Add(601 * time.Second) }
	if _, err := runDangerCommand(context.Background(), c, cmd, map[string]string{"__cli_yes": "true", "confirm_token": token}, token); err != nil {
		t.Fatalf("跨一片仍应有效：%v", err)
	}
	// 再过一片（1200s+）：过期拒绝，不发请求
	called = false
	confirmNow = func() time.Time { return base.Add(1201 * time.Second) }
	if _, err := runDangerCommand(context.Background(), c, cmd, map[string]string{"__cli_yes": "true", "confirm_token": token}, token); err == nil {
		t.Fatal("过期令牌应报错")
	}
	if called {
		t.Fatal("过期令牌不应发请求")
	}
}

// TestDangerWireAllDestructive 断言注册表里全部 destructive 命令都已接入双门槛执行器。
func TestDangerWireAllDestructive(t *testing.T) {
	wired, total := 0, 0
	for _, cmd := range Commands {
		if cmd.SideEffect != "destructive" {
			continue
		}
		total++
		if cmd.Run == nil {
			t.Errorf("destructive 命令 %s 未接线（Run 为空）", cmd.Name)
			continue
		}
		wired++
	}
	if total == 0 {
		t.Fatal("注册表里没有 destructive 命令，接线断言失效")
	}
	if wired != total {
		t.Fatalf("destructive 接线 %d/%d", wired, total)
	}
}

// TestDangerCLIFlow 走完整 run()：dry-run 取令牌 → 缺令牌退出 2 → 错令牌退出 2 → 正确令牌退出 0，
// 且前两次绝不发请求。验证入口层把 usageError 映射成退出码 2。
func TestDangerCLIFlow(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var dry bytes.Buffer
	if code := run([]string{"--url", srv.URL, "--token", "t", "trash", "empty"}, &dry); code != 0 {
		t.Fatalf("dry-run 退出码=%d，输出=%s", code, dry.String())
	}
	var env struct {
		OK      bool            `json:"ok"`
		Command string          `json:"command"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(dry.Bytes(), &env); err != nil {
		t.Fatalf("信封解析失败：%v（%s）", err, dry.String())
	}
	if env.Command != "trash empty" {
		t.Fatalf("信封 command=%q", env.Command)
	}
	token := confirmTokenFromDryRun(t, env.Data)

	var buf bytes.Buffer
	if code := run([]string{"--url", srv.URL, "--token", "t", "trash", "empty", "--yes"}, &buf); code != 2 {
		t.Fatalf("缺 confirm_token 退出码应=2，实际=%d，输出=%s", code, buf.String())
	}
	if called {
		t.Fatal("缺 confirm_token 不应发请求")
	}

	buf.Reset()
	if code := run([]string{"--url", srv.URL, "--token", "t", "trash", "empty", "--yes", "--confirm-token", "ffffffffffffffff"}, &buf); code != 2 {
		t.Fatalf("错误 confirm_token 退出码应=2，实际=%d，输出=%s", code, buf.String())
	}
	if called {
		t.Fatal("错误 confirm_token 不应发请求")
	}

	buf.Reset()
	if code := run([]string{"--url", srv.URL, "--token", "t", "trash", "empty", "--yes", "--confirm-token", token}, &buf); code != 0 {
		t.Fatalf("正确令牌退出码应=0，实际=%d，输出=%s", code, buf.String())
	}
	if !called {
		t.Fatal("正确令牌应发请求")
	}
}
