// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"fmt"
	"testing"
)

// TestFilterActivePaths 覆盖第五轮 P1-4：陈旧快照成员必须被剔除，
// 只保留索引里仍为 active 的路径，且结果顺序与入参一致。
func TestFilterActivePaths(t *testing.T) {
	s := openHealthStore(t)
	ctx := context.Background()
	if _, err := s.DB().Exec(`
INSERT INTO files(path,name,size,status) VALUES
('/v/a.mkv','a.mkv',1,'active'),
('/v/b.mkv','b.mkv',1,'trashed'),
('/v/c.mkv','c.mkv',1,'active')`); err != nil {
		t.Fatal(err)
	}
	got, err := s.FilterActivePaths(ctx, []string{"/v/a.mkv", "/v/gone.mkv", "/v/b.mkv", "/v/c.mkv", "/v/a.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "/v/a.mkv" || got[1] != "/v/c.mkv" {
		t.Fatalf("期望 [/v/a.mkv /v/c.mkv]，实际 %v", got)
	}
}

// TestFilterActivePathsChunked 验证成员过多时按批查询仍能正确合并，
// 避免一次绑定参数超过 SQLite 变量上限。
func TestFilterActivePathsChunked(t *testing.T) {
	s := openHealthStore(t)
	ctx := context.Background()
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, 405)
	for i := 0; i < 405; i++ {
		p := fmt.Sprintf("/v/f%03d.bin", i)
		paths = append(paths, p)
		status := "active"
		if i == 402 {
			status = "trashed"
		}
		if _, err := tx.Exec(`INSERT INTO files(path,name,size,status) VALUES(?,?,1,?)`, p, fmt.Sprintf("f%03d.bin", i), status); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := s.FilterActivePaths(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 404 {
		t.Fatalf("期望 404 个存活路径，实际 %d", len(got))
	}
	for _, p := range got {
		if p == "/v/f402.bin" {
			t.Fatal("已隔离的路径不应出现在结果里")
		}
	}
}
