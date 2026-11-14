// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件回归 v3.26.0 F2：分组分页 page_size 上限放宽到 1000，默认值仍为 20。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// groupPageSize 调用分组处理器并取回显的 page_size。
func groupPageSize(t *testing.T, h http.HandlerFunc, target string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s 应返回 200，实际 %d（%s）", target, rec.Code, rec.Body.String())
	}
	var body struct {
		PageSize int `json:"page_size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s 响应不是合法 JSON：%v", target, err)
	}
	return body.PageSize
}

// TestGroupPageSizeBounds 覆盖默认值、下界与上界：1001 必须被钳到 1000，其余沿用原语义。
func TestGroupPageSizeBounds(t *testing.T) {
	s, _, _ := newTrashAPIServer(t)
	for _, tc := range []struct {
		target string
		want   int
	}{
		{"/api/groups", 20},
		{"/api/groups?page_size=0", 1},
		{"/api/groups?page_size=1000", 1000},
		{"/api/groups?page_size=1001", 1000},
		{"/api/groups?page_size=99999", 1000},
	} {
		if got := groupPageSize(t, s.handleGroups, tc.target); got != tc.want {
			t.Fatalf("%s 的 page_size 应为 %d，实际 %d", tc.target, tc.want, got)
		}
	}
}

// TestVideoGroupPageSizeCap 视频分组与主分组保持同一上限。
func TestVideoGroupPageSizeCap(t *testing.T) {
	s, _, _ := newTrashAPIServer(t)
	if got := groupPageSize(t, s.handleVideoGroups, "/api/groups/video?page_size=1001"); got != 1000 {
		t.Fatalf("视频分组 page_size 上限应为 1000，实际 %d", got)
	}
}
