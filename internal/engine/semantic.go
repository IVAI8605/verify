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

// F7 语义核心键分组（v1.36，轻量词典版）：文件名去噪（词典+规则）后按核心键
// 精确分组。仅辅助分组展示，不参与删除判定（用户决策）。

// semanticCache 语义分组内存缓存（独立于 fuzzy 缓存）。
type semanticCache struct {
	mu     sync.Mutex
	groups []store.Group
	ok     bool
}

// SemanticGroups 计算语义分组，随 bumpVersion 失效（缓存同 fuzzy）。
func (e *Engine) SemanticGroups(ctx context.Context) ([]store.Group, error) {
	e.sm.mu.Lock()
	if e.sm.ok {
		g := e.sm.groups
		e.sm.mu.Unlock()
		return g, nil
	}
	e.sm.mu.Unlock()

	files, err := e.st.FuzzyCandidates(ctx)
	if err != nil {
		return nil, err
	}
	noise := fname.NoiseSet(e.cfg.Get().NoiseWords)
	keys := make([]string, len(files))
	valid := make([]int, 0, len(files))
	for i, f := range files {
		k := fname.CoreKey(fname.Stem(f.Name), noise)
		keys[i] = k
		if k != "" && fname.HasMeaningful(k) {
			valid = append(valid, i)
		}
	}
	groups := make([]store.Group, 0)
	for _, idxs := range exactGroupsOf(valid, keys) {
		if len(idxs) < 2 {
			continue
		}
		g := store.Group{Key: keys[idxs[0]]}
		best := idxs[0]
		for _, i := range idxs {
			f := files[i]
			g.Files = append(g.Files, f)
			g.TotalSize += f.Size
			stem := fname.Stem(f.Name)
			if len(stem) < len(fname.Stem(files[best].Name)) {
				best = i
			}
		}
		g.Count = len(g.Files)
		g.Name = fname.Stem(files[best].Name)
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

	e.sm.mu.Lock()
	e.sm.groups = groups
	e.sm.ok = true
	e.sm.mu.Unlock()
	return groups, nil
}

// exactGroupsOf 按 keys 对 idxs 内的下标做精确键分组（返回含单元素组）。
func exactGroupsOf(idxs []int, keys []string) [][]int {
	m := map[string][]int{}
	for _, i := range idxs {
		m[keys[i]] = append(m[keys[i]], i)
	}
	out := make([][]int, 0, len(m))
	for _, g := range m {
		out = append(out, g)
	}
	return out
}
