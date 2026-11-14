// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestMigrateAudioWindowClearsStaleFingerprints 改变音频比较时长后旧指纹必须失效，
// 且重复调用同档位不会反复清空。
func TestMigrateAudioWindowClearsStaleFingerprints(t *testing.T) {
	s := openHealthStore(t)
	ctx := context.Background()
	id, err := s.UpsertOne(ctx, FileInfo{
		Path: "/a/x.mp3", Volume: "/a", Name: "x.mp3", Ext: "mp3",
		Size: 10, Mtime: 1, Ctime: 1,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE files SET audio=? WHERE id=?`, []byte{1, 2, 3}, id); err != nil {
		t.Fatal(err)
	}

	if err := s.MigrateAudioWindow(ctx, 15); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM files WHERE audio IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("迁移后仍有 %d 个旧音频指纹", n)
	}
	var val string
	if err := s.DB().QueryRow(`SELECT value FROM meta WHERE key='audio_window_sec'`).Scan(&val); err != nil {
		t.Fatal(err)
	}
	if val != "15" {
		t.Fatalf("meta 档位 = %q，want 15", val)
	}

	// 同档位再调一次：指纹已空，不应产生错误。
	if err := s.MigrateAudioWindow(ctx, 15); err != nil {
		t.Fatal(err)
	}
}
