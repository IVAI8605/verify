// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// processStart 记录进程启动时刻，用于 uptime_seconds（v3.26.0 F3）。
var processStart = time.Now()

// clockTicksPerSecond 是 Linux USER_HZ，utime/stime 以它为单位计秒。
// 目标平台 linux/amd64 固定为 100；非 Linux 上 /proc 读不到，指标按 0 返回。
const clockTicksPerSecond = 100

// processMetrics 是 GET /api/metrics 的响应体。
// 与 POST /api/metric（前端性能埋点）不是一回事：那个只写日志，这个读进程自身资源。
type processMetrics struct {
	PID             int     `json:"pid"`
	UptimeSeconds   int64   `json:"uptime_seconds"`
	CPUSecondsTotal float64 `json:"cpu_seconds_total"`
	RSSBytes        uint64  `json:"rss_bytes"`
	IOReadBytes     uint64  `json:"io_read_bytes"`
	IOWriteBytes    uint64  `json:"io_write_bytes"`
	Goroutines      int     `json:"goroutines"`
	Version         string  `json:"version"`
}

// handleMetrics 返回进程资源指标（v3.26.0 F3）：只读，不写日志、不落库、不碰业务数据。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	writeJSON(w, http.StatusOK, collectProcessMetrics())
}

// collectProcessMetrics 采集当前进程的 CPU / 内存 / IO / 协程数。
// 所有 /proc 读取失败都按 0 处理，保证非 Linux 环境（本机编译与测试）不报错。
func collectProcessMetrics() processMetrics {
	m := processMetrics{
		PID:           os.Getpid(),
		UptimeSeconds: int64(time.Since(processStart).Seconds()),
		Goroutines:    runtime.NumGoroutine(),
		Version:       Version,
	}
	if utime, stime, rssPages, ok := readSelfStat(); ok {
		m.CPUSecondsTotal = float64(utime+stime) / float64(clockTicksPerSecond)
		// #nosec G115 -- 页大小由内核固定为小正整数（通常 4096），转 uint64 不会溢出
		m.RSSBytes = rssPages * uint64(os.Getpagesize()) // #nosec G115
	}
	m.IOReadBytes, m.IOWriteBytes = readSelfIO()
	return m
}

// readSelfStat 解析 /proc/self/stat 的第 14/15/24 字段（utime、stime、rss 页数）。
// 第 2 字段 comm 可能含空格和括号，先按最后一个 ')' 切开再数，避免字段错位。
func readSelfStat() (utime, stime, rssPages uint64, ok bool) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, 0, 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, 0, false
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] 对应第 3 字段 state，所以第 14/15/24 字段分别落在下标 11/12/21。
	if len(fields) < 22 {
		return 0, 0, 0, false
	}
	u, err1 := strconv.ParseUint(fields[11], 10, 64)
	v, err2 := strconv.ParseUint(fields[12], 10, 64)
	p, err3 := strconv.ParseUint(fields[21], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return u, v, p, true
}

// readSelfIO 读取 /proc/self/io 的 read_bytes / write_bytes；读不到时返回 0。
func readSelfIO() (readBytes, writeBytes uint64) {
	b, err := os.ReadFile("/proc/self/io")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		n, perr := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if perr != nil {
			continue
		}
		switch k {
		case "read_bytes":
			readBytes = n
		case "write_bytes":
			writeBytes = n
		}
	}
	return readBytes, writeBytes
}
