// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package webauth 提供访问令牌的比较与 Cookie 值派生。
//
// 单独成包是为了能在 Windows 上直接跑测试：internal/web 依赖 syscall.Statfs_t，
// 在非 linux 目标下编译不过，安全相关逻辑放在那里会失去本地测试能力。
package webauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

// cookieScope 参与派生，换算法或换用途时改它即可让旧 Cookie 自然失效。
const cookieScope = "verify-cookie-v1|"

// TokenEqual 以常量时间比较访问令牌。
//
// 先把两侧各自摘要成固定长度再比较：既避免逐字节比较泄露时序信息，
// 也不会因为长度不同而提前返回、暴露令牌长度。
func TokenEqual(a, b string) bool {
	x := sha256.Sum256([]byte(a))
	y := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

// GenerateToken 生成新的访问令牌（v3.7.2）：24 字节密码学随机数转 base64url，
// 共 32 个字符，足够抵抗离线枚举，也不会因过长而难以手输。
func GenerateToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CookieValue 由访问令牌派生 Cookie 值，避免把明文令牌写进浏览器。
//
// 派生是确定性的：进程重启后旧 Cookie 依然有效，不需要额外的会话存储或密钥；
// 令牌变更时派生值随之变化，旧 Cookie 自然失效。拿到 Cookie 也无法反推令牌本身，
// 因此不会把用户在多处复用的那串令牌泄露给浏览器存储。
func CookieValue(token string) string {
	sum := sha256.Sum256([]byte(cookieScope + token))
	return hex.EncodeToString(sum[:])
}
