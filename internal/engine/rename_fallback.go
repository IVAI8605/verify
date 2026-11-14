// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"fmt"
	"os"
)

// renameNoOverwriteFallback 是 Lstat + os.Rename 的旧实现（v3.22.0 起作为回退）。
//
// 缺点：检查与 rename 之间存在微秒窗口，别的进程在这期间建了同名文件会被 os.Rename 覆盖。
// 支持 renameat2 的 Linux 文件系统走原子实现；FUSE / 老内核返回 ENOSYS 时退回这里。
func renameNoOverwriteFallback(oldPath, newPath string) error {
	oldInfo, err := os.Lstat(oldPath)
	if err != nil {
		// 源文件已不在，交给下面的 rename 走同一条失败提示。
		return err
	}
	if newInfo, lerr := os.Lstat(newPath); lerr == nil {
		// 同一个 inode（软链接目录里指到同一个文件）不算冲突。
		if !os.SameFile(oldInfo, newInfo) {
			return fmt.Errorf("目标文件已存在，已跳过以免覆盖")
		}
	}
	return os.Rename(oldPath, newPath)
}
