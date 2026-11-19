// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"reflect"
	"testing"
)

// TestTruncateHealthGroupsPrefix 回归 v4.1.1：
// 截断分组必须按展平前缀切，不能用路径集合匹配。重叠扫描根下同一路径会出现在
// 两个组，集合匹配会让两个副本都留下，实际探测数超过 total、进度 >100%。
func TestTruncateHealthGroupsPrefix(t *testing.T) {
	grouped := [][]string{
		{"/vol1/a", "/vol1/b", "/vol1/c"},
		{"/vol1/usb/a", "/vol1/usb/b", "/vol1/usb/c"}, // 与上一组部分路径相同（重叠根）
		{"/vol2/d"},
	}
	// 展平后的前 4 个：a b c a（第二组的 a 是第一组 a 的另一个副本）
	got := truncateHealthGroups(grouped, 4)
	var flat []string
	for _, g := range got {
		flat = append(flat, g...)
	}
	want := []string{"/vol1/a", "/vol1/b", "/vol1/c", "/vol1/usb/a"}
	if !reflect.DeepEqual(flat, want) {
		t.Fatalf("前缀截断应为 %v，实际 %v", want, flat)
	}
}

func TestTruncateHealthGroupsLimitWithinOneGroup(t *testing.T) {
	grouped := [][]string{{"a", "b", "c", "d"}, {"e"}}
	got := truncateHealthGroups(grouped, 2)
	var flat []string
	for _, g := range got {
		flat = append(flat, g...)
	}
	if !reflect.DeepEqual(flat, []string{"a", "b"}) {
		t.Fatalf("limit 落在单组内应为 [a b]，实际 %v", flat)
	}
	if len(got[1]) != 0 {
		t.Fatalf("后续组应清空，实际 %v", got[1])
	}
}

func TestTruncateHealthGroupsNoLimit(t *testing.T) {
	grouped := [][]string{{"a", "b"}, {"c"}}
	got := truncateHealthGroups(grouped, 10)
	var flat []string
	for _, g := range got {
		flat = append(flat, g...)
	}
	if !reflect.DeepEqual(flat, []string{"a", "b", "c"}) {
		t.Fatalf("limit 超过总数应全部保留，实际 %v", flat)
	}
}
