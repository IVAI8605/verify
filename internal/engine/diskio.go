// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"bufio"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// partitionPattern 匹配分区设备名。
//
// 只统计整盘：分区行与父盘行会重复计入同一份 I/O，
// 不排除就会把活跃度算成两倍。
var partitionPattern = regexp.MustCompile(`^(sd[a-z]+\d+|hd[a-z]+\d+|vd[a-z]+\d+|xvd[a-z]+\d+|nvme\d+n\d+p\d+|md\d+p\d+|mmcblk\d+p\d+)$`)

// DiskMonitor 通过 /proc/diskstats 观察整机磁盘 I/O 活跃度。
//
// 它只回答两个问题，不追求精确带宽：
//  1. 磁盘此刻是否正被别的任务使用——那就让路；
//  2. 磁盘最近是否一直闲着——那可能已休眠，不要去唤醒它。
//
// 采样只读内存里的虚拟文件，本身不产生磁盘 I/O。
type DiskMonitor struct {
	mu sync.Mutex

	lastSectors  uint64
	lastChangeAt time.Time
	firstSeenAt  time.Time
	lastSampleAt time.Time
	started      bool

	// activeWindow 内有过 I/O 就认为磁盘仍处于唤醒状态
	activeWindow time.Duration
	// busyRate 是判定"正在被使用"的扇区增长率阈值（扇区/秒）
	busyRate uint64
}

// NewDiskMonitor 构造监视器。
func NewDiskMonitor() *DiskMonitor {
	return &DiskMonitor{
		activeWindow: 90 * time.Second,
		busyRate:     4096, // 约 2 MiB/s
	}
}

// ReadDiskSectors 汇总所有整盘的读写扇区数。
func ReadDiskSectors(r io.Reader) (uint64, error) {
	var total uint64
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		if partitionPattern.MatchString(fields[2]) {
			continue
		}
		read, err := strconv.ParseUint(fields[5], 10, 64)
		if err != nil {
			continue
		}
		written, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}
		total += read + written
	}
	return total, sc.Err()
}

// Update 记录一次采样，返回本次是否处于"正在被使用"状态。
func (d *DiskMonitor) Update(sectors uint64, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.started {
		d.started = true
		d.firstSeenAt = now
		d.lastChangeAt = now
		d.lastSectors = sectors
		d.lastSampleAt = now
		return false
	}

	dt := now.Sub(d.lastSampleAt).Seconds()
	busy := false
	if sectors > d.lastSectors {
		d.lastChangeAt = now
		if dt > 0 && uint64(float64(sectors-d.lastSectors)/dt) >= d.busyRate {
			busy = true
		}
	}
	d.lastSectors = sectors
	d.lastSampleAt = now
	return busy
}

// IdleFor 返回磁盘已静默的时长。
// 刚启动还没观察到变化时，返回自首次采样以来的时长。
func (d *DiskMonitor) IdleFor(now time.Time) time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started {
		return 0
	}
	return now.Sub(d.lastChangeAt)
}

// AwakeRecently 判断磁盘是否在 activeWindow 内有过活动。
// 返回 false 表示可能已经休眠，此时不应主动跑重 I/O 任务。
func (d *DiskMonitor) AwakeRecently(now time.Time) bool {
	if !d.started {
		return true // 还没数据时按"醒着"处理，由其它条件兜底
	}
	return d.IdleFor(now) <= d.activeWindow
}
