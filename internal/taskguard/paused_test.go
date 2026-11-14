// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package taskguard

import (
	"errors"
	"strings"
	"testing"
)

// TestConflictMessageMentionsPause 锁定暂停态下的拒绝措辞（v3.25.2 P3-7）。
// 暂停任务仍占独占槽是刻意设计（暂停不等于退出运行循环），
// 所以这里同时断言「暂停不释放槽」，防止以后有人为了提示好看把槽放掉。
func TestConflictMessageMentionsPause(t *testing.T) {
	g := New(nil)
	done := mustAcquire(t, g, Task{Kind: KindScan, Group: GroupIO})
	defer done("done")

	g.SetPaused(true)
	_, err := g.Acquire(Task{Kind: KindTranscode, Group: GroupFFmpeg})
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("应返回 ConflictError，实际 %v", err)
	}
	if !ce.Paused || !strings.Contains(err.Error(), "已暂停") {
		t.Fatalf("暂停态下应提示「已暂停」，实际 %q", err.Error())
	}
	if _, err := g.Acquire(Task{Kind: KindHealth, Group: GroupIO}); err == nil {
		t.Fatal("暂停不得释放独占槽")
	}

	g.SetPaused(false)
	_, err = g.Acquire(Task{Kind: KindTranscode, Group: GroupFFmpeg})
	if err == nil {
		t.Fatal("恢复后仍应因扫描运行而拒绝")
	}
	if strings.Contains(err.Error(), "已暂停") {
		t.Fatalf("恢复后应回到「正在运行」措辞，实际 %q", err.Error())
	}
}
