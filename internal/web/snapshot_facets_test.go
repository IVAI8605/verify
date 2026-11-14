// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"verify/internal/store"
)

// facetSnapshot 构造一份带分布的已就绪快照，覆盖两个卷根、三种类型。
func facetSnapshot() *searchSnapshot {
	roots := []string{filepath.Clean("/vol1/1000/media"), filepath.Clean("/vol2/备份")}
	groups := []store.Group{
		{Key: "exact:1", Kind: "exact", Files: []store.FileRow{{Path: filepath.Clean("/vol1/1000/media/a.mkv")}}},
		{Key: "video:2", Kind: "video", Files: []store.FileRow{{Path: filepath.Clean("/vol1/1000/media/电影/b.mkv")}}},
		{Key: "name:3", Kind: "name", Files: []store.FileRow{{Path: filepath.Clean("/vol2/备份/c.mkv")}}},
	}
	return &searchSnapshot{
		ID: "s-facets", State: "ready", roots: roots, groups: groups,
		groupDirs: computeGroupDirs(groups, roots),
	}
}

// TestRuleSearchFacets 覆盖 v3.11.0 分段收窄：type 只影响 dirs/total，
// dir 同时过滤 types 与 dirs；未指定时返回全量分布。
func TestRuleSearchFacets(t *testing.T) {
	s := serverWithSnapshot(facetSnapshot())

	rec := httptest.NewRecorder()
	s.handleRuleSearchFacets(rec, httptest.NewRequest(http.MethodGet, "/api/rule-search/facets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	body := decodeRuleSearchBody(t, rec)
	if body["total"].(float64) != 3 {
		t.Fatalf("全量 total 应为 3，实际 %v", body["total"])
	}
	if len(body["types"].([]any)) != 3 || len(body["dirs"].([]any)) != 3 {
		t.Fatalf("期望 3 类 3 个目录，实际 %v", body)
	}

	rec = httptest.NewRecorder()
	s.handleRuleSearchFacets(rec, httptest.NewRequest(http.MethodGet, "/api/rule-search/facets?type=video", nil))
	body = decodeRuleSearchBody(t, rec)
	if body["total"].(float64) != 1 {
		t.Fatalf("type=video 时 total 应为 1，实际 %v", body["total"])
	}
	if len(body["types"].([]any)) != 3 {
		t.Fatalf("type 不应影响 types 全量分布，实际 %v", body["types"])
	}

	rec = httptest.NewRecorder()
	s.handleRuleSearchFacets(rec, httptest.NewRequest(http.MethodGet,
		"/api/rule-search/facets?dir="+filepath.Clean("/vol1/1000/media"), nil))
	body = decodeRuleSearchBody(t, rec)
	if body["total"].(float64) != 2 {
		t.Fatalf("dir 过滤后 total 应为 2，实际 %v", body["total"])
	}
	if len(body["types"].([]any)) != 2 {
		t.Fatalf("dir 应同时过滤 types，实际 %v", body["types"])
	}
}

// TestRuleSearchFacetsNoSnapshot 快照未建立时与 results 保持一致，返回 409 而不是空指针。
func TestRuleSearchFacetsNoSnapshot(t *testing.T) {
	s := serverWithSnapshot(nil)
	rec := httptest.NewRecorder()
	s.handleRuleSearchFacets(rec, httptest.NewRequest(http.MethodGet, "/api/rule-search/facets", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("期望 409，实际 %d", rec.Code)
	}
}

// TestFacetsDirCountMatchesResults 校验 v3.11.1 的分母口径：
// 跨目录的组在目录 chip 上各计一次，但 facets.total 必须按组去重，
// 与同一 dir/type/q 条件下 results.total 完全相等。
func TestFacetsDirCountMatchesResults(t *testing.T) {
	roots := []string{filepath.Clean("/vol1/1000/media")}
	groups := []store.Group{
		{Key: "cross", Kind: "exact", Files: []store.FileRow{
			{Path: filepath.Clean("/vol1/1000/media/A/a.mkv")},
			{Path: filepath.Clean("/vol1/1000/media/B/a.mkv")},
		}},
	}
	s := serverWithSnapshot(&searchSnapshot{
		ID: "s-cross", State: "ready", roots: roots, groups: groups,
		groupDirs: computeGroupDirs(groups, roots),
	})

	for _, dir := range []string{"", filepath.Clean("/vol1/1000/media/A"), filepath.Clean("/vol1/1000/media/B")} {
		facetsURL := "/api/rule-search/facets"
		resultsURL := "/api/rule-search/results?page_size=1"
		if dir != "" {
			facetsURL += "?dir=" + dir
			resultsURL += "&dir=" + dir
		}
		rec := httptest.NewRecorder()
		s.handleRuleSearchFacets(rec, httptest.NewRequest(http.MethodGet, facetsURL, nil))
		fb := decodeRuleSearchBody(t, rec)
		rec = httptest.NewRecorder()
		s.handleRuleSearchResults(rec, httptest.NewRequest(http.MethodGet, resultsURL, nil))
		rb := decodeRuleSearchBody(t, rec)
		if fb["total"].(float64) != rb["total"].(float64) {
			t.Fatalf("dir=%q 时 facets.total=%v 与 results.total=%v 不一致", dir, fb["total"], rb["total"])
		}
		if rb["total"].(float64) != 1 {
			t.Fatalf("dir=%q 应命中跨目录组 1 组，实际 %v", dir, rb["total"])
		}
	}

	rec := httptest.NewRecorder()
	s.handleRuleSearchFacets(rec, httptest.NewRequest(http.MethodGet, "/api/rule-search/facets", nil))
	dirs := decodeRuleSearchBody(t, rec)["dirs"].([]any)
	if len(dirs) != 2 {
		t.Fatalf("跨目录组应在两个目录 chip 上各出现一次，实际 %v", dirs)
	}
}

// TestFacetsTotalEqualsResultsWithKeyword 确认 q 也进入 facets 的 total 口径：
// 界面按关键字筛选时，「共 M 组」与列表总数不能分叉。
func TestFacetsTotalEqualsResultsWithKeyword(t *testing.T) {
	groups := []store.Group{
		{Key: "k1", Kind: "exact", Name: "影片A", Files: []store.FileRow{{Path: filepath.Clean("/vol1/x/影片A.mkv")}}},
		{Key: "k2", Kind: "exact", Name: "影片B", Files: []store.FileRow{{Path: filepath.Clean("/vol1/x/影片B.mkv")}}},
	}
	s := serverWithSnapshot(&searchSnapshot{
		ID: "s-q", State: "ready", groups: groups,
		groupDirs: computeGroupDirs(groups, nil),
	})

	rec := httptest.NewRecorder()
	s.handleRuleSearchFacets(rec, httptest.NewRequest(http.MethodGet, "/api/rule-search/facets?type=exact&q=影片A", nil))
	fb := decodeRuleSearchBody(t, rec)
	rec = httptest.NewRecorder()
	s.handleRuleSearchResults(rec, httptest.NewRequest(http.MethodGet, "/api/rule-search/results?type=exact&q=影片A", nil))
	rb := decodeRuleSearchBody(t, rec)
	if fb["total"].(float64) != 1 || rb["total"].(float64) != 1 {
		t.Fatalf("q 过滤后应各为 1，实际 facets=%v results=%v", fb["total"], rb["total"])
	}
}

// TestKeywordMaskCache 覆盖 v3.13.0（第十一轮 P2-5）：同一关键字的匹配位图被缓存复用；
// 组集合被 removePaths 改写后必须整体失效，不能拿旧下标去判断新组。
func TestKeywordMaskCache(t *testing.T) {
	groups := []store.Group{
		{Key: "a", Kind: "exact", Name: "影片A", Files: []store.FileRow{{Path: "/vol1/x/a.mkv"}}},
		{Key: "b", Kind: "exact", Name: "影片B", Files: []store.FileRow{{Path: "/vol1/x/b.mkv"}}},
	}
	snap := &searchSnapshot{ID: "s-kw", State: "ready", groups: groups}
	m := &snapshotManager{cur: snap}

	first := snap.keywordMask(groups, "影片a")
	if len(first) != 2 || !first[0] || first[1] {
		t.Fatalf("关键字位图错误：%v", first)
	}
	second := snap.keywordMask(groups, "影片a")
	if len(second) != 2 || !second[0] || second[1] {
		t.Fatalf("缓存命中后结果应一致：%v", second)
	}
	if &first[0] != &second[0] {
		t.Fatal("同一关键字应命中缓存，而不是重新计算")
	}

	// removePaths 改写组集合：A 组只剩一个文件会被丢弃，即便长度变化也要保证不复用旧位图。
	m.removePaths([]string{"/vol1/x/a.mkv"})
	snap.mu.RLock()
	now := snap.groups
	snap.mu.RUnlock()
	mask := snap.keywordMask(now, "影片a")
	for i := range mask {
		if mask[i] {
			t.Fatalf("移除 A 后不应再命中（旧位图被复用）：%v", mask)
		}
	}
}

// TestFacetsDeepDirBackfill 覆盖 v3.13.0（第十一轮 P2-6）：dir 指向顶层目录以下的子目录时，
// groupDirs 只记顶层目录，dirs 会为空；此时必须把收窄目录本身回填成唯一项，
// 否则界面在「已按深层目录收窄」的状态下看不到任何目录提示。
func TestFacetsDeepDirBackfill(t *testing.T) {
	roots := []string{filepath.Clean("/vol1/1000/media")}
	deep := filepath.Clean("/vol1/1000/media/电影/2024")
	groups := []store.Group{
		{Key: "d1", Kind: "exact", Files: []store.FileRow{{Path: filepath.Join(deep, "a.mkv")}}},
	}
	s := serverWithSnapshot(&searchSnapshot{
		ID: "s-deep", State: "ready", roots: roots, groups: groups,
		groupDirs: computeGroupDirs(groups, roots),
	})

	rec := httptest.NewRecorder()
	s.handleRuleSearchFacets(rec, httptest.NewRequest(http.MethodGet, "/api/rule-search/facets?dir="+deep, nil))
	body := decodeRuleSearchBody(t, rec)
	if body["total"].(float64) != 1 {
		t.Fatalf("深层 dir 应命中 1 组，实际 %v", body["total"])
	}
	dirs := body["dirs"].([]any)
	if len(dirs) != 1 {
		t.Fatalf("深层 dir 应收窄为唯一目录项，实际 %v", dirs)
	}
	item := dirs[0].(map[string]any)
	if item["dir"] != deep || item["count"].(float64) != 1 {
		t.Fatalf("回填目录项应为 %s/1，实际 %v", deep, item)
	}
}

// TestRuleSearchResultsDirFilter 覆盖第十轮 P1：dir 必须真正过滤列表，
// 且按路径段边界判断，/vol5/media 不能匹配 /vol5/media2。
func TestRuleSearchResultsDirFilter(t *testing.T) {
	groups := []store.Group{
		{Key: "a", Kind: "exact", Files: []store.FileRow{{Path: filepath.Clean("/vol5/media/a.mkv")}}},
		{Key: "b", Kind: "exact", Files: []store.FileRow{{Path: filepath.Clean("/vol5/media2/b.mkv")}}},
	}
	s := serverWithSnapshot(&searchSnapshot{ID: "s-dir", State: "ready", groups: groups})

	rec := httptest.NewRecorder()
	s.handleRuleSearchResults(rec, httptest.NewRequest(http.MethodGet,
		"/api/rule-search/results?dir="+filepath.Clean("/vol5/media"), nil))
	body := decodeRuleSearchBody(t, rec)
	if body["total"].(float64) != 1 {
		t.Fatalf("dir 应只命中同名前缀目录本身，实际 total=%v", body["total"])
	}
	gs := body["groups"].([]any)
	if len(gs) != 1 || gs[0].(map[string]any)["key"] != "a" {
		t.Fatalf("目录边界判断错误，实际 %v", gs)
	}
}

// TestRuleSearchResultsHasMore 确认 has_more 不再靠 length < total 猜：
// 末页恰好取满时应为 false，前面还有数据时为 true。
func TestRuleSearchResultsHasMore(t *testing.T) {
	groups := make([]store.Group, 0, 5)
	for i := 0; i < 5; i++ {
		groups = append(groups, store.Group{Key: "k" + string(rune('0'+i)), Kind: "exact"})
	}
	s := serverWithSnapshot(&searchSnapshot{ID: "s-more", State: "ready", groups: groups})

	rec := httptest.NewRecorder()
	s.handleRuleSearchResults(rec, httptest.NewRequest(http.MethodGet, "/api/rule-search/results?page=1&page_size=2", nil))
	body := decodeRuleSearchBody(t, rec)
	if body["has_more"] != true {
		t.Fatalf("第一页应 has_more=true，实际 %v", body["has_more"])
	}

	rec = httptest.NewRecorder()
	s.handleRuleSearchResults(rec, httptest.NewRequest(http.MethodGet, "/api/rule-search/results?page=3&page_size=2", nil))
	body = decodeRuleSearchBody(t, rec)
	if body["has_more"] != false {
		t.Fatalf("末页应 has_more=false，实际 %v", body["has_more"])
	}
}
