// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 本文件是 v3.3.0 的「偏好反哺」（设计文档 §4.3 V3.3-1）。
//
// 现状：user_pref 只统计采纳/改选，从未影响打分。这里把统计结果换算成
// 质量 / 路径整洁两项权重，并让 RetainSuggest 读取；仍然只改标记，不删除。
//
// 模型刻意保守：
//   - 不足 20 条记录时完全使用默认权重（零回归）；
//   - 改选比例越高，说明只按画质排序越不合用户习惯，逐步提高路径权重；
//   - 权重总和保持 1，完整性/文件名/新旧三项不变；
//   - 前缀统计只展示，不参与打分，避免「某个目录因为有历史记录就直接胜出」。

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// RetainWeights 是五项信号的权重（总和约等于 1）。
type RetainWeights struct {
	Quality   float64 `json:"quality"`
	Integrity float64 `json:"integrity"`
	PathTidy  float64 `json:"path_tidy"`
	NameQ     float64 `json:"name_q"`
	Fresh     float64 `json:"fresh"`
}

// RetainPrefixStat 是按目录前缀聚合的偏好统计（仅展示）。
type RetainPrefixStat struct {
	Prefix  string `json:"prefix"`
	Adopt   int    `json:"adopt"`
	Changed int    `json:"changed"`
}

// RetainProfile 是学习档案，对应 GET /api/retain/pref/profile。
type RetainProfile struct {
	Learned     int                `json:"learned"`
	Adopt       int                `json:"adopt"`
	Changed     int                `json:"changed"`
	Enabled     bool               `json:"enabled"`
	Weights     RetainWeights      `json:"weights"`
	TopPrefixes []RetainPrefixStat `json:"top_prefixes"`
}

// retainLearn 保存当前生效的学习结果；enabled=false 时忽略权重。
var retainLearn struct {
	mu       sync.RWMutex
	enabled  bool
	weights  RetainWeights
	prefixes []RetainPrefixStat
	learned  int
	adopt    int
	changed  int
}

const retainLearnMetaKey = "retain_learn_enabled"
const retainLearnMinSamples = 20

// defaultRetainWeights 按现有全局开关给出默认权重。
func defaultRetainWeights() RetainWeights {
	q, p := 0.45, 0.10
	if !RetainQualityFirst {
		q, p = 0.25, 0.40
	}
	return RetainWeights{Quality: q, Integrity: 0.30, PathTidy: p, NameQ: 0.10, Fresh: 0.05}
}

// currentRetainWeights 返回打分时应使用的权重。
func currentRetainWeights() RetainWeights {
	retainLearn.mu.RLock()
	defer retainLearn.mu.RUnlock()
	if retainLearn.enabled {
		return retainLearn.weights
	}
	return defaultRetainWeights()
}

// RetainLearnEnabled 返回偏好反哺是否已启用。
func (e *Engine) RetainLearnEnabled() bool {
	retainLearn.mu.RLock()
	defer retainLearn.mu.RUnlock()
	return retainLearn.enabled
}

// LoadRetainProfile 在启动时读取持久化开关并重算一次档案。
func (e *Engine) LoadRetainProfile(ctx context.Context) {
	enabled := false
	if v, ok, err := e.st.GetMeta(ctx, retainLearnMetaKey); err == nil && ok {
		enabled = v == "1"
	}
	_, _ = e.RefreshRetainProfile(ctx, enabled, false)
}

// RefreshRetainProfile 重新聚合偏好并（可选）持久化开关。
func (e *Engine) RefreshRetainProfile(ctx context.Context, enable bool, persist bool) (RetainProfile, error) {
	entries, err := e.st.ListUserPref(ctx, 10000)
	if err != nil {
		return RetainProfile{}, err
	}
	learned := len(entries)
	changed := 0
	prefixCount := map[string]*RetainPrefixStat{}
	for _, it := range entries {
		// v3.17.6（F-22）：chosen 为空或与建议相同都算「采纳」——用户明确点了建议项时，
		// 旧逻辑把它记成改选并给出相反的前缀权重，与 UserPrefStats 的统计口径对不上。
		if it.Chosen == "" || it.Chosen == it.Suggested {
			// 采纳建议：suggested 就是用户实际保留的文件。
			p := retainPrefix(it.Suggested)
			if p != "" {
				if prefixCount[p] == nil {
					prefixCount[p] = &RetainPrefixStat{Prefix: p}
				}
				prefixCount[p].Adopt++
			}
			continue
		}
		// 改选：chosen 是被清理的路径，记作该前缀的「不采纳」。
		changed++
		p := retainPrefix(it.Chosen)
		if p != "" {
			if prefixCount[p] == nil {
				prefixCount[p] = &RetainPrefixStat{Prefix: p}
			}
			prefixCount[p].Changed++
		}
	}
	prefixes := make([]RetainPrefixStat, 0, len(prefixCount))
	for _, v := range prefixCount {
		prefixes = append(prefixes, *v)
	}
	sort.Slice(prefixes, func(i, j int) bool {
		ai, aj := prefixes[i].Adopt+prefixes[i].Changed, prefixes[j].Adopt+prefixes[j].Changed
		if ai != aj {
			return ai > aj
		}
		return prefixes[i].Prefix < prefixes[j].Prefix
	})
	if len(prefixes) > 8 {
		prefixes = prefixes[:8]
	}

	w := defaultRetainWeights()
	if learned >= retainLearnMinSamples {
		ratio := float64(changed) / float64(learned)
		// 改选越多，越偏向「路径/位置稳定性」；quality 从 .45 降到 .15，path 从 .10 升到 .40。
		q := 0.45 - 0.30*ratio
		p := 0.55 - q
		w.Quality, w.PathTidy = round2(q), round2(p)
	}

	retainLearn.mu.Lock()
	retainLearn.enabled = enable
	retainLearn.weights = w
	retainLearn.prefixes = prefixes
	retainLearn.learned = learned
	retainLearn.adopt = learned - changed
	retainLearn.changed = changed
	retainLearn.mu.Unlock()

	if persist {
		flag := "0"
		if enable {
			flag = "1"
		}
		_ = e.st.SetMeta(ctx, retainLearnMetaKey, flag)
	}
	return RetainProfile{
		Learned: learned, Adopt: learned - changed, Changed: changed, Enabled: enable,
		Weights: w, TopPrefixes: prefixes,
	}, nil
}

// RetainProfileNow 返回当前档案（不重算）。
func (e *Engine) RetainProfileNow(ctx context.Context) RetainProfile {
	_ = ctx
	retainLearn.mu.RLock()
	defer retainLearn.mu.RUnlock()
	ps := make([]RetainPrefixStat, len(retainLearn.prefixes))
	copy(ps, retainLearn.prefixes)
	return RetainProfile{
		Learned: retainLearn.learned, Adopt: retainLearn.adopt, Changed: retainLearn.changed,
		Enabled: retainLearn.enabled, Weights: currentRetainWeightsLocked(), TopPrefixes: ps,
	}
}

// currentRetainWeightsLocked 在已持锁时取权重，避免 RWMutex 重入。
func currentRetainWeightsLocked() RetainWeights {
	if retainLearn.enabled {
		return retainLearn.weights
	}
	return defaultRetainWeights()
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

// retainPrefix 取 NAS 路径的前两段作为可读前缀（/vol6/1000/<共享目录>/... → /vol6/1000）。
func retainPrefix(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	parts := strings.Split(p, "/")
	n := 2
	if len(parts) < n {
		n = len(parts)
	}
	return "/" + strings.Join(parts[:n], "/")
}
