// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package taskguard

import "testing"

func mustAcquire(t *testing.T, g *Guard, tk Task) func(string) {
	t.Helper()
	done, err := g.Acquire(tk)
	if err != nil {
		t.Fatalf("Acquire(%s) 失败：%v", tk.Kind, err)
	}
	return done
}

// TestExclusiveGroupsConflict 验证两个独占组互斥，非独占不参与拒绝。
func TestExclusiveGroupsConflict(t *testing.T) {
	g := New(nil)
	doneScan := mustAcquire(t, g, Task{Kind: KindScan, Group: GroupIO})
	defer doneScan("done")
	if _, err := g.Acquire(Task{Kind: KindCrop, Group: GroupFFmpeg}); err == nil {
		t.Fatal("扫描运行时 crop 应被拒")
	}
	if _, err := g.Acquire(Task{Kind: KindHealth, Group: GroupIO}); err == nil {
		t.Fatal("同组任务应被拒")
	}
	doneRename := mustAcquire(t, g, Task{Kind: KindRename})
	doneRename("done")
	if len(g.Running()) != 1 {
		t.Fatalf("非独占任务结束后应只剩扫描，实际 %+v", g.Running())
	}
}

// TestSameGroupAfterRelease 验证释放后可以再次申请。
func TestSameGroupAfterRelease(t *testing.T) {
	g := New(nil)
	done := mustAcquire(t, g, Task{Kind: KindTranscode, Group: GroupFFmpeg})
	done("done")
	if _, ok := g.BusyGroup(); ok {
		t.Fatal("释放后不应再占用独占槽")
	}
	done2 := mustAcquire(t, g, Task{Kind: KindScan, Group: GroupIO})
	done2("done")
}

// TestDisabledGuardNeverRejects 验证开关关闭后完全放行，且不留下登记。
func TestDisabledGuardNeverRejects(t *testing.T) {
	g := New(func() bool { return false })
	d1 := mustAcquire(t, g, Task{Kind: KindScan, Group: GroupIO})
	d2 := mustAcquire(t, g, Task{Kind: KindTranscode, Group: GroupFFmpeg})
	d1("done")
	d2("done")
	if len(g.Running()) != 0 || len(g.History()) != 0 {
		t.Fatalf("关闭时不应登记：running=%v history=%v", g.Running(), g.History())
	}
}

// TestHistoryKeepsTen 验证历史只保留最近 10 条。
func TestHistoryKeepsTen(t *testing.T) {
	g := New(nil)
	for i := 0; i < 13; i++ {
		done := mustAcquire(t, g, Task{Kind: KindRename})
		done("done")
	}
	if n := len(g.History()); n != maxHistory {
		t.Fatalf("历史应保留 %d 条，实际 %d", maxHistory, n)
	}
}
