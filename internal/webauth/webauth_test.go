// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package webauth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestTokenEqual 覆盖相等、不等、长度差异与空值四种情况。
func TestTokenEqual(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"相同令牌", "s3cret-token", "s3cret-token", true},
		{"大小写不同", "s3cret-token", "S3cret-token", false},
		{"前缀相同但更长", "s3cret-token", "s3cret-token-x", false},
		{"完全不同", "abc", "xyz", false},
		{"两侧都为空", "", "", true},
		{"一侧为空", "", "abc", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TokenEqual(c.a, c.b); got != c.want {
				t.Fatalf("TokenEqual(%q, %q) = %v，期望 %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestTokenEqualIsSymmetric 保证参数顺序不影响结果，比较逻辑不能有方向性偏差。
func TestTokenEqualIsSymmetric(t *testing.T) {
	if TokenEqual("a", "b") != TokenEqual("b", "a") {
		t.Fatal("比较结果与参数顺序相关")
	}
}

// TestCookieValueNotPlaintext 是这次修复的核心断言：Cookie 值不得等于令牌本身。
func TestCookieValueNotPlaintext(t *testing.T) {
	const token = "s3cret-token"
	v := CookieValue(token)
	if v == token {
		t.Fatal("Cookie 值等于明文令牌")
	}
	if strings.Contains(v, token) {
		t.Fatal("Cookie 值包含明文令牌片段")
	}
	if len(v) != 64 {
		t.Fatalf("期望 64 个十六进制字符，实际 %d", len(v))
	}
}

// TestCookieValueDeterministic 保证重启后旧 Cookie 仍然有效，不需要会话存储。
func TestCookieValueDeterministic(t *testing.T) {
	if CookieValue("t") != CookieValue("t") {
		t.Fatal("同一令牌两次派生的结果不一致")
	}
}

// TestCookieValueDiffersByToken 保证换令牌后旧 Cookie 失效。
func TestCookieValueDiffersByToken(t *testing.T) {
	if CookieValue("t1") == CookieValue("t2") {
		t.Fatal("不同令牌派生出相同的 Cookie 值")
	}
}

// TestGenerateToken 覆盖长度、字符集与随机性：自动生成的令牌必须是
// URL 安全字符且每次不同，否则会出现「重启后令牌重复」这类灾难性后果。
func TestGenerateToken(t *testing.T) {
	a, err := GenerateToken()
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if len(a) != 32 {
		t.Fatalf("期望 32 个字符，实际 %d（%q）", len(a), a)
	}
	if strings.ContainsAny(a, "+/= \t\n") {
		t.Fatalf("令牌含非 URL 安全字符：%q", a)
	}
	b, err := GenerateToken()
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if a == b {
		t.Fatal("两次生成的令牌相同")
	}
	if TokenEqual(a, a) != true || TokenEqual(a, b) != false {
		t.Fatal("生成的令牌无法与 TokenEqual 正确配合")
	}
}

// TestCookieValueIsNotBareHash 确认派生前缀生效：与直接 sha256(token) 不同，
// 这样同一令牌在别处被直接哈希使用时不会与本站 Cookie 撞值。
func TestCookieValueIsNotBareHash(t *testing.T) {
	sum := sha256.Sum256([]byte("t"))
	bare := hex.EncodeToString(sum[:])
	if CookieValue("t") == bare {
		t.Fatal("派生值等于直接 sha256(token)，前缀没有生效")
	}
	if CookieValue("") == CookieValue("t") {
		t.Fatal("空令牌与普通令牌的派生值不应相同")
	}
}
