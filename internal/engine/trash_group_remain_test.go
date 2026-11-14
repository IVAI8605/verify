// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"verify/internal/store"
)

// insertGroupRow 直接写一行 files 记录：分组校验只读 path/name/hash，
// 不需要跑完整扫描链路，避免测试被扫描逻辑牵连。
func insertGroupRow(t *testing.T, e *Engine, path, name, hash string) {
	t.Helper()
	_, err := e.st.DB().ExecContext(context.Background(), `
INSERT INTO files(path,volume,name,size,mtime,ctime,nlink,inode,ext,hash,last_seen,status)
VALUES(?,?,?,?,0,0,0,1,'',?,0,'active')`,
		path, "/vol1", name, 1, hash)
	if err != nil {
		t.Fatalf("插入测试记录失败：%v", err)
	}
}

// TestKeepOneInGroup 覆盖「至少保留一个」的判定本身：
// 空成员（分组已不存在）也必须拒绝，不能退化成放行。
func TestKeepOneInGroup(t *testing.T) {
	members := []string{"/vol1/a/1.mkv", "/vol1/b/1.mkv"}
	cases := []struct {
		name     string
		members  []string
		selected []string
		wantErr  bool
	}{
		{"保留一个即通过", members, []string{members[1]}, false},
		{"整组清空应拒绝", members, members, true},
		{"空分组应拒绝", nil, []string{"/vol1/a/1.mkv"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := KeepOneInGroup(c.members, c.selected)
			if c.wantErr != (err != nil) {
				t.Fatalf("期望出错=%v，实际 err=%v", c.wantErr, err)
			}
		})
	}
}

// TestCheckKeepOneByKey 覆盖 P1-1/P1-2 回归：旧实现只认 h:/n:，
// 其余键一律落到视频分支导致校验形同虚设；现在按前缀取成员，认不出的键直接拒绝；
// 跨分组勾选时逐组校验，任一组被清空就拒绝整批。
func TestCheckKeepOneByKey(t *testing.T) {
	e, _ := newHealthTestEngine(t)
	ctx := context.Background()
	insertGroupRow(t, e, "/vol1/a/1.mkv", "1.mkv", "hash-1")
	insertGroupRow(t, e, "/vol1/b/1.mkv", "1.mkv", "hash-1")
	insertGroupRow(t, e, "/vol1/c/2.mkv", "2.mkv", "hash-2")

	cases := []struct {
		name    string
		keys    []string
		paths   []string
		wantErr string // 空串表示应通过
	}{
		{"精确组保留一个", []string{"h:hash-1"}, []string{"/vol1/a/1.mkv"}, ""},
		{"精确组整组清空", []string{"h:hash-1"}, []string{"/vol1/a/1.mkv", "/vol1/b/1.mkv"}, "至少保留一个"},
		{"同名组保留一个", []string{"n:1.mkv"}, []string{"/vol1/b/1.mkv"}, ""},
		{"同名组整组清空", []string{"n:1.mkv"}, []string{"/vol1/a/1.mkv", "/vol1/b/1.mkv"}, "至少保留一个"},
		{"分组已不存在", []string{"h:hash-gone"}, []string{"/vol1/a/1.mkv"}, "未找到该分组的成员"},
		{"无法识别的键", []string{"weird:1"}, []string{"/vol1/a/1.mkv"}, "无法识别的分组标识"},
		{"规则类键需快照", []string{"content:h\x1fhash-1"}, []string{"/vol1/a/1.mkv"}, "结果页快照"},
		{
			// P1-2：第一组仍留一个，第二组被清空，必须拦下来，不能只看第一个键
			"跨组勾选第二组被清空",
			[]string{"h:hash-1", "h:hash-2"},
			[]string{"/vol1/a/1.mkv", "/vol1/c/2.mkv"},
			"至少保留一个",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			groups := make([]TrashGroup, 0, len(c.keys))
			for _, k := range c.keys {
				groups = append(groups, TrashGroup{Key: k})
			}
			err := e.checkKeepOne(ctx, groups, c.paths)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("不应报错，实际 %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("期望包含 %q 的错误，实际 %v", c.wantErr, err)
			}
		})
	}
}

// TestCheckKeepOneImportGroup 导入会话的隔离入口也要能按 import:{会话}:{组号} 取到成员。
// 只算本机已匹配的行：未匹配的行磁盘上没有文件，不能充当「保留的那一份」。
func TestCheckKeepOneImportGroup(t *testing.T) {
	e, _ := newHealthTestEngine(t)
	ctx := context.Background()
	insertGroupRow(t, e, "/vol1/a/1.mkv", "1.mkv", "hash-1")
	insertGroupRow(t, e, "/vol1/b/1.mkv", "1.mkv", "hash-1")
	sess, err := e.st.CreateImportSession(ctx, "测试会话", "csv", []store.ImportRow{
		{GroupNo: 3, Path: "/vol1/a/1.mkv", Name: "1.mkv"},
		{GroupNo: 3, Path: "/vol1/b/1.mkv", Name: "1.mkv"},
		{GroupNo: 3, Path: "/vol1/gone/1.mkv", Name: "1.mkv"}, // 本机不存在，不计入成员
	})
	if err != nil {
		t.Fatalf("创建导入会话失败：%v", err)
	}
	key := fmt.Sprintf("import:%d:3", sess.ID)
	groups := []TrashGroup{{Key: key}}
	if err := e.checkKeepOne(ctx, groups, []string{"/vol1/a/1.mkv"}); err != nil {
		t.Fatalf("组内保留一份不应报错：%v", err)
	}
	if err := e.checkKeepOne(ctx, groups, []string{"/vol1/a/1.mkv", "/vol1/b/1.mkv"}); err == nil {
		t.Fatal("整组清空应被拒绝")
	}
	// 组号 0 是唯一/孤组，没有重复副本，不允许走分组校验
	uniqueKey := fmt.Sprintf("import:%d:0", sess.ID)
	if err := e.checkKeepOne(ctx, []TrashGroup{{Key: uniqueKey}}, []string{"/vol1/a/1.mkv"}); err == nil {
		t.Fatal("唯一/孤组不应作为重复组参与校验")
	}
}

// TestCheckKeepOneWithSnapshotMembers 结果页快照给的成员优先于分组键：
// 规则类键（引擎无法重算）也能校验，且不再走按前缀解析的分支。
func TestCheckKeepOneWithSnapshotMembers(t *testing.T) {
	e, _ := newHealthTestEngine(t)
	ctx := context.Background()
	insertGroupRow(t, e, "/vol1/a/1.mkv", "1.mkv", "hash-1")
	insertGroupRow(t, e, "/vol1/b/1.mkv", "1.mkv", "hash-1")
	members := []string{"/vol1/a/1.mkv", "/vol1/b/1.mkv"}
	group := []TrashGroup{{Key: "content:h\x1fhash-1", Members: members}}

	if err := e.checkKeepOne(ctx, group, members[:1]); err != nil {
		t.Fatalf("保留一个不应报错：%v", err)
	}
	if err := e.checkKeepOne(ctx, group, members); err == nil {
		t.Fatal("整组清空应被拒绝")
	}
}

// TestCheckKeepOneStaleSnapshotMembers 覆盖第五轮 P1-4：
// 快照里 a 已被隔离（索引里不再是 active），此时再勾 b 仍必须被拦下，
// 否则同组最后一份真实副本也会被移走。
func TestCheckKeepOneStaleSnapshotMembers(t *testing.T) {
	e, _ := newHealthTestEngine(t)
	ctx := context.Background()
	insertGroupRow(t, e, "/vol1/b/1.mkv", "1.mkv", "hash-1")
	// a 只存在于旧快照，索引里没有它
	group := []TrashGroup{{Key: "content:h\x1fhash-1", Members: []string{"/vol1/a/1.mkv", "/vol1/b/1.mkv"}}}

	err := e.checkKeepOne(ctx, group, []string{"/vol1/b/1.mkv"})
	if err == nil || !strings.Contains(err.Error(), "至少保留一个") {
		t.Fatalf("陈旧成员不应被当作保留项，实际 err=%v", err)
	}
}

// TestTrashFilesSerializedKeepOne 覆盖第五轮 P1-5：两个请求同时各勾同组一个文件，
// 写锁让「校验 + 移动」串行，后到的请求必须被 keep-one 拦下，不能两份都进隔离区。
func TestTrashFilesSerializedKeepOne(t *testing.T) {
	root := t.TempDir()
	useScanRoot(t, root)
	e, st := newHealthTestEngine(t)
	ctx := context.Background()

	a := filepath.Join(root, "a.txt")
	b := filepath.Join(root, "b.txt")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpsertOne(ctx, store.FileInfo{
			Path: p, Volume: root, Name: filepath.Base(p), Size: info.Size(),
			Mtime: info.ModTime().UnixNano(), Ext: "txt",
		}, 1); err != nil {
			t.Fatal(err)
		}
	}

	group := []TrashGroup{{Key: "content:h\x1fhash-1", Members: []string{a, b}}}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	passed := 0
	for _, sel := range [][]string{{a}, {b}} {
		wg.Add(1)
		go func(sel []string) {
			defer wg.Done()
			<-start
			if _, err := e.TrashFiles(ctx, sel, group); err == nil {
				mu.Lock()
				passed++
				mu.Unlock()
			}
		}(sel)
	}
	close(start)
	wg.Wait()

	if passed != 1 {
		t.Fatalf("同组两份只能移走一份，实际通过 %d 个请求", passed)
	}
	// 恰好一份留在原位置：两份都在说明没隔离成功，两份都不在说明 keep-one 被绕过。
	_, errA := os.Stat(a)
	_, errB := os.Stat(b)
	if (errA == nil) == (errB == nil) {
		t.Fatalf("应恰好保留一份，实际 a 存在=%v b 存在=%v", errA == nil, errB == nil)
	}
}
