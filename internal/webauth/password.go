// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package webauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// v4.5.4：访问密码使用 PBKDF2-HMAC-SHA256 加盐存储，配置文件里不再出现可
// 直接用于登录的明文凭据。迭代次数按 OWASP 2023 的 PBKDF2-SHA256 建议值。
const (
	passwordIterations = 210000
	passwordSaltBytes  = 16
	passwordKeyBytes   = 32
	passwordScheme     = "pbkdf2-sha256"
	maxPasswordIters   = 10_000_000
)

// PasswordProblem 返回密码不合规的原因；空串表示通过。
// 只限制长度与全空白，不强制字符组合，避免把中文密码或长口令误判为弱密码。
func PasswordProblem(pw string) string {
	n := utf8.RuneCountInString(pw)
	if n < 8 {
		return "访问密码至少 8 个字符"
	}
	if n > 128 {
		return "访问密码最多 128 个字符"
	}
	if strings.TrimSpace(pw) == "" {
		return "访问密码不能全是空白"
	}
	return ""
}

// HashPassword 生成 `pbkdf2-sha256$迭代次数$盐$摘要` 形式的字符串。
func HashPassword(pw string) (string, error) {
	if msg := PasswordProblem(pw); msg != "" {
		return "", errors.New(msg)
	}
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2SHA256([]byte(pw), salt, passwordIterations, passwordKeyBytes)
	return fmt.Sprintf("%s$%d$%s$%s", passwordScheme, passwordIterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword 校验明文密码与已存哈希是否匹配，比较使用常量时间。
func VerifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordScheme {
		return false
	}
	iters, err := strconv.Atoi(parts[1])
	if err != nil || iters <= 0 || iters > maxPasswordIters {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) < 8 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) < 16 {
		return false
	}
	got := pbkdf2SHA256([]byte(pw), salt, iters, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// PasswordCookieValue 由密码哈希派生登录 Cookie 值；改密码后旧 Cookie 自然失效。
func PasswordCookieValue(passwordHash string) string {
	sum := sha256.Sum256([]byte("verify-cookie-password-v1|" + passwordHash))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// pbkdf2SHA256 按 RFC 8018 实现 PBKDF2，避免为此引入新的第三方依赖。
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	hLen := sha256.Size
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)
	var buf [4]byte
	for block := 1; block <= blocks; block++ {
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write(buf[:])
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iter; i++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}
