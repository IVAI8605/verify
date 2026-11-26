package main

import (
	"bytes"
	"strings"
	"testing"
)

// runCLI 执行一次 CLI 调用并捕获 stdout；退出码非 0 视为测试失败。
func runCLI(t *testing.T, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	code := run(args, &buf)
	if code != 0 {
		t.Fatalf("run(%v) 退出码=%d，期望 0；输出=%q", args, code, buf.String())
	}
	return buf.String()
}

// TestVersionSubcommand 验证 version 子命令有输出。
func TestVersionSubcommand(t *testing.T) {
	out := runCLI(t, "version")
	if strings.TrimSpace(out) == "" {
		t.Fatal("version 不应为空")
	}
}

// TestVersionSubcommandNoTokenLeak 验证全局 token 参数不会以明文出现在输出里。
func TestVersionSubcommandNoTokenLeak(t *testing.T) {
	const placeholder = "cli-明文令牌-禁止回显-9f7c"
	out := runCLI(t, "--token", placeholder, "version")
	if strings.Contains(out, placeholder) {
		t.Fatalf("version 输出泄漏了令牌明文：%q", out)
	}
}

// TestGlobalFlagsAccepted 验证六个全局参数都能被解析，且不影响 version 输出。
func TestGlobalFlagsAccepted(t *testing.T) {
	args := []string{
		"--url", "http://127.0.0.1:8898",
		"--token", "abc",
		"--data-dir", "/vol1/@appdata/verify/data",
		"--format", "json",
		"--raw",
		"--help-json",
		"version",
	}
	out := runCLI(t, args...)
	if strings.TrimSpace(out) == "" {
		t.Fatal("带全局参数时 version 仍应有输出")
	}
}

// TestRegisteredCommandNotExecuted 验证非只读类命令（此处用 trigger 类的 scan）返回非 0
// 并给出中文提示，防止脚本把「退出码 0 + 空输出」误判为操作已生效（destructive 命令尤其危险）。
func TestRegisteredCommandNotExecuted(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{"scan"}, &buf)
	if code == 0 {
		t.Fatalf("已登记但执行尚未接入的命令不应返回 0；输出=%q", buf.String())
	}
	if !strings.Contains(buf.String(), "尚未接入") {
		t.Fatalf("应给出中文提示说明执行尚未接入；输出=%q", buf.String())
	}
}

// TestUnknownSubcommand 验证未知子命令返回非 0 退出码并给出中文提示。
func TestUnknownSubcommand(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{"不存在的子命令"}, &buf)
	if code == 0 {
		t.Fatalf("未知子命令退出码应为非 0，实际=%d，输出=%q", code, buf.String())
	}
	if !strings.Contains(buf.String(), "未知子命令") {
		t.Fatalf("未知子命令应给出中文提示，实际输出=%q", buf.String())
	}
}
