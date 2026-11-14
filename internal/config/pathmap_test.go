// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package config

import (
	"strings"
	"testing"
)

func testMaps(t *testing.T, text string) PathMaps {
	t.Helper()
	ms, err := ParsePathMaps(text)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// TestParsePathMaps 解析、清洗与排序。
func TestParsePathMaps(t *testing.T) {
	ms := testMaps(t, "/vol1 = \\\\NAS\\vol1\n/vol1/media = \\\\NAS\\media\n\n  \n/vol1/1000/电影 = Z:/电影")
	if len(ms) != 3 {
		t.Fatalf("应解析出 3 条，实际 %d: %+v", len(ms), ms)
	}
	// 最长前缀在前
	if ms[0].Nas != "/vol1/1000/电影" || ms[1].Nas != "/vol1/media" || ms[2].Nas != "/vol1" {
		t.Fatalf("应按 NAS 前缀长度降序: %+v", ms)
	}
	// 尾分隔符清洗
	if ms[2].Ext != `\\NAS\vol1` {
		t.Fatalf("外部路径尾分隔符应去除: %q", ms[2].Ext)
	}
	// NAS 侧反斜杠归一为 /
	if !strings.HasPrefix(ms[2].Nas, "/vol1") {
		t.Fatalf("NAS 侧应为 / 分隔: %q", ms[2].Nas)
	}
}

// TestParsePathMapsErrors 非法输入逐行报错。
func TestParsePathMapsErrors(t *testing.T) {
	cases := map[string]string{
		"没有等号":                       "格式",
		"=只有外部路径":                    "格式",
		"只有左边=":                      "格式",
		"vol1/no-slash = \\\\NAS\\x": "以 / 开头",
		"/vol1 = ":                   "格式",
	}
	for name, want := range cases {
		if _, err := ParsePathMaps(name); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q 应报错含 %q，实际: %v", name, want, err)
		}
	}
	// 空文本合法（清空映射）
	ms, err := ParsePathMaps("\n  \n")
	if err != nil || len(ms) != 0 {
		t.Fatalf("空文本应解析为空表: %v %v", ms, err)
	}
}

// TestToExt NAS→外部：目录边界、最长前缀、尾斜杠保留、分隔符转换。
func TestToExt(t *testing.T) {
	ms := testMaps(t, "/vol1 = \\\\NAS\\vol1\n/vol1/media = \\\\NAS\\media")
	cases := map[string]string{
		"/vol1/media/abc/":   `\\NAS\media\abc\`,        // 命中 + 尾斜杠保留
		"/vol1/media":        `\\NAS\media`,             // 恰好等于前缀
		"/vol1/other/x.mp4":  `\\NAS\vol1\other\x.mp4`,  // 落到宽泛映射
		"/vol2/media/x.jpg":  "",                        // 未命中 → 空
		"/vol1/media2/x.jpg": `\\NAS\vol1\media2\x.jpg`, // 目录边界：media ≠ media2
	}
	for in, want := range cases {
		if got := ms.ToExt(in); got != want {
			t.Fatalf("ToExt(%q) = %q，期望 %q", in, got, want)
		}
	}
	// 盘符形态
	ms2 := testMaps(t, "/vol1/1000/电影 = Z:/电影")
	if got := ms2.ToExt("/vol1/1000/电影/abc/"); got != `Z:\电影\abc\` {
		t.Fatalf("盘符映射: %q", got)
	}
}

// TestNormalizePathMapsCollapse 外部路径内部的重复反斜杠折叠为单个（UNC 头保留）。
func TestNormalizePathMapsCollapse(t *testing.T) {
	ms := testMaps(t, "/tmp/x = \\\\FNOS\\\\share\n/tmp/y = Z:\\\\movie")
	if ms[0].Ext != `\\FNOS\share` {
		t.Fatalf("UNC 中间重复反斜杠应折叠: %q", ms[0].Ext)
	}
	if ms[1].Ext != `Z:\movie` {
		t.Fatalf("盘符路径重复反斜杠应折叠: %q", ms[1].Ext)
	}
	// 折叠后 ToExt 输出正确
	if got := ms.ToExt("/tmp/x/a/"); got != `\\FNOS\share\a\` {
		t.Fatalf("ToExt: %q", got)
	}
}

// TestToNas 外部→NAS：大小写不敏感、混合分隔符、边界、未命中。
func TestToNas(t *testing.T) {
	ms := testMaps(t, "/vol1/media = \\\\FNOS\\media\n/vol1 = \\\\NAS\\vol1")
	cases := map[string]string{
		`\\FNOS\media\abc\x.mp4`: "/vol1/media/abc/x.mp4",
		`\\fnos\media\abc\x.mp4`: "/vol1/media/abc/x.mp4", // 大小写不敏感
		`//FNOS/media/abc/x.mp4`: "/vol1/media/abc/x.mp4", // 正斜杠也认
		`\\FNOS\media`:           "/vol1/media",           // 恰好相等
		`\\NAS\vol1\other\a.jpg`: "/vol1/other/a.jpg",
		`\\FNOS\media2\x.jpg`:    "", // 目录边界
		`Z:\电影\x.mp4`:            "", // 未配置的映射
	}
	for in, want := range cases {
		if got := ms.ToNas(in); got != want {
			t.Fatalf("ToNas(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestToNasCasePreserve 反向映射保留原大小写（仅匹配不敏感）。
func TestToNasCasePreserve(t *testing.T) {
	ms := testMaps(t, "/vol1/media = \\\\FNOS\\Media")
	got := ms.ToNas(`\\FNOS\MEDIA\Abc\X.MP4`)
	if got != "/vol1/media/Abc/X.MP4" {
		t.Fatalf("剩余部分大小写应保留: %q", got)
	}
}
