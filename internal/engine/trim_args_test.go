// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"testing"
)

// TestTrimArgsSeekPlacement 锁定两种模式的定位参数位置：
// fast 用输入 seek（-ss 在 -i 之前）走流复制；precise 用输出 seek（-ss 在 -i 之后）帧级精确。
// v1.42.0 的 BUG-05 根因是流复制在长 GOP 上无法剪短，修复依赖精确模式回退，
// 这里同时防止后续误把 fast 的 -ss 挪到输出侧。
func TestTrimArgsSeekPlacement(t *testing.T) {
	caseOf := func(mode, want string) {
		t.Helper()
		args := trimArgs(mode, 5, "in.mp4", "out.mp4")
		iIdx, ssIdx := -1, -1
		for i, a := range args {
			switch a {
			case "-i":
				iIdx = i
			case "-ss":
				ssIdx = i
			}
		}
		if iIdx < 0 || ssIdx < 0 {
			t.Fatalf("%s 参数缺少 -i 或 -ss：%v", mode, args)
		}
		if want == "before" && ssIdx > iIdx {
			t.Fatalf("fast 的 -ss 应在 -i 之前，实际参数：%v", args)
		}
		if want == "after" && ssIdx < iIdx {
			t.Fatalf("precise 的 -ss 应在 -i 之后，实际参数：%v", args)
		}
	}
	caseOf("fast", "before")
	caseOf("precise", "after")
}
