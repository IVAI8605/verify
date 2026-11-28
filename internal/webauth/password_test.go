package webauth

import (
	"strings"
	"testing"
)

func TestPasswordHashAndVerify(t *testing.T) {
	h, err := HashPassword("正确的密码 abc123")
	if err != nil {
		t.Fatalf("生成哈希失败：%v", err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$210000$") {
		t.Fatalf("哈希格式不对：%s", h)
	}
	if !VerifyPassword(h, "正确的密码 abc123") {
		t.Fatal("正确密码应通过")
	}
	if VerifyPassword(h, "错误的密码 abc123") {
		t.Fatal("错误密码不应通过")
	}
	h2, err := HashPassword("正确的密码 abc123")
	if err != nil {
		t.Fatal(err)
	}
	if h == h2 {
		t.Fatal("相同密码两次生成的盐不同，哈希不应相同")
	}
	if VerifyPassword(h2, "正确的密码 abc123") == false {
		t.Fatal("第二个哈希应通过")
	}
}

func TestVerifyPasswordRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "plain", "pbkdf2-sha256$0$AA$AA", "pbkdf2-sha256$99999999$AA$AA", "pbkdf2-sha256$x$AA$AA", "bcrypt$1$AA$AA"} {
		if VerifyPassword(s, "whatever") {
			t.Fatalf("非法哈希不应通过：%q", s)
		}
	}
}

func TestPasswordProblem(t *testing.T) {
	cases := []struct {
		pw string
		ok bool
	}{
		{"短", false},
		{"1234567", false},
		{"12345678", true},
		{"        ", false},
		{"中文访问密码足够长", true},
		{strings.Repeat("a", 128), true},
		{strings.Repeat("a", 129), false},
	}
	for _, c := range cases {
		got := PasswordProblem(c.pw) == ""
		if got != c.ok {
			t.Fatalf("PasswordProblem(%q)=%v，期望 %v", c.pw, got, c.ok)
		}
	}
}

func TestPasswordCookieValue(t *testing.T) {
	a := PasswordCookieValue("hash-a")
	b := PasswordCookieValue("hash-b")
	if a == b || len(a) == 0 {
		t.Fatal("不同哈希应派生不同 Cookie")
	}
	if a != PasswordCookieValue("hash-a") {
		t.Fatal("派生应稳定")
	}
}
