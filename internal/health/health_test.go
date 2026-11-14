// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckNameDefaultRules 覆盖默认开启的三条规则：emoji、首尾空格、重复字符。
func TestCheckNameDefaultRules(t *testing.T) {
	cases := []struct {
		name     string
		wantBad  bool
		wantWhy  string
		suggest  string
		original string
	}{
		{name: "😀report.pdf", wantBad: true, wantWhy: ReasonEmoji, suggest: "report.pdf"},
		{name: " note.txt", wantBad: true, wantWhy: ReasonSpace, suggest: "note.txt"},
		{name: "note .txt", wantBad: true, wantWhy: ReasonSpace, suggest: "note.txt"},
		{name: "a___b.txt", wantBad: true, wantWhy: ReasonRepeat, suggest: "a_b.txt"},
		{name: "a..b.txt", wantBad: false},
		{name: "a  b.txt", wantBad: true, wantWhy: ReasonSpaces, suggest: "a b.txt"},
		{name: "a b.txt", wantBad: false},
		{name: "x    y  z.txt", wantBad: true, wantWhy: ReasonSpaces, suggest: "x y z.txt"},
		{name: "normal_file.txt", wantBad: false},
		{name: "中文文件名.txt", wantBad: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, bad := CheckName(c.name, NameOptions{})
			if bad != c.wantBad {
				t.Fatalf("检出=%v，期望 %v（原因 %v）", bad, c.wantBad, f.Reasons)
			}
			if !bad {
				return
			}
			if !hasNameReason(f, c.wantWhy) {
				t.Fatalf("应包含原因 %q，实际 %v", c.wantWhy, f.Reasons)
			}
			if f.Suggested != c.suggest {
				t.Fatalf("建议名 %q，期望 %q", f.Suggested, c.suggest)
			}
		})
	}
}

// TestNormalizeKeepsChinese 验证中文正文不会被改动，避免把合法文件名改坏。
func TestNormalizeKeepsChinese(t *testing.T) {
	got := NormalizeName("  我的 报告（终稿）  .PDF", NameOptions{})
	if got != "我的 报告（终稿）.pdf" {
		t.Fatalf("规范化结果 %q", got)
	}
}

// TestNormalizeLowercasesExt 验证扩展名统一小写（大写修正落在这里）。
func TestNormalizeLowercasesExt(t *testing.T) {
	got := NormalizeName("MOVIE ___ 01.MP4", NameOptions{})
	if got != "MOVIE _ 01.mp4" {
		t.Fatalf("规范化结果 %q", got)
	}
}

// TestRestrictCharset 验证白名单规则只在显式开启时生效。
func TestRestrictCharset(t *testing.T) {
	const name = "a b&c.txt"
	if _, bad := CheckName(name, NameOptions{}); bad {
		t.Fatal("默认不应启用字符集规则")
	}
	f, bad := CheckName(name, NameOptions{RestrictCharset: true})
	if !bad {
		t.Fatal("开启后应检出受限字符")
	}
	if !hasNameReason(f, ReasonCharset) {
		t.Fatalf("应包含 charset 原因，实际 %v", f.Reasons)
	}
	if f.Suggested != "a_b_c.txt" {
		t.Fatalf("建议名 %q", f.Suggested)
	}
}

// TestNonCJKRule 验证"非 ASCII 且非中日韩"规则的开关行为。
func TestNonCJKRule(t *testing.T) {
	const name = "файл.txt"
	if _, bad := CheckName(name, NameOptions{}); bad {
		t.Fatal("默认不应启用非中日韩规则")
	}
	f, bad := CheckName(name, NameOptions{NonCJKNonASCII: true})
	if !bad || !hasNameReason(f, ReasonNonCJK) {
		t.Fatalf("开启后应检出，实际 %v", f.Reasons)
	}
}

// TestNormalizeEmptyBase 验证极端输入不会产生空文件名。
func TestNormalizeEmptyBase(t *testing.T) {
	if got := NormalizeName("😀.txt", NameOptions{}); got != "unnamed.txt" {
		t.Fatalf("应回退为 unnamed.txt，实际 %q", got)
	}
}

// TestCheckEmptySizeZero 验证零字节判定不看内容。
func TestCheckEmptySizeZero(t *testing.T) {
	f, ok := CheckEmpty("whatever", 0, ContentNone)
	if !ok || f.Reason != ReasonZeroSize {
		t.Fatalf("零字节应判定为空文件，实际 %v %v", ok, f.Reason)
	}
}

func TestCheckEmptyNonZeroSkipped(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := CheckEmpty(p, 4, ContentNone); ok {
		t.Fatal("基础模式不应把非空文件计入")
	}
}

// TestCheckEmptyAllZero 验证全零内容能被识别，并在遇到非零字节时短路。
func TestCheckEmptyAllZero(t *testing.T) {
	dir := t.TempDir()
	allZero := filepath.Join(dir, "zero.bin")
	if err := os.WriteFile(allZero, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	f, ok := CheckEmpty(allZero, 4096, ContentZeroed)
	if !ok || f.Reason != ReasonAllZero {
		t.Fatalf("全零文件应命中，实际 %v %v", ok, f.Reason)
	}

	mixed := filepath.Join(dir, "mixed.bin")
	buf := make([]byte, 4096)
	buf[2048] = 0x41
	if err := os.WriteFile(mixed, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := CheckEmpty(mixed, 4096, ContentZeroed); ok {
		t.Fatal("含非零字节的文件不应命中")
	}
}

// TestCheckEmptyUnprintable 验证不可打印字符判定，换行与制表符视为可打印。
func TestCheckEmptyUnprintable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ctrl.bin")
	if err := os.WriteFile(p, []byte{0x00, 0x01, 0x02, 0x1F}, 0o600); err != nil {
		t.Fatal(err)
	}
	f, ok := CheckEmpty(p, 4, ContentUnprintable)
	if !ok || f.Reason != ReasonUnprintable {
		t.Fatalf("不可打印内容应命中，实际 %v %v", ok, f.Reason)
	}

	text := filepath.Join(dir, "text.txt")
	if err := os.WriteFile(text, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := CheckEmpty(text, 6, ContentUnprintable); ok {
		t.Fatal("普通文本不应命中")
	}
}

// TestHasVideoExt 验证视频扩展名白名单与大小写处理。
func TestHasVideoExt(t *testing.T) {
	for _, name := range []string{"a.mp4", "a.MKV", "a.m2ts", "a.webm"} {
		if !HasVideoExt(name) {
			t.Fatalf("%s 应被识别为视频", name)
		}
	}
	for _, name := range []string{"a.jpg", "a.txt", "a"} {
		if HasVideoExt(name) {
			t.Fatalf("%s 不应被识别为视频", name)
		}
	}
}

// TestKindValid 验证检测类型枚举。
func TestKindValid(t *testing.T) {
	for _, k := range AllKinds() {
		if !k.Valid() {
			t.Fatalf("%s 应为合法类型", k)
		}
		if strings.TrimSpace(k.Label()) == "" {
			t.Fatalf("%s 缺少中文标签", k)
		}
	}
	if Kind("nope").Valid() {
		t.Fatal("未知类型不应合法")
	}
}

func hasNameReason(f NameFinding, want string) bool {
	for _, r := range f.Reasons {
		if r == want {
			return true
		}
	}
	return false
}
