// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package config

import (
	"strings"
	"time"
)

// QuietRange 是解析后的静默时段，两端均为「当天零点起的分钟数」。
type QuietRange struct {
	Start int
	End   int
	Valid bool
}

// ParseQuietHours 解析 "HH:MM-HH:MM"。
//
// 支持跨零点（如 23:00-07:00），格式不合法时返回 Valid=false——
// 宁可当成"没有设置"，也不要在解析失败时误把整段时间当成静默期。
func ParseQuietHours(s string) QuietRange {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return QuietRange{}
	}
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return QuietRange{}
	}
	start, ok1 := parseHHMM(parts[0])
	end, ok2 := parseHHMM(parts[1])
	if !ok1 || !ok2 || start == end {
		return QuietRange{}
	}
	return QuietRange{Start: start, End: end, Valid: true}
}

// parseHHMM 把 "HH:MM" 转成分钟数，越界返回 false。
func parseHHMM(s string) (int, bool) {
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 || len(parts[0]) == 0 || len(parts[1]) == 0 {
		return 0, false
	}
	h, ok1 := atoiN(parts[0])
	m, ok2 := atoiN(parts[1])
	if !ok1 || !ok2 || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// atoiN 只解析纯数字，避免引入 strconv 的宽松格式（如 "+8"、" 8"）。
func atoiN(s string) (int, bool) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 100000 {
			return 0, false
		}
	}
	return n, true
}

// Contains 判断时刻 t 是否落在区间内（支持跨零点）。
func (r QuietRange) Contains(t time.Time) bool {
	if !r.Valid {
		return false
	}
	cur := t.Hour()*60 + t.Minute()
	if r.Start < r.End {
		return cur >= r.Start && cur < r.End
	}
	// 跨零点：23:00-07:00 表示 [23:00, 24:00) ∪ [00:00, 07:00)
	return cur >= r.Start || cur < r.End
}

// InQuietHours 判断给定时刻是否落在静默时段内。
func (c *Config) InQuietHours(t time.Time) bool {
	return ParseQuietHours(c.QuietHours).Contains(t)
}

// AutoScanAllowed 判断此刻是否允许启动"主动扫描"类后台任务。
//
// 休眠友好模式下永远不允许：这类任务会把已经休眠的磁盘唤醒，
// 而它们带来的收益（自动发现新增重复文件）远小于反复唤醒的代价。
func (c *Config) AutoScanAllowed(t time.Time) bool {
	if c.SleepFriendly {
		return false
	}
	return !c.InQuietHours(t)
}

// clampInt 把 v 限制在 [lo, hi]；v==0 且 zeroOK 时保持 0（表示"自动"）。
func clampInt(v, zeroOK, lo, hi int) int {
	if v == 0 && zeroOK == 0 {
		return 0
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
