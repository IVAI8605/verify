// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import "context"

// CropEntry 单个文件的黑边检测结果。
type CropEntry struct {
	ID int64
	W  int
	H  int
}

// SetCropLabels 批量落黑边标签（同 id 覆盖）。
func (s *Store) SetCropLabels(ctx context.Context, entries []CropEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `UPDATE files SET crop_w=?, crop_h=?, crop_at=strftime('%s','now') WHERE id=?`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.W, e.H, e.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CropCandidates 待检测的视频（未打过标签或未检测）：id/path。
type CropCandidate struct {
	ID   int64
	Path string
}

// CropCandidates 返回最多 limit 个视频扩展名的活动文件（只取未检测的）。
// v3.19.0：补上 crop_at=0 条件。此前排序把未检测的排在前面但仍会把已打标的
// 一起返回，前端反复点「识别竖横屏」会重复跑 ffmpeg，且无法判断「还剩多少」。
func (s *Store) CropCandidates(ctx context.Context, limit int) ([]CropCandidate, error) {
	// 占位符必须真的传参：v1.39 上线时漏传，modernc SQLite 直接报
	// "missing argument with index 1"，整个竖横屏识别 100% 失败（v1.42.0，BUG-04）。
	if limit < 1 {
		limit = 100
	}
	// #nosec G202 -- 片段仅由常量 videoExts 生成
	rows, err := s.db.QueryContext(ctx, `SELECT id, path FROM files
		WHERE status='active' AND crop_at=0 AND ext IN (`+videoExtSQL()+`)
		ORDER BY path LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]CropCandidate, 0, limit)
	for rows.Next() {
		var c CropCandidate
		if err := rows.Scan(&c.ID, &c.Path); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountCropCandidates 统计尚未打标的活动视频数量（v3.19.0）。
// 前端用它判断是否继续下一批，并作为进度条分母的一部分。
func (s *Store) CountCropCandidates(ctx context.Context) (int, error) {
	var n int
	// #nosec G202 -- 片段仅由常量 videoExts 生成
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files
		WHERE status='active' AND crop_at=0 AND ext IN (`+videoExtSQL()+`)`).Scan(&n)
	return n, err
}

// CropFilterFiles 按黑边标签筛文件（大文件页）：label = landscape/portrait/square。
// 标签由 crop_w/crop_h 动态判定，空 label 不过滤。
func (s *Store) CropFilterFiles(ctx context.Context, label string, limit int, smallest bool) ([]FileRow, error) {
	if label == "" {
		return s.TopFiles(ctx, GroupQuery{}, limit, smallest)
	}
	cond := ""
	switch label {
	case "landscape":
		cond = `crop_w > 0 AND crop_h > 0 AND crop_w * 100 > crop_h * 105`
	case "portrait":
		cond = `crop_w > 0 AND crop_h > 0 AND crop_w * 100 < crop_h * 95`
	case "square":
		cond = `crop_w > 0 AND crop_h > 0 AND crop_w * 100 BETWEEN crop_h * 95 AND crop_h * 105`
	default:
		return s.TopFiles(ctx, GroupQuery{}, limit, smallest)
	}
	order := "DESC"
	if smallest {
		order = "ASC"
	}
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files
		WHERE status='active' AND `+cond+` ORDER BY size `+order+` LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]FileRow, 0, limit)
	for rows.Next() {
		f, err := s.scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}
