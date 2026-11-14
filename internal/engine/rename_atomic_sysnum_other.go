// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

//go:build linux && !amd64 && !arm64

package engine

// sysRenameat2 为 0 表示该架构未提供调用号，renameNoOverwrite 会直接走回退实现。
const sysRenameat2 = 0
