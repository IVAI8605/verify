// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 回归 v4.0.1 数据安全检查 问题4：哈希落库前重新 Lstat 复核，
// 文件在「选中→落库」窗口内被改动（同大小、mtime 回拨）时不得写入旧哈希。

import (
	"os"
	"path/filepath"
	"testing"

	"verify/internal/store"
)

func TestHashSnapshotStillValid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	ct, ok := fileCtime(fi)
	if !ok {
		t.Skip("平台不提供 ctime，跳过 ctime 相关断言")
	}

	good := store.FileRow{Path: p, Size: fi.Size(), Mtime: fi.ModTime().UnixNano(), Ctime: ct}
	if !hashSnapshotStillValid(good) {
		t.Fatal("未改动的文件应通过复核")
	}

	// 大小变了：不得写入
	badSize := good
	badSize.Size = good.Size + 1
	if hashSnapshotStillValid(badSize) {
		t.Fatal("大小改变后不应通过复核")
	}

	// 同大小但 ctime 变了（内容被替换、mtime 被回拨的典型场景）：不得写入
	badCtime := good
	badCtime.Ctime = ct + 1
	if hashSnapshotStillValid(badCtime) {
		t.Fatal("ctime 改变后不应通过复核")
	}

	// 库中 ctime=0（旧行/未采集）而磁盘能给出真实 ctime：按「放弃乐观写入」判为不一致。
	zeroCtime := good
	zeroCtime.Ctime = 0
	if hashSnapshotStillValid(zeroCtime) {
		t.Fatal("库中 ctime=0 但磁盘有 ctime 时应放弃乐观写入")
	}

	// 文件已不存在：不得写入
	missing := good
	missing.Path = filepath.Join(dir, "nope.bin")
	if hashSnapshotStillValid(missing) {
		t.Fatal("文件不存在时不应通过复核")
	}
}
