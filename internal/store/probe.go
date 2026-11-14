// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"fmt"
	"time"
)

// 探测结论状态。
const (
	// ProbeOK 文件可以正常解码。
	ProbeOK = "ok"
	// ProbeBroken 文件无法解码。
	ProbeBroken = "broken"
	// ProbeTimeout 探测超时，属于疑似损坏。
	ProbeTimeout = "timeout"
	// ProbeError 探测过程中出现非解码类错误。
	ProbeError = "error"
)

// ProbeAlgoVersion 是损坏探测判定规则的版本号。
// v3.9.11 把「退出码 0 但有告警也判损坏、只看开头 1 秒」改成「退出码 0 即正常 +
// ffprobe 复核 + 片中二次解码」，旧规则落库的 broken/timeout 结论必须作废，
// 否则新版逻辑再好，界面仍会沿用旧缓存里的误报。
const ProbeAlgoVersion = 2

// ProbeEntry 是一条视频探测缓存。
//
// 关键在于它也记录 ProbeOK：只有把"健康"这个结论存下来，
// 下次巡检才能跳过绝大多数文件，而不是每次都重新调 ffmpeg。
type ProbeEntry struct {
	Path     string
	Size     int64
	Mtime    int64
	Status   string
	Reason   string
	Detail   string
	ProbedAt int64
}

// ProbeCache 批量读取探测缓存，key 为路径。
func (s *Store) ProbeCache(ctx context.Context) (map[string]ProbeEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path,size,mtime,status,reason,detail,probed_at FROM probe_cache`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]ProbeEntry, 4096)
	for rows.Next() {
		var e ProbeEntry
		if err := rows.Scan(&e.Path, &e.Size, &e.Mtime, &e.Status, &e.Reason, &e.Detail, &e.ProbedAt); err != nil {
			return nil, err
		}
		out[e.Path] = e
	}
	return out, rows.Err()
}

// SaveProbeCache 批量写入探测结论，同一路径覆盖。
func (s *Store) SaveProbeCache(ctx context.Context, entries []ProbeEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO probe_cache(path,size,mtime,status,reason,detail,probed_at)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET
		size=excluded.size, mtime=excluded.mtime, status=excluded.status,
		reason=excluded.reason, detail=excluded.detail, probed_at=excluded.probed_at`)
	if err != nil {
		return err
	}
	for _, e := range entries {
		at := e.ProbedAt
		if at == 0 {
			at = now
		}
		if _, err := stmt.ExecContext(ctx, e.Path, e.Size, e.Mtime, e.Status, e.Reason, e.Detail, at); err != nil {
			_ = stmt.Close()
			return fmt.Errorf("写入探测缓存 %s: %w", e.Path, err)
		}
	}
	if err := stmt.Close(); err != nil {
		return err
	}
	return tx.Commit()
}

// MigrateProbeAlgo 在判定规则版本变化时清掉非 OK 的探测缓存，返回清理条数。
// 只删 broken/timeout/error：新规则比旧规则宽松，已判 OK 的文件不必重解；
// 全表清空会让上万个视频在下次健康扫描时重新调用 ffmpeg，代价过高。
func (s *Store) MigrateProbeAlgo(ctx context.Context, version int) (int64, error) {
	var cur string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='probe_algo'`).Scan(&cur)
	if err == nil && cur == fmt.Sprint(version) {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM probe_cache WHERE status<>?`, ProbeOK)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('probe_algo',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fmt.Sprint(version))
	return n, err
}

// PruneProbeCache 删除不存在的文件的缓存，控制表体积。
func (s *Store) PruneProbeCache(ctx context.Context, keep map[string]bool) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM probe_cache`)
	if err != nil {
		return 0, err
	}
	var stale []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if !keep[p] {
			stale = append(stale, p)
		}
	}
	_ = rows.Close()
	if len(stale) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `DELETE FROM probe_cache WHERE path=?`)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, p := range stale {
		if _, err := stmt.ExecContext(ctx, p); err != nil {
			_ = stmt.Close()
			return n, err
		}
		n++
	}
	if err := stmt.Close(); err != nil {
		return n, err
	}
	return n, tx.Commit()
}

// ProbeStats 返回缓存覆盖率，用于界面显示"已探测 X / 共 Y"。
func (s *Store) ProbeStats(ctx context.Context) (total int64, broken int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN status<>? THEN 1 ELSE 0 END),0) FROM probe_cache`, ProbeOK).Scan(&total, &broken)
	return total, broken, err
}
