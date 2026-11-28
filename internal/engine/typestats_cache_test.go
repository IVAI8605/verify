package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTypeStatsCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	types := []TypeStat{{Ext: "mp4", Files: 3, Bytes: 100}, {Ext: "jpg", Files: 2, Bytes: 40}}
	at, err := saveTypeStats(dir, types)
	if err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if at <= 0 {
		t.Fatalf("时间戳无效：%d", at)
	}
	got, gotAt, err := loadTypeStats(dir)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if gotAt != at || len(got) != len(types) || got[0].Ext != "mp4" || got[0].Files != 3 {
		t.Fatalf("回读不一致：at=%d types=%+v", gotAt, got)
	}
	if info, err := os.Stat(typeStatsPath(dir)); err != nil {
		t.Fatalf("缓存文件不存在：%v", err)
	} else if info.Mode().Perm()&0o077 != 0 {
		// 部分文件系统（NAS 上的非 POSIX 卷）会把 0600 报成 0700，只要求组/其他人不可读写。
		t.Fatalf("缓存不应允许组或其他用户访问，实际 %o", info.Mode().Perm())
	}
}

func TestTypeStatsCacheIndexOnly(t *testing.T) {
	dir := t.TempDir()
	types := []TypeStat{{Ext: "jpg", Files: 1, Bytes: 5}}
	if at, err := writeTypeStats(dir, types, 0); err != nil || at != 0 {
		t.Fatalf("索引来源不应带时间：at=%d err=%v", at, err)
	}
	got, gotAt, err := loadTypeStats(dir)
	if err != nil || gotAt != 0 || len(got) != 1 || got[0].Ext != "jpg" {
		t.Fatalf("索引来源回读失败：types=%+v at=%d err=%v", got, gotAt, err)
	}
}

func TestTypeStatsCacheMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	if types, at, err := loadTypeStats(dir); err != nil || len(types) != 0 || at != 0 {
		t.Fatalf("无缓存时应返回空，实际 types=%+v at=%d err=%v", types, at, err)
	}
	if err := os.WriteFile(typeStatsPath(dir), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadTypeStats(dir); err == nil {
		t.Fatal("损坏缓存应返回错误")
	}
	if err := os.Remove(typeStatsPath(dir)); err != nil {
		t.Fatal(err)
	}
	if at, err := saveTypeStats(dir, nil); err != nil || at != 0 {
		t.Fatalf("空类型不应写盘：at=%d err=%v", at, err)
	}
	if _, err := os.Stat(filepath.Join(dir, typeStatsFileName)); !os.IsNotExist(err) {
		t.Fatalf("空类型不应创建缓存文件：%v", err)
	}
}
