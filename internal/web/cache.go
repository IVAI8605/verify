// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"verify/internal/cache"
	"verify/internal/perm"
)

// 缓存管理（v1.31）：按类占用统计、单项清理、SSD 介质提示。
// 内存侧缩略图 LRU 与磁盘缩略图缓存的上限来自配置（mem_budget_mb / thumb_cache_max_mb）。

// cacheKindInfo 一类缓存的统计展示。
type cacheKindInfo struct {
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Count     int64  `json:"count"`
	Bytes     int64  `json:"bytes"`
	Clearable bool   `json:"clearable"`
}

// handleCacheStats GET /api/cache/stats。
func (s *Server) handleCacheStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	dir := s.cacheDir()
	media := cache.DetectMedia(dir)
	cfg := s.cfg.Get()

	// 磁盘缩略图占用（.jpg 全目录扫描；缓存目录即缩略图目录）
	var diskBytes, diskCount int64
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jpg") {
				continue
			}
			if info, err := e.Info(); err == nil {
				diskCount++
				diskBytes += info.Size()
			}
		}
	}
	classes := []cacheKindInfo{
		{Kind: "thumb_disk", Label: "缩略图（磁盘）", Count: diskCount, Bytes: diskBytes, Clearable: true},
		{Kind: "thumb_mem", Label: "缩略图（内存）", Count: int64(s.thumbs.Len()), Bytes: s.thumbs.Bytes(), Clearable: true},
	}
	if dbStats, err := s.st.DBCacheStats(r.Context()); err == nil {
		classes = append(classes,
			cacheKindInfo{Kind: "probe", Label: "视频探测缓存", Count: dbStats.Probe.Count, Bytes: dbStats.Probe.Bytes, Clearable: true},
			cacheKindInfo{Kind: "audio_fp", Label: "音频指纹", Count: dbStats.AudioFP.Count, Bytes: dbStats.AudioFP.Bytes, Clearable: true},
			cacheKindInfo{Kind: "intro_fp", Label: "片头指纹", Count: dbStats.IntroFP.Count, Bytes: dbStats.IntroFP.Bytes, Clearable: true},
			cacheKindInfo{Kind: "img_fp", Label: "图片指纹", Count: dbStats.ImgFP.Count, Bytes: dbStats.ImgFP.Bytes, Clearable: true},
			cacheKindInfo{Kind: "video_fp", Label: "视频分析结论", Count: dbStats.VideoFPMeta.Count, Bytes: 0, Clearable: false},
		)
	}
	memAuto := cfg.MemBudgetMB == 0
	budget := cfg.MemBudgetMB
	if memAuto {
		budget = cache.AutoMemBudgetMB()
	}
	writeJSON(w, 200, cacheStatsResponse{Media: media, SSDHint: media == cache.MediaHDD, CacheDir: dir, MemBudgetMB: budget, MemBudgetAuto: memAuto, ThumbMaxMB: cfg.ThumbCacheMaxMB, Classes: classes})
}

// handleCacheClear POST /api/cache/clear {kind}。
// 清理后各功能自动回退到重算（指纹/探测/缩略图均为可再生数据）。
func (s *Server) handleCacheClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	var cleared int64
	var err error
	switch body.Kind {
	case "thumb_disk":
		cleared = clearThumbDisk(s.cacheDir())
	case "thumb_mem":
		s.thumbs.Purge()
		cleared = 1
	case "probe":
		cleared, err = s.st.ClearProbeCache(r.Context())
	case "audio_fp":
		cleared, err = s.st.ClearAudioFP(r.Context())
	case "intro_fp":
		cleared, err = s.st.ClearIntroFP(r.Context())
	case "img_fp":
		cleared, err = s.st.ClearImgFP(r.Context())
	default:
		writeJSON(w, 400, errorResponse{Error: "未知的缓存类别"})
		return
	}
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: "清理失败：" + err.Error()})
		return
	}
	s.e.NotifyUpdate()
	writeJSON(w, 200, cacheClearedResponse{OK: true, Cleared: cleared})
}

// clearThumbDisk 删除 cacheDir 下全部 .jpg 缩略图文件，返回删除数。
func clearThumbDisk(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jpg") {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil { // #nosec G703 -- 路径由受管 cacheDir 拼接
			n++
		}
	}
	return n
}

// applyCacheBudget 按当前配置调整内存侧缓存容量（设置保存后与启动时各调一次）。
// 预算的 1/8 给缩略图 LRU，下限 8MiB、上限 512MiB；条目上限 65536。
func (s *Server) applyCacheBudget() {
	cfg := s.cfg.Get()
	budgetMB := cfg.MemBudgetMB
	if budgetMB == 0 {
		budgetMB = cache.AutoMemBudgetMB()
	}
	bytes := int64(budgetMB) << 20
	thumbBytes := bytes / 8
	if thumbBytes < 8<<20 {
		thumbBytes = 8 << 20
	}
	if thumbBytes > 512<<20 {
		thumbBytes = 512 << 20
	}
	s.thumbs.Resize(65536, thumbBytes)
}

// ensureCacheDir 确保缓存目录存在且权限收紧（thumbBytes 写盘前调用）。
func (s *Server) ensureCacheDir() string {
	dir := s.cacheDir()
	_ = os.MkdirAll(dir, 0o750)
	perm.Tighten(dir, 0o750)
	return dir
}

// thumbDiskMaxBytes 当前配置下的磁盘缩略图上限；0=不限。
func (s *Server) thumbDiskMaxBytes() int64 {
	mb := s.cfg.Get().ThumbCacheMaxMB
	if mb == 0 {
		return 1 << 50 // 实际不限
	}
	return int64(mb) << 20
}
