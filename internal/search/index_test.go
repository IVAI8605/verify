// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package search

import (
	"strings"
	"testing"
)

func testEntries() []Entry {
	mk := func(id int64, name, path, ext string, size, mtime int64) Entry {
		return Entry{ID: id, Name: name, NameL: strings.ToLower(name), Path: path, PathL: strings.ToLower(path), Ext: ext, Size: size, Mtime: mtime}
	}
	return []Entry{
		mk(1, "Movie.2024.mkv", "/vol6/资源/电影/Movie.2024.mkv", "mkv", 1000, 100),
		mk(2, "movie_extra.mp4", "/vol6/资源/电影/movie_extra.mp4", "mp4", 2000, 200),
		mk(3, "某剧集.S01E01.mkv", "/vol6/资源/剧集/某剧集.S01E01.mkv", "mkv", 3000, 300),
		mk(4, "note.txt", "/vol6/文档/note.txt", "txt", 10, 400),
	}
}

func newLoaded() *Index {
	ix := New(0)
	ix.Load(testEntries())
	return ix
}

func names(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

// TestQuerySubstring 验证默认子串匹配默认忽略大小写，开关后区分大小写。
func TestQuerySubstring(t *testing.T) {
	ix := newLoaded()
	r := ix.Query(Query{Text: "movie"})
	if r.Total != 2 || len(r.Items) != 2 {
		t.Fatalf("忽略大小写应命中 2 个，实际 total=%d items=%d", r.Total, len(r.Items))
	}
	r = ix.Query(Query{Text: "Movie", CaseSensitive: true})
	if r.Total != 1 {
		t.Fatalf("区分大小写时只应命中 Movie，实际 %d", r.Total)
	}
}

// TestQueryWildcard 验证 * 与 ? 通配符。
func TestQueryWildcard(t *testing.T) {
	ix := newLoaded()
	r := ix.Query(Query{Text: "*.mkv", Mode: "wildcard"})
	if r.Total != 2 {
		t.Fatalf("通配符应命中 2 个 mkv，实际 %d", r.Total)
	}
	r = ix.Query(Query{Text: "?ote.txt", Mode: "wildcard"})
	if r.Total != 1 {
		t.Fatalf("? 通配符应命中 1 个，实际 %d", r.Total)
	}
}

// TestQueryRegex 验证正则匹配与危险模式拒绝。
func TestQueryRegex(t *testing.T) {
	ix := newLoaded()
	r := ix.Query(Query{Text: `S\d{2}E\d{2}`, Mode: "regex"})
	if r.Total != 1 {
		t.Fatalf("正则应命中剧集文件，实际 %d", r.Total)
	}
	if _, err := compileSafeRegex(`(a+)+`, false); err == nil {
		t.Fatal("嵌套量词应被拒绝")
	}
	if _, err := compileSafeRegex(strings.Repeat("a", 201), false); err == nil {
		t.Fatal("超长正则应被拒绝")
	}
}

// TestQueryFiltersAndSort 验证扩展名、大小、时间过滤与排序。
func TestQueryFiltersAndSort(t *testing.T) {
	ix := newLoaded()
	r := ix.Query(Query{Text: "movie", Exts: []string{"mp4"}})
	if r.Total != 1 || r.Items[0].Name != "movie_extra.mp4" {
		t.Fatalf("扩展名过滤不正确: total=%d", r.Total)
	}
	r = ix.Query(Query{Text: "movie", MinSize: 1500})
	if r.Total != 1 || r.Items[0].Size != 2000 {
		t.Fatalf("大小过滤不正确: total=%d", r.Total)
	}
	r = ix.Query(Query{Text: "movie", Since: 150})
	if r.Total != 1 || r.Items[0].Mtime != 200 {
		t.Fatalf("时间过滤不正确: total=%d", r.Total)
	}
	r = ix.Query(Query{Text: "movie", Sort: "size", Order: "desc"})
	if r.Total != 2 || r.Items[0].Size != 2000 {
		t.Fatalf("按大小降序不正确: %v", names(r.Items))
	}
}

// TestQueryDirAndPaging 验证目录前缀过滤与分页截断。
func TestQueryDirAndPaging(t *testing.T) {
	ix := newLoaded()
	r := ix.Query(Query{Text: "vol6", Dir: "/vol6/文档"})
	if r.Total != 1 || r.Items[0].Name != "note.txt" {
		t.Fatalf("目录过滤不正确: total=%d", r.Total)
	}
	r = ix.Query(Query{Text: "movie", Limit: 1})
	if r.Total != 2 || len(r.Items) != 1 || !r.Truncated {
		t.Fatalf("分页不正确: total=%d items=%d truncated=%v", r.Total, len(r.Items), r.Truncated)
	}
}

// TestIndexDegraded 验证超过上限时置降级标记，查询返回空并由调用方回退。
func TestIndexDegraded(t *testing.T) {
	ix := New(1)
	ix.Load([]Entry{
		{ID: 1, Name: "a.txt", NameL: "a.txt", Path: "/a.txt", PathL: "/a.txt", Ext: "txt"},
		{ID: 2, Name: "b.txt", NameL: "b.txt", Path: "/b.txt", PathL: "/b.txt", Ext: "txt"},
	})
	if ix.Ready() {
		t.Fatal("超过上限的索引不应 Ready")
	}
	if st := ix.Status(); !st.Degraded || st.Entries != 0 {
		t.Fatalf("降级状态不正确: %+v", st)
	}
}

// TestQueryCursorPagingNoDup 回归 v3.20.0：keyset 分页在每种排序下都要不重不漏。
// 故意造大量同大小/同时间的行，如果没有按 ID 决出稳定次序，翻页就会漏行或重复。
func TestQueryCursorPagingNoDup(t *testing.T) {
	ix := New(0)
	entries := make([]Entry, 0, 25)
	for i := int64(1); i <= 25; i++ {
		name := "same.txt"
		if i%5 == 0 {
			name = "other.txt"
		}
		path := "/v/" + string(rune('a'+i%5)) + string(rune('a'+i))
		entries = append(entries, Entry{
			ID: i, Name: name, NameL: name, Path: path, PathL: path,
			Ext: "txt", Size: 100, Mtime: 1000, Volume: "/v",
		})
	}
	ix.Load(entries)

	for _, sortKey := range []string{"name", "path", "ext", "size", "mtime", "volume"} {
		for _, order := range []string{"asc", "desc"} {
			seen := map[int64]bool{}
			cursor := ""
			for pages := 0; pages < 50; pages++ {
				r := ix.Query(Query{Text: "txt", Sort: sortKey, Order: order, Limit: 4, Cursor: cursor})
				if r.Total != 25 {
					t.Fatalf("%s/%s 总数应为 25，实际 %d", sortKey, order, r.Total)
				}
				for _, it := range r.Items {
					if seen[it.ID] {
						t.Fatalf("%s/%s 第 %d 页出现重复行 ID=%d", sortKey, order, pages, it.ID)
					}
					seen[it.ID] = true
				}
				if r.NextCursor == "" {
					break
				}
				cursor = r.NextCursor
			}
			if len(seen) != 25 {
				t.Fatalf("%s/%s 翻页后只见到 %d 行，应为 25", sortKey, order, len(seen))
			}
		}
	}
}

// TestQueryCountOnlyAndExclude 验证 count_only 不组装明细、排除路径参与总数口径。
func TestQueryCountOnlyAndExclude(t *testing.T) {
	ix := newLoaded()
	r := ix.Query(Query{Text: "movie", CountOnly: true})
	if r.Total != 2 || len(r.Items) != 0 || r.NextCursor != "" {
		t.Fatalf("count_only 应只回总数：total=%d items=%d cursor=%q", r.Total, len(r.Items), r.NextCursor)
	}
	r = ix.Query(Query{Text: "movie", ExcludePaths: map[string]bool{"/vol6/资源/电影/movie_extra.mp4": true}})
	if r.Total != 1 || len(r.Items) != 1 || r.Items[0].Name != "Movie.2024.mkv" {
		t.Fatalf("排除路径后应只剩 1 行：total=%d items=%v", r.Total, names(r.Items))
	}
}
