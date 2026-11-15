// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package fname

import "testing"

// TestPinyinKey 音序键核心场景：拼音替换的单字/整词与汉字归到同键。
func TestPinyinKey(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{"沙丘", "shaqiu", true},   // 整词拼音替换
		{"沙丘2", "shaqiu2", true}, // 带序号
		{"第01集", "di01集", true},  // 单字替换（集→jie 缺失形态）
		{"Movie 沙丘", "movie shaqiu", true},
		{"沙丘", "沙丘2", false},
		{"ABC", "abc", true},         // 大小写归一
		{"a_b-c.d", "a b c d", true}, // 分隔符折叠
		{"", "", true},
	}
	for _, c := range cases {
		ka, kb := PinyinKey(c.a), PinyinKey(c.b)
		if (ka == kb) != c.same {
			t.Fatalf("PinyinKey(%q)=%q PinyinKey(%q)=%q，期望 same=%v", c.a, ka, c.b, kb, c.same)
		}
	}
}

// TestNearGroups 编辑距离 ≤1 分组：同键、单字符差异成组；距离 2 不组。
func TestNearGroups(t *testing.T) {
	keys := []string{
		"shaqiu",   // 0
		"shaqi",    // 1  删尾 → 距离1
		"shaqiu2",  // 2  插入 → 与 0 距离1
		"shbqiu",   // 3  替换 → 与 0 距离1
		"shaqiu33", // 4  距离 2，不应与 0 同组
		"zzz",      // 5 独立
		"zzz",      // 6 与 5 同键成组
		"",         // 7 空键跳过
	}
	groups := NearGroups(keys)
	// 组以根下标为键；0/1/2/3 应同组，4 单独，5/6 同组
	rootOf := func(i int) int { return groupsRoot(groups, i) }
	if rootOf(0) != rootOf(1) || rootOf(0) != rootOf(2) || rootOf(0) != rootOf(3) {
		t.Fatalf("距离 1 的键应同组：%v", groups)
	}
	if rootOf(0) == rootOf(4) {
		t.Fatalf("距离 2 不应成组：%v", groups)
	}
	if rootOf(5) != rootOf(6) {
		t.Fatalf("同键应成组：%v", groups)
	}
	if rootOf(0) == rootOf(5) {
		t.Fatalf("无关键不应成组：%v", groups)
	}
}

// TestNearGroupsRunes 验证多字节字符按 rune 删除（v3.10.11 改哈希签名后仍成立）：
// 「丙丁」与「丁丙」共享删字变体「丁」，但真实距离是 2，不能被误并。
func TestNearGroupsRunes(t *testing.T) {
	keys := []string{
		"甲乙", // 0
		"甲",  // 1 删尾 → 距离1
		"丙丁", // 2
		"丁丙", // 3 与 2 共享变体「丁」，真实距离 2
	}
	groups := NearGroups(keys)
	// 只有 0/1 成组；2/3 虽是候选（共享变体「丁」）但真实距离 2，必须留在组外。
	if len(groups) != 1 || len(groups[0]) != 2 || groups[0][0] != 0 || groups[0][1] != 1 {
		t.Fatalf("期望只有 [0 1] 成组，实际 %v", groups)
	}
}

// groupsRoot 找包含 i 的组的首元素（测试辅助）。
func groupsRoot(groups [][]int, i int) int {
	for _, g := range groups {
		for _, m := range g {
			if m == i {
				return g[0]
			}
		}
	}
	return -1
}

// TestEditDistanceLE1 边界。
func TestEditDistanceLE1(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"", "", true},
		{"a", "", true},
		{"ab", "ba", false}, // 2 次替换
		{"中", "中", true},
		{"abc", "axc", true},
		{"abc", "axcd", false},
	}
	for _, c := range cases {
		if got := editDistanceLE1(c.a, c.b); got != c.want {
			t.Fatalf("editDistanceLE1(%q,%q)=%v，期望 %v", c.a, c.b, got, c.want)
		}
	}
}

// TestStemAndHasLetters。
func TestStemAndHasLetters(t *testing.T) {
	if Stem("第01集.mp4") != "第01集" {
		t.Fatal("Stem 主名")
	}
	if Stem("noext") != "noext" {
		t.Fatal("Stem 无扩展名")
	}
	if HasLetters("001 - .") {
		t.Fatal("纯数字符号应判 false")
	}
	if !HasLetters("沙丘2") || !HasLetters("abc123") {
		t.Fatal("含字母/汉字应判 true")
	}
}

// TestExt 扩展名归一：大小写、前导点、多点、无扩展名与隐藏文件。
func TestExt(t *testing.T) {
	cases := []struct{ in, want string }{
		{"movie.MP4", "mp4"},
		{"a.tar.GZ", "gz"},
		{"noext", ""},
		{".hidden", "hidden"},
		{"trailing.", ""},
		{"片名.MKV", "mkv"},
	}
	for _, c := range cases {
		if got := Ext(c.in); got != c.want {
			t.Fatalf("Ext(%q)=%q，期望 %q", c.in, got, c.want)
		}
	}
}
