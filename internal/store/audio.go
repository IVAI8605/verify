// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"verify/internal/phash"
)

// AudioExts 是纯音频查重的扩展名（不含点、小写）。与视频扩展名无交集。
var AudioExts = []string{"mp3", "flac", "m4a", "aac", "ogg", "wav", "wma", "ape"}

func audioExtSQL() string {
	return "'" + strings.Join(AudioExts, "','") + "'"
}

// AudioResult 是一次音频指纹计算的落库结果；Err 复用 video_error 字段（列不改名）。
type AudioResult struct {
	ID    int64
	Audio []byte
	Err   string
	// Duration 是文件总时长（秒，ffprobe 结果）。0 表示探测失败或旧数据，
	// 此时分组不做时长约束（v3.10.10，P2-7）。
	Duration float64
}

// AudioCandidates 拉取需要计算音频指纹的纯音频文件：
// 无指纹且无错误（video_error 复用）；处理失败且文件未变化的不重试。
func (s *Store) AudioCandidates(ctx context.Context, limit int) ([]FileRow, error) {
	// #nosec G202 -- 扩展名清单来自 audioExts 白名单常量集合
	// 注意：SQL 字符串里不能写 Go 注释（v1.42.0 修复，BUG-02）。
	// 注释会原样进入 SQL，SQLite 不认 "//"，直接报 near "/" 语法错误。
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files
		WHERE status='active' AND ext IN (`+audioExtSQL()+`)
		AND audio IS NULL AND (video_error IS NULL OR video_error='')
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

// CountAudioCandidates 返回待分析音频文件数（进度条用）。
func (s *Store) CountAudioCandidates(ctx context.Context) (int64, error) {
	var n int64
	// #nosec G202 -- 扩展名清单来自 audioExts 白名单常量集合
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files
		WHERE status='active' AND ext IN (`+audioExtSQL()+`)
		AND audio IS NULL AND (video_error IS NULL OR video_error='')`).Scan(&n)
	return n, err
}

// SetAudioBatch 批量写入音频指纹（audio 列与视频共用，维度一致，MigrateFingerprintDim 无需改动）。
func (s *Store) SetAudioBatch(ctx context.Context, items []AudioResult) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for _, it := range items {
		if it.Err != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE files SET audio=NULL, video_error=?, video_at=?, video_tries=video_tries+1 WHERE id=?`,
				it.Err, now, it.ID); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET audio=?, video_error=NULL, video_at=?, video_tries=video_tries+1,
			duration=CASE WHEN ? > 0 THEN ? ELSE duration END WHERE id=?`,
			it.Audio, now, it.Duration, it.Duration, it.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AudioFP 是参与聚类的音频行。
type AudioFP struct {
	ID     int64
	Path   string
	Volume string
	Name   string
	Size   int64
	// Alloc 是实际占用字节数（0 表示旧库未采集），只用于可回收空间估算。
	Alloc int64
	Mtime int64
	Ctime int64
	Nlink int64
	Inode uint64
	Hash  string
	Audio []byte
	// Duration 是音频时长（秒）；旧库可能为 0，此时不参与时长容差判断。
	Duration float64
}

// AudioFingerprints 拉全部已算出指纹的纯音频行。
func (s *Store) AudioFingerprints(ctx context.Context) ([]AudioFP, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,path,volume,name,size,alloc,mtime,ctime,nlink,inode,hash,duration,audio FROM files
		WHERE status='active' AND ext IN (`+audioExtSQL()+`) AND audio IS NOT NULL AND (video_error IS NULL OR video_error='')
		ORDER BY path`) // #nosec G202 -- 扩展名清单来自 audioExts 白名单常量集合，外部输入不进入 SQL
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AudioFP
	for rows.Next() {
		var a AudioFP
		var hash sql.NullString
		var dur sql.NullFloat64
		if err := rows.Scan(&a.ID, &a.Path, &a.Volume, &a.Name, &a.Size, &a.Alloc, &a.Mtime, &a.Ctime, &a.Nlink, &a.Inode, &hash, &dur, &a.Audio); err != nil {
			return nil, err
		}
		a.Hash = hash.String
		a.Duration = dur.Float64
		out = append(out, a)
	}
	return out, rows.Err()
}

// MigrateAudioWindow 在纯音频比较时长变化时清空旧指纹，避免 5 秒/15 秒/全片
// 的指纹混在一起比较（v2.3.0）。meta 记录当前档位，重复调用不会重复清空。
func (s *Store) MigrateAudioWindow(ctx context.Context, seconds int) error {
	const key = "audio_window_sec"
	var cur string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&cur)
	if err == nil && cur == fmt.Sprint(seconds) {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE files SET audio=NULL, video_error=NULL, video_at=0, video_tries=0
		WHERE status='active' AND ext IN (`+audioExtSQL()+`)`) // #nosec G202 -- 扩展名清单来自常量集合
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, fmt.Sprint(seconds))
	return err
}

// audioGroupRow 把 AudioFP 转成通用 Group 文件行（audio 指纹不进 FileRow，文件信息按需取列）。
func (s *Store) audioGroupFiles(ctx context.Context, ids []int64) ([]FileRow, error) {
	var out []FileRow
	for _, id := range ids {
		f, err := s.GetFileByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if f != nil {
			out = append(out, *f)
		}
	}
	return out, nil
}

// audioDurationTol 是音频分组的时长容差（秒），与视频分组保持同一口径。
const audioDurationTol = 2.5

// durationCompatible 判断两条音频的时长是否接近到可以判为「同一段录音」。
// 任一侧时长未知（0 = 旧库数据或 ffprobe 失败）时不拦截——旧数据没有时长列，
// 若按未知即拒绝会让升级后的分组整体消失（v3.10.10，P2-7）。
func durationCompatible(a, b float64) bool {
	if a <= 0 || b <= 0 {
		return true
	}
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= audioDurationTol
}

// AudioGroups 纯音频聚类：与组内代表元的音频 Cosine 相似度 ≥ audioThreshold 判相似。
// n 较小时 O(n²) 全对比（5000 首 ≈ 千万级 Cosine，秒级）；超大库降级为「指纹整串相同」分桶。
// 字节级相同的文件已被精确查重覆盖，这里专抓「不同编码/不同容器、同一段录音」。
// 判定口径（v3.10.10，P2-7）：指纹相似 **且** 时长相差 ≤2.5 秒；只看指纹会把
// 恒定频谱素材（测试音、白噪声、工频底噪）并成一组，用户可能只留一份删掉另一个。
func (s *Store) AudioGroups(ctx context.Context, audioThr float64) ([]Group, int64, error) {
	fps, err := s.AudioFingerprints(ctx)
	if err != nil {
		return nil, 0, err
	}
	const bruteLimit = 20000
	var cluster [][]AudioFP
	if len(fps) <= bruteLimit {
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
		for i := 0; i < len(fps); i++ {
			for j := i + 1; j < len(fps); j++ {
				// 先比时长再算 Cosine：时长不符的配对直接跳过，顺便省掉余弦计算。
				if !durationCompatible(fps[i].Duration, fps[j].Duration) {
					continue
				}
				if phash.Cosine(fps[i].Audio, fps[j].Audio) >= audioThr {
					union(i, j)
				}
			}
		}
		m := make(map[int][]AudioFP)
		for i := range fps {
			r := find(i)
			m[r] = append(m[r], fps[i])
		}
		for _, v := range m {
			if len(v) >= 2 {
				cluster = append(cluster, v)
			}
		}
	} else {
		// 降级：指纹整串相同分桶（保守，不误报）。
		// 桶内仍要按时长再分段，否则同指纹不同时长的素材会被并组（P2-7 同一口径）。
		bm := make(map[string][]AudioFP)
		for _, f := range fps {
			key := string(f.Audio)
			bm[key] = append(bm[key], f)
		}
		for _, v := range bm {
			if len(v) < 2 {
				continue
			}
			sort.Slice(v, func(i, j int) bool { return v[i].Duration < v[j].Duration })
			// 与段首比较而非相邻比较：相邻链式会传递合并，3 秒的提示音可能被
			// 一路「接力」并到 3600 秒的录音里，段首比较能把跨度限制在容差内。
			var seg []AudioFP
			flush := func() {
				if len(seg) >= 2 {
					cluster = append(cluster, seg)
				}
			}
			for _, f := range v {
				if len(seg) > 0 && !durationCompatible(seg[0].Duration, f.Duration) {
					flush()
					seg = nil
				}
				seg = append(seg, f)
			}
			flush()
		}
	}

	var groups []Group
	for _, members := range cluster {
		rep := members[0]
		var files []FileRow
		ids := make([]int64, 0, len(members))
		var maxsize int64
		for _, m := range members {
			ids = append(ids, m.ID)
			// v3.10.8：稀疏文件的逻辑大小可能远大于实际占用，Waste 必须按占用算。
			occ := m.Size
			if m.Alloc > 0 {
				occ = m.Alloc
			}
			if occ > maxsize {
				maxsize = occ
			}
		}
		files, err := s.audioGroupFiles(ctx, ids)
		if err != nil {
			return nil, 0, err
		}
		if len(files) < 2 {
			continue
		}
		var totalSize int64
		for _, f := range files {
			totalSize += f.AllocSize()
		}
		groups = append(groups, Group{
			Key:       "audio:" + fmt.Sprintf("%x", sha256.Sum256([]byte(rep.Path)))[:16],
			Name:      files[0].Name,
			Count:     len(files),
			TotalSize: totalSize,
			Waste:     totalSize - maxsize,
			Files:     files,
		})
	}
	return groups, int64(len(groups)), nil
}

// FolderGroups 文件夹整体查重（单层目录签名，设计文档 8.2 第一版）：
// 按直接父目录聚合 (name,size,hash)，签名 = 按文件名排序后拼接的 SHA-256。
// 只包含已算出 hash 的文件（有未哈希文件的目录整目录跳过，避免误判）；
// 目录内文件数 ≥ 2 才参与；子目录内文件属于子目录的签名，不进父目录（按直接父目录聚合天然满足）。
func (s *Store) FolderGroups(ctx context.Context) ([]Group, int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, name, size, hash FROM files
		WHERE status='active' AND hash IS NOT NULL AND hash != ''`)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	type entry struct {
		path, name, hash string
		size             int64
	}
	dirs := make(map[string][]entry)
	for rows.Next() {
		var p, name, hash string
		var size int64
		if err := rows.Scan(&p, &name, &size, &hash); err != nil {
			return nil, 0, err
		}
		dir := filepath.Dir(p)
		dirs[dir] = append(dirs[dir], entry{path: p, name: name, hash: hash, size: size})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// 一次拉全量行（供组装 FileRow），路径 → 行
	rowMap := make(map[string]*FileRow)
	r2, err := s.db.QueryContext(ctx, fileCols+` FROM files WHERE status='active'`)
	if err != nil {
		return nil, 0, err
	}
	for r2.Next() {
		f, err := s.scanFile(r2)
		if err != nil {
			_ = r2.Close()
			return nil, 0, err
		}
		rowMap[f.Path] = f
	}
	if err := r2.Err(); err != nil {
		_ = r2.Close()
		return nil, 0, err
	}
	_ = r2.Close()

	// 签名 → 同签名目录集合
	type sigGroup struct {
		dirs  map[string]bool
		files []FileRow
		total int64
		maxsz int64
	}
	sigMap := make(map[string]*sigGroup)
	for dir, entries := range dirs {
		if len(entries) < 2 {
			continue
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
		h := sha256.New()
		for _, e := range entries {
			line := e.name + "|" + strconv.FormatInt(e.size, 10) + "|" + e.hash + "\n"
			_, _ = h.Write([]byte(line))
		}
		sig := fmt.Sprintf("%x", h.Sum(nil))[:16]
		g := sigMap[sig]
		if g == nil {
			g = &sigGroup{dirs: make(map[string]bool)}
			sigMap[sig] = g
		}
		g.dirs[dir] = true
		for _, e := range entries {
			if f := rowMap[e.path]; f != nil {
				g.files = append(g.files, *f)
				g.total += f.AllocSize()
				if occ := f.AllocSize(); occ > g.maxsz {
					g.maxsz = occ
				}
			}
		}
	}
	var groups []Group
	for sig, g := range sigMap {
		// 必须 ≥2 个**不同目录**互为副本；同一目录内的重复文件属于精确查重范畴，
		// 不构成「文件夹重复」（否则会出现「互为副本的 1 个文件夹」的语义错误）。
		// v3.17.5（F-19）：同时要求成员数 ≥2——竞态窗口里文件行可能已被删掉，
		// 只剩 1 个成员时 Files[0] 虽然存在，但展示与后续操作都是单文件重复组。
		if len(g.dirs) < 2 || len(g.files) < 2 {
			continue
		}
		// 目录路径取组内任一文件的父目录
		dir := filepath.Dir(g.files[0].Path)
		groups = append(groups, Group{
			Key:       "folder:" + sig,
			Name:      dir,
			Count:     len(g.files),
			TotalSize: g.total,
			Waste:     g.total - g.maxsz,
			Files:     g.files,
		})
	}
	return groups, int64(len(groups)), nil
}
