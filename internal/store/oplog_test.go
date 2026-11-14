// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"verify/internal/oplog"
)

func TestInsertAndListOps(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	e := oplog.New("trash_delete")
	e.Actor = "10.0.0.7"
	e.ReqID = "req-1"
	e.Summary = "永久删除隔离区文件 2 个"
	e.TargetCount = 2
	e.Details = []oplog.Detail{{Path: "/vol1/a.mkv", Size: 100}, {Path: "/vol1/b.mkv", Size: 200}}
	if err := s.InsertOp(ctx, e, oplog.DefaultDetailLimit); err != nil {
		t.Fatal(err)
	}
	rows, total, err := s.ListOps(ctx, OpQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("期望 1 条，实际 total=%d rows=%d", total, len(rows))
	}
	r := rows[0]
	if r.Action != "trash_delete" || r.Actor != "10.0.0.7" || r.ReqID != "req-1" || r.TargetCount != 2 {
		t.Fatalf("记录字段不符：%+v", r)
	}
	if !strings.Contains(r.DetailJSON, "/vol1/a.mkv") {
		t.Fatalf("明细未落库：%s", r.DetailJSON)
	}
}

// TestListOpsKeyset 验证 before_id 翻页（v3.10.11，第七轮 P2-3）：
// 游标只影响取哪些行，不影响 total；连续两页不重不漏。
func TestListOpsKeyset(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	for i := 0; i < 5; i++ {
		e := oplog.New("trash_delete")
		e.Summary = "第 " + strconv.Itoa(i+1) + " 条"
		if err := s.InsertOp(ctx, e, oplog.DefaultDetailLimit); err != nil {
			t.Fatal(err)
		}
	}
	first, total, err := s.ListOps(ctx, OpQuery{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(first) != 2 {
		t.Fatalf("首页期望 total=5 且 2 条，实际 total=%d rows=%d", total, len(first))
	}
	second, total2, err := s.ListOps(ctx, OpQuery{PageSize: 2, BeforeID: first[len(first)-1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if total2 != 5 {
		t.Fatalf("keyset 翻页不应改变 total，实际 %d", total2)
	}
	if len(second) != 2 || second[0].ID >= first[len(first)-1].ID {
		t.Fatalf("第二页应从游标继续往前取，实际 %+v", second)
	}
}

func TestOpLogMissingActionRejected(t *testing.T) {
	s := openHealthStore(t)
	if err := s.InsertOp(context.Background(), oplog.Entry{}, oplog.DefaultDetailLimit); err == nil {
		t.Fatal("缺少动作类型应报错")
	}
}

func TestOpLogDetailTruncated(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	e := oplog.New("trash_empty")
	e.TargetCount = 250
	for i := 0; i < 250; i++ {
		e.Details = append(e.Details, oplog.Detail{Path: "/vol1/f.dat"})
	}
	if err := s.InsertOp(ctx, e, oplog.DefaultDetailLimit); err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.ListOps(ctx, OpQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Items     []oplog.Detail `json:"items"`
		Total     int            `json:"total"`
		Truncated bool           `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(rows[0].DetailJSON), &p); err != nil {
		t.Fatal(err)
	}
	if !p.Truncated || p.Total != 250 || len(p.Items) != oplog.DefaultDetailLimit {
		t.Fatalf("批量操作明细未按上限截断：total=%d items=%d truncated=%v", p.Total, len(p.Items), p.Truncated)
	}
}

func TestOpLogFilterAndPaging(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	for i := 0; i < 5; i++ {
		e := oplog.New("login_ok")
		e.TS = time.Now().UnixMilli() + int64(i)
		if err := s.InsertOp(ctx, e, oplog.DefaultDetailLimit); err != nil {
			t.Fatal(err)
		}
	}
	e := oplog.New("trash_delete")
	e.Result = oplog.ResultFail
	e.Summary = "永久删除失败"
	if err := s.InsertOp(ctx, e, oplog.DefaultDetailLimit); err != nil {
		t.Fatal(err)
	}
	rows, total, err := s.ListOps(ctx, OpQuery{Action: "trash_delete", Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].Result != oplog.ResultFail {
		t.Fatalf("按动作筛选不符：total=%d rows=%+v", total, rows)
	}
	rows, total, err = s.ListOps(ctx, OpQuery{Keyword: "永久删除", Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("按关键词筛选不符：total=%d", total)
	}
	rows, total, err = s.ListOps(ctx, OpQuery{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 || len(rows) != 2 {
		t.Fatalf("分页不符：total=%d rows=%d", total, len(rows))
	}
}

// TestOpLogSurvivesTrashRowDeletion 是阶段 A 最关键的语义：
// 隔离区业务行被清掉后，审计行仍能回答「删过什么」。
func TestOpLogSurvivesTrashRowDeletion(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	if err := s.AddTrash(ctx, "/vol1/a.mkv", "/vol1/.verify-trash/2026-11-05/a.mkv",
		"/vol1", "hash-a", "", "duplicate", 100); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListTrash(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("隔离区应有 1 条：len=%d err=%v", len(items), err)
	}
	e := oplog.New("trash_delete")
	e.Summary = "永久删除隔离区文件 1 个"
	e.TargetCount = 1
	e.Details = []oplog.Detail{{Path: items[0].OrigPath, NewPath: items[0].TrashPath, Size: items[0].Size}}
	if err := s.InsertOp(ctx, e, oplog.DefaultDetailLimit); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTrashRows(ctx, []int64{items[0].ID}); err != nil {
		t.Fatal(err)
	}
	left, err := s.ListTrash(ctx)
	if err != nil || len(left) != 0 {
		t.Fatalf("隔离区业务行应已清空：len=%d err=%v", len(left), err)
	}
	rows, total, err := s.ListOps(ctx, OpQuery{Action: "trash_delete", Page: 1, PageSize: 10})
	if err != nil || total != 1 || !strings.Contains(rows[0].DetailJSON, "/vol1/a.mkv") {
		t.Fatalf("审计行应保留且含原路径：total=%d rows=%+v err=%v", total, rows, err)
	}
}

func TestPurgeOpsBefore(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)
	old := oplog.New("login_ok")
	old.TS = time.Now().Add(-48 * time.Hour).UnixMilli()
	if err := s.InsertOp(ctx, old, oplog.DefaultDetailLimit); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertOp(ctx, oplog.New("login_ok"), oplog.DefaultDetailLimit); err != nil {
		t.Fatal(err)
	}
	n, err := s.PurgeOpsBefore(ctx, time.Now().Add(-24*time.Hour).UnixMilli())
	if err != nil || n != 1 {
		t.Fatalf("应清理 1 条过期记录：n=%d err=%v", n, err)
	}
	_, total, err := s.ListOps(ctx, OpQuery{Page: 1, PageSize: 10})
	if err != nil || total != 1 {
		t.Fatalf("清理后应剩 1 条：total=%d err=%v", total, err)
	}
}
