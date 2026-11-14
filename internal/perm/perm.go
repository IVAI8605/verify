// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package perm 收敛应用自建文件与目录的权限。
//
// os.OpenFile / os.WriteFile 的 perm 参数只在**创建**时生效，os.MkdirAll 也不会
// 修改已经存在的目录。因此升级到收紧权限的版本后，旧安装上已有的日志、配置、
// 缓存目录都会保留原来的宽权限。启动阶段必须显式调一次 Tighten，否则修复对
// 存量用户等于零收益。
package perm

import "os"

// Tighten 在当前权限比 target 更宽时收紧到 target，并返回是否真的改过。
//
// 只收紧、不放松：当前权限若已经等于或严于 target（例如用户手工设成 0700），
// 则保持原样，避免把用户自己的加固动作覆盖掉。路径不存在或 chmod 失败都只是
// 返回 false，由调用方决定是否记录日志——权限收紧属于尽力而为的兜底动作，
// 不应让主流程失败。
func Tighten(path string, target os.FileMode) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	cur := info.Mode().Perm()
	if cur&^target == 0 {
		return false
	}
	return os.Chmod(path, target) == nil
}
