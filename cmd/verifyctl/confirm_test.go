package main

import (
	"testing"
)

// TestMakeConfirmTokenDeterministicAndDistinct 验证同一时间片内令牌确定，
// 命令或参数不同令牌必不同，长度固定为 16 位十六进制（短 token）。
func TestMakeConfirmTokenDeterministicAndDistinct(t *testing.T) {
	cmd := Command{Name: "trash empty", Path: "/api/trash/empty", Method: "POST"}
	p := map[string]string{"confirm": "true"}
	tok1 := makeConfirmToken(cmd, p)
	tok2 := makeConfirmToken(cmd, p)
	if tok1 != tok2 {
		t.Fatalf("同一时间片内令牌应确定：%s vs %s", tok1, tok2)
	}
	if len(tok1) != 16 {
		t.Fatalf("令牌应为 16 位十六进制，实际 %d 位：%s", len(tok1), tok1)
	}
	if makeConfirmToken(cmd, map[string]string{"confirm": "false"}) == tok1 {
		t.Fatal("参数不同令牌必须不同")
	}
	other := Command{Name: "trash delete", Path: "/api/trash/delete", Method: "POST"}
	if makeConfirmToken(other, p) == tok1 {
		t.Fatal("命令不同令牌必须不同")
	}
}

// TestMakeConfirmTokenIgnoresControlParams 验证控制/内部参数（confirm_token、pretty、__cli_*）
// 不进令牌哈希：dry-run 签发与执行校验必须基于同一份「将要发送的参数」。
func TestMakeConfirmTokenIgnoresControlParams(t *testing.T) {
	cmd := Command{Name: "trash empty", Path: "/api/trash/empty", Method: "POST"}
	base := makeConfirmToken(cmd, map[string]string{"confirm": "true"})
	withControl := makeConfirmToken(cmd, map[string]string{
		"confirm": "true", "confirm_token": "whatever", "pretty": "true",
		"__cli_yes": "true", "__cli_dry_run": "true",
	})
	if base != withControl {
		t.Fatal("控制/内部参数不得影响确认令牌")
	}
}

// TestVerifyConfirmToken 覆盖相同/不同/空/长度不一致四种比较分支。
func TestVerifyConfirmToken(t *testing.T) {
	if !verifyConfirmToken("abc123", "abc123") {
		t.Fatal("相同令牌应通过")
	}
	if verifyConfirmToken("abc123", "abc124") {
		t.Fatal("不同令牌应拒绝")
	}
	if verifyConfirmToken("", "abc123") || verifyConfirmToken("abc123", "") {
		t.Fatal("空令牌应拒绝")
	}
	if verifyConfirmToken("ab", "abc") {
		t.Fatal("长度不一致应拒绝")
	}
}
