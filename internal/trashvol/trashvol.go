// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package trashvol 判定隔离区记录所在卷是否可信。
//
// 抽成独立包的原因：这段判定原来写在 engine.HealTrashRows 里，而 engine 依赖
// Linux 专有的 syscall.Stat_t，在 Windows 上无法编译、也就无法单测。隔离记录被误删
// 是不可逆的恢复线索丢失，必须有能直接跑的正反例测试（v3.25.1）。
package trashvol

import (
	"os"
	"path/filepath"
	"strings"
)

// VolumeOf 返回 path 所属的最长卷前缀；路径不在任何卷内时返回空串。
// 按路径段边界匹配，避免 /vol1 误吞 /vol10 这类前缀相似的卷。
func VolumeOf(path string, vols []string) string {
	best := ""
	for _, v := range vols {
		if path == v || strings.HasPrefix(path, v+string(os.PathSeparator)) {
			if len(v) > len(best) {
				best = v
			}
		}
	}
	return best
}

// VolumeTrusted 报告卷当前是否可信。
//
// 「卷根 Stat 成功」不等于「卷真的挂载着」：飞牛存储池或外接盘卸载后，挂载点常残留
// 空目录，此时隔离文件全部 Lstat 不到。隔离目录是应用建在卷根下的，卷真正挂载时它
// 必然存在；不存在就判定为不可信，宁可多留「已丢失」条目也不误删恢复线索。
func VolumeTrusted(vol string) bool {
	if vol == "" {
		return false
	}
	fi, err := os.Stat(vol)
	if err != nil || !fi.IsDir() {
		return false
	}
	td, err := os.Stat(filepath.Join(vol, ".verify-trash"))
	return err == nil && td.IsDir()
}

// RowRemovable 报告一条隔离记录能否安全删除。
//
// 只有同时满足四点才返回 true：隔离文件确实不存在、能定位到所属卷、该卷可信、
// 且隔离文件所在的批次目录仍然存在。其它任何情况（文件还在、Stat 报非 ENOENT、
// 路径不在卷内、卷不可信、批次目录整体消失）一律保留。
func RowRemovable(trashPath string, vols []string) bool {
	if trashPath == "" {
		return false
	}
	if _, err := os.Lstat(trashPath); err == nil || !os.IsNotExist(err) {
		return false
	}
	vol := VolumeOf(trashPath, vols)
	if vol == "" {
		return false
	}
	if !VolumeTrusted(vol) {
		return false
	}
	// v4.1.2：隔离目录是「.verify-trash/<日期>/<原相对路径>/<文件>」。日期目录
	// 就在用户可见的卷上，外部进程或用户可能把它改名/搬走（文件其实还在新目录里）。
	// 此时精确路径 Lstat 也是 ENOENT、卷根 .verify-trash 也还在，旧判定会误删整批记录，
	// 让文件变成「不在隔离区列表里」的孤儿，恢复线索永久丢失。
	// 文件被单独删掉时父目录仍在（正常幽灵行，照常清理）；父目录整体消失则一律保留。
	parent := filepath.Dir(trashPath)
	if fi, err := os.Stat(parent); err != nil || !fi.IsDir() {
		return false
	}
	return true
}
