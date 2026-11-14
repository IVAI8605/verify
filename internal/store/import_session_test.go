// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestImportSessionLifecycle 会话创建、列表、按组读回、删除。
func TestImportSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	// 当前库里放两个文件，用于验证 matched 回填
	for _, p := range []string{"/vol1/media/a.mp4", "/vol1/media/b.mp4"} {
		if _, err := s.UpsertOne(ctx, FileInfo{Path: p, Name: base(p), Ext: "mp4", Size: 100}, 1); err != nil {
			t.Fatal(err)
		}
	}

	rows := []ImportRow{
		{GroupNo: 1, Path: "/vol1/media/a.mp4", Name: "a.mp4", Size: 100, Chk: true},
		{GroupNo: 1, Path: "/vol1/media/b.mp4", Name: "b.mp4", Size: 100, Chk: false},
		{GroupNo: 2, Path: "C:/Users/x/elsewhere.jpg", Name: "elsewhere.jpg", Size: 9, Chk: true},
		{GroupNo: 0, Path: "C:/Users/x/unique.jpg", Name: "unique.jpg", Size: 5},
	}
	sess, err := s.CreateImportSession(ctx, "测试导入", "dc5", rows)
	if err != nil {
		t.Fatal(err)
	}
	if sess.TotalRows != 4 || sess.GroupCount != 2 || sess.MatchedRows != 2 {
		t.Fatalf("会话统计不符: %+v", sess)
	}

	list, err := s.ListImportSessions(ctx)
	if err != nil || len(list) != 1 || list[0].ID != sess.ID {
		t.Fatalf("会话列表不符: %+v err=%v", list, err)
	}

	groups, total, err := s.ImportGroups(ctx, sess.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("分组总数应为 3，实际 %d", total)
	}
	if len(groups) != 3 {
		t.Fatalf("应聚合出 3 个组（含 groupno=0），实际 %d: %+v", len(groups), groups)
	}
	// 组号升序：0 在最前，1、2 随后
	if groups[0].GroupNo != 0 || groups[1].GroupNo != 1 || groups[2].GroupNo != 2 {
		t.Fatalf("组号排序不符: %d,%d,%d", groups[0].GroupNo, groups[1].GroupNo, groups[2].GroupNo)
	}
	if len(groups[1].Rows) != 2 {
		t.Fatalf("组 1 应有 2 行")
	}
	// matched 回填：本机两行 matched，C:\ 两行 unmatched
	if !groups[1].Rows[0].Matched || !groups[1].Rows[1].Matched {
		t.Fatalf("本机路径应 matched: %+v", groups[1].Rows)
	}
	if groups[2].Rows[0].Matched || groups[0].Rows[0].Matched {
		t.Fatalf("C:\\ 路径应 unmatched: %+v", groups[2].Rows)
	}

	// v3.10.8（P2-11）：按组分页，每页 2 组，第二页应只剩 1 组。
	p1, total1, err := s.ImportGroups(ctx, sess.ID, 1, 2)
	if err != nil || total1 != 3 || len(p1) != 2 {
		t.Fatalf("第 1 页应为 2 组、总数 3，实际 %d 组 total=%d err=%v", len(p1), total1, err)
	}
	p2, _, err := s.ImportGroups(ctx, sess.ID, 2, 2)
	if err != nil || len(p2) != 1 || p2[0].GroupNo != 2 {
		t.Fatalf("第 2 页应只剩组 2，实际 %+v err=%v", p2, err)
	}

	if err := s.DeleteImportSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	list, err = s.ListImportSessions(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("删除后列表应为空: %+v err=%v", list, err)
	}
	// 行也一并删除
	groups, _, err = s.ImportGroups(ctx, sess.ID, 0, 0)
	if err != nil || len(groups) != 0 {
		t.Fatalf("删除后行应为空: %+v err=%v", groups, err)
	}
	if err := s.DeleteImportSession(ctx, sess.ID); !errors.Is(err, ErrImportSessionNotFound) {
		t.Fatalf("删除不存在的会话应返回 ErrImportSessionNotFound，实际: %v", err)
	}
}

// TestImportSessionEmpty 拒绝空导入。
func TestImportSessionEmpty(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	if _, err := s.CreateImportSession(ctx, "空", "dc5", nil); err == nil || !strings.Contains(err.Error(), "为空") {
		t.Fatalf("空导入应被拒绝，实际: %v", err)
	}
}

func base(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
