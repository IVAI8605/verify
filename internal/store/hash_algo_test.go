// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"database/sql"
	"testing"
)

// TestEnsureHashAlgoResetsOnce 验证算法版本变化时只清一次，并保留后续扫描的重算机会。
func TestEnsureHashAlgoResetsOnce(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	if _, err := s.DB().Exec(`INSERT INTO files(path,name,size,quick,hash,status) VALUES('/a.txt','a.txt',10,'q','h','active')`); err != nil {
		t.Fatal(err)
	}

	reset, err := s.EnsureHashAlgo(context.Background(), "blake3-q4k-v1")
	if err != nil {
		t.Fatal(err)
	}
	if !reset {
		t.Fatal("首次切换算法应报告已清空旧哈希")
	}
	var quick, hash sql.NullString
	if err := s.DB().QueryRow(`SELECT quick,hash FROM files WHERE path='/a.txt'`).Scan(&quick, &hash); err != nil {
		t.Fatal(err)
	}
	if quick.Valid || hash.Valid {
		t.Fatalf("旧预哈希/完整哈希应被清空: quick=%v hash=%v", quick, hash)
	}

	reset, err = s.EnsureHashAlgo(context.Background(), "blake3-q4k-v1")
	if err != nil {
		t.Fatal(err)
	}
	if reset {
		t.Fatal("版本未变化时不应重复清空")
	}
}
