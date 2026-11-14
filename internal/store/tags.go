// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"verify/internal/fname"
)

// 标签取值来源（v2.7.0）。
// 只支持两种自动来源：目录名最省事且零输入，文件名正则可以表达"剧集名/年份"这类规则；
// 手动给单个文件打标签留到后续版本，避免本轮 UI 膨胀。
const (
	TagSourceDirname       = "dirname"        // 用文件所在目录名
	TagSourceFilenameRegex = "filename_regex" // 用文件名正则的第一捕获组
)

// compileTagPattern 把标签取值规则编译成正则；目录名来源不需要正则。
func compileTagPattern(source, pattern string) (*regexp.Regexp, error) {
	if source != TagSourceFilenameRegex {
		return nil, nil
	}
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("文件名规则不能为空")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("文件名规则无效（%v）", err)
	}
	// 必须有捕获组：没有捕获组只能拿到整段匹配，用户通常拿不到想要的值
	if re.NumSubexp() < 1 {
		return nil, fmt.Errorf("文件名规则需要包含 1 个括号捕获组")
	}
	return re, nil
}

// tagValueOf 按规则从路径/文件名算出一个标签值；空串表示这个文件不参与该标签。
func tagValueOf(path, name, source string, re *regexp.Regexp) string {
	if source == TagSourceFilenameRegex {
		if re == nil {
			return ""
		}
		m := re.FindStringSubmatch(name)
		if len(m) < 2 {
			return ""
		}
		return strings.TrimSpace(m[1])
	}
	dir := filepath.Dir(path)
	base := filepath.Base(dir)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return ""
	}
	return strings.TrimSpace(base)
}

// ComputeFileTags 按标签定义重算所有文件的标签值（v2.7.0），返回写入的文件数。
// 先清空该标签的旧值再写入，重复调用结果一致；读写分成两步，
// 避免在遍历结果集的同时对同一连接发起写操作。
func (s *Store) ComputeFileTags(ctx context.Context, tagID int64) (int, error) {
	var source, pattern string
	err := s.db.QueryRowContext(ctx, `SELECT value_source, value_pattern FROM video_tags WHERE id=?`, tagID).
		Scan(&source, &pattern)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("标签不存在")
	}
	if err != nil {
		return 0, err
	}
	re, err := compileTagPattern(source, pattern)
	if err != nil {
		return 0, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, path, name FROM files WHERE status='active'`)
	if err != nil {
		return 0, err
	}
	type valRow struct {
		id  int64
		val string
	}
	var vals []valRow
	for rows.Next() {
		var id int64
		var path, name string
		if err := rows.Scan(&id, &path, &name); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if v := tagValueOf(path, name, source, re); v != "" {
			vals = append(vals, valRow{id, v})
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// 分批提交：百万文件时单条大事务会长时间占住 WAL，界面查询会一起排队
	const batch = 5000
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM file_tags WHERE tag_id=?`, tagID); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	for i, v := range vals {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO file_tags(file_id, tag_id, value) VALUES(?,?,?)`,
			v.id, tagID, v.val); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if (i+1)%batch == 0 {
			if err := tx.Commit(); err != nil {
				return 0, err
			}
			if tx, err = s.db.BeginTx(ctx, nil); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(vals), nil
}

// TagGroups 按标签值聚组（v2.7.0）。只做辅助展示，不参与任何删除判定。
// 返回结构与精确分组一致，前端可以直接复用分组列表组件。
func (s *Store) TagGroups(ctx context.Context, tagID int64, q GroupQuery) ([]Group, int64, error) {
	groups, err := s.buildTagGroups(ctx, tagID)
	if err != nil {
		return nil, 0, err
	}
	total := int64(len(groups))
	limit, offset, _ := q.limit()
	if offset >= len(groups) {
		return []Group{}, total, nil
	}
	end := offset + limit
	if end > len(groups) {
		end = len(groups)
	}
	return groups[offset:end], total, nil
}

// TagGroupMembers 按分组键 t:{tagID}:{规范化值} 取组内全部路径（v3.10.8，P1-3）。
// 「整组隔离前至少保留一个」需要在引擎侧重算成员，标签值的规范化规则只在 store 里实现一次。
func (s *Store) TagGroupMembers(ctx context.Context, tagID int64, key string) ([]string, error) {
	groups, err := s.buildTagGroups(ctx, tagID)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if g.Key != key {
			continue
		}
		paths := make([]string, 0, len(g.Files))
		for _, f := range g.Files {
			paths = append(paths, f.Path)
		}
		return paths, nil
	}
	return nil, nil
}

// buildTagGroups 计算某个标签的全部聚合分组（不分页）。
func (s *Store) buildTagGroups(ctx context.Context, tagID int64) ([]Group, error) {
	var mode string
	var join int
	err := s.db.QueryRowContext(ctx, `SELECT match_mode, join_group FROM video_tags WHERE id=?`, tagID).
		Scan(&mode, &join)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("标签不存在")
	}
	if err != nil {
		return nil, err
	}
	// 仅显示 / 不参与分组的标签不产出分组，避免用户误以为这些组可以拿来删
	if mode == TagModeDisplayOnly || join == 0 {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx, `SELECT f.id, f.path, f.volume, f.name, f.size, f.alloc, f.mtime, f.ctime, f.nlink, f.inode, f.ext, ft.value
		FROM file_tags ft JOIN files f ON f.id=ft.file_id
		WHERE ft.tag_id=? AND f.status='active' AND ft.value<>''`, tagID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	byKey := map[string][]FileRow{}
	nameOf := map[string]string{}
	for rows.Next() {
		var f FileRow
		var val string
		if err := rows.Scan(&f.ID, &f.Path, &f.Volume, &f.Name, &f.Size, &f.Alloc, &f.Mtime, &f.Ctime,
			&f.Nlink, &f.Inode, &f.Ext, &val); err != nil {
			return nil, err
		}
		k := tagGroupKey(mode, val)
		if k == "" {
			continue
		}
		if _, ok := nameOf[k]; !ok {
			nameOf[k] = strings.TrimSpace(val)
		}
		byKey[k] = append(byKey[k], f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 相似模式在规范化键之上再做近似合并，值种类少时才做，避免超宽标签把界面卡住
	if mode == TagModeSimilar {
		byKey, nameOf = mergeSimilarTagKeys(byKey, nameOf)
	}

	groups := make([]Group, 0, len(byKey))
	for k, files := range byKey {
		// 只有一个文件的标签值不构成重复，不展示
		if len(files) < 2 {
			continue
		}
		var sum, maxSize int64
		for _, f := range files {
			occ := f.AllocSize()
			sum += occ
			if occ > maxSize {
				maxSize = occ
			}
		}
		g := Group{
			Key:       "t:" + strconv.FormatInt(tagID, 10) + ":" + k,
			Name:      nameOf[k],
			Count:     len(files),
			TotalSize: sum,
			Waste:     sum - maxSize,
			Files:     files,
		}
		if g.Waste < 0 {
			g.Waste = 0
		}
		groups = append(groups, g)
	}
	// 与其它分组页一致：可回收空间大的排前面
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Waste != groups[j].Waste {
			return groups[i].Waste > groups[j].Waste
		}
		return groups[i].Name < groups[j].Name
	})
	return groups, nil
}

// tagGroupKey 把标签值转成聚组键；空值不参与。
func tagGroupKey(mode, v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if mode == TagModeExactWS || mode == TagModeSimilar {
		return strings.ToLower(removeAllSpace(v))
	}
	return v
}

// removeAllSpace 去掉所有空白（含全角空格），用于"含空白变体"匹配。
func removeAllSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// AttachFileTags 给分组里的文件批量补上标签值（v2.8.0），供结果列表显示徽标。
// 只查当前页涉及的文件 id，每 500 个一批，避免触碰 SQLite 的变量数上限。
func (s *Store) AttachFileTags(ctx context.Context, groups []Group) error {
	ids := make([]int64, 0, len(groups)*4)
	seen := make(map[int64]bool, len(groups)*4)
	for i := range groups {
		for _, f := range groups[i].Files {
			if !seen[f.ID] {
				seen[f.ID] = true
				ids = append(ids, f.ID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}

	type tagged struct {
		fileID int64
		tv     TagValue
	}
	var all []tagged
	const batch = 500
	for start := 0; start < len(ids); start += batch {
		end := start + batch
		if end > len(ids) {
			end = len(ids)
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", end-start), ",")
		args := make([]any, 0, end-start)
		for _, id := range ids[start:end] {
			args = append(args, id)
		}
		// #nosec G202 -- 占位符由固定 "?," 拼接，值全部走参数绑定
		rows, err := s.db.QueryContext(ctx, `SELECT ft.file_id, t.name, ft.value
			FROM file_tags ft JOIN video_tags t ON t.id=ft.tag_id
			WHERE ft.file_id IN (`+ph+`) AND ft.value<>'' AND t.join_group=1
			ORDER BY t.sort_order, t.id`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r tagged
			if err := rows.Scan(&r.fileID, &r.tv.Name, &r.tv.Value); err != nil {
				_ = rows.Close()
				return err
			}
			all = append(all, r)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	byFile := make(map[int64][]TagValue, len(seen))
	for _, r := range all {
		byFile[r.fileID] = append(byFile[r.fileID], r.tv)
	}
	for i := range groups {
		// 先取出切片再按下标访问，避免多层组合索引触发 gosec G602 误报
		files := groups[i].Files
		for j := range files {
			if tags, ok := byFile[files[j].ID]; ok {
				files[j].Tags = tags
			}
		}
	}
	return nil
}

// mergeSimilarTagKeys 把相似文本的键合并（相似模式）。
// 编辑距离 ≤1 交给 fname.NearGroups（删字签名桶，避免 O(n²) 配对）；
// 包含关系（"某剧集" vs "某剧集 第二季"）在标签值里很常见，单独补一轮合并。
// 键超过 2000 个时放弃合并，防止误设的超宽标签把界面卡死。
func mergeSimilarTagKeys(byKey map[string][]FileRow, nameOf map[string]string) (map[string][]FileRow, map[string]string) {
	if len(byKey) < 2 {
		return byKey, nameOf
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 2000 {
		return byKey, nameOf
	}
	parent := make([]int, len(keys))
	for i := range parent {
		parent[i] = i
	}
	// find 只做路径压缩，不递归调用自身，可以直接合并声明与赋值（staticcheck S1021）
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		if ra, rb := find(a), find(b); ra != rb {
			parent[rb] = ra
		}
	}
	for _, g := range fname.NearGroups(keys) {
		for _, idx := range g[1:] {
			union(g[0], idx)
		}
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			a, b := keys[i], keys[j]
			if len(a) > len(b) {
				a, b = b, a
			}
			// 单字包含会过度合并（"剧"命中一堆），至少两个字才认
			if len([]rune(a)) >= 2 && strings.Contains(b, a) {
				union(i, j)
			}
		}
	}
	out := map[string][]FileRow{}
	outName := map[string]string{}
	for i, k := range keys {
		root := keys[find(i)]
		out[root] = append(out[root], byKey[k]...)
		if _, ok := outName[root]; !ok {
			outName[root] = nameOf[k]
		}
	}
	return out, outName
}
