// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

// seedEqualGroups 插入 n 组「大小与数量完全一致」的重复文件：
// 每组 2 个文件、size 全为 1000，因此按 total_size / waste / count 排序时全部等值。
// 这是 P2-5 要防的场景——只有聚合列参与排序时，翻页先后不由 SQL 保证。
//
// 说明：当前查询计划借 GROUP BY 的索引扫描，等值行通常恰好有序，所以去掉次序键
// 也不一定立刻复现漂移；本组用例的作用是锁定「翻页不重不漏 + 顺序契约」，
// 防止将来换索引或查询计划后静默退化。
func seedEqualGroups(t *testing.T, s *Store, n int) []string {
	t.Helper()
	ctx := context.Background()
	hashes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		// 用固定宽度十六进制串当哈希，保证与真实 BLAKE3 同形且不命中 'ERR%' 过滤。
		h := fmt.Sprintf("%064x", i+1)
		hashes = append(hashes, h)
		for j := 0; j < 2; j++ {
			name := fmt.Sprintf("g%02d-%d.mkv", i, j)
			id, err := s.UpsertOne(ctx, FileInfo{
				Path:   fmt.Sprintf("/vol1/media/%s", name),
				Volume: "/vol1",
				Name:   name,
				Size:   1000,
				Mtime:  1000,
				Ctime:  1000,
				Ext:    "mkv",
			}, 1)
			if err != nil {
				t.Fatalf("插入文件失败：%v", err)
			}
			if err := s.SetHash(ctx, id, h); err != nil {
				t.Fatalf("写入哈希失败：%v", err)
			}
		}
	}
	return hashes
}

// TestExactGroupsPagingStableOnTies 覆盖 v3.25.2 只读排查 P2-5：
// 30 组等值分组（大小、数量、可回收空间全一样）按 total_size 翻页，
// 必须不重不漏，且整体顺序由 hash 次序键唯一确定。
func TestExactGroupsPagingStableOnTies(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	hashes := seedEqualGroups(t, s, 30)

	want := append([]string{}, hashes...)
	sort.Strings(want) // 次序键升序：与主排序（等值）无关，哈希字典序

	const pageSize = 7
	var got []string
	for page := 1; page <= 6; page++ {
		gs, total, err := s.ExactGroups(ctx, GroupQuery{
			Page: page, PageSize: pageSize, Sort: "total_size", Order: "desc",
		})
		if err != nil {
			t.Fatalf("第 %d 页查询失败：%v", page, err)
		}
		if page == 1 && total != int64(len(want)) {
			t.Fatalf("总数应为 %d，实际 %d", len(want), total)
		}
		if len(gs) == 0 {
			break
		}
		for _, g := range gs {
			got = append(got, g.Hash)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("翻页结果与次序键顺序不一致：\n got=%v\nwant=%v", got, want)
	}

	// 同一页重复请求必须给出同一批哈希；等值排序漂移会让翻页重复或漏行。
	again, _, err := s.ExactGroups(ctx, GroupQuery{
		Page: 2, PageSize: pageSize, Sort: "total_size", Order: "desc", SkipTotal: true,
	})
	if err != nil {
		t.Fatalf("重复请求第二页失败：%v", err)
	}
	gotAgain := make([]string, 0, len(again))
	for _, g := range again {
		gotAgain = append(gotAgain, g.Hash)
	}
	if !reflect.DeepEqual(gotAgain, want[pageSize:pageSize*2]) {
		t.Fatalf("同一页两次结果不一致：\n got=%v\nwant=%v", gotAgain, want[pageSize:pageSize*2])
	}
}

// TestNameGroupsPagingStableOnTies 与精确分组同一口径：
// 30 组同名、同大小、同数量的分组按 total_size 翻页，输出必须按 lower(name) 唯一有序。
func TestNameGroupsPagingStableOnTies(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	const groupCount = 30
	want := make([]string, 0, groupCount)
	for i := 0; i < groupCount; i++ {
		name := fmt.Sprintf("n%02d.mkv", i)
		want = append(want, "n:"+name)
		for j := 0; j < 2; j++ {
			if _, err := s.UpsertOne(ctx, FileInfo{
				Path:   fmt.Sprintf("/vol1/dir%d/%s", j, name),
				Volume: "/vol1",
				Name:   name,
				Size:   1000,
				Mtime:  1000,
				Ctime:  1000,
				Ext:    "mkv",
			}, 1); err != nil {
				t.Fatalf("插入文件失败：%v", err)
			}
		}
	}

	const pageSize = 7
	var got []string
	for page := 1; page <= 6; page++ {
		gs, total, err := s.NameGroups(ctx, GroupQuery{
			Page: page, PageSize: pageSize, Sort: "total_size", Order: "desc",
		})
		if err != nil {
			t.Fatalf("第 %d 页查询失败：%v", page, err)
		}
		if page == 1 && total != int64(groupCount) {
			t.Fatalf("总数应为 %d，实际 %d", groupCount, total)
		}
		if len(gs) == 0 {
			break
		}
		for _, g := range gs {
			got = append(got, g.Key)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("同名分组翻页顺序不对：\n got=%v\nwant=%v", got, want)
	}
}
