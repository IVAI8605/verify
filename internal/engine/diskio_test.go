// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"strings"
	"testing"
	"time"
)

// TestReadDiskSectors 验证只统计整盘：分区行必须被排除，否则活跃度会翻倍。
func TestReadDiskSectors(t *testing.T) {
	// 字段：major minor name reads reads_merged sectors_read ms writes ... sectors_written
	sample := strings.Join([]string{
		"   8       0 sda 100 0 1000 0 200 0 2000 0 0 0 0",
		"   8       1 sda1 90 0 900 0 190 0 1900 0 0 0 0",
		"   8      16 sdb 10 0 300 0 20 0 400 0 0 0 0",
		" 259       0 nvme0n1 5 0 500 0 6 0 600 0 0 0 0",
		" 259       1 nvme0n1p1 4 0 400 0 5 0 500 0 0 0 0",
	}, "\n")

	got, err := ReadDiskSectors(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	// 整盘：sda(1000+2000) + sdb(300+400) + nvme0n1(500+600) = 4800
	want := uint64(4800)
	if got != want {
		t.Fatalf("扇区合计 %d，期望 %d（分区行不应计入）", got, want)
	}
}

// TestDiskMonitorIdleAndBusy 验证活跃度与静默时长的判定。
func TestDiskMonitorIdleAndBusy(t *testing.T) {
	d := NewDiskMonitor()
	base := time.Now()

	d.Update(1000, base)
	// 每秒增长 100 扇区：低于 4096 的阈值，不算忙
	if busy := d.Update(1100, base.Add(time.Second)); busy {
		t.Fatal("低速率不应判定为占用")
	}
	// 每秒增长 8192 扇区：超过阈值，判定为忙
	if busy := d.Update(9292, base.Add(2*time.Second)); !busy {
		t.Fatal("高吞吐应判定为磁盘正被占用")
	}
	// 之后十个采样点没有增长：静默 10 秒
	for i := 3; i <= 13; i++ {
		d.Update(9292, base.Add(time.Duration(i)*time.Second))
	}
	if idle := d.IdleFor(base.Add(13 * time.Second)); idle < 10*time.Second {
		t.Fatalf("静默时长应接近 11 秒，实际 %v", idle)
	}
	if !d.AwakeRecently(base.Add(13 * time.Second)) {
		t.Fatal("90 秒窗口内仍应视为醒着")
	}
	if d.AwakeRecently(base.Add(300 * time.Second)) {
		t.Fatal("超过窗口应视为可能休眠")
	}
}

// TestDiskMonitorFirstSample 验证首次采样不误判为繁忙。
func TestDiskMonitorFirstSample(t *testing.T) {
	d := NewDiskMonitor()
	if busy := d.Update(12345, time.Now()); busy {
		t.Fatal("首次采样不应判定为忙")
	}
}
