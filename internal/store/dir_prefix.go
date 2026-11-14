// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"path/filepath"
	"strings"
)

// 本文件是「目录级移动」的索引同步层（v3.14.0）。
//
// 待定问题 #6 的硬要求：任何目录级操作必须按前缀同步 files 与 health_results，
// 不能等下次全量扫描收敛。移动一个目录等于重写它下面全部索引行的 path 前缀——
// 逐行 UPDATE 会把磁盘读满，这里按目录分批拉路径、复用 RenameFilePathsBatch 的
// 「先删新路径、再 UPDATE 旧路径」写法，name/ext 不变（文件名没改，只是换了位置）。

// DirMoveSummary 是一次目录前缀改写的统计。
type DirMoveSummary struct {
	Files   int // 索引里被改写 path 的文件行数
	Missing int // 磁盘上已不在索引里的文件（移动前就没入库，如零字节文件）
}

// CountUnderPrefix 返回 files 表里位于 root（含子目录）下的行数，供移动预览显示规模。
func (s *Store) CountUnderPrefix(ctx context.Context, root string) (int, error) {
	prefix := root + string(filepath.Separator)
	var n int
	// 不能用 LIKE 做前缀比较：SQLite 的 LIKE 对 ASCII 默认不区分大小写，
	// /vol1/Video 会连带命中 /vol1/video，目录移动时把另一个目录的索引一起改走。
	// substr 按字符截取，列默认 BINARY 排序，等价于按字节精确比较前缀。
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM files WHERE path = ? OR substr(path, 1, length(?)) = ?`,
		root, prefix, prefix).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ListPathsUnderPrefix 分批返回 files 表里路径位于 root（含子目录）下的全部路径。
// keyset 分页（按 id），目录下有几万个文件时代价恒定。
func (s *Store) ListPathsUnderPrefix(ctx context.Context, root string, afterID int64, limit int) ([]FilePathRow, error) {
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	prefix := root + string(filepath.Separator)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, path FROM files
		 WHERE id > ? AND (path = ? OR substr(path, 1, length(?)) = ?)
		 ORDER BY id LIMIT ?`, afterID, root, prefix, prefix, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]FilePathRow, 0, limit)
	for rows.Next() {
		var r FilePathRow
		if err := rows.Scan(&r.ID, &r.Path); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EscapeLike 把 LIKE 通配符转义掉：路径与关键字里合法出现 % 和 _，不能当模式解释。
// v3.17.4（F-12）：从私有函数导出，Web 层的降级搜索也复用它，避免各写一份。
func EscapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// RewriteDirPrefix 把 files 表里位于 oldDir 下的行按映射改到 newDir 下，
// 返回改写统计。name/ext 不重算——移动目录不改文件名。
func (s *Store) RewriteDirPrefix(ctx context.Context, oldDir, newDir string) (*DirMoveSummary, error) {
	sum := &DirMoveSummary{}
	// 一批一批拉：每批在独立事务里改写，避免一个几万行的大事务把 WAL 顶满。
	const batch = 2000
	var after int64
	for {
		rows, err := s.ListPathsUnderPrefix(ctx, oldDir, after, batch)
		if err != nil {
			return sum, err
		}
		if len(rows) == 0 {
			return sum, nil
		}
		pairs := make([][2]string, 0, len(rows))
		for _, r := range rows {
			after = r.ID
			rel := strings.TrimPrefix(r.Path, oldDir)
			pairs = append(pairs, [2]string{r.Path, filepath.Join(newDir, rel)})
		}
		missing, err := s.RenameFilePathsBatch(ctx, pairs)
		if err != nil {
			return sum, err
		}
		sum.Files += len(pairs) - len(missing)
		sum.Missing += len(missing)
		if len(rows) < batch {
			return sum, nil
		}
	}
}

// RewriteHealthDirPrefix 把 health_results 里位于 oldDir 下的行改到 newDir 下。
// 与 files 不同，这里直接 UPDATE：健康行没有唯一约束挡路，前缀替换一次搞定。
//
// substr/length 都按字符数计数：目录名含中文时 Go 的 len() 是字节数，
// 直接把数字算好传进去会算错起点，所以把前缀交给 SQLite 自己量。
// 新前缀补上结尾分隔符：substr 从「前缀 + 1」起取，剥掉的正是旧分隔符。
func (s *Store) RewriteHealthDirPrefix(ctx context.Context, oldDir, newDir string) (int64, error) {
	sep := string(filepath.Separator)
	oldPrefix := oldDir + sep
	newPrefix := newDir + sep
	res, err := s.db.ExecContext(ctx,
		`UPDATE health_results SET path = ? || substr(path, length(?) + 1)
		 WHERE path = ? OR substr(path, 1, length(?)) = ?`,
		newPrefix, oldPrefix, oldDir, oldPrefix, oldPrefix)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DropHealthUnderPrefix 删除位于 root（含子目录）下的健康结论。
// 目录移走后这些结论指向旧路径，留着只会出现点不动的死行。
func (s *Store) DropHealthUnderPrefix(ctx context.Context, root string) (int64, error) {
	prefix := root + string(filepath.Separator)
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM health_results WHERE path = ? OR substr(path, 1, length(?)) = ?`,
		root, prefix, prefix)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteUnderPrefix 删除 files 表里位于 root（含子目录）下的行，返回删除数。
// 撤销目录移动且部分文件已被其它操作改走时，兜底清掉仍指向旧位置的残留行。
func (s *Store) DeleteUnderPrefix(ctx context.Context, root string) (int64, error) {
	prefix := root + string(filepath.Separator)
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM files WHERE path = ? OR substr(path, 1, length(?)) = ?`,
		root, prefix, prefix)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err == nil && n > 0 {
		// v3.17.4（F-05）：目录级删除会一次性少掉大量路径，搜索快照必须失效。
		s.dataVersion.Add(1)
	}
	return n, err
}
