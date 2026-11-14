// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import "testing"

// TestTranscodeCancelJobWaiting v3.10.8：排队中的任务可单独取消，且重复取消返回 false。
func TestTranscodeCancelJobWaiting(t *testing.T) {
	e := &Engine{}
	e.tq.tasks = []*TranscodeTask{
		{ID: 1, Status: "waiting", Path: "/vol1/a.mkv"},
		{ID: 2, Status: "waiting", Path: "/vol1/b.mkv"},
	}
	if !e.TranscodeCancelJob(1) {
		t.Fatal("排队中的任务应可取消")
	}
	if e.tq.tasks[0].Status != "cancelled" || e.tq.tasks[0].Error == "" {
		t.Fatalf("取消后状态不正确：%+v", e.tq.tasks[0])
	}
	if e.tq.tasks[1].Status != "waiting" {
		t.Fatal("取消一条不应影响其它任务")
	}
	if e.TranscodeCancelJob(1) {
		t.Fatal("已取消的任务再次取消应返回 false")
	}
	if e.TranscodeCancelJob(99) {
		t.Fatal("不存在的任务应返回 false")
	}
}
