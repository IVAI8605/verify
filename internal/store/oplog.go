// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"verify/internal/oplog"
)

// InsertOp 独立提交一条操作记录。
// 审计行绝不与业务表放进同一事务：业务回滚、隔离区清空都不能把它带走。
func (s *Store) InsertOp(ctx context.Context, e oplog.Entry, detailLimit int) error {
	if e.Action == "" {
		return fmt.Errorf("操作记录缺少动作类型")
	}
	if e.TS == 0 {
		e.TS = time.Now().UnixMilli()
	}
	if e.Source == "" {
		e.Source = oplog.SourceUI
	}
	if e.Result == "" {
		e.Result = oplog.ResultOK
	}
	detail := oplog.EncodeDetails(e.Details, detailLimit)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO op_log(ts,req_id,actor,source,action,result,summary,target_count,detail_json,error,duration_ms)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		e.TS, e.ReqID, e.Actor, e.Source, e.Action, e.Result, e.Summary,
		e.TargetCount, detail, e.Error, e.DurationMS)
	if err != nil {
		return fmt.Errorf("写入操作记录（%s）：%w", e.Action, err)
	}
	return nil
}

// OpQuery 是操作记录页的筛选条件；时间均为 Unix 毫秒。
type OpQuery struct {
	Since    int64
	Until    int64
	Action   string
	Result   string
	Keyword  string
	Page     int
	PageSize int
	All      bool // 导出时不分页
	// BeforeID 非 0 时改为 keyset 翻页：只取 id 小于它的行（v3.10.11，第七轮 P2-3）。
	// 深分页下 OFFSET 要逐行跳过，10 万条以后线性变慢；keyset 直接定位。
	BeforeID int64
}

// OpRow 是查询返回的一行操作记录。
type OpRow struct {
	ID          int64  `json:"id"`
	TS          int64  `json:"ts"`
	ReqID       string `json:"req_id,omitempty"`
	Actor       string `json:"actor,omitempty"`
	Source      string `json:"source,omitempty"`
	Action      string `json:"action"`
	Result      string `json:"result"`
	Summary     string `json:"summary,omitempty"`
	TargetCount int    `json:"target_count"`
	DetailJSON  string `json:"detail_json,omitempty"`
	Error       string `json:"error,omitempty"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
}

// opWhere 把筛选条件编译成参数化 WHERE，避免拼字符串注入。
func (q OpQuery) opWhere() (string, []any) {
	conds := []string{"1=1"}
	args := make([]any, 0, 6)
	if q.Since > 0 {
		conds = append(conds, "ts>=?")
		args = append(args, q.Since)
	}
	if q.Until > 0 {
		conds = append(conds, "ts<=?")
		args = append(args, q.Until)
	}
	if q.Action != "" {
		conds = append(conds, "action=?")
		args = append(args, q.Action)
	}
	if q.Result != "" {
		conds = append(conds, "result=?")
		args = append(args, q.Result)
	}
	if kw := strings.TrimSpace(q.Keyword); kw != "" {
		// v3.17.4（F-12）：操作记录里的路径可能带 % 或 _，按普通字符做子串匹配。
		conds = append(conds, `(summary LIKE ? ESCAPE '\' OR detail_json LIKE ? ESCAPE '\')`)
		like := "%" + EscapeLike(kw) + "%"
		args = append(args, like, like)
	}
	return strings.Join(conds, " AND "), args
}

// ListOps 按时间倒序分页查询操作记录，返回当前页与总数。
func (s *Store) ListOps(ctx context.Context, q OpQuery) ([]OpRow, int, error) {
	where, args := q.opWhere()
	var total int
	// where 只由 opWhere 里的固定条件片段拼成，外部输入全部走 ? 参数。
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM op_log WHERE "+where, args...).Scan(&total); err != nil { // #nosec G202 -- 同上，无外部输入进入 SQL
		return nil, 0, fmt.Errorf("统计操作记录：%w", err)
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	size := q.PageSize
	if size < 1 || size > 200 {
		size = 50
	}
	queryArgs := append([]any(nil), args...)
	// #nosec G202 -- where 只由 opWhere 里的固定条件片段拼成，外部输入全部走 ? 参数
	sqlText := `SELECT id,ts,req_id,actor,source,action,result,summary,target_count,detail_json,error,duration_ms
		 FROM op_log WHERE ` + where
	if q.BeforeID > 0 {
		// keyset 翻页：上限条件不能进 COUNT(*)，否则 total 会被算成「剩余条数」。
		sqlText += " AND id<?"
		queryArgs = append(queryArgs, q.BeforeID)
	}
	sqlText += " ORDER BY id DESC"
	if !q.All {
		sqlText += " LIMIT ?"
		queryArgs = append(queryArgs, size)
		if q.BeforeID <= 0 {
			// 老前端仍按 page 翻页，保留 OFFSET 回退；新前端用 before_id 走 keyset。
			sqlText += " OFFSET ?"
			queryArgs = append(queryArgs, (page-1)*size)
		}
	}
	rows, err := s.db.QueryContext(ctx, sqlText, queryArgs...) // #nosec G202 -- where 只由固定条件片段拼成，筛选值全部参数化
	if err != nil {
		return nil, 0, fmt.Errorf("查询操作记录：%w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]OpRow, 0, size)
	for rows.Next() {
		var r OpRow
		if err := rows.Scan(&r.ID, &r.TS, &r.ReqID, &r.Actor, &r.Source, &r.Action,
			&r.Result, &r.Summary, &r.TargetCount, &r.DetailJSON, &r.Error, &r.DurationMS); err != nil {
			return nil, 0, fmt.Errorf("读取操作记录：%w", err)
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("遍历操作记录：%w", err)
	}
	return items, total, nil
}

// WalkOps 按筛选条件逐行回调（id 倒序），供导出使用。
// v3.10.8（P2-10）：以前的「导出全部」把整张 op_log 读进 []OpRow，长期运行后一次导出
// 就可能占满内存；这里改为边查边写，回调返回 false 即提前结束。
// 返回实际遍历的条数。
func (s *Store) WalkOps(ctx context.Context, q OpQuery, fn func(OpRow) bool) (int, error) {
	where, args := q.opWhere()
	sqlText := `SELECT id,ts,req_id,actor,source,action,result,summary,target_count,detail_json,error,duration_ms
		 FROM op_log WHERE ` + where + ` ORDER BY id DESC` // #nosec G202 -- where 只由 opWhere 里的固定条件片段拼成，筛选值全部参数化
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return 0, fmt.Errorf("查询操作记录：%w", err)
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var r OpRow
		if err := rows.Scan(&r.ID, &r.TS, &r.ReqID, &r.Actor, &r.Source, &r.Action,
			&r.Result, &r.Summary, &r.TargetCount, &r.DetailJSON, &r.Error, &r.DurationMS); err != nil {
			return n, fmt.Errorf("读取操作记录：%w", err)
		}
		n++
		if !fn(r) {
			return n, nil
		}
	}
	if err := rows.Err(); err != nil {
		return n, fmt.Errorf("遍历操作记录：%w", err)
	}
	return n, nil
}

// PurgeOpsBefore 删除指定时间之前的记录，返回删除条数；保留期清理用。
func (s *Store) PurgeOpsBefore(ctx context.Context, ts int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM op_log WHERE ts<?", ts)
	if err != nil {
		return 0, fmt.Errorf("清理过期操作记录：%w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}
