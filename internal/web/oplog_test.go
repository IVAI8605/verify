// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"verify/internal/config"
)

func testOpLogServer(t *testing.T) *Server {
	t.Helper()
	mgr, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: mgr}
}

func TestConfigDiffDetailsRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	old := config.Default(dir)
	cur := config.Default(dir)
	cur.AccessToken = "supersecret-token"
	cur.AIEndpoint = "http://user:pass@host/v1"
	cur.OpLogReads = true

	details := configDiffDetails(old, cur)
	if len(details) != 3 {
		t.Fatalf("应有 3 项变更，实际 %d：%+v", len(details), details)
	}
	joined := ""
	for _, d := range details {
		joined += d.Field + d.Old + d.New + "|"
		if d.Field == "access_token" || d.Field == "ai_endpoint" {
			if d.Old != "（已隐藏）" || d.New != "（已隐藏）" {
				t.Fatalf("敏感字段必须只记已隐藏：%+v", d)
			}
		}
	}
	if strings.Contains(joined, "supersecret-token") || strings.Contains(joined, "user:pass") {
		t.Fatalf("diff 泄露敏感值：%s", joined)
	}
	if !strings.Contains(joined, "op_log_reads") {
		t.Fatal("普通字段应记录改前改后")
	}
}

func TestShouldLogRequest(t *testing.T) {
	s := testOpLogServer(t)
	c := s.cfg.Get()
	c.OpLogEnabled = true
	c.OpLogReads = false
	if err := s.cfg.Replace(c); err != nil {
		t.Fatal(err)
	}
	post := httptest.NewRequest(http.MethodPost, "/api/scan", nil)
	if !s.shouldLogRequest(post) {
		t.Fatal("写请求应记录")
	}
	if s.shouldLogRequest(httptest.NewRequest(http.MethodGet, "/api/groups", nil)) {
		t.Fatal("只读开关关闭时 GET 不应记录")
	}
	if s.shouldLogRequest(httptest.NewRequest(http.MethodPost, "/api/trash", nil)) {
		t.Fatal("已有业务埋点的接口应跳过中间件汇总")
	}

	c.OpLogReads = true
	if err := s.cfg.Replace(c); err != nil {
		t.Fatal(err)
	}
	if !s.shouldLogRequest(httptest.NewRequest(http.MethodGet, "/api/groups", nil)) {
		t.Fatal("只读开关打开后 GET 应记录")
	}
	if s.shouldLogRequest(httptest.NewRequest(http.MethodGet, "/api/status", nil)) {
		t.Fatal("高频状态轮询必须始终排除")
	}
	if s.shouldLogRequest(httptest.NewRequest(http.MethodGet, "/api/events", nil)) {
		t.Fatal("SSE 长连接必须始终排除")
	}
	c.OpLogEnabled = false
	if err := s.cfg.Replace(c); err != nil {
		t.Fatal(err)
	}
	if s.shouldLogRequest(httptest.NewRequest(http.MethodPost, "/api/scan", nil)) {
		t.Fatal("总开关关闭后写请求也不记录（删除类审计另有保障）")
	}
}

func TestRequestDetailsOnlyPaths(t *testing.T) {
	body := `{"paths":["/vol1/a.mkv","/vol1/b.mkv"],"ids":[3],"access_token":"secret"}`
	r := httptest.NewRequest(http.MethodPost, "/api/move", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	details, count := requestDetails(r)
	if count != 3 || len(details) != 2 || details[0].Path != "/vol1/a.mkv" {
		t.Fatalf("明细提取不符：count=%d details=%+v", count, details)
	}
	got, err := io.ReadAll(r.Body)
	if err != nil || string(got) != body {
		t.Fatalf("请求体必须完整重建，实际 %q err=%v", string(got), err)
	}
	// 上传类请求不能读 body，避免破坏原请求。
	up := httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader("binary"))
	up.Header.Set("Content-Type", "multipart/form-data")
	if d, n := requestDetails(up); len(d) != 0 || n != 0 {
		t.Fatal("multipart 应跳过明细提取")
	}
}

func TestCSVInjectionGuard(t *testing.T) {
	if csvSafe("=1+1") != "'=1+1" || csvSafe("@SUM(A1)") != "'@SUM(A1)" {
		t.Fatal("公式前缀未加保护")
	}
	if csvSafe("/vol1/a.mkv") != "/vol1/a.mkv" {
		t.Fatal("普通文本不应被改写")
	}
}

func TestOpAction(t *testing.T) {
	if opAction(http.MethodPost, "/api/scan") != "scan_start" {
		t.Fatal("已知接口应映射到固定动作名")
	}
	if got := opAction(http.MethodDelete, "/api/some/new/thing"); !strings.HasPrefix(got, "api_delete_some_new_thing") {
		t.Fatalf("未知接口应生成稳定动作名，实际 %s", got)
	}
}
