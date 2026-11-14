// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// TypeStat 是某类扩展名在本次扫描范围内的文件数与总大小（v2.2.0 类型分布图）。
type TypeStat struct {
	Ext   string `json:"ext"`
	Files int64  `json:"files"`
	Bytes int64  `json:"bytes"`
}

// scanStats 是本次扫描的枚举与跳过统计（v2.0.0）。
// 枚举在单个 WalkDir 协程里累加，哈希失败来自多个 worker，所以统一用原子计数。
type scanStats struct {
	scannedFiles atomic.Int64 // 枚举到的普通文件数（含被跳过的）
	scannedBytes atomic.Int64 // 上述文件的总大小
	nonRegular   atomic.Int64 // 符号链接/设备/管道等非普通文件
	readErrors   atomic.Int64 // WalkDir 错误与 d.Info() 失败
	smallFiles   atomic.Int64 // 小于最小文件大小（仍入库，不参与查重）
	hashFailures atomic.Int64 // 预哈希或 BLAKE3 单文件失败
	excludedDirs atomic.Int64 // 按目录名/路径/隔离区被跳过的文件夹
	// badNames：路径原始字节不是合法 UTF-8 的文件数（v3.11.0）。
	// 这类文件照常入库与参与分组，只在响应里带 name_encoding 标记，扫描结束记一条汇总日志。
	badNames atomic.Int64

	typeMu    sync.Mutex
	typeFiles map[string]int64
	typeBytes map[string]int64
}

func (s *scanStats) reset() {
	s.scannedFiles.Store(0)
	s.scannedBytes.Store(0)
	s.nonRegular.Store(0)
	s.readErrors.Store(0)
	s.smallFiles.Store(0)
	s.hashFailures.Store(0)
	s.excludedDirs.Store(0)
	s.badNames.Store(0)
	s.typeMu.Lock()
	s.typeFiles = map[string]int64{}
	s.typeBytes = map[string]int64{}
	s.typeMu.Unlock()
}

// addScanned 累计文件数与大小，同时按扩展名分桶（v2.2.0）。
func (s *scanStats) addScanned(ext string, size int64) {
	ext = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ext)), ".")
	s.scannedFiles.Add(1)
	s.scannedBytes.Add(size)
	s.typeMu.Lock()
	s.typeFiles[ext]++
	s.typeBytes[ext] += size
	s.typeMu.Unlock()
}

// typeStats 返回按占用从大到小排序的类型分布，供前端 SVG 环形图使用。
func (s *scanStats) typeStats() []TypeStat {
	s.typeMu.Lock()
	out := make([]TypeStat, 0, len(s.typeFiles))
	for ext, n := range s.typeFiles {
		out = append(out, TypeStat{Ext: ext, Files: n, Bytes: s.typeBytes[ext]})
	}
	s.typeMu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Ext < out[j].Ext
	})
	return out
}

// skippedFiles 是计入「跳过 N 个文件」的各类之和；文件夹不换算成文件数。
func (s *scanStats) skippedFiles() int64 {
	return s.nonRegular.Load() + s.readErrors.Load() + s.smallFiles.Load() + s.hashFailures.Load()
}

// summary 生成扫描结束后的一段中文摘要；没有任何跳过时返回空串。
func (s *scanStats) summary(minSize int64) string {
	files := s.skippedFiles()
	dirs := s.excludedDirs.Load()
	if files == 0 && dirs == 0 {
		return ""
	}
	head := make([]string, 0, 2)
	if files > 0 {
		head = append(head, fmt.Sprintf("%d 个文件", files))
	}
	if dirs > 0 {
		head = append(head, fmt.Sprintf("%d 个文件夹", dirs))
	}
	details := make([]string, 0, 5)
	if dirs > 0 {
		details = append(details, fmt.Sprintf("排除文件夹 %d 个", dirs))
	}
	if n := s.nonRegular.Load(); n > 0 {
		details = append(details, fmt.Sprintf("非普通文件 %d 个", n))
	}
	if n := s.readErrors.Load(); n > 0 {
		details = append(details, fmt.Sprintf("读取失败 %d 个", n))
	}
	if n := s.smallFiles.Load(); n > 0 {
		details = append(details, fmt.Sprintf("小于 %s 的文件 %d 个", formatScanSize(minSize), n))
	}
	if n := s.hashFailures.Load(); n > 0 {
		details = append(details, fmt.Sprintf("校验失败 %d 个", n))
	}
	return "跳过 " + strings.Join(head, "、") + "（" + strings.Join(details, "、") + "）"
}

// applyTo 把最终数字与摘要写进对外状态。
func (s *scanStats) applyTo(st *State, minSize int64) {
	st.ScannedFiles = s.scannedFiles.Load()
	st.ScannedBytes = s.scannedBytes.Load()
	st.Skipped = s.skippedFiles()
	st.SkippedDirs = s.excludedDirs.Load()
	st.SkipReason = s.summary(minSize)
	st.Types = s.typeStats()
}

// formatScanSize 把字节数转成适合摘要的短单位（1 KB、1.5 KB、976.6 KB、5 GB）。
func formatScanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	for i, u := range units {
		v /= 1024
		if v < 1024 || i == len(units)-1 {
			// 最多保留一位小数，并去掉多余的 .0：真机上 1000000 字节曾显示成
			// 「小于 976.5625 KB」，用户很难读。
			s := strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
			return s + " " + u
		}
	}
	return fmt.Sprintf("%d B", n)
}
