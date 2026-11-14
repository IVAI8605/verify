// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package search

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Entry 是索引里的一条文件记录，只保留搜索与展示需要的字段。
type Entry struct {
	ID     int64
	Name   string
	NameL  string
	Path   string
	PathL  string
	Ext    string
	Size   int64
	Mtime  int64
	Volume string
}

// Query 是一次搜索请求。
type Query struct {
	Text          string
	Mode          string // substring / wildcard / regex
	CaseSensitive bool
	Exts          []string
	MinSize       int64
	MaxSize       int64
	Since         int64 // 纳秒，与 files.mtime 同单位
	Until         int64
	Dir           string
	Sort          string // name / path / ext / size / mtime / volume
	Order         string // asc / desc
	Limit         int
	Offset        int
	// Cursor 是 keyset 分页游标（v3.20.0）。空串表示从头取；
	// 非空时忽略 Offset，从游标之后继续，保证滚动加载不重不漏。
	Cursor string
	// CountOnly 只要总数不要明细（v3.20.0），省掉组装返回行的开销。
	CountOnly bool
	// ExcludePaths 是选择集里被逐个取消勾选的路径（v3.20.0）。
	// 只用于「全选后排除少数几条」的解析，不走 URL 参数。
	ExcludePaths map[string]bool
}

// Item 是返回给前端的搜索结果行。
type Item struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Ext    string `json:"ext"`
	Size   int64  `json:"size"`
	Mtime  int64  `json:"mtime"`
	Volume string `json:"volume"`
}

// MarshalJSON 给编码异常路径补 name_encoding（v3.11.0）。
// 与 store.FileRow 保持一致：JSON 编码会把非法字节替换成 U+FFFD，
// 前端只能靠这个字段判断该行不可操作。
func (it Item) MarshalJSON() ([]byte, error) {
	type plain Item
	out := struct {
		plain
		NameEncoding string `json:"name_encoding,omitempty"`
	}{plain: plain(it)}
	if !utf8.ValidString(it.Path) || !utf8.ValidString(it.Name) {
		out.NameEncoding = "invalid"
	}
	return json.Marshal(out)
}

// Result 是一次查询的结果与耗时。
type Result struct {
	Total     int
	Items     []Item
	Degraded  bool
	Truncated bool
	// NextCursor 指向下一页（keyset）；空串表示已经到底（v3.20.0）。
	NextCursor string
	Took       time.Duration
}

// Index 是内存文件名索引。查询走读锁，重建在临时切片完成后一次性替换。
type Index struct {
	mu         sync.RWMutex
	entries    []Entry
	builtAt    time.Time
	building   bool
	degraded   bool
	maxEntries int
}

// New 创建索引；maxEntries<=0 时用默认 300 万条。
func New(maxEntries int) *Index {
	if maxEntries <= 0 {
		maxEntries = 3000000
	}
	return &Index{maxEntries: maxEntries}
}

// SetMaxEntries 调整上限，下一次 Build 生效。
func (ix *Index) SetMaxEntries(n int) {
	if n <= 0 {
		return
	}
	ix.mu.Lock()
	ix.maxEntries = n
	ix.mu.Unlock()
}

// Status 描述当前索引状态。
type Status struct {
	Entries    int   `json:"entries"`
	BuiltAt    int64 `json:"built_at"`
	Building   bool  `json:"building"`
	Degraded   bool  `json:"degraded"`
	EstBytes   int64 `json:"est_bytes"`
	MaxEntries int   `json:"max_entries"`
}

func (ix *Index) Status() Status {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return Status{
		Entries: len(ix.entries),
		// v3.25.0（P2-19）：用纳秒而不是秒。搜索凭据靠这个值判断索引是否重建过，
		// 同一秒内重建两次时秒级时间戳相同，旧凭据会被误当成仍有效。
		BuiltAt:    ix.builtAt.UnixNano(),
		Building:   ix.building,
		Degraded:   ix.degraded,
		EstBytes:   int64(len(ix.entries)) * 250,
		MaxEntries: ix.maxEntries,
	}
}

// Ready 表示索引已可直接查询（构建完成且未降级）。
func (ix *Index) Ready() bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.entries) > 0 && !ix.degraded && !ix.building
}

// Build 从 files 表全量重建索引。文件数超过上限时置降级标记、不载入内存。
func (ix *Index) Build(ctx context.Context, db *sql.DB) (int, error) {
	ix.mu.Lock()
	if ix.building {
		ix.mu.Unlock()
		return 0, nil
	}
	ix.building = true
	maxEntries := ix.maxEntries
	ix.mu.Unlock()

	defer func() {
		ix.mu.Lock()
		ix.building = false
		ix.mu.Unlock()
	}()

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE status='active'`).Scan(&count); err != nil {
		return 0, err
	}
	if count > maxEntries {
		ix.mu.Lock()
		ix.entries = nil
		ix.degraded = true
		ix.builtAt = time.Now()
		ix.mu.Unlock()
		return count, nil
	}

	rows, err := db.QueryContext(ctx, `SELECT id, name, path, ext, size, mtime, volume FROM files WHERE status='active'`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	entries := make([]Entry, 0, count)
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Name, &e.Path, &e.Ext, &e.Size, &e.Mtime, &e.Volume); err != nil {
			return 0, err
		}
		e.NameL = strings.ToLower(e.Name)
		e.PathL = strings.ToLower(e.Path)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// 预排序按名称，默认排序时可直接使用，避免每次查询再排一次
	sort.Slice(entries, func(i, j int) bool { return entries[i].NameL < entries[j].NameL })

	ix.mu.Lock()
	ix.entries = entries
	ix.degraded = false
	ix.builtAt = time.Now()
	ix.mu.Unlock()
	return len(entries), nil
}

// Load 用给定条目替换索引（供测试与将来的增量重建使用），并遵守内存上限。
func (ix *Index) Load(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].NameL < entries[j].NameL })
	ix.mu.Lock()
	if len(entries) > ix.maxEntries {
		ix.entries = nil
		ix.degraded = true
	} else {
		ix.entries = entries
		ix.degraded = false
	}
	ix.builtAt = time.Now()
	ix.mu.Unlock()
}

// RebuildIfStale 在文件数与索引条数差异明显时触发后台重建（用于扫描后自动跟上）。
// 只在降级状态下直接重建；正常情况下差异超过 1% 才重建，避免频繁抖动。
func (ix *Index) RebuildIfStale(ctx context.Context, db *sql.DB) {
	ix.mu.RLock()
	cur := len(ix.entries)
	degraded := ix.degraded
	building := ix.building
	ix.mu.RUnlock()
	if building {
		return
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE status='active'`).Scan(&count); err != nil {
		return
	}
	if !degraded && cur > 0 && abs(count-cur)*100 <= cur {
		return
	}
	_, _ = ix.Build(ctx, db)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Query 在索引里执行搜索。索引未就绪时返回 Degraded=true 且无结果，由调用方回退 SQL。
func (ix *Index) Query(q Query) Result {
	start := time.Now()
	ix.mu.RLock()
	entries := ix.entries
	degraded := ix.degraded
	ix.mu.RUnlock()

	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}
	if q.Offset < 0 {
		q.Offset = 0
	}

	matcher, err := buildMatcher(q)
	if err != nil || len(entries) == 0 {
		return Result{Degraded: degraded, Took: time.Since(start)}
	}

	hits := make([]Entry, 0, 256)
	for _, e := range entries {
		if !matcher(e) {
			continue
		}
		if q.MinSize > 0 && e.Size < q.MinSize {
			continue
		}
		if q.MaxSize > 0 && e.Size > q.MaxSize {
			continue
		}
		if q.Since > 0 && e.Mtime < q.Since {
			continue
		}
		if q.Until > 0 && e.Mtime >= q.Until {
			continue
		}
		if q.Dir != "" && !strings.HasPrefix(e.Path, q.Dir) {
			continue
		}
		// 全选后逐条取消的路径在这里排除，总数与分页口径保持一致（v3.20.0）。
		if q.ExcludePaths != nil && q.ExcludePaths[e.Path] {
			continue
		}
		hits = append(hits, e)
	}

	total := len(hits)
	if q.CountOnly {
		return Result{Total: total, Degraded: degraded, Took: time.Since(start)}
	}

	sortHits(hits, q.Sort, q.Order)

	begin := q.Offset
	if q.Cursor != "" {
		cur, ok := decodeCursor(q.Cursor)
		if !ok || cur.Sort != q.Sort || cur.Order != q.Order {
			// 游标与当前排序不匹配：宁可返回空页让前端重置，也不重复或跳过数据。
			return Result{Total: total, Degraded: degraded, Took: time.Since(start)}
		}
		begin = cursorStart(hits, q.Sort, q.Order, cur)
	}
	if begin < 0 {
		begin = 0
	}
	if begin > total {
		begin = total
	}
	items := make([]Item, 0, limit)
	for i := begin; i < total && len(items) < limit; i++ {
		e := hits[i]
		items = append(items, Item{ID: e.ID, Name: e.Name, Path: e.Path, Ext: e.Ext, Size: e.Size, Mtime: e.Mtime, Volume: e.Volume})
	}
	res := Result{
		Total:     total,
		Items:     items,
		Degraded:  degraded,
		Truncated: total > begin+len(items),
		Took:      time.Since(start),
	}
	if res.Truncated && len(items) > 0 {
		res.NextCursor = encodeCursor(hits[begin+len(items)-1], q)
	}
	return res
}

// buildMatcher 把查询文本编译成逐条判断函数；正则/通配符只编译一次。
func buildMatcher(q Query) (func(Entry) bool, error) {
	text := q.Text
	if strings.TrimSpace(text) == "" {
		return func(Entry) bool { return false }, nil
	}
	exts := make(map[string]bool, len(q.Exts))
	for _, e := range q.Exts {
		e = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(e, ".")))
		if e != "" {
			exts[e] = true
		}
	}
	extOK := func(e Entry) bool {
		if len(exts) == 0 {
			return true
		}
		return exts[strings.ToLower(e.Ext)]
	}

	switch q.Mode {
	case "regex":
		re, err := compileSafeRegex(text, q.CaseSensitive)
		if err != nil {
			return nil, err
		}
		return func(e Entry) bool {
			if !extOK(e) {
				return false
			}
			return re.MatchString(e.Name) || re.MatchString(e.Path)
		}, nil
	case "wildcard":
		re, err := compileSafeRegex(wildcardToRegexp(text), q.CaseSensitive)
		if err != nil {
			return nil, err
		}
		return func(e Entry) bool {
			if !extOK(e) {
				return false
			}
			return re.MatchString(e.Name) || re.MatchString(e.Path)
		}, nil
	default:
		if q.CaseSensitive {
			return func(e Entry) bool {
				if !extOK(e) {
					return false
				}
				return strings.Contains(e.Name, text) || strings.Contains(e.Path, text)
			}, nil
		}
		lt := strings.ToLower(text)
		return func(e Entry) bool {
			if !extOK(e) {
				return false
			}
			return strings.Contains(e.NameL, lt) || strings.Contains(e.PathL, lt)
		}, nil
	}
}

// compileSafeRegex 编译用户正则：限制长度并拒绝明显的嵌套量词，降低 ReDoS 风险。
func compileSafeRegex(pat string, caseSensitive bool) (*regexp.Regexp, error) {
	if len(pat) > 200 {
		return nil, errRegexTooLong
	}
	if nestedQuantifier.MatchString(pat) {
		return nil, errRegexUnsafe
	}
	if !caseSensitive {
		pat = "(?i)" + pat
	}
	return regexp.Compile(pat)
}

var (
	// nestedQuantifier 粗略识别 "(...)+*" / "(...*)+" 这类易爆模式，宁可拒绝也不卡死服务
	nestedQuantifier = regexp.MustCompile(`[(][^)]{0,40}[*+][^)]{0,40}[)][*+{]`)
	// wildcardToRegexp 把 * ? 转成正则，其余字符全部转义
	errRegexTooLong = errString("正则过长（最多 200 字符）")
	errRegexUnsafe  = errString("正则包含易导致卡死的嵌套量词")
)

type errString string

func (e errString) Error() string { return string(e) }

func wildcardToRegexp(pat string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pat {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return b.String()
}

// cursor 是 keyset 分页的位置标记（v3.20.0）。
// 字符串排序键放 Key，数值排序键放 Num；ID 用于同值时的稳定次序。
type cursor struct {
	Sort  string `json:"s"`
	Order string `json:"o"`
	Key   string `json:"k"`
	Num   int64  `json:"n"`
	ID    int64  `json:"id"`
}

func encodeCursor(e Entry, q Query) string {
	cur := cursor{Sort: q.Sort, Order: q.Order, ID: e.ID}
	switch q.Sort {
	case "size":
		cur.Num = e.Size
	case "mtime":
		cur.Num = e.Mtime
	default:
		cur.Key = entryStringKey(e, q.Sort)
	}
	b, _ := json.Marshal(cur)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursor, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, false
	}
	var cur cursor
	if err := json.Unmarshal(b, &cur); err != nil {
		return cursor{}, false
	}
	return cur, true
}

// entryStringKey 返回一条记录在字符串排序键下的比较值。
func entryStringKey(e Entry, sortKey string) string {
	switch sortKey {
	case "path":
		return e.PathL
	case "ext":
		return strings.ToLower(e.Ext)
	case "volume":
		return e.Volume
	default:
		return e.NameL
	}
}

func cmpInt64(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// compareHit 比较两条记录在给定排序键下的先后，返回 -1 / 0 / 1。
func compareHit(a, b Entry, sortKey string) int {
	switch sortKey {
	case "size":
		return cmpInt64(a.Size, b.Size)
	case "mtime":
		return cmpInt64(a.Mtime, b.Mtime)
	default:
		ka, kb := entryStringKey(a, sortKey), entryStringKey(b, sortKey)
		if ka < kb {
			return -1
		}
		if ka > kb {
			return 1
		}
		return 0
	}
}

// sortHits 按排序键排序；同值时统一按 ID 决出次序，保证 keyset 分页不重不漏。
func sortHits(hits []Entry, key, order string) {
	desc := strings.EqualFold(order, "desc")
	sort.Slice(hits, func(i, j int) bool {
		if c := compareHit(hits[i], hits[j], key); c != 0 {
			if desc {
				return c > 0
			}
			return c < 0
		}
		if desc {
			return hits[i].ID > hits[j].ID
		}
		return hits[i].ID < hits[j].ID
	})
}

// cursorStart 用二分查找定位游标之后的第一条记录。
func cursorStart(hits []Entry, key, order string, cur cursor) int {
	desc := strings.EqualFold(order, "desc")
	return sort.Search(len(hits), func(i int) bool {
		e := hits[i]
		if key == "size" || key == "mtime" {
			v := e.Size
			if key == "mtime" {
				v = e.Mtime
			}
			if v != cur.Num {
				if desc {
					return v < cur.Num
				}
				return v > cur.Num
			}
		} else if k := entryStringKey(e, key); k != cur.Key {
			if desc {
				return k < cur.Key
			}
			return k > cur.Key
		}
		if desc {
			return e.ID < cur.ID
		}
		return e.ID > cur.ID
	})
}
