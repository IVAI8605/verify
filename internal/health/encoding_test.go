// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// gbkName 返回 "测试.mp4" 的 GBK 字节串，用来构造真实的非 UTF-8 文件名。
func gbkName() string {
	return string([]byte{0xB2, 0xE2, 0xCA, 0xD4, '.', 'm', 'p', '4'})
}

// TestRepairEncodingGB18030 覆盖 v3.12.0 的主路径：GBK/GB18030 中文名转成 UTF-8。
func TestRepairEncodingGB18030(t *testing.T) {
	raw := gbkName()
	if utf8.ValidString(raw) {
		t.Fatal("用例构造有误：GBK 字节不应是合法 UTF-8")
	}
	got, changed := RepairEncoding(raw)
	if !changed {
		t.Fatal("非 UTF-8 名字必须报告 changed=true")
	}
	if got != "测试.mp4" {
		t.Fatalf("期望 测试.mp4，实际 %q", got)
	}
	// 已经是合法 UTF-8 的名字不能被当成待修复
	if s, ch := RepairEncoding("正常.mp4"); ch || s != "正常.mp4" {
		t.Fatalf("合法名字不应改动，实际 %q changed=%v", s, ch)
	}
}

// TestRepairEncodingFallback 覆盖解不出 GB18030 时的兜底：非法字节转 %XX，且不丢信息。
func TestRepairEncodingFallback(t *testing.T) {
	raw := string([]byte{'a', 0x80, 'b', 0xFF, 'c'})
	got, changed := RepairEncoding(raw)
	if !changed {
		t.Fatal("非法字节应触发修复")
	}
	if got != "a%80b%FFc" {
		t.Fatalf("期望 a%%80b%%FFc，实际 %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("修复结果必须是合法 UTF-8：%q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("修复结果不应含 U+FFFD：%q", got)
	}
}

// TestCheckNameInvalidEncoding 验证非 UTF-8 名字归入「文件名不规范」，且建议名与改名目标一致。
func TestCheckNameInvalidEncoding(t *testing.T) {
	raw := gbkName()
	f, bad := CheckName(raw, NameOptions{})
	if !bad {
		t.Fatal("非 UTF-8 文件名应被检测出来")
	}
	if len(f.Reasons) != 1 || f.Reasons[0] != ReasonEncoding {
		t.Fatalf("原因应只有 invalid_encoding，实际 %v", f.Reasons)
	}
	if f.Suggested != "测试.mp4" {
		t.Fatalf("建议名期望 测试.mp4，实际 %q", f.Suggested)
	}
	target, ok := BuildTarget(KindName, raw, "", NameOptions{})
	if !ok || target != "测试.mp4" {
		t.Fatalf("改名目标期望 测试.mp4，实际 %q ok=%v", target, ok)
	}
}
