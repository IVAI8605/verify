// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestPeerPackage 验证指纹包导入、匹配与删除。
func TestPeerPackage(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	rows := []PeerFile{
		{Path: "/vol6/a.mp4", Size: 100, Hash: "h1", Quick: "q1"},
		{Path: "/vol6/b.mp4", Size: 200, Hash: "h2"},
	}
	if err := s.SavePeerPackage(ctx, "NAS-B", rows); err != nil {
		t.Fatal(err)
	}
	peers, err := s.ListSyncPeers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].Machine != "NAS-B" || peers[0].RowCount != 2 {
		t.Fatalf("ListSyncPeers = %+v", peers)
	}
	idx, err := s.LoadPeerIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.MatchPeer("/other.mp4", 1, "h1"); got != "NAS-B" {
		t.Fatalf("按 hash 匹配 = %q，want NAS-B", got)
	}
	if got := idx.MatchPeer("/vol6/b.mp4", 200, ""); got != "NAS-B" {
		t.Fatalf("按 path+size 匹配 = %q，want NAS-B", got)
	}
	if got := idx.MatchPeer("/vol6/c.mp4", 300, ""); got != "" {
		t.Fatalf("不应匹配，得到 %q", got)
	}
	// 再次导入同一机器应整体替换，而不是叠加。
	if err := s.SavePeerPackage(ctx, "NAS-B", rows[:1]); err != nil {
		t.Fatal(err)
	}
	peers, _ = s.ListSyncPeers(ctx)
	if peers[0].RowCount != 1 {
		t.Fatalf("重复导入后 row_count = %d，want 1", peers[0].RowCount)
	}
	if err := s.DeleteSyncPeer(ctx, "NAS-B"); err != nil {
		t.Fatal(err)
	}
	if peers, _ := s.ListSyncPeers(ctx); len(peers) != 0 {
		t.Fatalf("删除后仍有 %d 台", len(peers))
	}
}
