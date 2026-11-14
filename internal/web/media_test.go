// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"verify/internal/config"
)

// TestFolderGroupsDisabledSkipsStore 关闭「计算重复文件夹」后必须直接返回
// disabled，不能再去查库。这里的 Server 故意不注入 store：一旦实现里
// 仍然调用查询就会空指针，测试立即失败。
func TestFolderGroupsDisabledSkipsStore(t *testing.T) {
	m, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := m.Get()
	c.ComputeDupFolders = false
	if err := m.Replace(c); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: m}

	rec := httptest.NewRecorder()
	s.handleFolderGroups(rec, httptest.NewRequest(http.MethodGet, "/api/groups/folder", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，want 200，body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Groups   []json.RawMessage `json:"groups"`
		Total    int               `json:"total"`
		Disabled bool              `json:"disabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if !body.Disabled || body.Total != 0 || len(body.Groups) != 0 {
		t.Fatalf("关闭后应返回空结果 + disabled=true，实际 %+v", body)
	}
}
