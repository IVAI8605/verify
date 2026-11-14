// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

// 本文件是 v3.0.0 从 store.go 抽出的「分组查询」逻辑块（设计文档 §1.2）：
// Group/GroupQuery 结构体 + 精确查重、同名分组、大文件 Top N 与关键词搜索。
// 这些查询都依赖 GroupQuery 的过滤条件拼装，放在一起方便对拍；
// 基础读写（Open/migrate/Upsert/Counts 等）留在 store.go。
// 拆分只换文件边界，函数名、签名、SQL 行为均保持与原实现一致。

import (
	"context"
	"strconv"
	"strings"
)

type Group struct {
	Key        string    `json:"key"`
	Kind       string    `json:"kind,omitempty"` // v3.8.0：合并结果页的类型标记
	Name       string    `json:"name"`
	Hash       string    `json:"hash,omitempty"`
	Count      int       `json:"count"`
	TotalSize  int64     `json:"total_size"`
	Waste      int64     `json:"waste"`
	Similarity float64   `json:"similarity,omitempty"`
	Files      []FileRow `json:"files"`
}

type GroupQuery struct {
	MinSize int64
	MaxSize int64 // 0 表示不限
	Ext     string
	Exts    []string // 多扩展名（不含点，小写）；与 Ext 合并生效
	Keyword string
	// Since/Until 按修改时间过滤，纳秒（与 files.mtime 同单位）；0 表示不限。
	// 只用 mtime：Linux 的 ctime 是 inode 变更时间，不是创建时间，暴露会误导。
	Since    int64
	Until    int64
	Roots    []string // 只返回这些目录下的文件分组；空表示不限
	Page     int
	PageSize int
	// Sort/Order 组级排序（v1.14.2 §4.2）：Sort 取 waste|total_size|count|max_mtime|
	// min_mtime|name|max_size，Order 取 asc|desc；白名单映射 ORDER BY，非法回退默认。
	Sort  string
	Order string

	// v2.4.0 高级过滤：
	// DateOnly 仅日期：mtime 截断到当天零点再与 Since/Until 比较。
	DateOnly bool
	// SameDrive 卷前缀（如 "/vol1"），空表示不限；匹配路径前缀 `/volN/`。
	SameDrive string
	// MaxDurDiff 视频/音频时长容差（秒）；<=0 表示不限（由分组算法的默认值兜底）。
	MaxDurDiff float64
	// IgnoreSameFolder 排除同目录内的文件对（影响 video/intro/audio 分组展示）。
	IgnoreSameFolder bool
	// v2.8.0 高级过滤补充：
	// FolderName/FolderDepth 只保留"指定层级目录名等于 FolderName"的文件；
	// FolderDepth 为 0 表示任意层级，否则从卷根算第 N 段（1=/vol6 后的第一层）。
	FolderName  string
	FolderDepth int
	// SimilarName 只保留组内文件名彼此相似的最大簇（编辑距离 ≤1）。
	SimilarName bool
	// SizeTolKB 组内文件大小极差不超过该容差（KB）；0 表示不限。
	SizeTolKB int
	// v2.9.0 标签过滤：只保留命中 TagName 标签（TagValue 非空时还要求值相同）的文件。
	TagName  string
	TagValue string
	// SkipTotal 翻页时跳过 COUNT（v3.10.10，第七轮 P1-2）：
	// 单页原本要跑 COUNT + 分页两趟全表聚合，第二页起复用第一页的总数即可。
	// 跳过时返回的 total 为 -1，调用方必须保留自己已知的总数。
	SkipTotal bool
	// MaxGroups>0 时放宽单页 200 条的上限，供导出/报告一次取全量分组
	// （v3.10.10，第七轮 P1-4），避免每 200 组重复跑一遍全表聚合。
	MaxGroups int
}

// filterCondition 追加内容类过滤条件（大小上限、扩展名、关键词、时间区间）。
// 目录过滤在 rootCondition，两者分开是因为组内文件加载只套目录、不套内容过滤。
func (q GroupQuery) filterCondition(where string, args []any) (string, []any) {
	if q.MaxSize > 0 {
		where += ` AND size<=?`
		args = append(args, q.MaxSize)
	}
	exts := make([]string, 0, len(q.Exts)+1)
	for _, e := range q.Exts {
		e = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(e, ".")))
		if e != "" {
			exts = append(exts, e)
		}
	}
	if q.Ext != "" {
		exts = append(exts, strings.ToLower(strings.TrimPrefix(q.Ext, ".")))
	}
	if len(exts) == 1 {
		where += ` AND ext=?`
		args = append(args, exts[0])
	} else if len(exts) > 1 {
		// 展开成占位符，禁止把值拼进 SQL
		ph := strings.TrimSuffix(strings.Repeat("?,", len(exts)), ",")
		where += ` AND ext IN (` + ph + `)`
		for _, e := range exts {
			args = append(args, e)
		}
	}
	if q.Keyword != "" {
		// v3.17.4（F-12）：关键里的 % 与 _ 是普通字符，必须转义后再当子串匹配。
		where += ` AND name LIKE ? ESCAPE '\'`
		args = append(args, "%"+EscapeLike(q.Keyword)+"%")
	}
	if q.Since > 0 {
		if q.DateOnly {
			// 仅日期：用 SQLite date() 函数把 mtime（纳秒）和 Since 都截到本地日期再比较，
			// 不受传入时间精度影响。Since=0 时不加条件。
			where += ` AND date(mtime/1000000000, 'unixepoch', 'localtime')>=date(?/1000000000, 'unixepoch', 'localtime')`
			args = append(args, q.Since)
		} else {
			where += ` AND mtime>=?`
			args = append(args, q.Since)
		}
	}
	if q.Until > 0 {
		if q.DateOnly {
			where += ` AND date(mtime/1000000000, 'unixepoch', 'localtime')<=date(?/1000000000, 'unixepoch', 'localtime')`
			args = append(args, q.Until)
		} else {
			where += ` AND mtime<?`
			args = append(args, q.Until)
		}
	}
	if q.SameDrive != "" {
		drive := strings.TrimRight(q.SameDrive, "/")
		// v3.17.4（F-12）：卷路径里的 % 与 _ 同样是普通字符。
		where += ` AND path LIKE ? ESCAPE '\'`
		args = append(args, EscapeLike(drive)+"/%")
	}
	// v2.8.0 文件夹名 + 层级：SQL 只做"路径里出现该目录名"的粗筛，转义 LIKE 通配符。
	// 层级无法用 LIKE 精确表达（% 会跨斜杠匹配），由 Web 层内存过滤按段落精修。
	if q.FolderName != "" {
		esc := EscapeLike(q.FolderName)
		where += ` AND path LIKE ? ESCAPE '\'`
		args = append(args, "%/"+esc+"/%")
	}
	return where, args
}

// groupSQLSort 组级排序的 SQL ORDER BY 白名单映射（v1.14.2 §4.2）。
// 列名与方向均受控，外部输入不进 SQL 字符串。

// occupExpr 是「实际占用」的 SQL 表达式（旧库未采集 alloc 时回退逻辑大小）。
// 组头统计、排序与可回收空间必须共用它，否则会出现「列表按实际占用、排序按逻辑大小」（v3.10.10，P2-9）。
const occupExpr = `CASE WHEN alloc>0 THEN alloc ELSE size END`

// v3.25.2（只读排查 P2-5）：返回值只含聚合列，调用方必须再追加 `, hash` / `, lower(name)`
// 作为次序键。当前查询计划借 GROUP BY 索引扫描，等值时通常恰好按分组键有序，
// 但这不是 SQL 保证的：一旦换索引或查询计划，LIMIT/OFFSET 翻页就可能重复或漏行。

func groupSQLSort(sortKey, order string) string {
	dir := "DESC"
	if order == "asc" {
		dir = "ASC"
	}
	switch sortKey {
	case "total_size":
		return "SUM(" + occupExpr + ") " + dir
	case "count":
		return "COUNT(*) " + dir
	case "max_size":
		return "MAX(" + occupExpr + ") " + dir
	case "max_mtime":
		return "MAX(mtime) " + dir
	case "min_mtime":
		return "MIN(mtime) " + dir
	case "name":
		return "MAX(name) " + dir
	default:
		return "(SUM(" + occupExpr + ")-MAX(" + occupExpr + ")) " + dir
	}
}

// rootCondition 生成目录范围过滤条件，避免把未选中的文件夹算进结果。
func (q GroupQuery) rootCondition(where string, args []any) (string, []any) {
	var parts []string
	for _, root := range q.Roots {
		root = strings.TrimRight(strings.TrimSpace(root), "/")
		if root == "" {
			continue
		}
		// LIKE 里的通配符需要转义，否则路径中的 % 或 _ 会扩大匹配范围。
		escaped := EscapeLike(root)
		parts = append(parts, `(path = ? OR path LIKE ? ESCAPE '\')`)
		args = append(args, root, escaped+"/%")
	}
	if len(parts) == 0 {
		return where, args
	}
	return where + ` AND (` + strings.Join(parts, ` OR `) + `)`, args
}

// TopFiles 按大小排序返回已索引文件（v1.14.0 引入 10.1 大文件 Top N）。
// smallest=false 取最大（盘满找大户），true 取最小；范围过滤复用 rootCondition。
// 只覆盖已索引文件——受最小体积与扫描根范围限制。
func (s *Store) TopFiles(ctx context.Context, q GroupQuery, limit int, smallest bool) ([]FileRow, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	where := `status='active'`
	args := []any{}
	where, args = q.rootCondition(where, args)
	order := "DESC"
	if smallest {
		order = "ASC"
	}
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files WHERE `+where+` ORDER BY size `+order+` LIMIT ?`, append(args, limit)...)
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

func (q GroupQuery) limit() (int, int, int) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = 20
	}
	// 交互页单页最多 200 组；导出/报告传 MaxGroups 时放宽到该上限（v3.10.10）
	capSize := 200
	if q.MaxGroups > capSize {
		capSize = q.MaxGroups
	}
	if q.PageSize > capSize {
		q.PageSize = capSize
	}
	return q.PageSize, (q.Page - 1) * q.PageSize, q.Page
}

// minGroupSize 给分组查询一个 1 字节的下限（v3.10.8）。
//
// 0 字节文件的内容必然相同，按哈希会全部挤进同一个「巨型组」：可回收空间恒为 0，
// 结果页却要渲染几千行。它们也不符合用户对「重复文件」的理解，因此不参与分组。
func (q GroupQuery) minGroupSize() int64 {
	if q.MinSize < 1 {
		return 1
	}
	return q.MinSize
}

func (s *Store) ExactGroups(ctx context.Context, q GroupQuery) ([]Group, int64, error) {
	limit, offset, _ := q.limit()
	where := `status='active' AND hash IS NOT NULL AND hash<>'' AND hash NOT LIKE 'ERR%' AND size>=?`
	args := []any{q.minGroupSize()}
	where, args = q.filterCondition(where, args)
	where, args = q.rootCondition(where, args)
	// 翻页跳过 COUNT（v3.10.10，第七轮 P1-2）：第二页起复用第一页的总数，total=-1 表示未计算
	var total int64 = -1
	// 大小容差必须在分组聚合上判断（组内极差），所以放进 HAVING（v2.8.0）
	having := `COUNT(*)>1`
	var havingArgs []any
	if q.SizeTolKB > 0 {
		having += ` AND MAX(size)-MIN(size)<=?`
		havingArgs = append(havingArgs, int64(q.SizeTolKB)*1024)
	}
	countArgs := append(append([]any{}, args...), havingArgs...)
	if !q.SkipTotal {
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT hash FROM files INDEXED BY idx_files_hash_active WHERE `+where+` GROUP BY hash HAVING `+having+`)`, countArgs...).Scan(&total)
		if err != nil {
			return nil, 0, err
		}
	}
	args2 := append(append(append([]any{}, args...), havingArgs...), limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT hash, COUNT(*), SUM(`+occupExpr+`) FROM files INDEXED BY idx_files_hash_active WHERE `+where+`
		GROUP BY hash HAVING `+having+` ORDER BY `+groupSQLSort(q.Sort, q.Order)+`, hash LIMIT ? OFFSET ?`, args2...) // #nosec G202 -- 排序子串来自 groupSQLSort 白名单映射
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	var groups []Group
	var hashes []string
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.Hash, &g.Count, &g.TotalSize); err != nil {
			return nil, 0, err
		}
		g.Key = "h:" + g.Hash
		g.Waste = g.TotalSize - g.TotalSize/int64(g.Count) // 估算：保留一份
		groups = append(groups, g)
		hashes = append(hashes, g.Hash)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(hashes) == 0 {
		return []Group{}, total, nil
	}
	ph := strings.Repeat("?,", len(hashes))
	ph = ph[:len(ph)-1]
	args = make([]any, len(hashes))
	for i, h := range hashes {
		args[i] = h
	}
	// 组内文件也要按扫描目录裁剪，否则结果里会混入未选中目录的同哈希文件；
	// 同时套用与组头相同的筛选条件（v3.10.10，P2-1），保证计数与展开列表一致。
	loadWhere, loadArgs := q.rootCondition(`hash IN (`+ph+`)`, args)
	loadWhere, loadArgs = q.filterCondition(loadWhere, loadArgs)
	// 保持与分组相同的顺序
	fs, err := s.filesWhere(ctx, loadWhere, loadArgs)
	if err != nil {
		return nil, 0, err
	}
	byHash := map[string][]FileRow{}
	for _, f := range fs {
		byHash[f.Hash] = append(byHash[f.Hash], f)
	}
	for i := range groups {
		groups[i].Files = byHash[groups[i].Hash]
		if len(groups[i].Files) > 0 {
			groups[i].Name = groups[i].Files[0].Name
		}
		// v3.10.10（P2-3）：统一走 RecomputeStats——按实际占用、按 (dev,inode)
		// 去重硬链接。此前只用 volume:inode，同一文件挂在不同扫描根下会被算两份。
		groups[i].RecomputeStats()
	}
	return groups, total, nil
}

func (s *Store) NameGroups(ctx context.Context, q GroupQuery) ([]Group, int64, error) {
	limit, offset, _ := q.limit()
	where := `status='active' AND size>=?`
	args := []any{q.minGroupSize()}
	where, args = q.filterCondition(where, args)
	where, args = q.rootCondition(where, args)
	// 翻页跳过 COUNT（v3.10.10，第七轮 P1-2），total=-1 表示本次未计算
	var total int64 = -1
	having := `COUNT(*)>1`
	var havingArgs []any
	if q.SizeTolKB > 0 {
		having += ` AND MAX(size)-MIN(size)<=?`
		havingArgs = append(havingArgs, int64(q.SizeTolKB)*1024)
	}
	countArgs := append(append([]any{}, args...), havingArgs...)
	if !q.SkipTotal {
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT lower(name) FROM files INDEXED BY idx_files_lower_name_active WHERE `+where+` GROUP BY lower(name) HAVING `+having+`)`, countArgs...).Scan(&total)
		if err != nil {
			return nil, 0, err
		}
	}
	args2 := append(append(append([]any{}, args...), havingArgs...), limit, offset)
	// v3.10.8：total 与 MAX 都取实际占用（alloc），旧库未采集时回退 size。
	rows, err := s.db.QueryContext(ctx, `SELECT lower(name), COUNT(*), SUM(`+occupExpr+`), MAX(`+occupExpr+`), MAX(name) FROM files INDEXED BY idx_files_lower_name_active WHERE `+where+`
		GROUP BY lower(name) HAVING `+having+` ORDER BY `+groupSQLSort(q.Sort, q.Order)+`, lower(name) LIMIT ? OFFSET ?`, args2...) // #nosec G202 -- 同上，白名单映射
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	var groups []Group
	var keys []string
	for rows.Next() {
		var g Group
		var rawKey string
		var maxSize int64
		var nm string
		if err := rows.Scan(&rawKey, &g.Count, &g.TotalSize, &maxSize, &nm); err != nil {
			return nil, 0, err
		}
		g.Key = "n:" + rawKey
		g.Name = nm
		g.Waste = g.TotalSize - maxSize
		groups = append(groups, g)
		keys = append(keys, rawKey)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(keys) == 0 {
		return []Group{}, total, nil
	}
	ph := strings.Repeat("?,", len(keys))
	ph = ph[:len(ph)-1]
	args = make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	// 与精确查重同理：返回的文件行不能超出本次选择的目录范围，
	// 且要与组头计数共用同一套筛选条件（v3.10.10，P2-1）。
	loadWhere, loadArgs := q.rootCondition(`lower(name) IN (`+ph+`)`, args)
	loadWhere, loadArgs = q.filterCondition(loadWhere, loadArgs)
	// v3.10.10（P1-2）：分组键必须由 SQL 的 lower(name) 产出，不能用 Go 的
	// strings.ToLower 重建。SQLite lower() 只折叠 ASCII，Go 会做 Unicode 折叠
	// 并把非法 UTF-8 换成替换字符；文件名含 Ä/全角Ａ/非法字节时两边键不一致，
	// 组头有计数、展开列表却是空的。
	fs, fsKeys, err := s.filesWhereWithKey(ctx, loadWhere, loadArgs)
	if err != nil {
		return nil, 0, err
	}
	byKey := map[string][]FileRow{}
	for i := range fs {
		byKey[fsKeys[i]] = append(byKey[fsKeys[i]], fs[i])
	}
	for i := range groups {
		groups[i].Files = byKey[keys[i]]
		groups[i].RecomputeStats()
	}
	return groups, total, nil
}

// hardLinkKey 返回硬链接去重键（v3.10.10，P2-3）。
// 同一 inode 的多个名字只占一份空间；同一文件可能同时挂在两个扫描根下，
// 所以优先用 (dev,inode)。旧库 dev 还是 0 时退回 volume:inode，行为同旧版；
// inode 拿不到（Windows/特殊 FS）时按路径各自独立，不做去重。
func hardLinkKey(f FileRow) string {
	if f.Inode <= 0 {
		return "p:" + f.Path
	}
	if f.Dev > 0 {
		return "d:" + strconv.FormatInt(f.Dev, 10) + ":" + strconv.FormatInt(f.Inode, 10)
	}
	return "v:" + f.Volume + ":" + strconv.FormatInt(f.Inode, 10)
}

// RecomputeStats 按组内文件重算 Count/TotalSize/Waste（v3.10.10，P2-3 / P2-10）。
// 统一三件事：按实际占用（稀疏文件不能按逻辑大小）、按 (dev,inode) 去重硬链接、
// Waste = 去重后总量 - 最大一份。精确组、同名组、网页二次筛选后的组都走这里，
// 避免「组头按一套算法、展开列表按另一套」。
func (g *Group) RecomputeStats() {
	byLink := make(map[string]int64, len(g.Files))
	var total int64
	for _, f := range g.Files {
		occup := f.AllocSize()
		k := hardLinkKey(f)
		if prev, ok := byLink[k]; ok {
			if occup > prev {
				total += occup - prev
				byLink[k] = occup
			}
			continue
		}
		byLink[k] = occup
		total += occup
	}
	var max int64
	for _, v := range byLink {
		if v > max {
			max = v
		}
	}
	g.Count = len(g.Files)
	g.TotalSize = total
	if g.Waste = total - max; g.Waste < 0 {
		g.Waste = 0
	}
}

func (s *Store) filesWhere(ctx context.Context, where string, args []any) ([]FileRow, error) {
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files WHERE status='active' AND `+where+` ORDER BY path`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []FileRow{}
	for rows.Next() {
		f, err := s.scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// filesWhereWithKey 与 filesWhere 相同，但额外返回每行由 SQLite 计算的
// lower(name) 分组键（v3.10.10，P1-2）。键必须与 GROUP BY lower(name) 同源，
// 否则非 ASCII / 非法 UTF-8 文件名会查不到组。
func (s *Store) filesWhereWithKey(ctx context.Context, where string, args []any) ([]FileRow, []string, error) {
	// where 由本包调用方按固定片段拼装，只含列名与 "?" 占位符，值全部走 args 绑定。
	rows, err := s.db.QueryContext(ctx, `SELECT lower(name), `+fileColsNoSelect+` FROM files WHERE status='active' AND `+where+` ORDER BY path`, args...) // #nosec G202 -- where 为内部固定片段，值走参数绑定
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []FileRow{}
	keys := []string{}
	for rows.Next() {
		var key string
		f, err := s.scanFileExtra(rows, &key)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, *f)
		keys = append(keys, key)
	}
	return out, keys, rows.Err()
}

// SearchFiles 按路径子串查已索引文件，供搜索索引不可用时的降级查询使用。
// v3.9.10 备注：当前零调用（降级走的是 internal/search 的回退 SQL），
// 保留作为 store 层的最小可用查询，改动降级逻辑时优先复用而不是另写 SQL。
func (s *Store) SearchFiles(ctx context.Context, keyword string, limit int) ([]FileRow, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files WHERE status='active' AND path LIKE ? ESCAPE '\' LIMIT ?`, "%"+EscapeLike(keyword)+"%", limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []FileRow{}
	for rows.Next() {
		f, err := s.scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}
