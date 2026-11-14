// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ImportSession 是一次 CSV 导入的会话记录。
type ImportSession struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Source      string `json:"source"`
	CreatedAt   int64  `json:"created_at"`
	TotalRows   int64  `json:"total_rows"`
	GroupCount  int64  `json:"group_count"`
	MatchedRows int64  `json:"matched_rows"`
}

// ImportRow 是导入会话里的一行。
// Matched 表示路径在当前 files 表中找到；只有 matched 行允许执行隔离/删除等文件操作。
type ImportRow struct {
	ID       int64   `json:"id"`
	GroupNo  int64   `json:"group_no"`
	Path     string  `json:"path"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Hash     string  `json:"hash"`
	Chk      bool    `json:"chk"`
	Protect  bool    `json:"protect"`
	Hardlink int64   `json:"hardlink"`
	Datec    int64   `json:"datec"`
	Datem    int64   `json:"datem"`
	Duration float64 `json:"duration"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Matched  bool    `json:"matched"`
}

// ImportGroup 是导入会话按 group_no 聚合后的一个分组。
// group_no=0 表示唯一/剩余文件（含「仅标记行」导出造成的孤组），UI 按单文件组展示。
type ImportGroup struct {
	GroupNo int64       `json:"group_no"`
	Rows    []ImportRow `json:"rows"`
}

// ErrImportSessionNotFound 会话不存在（已被删除）。
var ErrImportSessionNotFound = errors.New("导入会话不存在")

// CreateImportSession 新建会话并整批写入行。
// 路径匹配在同一个事务里做：逐行查 files 表（复用 files.path 的 UNIQUE 索引），
// 匹配不上照常入库、matched=0——这不是错误，是设计。
func (s *Store) CreateImportSession(ctx context.Context, name, source string, rows []ImportRow) (ImportSession, error) {
	sess := ImportSession{
		Name:      name,
		Source:    source,
		CreatedAt: time.Now().Unix(),
	}
	if len(rows) == 0 {
		return sess, errors.New("导入内容为空")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sess, err
	}
	fail := func(err error) (ImportSession, error) {
		_ = tx.Rollback()
		return sess, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO import_session(name,source,created_at) VALUES(?,?,?)`,
		sess.Name, sess.Source, sess.CreatedAt)
	if err != nil {
		return fail(err)
	}
	sess.ID, err = res.LastInsertId()
	if err != nil {
		return fail(err)
	}
	ins, err := tx.PrepareContext(ctx,
		`INSERT INTO import_row(session_id,group_no,path,name,size,hash,chk,protect,hardlink,datec,datem,duration,width,height,matched)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return fail(err)
	}
	// 路径匹配用预编译语句逐行查，走 files.path 的 UNIQUE 索引；导入不频繁，可接受。
	match, err := tx.PrepareContext(ctx, `SELECT 1 FROM files WHERE path=?`)
	if err != nil {
		_ = ins.Close()
		return fail(err)
	}
	groups := make(map[int64]bool)
	for i := range rows {
		r := &rows[i]
		var one int
		err := match.QueryRowContext(ctx, r.Path).Scan(&one)
		switch {
		case err == nil:
			r.Matched = true
			sess.MatchedRows++
		case errors.Is(err, sql.ErrNoRows):
			r.Matched = false
		default:
			_ = ins.Close()
			_ = match.Close()
			return fail(fmt.Errorf("匹配 %s: %w", r.Path, err))
		}
		if r.GroupNo != 0 {
			groups[r.GroupNo] = true
		}
		_, err = ins.ExecContext(ctx, sess.ID, r.GroupNo, r.Path, r.Name, r.Size, r.Hash,
			r.Chk, r.Protect, r.Hardlink, r.Datec, r.Datem, r.Duration, r.Width, r.Height, r.Matched)
		if err != nil {
			_ = ins.Close()
			_ = match.Close()
			return fail(fmt.Errorf("写入 %s: %w", r.Path, err))
		}
	}
	_ = ins.Close()
	_ = match.Close()
	sess.TotalRows = int64(len(rows))
	sess.GroupCount = int64(len(groups))
	if _, err := tx.ExecContext(ctx,
		`UPDATE import_session SET total_rows=?,group_count=?,matched_rows=? WHERE id=?`,
		sess.TotalRows, sess.GroupCount, sess.MatchedRows, sess.ID); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return sess, err
	}
	return sess, nil
}

// ListImportSessions 列出全部会话，新的在前。
func (s *Store) ListImportSessions(ctx context.Context) ([]ImportSession, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,name,source,created_at,total_rows,group_count,matched_rows FROM import_session ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ImportSession
	for rows.Next() {
		var sess ImportSession
		if err := rows.Scan(&sess.ID, &sess.Name, &sess.Source, &sess.CreatedAt, &sess.TotalRows, &sess.GroupCount, &sess.MatchedRows); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// GetImportSession 取单个会话。
func (s *Store) GetImportSession(ctx context.Context, id int64) (ImportSession, error) {
	var sess ImportSession
	err := s.db.QueryRowContext(ctx,
		`SELECT id,name,source,created_at,total_rows,group_count,matched_rows FROM import_session WHERE id=?`, id).
		Scan(&sess.ID, &sess.Name, &sess.Source, &sess.CreatedAt, &sess.TotalRows, &sess.GroupCount, &sess.MatchedRows)
	if errors.Is(err, sql.ErrNoRows) {
		return sess, ErrImportSessionNotFound
	}
	return sess, err
}

// DeleteImportSession 删除会话及其全部行。
func (s *Store) DeleteImportSession(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM import_row WHERE session_id=?`, id); err != nil {
		_ = tx.Rollback()
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM import_session WHERE id=?`, id)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return ErrImportSessionNotFound
	}
	return tx.Commit()
}

// ImportGroupMemberPaths 返回导入会话中某组「本机已匹配」的路径（v3.10.10，第六轮 P1-3）。
// 只算 matched 行：未匹配的行磁盘上没有对应文件，不能充当「保留的那一份」。
func (s *Store) ImportGroupMemberPaths(ctx context.Context, sessionID, groupNo int64) ([]string, error) {
	if sessionID <= 0 || groupNo < 0 {
		return nil, ErrImportSessionNotFound
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT path FROM import_row WHERE session_id=? AND group_no=? AND matched=1 ORDER BY id`,
		sessionID, groupNo)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ImportGroups 把会话行按 group_no 聚合返回，组号升序（group_no=0 的唯一/孤组排在最前）、组内按行序。
// page/pageSize 均 >0 时按「组」分页（不能按行切，否则会返回半组）；否则返回全部，兼容旧调用。
// 第二个返回值是分组总数，供界面显示分页信息。
func (s *Store) ImportGroups(ctx context.Context, sessionID int64, page, pageSize int) ([]ImportGroup, int, error) {
	// v3.10.8（P2-11）：原实现无条件把会话全部行读进内存并整体 JSON 化，
	// 上限 50 万行时响应体可达数百 MB。分页参数缺省时行为不变，避免影响现有前端。
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT group_no) FROM import_row WHERE session_id=?`, sessionID).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}
	paged := page > 0 && pageSize > 0
	if !paged {
		// 未分页时不能套用 500 的上限，否则大会话会被静默截断，旧调用方行为改变。
		page, pageSize = 1, total
	} else if pageSize > 500 {
		pageSize = 500
	}
	gnRows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT group_no FROM import_row WHERE session_id=? ORDER BY group_no LIMIT ? OFFSET ?`,
		sessionID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	var groupNos []int64
	for gnRows.Next() {
		var gn int64
		if err := gnRows.Scan(&gn); err != nil {
			_ = gnRows.Close()
			return nil, 0, err
		}
		groupNos = append(groupNos, gn)
	}
	if err := gnRows.Err(); err != nil {
		_ = gnRows.Close()
		return nil, 0, err
	}
	_ = gnRows.Close()
	if len(groupNos) == 0 {
		return []ImportGroup{}, total, nil
	}
	// v3.10.9：用子查询代替 IN 占位符列表。未分页模式下组数可能上万，
	// 逐个拼占位符会撞 SQLite 的变量上限（too many SQL variables）；
	// 子查询把同一段分页留在 SQL 内，绑定参数数量恒定。
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,group_no,path,name,size,hash,chk,protect,hardlink,datec,datem,duration,width,height,matched
		 FROM import_row WHERE session_id=? AND group_no IN (
		   SELECT DISTINCT group_no FROM import_row WHERE session_id=? ORDER BY group_no LIMIT ? OFFSET ?
		 )
		 ORDER BY group_no, id`,
		sessionID, sessionID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	var out []ImportGroup
	cur := int64(-1)
	for rows.Next() {
		var r ImportRow
		var chk, protect, matched int
		if err := rows.Scan(&r.ID, &r.GroupNo, &r.Path, &r.Name, &r.Size, &r.Hash, &chk, &protect, &r.Hardlink,
			&r.Datec, &r.Datem, &r.Duration, &r.Width, &r.Height, &matched); err != nil {
			return nil, 0, err
		}
		r.Chk = chk != 0
		r.Protect = protect != 0
		r.Matched = matched != 0
		if r.GroupNo != cur {
			out = append(out, ImportGroup{GroupNo: r.GroupNo})
			cur = r.GroupNo
		}
		out[len(out)-1].Rows = append(out[len(out)-1].Rows, r)
	}
	return out, total, rows.Err()
}
