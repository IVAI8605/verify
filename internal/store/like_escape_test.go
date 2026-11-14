// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

// 本文件是 v3.17.4 的数据层回归：LIKE 通配符转义（F-12）与 dataVersion 递增（F-05）。

import (
	"context"
	"testing"
)

// TestSearchFilesEscapesLike 回归 F-12：关键字里的 _ 与 % 按普通字符匹配。
func TestSearchFilesEscapesLike(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	if _, err := s.DB().Exec(`INSERT INTO files(path,name,size,status) VALUES
		('/v/a_b.txt','a_b.txt',1,'active'),
		('/v/axb.txt','axb.txt',1,'active'),
		('/v/100%.txt','100%.txt',1,'active')`); err != nil {
		t.Fatal(err)
	}
	rows, err := s.SearchFiles(ctx, "_", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "a_b.txt" {
		t.Fatalf("下划线应按字面匹配 a_b.txt，实际 %+v", rows)
	}
	rows, err = s.SearchFiles(ctx, "100%", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "100%.txt" {
		t.Fatalf("百分号应按字面匹配 100%%.txt，实际 %+v", rows)
	}
}

// TestOpLogKeywordEscapesLike 回归 F-12：操作记录关键词同样不能被当成 LIKE 模式。
func TestOpLogKeywordEscapesLike(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	for _, summary := range []string{"/vol1/a_b.mkv", "/vol1/axb.mkv"} {
		if _, err := s.DB().Exec(`INSERT INTO op_log(ts,action,summary,result) VALUES(1,'rename',?,'ok')`, summary); err != nil {
			t.Fatal(err)
		}
	}
	rows, total, err := s.ListOps(ctx, OpQuery{Keyword: "_", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("下划线应只命中 1 条，实际 total=%d rows=%d", total, len(rows))
	}
}

// TestDataVersionBumpsOnHashWrites 回归 F-05：写哈希后 dataVersion 必须递增，
// 否则搜索快照会一直判定为「还新鲜」，整轮扫描的结果都刷不出来。
func TestDataVersionBumpsOnHashWrites(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	if _, err := s.DB().Exec(`INSERT INTO files(path,name,size,mtime,quick,hash,status) VALUES('/v/a.bin','a.bin',1,0,'','','active')`); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.DB().QueryRow(`SELECT id FROM files WHERE path='/v/a.bin'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	v0 := s.DataVersion()
	if err := s.SetQuickBatch(ctx, []HashUpdate{{ID: id, Size: 1, Mtime: 0, Value: "q"}}); err != nil {
		t.Fatal(err)
	}
	if s.DataVersion() <= v0 {
		t.Fatal("批量写预哈希后 dataVersion 应递增")
	}
	v1 := s.DataVersion()
	if err := s.SetHash(ctx, id, "h"); err != nil {
		t.Fatal(err)
	}
	if s.DataVersion() <= v1 {
		t.Fatal("写全量哈希后 dataVersion 应递增")
	}
}
