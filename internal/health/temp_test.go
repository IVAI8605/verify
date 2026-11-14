// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"strings"
	"testing"
)

// TestCheckTemp 临时文件清单匹配规则。
func TestCheckTemp(t *testing.T) {
	pats := DefaultTempExts
	cases := []struct {
		name string
		want bool
	}{
		{"video.tmp", true},
		{"x.tar.tmp", true},
		{"backup.bak", true},
		{"movie.crdownload", true},
		{"file.part", true},
		{"notes.txt~", true},
		{"Thumbs.db", true},
		{"thumbs.db", true},
		// v1.42.0：默认清单补 .DS_Store（BUG-06），大小写不敏感。
		{".DS_Store", true},
		{".ds_store", true},
		{"desktop.ini", true},
		{"photo.jpg", false},
		{"report.docx", false},
		{"tmp", false}, // 无点不后缀匹配
		{"", false},
		{"thumbs.db.bak", true},
	}
	for _, c := range cases {
		if got := CheckTemp(c.name, pats); got != c.want {
			t.Fatalf("CheckTemp(%q) = %v，期望 %v", c.name, got, c.want)
		}
	}
	// 空清单 = 关闭检测
	if CheckTemp("x.tmp", nil) {
		t.Fatal("空清单不应命中")
	}
	// v1.41.10：去掉「主名必须比清单项长」的限制，整个文件名就是清单项时也算命中。
	// 否则 .DS_Store 这类隐藏文件永远抓不到——它的完整名字就是清单项。
	if !CheckTemp(".tmp", pats) {
		t.Fatal("整个文件名等于清单项时应命中")
	}
}

// TestSanitizeTempPatterns 清单清洗。
func TestSanitizeTempPatterns(t *testing.T) {
	got := SanitizeTempPatterns([]string{" .tmp ", "", ".bak", ".tmp", string(make([]byte, 200))})
	if len(got) != 2 || got[0] != ".tmp" || got[1] != ".bak" {
		t.Fatalf("SanitizeTempPatterns = %v", got)
	}
}

// TestMatchTemp 校验能拿到命中的具体清单项——原因文案要靠它说明「是谁生成的」。
func TestMatchTemp(t *testing.T) {
	patterns := []string{".tmp", ".crdownload", "~", "Thumbs.db", ".DS_Store"}
	cases := []struct {
		base string
		want string
	}{
		{"下载到一半.tmp", ".tmp"},
		{"x.tar.tmp", ".tmp"},
		{"影片.mp4.crdownload", ".crdownload"},
		{"笔记.md~", "~"},
		{"Thumbs.db", "Thumbs.db"},
		{"thumbs.db", "Thumbs.db"},
		{".DS_Store", ".DS_Store"},
		{"正常文件.mp4", ""},
		{"tmp.txt", ""},
	}
	for _, c := range cases {
		if got := MatchTemp(c.base, patterns); got != c.want {
			t.Errorf("MatchTemp(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

// TestTempReason 校验常见清单项给出具体原因，自定义项退回通用文案。
func TestTempReason(t *testing.T) {
	cases := []struct {
		base, pattern, wantPart string
	}{
		{"a.crdownload", ".crdownload", "浏览器下载未完成"},
		{"Thumbs.db", "Thumbs.db", "缩略图缓存"},
		{"a.md~", "~", "编辑器备份"},
		{"weird.xyz", ".xyz", "命中临时文件清单项 .xyz"},
	}
	for _, c := range cases {
		got := TempReason(c.base, c.pattern)
		if !strings.Contains(got, c.wantPart) {
			t.Errorf("TempReason(%q) = %q, 期望包含 %q", c.pattern, got, c.wantPart)
		}
	}
}
