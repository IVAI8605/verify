// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import "testing"

func TestRenameMethodRemove(t *testing.T) {
	if got := applyOneMethod("abcdef", 0, RenameMethod{Type: "remove", Count: 2}, nil); got != "cdef" {
		t.Fatalf("从头删 2 = %q", got)
	}
	if got := applyOneMethod("abcdef", 0, RenameMethod{Type: "remove", Count: 2, FromEnd: true}, nil); got != "abcd" {
		t.Fatalf("从尾删 2 = %q", got)
	}
}

func TestRenameMethodRemovePatternAndReplace(t *testing.T) {
	if got := applyOneMethod("a_1080p_b", 0, RenameMethod{Type: "remove_pattern", Pattern: "_1080p"}, nil); got != "a_b" {
		t.Fatalf("按文本删 = %q", got)
	}
	if got := applyOneMethod("a_1080p_b_1080p", 0, RenameMethod{Type: "remove_pattern", Pattern: `_1080p`, Regex: true}, nil); got != "a_b" {
		t.Fatalf("按正则删 = %q", got)
	}
	if got := applyOneMethod("a-a-a", 0, RenameMethod{Type: "replace", Pattern: "a", Replacement: "X", Occurrence: 2}, nil); got != "a-X-a" {
		t.Fatalf("只替换第 2 次 = %q", got)
	}
	if got := applyOneMethod("abc", 0, RenameMethod{Type: "list_replace", Pairs: []RenamePair{{From: "a", To: "1"}, {From: "b", To: "2"}}}, nil); got != "12c" {
		t.Fatalf("多对替换 = %q", got)
	}
}

func TestRenameMethodAddAndRenumber(t *testing.T) {
	if got := applyOneMethod("abc", 0, RenameMethod{Type: "add", Text: "X_", Position: "head"}, nil); got != "X_abc" {
		t.Fatalf("头插 = %q", got)
	}
	if got := applyOneMethod("abc", 1, RenameMethod{Type: "add", Text: "<Inc Nr:3>"}, nil); got != "abc002" {
		t.Fatalf("尾插 Inc = %q", got)
	}
	if got := applyOneMethod("abc", 2, RenameMethod{Type: "renumber", Start: 5, Step: 2, Digits: 3, Text: "_"}, nil); got != "abc_009" {
		t.Fatalf("重编号 = %q", got)
	}
}

func TestRenameMethodCaseTrimSwapList(t *testing.T) {
	if got := applyOneMethod("hello world", 0, RenameMethod{Type: "new_case", Mode: "title"}, nil); got != "Hello World" {
		t.Fatalf("title = %q", got)
	}
	if got := applyOneMethod("  abc  ", 0, RenameMethod{Type: "trim"}, nil); got != "abc" {
		t.Fatalf("trim = %q", got)
	}
	if got := applyOneMethod("a_b_c", 0, RenameMethod{Type: "swap", Separator: "_", IndexA: 0, IndexB: 2}, nil); got != "c_b_a" {
		t.Fatalf("swap = %q", got)
	}
	names := []string{"first", "second"}
	if got := applyOneMethod("x", 1, RenameMethod{Type: "list", Names: names}, nil); got != "second" {
		t.Fatalf("list = %q", got)
	}
}

func TestApplyToExtensionAndBoth(t *testing.T) {
	// ApplyTo=extension 只改扩展名，主名不变。
	m := []RenameMethod{{Type: "new_case", ApplyTo: "extension", Mode: "upper"}}
	if got, _ := applyRenameMethods("a.mp4", 0, m, nil); got != "a.MP4" {
		t.Fatalf("扩展名大写 = %q", got)
	}
	// ApplyTo=both 作用于完整名。
	m2 := []RenameMethod{{Type: "remove", ApplyTo: "both", Count: 2}}
	if got, _ := applyRenameMethods("ab.mp4", 0, m2, nil); got != ".mp4" {
		t.Fatalf("完整名删 2 = %q", got)
	}
}

func TestChainMethods(t *testing.T) {
	// Remove → Replace → Renumber 链式组合。
	methods := []RenameMethod{
		{Type: "remove", Count: 5},                                       // "1080p_Movie" → "_Movie"
		{Type: "replace", Pattern: "_", Replacement: " ", Occurrence: 0}, // " Movie"
		{Type: "trim"}, // "Movie"
		{Type: "renumber", Start: 1, Digits: 2, Text: " "}, // "Movie 01"
	}
	if got, _ := applyRenameMethods("1080p_Movie.mp4", 0, methods, nil); got != "Movie 01.mp4" {
		t.Fatalf("链式结果 = %q", got)
	}
}

func TestTimestampMethodTouch(t *testing.T) {
	got, touch := applyRenameMethods("a.mp4", 0, []RenameMethod{{Type: "timestamp"}}, nil)
	if got != "a.mp4" || !touch {
		t.Fatalf("timestamp 应只置 touch，得到 %q/%v", got, touch)
	}
}
