// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

// 本文件是 v3.17.6 的数据一致性回归（F-23 / F-24）：删索引行时，
// 挂在 file_id 上的标签与按路径存的探测缓存不能留下孤儿。

import (
	"context"
	"testing"
)

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeleteRemovesDerivedRows(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	if _, err := s.DB().Exec(`INSERT INTO files(path,name,size,last_seen,status) VALUES
		('/v/a.bin','a.bin',1,1,'active'),
		('/v/b.bin','b.bin',1,1,'active')`); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.DB().QueryRow(`SELECT id FROM files WHERE path='/v/a.bin'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO file_tags(file_id,tag_id,value) VALUES(?,1,'x')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO probe_cache(path,status) VALUES('/v/a.bin','ok'),('/v/b.bin','ok')`); err != nil {
		t.Fatal(err)
	}

	// 单条删除：标签与探测缓存同事务清掉
	if err := s.DeleteByPath(ctx, "/v/a.bin"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM file_tags`); n != 0 {
		t.Fatalf("删除文件后 file_tags 应无残留，实际 %d", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM probe_cache WHERE path='/v/a.bin'`); n != 0 {
		t.Fatalf("删除文件后 probe_cache 应无残留，实际 %d", n)
	}

	// 扫描代次清理：同样不能留下探测缓存
	if _, err := s.DeleteMissing(ctx, 3); err != nil { // last_seen=1 < 3-1
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM probe_cache WHERE path='/v/b.bin'`); n != 0 {
		t.Fatalf("DeleteMissing 后 probe_cache 应无残留，实际 %d", n)
	}
}
