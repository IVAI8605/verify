// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRawThumbInvalidPathReturns400 锁定 /api/raw 与 /api/thumb 的参数非法状态码。
//
// 以前这里回 403，而审计中间件对 401/403 一律跳过（它们由 recordDenied 单独节流记录），
// 于是拿非法路径反复探测这两个接口在操作记录里完全无痕。改成 400 后：
//   - 403 只表示 CSRF / 授权拒绝，语义不再混用；
//   - 400 会走中间件正常记一行 ResultFail，探测留痕。
func TestRawThumbInvalidPathReturns400(t *testing.T) {
	s, _, _ := newTrashAPIServer(t)
	cases := []struct {
		name    string
		target  string
		handler http.HandlerFunc
	}{
		{"raw", "/api/raw?path=../../etc/passwd", s.handleRaw},
		{"thumb", "/api/thumb?path=../../etc/passwd", s.handleThumb},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "http://nas:8898"+c.target, nil)
		rec := httptest.NewRecorder()
		c.handler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s 参数非法应回 400，实际 %d", c.name, rec.Code)
		}
	}
}
