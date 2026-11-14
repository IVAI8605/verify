// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// seedTrashRows 按给定 (id, deleted_at) 插入隔离记录，用于分页用例。
func seedTrashRows(t *testing.T, s *Store, rows [][2]int64) {
	t.Helper()
	for _, r := range rows {
		if _, err := s.db.ExecContext(context.Background(),
			`INSERT INTO quarantine(id,orig_path,trash_path,volume,size,hash,deleted_at,reason)
			 VALUES(?,?,?,?,0,'',?,'duplicate')`,
			r[0], "/vol1/a/f.mkv", fmt.Sprintf("/vol1/@appdata/verify/.verify-trash/f-%d.mkv", r[0]), "/vol1", r[1]); err != nil {
			t.Fatalf("插入隔离行失败：%v", err)
		}
	}
}

// TestListTrashPageKeyset 覆盖 v3.25.2 的隔离区分页：
// deleted_at DESC 且同秒内按 id DESC，翻页不重不漏，游标指向本页最后一行。
func TestListTrashPageKeyset(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	seedTrashRows(t, s, [][2]int64{{1, 100}, {2, 200}, {3, 200}, {4, 300}, {5, 50}})

	page, err := s.ListTrashPage(ctx, "", 2)
	if err != nil {
		t.Fatalf("第一页失败：%v", err)
	}
	if len(page.Rows) != 2 || page.Rows[0].ID != 4 || page.Rows[1].ID != 3 {
		t.Fatalf("第一页顺序应为 [4 3]，实际 %v", idsOf(page.Rows))
	}
	if page.Total != 5 || !page.HasMore || page.NextCursor != "200:3" {
		t.Fatalf("第一页分页信息不对：total=%d has_more=%v cursor=%q", page.Total, page.HasMore, page.NextCursor)
	}

	page2, err := s.ListTrashPage(ctx, page.NextCursor, 2)
	if err != nil {
		t.Fatalf("第二页失败：%v", err)
	}
	if len(page2.Rows) != 2 || page2.Rows[0].ID != 2 || page2.Rows[1].ID != 1 {
		t.Fatalf("第二页顺序应为 [2 1]，实际 %v", idsOf(page2.Rows))
	}
	if !page2.HasMore || page2.NextCursor != "100:1" {
		t.Fatalf("第二页应还有一行：has_more=%v cursor=%q", page2.HasMore, page2.NextCursor)
	}

	page3, err := s.ListTrashPage(ctx, page2.NextCursor, 2)
	if err != nil {
		t.Fatalf("第三页失败：%v", err)
	}
	if len(page3.Rows) != 1 || page3.Rows[0].ID != 5 {
		t.Fatalf("第三页应为 [5]，实际 %v", idsOf(page3.Rows))
	}
	if page3.HasMore || page3.NextCursor != "" {
		t.Fatalf("第三页应已到底：has_more=%v cursor=%q", page3.HasMore, page3.NextCursor)
	}
}

// TestListTrashPageBadCursor 游标非法时返回可识别的错误，Web 层据此回 400。
func TestListTrashPageBadCursor(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	seedTrashRows(t, s, [][2]int64{{1, 100}})
	for _, c := range []string{"abc", "100", ":5", "x:5", "100:"} {
		if _, err := s.ListTrashPage(ctx, c, 10); !errors.Is(err, ErrTrashCursor) {
			t.Fatalf("游标 %q 应返回 ErrTrashCursor，实际 %v", c, err)
		}
	}
}

// TestListTrashPageLimitClamp limit 超上限时按 TrashPageMax 截断，而不是报错或全量返回。
func TestListTrashPageLimitClamp(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	seedTrashRows(t, s, [][2]int64{{1, 100}, {2, 200}})
	page, err := s.ListTrashPage(ctx, "", 9999)
	if err != nil {
		t.Fatalf("分页失败：%v", err)
	}
	if len(page.Rows) != 2 || page.HasMore {
		t.Fatalf("两行时应全返回且没有更多：len=%d has_more=%v", len(page.Rows), page.HasMore)
	}
}

// TestListTrashLegacyShape 老路径（不传分页参数）仍返回全部行，字段与顺序不变。
func TestListTrashLegacyShape(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	seedTrashRows(t, s, [][2]int64{{1, 100}, {2, 300}, {3, 200}})
	rows, err := s.ListTrash(ctx)
	if err != nil {
		t.Fatalf("ListTrash 失败：%v", err)
	}
	if len(rows) != 3 || rows[0].ID != 2 || rows[2].ID != 1 {
		t.Fatalf("老路径应返回全部 3 行并按 deleted_at DESC，实际 %v", idsOf(rows))
	}
}

// TestDeleteTrashRowsAtomic 删除失败时保持原状：上下文取消后一行都不能少。
// 这正是 heal 限批「失败不部分写入」所依赖的 store 层保证。
func TestDeleteTrashRowsAtomic(t *testing.T) {
	s := openHealthStore(t)
	seedTrashRows(t, s, [][2]int64{{1, 100}, {2, 200}, {3, 300}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.DeleteTrashRows(ctx, []int64{1, 2, 3}); err == nil {
		t.Fatal("上下文已取消，删除应当失败")
	}
	n, err := s.CountTrash(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("失败后应保留全部 3 行，实际 %d（err=%v）", n, err)
	}
}

func idsOf(rows []TrashRow) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}
