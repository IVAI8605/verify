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

// modeTestServer 只注入配置；被模式拦住的分支不应触碰 store/engine，
// 所以这里故意不构造完整实例，一旦实现漏了门禁就会空指针。
func modeTestServer(t *testing.T, mode string) *Server {
	t.Helper()
	m, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := m.Get()
	c.MatchMode = mode
	if err := m.Replace(c); err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: m}
}

func assertModeDisabled(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，want 200，body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Groups   []json.RawMessage `json:"groups"`
		Total    int               `json:"total"`
		Disabled bool              `json:"disabled"`
		Reason   string            `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if !body.Disabled || body.Total != 0 || len(body.Groups) != 0 || body.Reason == "" {
		t.Fatalf("应返回 disabled + 空结果 + 原因，实际 %+v", body)
	}
}

// TestMatchModeExactBlocksMediaHandlers 完全相同模式不能查视频/音频/图片分组。
func TestMatchModeExactBlocksMediaHandlers(t *testing.T) {
	s := modeTestServer(t, config.MatchModeExact)
	for name, fn := range map[string]http.HandlerFunc{
		"video":    s.handleVideoGroups,
		"audio":    s.handleAudioGroups,
		"photo":    s.handlePhotoGroups,
		"fuzzy":    s.handleFuzzyGroups,
		"semantic": s.handleSemanticGroups,
	} {
		rec := httptest.NewRecorder()
		fn(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if !isDisabledBody(t, rec) {
			t.Fatalf("%s 在 exact 模式下应被禁用", name)
		}
	}
}

// TestMatchModeSimilarBlocksNameAndExact 相似视频模式只保留视频/片头/音频入口。
func TestMatchModeSimilarBlocksNameAndExact(t *testing.T) {
	s := modeTestServer(t, config.MatchModeSimilar)
	rec := httptest.NewRecorder()
	s.handleGroups(rec, httptest.NewRequest(http.MethodGet, "/api/groups?mode=name", nil))
	assertModeDisabled(t, rec)

	rec = httptest.NewRecorder()
	s.handleGroups(rec, httptest.NewRequest(http.MethodGet, "/api/groups", nil))
	assertModeDisabled(t, rec)
}

// TestMatchModeIgnoreContentBlocksVideo 忽略内容模式只按文件名/属性分组。
func TestMatchModeIgnoreContentBlocksVideo(t *testing.T) {
	s := modeTestServer(t, config.MatchModeIgnoreContent)
	rec := httptest.NewRecorder()
	s.handleVideoGroups(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assertModeDisabled(t, rec)
}

// TestMatchModeCustomKinds 自定义组合只放行勾选的结果类型（v3.6.0）。
func TestMatchModeCustomKinds(t *testing.T) {
	cfg := &config.Config{MatchMode: config.MatchModeCustom, MatchKinds: []string{"audio", "photo"}}
	for _, kind := range []string{"audio", "photo"} {
		if !matchModeAllows(cfg, kind) {
			t.Fatalf("自定义组合应放行 %s", kind)
		}
	}
	for _, kind := range []string{"exact", "video", "name", "folder"} {
		if matchModeAllows(cfg, kind) {
			t.Fatalf("自定义组合不应放行未勾选的 %s", kind)
		}
	}
}

func isDisabledBody(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	var body struct {
		Disabled bool `json:"disabled"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Disabled
}
