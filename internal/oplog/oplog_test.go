// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package oplog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeDetailsEmpty(t *testing.T) {
	if got := EncodeDetails(nil, 200); got != "" {
		t.Fatalf("空明细应返回空串，实际 %q", got)
	}
}

func TestEncodeDetailsCountLimit(t *testing.T) {
	items := make([]Detail, 250)
	for i := range items {
		items[i] = Detail{Path: "/vol1/a/file.dat"}
	}
	got := EncodeDetails(items, 200)
	var p detailPayload
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatal(err)
	}
	if !p.Truncated || p.Total != 250 || len(p.Items) != 200 {
		t.Fatalf("期望 truncated=true total=250 items=200，实际 %+v", p)
	}
}

func TestEncodeDetailsByteCap(t *testing.T) {
	// 每条路径约 3KB，200 条远超 64KB 上限，必须按字节继续截。
	long := strings.Repeat("很长的文件名", 300)
	items := make([]Detail, 300)
	for i := range items {
		items[i] = Detail{Path: long}
	}
	got := EncodeDetails(items, 200)
	if len(got) > MaxDetailBytes {
		t.Fatalf("明细超过 %d 字节上限：%d", MaxDetailBytes, len(got))
	}
	var p detailPayload
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatal(err)
	}
	if !p.Truncated || p.Total != 300 || len(p.Items) == 0 {
		t.Fatalf("期望按字节截断且保留部分明细，实际 total=%d items=%d truncated=%v", p.Total, len(p.Items), p.Truncated)
	}
}

func TestContextRoundTrip(t *testing.T) {
	ctx := WithReqID(context.Background(), "req-123")
	ctx = WithActor(ctx, "10.0.0.7")
	e := New("trash_delete")
	e.FromContext(ctx)
	if e.ReqID != "req-123" || e.Actor != "10.0.0.7" {
		t.Fatalf("上下文未补齐：%+v", e)
	}
	if ReqID(context.Background()) != "" || Actor(nil) != "" {
		t.Fatal("空上下文不应返回请求 ID 或来源")
	}
	if len(NewReqID()) != 16 {
		t.Fatal("请求 ID 应为 16 位十六进制")
	}
}

func TestSafePathAndClientIP(t *testing.T) {
	if got := SafePath("/api/events?token=secret"); got != "/api/events" {
		t.Fatalf("查询串未去掉：%q", got)
	}
	if got := SafePath("/api/oplog"); got != "/api/oplog" {
		t.Fatalf("无查询串时被误改：%q", got)
	}
	if got := ClientIP("10.0.0.7:54321"); got != "10.0.0.7" {
		t.Fatalf("IP 解析错误：%q", got)
	}
	if got := ClientIP("bad-addr"); got != "bad-addr" {
		t.Fatalf("无法解析时应原样返回：%q", got)
	}
}

func TestSetFailure(t *testing.T) {
	e := New("trash_delete")
	e.SetFailure([]string{"权限不足", "权限不足", "磁盘错误", "另一个错误"})
	if e.Result != ResultPartial || !strings.Contains(e.Error, "权限不足") || strings.Count(e.Error, "；") != 2 {
		t.Fatalf("失败原因去重/截断不符：%+v", e)
	}
	ok := New("login_ok")
	ok.SetFailure(nil)
	if ok.Result != ResultOK || ok.Error != "" {
		t.Fatalf("无失败不应改状态：%+v", ok)
	}
}
