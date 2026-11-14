// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestFilesNeedingQuickCursor 回归 v3.10.3：候选查询要按 path 续扫，
// 每批只返回 after 之后的数据，不能重复也不能漏。
func TestFilesNeedingQuickCursor(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	for _, p := range []string{"/v/a.bin", "/v/b.bin", "/v/c.bin", "/v/d.bin", "/v/e.bin"} {
		if _, err := s.UpsertOne(ctx, FileInfo{Path: p, Volume: "/v", Name: p, Size: 10}, 1); err != nil {
			t.Fatal(err)
		}
	}

	after := ""
	var got []string
	for {
		rows, err := s.FilesNeedingQuick(ctx, 1, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			got = append(got, r.Path)
		}
		after = rows[len(rows)-1].Path
	}
	if len(got) != 5 {
		t.Fatalf("续扫应覆盖 5 个文件，实际 %d：%v", len(got), got)
	}
	for i, want := range []string{"/v/a.bin", "/v/b.bin", "/v/c.bin", "/v/d.bin", "/v/e.bin"} {
		if got[i] != want {
			t.Fatalf("第 %d 个应为 %s，实际 %s（顺序或重复错误）", i, want, got[i])
		}
	}
}

// TestRefreshDupQuick 回归 v3.10.3：全量哈希候选改为读物化表后，
// 只有预哈希重复且缺完整哈希的文件应被返回，ERR 和单例不入表。
func TestRefreshDupQuick(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	ids := map[string]int64{}
	for _, p := range []string{"/v/a.bin", "/v/b.bin", "/v/solo.bin", "/v/x.bin", "/v/y.bin"} {
		id, err := s.UpsertOne(ctx, FileInfo{Path: p, Volume: "/v", Name: p, Size: 10}, 1)
		if err != nil {
			t.Fatal(err)
		}
		ids[p] = id
	}
	for p, quick := range map[string]string{
		"/v/a.bin": "h1", "/v/b.bin": "h1", "/v/solo.bin": "h2", "/v/x.bin": "ERR", "/v/y.bin": "ERR",
	} {
		if err := s.SetQuick(ctx, ids[p], quick); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.RefreshDupQuick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("只有 h1 一个重复预哈希组，实际 %d", n)
	}
	if total, err := s.CountNeedingFull(ctx, 1); err != nil || total != 2 {
		t.Fatalf("全量哈希候选应为 2 个，实际 %d（err=%v）", total, err)
	}

	first, err := s.FilesNeedingFullHash(ctx, 1, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Path != "/v/a.bin" {
		t.Fatalf("首批应返回 /v/a.bin，实际 %+v", first)
	}
	second, err := s.FilesNeedingFullHash(ctx, 1, first[0].Path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Path != "/v/b.bin" {
		t.Fatalf("续扫应返回 /v/b.bin，实际 %+v", second)
	}
	if rest, _ := s.FilesNeedingFullHash(ctx, 1, second[0].Path, 1); len(rest) != 0 {
		t.Fatalf("续扫结束后不应再有候选，实际 %+v", rest)
	}
}

// TestMigrateClearsHashErr 回归 v3.10.3：旧库里的 'ERR' 要一次性还原成 NULL，
// 否则失败文件永远不会再次进入候选。迁移只做一次，重复执行保持幂等。
func TestMigrateClearsHashErr(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	id, err := s.UpsertOne(ctx, FileInfo{Path: "/v/bad.bin", Volume: "/v", Name: "bad.bin", Size: 10}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE files SET quick='ERR', hash='ERR' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	// 模拟旧库：没有清理标记，让 migrate 再跑一次。
	if _, err := s.DB().Exec(`DELETE FROM meta WHERE key='hash_err_cleanup'`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, s.DB()); err != nil {
		t.Fatal(err)
	}

	var quick, hash any
	if err := s.DB().QueryRow(`SELECT quick, hash FROM files WHERE id=?`, id).Scan(&quick, &hash); err != nil {
		t.Fatal(err)
	}
	if quick != nil || hash != nil {
		t.Fatalf("ERR 应被还原成 NULL，实际 quick=%v hash=%v", quick, hash)
	}

	// 再跑一次不应报错（幂等）。
	if err := migrate(ctx, s.DB()); err != nil {
		t.Fatalf("重复迁移应幂等：%v", err)
	}
}
