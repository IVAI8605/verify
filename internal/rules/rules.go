// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package rules 实现 v3.8.0 的「搜索规则组合」内存分组。
//
// 设计前提（设计文档 D1）：搜索阶段已经把全部命中行读进内存，所以这里不再写动态 SQL
// GROUP BY，而是用复合键在 Go 里聚合。好处是所有条件（名称/大小/扩展名/卷/日期/
// 文件夹名）共用一套逻辑，可在 Windows 本机单测；查询层只负责流式读行。
package rules

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"verify/internal/config"
	"verify/internal/fname"
	"verify/internal/store"
)

// Totals 是快照的总量摘要。
type Totals struct {
	Groups int              `json:"groups"`
	Files  int              `json:"files"`
	Waste  int64            `json:"waste"`
	ByKind map[string]int64 `json:"by_kind"` // 各类型组数
}

// Build 按规则对内存中的文件行分组。
//
// 返回的组只包含纯元数据/内容类（content/name/size/attributes/all）；
// 媒体类（video/intro/audio/photo/folder/fuzzy/semantic）由调用方用现有算法出组后
// 再调 ApplyCriteria 叠加条件。
func Build(ctx context.Context, files []store.FileRow, rules *config.SearchRules, roots []string) ([]store.Group, Totals) {
	totals := Totals{ByKind: map[string]int64{}}
	if rules == nil || len(files) == 0 {
		return nil, totals
	}
	rules = config.NormalizeSearchRules(rules)
	if rules == nil {
		return nil, totals
	}
	base := rules.Base
	attrs := attrSet(rules.Attrs)

	// 媒体 base 不在这里处理。
	switch base {
	case config.RuleBaseVideo, config.RuleBaseIntro, config.RuleBaseAudio,
		config.RuleBasePhoto, config.RuleBaseFolder, config.RuleBaseFuzzy,
		config.RuleBaseSemantic:
		return nil, totals
	}

	// all 只合并“精确 + 同名”两种旧结果，避免把按大小也塞进去产生超大同尺寸组。
	kinds := []string{base}
	if base == config.RuleBaseAll {
		kinds = []string{config.RuleBaseContent, config.RuleBaseName}
	}

	groups := make([]store.Group, 0, len(files)/2)
	for _, kind := range kinds {
		groups = append(groups, buildKind(ctx, kind, files, rules, attrs, roots)...)
	}
	// “相似文件名”不是精确键，不能在 GROUP BY 里表达：先按其它条件成组，再按相似度切分。
	if attrs[config.RuleAttrNameSimilar] {
		groups = splitBySimilarName(groups, rules.NameSimilarPct)
	}
	// Kind 用前端结果页的命名：content→exact；attributes 只有 size 时按“按大小”展示。
	for i := range groups {
		switch groups[i].Kind {
		case config.RuleBaseContent:
			groups[i].Kind = "exact"
		case config.RuleBaseAttributes:
			if len(rules.Attrs) == 1 && attrs[config.RuleAttrSize] {
				groups[i].Kind = "size"
			} else {
				groups[i].Kind = "attributes"
			}
		}
	}
	for _, g := range groups {
		totals.Groups++
		totals.Files += len(g.Files)
		totals.Waste += g.Waste
		totals.ByKind[g.Kind]++
	}
	if err := ctx.Err(); err != nil {
		return nil, totals
	}
	return groups, totals
}

func buildKind(ctx context.Context, kind string, files []store.FileRow, rules *config.SearchRules, attrs map[string]bool, roots []string) []store.Group {
	if err := ctx.Err(); err != nil {
		return nil
	}
	// 先按规则过滤（副本名、目录范围），再进入分组。
	rows := make([]store.FileRow, 0, len(files))
	for _, f := range files {
		if !underRoots(f.Path, roots) {
			continue
		}
		if rules.IgnoreCopyNames && isCopyName(f.Name, rules.CopyPatterns) {
			continue
		}
		rows = append(rows, f)
	}
	if len(rows) == 0 {
		return nil
	}

	sizeTol := int64(rules.SizeToleranceKB) * 1024
	sizeInKey := attrs[config.RuleAttrSize]
	if kind == config.RuleBaseSize {
		sizeInKey = true
	}

	// 大小容差需要聚类：按“除大小外的键”分桶，桶内按大小贪心成组。
	if sizeInKey && sizeTol > 0 {
		return clusterBySize(kind, rows, rules, attrs, roots, sizeTol)
	}

	buckets := map[string][]store.FileRow{}
	for _, f := range rows {
		k, ok := groupKey(kind, f, rules, attrs, roots, true)
		if !ok {
			continue
		}
		buckets[k] = append(buckets[k], f)
	}
	out := make([]store.Group, 0, len(buckets))
	for k, fs := range buckets {
		if len(fs) < 2 {
			continue
		}
		g := makeGroup(kind, k, fs)
		if rules.IgnoreSameFolder && allSameFolder(g.Files) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// clusterBySize 处理“相同文件大小 + 容差”的条件。
// 除 size 外的键相同的候选先分桶，桶内按大小排序，再按“最大-最小 ≤ 容差”贪心成组，
// 避免链式合并把整个大小区间吞成一组。
func clusterBySize(kind string, rows []store.FileRow, rules *config.SearchRules, attrs map[string]bool, roots []string, tol int64) []store.Group {
	// 复制一份属性集合并去掉 size，用于前缀键。
	prefixAttrs := map[string]bool{}
	for k, v := range attrs {
		if k != config.RuleAttrSize {
			prefixAttrs[k] = v
		}
	}
	buckets := map[string][]store.FileRow{}
	for _, f := range rows {
		k, ok := groupKey(kind, f, rules, prefixAttrs, roots, false)
		if !ok {
			continue
		}
		buckets[k] = append(buckets[k], f)
	}
	out := make([]store.Group, 0)
	for prefix, fs := range buckets {
		sort.Slice(fs, func(i, j int) bool { return fs[i].Size < fs[j].Size })
		for i := 0; i < len(fs); {
			min := fs[i].Size
			j := i + 1
			for j < len(fs) && fs[j].Size-min <= tol {
				j++
			}
			if j-i >= 2 {
				g := makeGroup(kind, prefix+"\x1f"+strconv.FormatInt(min, 10), fs[i:j])
				if !(rules.IgnoreSameFolder && allSameFolder(g.Files)) {
					out = append(out, g)
				}
			}
			i = j
		}
	}
	return out
}

// groupKey 返回一行的复合分组键；ok=false 表示该条件为空的非法组合（例如 attributes 没有属性）。
func groupKey(kind string, f store.FileRow, rules *config.SearchRules, attrs map[string]bool, roots []string, withSize bool) (string, bool) {
	parts := make([]string, 0, len(attrs)+1)
	switch kind {
	case config.RuleBaseContent:
		if f.Hash == "" || strings.HasPrefix(f.Hash, "ERR") {
			return "", false
		}
		parts = append(parts, "h", f.Hash)
	case config.RuleBaseName:
		parts = append(parts, "n", normName(f.Name, rules.CaseSensitive))
	case config.RuleBaseSize:
		if withSize {
			parts = append(parts, "s", strconv.FormatInt(f.Size, 10))
		} else {
			parts = append(parts, "s")
		}
	case config.RuleBaseAttributes:
		parts = append(parts, "a")
	case config.RuleBaseAll:
		parts = append(parts, "x")
	default:
		return "", false
	}
	// 按固定顺序追加属性，保证同样的选择得到同样的键。
	for _, a := range config.RuleAttrOrder {
		if !attrs[a] || a == config.RuleAttrNameSimilar {
			continue
		}
		// base 已经贡献的键不再重复。
		if (kind == config.RuleBaseName && a == config.RuleAttrName) ||
			(kind == config.RuleBaseSize && a == config.RuleAttrSize) {
			continue
		}
		if a == config.RuleAttrSize && !withSize {
			continue
		}
		parts = append(parts, attrKey(a, f, rules, roots))
	}
	if kind == config.RuleBaseAttributes && withSize && len(parts) == 1 {
		return "", false
	}
	return kind + ":" + strings.Join(parts, "\x1f"), true
}

func attrKey(a string, f store.FileRow, rules *config.SearchRules, roots []string) string {
	switch a {
	case config.RuleAttrName:
		return "n=" + normName(f.Name, rules.CaseSensitive)
	case config.RuleAttrSize:
		return "s=" + strconv.FormatInt(f.Size, 10)
	case config.RuleAttrExt:
		return "e=" + strings.ToLower(f.Ext)
	case config.RuleAttrVolume:
		return "v=" + f.Volume
	case config.RuleAttrMtimeDate:
		return "d=" + time.Unix(0, f.Mtime).Format("2006-01-02")
	case config.RuleAttrFolderName:
		return "f=" + folderKey(f.Path, roots, rules)
	}
	return a + "="
}

// folderKey 按 full / depth / scan_root 三种口径取文件夹名。
func folderKey(p string, roots []string, rules *config.SearchRules) string {
	dir := path.Dir(p)
	base := path.Base(dir)
	mode := rules.FolderNameMode
	if mode == "depth" || mode == "scan_root" {
		rel, ok := relativeToRoot(dir, roots)
		if ok {
			switch mode {
			case "depth":
				segs := strings.Split(strings.Trim(rel, "/"), "/")
				if len(segs) >= rules.FolderNameDepth {
					return segs[rules.FolderNameDepth-1]
				}
				return base
			case "scan_root":
				return rel
			}
		}
	}
	return base
}

// relativeToRoot 返回 dir 相对某个扫描根的路径；取最长匹配的根。
func relativeToRoot(dir string, roots []string) (string, bool) {
	best := ""
	for _, r := range roots {
		r = strings.TrimRight(r, "/")
		if r == "" {
			continue
		}
		if dir == r {
			if len(r) > len(best) {
				best = r
			}
			continue
		}
		if strings.HasPrefix(dir, r+"/") && len(r) > len(best) {
			best = r
		}
	}
	if best == "" {
		return "", false
	}
	if dir == best {
		return "/", true
	}
	return dir[len(best):], true
}

func normName(n string, caseSensitive bool) string {
	if caseSensitive {
		return n
	}
	return strings.ToLower(n)
}

func attrSet(attrs []string) map[string]bool {
	m := map[string]bool{}
	for _, a := range attrs {
		m[a] = true
	}
	return m
}

func underRoots(p string, roots []string) bool {
	if len(roots) == 0 {
		return true
	}
	for _, r := range roots {
		r = strings.TrimRight(r, "/")
		if r == "" || p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

func isCopyName(name string, patterns []string) bool {
	lower := strings.ToLower(name)
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

func allSameFolder(files []store.FileRow) bool {
	if len(files) == 0 {
		return false
	}
	dir := path.Dir(files[0].Path)
	for _, f := range files[1:] {
		if path.Dir(f.Path) != dir {
			return false
		}
	}
	return true
}

func makeGroup(kind, key string, files []store.FileRow) store.Group {
	g := store.Group{Key: key, Kind: kind, Files: files, Count: len(files), Name: files[0].Name}
	var maxSize int64
	for _, f := range files {
		g.TotalSize += f.Size
		if f.Size > maxSize {
			maxSize = f.Size
		}
	}
	// 与现有结果页同口径：同一 (volume,inode) 的多个名字只算一份空间。
	distinct := map[string]int64{}
	var distinctSum int64
	for _, f := range files {
		k := f.Volume + ":" + strconv.FormatInt(f.Inode, 10)
		if prev, ok := distinct[k]; ok {
			if f.Size > prev {
				distinct[k] = f.Size
				distinctSum += f.Size - prev
			}
		} else {
			distinct[k] = f.Size
			distinctSum += f.Size
		}
	}
	g.Waste = distinctSum - maxSize
	if g.Waste < 0 {
		g.Waste = 0
	}
	if kind == config.RuleBaseContent && len(files) > 0 {
		g.Hash = files[0].Hash
	}
	return g
}

// ApplyCriteria 把规则条件叠加到媒体组（video/intro/audio/photo/folder/fuzzy/semantic）。
//
// 语义：先剔除副本名/目录外文件，再按每个属性把组切成“同值子集”，
// 子集不足 2 个文件就丢弃；同目录忽略按 D3（整组同目录才丢）。
func ApplyCriteria(groups []store.Group, rules *config.SearchRules, roots []string) []store.Group {
	if rules == nil || len(groups) == 0 {
		return groups
	}
	rules = config.NormalizeSearchRules(rules)
	attrs := attrSet(rules.Attrs)
	// 媒体组不把 name 当“必须同名”，否则相似视频组会被打散；只应用文件属性类条件。
	if attrs[config.RuleAttrNameSimilar] {
		delete(attrs, config.RuleAttrNameSimilar)
	}
	out := make([]store.Group, 0, len(groups))
	for _, g := range groups {
		fs := make([]store.FileRow, 0, len(g.Files))
		for _, f := range g.Files {
			if !underRoots(f.Path, roots) {
				continue
			}
			if rules.IgnoreCopyNames && isCopyName(f.Name, rules.CopyPatterns) {
				continue
			}
			fs = append(fs, f)
		}
		if len(attrs) == 0 {
			if rules.DurationToleranceSec > 0 && len(fs) > 0 {
				// 媒体组的时长容差：以第一个文件为锚，超出容差的成员视为不同视频。
				anchor := fs[0].Duration
				kept := fs[:0]
				for _, f := range fs {
					if anchor == 0 || f.Duration == 0 || abs64(f.Duration-anchor) <= float64(rules.DurationToleranceSec) {
						kept = append(kept, f)
					}
				}
				fs = kept
			}
			if len(fs) >= 2 && !(rules.IgnoreSameFolder && allSameFolder(fs)) {
				ng := g
				ng.Files = fs
				ng.Count = len(fs)
				out = append(out, ng)
			}
			continue
		}
		buckets := map[string][]store.FileRow{}
		for _, f := range fs {
			parts := make([]string, 0, len(attrs))
			for _, a := range config.RuleAttrOrder {
				if attrs[a] {
					parts = append(parts, attrKey(a, f, rules, roots))
				}
			}
			k := strings.Join(parts, "\x1f")
			buckets[k] = append(buckets[k], f)
		}
		for k, sub := range buckets {
			if len(sub) < 2 || (rules.IgnoreSameFolder && allSameFolder(sub)) {
				continue
			}
			ng := makeGroup(g.Kind, g.Key+"|"+k, sub)
			ng.Similarity = g.Similarity
			out = append(out, ng)
		}
	}
	return out
}

// SortByWaste 按可释放空间降序，稳定排序。
func SortByWaste(groups []store.Group) {
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Waste != groups[j].Waste {
			return groups[i].Waste > groups[j].Waste
		}
		return groups[i].Key < groups[j].Key
	})
}

// Similarity 计算两个名字（已去扩展名）的相似度百分比 0–100。
// 用最长公共子序列的近似：1 - 编辑距离/较长长度。导出给测试与媒体条件复用。
func Similarity(a, b string) int {
	if a == b {
		return 100
	}
	la, lb := len([]rune(a)), len([]rune(b))
	if la == 0 || lb == 0 {
		return 0
	}
	d := levenshtein([]rune(a), []rune(b))
	max := la
	if lb > max {
		max = lb
	}
	pct := int(float64(max-d)/float64(max)*100 + 0.5)
	if pct < 0 {
		pct = 0
	}
	return pct
}

func levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

func abs64(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// splitBySimilarName 在已有组内按文件名相似度再切分（“相似文件名”条件）。
// 用贪婪聚类：每个文件与簇首比较，达到阈值就并入；不满足就另起一簇。
func splitBySimilarName(groups []store.Group, pct int) []store.Group {
	if pct <= 0 {
		pct = 90
	}
	out := make([]store.Group, 0, len(groups))
	for _, g := range groups {
		type item struct {
			f    store.FileRow
			stem string
		}
		items := make([]item, 0, len(g.Files))
		for _, f := range g.Files {
			stem := fname.Stem(f.Name)
			if !fname.HasLetters(stem) {
				// 纯数字/符号名不参与相似判断，单独保留在原组里。
				items = append(items, item{f: f, stem: ""})
				continue
			}
			items = append(items, item{f: f, stem: strings.ToLower(stem)})
		}
		clusters := make([][]int, 0, len(items))
		// 先按拼音键首字符 + 长度区间分桶，避免超大组两两比较。
		buckets := map[string][]int{}
		for i, it := range items {
			if it.stem == "" {
				continue
			}
			key := fname.PinyinKey(it.stem)
			first := "?"
			if r := []rune(key); len(r) > 0 {
				first = string(r[0])
			}
			bk := fmt.Sprintf("%s|%d", first, len([]rune(it.stem))/4)
			buckets[bk] = append(buckets[bk], i)
		}
		for _, idxs := range buckets {
			for _, i := range idxs {
				placed := false
				for ci := range clusters {
					if Similarity(items[i].stem, items[clusters[ci][0]].stem) >= pct {
						clusters[ci] = append(clusters[ci], i)
						placed = true
						break
					}
				}
				if !placed {
					clusters = append(clusters, []int{i})
				}
			}
		}
		for ci, c := range clusters {
			if len(c) < 2 {
				continue
			}
			fs := make([]store.FileRow, len(c))
			for j, i := range c {
				fs[j] = items[i].f
			}
			ng := makeGroup(g.Kind, fmt.Sprintf("%s|sim%d", g.Key, ci), fs)
			ng.Similarity = float64(Similarity(items[c[0]].stem, items[c[1]].stem)) / 100
			out = append(out, ng)
		}
	}
	return out
}

// FuzzyGroups 实现“相似文件名”条件：先按拼音键与首字符分桶，再按相似度百分比聚类。
// 返回组与 Totals；组 Kind 为 fuzzy。
func FuzzyGroups(files []store.FileRow, rules *config.SearchRules, roots []string) []store.Group {
	if rules == nil {
		return nil
	}
	pct := rules.NameSimilarPct
	if pct <= 0 {
		pct = 90
	}
	type item struct {
		row  store.FileRow
		stem string
		key  string
	}
	items := make([]item, 0, len(files))
	for _, f := range files {
		if !underRoots(f.Path, roots) {
			continue
		}
		if rules.IgnoreCopyNames && isCopyName(f.Name, rules.CopyPatterns) {
			continue
		}
		stem := fname.Stem(f.Name)
		if !fname.HasLetters(stem) {
			continue
		}
		if rules.CaseSensitive {
			// 保留原样大小写
		} else {
			stem = strings.ToLower(stem)
		}
		items = append(items, item{row: f, stem: stem, key: fname.PinyinKey(stem)})
	}
	// 分桶：拼音键首字符 + 长度区间，避免全量两两比较。
	buckets := map[string][]int{}
	for i, it := range items {
		first := "?"
		if r := []rune(it.key); len(r) > 0 {
			first = string(r[0])
		}
		bk := fmt.Sprintf("%s|%d", first, len([]rune(it.stem))/4)
		buckets[bk] = append(buckets[bk], i)
	}
	out := make([]store.Group, 0)
	for _, idxs := range buckets {
		clusters := make([][]int, 0)
		for _, i := range idxs {
			placed := false
			for ci := range clusters {
				if Similarity(items[i].stem, items[clusters[ci][0]].stem) >= pct {
					clusters[ci] = append(clusters[ci], i)
					placed = true
					break
				}
			}
			if !placed {
				clusters = append(clusters, []int{i})
			}
		}
		for _, c := range clusters {
			if len(c) < 2 {
				continue
			}
			fs := make([]store.FileRow, len(c))
			for j, i := range c {
				fs[j] = items[i].row
			}
			g := makeGroup(config.RuleBaseFuzzy, "fuzzy:"+items[c[0]].key, fs)
			g.Similarity = float64(Similarity(items[c[0]].stem, items[c[1]].stem)) / 100
			out = append(out, g)
		}
	}
	return out
}
