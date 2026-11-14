// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

// 本文件是 v3.4.0 跨设备指纹包存储（设计文档 §5 V3.4-1）。
// 只保存另一台机器导出的文件指纹（path/size/hash/quick）与机器名，
// 不保存任何凭据；删除 peer_files 即回到单机行为。

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// PeerFile 是另一台机器的一条文件指纹。
type PeerFile struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Hash  string `json:"hash,omitempty"`
	Quick string `json:"quick,omitempty"`
}

// SyncPeer 是导入过的机器记录。
type SyncPeer struct {
	Machine    string `json:"machine"`
	ImportedAt int64  `json:"imported_at"`
	RowCount   int64  `json:"row_count"`
}

// SavePeerPackage 用一份指纹包整体替换某台机器的记录（先删后插，单事务）。
func (s *Store) SavePeerPackage(ctx context.Context, machine string, rows []PeerFile) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO sync_peers(machine,imported_at,row_count) VALUES(?,?,?)
		ON CONFLICT(machine) DO UPDATE SET imported_at=excluded.imported_at,row_count=excluded.row_count`,
		machine, time.Now().Unix(), len(rows)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM peer_files WHERE machine=?`, machine); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO peer_files(machine,path,size,hash,quick) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, machine, r.Path, r.Size, nullIfEmpty(r.Hash), nullIfEmpty(r.Quick)); err != nil {
			_ = stmt.Close()
			return err
		}
	}
	_ = stmt.Close()
	return tx.Commit()
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// ListSyncPeers 返回已导入的机器列表。
func (s *Store) ListSyncPeers(ctx context.Context) ([]SyncPeer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT machine,imported_at,row_count FROM sync_peers ORDER BY machine`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []SyncPeer{}
	for rows.Next() {
		var p SyncPeer
		if err := rows.Scan(&p.Machine, &p.ImportedAt, &p.RowCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteSyncPeer 删除一台机器的指纹包。
func (s *Store) DeleteSyncPeer(ctx context.Context, machine string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM peer_files WHERE machine=?`, machine); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sync_peers WHERE machine=?`, machine); err != nil {
		return err
	}
	return tx.Commit()
}

// PeerIndex 是跨设备匹配的内存索引。
type PeerIndex struct {
	ByHash     map[string][]string // hash → 机器名
	ByPathSize map[string][]string // path|size → 机器名
}

// LoadPeerIndex 一次性把 peer_files 读进内存；数据量大时由调用方控制频率。
func (s *Store) LoadPeerIndex(ctx context.Context) (*PeerIndex, error) {
	idx := &PeerIndex{ByHash: map[string][]string{}, ByPathSize: map[string][]string{}}
	rows, err := s.db.QueryContext(ctx, `SELECT machine,path,size,COALESCE(hash,'') FROM peer_files`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var machine, path, hash string
		var size int64
		if err := rows.Scan(&machine, &path, &size, &hash); err != nil {
			return nil, err
		}
		if hash != "" {
			idx.ByHash[hash] = appendUnique(idx.ByHash[hash], machine)
		}
		k := pathSizeKey(path, size)
		idx.ByPathSize[k] = appendUnique(idx.ByPathSize[k], machine)
	}
	return idx, rows.Err()
}

func pathSizeKey(path string, size int64) string {
	return path + "|" + strconv.FormatInt(size, 10)
}

// appendUnique 避免同一台机器因同 hash 多文件重复出现。
func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// MatchPeer 返回与文件匹配的机器名（优先 hash，退化为 path+size）。
func (idx *PeerIndex) MatchPeer(path string, size int64, hash string) string {
	if idx == nil {
		return ""
	}
	if hash != "" {
		if ms := idx.ByHash[hash]; len(ms) > 0 {
			return ms[0]
		}
	}
	if ms := idx.ByPathSize[pathSizeKey(path, size)]; len(ms) > 0 {
		return ms[0]
	}
	return ""
}

// PeerExportRow 是导出时从 files 表读取的一行。
type PeerExportRow struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Hash  string `json:"hash,omitempty"`
	Quick string `json:"quick,omitempty"`
}

// ExportPeerRows 逐行回调导出指纹（Web 层可边查边写，避免一次性装进内存）。
func (s *Store) ExportPeerRows(ctx context.Context, fn func(PeerExportRow) error) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path,size,COALESCE(hash,''),COALESCE(quick,'') FROM files
		WHERE status='active' AND ((hash IS NOT NULL AND hash<>'' AND hash NOT LIKE 'ERR%')
		OR (quick IS NOT NULL AND quick<>'')) ORDER BY path`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var n int64
	for rows.Next() {
		var r PeerExportRow
		if err := rows.Scan(&r.Path, &r.Size, &r.Hash, &r.Quick); err != nil {
			return n, err
		}
		if err := fn(r); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// PeerPackageHeader 是指纹包的固定头（版本号用于未来兼容）。
const PeerPackageVersion = 1

// ValidatePeerMachine 校验机器名长度与空值，返回中文错误。
func ValidatePeerMachine(machine string) error {
	if machine == "" {
		return fmt.Errorf("机器名不能为空")
	}
	if len([]rune(machine)) > 64 {
		return fmt.Errorf("机器名最多 64 个字符")
	}
	return nil
}
