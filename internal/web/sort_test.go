// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"net/http/httptest"
	"testing"

	"verify/internal/store"
)

// TestSortParamWhitelist 验证白名单收录 path/type（此前缺失导致 ?sort=path/type 静默回退 waste）。
func TestSortParamWhitelist(t *testing.T) {
	cases := []struct {
		query   string
		wantKey string
		wantOrd string
	}{
		{"?sort=path", "path", "desc"},
		{"?sort=type&order=asc", "type", "asc"},
		{"?sort=total_size&order=asc", "total_size", "asc"},
		{"?sort=bogus", "waste", "desc"},              // 非法键回退
		{"?sort=path&order=sideways", "path", "desc"}, // 非法方向回退
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/"+c.query, nil)
		key, ord := sortParam(r)
		if key != c.wantKey || ord != c.wantOrd {
			t.Fatalf("sortParam(%q)=(%q,%q)，期望 (%q,%q)", c.query, key, ord, c.wantKey, c.wantOrd)
		}
	}
}

// TestSortGroupsPathAndType 覆盖 sortGroups 里 path/type 两个分支的比较器。
func TestSortGroupsPathAndType(t *testing.T) {
	a := store.Group{
		Name: "A",
		Files: []store.FileRow{
			{Path: "/vol1/b/x.mkv", Ext: "mkv"},
		},
	}
	b := store.Group{
		Name: "B",
		Files: []store.FileRow{
			{Path: "/vol1/a/y.mp4", Ext: "mp4"},
		},
	}

	byPath := sortGroups([]store.Group{a, b}, "path", "asc")
	if firstPath(byPath[0]) != "/vol1/a/y.mp4" {
		t.Fatalf("path 升序首组应为 /vol1/a/y.mp4，实际 %q", firstPath(byPath[0]))
	}

	byType := sortGroups([]store.Group{a, b}, "type", "asc")
	if firstExt(byType[0]) != "mkv" {
		t.Fatalf("type 升序首组应为 mkv，实际 %q", firstExt(byType[0]))
	}
}
