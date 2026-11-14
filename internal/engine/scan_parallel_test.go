// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDeviceGroupsMergesSameDevice 回归 v3.10.2：同一块盘上的多个挂载点必须合并成一组，
// 否则并行遍历会自己跟自己抢 IO；不同盘则各成一组。
func TestDeviceGroupsMergesSameDevice(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	for _, dir := range []string{a, b} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	groups := deviceGroups([]string{a, b})
	if len(groups) != 1 {
		t.Fatalf("同盘的两个目录应合并成 1 组，实际 %d 组：%+v", len(groups), groups)
	}
	if len(groups[0]) != 2 {
		t.Fatalf("合并后应保留两个目录，实际 %+v", groups[0])
	}
	if got := deviceGroupCount([]string{a, b}); got != 1 {
		t.Fatalf("deviceGroupCount 应为 1，实际 %d", got)
	}
}

// TestDeviceGroupsKeepsUnknownSeparate 验证拿不到设备号的根目录单独成组：
// 无法判断是否同盘时宁可串行，也不要冒险并行。
func TestDeviceGroupsKeepsUnknownSeparate(t *testing.T) {
	root := t.TempDir()
	groups := deviceGroups([]string{root, filepath.Join(root, "not-exist")})
	if len(groups) != 2 {
		t.Fatalf("不存在的根应单独成组，实际 %d 组：%+v", len(groups), groups)
	}
}

// TestDeviceGroupsSingleRoot 验证单根时不做多余分组。
func TestDeviceGroupsSingleRoot(t *testing.T) {
	root := t.TempDir()
	groups := deviceGroups([]string{root})
	if len(groups) != 1 || len(groups[0]) != 1 || groups[0][0] != root {
		t.Fatalf("单根应原样返回，实际 %+v", groups)
	}
}
