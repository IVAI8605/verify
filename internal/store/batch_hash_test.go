// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"database/sql"
	"testing"
)

// TestSetQuickBatchSkipsChangedFile 回归 v3.10.4 必修 1：
// 批量写哈希必须带 size/mtime 乐观条件。文件在「选中」到「落库」之间被改动时，
// 旧内容的哈希不能被写到新大小上，否则候选查询再也不会重算，可能据此误删。
func TestSetQuickBatchSkipsChangedFile(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	id, err := s.UpsertOne(ctx, FileInfo{
		Path: "/v/a.mp4", Volume: "/v", Name: "a.mp4", Ext: "mp4",
		Size: 100, Mtime: 10, Ctime: 10,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}

	// 文件被改动：UpsertBatch 会把 quick/hash 置 NULL。
	if _, err := s.UpsertOne(ctx, FileInfo{
		Path: "/v/a.mp4", Volume: "/v", Name: "a.mp4", Ext: "mp4",
		Size: 200, Mtime: 20, Ctime: 10,
	}, 2); err != nil {
		t.Fatal(err)
	}

	// 用改动前的大小/时间写旧哈希：条件不命中，不得落库。
	if err := s.SetQuickBatch(ctx, []HashUpdate{{ID: id, Size: 100, Mtime: 10, Value: "stale"}}); err != nil {
		t.Fatal(err)
	}
	if got := readQuick(t, s, id); got.Valid {
		t.Fatalf("文件大小已变，旧哈希不应落库，实际 quick=%q", got.String)
	}

	// 用当前大小/时间写入：正常落库。
	if err := s.SetQuickBatch(ctx, []HashUpdate{{ID: id, Size: 200, Mtime: 20, Value: "fresh"}}); err != nil {
		t.Fatal(err)
	}
	if got := readQuick(t, s, id); !got.Valid || got.String != "fresh" {
		t.Fatalf("当前大小/时间应能正常落库，实际 quick=%v", got)
	}
}

func readQuick(t *testing.T, s *Store, id int64) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := s.DB().QueryRow(`SELECT quick FROM files WHERE id=?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
