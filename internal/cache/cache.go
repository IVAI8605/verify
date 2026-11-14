// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package cache 提供缓存基础设施的公共能力：
// 缓存目录介质检测（SSD/HDD 提示）与内存预算自动检测。
// 纯 Go 读取 /proc 与 /sys，零外部依赖，不使用 fnOS 私有 API（v1.31 引入）。
package cache

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 介质类型。
const (
	MediaSSD     = "ssd"
	MediaHDD     = "hdd"
	MediaUnknown = "unknown"
)

// DetectMedia 判断 dir 所在文件系统的介质类型。
// 依据：/proc/self/mountinfo 找到覆盖 dir 的最长挂载点 → 设备主:次号 →
// /sys/dev/block/<maj>:<min>/queue/rotational（0=SSD，1=HDD）。
// 非 Linux（开发机）或无法判定时返回 unknown，UI 不显示提示。
func DetectMedia(dir string) string {
	if dir == "" {
		return MediaUnknown
	}
	maj, min, ok := deviceOf(dir)
	if !ok {
		return MediaUnknown
	}
	if rot, ok := rotationalFor(maj, min); ok {
		if rot {
			return MediaHDD
		}
		return MediaSSD
	}
	return MediaUnknown
}

// FSType 返回覆盖 dir 的最长挂载点的文件系统类型（如 ext4、fuse.rclone）。
// 供授权探测使用：WebDAV/云存储等 fuse 挂载不受飞牛 trimacl 授权机制管辖，
// 全局可读（allow_other），"能打开"不代表"已授权"。
func FSType(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	b, err := os.ReadFile("/proc/self/mountinfo") // #nosec G304 -- 固定路径内核接口
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	bestLen := -1
	fstype := ""
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		// 行格式：<id> <parent> <maj:min> <mountpoint> <opts...> - <fstype> <source> <opts...>
		sep := -1
		for i, f := range fields {
			if f == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || len(fields) < sep+3 {
			continue
		}
		mp := unescapeMount(fields[4])
		if !within(abs, mp) {
			continue
		}
		if L := len(mp); L > bestLen {
			bestLen = L
			fstype = fields[sep+1]
		}
	}
	if bestLen < 0 || fstype == "" {
		return "", false
	}
	return fstype, true
}

// ManagedMountFS 判断挂载类型是否受飞牛 trimacl 授权机制管辖。
// trimafs/ext 系/btrfs/xfs 为本地盘挂载（挂载选项带 trimacl）；fuse 系
// （rclone WebDAV/云盘）、nfs/cifs/9p 等网络或用户态挂载全局可读，不受管辖。
func ManagedMountFS(dir string) bool {
	fs, ok := FSType(dir)
	if !ok {
		return false
	}
	switch {
	case strings.HasPrefix(fs, "fuse"), fs == "nfs", fs == "nfs4", fs == "cifs", fs == "smbfs", fs == "9p", fs == "ncpfs":
		return false
	}
	return true
}

// deviceOf 返回覆盖 dir 的最长挂载点的主:次设备号。
func deviceOf(dir string) (int, int, bool) {
	b, err := os.ReadFile("/proc/self/mountinfo") // #nosec G304 -- 固定路径内核接口
	if err != nil {
		return 0, 0, false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return 0, 0, false
	}
	bestLen := -1
	var maj, min int
	for _, line := range strings.Split(string(b), "\n") {
		// mountinfo 行：<id> <parent> <maj:min> <mountpoint> <opts...> - <fstype> <source> <opts...>
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		parts := strings.SplitN(fields[2], ":", 2)
		if len(parts) != 2 {
			continue
		}
		m, err1 := strconv.Atoi(parts[0])
		n, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			continue
		}
		mp := unescapeMount(fields[4])
		if !within(abs, mp) {
			continue
		}
		if L := len(mp); L > bestLen {
			bestLen, maj, min = L, m, n
		}
	}
	if bestLen < 0 {
		return 0, 0, false
	}
	return maj, min, true
}

// rotationalFor 读取设备的 rotational 标志；dm/crypt 等无 queue 属性时
// 尝试其 slaves（底层物理设备）。
func rotationalFor(maj, min int) (rot bool, ok bool) {
	base := "/sys/dev/block/" + strconv.Itoa(maj) + ":" + strconv.Itoa(min)
	if b, err := os.ReadFile(base + "/queue/rotational"); err == nil { // #nosec G304 -- 固定路径内核接口
		v := strings.TrimSpace(string(b))
		if v == "0" {
			return false, true
		}
		if v == "1" {
			return true, true
		}
		return false, false
	}
	// dm 等虚拟设备：读 slaves 目录下第一个底层设备的 rotational
	entries, err := os.ReadDir(base + "/slaves")
	if err != nil || len(entries) == 0 {
		return false, false
	}
	b, err := os.ReadFile("/sys/block/" + entries[0].Name() + "/queue/rotational") // #nosec G304 -- 固定路径内核接口
	if err != nil {
		return false, false
	}
	switch strings.TrimSpace(string(b)) {
	case "0":
		return false, true
	case "1":
		return true, true
	}
	return false, false
}

// within 判断 path 是否位于挂载点 mp 内（目录边界）。
func within(path, mp string) bool {
	if mp == "/" {
		return true
	}
	return path == mp || strings.HasPrefix(path, mp+"/")
}

// unescapeMount 还原 mountinfo 挂载点的八进制转义（空格 \040 等）。
func unescapeMount(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// AutoMemBudgetMB 依据系统总内存给出默认内存预算（MB），公式见 v1.31 设计文档 §1.3。
// 非 Linux 或解析失败返回 2048（保守默认）。
func AutoMemBudgetMB() int {
	b, err := os.ReadFile("/proc/meminfo") // #nosec G304 -- 固定路径内核接口
	if err != nil {
		return 2048
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		f := strings.Fields(strings.TrimPrefix(line, "MemTotal:"))
		if len(f) < 1 {
			return 2048
		}
		kb, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return 2048
		}
		return budgetFromKB(kb)
	}
	return 2048
}

// budgetFromKB 是 AutoMemBudgetMB 的纯函数核心（可移植测试）。
func budgetFromKB(kb int64) int {
	const GiB = 1024 * 1024 // KiB 计
	switch {
	case kb <= 8*GiB:
		return 1024
	case kb <= 16*GiB:
		return 2048
	case kb <= 32*GiB:
		return 4096
	case kb <= 64*GiB:
		return 8192
	default:
		return 12288
	}
}
