// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"verify/internal/store"
)

// serverWithSnapshot 构造只带快照管理器的最小 Server，避免单测依赖数据库与引擎。
// cur 为 nil 表示「启动后尚未跑过搜索」，正是本次修复要覆盖的状态。
func serverWithSnapshot(cur *searchSnapshot) *Server {
	s := &Server{}
	s.snap = newSnapshotManager(s)
	s.snap.cur = cur
	return s
}

func decodeRuleSearchBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v，原文 %q", err, rec.Body.String())
	}
	return body
}

// TestRuleSearchResultsNoSnapshot 回归 2026-11-06 真机审查发现的 P1 崩溃：
// 快照未建立时接口必须返回 409，不能空指针崩溃把连接直接关掉。
func TestRuleSearchResultsNoSnapshot(t *testing.T) {
	s := serverWithSnapshot(nil)
	rec := httptest.NewRecorder()
	s.handleRuleSearchResults(rec, httptest.NewRequest(http.MethodGet, "/api/rule-search/results", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("期望 409，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	body := decodeRuleSearchBody(t, rec)
	if body["error"] != "搜索结果未就绪" || body["state"] != "none" {
		t.Fatalf("期望未就绪提示与 state=none，实际 %v", body)
	}
}

// TestRuleSearchResultsBuilding 快照正在构建时按原逻辑返回 409，并带回真实状态。
func TestRuleSearchResultsBuilding(t *testing.T) {
	s := serverWithSnapshot(&searchSnapshot{ID: "s1", State: "building"})
	rec := httptest.NewRecorder()
	s.handleRuleSearchResults(rec, httptest.NewRequest(http.MethodGet, "/api/rule-search/results?page_size=3", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("期望 409，实际 %d", rec.Code)
	}
	body := decodeRuleSearchBody(t, rec)
	if body["state"] != "building" {
		t.Fatalf("期望 state=building，实际 %v", body)
	}
}

// TestRuleSearchResultsReady 快照就绪时仍能正常返回分组，确认修复没有改动正常路径。
func TestRuleSearchResultsReady(t *testing.T) {
	s := serverWithSnapshot(&searchSnapshot{
		ID:     "s2",
		State:  "ready",
		groups: []store.Group{{Key: "k1", Name: "示例组", Count: 2}},
	})
	rec := httptest.NewRecorder()
	s.handleRuleSearchResults(rec, httptest.NewRequest(http.MethodGet, "/api/rule-search/results?page_size=3", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Groups   []store.Group `json:"groups"`
		Total    int           `json:"total"`
		Page     int           `json:"page"`
		PageSize int           `json:"page_size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应解析失败：%v", err)
	}
	if body.Total != 1 || len(body.Groups) != 1 || body.Page != 1 {
		t.Fatalf("期望 1 组且在首页，实际 %+v", body)
	}
}

// TestRuleSearchResultsAfterInvalidate 规则或目录变更后快照被丢弃，同样不能崩溃。
func TestRuleSearchResultsAfterInvalidate(t *testing.T) {
	s := serverWithSnapshot(&searchSnapshot{ID: "s3", State: "ready"})
	s.snap.invalidate()
	rec := httptest.NewRecorder()
	s.handleRuleSearchResults(rec, httptest.NewRequest(http.MethodGet, "/api/rule-search/results", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("期望 409，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if decodeRuleSearchBody(t, rec)["state"] != "none" {
		t.Fatal("快照失效后应报告 state=none")
	}
}

// TestRuleSearchResultsRejectsNonGET 非 GET 请求仍按原口径返回 405。
func TestRuleSearchResultsRejectsNonGET(t *testing.T) {
	s := serverWithSnapshot(nil)
	rec := httptest.NewRecorder()
	s.handleRuleSearchResults(rec, httptest.NewRequest(http.MethodPost, "/api/rule-search/results", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("期望 405，实际 %d", rec.Code)
	}
}

// TestSnapshotGroupMembers 覆盖 P1-1 的补强：规则类分组键（content:/name:/size:/attributes:/all:）
// 带属性片段，引擎无法按前缀重算，只能由快照给出成员；
// 快照未就绪或键不存在时必须返回 ok=false，让引擎走自己的兜底而不是当成「无需校验」。
func TestSnapshotGroupMembers(t *testing.T) {
	s := serverWithSnapshot(&searchSnapshot{
		ID:    "s4",
		State: "ready",
		groups: []store.Group{{Key: "content:h\x1fa", Files: []store.FileRow{
			{Path: "/vol1/a/1.mkv"},
			{Path: "/vol1/b/1.mkv"},
		}}},
	})
	members, ok := s.snap.groupMembers("content:h\x1fa")
	if !ok || len(members) != 2 || members[1] != "/vol1/b/1.mkv" {
		t.Fatalf("应取到 2 个成员，实际 ok=%v members=%v", ok, members)
	}
	if _, ok := s.snap.groupMembers("content:h\x1fb"); ok {
		t.Fatal("不存在的键不应命中")
	}
	if _, ok := s.snap.groupMembers(""); ok {
		t.Fatal("空键不应命中")
	}
	s.snap.cur.State = "building"
	if _, ok := s.snap.groupMembers("content:h\x1fa"); ok {
		t.Fatal("快照未就绪时不应命中")
	}
}

// TestSnapshotRemovePaths 覆盖第五轮 P1-4：隔离成功后把文件从快照剔除，
// 剩余条目继续可操作（不整份失效），组内不足两份时整组消失，统计同步更新。
func TestSnapshotRemovePaths(t *testing.T) {
	s := serverWithSnapshot(&searchSnapshot{
		ID:    "s5",
		State: "ready",
		groups: []store.Group{
			{Key: "k1", Count: 3, TotalSize: 60, Waste: 40, Files: []store.FileRow{
				{Path: "/vol1/a/1.mkv", Size: 10},
				{Path: "/vol1/b/1.mkv", Size: 20},
				{Path: "/vol1/c/1.mkv", Size: 30},
			}},
			{Key: "k2", Count: 2, TotalSize: 20, Waste: 10, Files: []store.FileRow{
				{Path: "/vol1/d/2.mkv", Size: 10},
				{Path: "/vol1/e/2.mkv", Size: 10},
			}},
		},
	})
	s.snap.removePaths([]string{"/vol1/a/1.mkv", "/vol1/d/2.mkv"})
	groups := s.snap.cur.groups
	if len(groups) != 1 || groups[0].Key != "k1" {
		t.Fatalf("k2 只剩一份应整组移除，实际 %+v", groups)
	}
	g := groups[0]
	if g.Count != 2 || len(g.Files) != 2 || g.TotalSize != 50 || g.Waste != 20 {
		t.Fatalf("k1 统计未按剩余成员重算：%+v", g)
	}
	if s.snap.cur.Totals.Groups != 1 || s.snap.cur.Totals.Files != 2 {
		t.Fatalf("快照汇总未更新：%+v", s.snap.cur.Totals)
	}
}
