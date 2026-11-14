// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// 本文件是「写操作前快照 + 操作后对账」的持久化层（v3.10.5）。
//
// 为什么放 SQLite：快照必须和审计记录同生共死，进程崩溃 / 断电后还能查到
// 「这次操作前这些文件是什么样」；放数据目录的散文件反而多一套格式与清理逻辑。
// 表里只写本次操作触及的条目，不复制 files 全表——34.8 万文件的库也不会变重。

// OpSnapshotItem 是操作前采集到的一条文件状态。
type OpSnapshotItem struct {
	Path  string
	Size  int64
	Mtime int64 // UnixNano，与 files.mtime 口径一致
	Hash  string
}

// OpSnapshotResult 是对账后的单条结果。
type OpSnapshotResult struct {
	Path      string
	Size      int64
	Mtime     int64
	Hash      string
	Expect    string // absent / relocated / renamed
	FinalPath string
	State     string // ok / still_exists / missing / changed / path_mismatch / skipped / pending
	Detail    string
}

// opSnapshotKeepDays 是快照保留天数。
// 一次十八万文件的改名就会写十八万行，不设上限会把库撑大；
// 30 天足够复盘一次事故，更早的让位给空间。
const opSnapshotKeepDays = 30

// CreateOpSnapshot 建一次快照运行记录，返回 run id。
func (s *Store) CreateOpSnapshot(ctx context.Context, kind string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO op_snapshot_run(kind, started_at) VALUES(?, ?)`,
		kind, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	// 顺手清过期快照；通常删 0 行，删不动也不影响本次操作。
	_ = s.pruneOpSnapshots(ctx, time.Now().AddDate(0, 0, -opSnapshotKeepDays).Unix())
	return res.LastInsertId()
}

// pruneOpSnapshots 删除 before 之前的快照（先条目后运行记录，避免留下孤儿行）。
func (s *Store) pruneOpSnapshots(ctx context.Context, before int64) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM op_snapshot_item WHERE run_id IN (SELECT id FROM op_snapshot_run WHERE started_at < ?)`,
		before); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM op_snapshot_run WHERE started_at < ?`, before)
	return err
}

// AddOpSnapshotItems 批量写入快照条目。
// v3.21.0：按 1000 条一片提交；40 万条改名如果整批一个事务，WAL 会被顶满。
func (s *Store) AddOpSnapshotItems(ctx context.Context, runID int64, items []OpSnapshotItem) error {
	if runID <= 0 || len(items) == 0 {
		return nil
	}
	const chunk = 1000
	for start := 0; start < len(items); start += chunk {
		end := start + chunk
		if end > len(items) {
			end = len(items)
		}
		if err := s.addOpSnapshotChunk(ctx, runID, items[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) addOpSnapshotChunk(ctx context.Context, runID int64, items []OpSnapshotItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO op_snapshot_item(run_id, path, size, mtime, hash, expect, state)
		 VALUES(?, ?, ?, ?, ?, 'absent', 'pending')`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	for _, it := range items {
		var hash any
		if it.Hash != "" {
			hash = it.Hash
		}
		if _, err := stmt.ExecContext(ctx, runID, it.Path, it.Size, it.Mtime, hash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FinishOpSnapshot 回填对账结果并关闭本次快照。
// v3.21.0：逐条回填按 1000 条一片提交，最后再单独更新运行汇总。
func (s *Store) FinishOpSnapshot(ctx context.Context, runID int64, results []OpSnapshotResult, note string) error {
	if runID <= 0 {
		return nil
	}
	const chunk = 1000
	checked, mismatched := 0, 0
	for start := 0; start < len(results); start += chunk {
		end := start + chunk
		if end > len(results) {
			end = len(results)
		}
		c, m, err := s.finishOpSnapshotChunk(ctx, runID, results[start:end])
		if err != nil {
			return err
		}
		checked += c
		mismatched += m
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE op_snapshot_run SET finished_at=?, checked=?, mismatched=?, note=? WHERE id=?`,
		time.Now().Unix(), checked, mismatched, note, runID)
	return err
}

func (s *Store) finishOpSnapshotChunk(ctx context.Context, runID int64, results []OpSnapshotResult) (int, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	stmt, err := tx.PrepareContext(ctx,
		`UPDATE op_snapshot_item
		 SET expect=?, final_path=?, state=?, detail=?, checked_at=?
		 WHERE run_id=? AND path=?`)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = stmt.Close() }()

	checked, mismatched := 0, 0
	for _, r := range results {
		if r.State != "skipped" {
			checked++
			if r.State != "ok" {
				mismatched++
			}
		}
		if _, err := stmt.ExecContext(ctx, r.Expect, r.FinalPath, r.State, r.Detail, now, runID, r.Path); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return checked, mismatched, nil
}

// FileHashesByPaths 按路径批量取已有哈希（只读，不做磁盘校验）。
// 快照记哈希只为事后追查，取不到就留空，绝不为了快照去重算全量哈希。
func (s *Store) FileHashesByPaths(ctx context.Context, paths []string) (map[string]string, error) {
	out := make(map[string]string, len(paths))
	const chunk = 400
	for start := 0; start < len(paths); start += chunk {
		end := start + chunk
		if end > len(paths) {
			end = len(paths)
		}
		batch := paths[start:end]
		args := make([]any, 0, len(batch))
		marks := make([]string, 0, len(batch))
		for _, p := range batch {
			args = append(args, p)
			marks = append(marks, "?")
		}
		// #nosec G202 -- 拼接的只有本循环生成的 "?" 占位符，路径值一律走参数绑定
		rows, err := s.db.QueryContext(ctx,
			`SELECT path, hash FROM files
			 WHERE path IN (`+strings.Join(marks, ",")+`)
			   AND hash IS NOT NULL AND hash<>''`, args...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var p, h string
			if err := rows.Scan(&p, &h); err != nil {
				_ = rows.Close()
				return out, err
			}
			out[p] = h
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return out, err
		}
		_ = rows.Close()
	}
	return out, nil
}

// OpSnapshotRun 是一条快照运行的汇总，供「未完成 run」识别使用（v3.10.10，第五轮 P1-1）。
type OpSnapshotRun struct {
	ID        int64
	Kind      string
	StartedAt int64
	Items     int
}

// ListUnfinishedOpSnapshots 返回 finished_at=0 的快照（进程崩溃 / 断电留下的未完成操作）。
// 只读查询，不碰文件；reconcile 由 engine 负责。
func (s *Store) ListUnfinishedOpSnapshots(ctx context.Context, limit int) ([]OpSnapshotRun, error) {
	if limit <= 0 || limit > 50 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT r.id, r.kind, r.started_at,
		        (SELECT COUNT(*) FROM op_snapshot_item i WHERE i.run_id=r.id)
		 FROM op_snapshot_run r
		 WHERE r.finished_at=0
		 ORDER BY r.started_at DESC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []OpSnapshotRun
	for rows.Next() {
		var r OpSnapshotRun
		if err := rows.Scan(&r.ID, &r.Kind, &r.StartedAt, &r.Items); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FinishUnfinishedOpSnapshotsByKind 把某一类未完成快照一次性结清（v3.10.12 P2-01）。
// 崩溃留下的快照没有机会走 finishOpSnapshot；对应的改名批次被撤销后，
// 快照里的「文件仍在原路径」其实是撤销后的正常结果，不该继续当异常报警。
func (s *Store) FinishUnfinishedOpSnapshotsByKind(ctx context.Context, kind, note string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE op_snapshot_run SET finished_at=?, note=? WHERE kind=? AND finished_at=0`,
		time.Now().Unix(), note, kind)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err == nil && n > 0 {
		s.dataVersion.Add(1)
	}
	return n, err
}

// OpSnapshotDiff 是一次快照对账结果的分类计数（v3.11.0，历史快照只读接口）。
type OpSnapshotDiff struct {
	StillExists  int `json:"still_exists"`
	Missing      int `json:"missing"`
	Changed      int `json:"changed"`
	PathMismatch int `json:"path_mismatch"`
	RecordLost   int `json:"record_lost"`
}

// OpSnapshotSummary 是历史写操作快照的一页条目（v3.11.0）。
// Items 只取对账差异优先的前若干条，完整明细仍可用 run id 走 ListOpSnapshotItems。
type OpSnapshotSummary struct {
	ID         int64
	Kind       string
	StartedAt  int64
	FinishedAt int64
	Checked    int
	Mismatched int
	Diff       OpSnapshotDiff
	Items      []OpSnapshotResult
	// ItemsTruncated 表示明细超过本次返回上限（v3.13.0）。
	// 界面据此显示「仅显示前 N 条」；恰好取满但确实只有这么多时不再误报截断。
	ItemsTruncated bool
}

// ListOpSnapshots 分页查询历史写操作快照（v3.11.0）。
//
// 只读：不触发任何文件操作，也不重算哈希。
// result 取值 all / ok / mismatch；keyword 同时匹配原路径与对账后路径。
func (s *Store) ListOpSnapshots(ctx context.Context, page, pageSize int, kind, result, keyword string, itemLimit int) (int, []OpSnapshotSummary, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	if itemLimit <= 0 || itemLimit > 200 {
		itemLimit = 200
	}

	// 两条静态 SQL 代替拼接 WHERE：条件是「参数为空则跳过」的形态，用户输入一律走 ?N 参数绑定，
	// 既没有注入面，也避免动态 SQL 触发 gosec G202（CI Lint 门禁）。
	const countSQL = `SELECT COUNT(*) FROM op_snapshot_run r
		WHERE (?1='' OR r.kind=?1)
		  AND (?2<>'ok' OR (r.finished_at>0 AND r.mismatched=0))
		  AND (?2<>'mismatch' OR r.mismatched>0)
		  AND (?3='' OR EXISTS (SELECT 1 FROM op_snapshot_item i
		        WHERE i.run_id=r.id AND (i.path LIKE ?3 ESCAPE '\' OR i.final_path LIKE ?3 ESCAPE '\')))`
	const listSQL = `SELECT r.id, r.kind, r.started_at, r.finished_at, r.checked, r.mismatched
		FROM op_snapshot_run r
		WHERE (?1='' OR r.kind=?1)
		  AND (?2<>'ok' OR (r.finished_at>0 AND r.mismatched=0))
		  AND (?2<>'mismatch' OR r.mismatched>0)
		  AND (?3='' OR EXISTS (SELECT 1 FROM op_snapshot_item i
		        WHERE i.run_id=r.id AND (i.path LIKE ?3 ESCAPE '\' OR i.final_path LIKE ?3 ESCAPE '\')))
		ORDER BY r.started_at DESC, r.id DESC LIMIT ?4 OFFSET ?5`

	like := ""
	if keyword != "" {
		// v3.17.4（F-12）：路径里的 % 与 _ 是普通字符，不能当 LIKE 通配符。
		like = "%" + EscapeLike(keyword) + "%"
	}

	var total int
	if err := s.db.QueryRowContext(ctx, countSQL, kind, result, like).Scan(&total); err != nil {
		return 0, nil, err
	}
	if total == 0 {
		return 0, nil, nil
	}

	rows, err := s.db.QueryContext(ctx, listSQL, kind, result, like, pageSize, (page-1)*pageSize)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]OpSnapshotSummary, 0, pageSize)
	for rows.Next() {
		var it OpSnapshotSummary
		if err := rows.Scan(&it.ID, &it.Kind, &it.StartedAt, &it.FinishedAt, &it.Checked, &it.Mismatched); err != nil {
			return 0, nil, err
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	_ = rows.Close()

	for i := range out {
		diffs, err := s.opSnapshotDiff(ctx, out[i].ID)
		if err != nil {
			return 0, nil, err
		}
		out[i].Diff = diffs
		items, err := s.ListOpSnapshotItems(ctx, out[i].ID, itemLimit)
		if err != nil {
			return 0, nil, err
		}
		out[i].Items = items
		// v3.13.0：只有取满上限才可能被截断，这时才多查一次总数；
		// 否则一个几十万行的快照会让每页固定多跑 N 次 COUNT。
		if len(items) == itemLimit {
			var n int
			if err := s.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM op_snapshot_item WHERE run_id=?`, out[i].ID).Scan(&n); err != nil {
				return 0, nil, err
			}
			out[i].ItemsTruncated = n > len(items)
		}
	}
	return total, out, nil
}

// opSnapshotDiff 统计一次快照各对账状态的条数。
func (s *Store) opSnapshotDiff(ctx context.Context, runID int64) (OpSnapshotDiff, error) {
	var d OpSnapshotDiff
	rows, err := s.db.QueryContext(ctx,
		`SELECT state, COUNT(*) FROM op_snapshot_item WHERE run_id=? GROUP BY state`, runID)
	if err != nil {
		return d, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return d, err
		}
		switch state {
		case "still_exists":
			d.StillExists = n
		case "missing":
			d.Missing = n
		case "changed":
			d.Changed = n
		case "path_mismatch":
			d.PathMismatch = n
		case "record_lost":
			d.RecordLost = n
		}
	}
	return d, rows.Err()
}

// GetOpSnapshotRun 读取一次快照的汇总（供后续界面或排障使用）。
func (s *Store) GetOpSnapshotRun(ctx context.Context, runID int64) (kind string, startedAt, finishedAt int64, checked, mismatched int, note string, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT kind, started_at, finished_at, checked, mismatched, note FROM op_snapshot_run WHERE id=?`, runID)
	err = row.Scan(&kind, &startedAt, &finishedAt, &checked, &mismatched, &note)
	return
}

// ListOpSnapshotItems 返回某次快照的条目，按不一致优先排序，供排障与界面展开。
func (s *Store) ListOpSnapshotItems(ctx context.Context, runID int64, limit int) ([]OpSnapshotResult, error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, size, mtime, COALESCE(hash,''), expect, final_path, state, detail
		 FROM op_snapshot_item WHERE run_id=?
		 ORDER BY CASE WHEN state IN ('ok','skipped','pending') THEN 1 ELSE 0 END, id
		 LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]OpSnapshotResult, 0, limit)
	for rows.Next() {
		var r OpSnapshotResult
		var hash sql.NullString
		if err := rows.Scan(&r.Path, &r.Size, &r.Mtime, &hash, &r.Expect, &r.FinalPath, &r.State, &r.Detail); err != nil {
			return nil, err
		}
		r.Hash = hash.String
		out = append(out, r)
	}
	return out, rows.Err()
}
