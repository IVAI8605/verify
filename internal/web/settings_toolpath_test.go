// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 回归 v4.1.1：同一次设置保存里「换扫描卷 + 把工具程序放进新卷」必须被拒绝。
// 旧实现用旧 cur.Volumes 校验工具路径，新卷要等后面才赋值，于是绕过约束。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsToolPathCheckedAgainstNewVolumes(t *testing.T) {
	s, _, _ := newTrashAPIServer(t)
	// 新卷：里面放一个可执行 ffmpeg
	newVol := t.TempDir()
	ffmpeg := filepath.Join(newVol, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 一次请求同时改 volumes 和 ffmpeg（工具落在刚设置的新卷里）
	body, err := json.Marshal(map[string]any{
		"volumes": []string{newVol},
		"ffmpeg":  ffmpeg,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader(body))
	s.handleSettings(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("工具程序落在本次新扫描卷内应返回 400，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains([]byte(resp.Error), []byte("不能位于扫描卷内")) {
		t.Fatalf("错误文案应指出工具在扫描卷内，实际 %q", fmt.Sprint(resp.Error))
	}
}
