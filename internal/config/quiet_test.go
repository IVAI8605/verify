// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package config

import (
	"testing"
	"time"
)

// TestParseQuietHours 覆盖合法与非法格式。
func TestParseQuietHours(t *testing.T) {
	cases := []struct {
		in    string
		valid bool
		start int
		end   int
	}{
		{"23:00-07:00", true, 23 * 60, 7 * 60},
		{"08:30-18:00", true, 8*60 + 30, 18 * 60},
		{" 09:00 - 10:00 ", true, 9 * 60, 10 * 60},
		{"", false, 0, 0},
		{"  ", false, 0, 0},
		{"-", false, 0, 0},
		{"23:00", false, 0, 0},
		{"25:00-07:00", false, 0, 0},
		{"23:70-07:00", false, 0, 0},
		{"23:00-23:00", false, 0, 0},
		{"abc-def", false, 0, 0},
		{"+8:00-09:00", false, 0, 0},
	}
	for _, c := range cases {
		got := ParseQuietHours(c.in)
		if got.Valid != c.valid {
			t.Fatalf("ParseQuietHours(%q).Valid = %v，期望 %v", c.in, got.Valid, c.valid)
		}
		if !c.valid {
			continue
		}
		if got.Start != c.start || got.End != c.end {
			t.Fatalf("ParseQuietHours(%q) = %d-%d，期望 %d-%d", c.in, got.Start, got.End, c.start, c.end)
		}
	}
}

// TestInQuietHoursSameDay 覆盖不跨零点的区间。
func TestInQuietHoursSameDay(t *testing.T) {
	c := &Config{QuietHours: "09:00-18:00"}
	cases := map[string]bool{
		"08:59": false,
		"09:00": true,
		"12:00": true,
		"17:59": true,
		"18:00": false,
		"23:30": false,
	}
	for hm, want := range cases {
		tm := mustTime(t, hm)
		if got := c.InQuietHours(tm); got != want {
			t.Fatalf("%s 落在 09:00-18:00 内 = %v，期望 %v", hm, got, want)
		}
	}
}

// TestInQuietHoursOvernight 覆盖跨零点区间，这是最容易写错的一种。
func TestInQuietHoursOvernight(t *testing.T) {
	c := &Config{QuietHours: "23:00-07:00"}
	cases := map[string]bool{
		"22:59": false,
		"23:00": true,
		"00:00": true,
		"03:30": true,
		"06:59": true,
		"07:00": false,
		"12:00": false,
	}
	for hm, want := range cases {
		tm := mustTime(t, hm)
		if got := c.InQuietHours(tm); got != want {
			t.Fatalf("%s 落在 23:00-07:00 内 = %v，期望 %v", hm, got, want)
		}
	}
}

// TestInQuietHoursInvalid 验证格式非法时不做任何限制。
func TestInQuietHoursInvalid(t *testing.T) {
	c := &Config{QuietHours: "乱写"}
	if c.InQuietHours(mustTime(t, "03:00")) {
		t.Fatal("格式非法时应视为未设置静默时段")
	}
}

// TestAutoScanAllowed 是这次休眠适配的核心断言。
func TestAutoScanAllowed(t *testing.T) {
	sleepy := &Config{SleepFriendly: true}
	if sleepy.AutoScanAllowed(mustTime(t, "12:00")) {
		t.Fatal("休眠友好模式下任何时刻都不应启动主动扫描")
	}

	awake := &Config{SleepFriendly: false, QuietHours: "23:00-07:00"}
	if awake.AutoScanAllowed(mustTime(t, "03:00")) {
		t.Fatal("静默时段内不应启动主动扫描")
	}
	if !awake.AutoScanAllowed(mustTime(t, "12:00")) {
		t.Fatal("非静默时段且未开启休眠友好时应允许")
	}

	plain := &Config{SleepFriendly: false}
	if !plain.AutoScanAllowed(mustTime(t, "03:00")) {
		t.Fatal("既没开休眠友好也没设静默时段时应允许")
	}
}

func mustTime(t *testing.T, hm string) time.Time {
	t.Helper()
	tm, err := time.Parse("15:04", hm)
	if err != nil {
		t.Fatalf("时间 %q 解析失败: %v", hm, err)
	}
	return tm
}
