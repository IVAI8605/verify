// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

//go:build linux && amd64

package engine

// sysRenameat2 是 amd64 的 renameat2 系统调用号（Linux UAPI，未在 Go syscall 包导出）。
const sysRenameat2 = 316
