// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// v3.8.0 搜索快照：D1 要求「搜索阶段一次全量装进内存，搜索完成后展示秒开」。
// 本文件只做调度：流式读库 → 纯 Go 规则分组（internal/rules）→ 合并现有媒体算法，
// 结果冻结成一份不可变快照；结果接口只从内存切片，不再查库。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"verify/internal/applog"
	"verify/internal/config"
	"verify/internal/export"
	"verify/internal/health"
	"verify/internal/rules"
	"verify/internal/store"
)

type searchSnapshot struct {
	ID          string       `json:"id"`
	State       string       `json:"state"` // building | ready | canceled | error
	Error       string       `json:"error,omitempty"`
	RulesKey    string       `json:"rules_key"`
	DataVersion int64        `json:"data_version"`
	CreatedAt   int64        `json:"created_at"`
	Loaded      int64        `json:"loaded"`
	Total       int64        `json:"total"`
	Totals      rules.Totals `json:"totals"`

	mu     sync.RWMutex
	groups []store.Group
	cancel context.CancelFunc
	// roots 是本次搜索的卷根，removePaths 重算 groupDirs 时仍需用它把路径归到顶层目录。
	roots []string
	// groupDirs 与 groups 一一对应，记录每组涉及的全部顶层目录（已去重）。
	// v3.11.1：facets 每次请求基于它现算，不再缓存「每组只归属一个目录」的旧分布——
	// 那种算法会把跨目录的组在多个目录重复计数，导致 facets.total 与 results.total 对不上。
	groupDirs [][]string
	// kwMu / kwCache 缓存「关键字 → 每组的匹配位图」（v3.13.0，第十一轮 P2-5）。
	// results、facets、export 三个入口都要按同一关键字扫一遍全量，且每个路径都要
	// 做一次 ToLower 分配；缓存后同一次输入只算一遍，三处共用。
	// 快照实例被替换时缓存随对象丢弃；removePaths 改写组集合会递增 gen 使旧位图全部失效。
	kwMu    sync.Mutex
	kwCache map[string]kwMaskEntry
	// groupsGen 是组集合代数：每次 removePaths 真正改动组集合时递增。
	// 用原子量而不是 snap.mu，是因为 keywordMask 在 handler 里脱离了 snap.mu 使用，
	// 只靠「长度相同」判断会让改写前后同长度的组集合误用同一份位图。
	groupsGen atomic.Uint64
}

// kwMaskEntry 是关键字位图缓存项，带上计算时的组集合代数。
type kwMaskEntry struct {
	gen  uint64
	mask []bool
}

type snapshotManager struct {
	srv *Server
	mu  sync.Mutex
	cur *searchSnapshot
	seq int64
}

func newSnapshotManager(s *Server) *snapshotManager {
	return &snapshotManager{srv: s}
}

func (m *snapshotManager) current() *searchSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur
}

// groupMembers 返回当前快照里指定分组键的全部成员路径。
// 结果页的分组键来自快照，规则类键（content:/name:/size:/attributes:/all:）带属性片段，
// 引擎无法按前缀重算，只能由快照给出；快照未就绪或键不存在时 ok=false，交回引擎按旧键兜底。
func (m *snapshotManager) groupMembers(key string) ([]string, bool) {
	if m == nil || key == "" {
		return nil, false
	}
	snap := m.current()
	if snap == nil {
		return nil, false
	}
	snap.mu.RLock()
	defer snap.mu.RUnlock()
	if snap.State != "ready" {
		return nil, false
	}
	for _, g := range snap.groups {
		if g.Key != key {
			continue
		}
		paths := make([]string, 0, len(g.Files))
		for _, f := range g.Files {
			paths = append(paths, f.Path)
		}
		return paths, true
	}
	return nil, false
}

// invalidate 在规则/目录/最小大小变化后丢弃快照；下次进入结果页会重新搜索。
func (m *snapshotManager) invalidate() {
	m.cancelCurrent()
	m.mu.Lock()
	m.cur = nil
	m.mu.Unlock()
}

// removePaths 从当前快照里剔除已离开原位置的文件（v3.10.10，第五轮 P1-4）。
// 隔离成功后结果页不应继续显示已移走的条目，也不应把它们算进组头「可释放空间」；
// 与整份 invalidate 相比，其余组仍可继续操作，用户不必为一次隔离重跑全量搜索。
// 组内不足两份说明已不再是重复组，整组一并移除。
func (m *snapshotManager) removePaths(paths []string) {
	if m == nil || len(paths) == 0 {
		return
	}
	snap := m.current()
	if snap == nil {
		return
	}
	gone := make(map[string]bool, len(paths))
	for _, p := range paths {
		gone[filepath.Clean(p)] = true
	}
	snap.mu.Lock()
	defer snap.mu.Unlock()
	if snap.State != "ready" {
		return
	}
	out := make([]store.Group, 0, len(snap.groups))
	changed := false
	for _, g := range snap.groups {
		files := make([]store.FileRow, 0, len(g.Files))
		var total, maxSize int64
		for _, f := range g.Files {
			if gone[filepath.Clean(f.Path)] {
				changed = true
				continue
			}
			files = append(files, f)
			total += f.Size
			if f.Size > maxSize {
				maxSize = f.Size
			}
		}
		if len(files) == len(g.Files) {
			out = append(out, g)
			continue
		}
		if len(files) < 2 {
			continue
		}
		g.Files = files
		g.Count = len(files)
		g.TotalSize = total
		g.Waste = total - maxSize
		out = append(out, g)
	}
	if !changed {
		return
	}
	snap.groups = out
	snap.Totals = snapTotals(out)
	// v3.11.1：隔离后组集合变了，目录归属必须同步重算，否则收窄栏还按旧组集合计数。
	snap.groupDirs = computeGroupDirs(out, snap.roots)
	// v3.13.0：关键字位图与 groups 下标强相关，递增代数让旧位图全部失效。
	snap.groupsGen.Add(1)
	snap.kwMu.Lock()
	snap.kwCache = nil
	snap.kwMu.Unlock()
}

// keywordMask 返回 groups 中每个组是否命中 keyword 的位图（下标与 groups 对齐）。
// keyword 必须已是小写；返回 nil 表示没有关键字，调用方跳过该条件即可。
//
// 缓存上限 8 个关键字：用户在结果页反复切换搜索词的场景有限，简单清空比维护 LRU 省代码。
func (snap *searchSnapshot) keywordMask(groups []store.Group, keyword string) []bool {
	if keyword == "" {
		return nil
	}
	gen := snap.groupsGen.Load()
	snap.kwMu.Lock()
	if e, ok := snap.kwCache[keyword]; ok && e.gen == gen && len(e.mask) == len(groups) {
		mask := e.mask
		snap.kwMu.Unlock()
		return mask
	}
	snap.kwMu.Unlock()

	mask := make([]bool, len(groups))
	for i, g := range groups {
		mask[i] = groupMatchesKeyword(g, keyword)
	}

	snap.kwMu.Lock()
	// 计算期间组集合被 removePaths 改写过就不写缓存：本次结果仍正确，下次请求重算。
	if snap.groupsGen.Load() == gen {
		if len(snap.kwCache) >= 8 {
			snap.kwCache = nil
		}
		if snap.kwCache == nil {
			snap.kwCache = make(map[string]kwMaskEntry, 8)
		}
		snap.kwCache[keyword] = kwMaskEntry{gen: gen, mask: mask}
	}
	snap.kwMu.Unlock()
	return mask
}

// groupMatchesKeyword 判断组名或任一成员路径是否包含 keyword（已小写）。
// 小写化按组做一次：同一组的路径前缀高度重合，但 Go 没有便宜的前缀共享手段，
// 位图缓存已经把「同一关键字重复扫」的开销摊平了。
func groupMatchesKeyword(g store.Group, keyword string) bool {
	if strings.Contains(strings.ToLower(g.Name), keyword) {
		return true
	}
	for _, f := range g.Files {
		if strings.Contains(strings.ToLower(f.Path), keyword) {
			return true
		}
	}
	return false
}

func rulesChanged(old, cur *config.Config) bool {
	a, _ := json.Marshal(old.SearchRules)
	b, _ := json.Marshal(cur.SearchRules)
	if string(a) != string(b) || old.MinSize != cur.MinSize {
		return true
	}
	if len(old.Volumes) != len(cur.Volumes) {
		return true
	}
	for i := range old.Volumes {
		if old.Volumes[i] != cur.Volumes[i] {
			return true
		}
	}
	return false
}

// cancelCurrent 取消正在构建的快照；已完成的快照保留。
func (m *snapshotManager) cancelCurrent() {
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	if cur != nil && cur.cancel != nil {
		cur.cancel()
	}
}

// start 启动一次新的搜索阶段。已有快照会保留到新快照完成，避免中途把可用结果清空。
func (m *snapshotManager) start() (*searchSnapshot, error) {
	if m.srv.e.State().Running {
		return nil, fmt.Errorf("查重扫描进行中，请等待扫描完成后再搜索")
	}
	// 新搜索取代旧搜索：先取消还在构建的那一份，避免两份快照同时占内存。
	m.cancelCurrent()
	cfg := m.srv.cfg.Get()
	rulesPtr := m.srv.activeSearchRules()
	roots := append([]string(nil), cfg.Volumes...)
	minSize := cfg.MinSize

	counts, err := m.srv.st.Counts(context.Background(), minSize)
	if err != nil {
		return nil, fmt.Errorf("读取文件数量失败：%w", err)
	}
	// D1 不允许静默截断：先按保守估值判断内存，超限就在搜索阶段直接失败。
	limitMB := searchMemLimitMB(cfg)
	if estimate := (counts.Files*800 + 64<<20) / (1 << 20); estimate > int64(limitMB) {
		return nil, fmt.Errorf("预计需要约 %d MB 内存，超过上限 %d MB；请缩小扫描目录或提高最小文件大小", estimate, limitMB)
	}

	key := rulesKey(rulesPtr, roots, minSize)
	ctx, cancel := context.WithCancel(context.Background())
	m.seq++
	snap := &searchSnapshot{
		ID:        fmt.Sprintf("s%d-%d", time.Now().UnixMilli(), m.seq),
		State:     "building",
		RulesKey:  key,
		CreatedAt: time.Now().Unix(),
		Total:     counts.Files,
		cancel:    cancel,
		roots:     roots,
	}
	m.mu.Lock()
	m.cur = snap
	m.mu.Unlock()

	go m.build(ctx, snap, rulesPtr, roots, minSize)
	return snap, nil
}

func (m *snapshotManager) build(ctx context.Context, snap *searchSnapshot, rulesPtr *config.SearchRules, roots []string, minSize int64) {
	files := make([]store.FileRow, 0, snap.Total)
	var loaded int64
	err := m.srv.st.WalkActiveFiles(ctx, minSize, func(f store.FileRow) error {
		files = append(files, f)
		loaded++
		if loaded%5000 == 0 {
			snap.mu.Lock()
			snap.Loaded = loaded
			snap.mu.Unlock()
		}
		return nil
	})
	if err != nil {
		m.fail(snap, fmt.Sprintf("读取文件索引失败：%v", err))
		return
	}
	// 构建前后数据版本不一致说明库在变，重试一次；仍不一致就报错。
	version := m.srv.st.DataVersion()
	if err := m.fillSnapshot(ctx, snap, files, rulesPtr, roots); err != nil {
		if ctx.Err() != nil {
			m.fail(snap, "搜索已取消")
			return
		}
		m.fail(snap, err.Error())
		return
	}
	if now := m.srv.st.DataVersion(); now != version {
		// 重试一次：重新读库再建。
		files = files[:0]
		loaded = 0
		version = now
		if err := m.srv.st.WalkActiveFiles(ctx, minSize, func(f store.FileRow) error {
			files = append(files, f)
			loaded++
			return nil
		}); err != nil {
			m.fail(snap, fmt.Sprintf("重建文件索引失败：%v", err))
			return
		}
		if err := m.fillSnapshot(ctx, snap, files, rulesPtr, roots); err != nil {
			m.fail(snap, err.Error())
			return
		}
		if m.srv.st.DataVersion() != version {
			m.fail(snap, "数据正在变化，请稍后重试")
			return
		}
	}
	rules.SortByWaste(snap.groups)
	snap.mu.Lock()
	snap.Loaded = int64(len(files))
	snap.Totals = snapTotals(snap.groups)
	snap.groupDirs = computeGroupDirs(snap.groups, snap.roots)
	snap.State = "ready"
	snap.DataVersion = version
	snap.mu.Unlock()
	applog.Info("搜索完成：%d 个文件，%d 组，可释放 %d 字节（快照 %s）", len(files), snap.Totals.Groups, snap.Totals.Waste, snap.ID)
}

func (m *snapshotManager) fillSnapshot(ctx context.Context, snap *searchSnapshot, files []store.FileRow, rulesPtr *config.SearchRules, roots []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	groups := make([]store.Group, 0, len(files)/2)
	if rulesPtr != nil {
		pure, _ := rules.Build(ctx, files, rulesPtr, roots)
		groups = append(groups, pure...)
		groups = append(groups, m.mediaGroups(ctx, files, rulesPtr, roots)...)
	} else {
		// 旧配置：沿用原来的全部结果类型。
		legacy := &config.SearchRules{Base: config.RuleBaseAll, NameSimilarPct: 90, FolderNameMode: "full", FolderNameDepth: 1}
		pure, _ := rules.Build(ctx, files, legacy, roots)
		groups = append(groups, pure...)
		groups = append(groups, m.mediaGroups(ctx, files, nil, roots)...)
	}
	// 去重键：媒体组与规则组可能撞 key 的概率很低，但统一加类型前缀更稳。
	seen := map[string]bool{}
	out := make([]store.Group, 0, len(groups))
	for _, g := range groups {
		k := kindOf(g) + "|" + g.Key
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, g)
	}
	snap.mu.Lock()
	snap.groups = out
	snap.mu.Unlock()
	return nil
}

// mediaGroups 收集媒体类组。base 为纯元数据类型时不收集；base=all 时收全部现有类型。
func (m *snapshotManager) mediaGroups(ctx context.Context, files []store.FileRow, rulesPtr *config.SearchRules, roots []string) []store.Group {
	base := config.RuleBaseAll
	if rulesPtr != nil {
		base = rulesPtr.Base
	}
	want := map[string]bool{}
	switch base {
	case config.RuleBaseAll:
		for _, k := range []string{"video", "intro", "audio", "photo", "folder", "fuzzy", "semantic"} {
			want[k] = true
		}
	case config.RuleBaseVideo, config.RuleBaseIntro, config.RuleBaseAudio,
		config.RuleBasePhoto, config.RuleBaseFolder, config.RuleBaseFuzzy,
		config.RuleBaseSemantic:
		want[base] = true
	default:
		return nil
	}
	cfg := m.srv.cfg.Get()
	out := make([]store.Group, 0)
	add := func(kind string, groups []store.Group, err error) {
		if err != nil || len(groups) == 0 {
			return
		}
		groups = filterGroupsByRoots(groups, roots)
		if rulesPtr != nil {
			groups = rules.ApplyCriteria(groups, rulesPtr, roots)
		}
		for i := range groups {
			groups[i].Kind = kind
			groups[i].Key = kind + ":" + groups[i].Key
		}
		out = append(out, groups...)
	}
	if want["video"] {
		g, err := m.srv.e.VideoGroups(ctx, false)
		add("video", g, err)
	}
	if want["intro"] {
		g, err := m.srv.e.IntroGroups(ctx, cfg.IntroSeconds, false)
		add("intro", g, err)
	}
	if want["audio"] {
		g, _, err := m.srv.st.AudioGroups(ctx, cfg.EffectiveAudioThreshold())
		add("audio", g, err)
	}
	if want["photo"] {
		g, _, err := m.srv.st.PhotoGroups(ctx, cfg.ImgHamming)
		add("photo", g, err)
	}
	if want["folder"] {
		g, _, err := m.srv.st.FolderGroups(ctx)
		add("folder", g, err)
	}
	if want["fuzzy"] {
		if rulesPtr != nil && base == config.RuleBaseFuzzy {
			add("fuzzy", rules.FuzzyGroups(files, rulesPtr, roots), nil)
		} else {
			g, err := m.srv.e.FuzzyGroups(ctx, true)
			add("fuzzy", g, err)
		}
	}
	if want["semantic"] {
		g, err := m.srv.e.SemanticGroups(ctx)
		add("semantic", g, err)
	}
	return out
}

func (m *snapshotManager) fail(snap *searchSnapshot, msg string) {
	snap.mu.Lock()
	snap.State = "error"
	snap.Error = msg
	snap.mu.Unlock()
	applog.Warn("搜索失败：%s", msg)
}

func kindOf(g store.Group) string {
	if g.Kind != "" {
		return g.Kind
	}
	if strings.HasPrefix(g.Key, "h:") {
		return "exact"
	}
	if strings.HasPrefix(g.Key, "n:") {
		return "name"
	}
	return "exact"
}

func snapTotals(groups []store.Group) rules.Totals {
	t := rules.Totals{Groups: len(groups), ByKind: map[string]int64{}}
	for _, g := range groups {
		t.Files += len(g.Files)
		t.Waste += g.Waste
		t.ByKind[kindOf(g)]++
	}
	return t
}

// typeFacet / dirFacet 是 /api/rule-search/facets 的响应元素（v3.11.0）。
type typeFacet struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

type dirFacet struct {
	Dir   string `json:"dir"`
	Count int    `json:"count"`
}

// groupUnderDir 判断组里是否有成员位于 dir 或其子目录。
// 必须按路径段边界比较：/vol5/media 不能匹配 /vol5/media2（第十轮 P1）。
func groupUnderDir(g store.Group, dir string) bool {
	prefix := dir + string(os.PathSeparator)
	for _, f := range g.Files {
		clean := filepath.Clean(f.Path)
		if clean == dir || strings.HasPrefix(clean, prefix) {
			return true
		}
	}
	return false
}

// computeGroupDirs 为每个组算出它涉及的全部顶层目录（去重）。
// dir 取「卷根下第一级目录」：界面据此按实际文件夹分段收窄，dir 过滤天然覆盖其所有子目录。
// 一个组可以同时落在多个顶层目录（跨目录重复），因此每个目录各记一次，
// 而 facets.total 另按组去重——两者口径不同，不能拿目录计数之和当总数。
func computeGroupDirs(groups []store.Group, roots []string) [][]string {
	out := make([][]string, len(groups))
	for i, g := range groups {
		seen := make(map[string]bool, 4)
		dirs := make([]string, 0, 2)
		for _, file := range g.Files {
			d := topDirOf(file.Path, roots)
			if d == "" || seen[d] {
				continue
			}
			seen[d] = true
			dirs = append(dirs, d)
		}
		out[i] = dirs
	}
	return out
}

// topDirOf 返回路径所属卷根下的第一级目录；文件直接在卷根下时返回卷根本身。
// 不解析软链接：这里只用于界面分布统计，路径均来自索引。
func topDirOf(p string, roots []string) string {
	sep := string(os.PathSeparator)
	clean := filepath.Clean(p)
	for _, root := range roots {
		r := filepath.Clean(root)
		if clean != r && !strings.HasPrefix(clean, r+sep) {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(clean, r), sep)
		if rel == "" {
			return r
		}
		if i := strings.Index(rel, sep); i > 0 {
			return filepath.Join(r, rel[:i])
		}
		return r
	}
	return ""
}

func rulesKey(r *config.SearchRules, roots []string, minSize int64) string {
	b, _ := json.Marshal(map[string]any{"rules": r, "roots": roots, "min": minSize})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// activeSearchRules 返回当前生效规则：新配置优先，旧配置按 match_mode 派生。
func (s *Server) activeSearchRules() *config.SearchRules {
	cfg := s.cfg.Get()
	if cfg.SearchRules != nil {
		return cfg.SearchRules
	}
	return config.DeriveSearchRules(cfg.MatchMode)
}

func searchMemLimitMB(cfg *config.Config) int {
	if cfg.SearchMemLimitMB > 0 {
		return cfg.SearchMemLimitMB
	}
	total := readMemTotalMB()
	if total <= 0 {
		total = 8192
	}
	v := total / 4
	if v < 256 {
		v = 256
	}
	if v > 4096 {
		v = 4096
	}
	return v
}

func readMemTotalMB() int {
	b, err := os.ReadFile("/proc/meminfo") // #nosec G304 -- 固定路径，不来自请求
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, _ := strconv.Atoi(fields[1])
		return kb / 1024
	}
	return 0
}

// ---------- HTTP ----------

func (s *Server) handleRuleSearchRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	snap, err := s.snap.start()
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": snap.ID})
}

func (s *Server) handleRuleSearchCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	s.snap.cancelCurrent()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleRuleSearchStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	cur := s.snap.current()
	if cur == nil {
		writeJSON(w, 200, map[string]any{"state": "none", "totals": rules.Totals{ByKind: map[string]int64{}}})
		return
	}
	cur.mu.RLock()
	stale := cur.State == "ready" && cur.DataVersion != s.st.DataVersion()
	body := map[string]any{
		"id": cur.ID, "state": cur.State, "error": cur.Error, "stale": stale,
		"loaded": cur.Loaded, "total": cur.Total, "totals": cur.Totals, "created_at": cur.CreatedAt,
	}
	cur.mu.RUnlock()
	writeJSON(w, 200, body)
}

func (s *Server) handleRuleSearchResults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	cur := s.snap.current()
	if cur == nil {
		// 启动后还没跑过搜索（或规则变更后快照被 invalidate）时 current() 返回 nil，
		// 直接 RLock 会空指针崩溃、连接被断开。与 status/export 的未就绪语义保持一致。
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "搜索结果未就绪", "state": "none", "detail": "还没有搜索结果，请先搜索",
		})
		return
	}
	cur.mu.RLock()
	if cur.State != "ready" {
		state, msg := cur.State, cur.Error
		cur.mu.RUnlock()
		writeJSON(w, 409, map[string]any{"error": "搜索结果未就绪", "state": state, "detail": msg})
		return
	}
	groups := cur.groups
	cur.mu.RUnlock()

	typeFilter := strings.TrimSpace(r.URL.Query().Get("type"))
	keyword := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	dirFilter := strings.TrimSpace(r.URL.Query().Get("dir"))
	if dirFilter != "" {
		dirFilter = filepath.Clean(dirFilter)
	}
	// v3.13.0（第十一轮 P2-5）：关键字匹配走快照级位图缓存，与 facets 共用一次扫描，
	// 不再让两个接口各扫一遍全量、每路径各做一次 ToLower 分配。
	mask := cur.keywordMask(groups, keyword)
	filtered := make([]store.Group, 0, len(groups))
	for i, g := range groups {
		if !matchKindType(g, typeFilter) {
			continue
		}
		if mask != nil && !mask[i] {
			continue
		}
		// v3.11.0：目录收窄必须真正过滤列表，不能只改 facets 计数（第十轮 P1）。
		if dirFilter != "" && !groupUnderDir(g, dirFilter) {
			continue
		}
		filtered = append(filtered, g)
	}
	sortGroupsForResults(filtered, r.URL.Query().Get("sort"), r.URL.Query().Get("order"))
	page := int(intParam(r, "page", 1, 1, 1<<20))
	pageSize := int(intParam(r, "page_size", 100, 1, 200))
	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	writeJSON(w, 200, map[string]any{
		"groups": filtered[start:end], "total": len(filtered), "page": page, "page_size": pageSize,
		// v3.11.0：明确告诉前端还有没有下一页，不要让前端用 length < total 猜
		// （末页恰好取满时那个判断会误判为还有数据）。
		"has_more": page*pageSize < len(filtered),
	})
}

// handleRuleSearchFacets GET /api/rule-search/facets?type=&dir=&q= —
// 返回快照的分类与顶层目录分布，供界面分段收窄（v3.11.0 起）。
//
// 语义：types 始终是「按目录 + 关键字过滤后」的全量类型分布，不受 type 参数影响；
// type 只影响 dirs 与 total，dir（含子目录）同时过滤 types 与 dirs。
// v3.11.1 起 total 与 results.total 使用同一套谓词、按组去重，
// 同一 (type, dir, q) 下两者必须相等，界面不能出现两个分母。
// 计算只遍历内存快照，不查库。
func (s *Server) handleRuleSearchFacets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	cur := s.snap.current()
	if cur == nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "搜索结果未就绪", "state": "none", "detail": "还没有搜索结果，请先搜索",
		})
		return
	}
	cur.mu.RLock()
	if cur.State != "ready" {
		state, msg := cur.State, cur.Error
		cur.mu.RUnlock()
		writeJSON(w, http.StatusConflict, map[string]any{"error": "搜索结果未就绪", "state": state, "detail": msg})
		return
	}
	groups := cur.groups
	groupDirs := cur.groupDirs
	cur.mu.RUnlock()

	typeFilter := strings.TrimSpace(r.URL.Query().Get("type"))
	dirFilter := strings.TrimSpace(r.URL.Query().Get("dir"))
	if dirFilter != "" {
		dirFilter = filepath.Clean(dirFilter)
	}
	keyword := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	sep := string(os.PathSeparator)

	typeCounts := make(map[string]int, 12)
	dirCounts := make(map[string]int, 12)
	total := 0
	// v3.13.0（第十一轮 P2-5）：与 results 共用同一份关键字位图。
	mask := cur.keywordMask(groups, keyword)
	for i, g := range groups {
		// v3.11.1：dir 与 results 用同一个 groupUnderDir（路径段边界），total 按组去重。
		// 这样同一 (type, dir, q) 下 facets.total 与 results.total 必然相等，
		// 界面「已显示 N / 共 M 组」和「全部 M」不会出现两个分母。
		if dirFilter != "" && !groupUnderDir(g, dirFilter) {
			continue
		}
		kind := kindOf(g)
		matched := mask == nil || mask[i]
		// types 只受 dir 与 q 影响，不受 type 影响：切类别时其它 chip 的计数不会塌成 0。
		if matched {
			typeCounts[kind]++
		}
		if !matchKindType(g, typeFilter) || !matched {
			continue
		}
		total++
		if i >= len(groupDirs) {
			continue
		}
		for _, d := range groupDirs[i] {
			if dirFilter != "" && d != dirFilter && !strings.HasPrefix(d, dirFilter+sep) {
				continue
			}
			dirCounts[d]++
		}
	}
	// v3.13.0（第十一轮 P2-6）：dir 指向非顶层子目录时，groupDirs 里记的都是顶层目录，
	// 按 dirFilter 前缀过滤后 dirCounts 为空，界面就失去「当前收窄在哪」的唯一提示。
	// 这时把收窄目录本身回填成唯一项，计数即 total。
	if dirFilter != "" && len(dirCounts) == 0 && total > 0 {
		dirCounts[dirFilter] = total
	}

	types := make([]typeFacet, 0, len(typeCounts))
	for k, v := range typeCounts {
		types = append(types, typeFacet{Type: k, Count: v})
	}
	sort.Slice(types, func(i, j int) bool {
		if types[i].Count != types[j].Count {
			return types[i].Count > types[j].Count
		}
		return types[i].Type < types[j].Type
	})
	dirs := make([]dirFacet, 0, len(dirCounts))
	for k, v := range dirCounts {
		dirs = append(dirs, dirFacet{Dir: k, Count: v})
	}
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].Count != dirs[j].Count {
			return dirs[i].Count > dirs[j].Count
		}
		return dirs[i].Dir < dirs[j].Dir
	})
	writeJSON(w, 200, map[string]any{"total": total, "types": types, "dirs": dirs})
}

func (s *Server) handleRuleSearchExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	// 导出只走快照组，避免重新查库导致内容与界面不一致（v3.9.10 起直接产出 CSV）。
	cur := s.snap.current()
	if cur == nil {
		writeJSON(w, 409, map[string]any{"error": "还没有搜索结果，请先搜索"})
		return
	}
	cur.mu.RLock()
	groups := cur.groups
	state := cur.State
	cur.mu.RUnlock()
	if state != "ready" {
		writeJSON(w, 409, map[string]any{"error": "搜索结果未就绪"})
		return
	}
	// 复用结果页同一套筛选与排序，保证下载内容与当前界面所见一致。
	typeFilter := strings.TrimSpace(r.URL.Query().Get("type"))
	keyword := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	// v3.13.0：与 results/facets 共用关键字位图。
	mask := cur.keywordMask(groups, keyword)
	filtered := make([]store.Group, 0, len(groups))
	for i, g := range groups {
		if !matchKindType(g, typeFilter) {
			continue
		}
		if mask != nil && !mask[i] {
			continue
		}
		filtered = append(filtered, g)
	}
	sortGroupsForResults(filtered, r.URL.Query().Get("sort"), r.URL.Query().Get("order"))

	name := fmt.Sprintf("verify-rule-search-%s.csv", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	// v3.10.8（P2-7）：传 nil 走默认标记规则，与结果页导出口径一致。
	opt := s.exportOptions(nil, false)
	s.applyPathMapping(&opt, "dc5")
	if err := export.WriteDC5CSV(w, filtered, opt); err != nil {
		// 响应头已发出，无法再改写状态码，只能留日志便于排查。
		applog.Warn("规则搜索导出失败（%v）", err)
	}
}

// matchKindType 判断组是否属于 typeFilter 指定的类别；空串或 "all" 表示不筛。
func matchKindType(g store.Group, typeFilter string) bool {
	if typeFilter == "" || typeFilter == "all" {
		return true
	}
	kind := kindOf(g)
	if typeFilter != "samevideo" {
		return kind == typeFilter
	}
	// samevideo 是「精确重复且至少一个成员是视频」的展示子集。
	if kind != "exact" {
		return false
	}
	for _, f := range g.Files {
		if health.HasVideoExt(f.Name) {
			return true
		}
	}
	return false
}

func sortGroupsForResults(groups []store.Group, sortKey, order string) {
	desc := order != "asc"
	less := func(i, j int) bool { return groups[i].Waste > groups[j].Waste }
	switch sortKey {
	case "total_size":
		less = func(i, j int) bool { return groups[i].TotalSize > groups[j].TotalSize }
	case "count":
		less = func(i, j int) bool { return groups[i].Count > groups[j].Count }
	case "name":
		less = func(i, j int) bool { return groups[i].Name < groups[j].Name }
	case "type":
		less = func(i, j int) bool { return kindOf(groups[i]) < kindOf(groups[j]) }
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if desc {
			return less(i, j)
		}
		return less(j, i)
	})
}
