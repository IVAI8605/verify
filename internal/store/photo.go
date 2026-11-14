// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"verify/internal/phash"
	"verify/internal/photo"
)

// ImageExts 是参与相似图片查重的扩展名（不含点、小写）。
// HEIC/AVIF/JXL/RAW/SVG 无纯 Go 解码器或 dHash 不适用，明确不支持；
// 该清单须与 README、设置页提示保持一致。
var ImageExts = []string{"jpg", "jpeg", "jpe", "png", "gif", "bmp", "dib", "tif", "tiff", "webp"}

func imageExtSQL() string {
	return "'" + strings.Join(ImageExts, "','") + "'"
}

// ImgResult 是一次图片指纹计算的落库结果。
// Err 非空表示该文件无法处理（格式不支持/超尺寸/损坏），img_hash 保持 NULL。
type ImgResult struct {
	ID    int64
	Hash  string // "h0|h1|h2|h3" 十六进制；失败时为空
	Err   string
	W, H  int
	Size  int64
	Mtime int64
}

// EnsureImgAlgo 在图片指纹算法版本变化时清空全部旧指纹。
// 返回 true 表示发生了清空（调用方记日志）。
func (s *Store) EnsureImgAlgo(ctx context.Context, version int) (bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='img_algo'`).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if v == strconv.Itoa(version) {
		return false, nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE files SET img_hash=NULL, img_error=NULL, img_at=0, img_size=0, img_mtime=0
		WHERE img_hash IS NOT NULL OR img_error IS NOT NULL`); err != nil {
		return false, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('img_algo',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(version))
	return true, err
}

// ImgCandidates 拉取需要计算图片指纹的文件：
// 从未处理过（img_at=0），或 size/mtime 已变化导致指纹失效（probe_cache 同款坑：
// 只看 img_at 有无值的话，同名替换后的文件永远拿旧哈希比对）。
// 处理失败且文件未变化的行不重试，避免坏文件反复消耗 IO。
func (s *Store) ImgCandidates(ctx context.Context, limit int) ([]FileRow, error) {
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files
		WHERE status='active' AND ext IN (`+imageExtSQL()+`)
		AND (img_at=0 OR img_size!=size OR img_mtime!=mtime)
		ORDER BY path LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []FileRow
	for rows.Next() {
		f, err := s.scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// SetImgBatch 批量写入图片指纹结果（单事务）。
func (s *Store) SetImgBatch(ctx context.Context, items []ImgResult) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE files
		SET img_hash=?, img_error=?, img_w=?, img_h=?, img_at=strftime('%s','now'), img_size=?, img_mtime=?
		WHERE id=?`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, it := range items {
		var hash any
		if it.Hash != "" {
			hash = it.Hash
		}
		if _, err := stmt.ExecContext(ctx, hash, it.Err, it.W, it.H, it.Size, it.Mtime, it.ID); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("写入图片指纹 %d: %w", it.ID, err)
		}
	}
	_ = stmt.Close()
	return tx.Commit()
}

// PhotoFP 是参与聚类的图片行：4 个方向哈希 + 最小行数据。
type PhotoFP struct {
	ID     int64
	Path   string
	Hashes [4]uint64
}

// parseImgHashes 解析 "h0|h1|h2|h3"。
func parseImgHashes(s string) ([4]uint64, bool) {
	var out [4]uint64
	parts := strings.Split(s, "|")
	if len(parts) != 4 {
		return out, false
	}
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 16, 64)
		if err != nil {
			return out, false
		}
		out[i] = v
	}
	return out, true
}

// PhotoGroups 相似图片聚类：
//  1. 拉全部有效图片指纹，按「单个哈希值 + 镜像哈希」建倒排索引分桶
//     （同一张图在另一份拷贝里可能整体旋转过，4 哈希循环移位后仍共享部分单值；
//     任何 O(n²) 全对比在十万张图上不可行）；
//  2. 同桶元素并查集聚类；
//  3. 精确阶段以组内首个元素为代表，保留与代表元距离 ≤ threshold 的成员，
//     距离超出者不进组（链式相似允许漏报，不误报）。
func (s *Store) PhotoGroups(ctx context.Context, threshold int) ([]Group, int64, error) {
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files
		WHERE status='active' AND ext IN (`+imageExtSQL()+`)
		AND img_hash IS NOT NULL AND (img_error IS NULL OR img_error='')
		ORDER BY path`)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	var fps []PhotoFP
	var frows []FileRow
	for rows.Next() {
		f, err := s.scanFile(rows)
		if err != nil {
			return nil, 0, err
		}
		if f.ImgHash == "" {
			continue
		}
		h, ok := parseImgHashes(f.ImgHash)
		if !ok {
			continue
		}
		fps = append(fps, PhotoFP{ID: f.ID, Path: f.Path, Hashes: h})
		frows = append(frows, *f)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// 并查集
	parent := make([]int, len(fps))
	for i := range parent {
		parent[i] = i
	}
	var find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) { parent[find(a)] = find(b) }

	index := make(map[uint64][]int)
	for i, fp := range fps {
		for _, h := range fp.Hashes {
			// 倒排索引同时收录镜像哈希：一张图与其水平翻转共享「翻转后的单值哈希」
			index[h] = append(index[h], i)
			index[phash.MirrorBits(h)] = append(index[phash.MirrorBits(h)], i)
		}
	}
	for _, members := range index {
		for k := 1; k < len(members); k++ {
			union(members[0], members[k])
		}
	}

	clusters := make(map[int][]int)
	for i := range fps {
		root := find(i)
		clusters[root] = append(clusters[root], i)
	}

	var groups []Group
	var total int64
	for _, members := range clusters {
		if len(members) < 2 {
			continue
		}
		rep := fps[members[0]]
		var files []FileRow
		var maxsize int64
		for _, mi := range members {
			fp := fps[mi]
			if photo.Distance(rep.Hashes, fp.Hashes) > threshold {
				continue
			}
			files = append(files, frows[mi])
			if occ := frows[mi].AllocSize(); occ > maxsize {
				maxsize = occ
			}
		}
		if len(files) < 2 {
			continue
		}
		var totalSize int64
		for _, f := range files {
			totalSize += f.AllocSize()
		}
		groups = append(groups, Group{
			Key:       fmt.Sprintf("img:%s", files[0].ImgHash),
			Name:      files[0].Name,
			Count:     len(files),
			TotalSize: totalSize,
			Waste:     totalSize - maxsize,
			Files:     files,
		})
	}
	// 组数给 UI 分页/统计用（这里一次性返回，量级可控：组数远小于图片数）
	total = int64(len(groups))
	return groups, total, nil
}
