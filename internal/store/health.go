// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// encodeReasons 把原因列表编成 JSON 文本；空列表存空串，与旧库的缺省值一致。
func encodeReasons(rs []string) string {
	if len(rs) == 0 {
		return ""
	}
	b, err := json.Marshal(rs)
	if err != nil {
		return ""
	}
	return string(b)
}

// decodeReasons 解码原因列；旧行或脏数据回退成单值 reason，保证界面永远拿得到原因。
func decodeReasons(raw, fallback string) []string {
	if raw != "" {
		var out []string
		if err := json.Unmarshal([]byte(raw), &out); err == nil && len(out) > 0 {
			return out
		}
	}
	if fallback == "" {
		return nil
	}
	return []string{fallback}
}

// HealthResult 是一条文件健康检测结果。
type HealthResult struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
	// Reasons 是一次命中的全部原因（v3.15.0）。Reason 仍是首个原因，保持旧调用方兼容。
	// 旧库的行没有这一列内容，读取时按 Reason 单值回退。
	Reasons    []string `json:"reasons,omitempty"`
	Detail     string   `json:"detail"`
	Size       int64    `json:"size"`
	Suggest    string   `json:"suggest,omitempty"`
	DetectedAt int64    `json:"detected_at"`
}

// HealthTask 是一次检测任务的最终摘要。
type HealthTask struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Total      int64  `json:"total"`
	Processed  int64  `json:"processed"`
	Errors     int64  `json:"errors"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at"`
}

// AuditEntry 是一条重命名/删除审计记录。
type AuditEntry struct {
	ID           int64  `json:"id"`
	TaskID       int64  `json:"task_id"`
	Operation    string `json:"operation"`
	OriginalPath string `json:"original_path"`
	NewPath      string `json:"new_path"`
	Timestamp    int64  `json:"timestamp"`
}

// ReplaceHealthResults 用一次检测的结果整体替换某类结果。
//
// 检测是"全量重算"语义：同一 kind 的旧结果在同一个事务里清掉再写入，
// 避免界面上出现上一轮已经修好的幽灵条目。
func (s *Store) ReplaceHealthResults(ctx context.Context, kind string, items []HealthResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM health_results WHERE kind=?`, kind); err != nil {
		return err
	}
	now := time.Now().Unix()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO health_results(kind,path,reason,detail,size,suggest,reasons,detected_at)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(kind,path) DO UPDATE SET
		reason=excluded.reason, detail=excluded.detail, size=excluded.size,
		suggest=excluded.suggest, reasons=excluded.reasons, detected_at=excluded.detected_at`)
	if err != nil {
		return err
	}
	for _, it := range items {
		at := it.DetectedAt
		if at == 0 {
			at = now
		}
		if _, err := stmt.ExecContext(ctx, kind, it.Path, it.Reason, it.Detail, it.Size, it.Suggest, encodeReasons(it.Reasons), at); err != nil {
			_ = stmt.Close()
			return fmt.Errorf("写入 %s: %w", it.Path, err)
		}
	}
	if err := stmt.Close(); err != nil {
		return err
	}
	return tx.Commit()
}

// maxHealthPageLimit 是单次查询的硬上限（v3.10.2 从 2000 放宽到 20000）。
// 健康页改成「有多少显示多少」，前端按 2000 一页并发拉取；这里放宽上限，
// 让单页请求能覆盖绝大多数场景，同时仍挡住一次读几十万行的极端请求。
const maxHealthPageLimit = 20000

// ListHealthResults 按类别分页返回结果，按体积降序（大文件优先处理）。
func (s *Store) ListHealthResults(ctx context.Context, kind string, limit, offset int) ([]HealthResult, error) {
	if limit <= 0 {
		limit = 500
	}
	// 超限时截断而不是回退到默认值：回退会让「想多拿一点」的调用方反而拿到更少。
	if limit > maxHealthPageLimit {
		limit = maxHealthPageLimit
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,path,reason,detail,size,suggest,reasons,detected_at
		FROM health_results WHERE kind=? ORDER BY size DESC, path LIMIT ? OFFSET ?`, kind, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []HealthResult{}
	for rows.Next() {
		var r HealthResult
		var rawReasons string
		if err := rows.Scan(&r.ID, &r.Kind, &r.Path, &r.Reason, &r.Detail, &r.Size, &r.Suggest, &rawReasons, &r.DetectedAt); err != nil {
			return nil, err
		}
		r.Reasons = decodeReasons(rawReasons, r.Reason)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountHealthByReason 返回某类里指定原因的条目数；kind 传空串表示不限类型。
// v3.15.0：用于统计「无法确定」的条目，界面据此把可操作数量与提示数量分开显示。
func (s *Store) CountHealthByReason(ctx context.Context, kind, reason string) (int64, error) {
	var n int64
	var err error
	if kind == "" {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM health_results WHERE reason=?`, reason).Scan(&n)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM health_results WHERE kind=? AND reason=?`, kind, reason).Scan(&n)
	}
	return n, err
}

// CountHealthResults 返回某类结果的总数。
func (s *Store) CountHealthResults(ctx context.Context, kind string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM health_results WHERE kind=?`, kind).Scan(&n)
	return n, err
}

// HealthCounts 一次返回四类结果的数量，供界面标签显示角标。
func (s *Store) HealthCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, COUNT(*) FROM health_results GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]int64{}
	for rows.Next() {
		var kind string
		var n int64
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

// SaveHealthTask 追加一条任务摘要并返回自增 ID。
func (s *Store) SaveHealthTask(ctx context.Context, t HealthTask) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO health_tasks(kind,status,total,processed,errors,started_at,finished_at)
		VALUES(?,?,?,?,?,?,?)`, t.Kind, t.Status, t.Total, t.Processed, t.Errors, t.StartedAt, t.FinishedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// LastHealthTask 返回某类检测最近一次的任务摘要；没有记录时返回 nil。
func (s *Store) LastHealthTask(ctx context.Context, kind string) (*HealthTask, error) {
	var t HealthTask
	err := s.db.QueryRowContext(ctx, `SELECT id,kind,status,total,processed,errors,started_at,finished_at
		FROM health_tasks WHERE kind=? ORDER BY id DESC LIMIT 1`, kind).
		Scan(&t.ID, &t.Kind, &t.Status, &t.Total, &t.Processed, &t.Errors, &t.StartedAt, &t.FinishedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

// AddAudit 写入一条审计记录。
func (s *Store) AddAudit(ctx context.Context, e AuditEntry) error {
	at := e.Timestamp
	if at == 0 {
		at = time.Now().Unix()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(task_id,operation,original_path,new_path,timestamp)
		VALUES(?,?,?,?,?)`, e.TaskID, e.Operation, e.OriginalPath, e.NewPath, at)
	return err
}

// AddAuditIfMissing 只在同一任务下没有同一条改名记录时写入（v3.10.12 P1-03）。
// 崩溃恢复可能重复处理同一条意图：若 flush 已写过审计、只差把意图标 done，
// 直接插入会让撤销时同一对路径回放两次，第二次必然失败并把批次误判成部分完成。
func (s *Store) AddAuditIfMissing(ctx context.Context, e AuditEntry) error {
	at := e.Timestamp
	if at == 0 {
		at = time.Now().Unix()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(task_id,operation,original_path,new_path,timestamp)
		SELECT ?,?,?,?,?
		WHERE NOT EXISTS (
			SELECT 1 FROM audit_log WHERE task_id=? AND operation=? AND original_path=? AND new_path=?
		)`,
		e.TaskID, e.Operation, e.OriginalPath, e.NewPath, at,
		e.TaskID, e.Operation, e.OriginalPath, e.NewPath)
	return err
}

// ListAuditByTask 按任务列出审计记录，供撤销使用；id 升序保证逆序回放即可还原。
func (s *Store) ListAuditByTask(ctx context.Context, taskID int64) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,operation,original_path,new_path,timestamp
		FROM audit_log WHERE task_id=? ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Operation, &e.OriginalPath, &e.NewPath, &e.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LastAuditTaskID 返回最近一次「健康页可撤销」的审计任务号，0 表示没有。
//
// v4.1.2：旧实现取全表 MAX(task_id)，而 rename 是跨入口共用的 operation（组内改名、
// 规则引擎都写它），trash/transcode 也各自申请 taskID。结果做完一次健康改名后，
// 只要在别处产生更大的任务号，刷新健康页拿到的就是那个无关任务——点撤销要么
// 撤销错对象，要么返回 done=0。这里只查健康页专属的 health_rename 与转简体 fanjian。
// 历史库里健康页写过 rename，已无法从 operation 区分，但那些操作早已过撤销窗口。
func (s *Store) LastAuditTaskID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(task_id),0) FROM audit_log WHERE operation IN ('health_rename','fanjian')`).Scan(&id)
	return id, err
}

// DeleteHealthResult 删除单条健康结果，重命名或清理后调用，避免界面残留。
// DropHealthByPath 删除某路径的全部健康结论。
// 改名后旧路径已经不存在，列表里若还挂着旧行，点进去只会失败。
func (s *Store) DropHealthByPath(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM health_results WHERE path = ?`, path)
	return err
}

func (s *Store) DeleteHealthResult(ctx context.Context, kind, path string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM health_results WHERE kind=? AND path=?`, kind, path)
	return err
}

// DropHealthByPaths 批量删除多个旧路径的健康结论（单事务，v3.10.2）。
//
// 批量改名一次可能改上千个文件，逐条 DELETE 会开上千个事务；
// 这里按 path 删除而不区分 kind，因为同一文件可能同时命中多类检测。
func (s *Store) DropHealthByPaths(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	return s.DeleteHealthResultsByPaths(ctx, "", paths)
}

// DeleteHealthResultsByPaths 按 kind + path 批量删除健康结论（kind 为空表示不限类型）。
// v3.10.10（第五轮 P2-8）：空目录被移入隔离区后，empty_dir 结论必须一起删，
// 否则刷新健康页仍列出已不存在的目录，再点清理只会报「目录已不存在」。
func (s *Store) DeleteHealthResultsByPaths(ctx context.Context, kind string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `DELETE FROM health_results WHERE path=? AND (?='' OR kind=?)`)
	if err != nil {
		return err
	}
	for _, p := range paths {
		if _, err := stmt.ExecContext(ctx, p, kind, kind); err != nil {
			_ = stmt.Close()
			return err
		}
	}
	_ = stmt.Close()
	return tx.Commit()
}

// NewAuditTaskID 返回一个新的审计任务号。
//
// v3.10.4：原先用 SELECT MAX(task_id)+1 且不在事务里，两个批量操作并发时
// 会取到同一个号，撤销一条任务会把另一条一起回放、改错文件。
// 现在用 meta 里的持久序列原子自增（单条 upsert + RETURNING，SQLite 写锁天然串行），
// 并取 max(序列+1, audit_log 最大号+1)，兼容升级前的旧数据。
func (s *Store) NewAuditTaskID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO meta(key,value)
		VALUES('audit_task_seq', CAST((SELECT COALESCE(MAX(task_id),0)+1 FROM audit_log) AS TEXT))
		ON CONFLICT(key) DO UPDATE SET value=CAST(MAX(CAST(value AS INTEGER)+1,
			(SELECT COALESCE(MAX(task_id),0)+1 FROM audit_log)) AS TEXT)
		RETURNING CAST(value AS INTEGER)`).Scan(&id)
	return id, err
}

// UpdateLinkMeta 硬链接合并后回写行的 inode 与 nlink（按实际 stat 结果，v1.13.0）。
// 行不存在（文件不在索引内）时静默忽略——目标可能从未被扫描到。
func (s *Store) UpdateLinkMeta(ctx context.Context, path string, inode, nlink int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE files SET inode=?, nlink=? WHERE path=?`, inode, nlink, path)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		// v3.17.4（F-05）：inode/nlink 是硬链接合并的分组依据，变化后快照应过期。
		s.dataVersion.Add(1)
	}
	return nil
}

// ClearHealthResults 清空某类结果，扫描开始时调用。
//
// 与 ReplaceHealthResults 的区别：本方法只清空，结果随后通过 AppendHealthResults
// 边扫边追加。这样界面在扫描过程中就能看到已命中的条目，而不是等到全部结束。
func (s *Store) ClearHealthResults(ctx context.Context, kind string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM health_results WHERE kind=?`, kind)
	return err
}

// DeleteHealthDetectedBefore 删除本轮开始前写入的全部健康结果。
//
// v3.10.4：全量检测改为「边扫边 upsert、成功后清理旧行」，取消时不再
// 先清空，避免抹掉上一轮完整结论；本方法只在扫描正常完成且无写库失败时调用。
func (s *Store) DeleteHealthDetectedBefore(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM health_results WHERE detected_at < ?`, before)
	return err
}

// AppendHealthResults 追加一批结果，同一 (kind,path) 覆盖旧记录。
//
// 分片写入是为了避免每命中一个文件就开一次事务：扫描器按批调用，
// 既能让前端看到进展，又不会把磁盘写放大到不可接受。
func (s *Store) AppendHealthResults(ctx context.Context, kind string, items []HealthResult) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO health_results(kind,path,reason,detail,size,suggest,reasons,detected_at)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(kind,path) DO UPDATE SET
		reason=excluded.reason, detail=excluded.detail, size=excluded.size,
		suggest=excluded.suggest, reasons=excluded.reasons, detected_at=excluded.detected_at`)
	if err != nil {
		return err
	}
	for _, it := range items {
		at := it.DetectedAt
		if at == 0 {
			at = now
		}
		if _, err := stmt.ExecContext(ctx, kind, it.Path, it.Reason, it.Detail, it.Size, it.Suggest, encodeReasons(it.Reasons), at); err != nil {
			_ = stmt.Close()
			return fmt.Errorf("写入 %s: %w", it.Path, err)
		}
	}
	if err := stmt.Close(); err != nil {
		return err
	}
	return tx.Commit()
}

// HealthPaths 返回某类结果里已记录的路径集合。
//
// v3.9.10 更正：这里只做查询，不负责断点续跑；当前全项目零调用，
// 保留是给后续「健康检测跳过已出结论文件」用，接调用前不要当成已实现的能力。
func (s *Store) HealthPaths(ctx context.Context, kind string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM health_results WHERE kind=?`, kind)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

// HealthRowRef 是健康结论的最小投影（id/kind/path/size），
// 供「磁盘存在性复核」这类只判断行是否还有效、不需要 detail/suggest 的场景使用。
type HealthRowRef struct {
	ID   int64
	Kind string
	Path string
	Size int64
}

// AllHealthRows 返回某类（kind 为空表示全部）健康结论的轻量投影。
// 路径复核要遍历整表，避免把 detail/suggest 这些大字段一起读进内存。
func (s *Store) AllHealthRows(ctx context.Context, kind string) ([]HealthRowRef, error) {
	q := `SELECT id, kind, path, size FROM health_results`
	args := []any{}
	if kind != "" {
		q += ` WHERE kind=?`
		args = append(args, kind)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []HealthRowRef{}
	for rows.Next() {
		var r HealthRowRef
		if err := rows.Scan(&r.ID, &r.Kind, &r.Path, &r.Size); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteHealthByIDs 按主键批量删除健康结论（幽灵行复核用）。
//
// 按 id 而不是按路径删除：复核与删除之间同一路径可能又被新一轮检测写入，
// 按路径删会误删刚生成的新结论；id 定位到复核时看到的那一行。
func (s *Store) DeleteHealthByIDs(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	const chunk = 400
	var total int64
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		part := ids[start:end]
		args := make([]any, 0, len(part))
		for _, id := range part {
			args = append(args, id)
		}
		// 拼接内容仅为 len(part) 个问号，无外部输入；SQL 主体是常量。
		ph := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		res, err := s.db.ExecContext(ctx, `DELETE FROM health_results WHERE id IN (`+ph+`)`, args...) // #nosec G202 -- ph 只含问号与逗号
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// HealthIgnore 是一条「误报忽略」记录。
type HealthIgnore struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size"`
	Mtime     int64  `json:"mtime"`
	Reason    string `json:"reason"`
	CreatedAt int64  `json:"created_at"`
}

// AddHealthIgnore 记录一条忽略；同一路径重复标记时覆盖（以最新文件身份为准）。
func (s *Store) AddHealthIgnore(ctx context.Context, it HealthIgnore) error {
	if it.CreatedAt == 0 {
		it.CreatedAt = time.Now().Unix()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO health_ignored(path,kind,size,mtime,reason,created_at)
		VALUES(?,?,?,?,?,?) ON CONFLICT(path,kind) DO UPDATE SET
		kind=excluded.kind, size=excluded.size, mtime=excluded.mtime,
		reason=excluded.reason, created_at=excluded.created_at`,
		it.Path, it.Kind, it.Size, it.Mtime, it.Reason, it.CreatedAt)
	return err
}

// RemoveHealthIgnore 取消若干路径的忽略标记。
func (s *Store) RemoveHealthIgnore(ctx context.Context, paths []string) (int64, error) {
	if len(paths) == 0 {
		return 0, nil
	}
	var total int64
	for _, p := range paths {
		res, err := s.db.ExecContext(ctx, `DELETE FROM health_ignored WHERE path=?`, p)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// HealthIgnoreMap 返回忽略表（kind+"\x00"+path → 记录），供检测时按 size+mtime 判断是否仍有效。
// v4.2.1：键带上 kind，忽略「损坏」不再连带隐藏同一文件的扩展名/文件名结论。
func (s *Store) HealthIgnoreMap(ctx context.Context) (map[string]HealthIgnore, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path,kind,size,mtime,reason,created_at FROM health_ignored`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]HealthIgnore{}
	for rows.Next() {
		var it HealthIgnore
		if err := rows.Scan(&it.Path, &it.Kind, &it.Size, &it.Mtime, &it.Reason, &it.CreatedAt); err != nil {
			return nil, err
		}
		out[it.Kind+"\x00"+it.Path] = it
	}
	return out, rows.Err()
}

// ListHealthIgnore 列出全部忽略记录（最新在前）。
func (s *Store) ListHealthIgnore(ctx context.Context) ([]HealthIgnore, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path,kind,size,mtime,reason,created_at FROM health_ignored ORDER BY created_at DESC, path`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []HealthIgnore{}
	for rows.Next() {
		var it HealthIgnore
		if err := rows.Scan(&it.Path, &it.Kind, &it.Size, &it.Mtime, &it.Reason, &it.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
