// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

//go:build !linux

package engine

// renameNoOverwrite 在非 Linux 平台回退到 Lstat + rename（v3.22.0）。
// 目标平台是 linux/amd64，这里主要为本机编译与测试保留实现。
func renameNoOverwrite(oldPath, newPath string) error {
	return renameNoOverwriteFallback(oldPath, newPath)
}
