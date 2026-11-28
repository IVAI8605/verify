// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件是 v3.17.4 的入口回归：日期口径（F-10）、外部程序路径（F-15）、
// 403 留档（F-21）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verify/internal/config"
	"verify/internal/store"
)

// TestLocalDayStartUsesLocalZone 回归 F-10：DateOnly 过滤按本地日截断，
// UTC+8 下本地 03:00 不能被算到前一天。
func TestLocalDayStartUsesLocalZone(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("CST", 8*3600)
	defer func() { time.Local = old }()

	ts := time.Date(2026, 11, 9, 3, 0, 0, 0, time.Local).UnixNano()
	want := time.Date(2026, 11, 9, 0, 0, 0, 0, time.Local)
	if got := localDayStart(ts); !got.Equal(want) {
		t.Fatalf("应截到本地当天 0 点，实际 %v", got)
	}
}

// TestCheckToolPath 回归 F-15：ffmpeg / ffprobe 只接受可执行普通文件，
// 且不能落在扫描卷内；留空表示自动探测。
func TestCheckToolPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	noExec := filepath.Join(dir, "noexec")
	if err := os.WriteFile(noExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := checkToolPath("FFmpeg", "", nil); msg != "" {
		t.Fatalf("留空应合法，实际 %q", msg)
	}
	if msg := checkToolPath("FFmpeg", exe, nil); msg != "" {
		t.Fatalf("可执行文件应合法，实际 %q", msg)
	}
	if msg := checkToolPath("FFmpeg", noExec, nil); msg == "" {
		// 部分网络文件系统（SMB/CIFS）会忽略 chmod，0644 也带执行位；
		// 这类环境无法验证“不可执行”，但不代表校验逻辑失效。
		if fi, err := os.Stat(noExec); err == nil && fi.Mode().Perm()&0o111 != 0 {
			t.Log("当前文件系统不保留可执行位，跳过“不可执行文件应被拒”断言")
		} else {
			t.Fatal("不可执行文件应被拒")
		}
	}
	if msg := checkToolPath("FFmpeg", filepath.Join(dir, "missing"), nil); msg == "" {
		t.Fatal("不存在的路径应被拒")
	}
	if msg := checkToolPath("FFmpeg", exe, []string{dir}); msg == "" {
		t.Fatal("位于扫描卷内的路径应被拒")
	}
}

// TestCSRFDeniedRecorded 回归 F-21：403 也要留档，并写明拒绝原因。
func TestCSRFDeniedRecorded(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := mgr.Get()
	c.OpLogEnabled = true
	if err := mgr.Replace(c); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: mgr, st: st}
	h := s.csrfGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "http://nas:8898/api/scan", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨站写请求应 403，实际 %d", rec.Code)
	}
	var summary string
	if err := st.DB().QueryRow(`SELECT summary FROM op_log WHERE action='auth_denied' ORDER BY id DESC LIMIT 1`).Scan(&summary); err != nil {
		t.Fatalf("403 应写入操作记录：%v", err)
	}
	if !strings.Contains(summary, "跨站写请求") {
		t.Fatalf("记录应写明拒绝原因，实际 %q", summary)
	}
}
