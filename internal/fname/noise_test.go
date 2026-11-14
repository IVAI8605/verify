// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package fname

import "testing"

// TestCoreKey 语义核心键：噪声剥离后同内容归同键。
func TestCoreKey(t *testing.T) {
	noise := NoiseSet(DefaultNoiseWords)
	cases := []struct {
		a, b string
		same bool
	}{
		{"沙丘.2016.1080p.HEVC.x264-GHD", "沙丘 2016", true},              // 规则噪声全剥、年份保留
		{"shaqiu.2016.1080p", "沙丘 2016", true},                        // 拼音形态
		{"阿凡达.2009.1080p.BluRay.x265.10bit.HDR 简中", "阿凡达 2009", true}, // CJK 噪声词整词命中
		{"movie.s01e01", "movie", true},                               // 剧集编号规则
		{"abc.def12345678", "abc", true},                              // 8 位十六进制剥离
		{"沙丘2", "沙丘", false},                                          // 序号不同不并
		{"2016", "", true},                                            // 年份是噪声例外？否——4 位年份保留，但这里只有它自己
	}
	for i, c := range cases {
		ka := CoreKey(c.a, noise)
		kb := CoreKey(c.b, noise)
		if i == 6 {
			// 4 位年份保留：CoreKey("2016") 应非空
			if ka == "" {
				t.Fatalf("4 位年份不应被剥离")
			}
			continue
		}
		if (ka == kb) != c.same {
			t.Fatalf("case %d: CoreKey(%q)=%q CoreKey(%q)=%q 期望 same=%v", i, c.a, ka, c.b, kb, c.same)
		}
	}
}

// TestSanitizeNoiseWords。
func TestSanitizeNoiseWords(t *testing.T) {
	got := SanitizeNoiseWords([]string{" 1080P ", "", "x", "1080p", "ok"})
	if len(got) != 3 || got[0] != "1080p" || got[1] != "x" || got[2] != "ok" {
		t.Fatalf("SanitizeNoiseWords = %v", got)
	}
}

// TestIsNoiseToken 规则类噪声。
func TestIsNoiseToken(t *testing.T) {
	if !isNoiseToken("s01e01") || !isNoiseToken("deadbeef1234") || !isNoiseToken("001") {
		t.Fatal("编号/哈希/序号应命中")
	}
	if isNoiseToken("2016") || isNoiseToken("沙丘") || isNoiseToken("abc") {
		t.Fatal("年份/汉字/字母词不应命中")
	}
}
