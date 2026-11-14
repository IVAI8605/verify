// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件是 v3.17.5 的入口参数回归：硬链接批量上限（F-14）与令牌强度（F-18）。

import "testing"

// TestRelinkLimit 回归 F-14：单批目标最多 500 个。
func TestRelinkLimit(t *testing.T) {
	r := &relinkReq{Keep: "/vol1/a.bin", Paths: make([]string, 501)}
	if r.validate() == "" {
		t.Fatal("超过 500 个目标应被拒")
	}
	r.Paths = make([]string, 500)
	if msg := r.validate(); msg != "" {
		t.Fatalf("500 个目标应通过，实际 %q", msg)
	}
}

// TestValidateTokenStrength 回归 F-18：非空令牌至少 16 位，留空合法。
func TestValidateTokenStrength(t *testing.T) {
	if msg := validateTokenStrength(""); msg != "" {
		t.Fatalf("留空应合法，实际 %q", msg)
	}
	if msg := validateTokenStrength("123456789012345"); msg == "" {
		t.Fatal("15 位令牌应被拒")
	}
	if msg := validateTokenStrength("1234567890123456"); msg != "" {
		t.Fatalf("16 位令牌应合法，实际 %q", msg)
	}
}
