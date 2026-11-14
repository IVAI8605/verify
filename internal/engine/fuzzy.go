// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"sort"
	"sync"

	"verify/internal/fname"
	"verify/internal/store"
)

// F3 模糊文件名分组（v1.34 引入）：文件主名统一到音序域（汉字→无调拼音）
// 后按 键相同 / 编辑距离≤1 分组。仅分组不删；结果随扫描版本失效。
// 依赖 internal/fname 共享基建（F5/F7 复用）。

// fuzzyCache 模糊分组内存缓存：随 bumpVersion 失效。
type fuzzyCache struct {
	mu     sync.Mutex
	groups []store.Group
	near   bool
	ok     bool
}

// FuzzyGroups 计算模糊文件名分组。near=true 时近似匹配（编辑距离≤1），
// false 仅同键精确匹配。
func (e *Engine) FuzzyGroups(ctx context.Context, near bool) ([]store.Group, error) {
	e.fz.mu.Lock()
	if e.fz.ok && e.fz.near == near {
		g := e.fz.groups
		e.fz.mu.Unlock()
		return g, nil
	}
	e.fz.mu.Unlock()

	files, err := e.st.FuzzyCandidates(ctx)
	if err != nil {
		return nil, err
	}
	// 主名（去扩展名）参与分组：同内容不同容器格式也能归组
	type item struct {
		row  store.FileRow
		stem string
		key  string
	}
	items := make([]item, 0, len(files))
	for _, f := range files {
		stem := fname.Stem(f.Name)
		if !fname.HasLetters(stem) {
			continue // 纯数字/符号名（001 等）噪音太大，不参与
		}
		items = append(items, item{row: f, stem: stem, key: fname.PinyinKey(stem)})
	}
	keys := make([]string, len(items))
	for i, it := range items {
		keys[i] = it.key
	}
	var indexGroups [][]int
	if near {
		indexGroups = fname.NearGroups(keys)
	} else {
		indexGroups = exactGroups(keys)
	}
	groups := make([]store.Group, 0, len(indexGroups))
	for _, idxs := range indexGroups {
		g := store.Group{Key: items[idxs[0]].key}
		best := idxs[0]
		for _, i := range idxs {
			it := items[i]
			g.Files = append(g.Files, it.row)
			g.TotalSize += it.row.Size
			if len(it.stem) < len(items[best].stem) || (len(it.stem) == len(items[best].stem) && it.stem < items[best].stem) {
				best = i
			}
		}
		g.Count = len(g.Files)
		g.Name = items[best].stem
		maxSize := int64(0)
		for _, f := range g.Files {
			if f.Size > maxSize {
				maxSize = f.Size
			}
		}
		g.Waste = g.TotalSize - maxSize
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Count != groups[j].Count {
			return groups[i].Count > groups[j].Count
		}
		return groups[i].Key < groups[j].Key
	})

	e.fz.mu.Lock()
	e.fz.groups = groups
	e.fz.near = near
	e.fz.ok = true
	e.fz.mu.Unlock()
	return groups, nil
}

// exactGroups 仅按键精确分组（近似匹配关闭时）。
func exactGroups(keys []string) [][]int {
	m := map[string][]int{}
	for i, k := range keys {
		if k == "" {
			continue
		}
		m[k] = append(m[k], i)
	}
	out := make([][]int, 0, len(m))
	for _, g := range m {
		if len(g) >= 2 {
			out = append(out, g)
		}
	}
	return out
}
