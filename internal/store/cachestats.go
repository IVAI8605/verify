// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
)

// CacheClassStats 一类缓存 DB 侧的占用统计。
// Bytes 是指纹/结论类 BLOB·TEXT 的实测字节数（LENGTH 聚合）。
type CacheClassStats struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

// CacheStats 全部 DB 侧缓存类的统计快照（v1.31 缓存管理）。
type CacheStats struct {
	Probe       CacheClassStats `json:"probe"`         // probe_cache 行
	AudioFP     CacheClassStats `json:"audio_fp"`      // files.audio（音频指纹 BLOB）
	IntroFP     CacheClassStats `json:"intro_fp"`      // files.intro_audio + intro_thumbs
	ImgFP       CacheClassStats `json:"img_fp"`        // files.img_hash / img_error
	VideoFPMeta CacheClassStats `json:"video_fp_meta"` // files 有视频结论的行（video_error/video_at）
}

// DBCacheStats 统计 DB 侧各缓存类。
func (s *Store) DBCacheStats(ctx context.Context) (CacheStats, error) {
	var out CacheStats
	row := s.db.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM probe_cache),
  (SELECT COUNT(*) FROM files WHERE audio IS NOT NULL),
  (SELECT COALESCE(SUM(LENGTH(audio)),0) FROM files WHERE audio IS NOT NULL),
  (SELECT COUNT(*) FROM files WHERE intro_audio IS NOT NULL),
  (SELECT COALESCE(SUM(LENGTH(intro_audio))+SUM(LENGTH(COALESCE(intro_thumbs,''))),0) FROM files WHERE intro_audio IS NOT NULL),
  (SELECT COUNT(*) FROM files WHERE img_hash IS NOT NULL OR img_error IS NOT NULL),
  (SELECT COALESCE(SUM(LENGTH(COALESCE(img_hash,'')))+SUM(LENGTH(COALESCE(img_error,''))),0) FROM files WHERE img_hash IS NOT NULL OR img_error IS NOT NULL),
  (SELECT COUNT(*) FROM files WHERE video_at > 0 OR video_error IS NOT NULL)`)
	if err := row.Scan(&out.Probe.Count,
		&out.AudioFP.Count, &out.AudioFP.Bytes,
		&out.IntroFP.Count, &out.IntroFP.Bytes,
		&out.ImgFP.Count, &out.ImgFP.Bytes,
		&out.VideoFPMeta.Count); err != nil {
		return out, err
	}
	return out, nil
}

// ClearProbeCache 清空视频探测缓存（巡检会全量重算）。
func (s *Store) ClearProbeCache(ctx context.Context) (int64, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM probe_cache`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// ClearAudioFP 清空音频指纹（files.audio）。
func (s *Store) ClearAudioFP(ctx context.Context) (int64, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE files SET audio=NULL WHERE audio IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// ClearIntroFP 清空片头指纹（intro_audio / intro_thumbs / intro_at / intro_error / intro_seconds）。
func (s *Store) ClearIntroFP(ctx context.Context) (int64, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE files SET intro_audio=NULL, intro_thumbs=NULL,
		intro_at=0, intro_error=NULL, intro_seconds=0
		WHERE intro_audio IS NOT NULL OR intro_error IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// ClearImgFP 清空图片指纹（img_hash / img_error / img_at）。
func (s *Store) ClearImgFP(ctx context.Context) (int64, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE files SET img_hash=NULL, img_error=NULL, img_at=0
		WHERE img_hash IS NOT NULL OR img_error IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
