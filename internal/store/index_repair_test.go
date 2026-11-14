// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// TestListFilePathsAfter 验证 keyset 分页按 id 升序推进，不会漏行也不会重复。
func TestListFilePathsAfter(t *testing.T) {
	s := openHealthStore(t)
	ctx := context.Background()
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := tx.Exec(`INSERT INTO files(path,name,size,status) VALUES(?,?,1,'active')`,
			fmt.Sprintf("/v/f%d.bin", i), fmt.Sprintf("f%d.bin", i)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var after int64
	var got []string
	for {
		batch, err := s.ListFilePathsAfter(ctx, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			break
		}
		for _, row := range batch {
			got = append(got, row.Path)
		}
		after = batch[len(batch)-1].ID
	}
	if len(got) != 5 {
		t.Fatalf("期望 5 条，实际 %d：%v", len(got), got)
	}
	for i, p := range got {
		if want := fmt.Sprintf("/v/f%d.bin", i); p != want {
			t.Fatalf("第 %d 条期望 %s，实际 %s", i, want, p)
		}
	}
}

// TestDeleteGhostPaths 验证幽灵行与健康结论同删，且跨分块（>400 条）时全部生效、
// 未列入的路径保持不变。
func TestDeleteGhostPaths(t *testing.T) {
	s := openHealthStore(t)
	ctx := context.Background()
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	ghosts := make([]string, 0, 405)
	for i := 0; i < 405; i++ {
		p := fmt.Sprintf("/v/ghost%03d.bin", i)
		ghosts = append(ghosts, p)
		if _, err := tx.Exec(`INSERT INTO files(path,name,size,status) VALUES(?,?,1,'active')`, p, fmt.Sprintf("ghost%03d.bin", i)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO files(path,name,size,status) VALUES('/v/keep.bin','keep.bin',1,'active')`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	// 幽灵路径各挂一条健康结论，验证 health_results 会跟着清掉。
	for _, p := range ghosts {
		if _, err := tx.Exec(`INSERT INTO health_results(kind,path,reason) VALUES('broken',?,'invalid_data')`, p); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO health_results(kind,path,reason) VALUES('broken','/v/keep.bin','ok')`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	n, err := s.DeleteGhostPaths(ctx, ghosts)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(ghosts)) {
		t.Fatalf("期望删除 %d 条，实际 %d", len(ghosts), n)
	}
	var files, health int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM files WHERE path LIKE '/v/ghost%'`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM health_results WHERE path LIKE '/v/ghost%'`).Scan(&health); err != nil {
		t.Fatal(err)
	}
	if files != 0 || health != 0 {
		t.Fatalf("幽灵行应全部清除，实际 files=%d health=%d", files, health)
	}
	var keepFiles, keepHealth int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM files WHERE path='/v/keep.bin'`).Scan(&keepFiles); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM health_results WHERE path='/v/keep.bin'`).Scan(&keepHealth); err != nil {
		t.Fatal(err)
	}
	if keepFiles != 1 || keepHealth != 1 {
		t.Fatalf("未列入的路径不应被删除，实际 files=%d health=%d", keepFiles, keepHealth)
	}
}

// TestDeleteGhostPathsToleratesHealthFailure 覆盖 v3.10.13 P2：
// 健康结论删不掉（例如表结构异常）时，索引行仍要正常清理，不能整批回滚，
// 否则用户看到「清理失败」但一条索引也没少。
func TestDeleteGhostPathsToleratesHealthFailure(t *testing.T) {
	s := openHealthStore(t)
	ctx := context.Background()
	paths := []string{"/v/a.bin", "/v/b.bin"}
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if _, err := tx.Exec(`INSERT INTO files(path,name,size,status) VALUES(?,?,1,'active')`, p, filepath.Base(p)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 制造「健康行删除必然失败」：直接删表，DELETE 会报 no such table。
	if _, err := s.DB().Exec(`DROP TABLE health_results`); err != nil {
		t.Fatal(err)
	}

	n, err := s.DeleteGhostPaths(ctx, paths)
	if err != nil {
		t.Fatalf("健康行删除失败不应阻断索引清理：%v", err)
	}
	if n != int64(len(paths)) {
		t.Fatalf("期望清理 %d 条索引行，实际 %d", len(paths), n)
	}
}
