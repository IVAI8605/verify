// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// F4 智能保留策略（一期规则打分版，v1.40 引入）：
// 信号集打分 → 每组保留建议 + 置信分级 + 一句话理由。
// **永不自动删除**——应用建议只改选择标记，清理始终走用户确认的既有通道。

// RetainFile 参与打分的文件（无状态 API 输入）。
type RetainFile struct {
	Path     string  `json:"path"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	BitRate  int64   `json:"bit_rate"`
	Duration float64 `json:"duration"`
	Mtime    int64   `json:"mtime"`
	Broken   bool    `json:"broken"` // health 结论：损坏/无法解码
}

// RetainScores 各信号得分（0–100），界面逐项展示。
type RetainScores struct {
	Quality   float64 `json:"quality"`   // 分辨率×码率（媒体质量）
	Integrity float64 `json:"integrity"` // 完整性（损坏重罚）
	PathTidy  float64 `json:"path_tidy"` // 路径整洁（更浅、无冗余目录）
	NameQ     float64 `json:"name_q"`    // 文件名质量（有描述、无噪声）
	Fresh     float64 `json:"fresh"`     // 修改时间新
}

// RetainSuggestion 单组建议。
type RetainSuggestion struct {
	Retain     string       `json:"retain"`               // 建议保留的路径
	Confidence string       `json:"confidence"`           // high | medium | low
	Reason     string       `json:"reason"`               // 一句话理由
	Scores     []RetainItem `json:"scores"`               // 每个候选的总分与明细
	Alternates []string     `json:"alternates,omitempty"` // 置信低时的其他合理候选
}

// RetainItem 单候选打分。
type RetainItem struct {
	Path   string       `json:"path"`
	Name   string       `json:"name"`
	Total  float64      `json:"total"`
	Detail RetainScores `json:"detail"`
}

// RetainQualityFirst 用户决策：默认质量/完整性为主、路径整洁为辅。
var RetainQualityFirst = true

// RetainSuggest 对一组重复文件给出保留建议。调用方保证 files ≥2。
func RetainSuggest(files []RetainFile) RetainSuggestion {
	items := make([]RetainItem, 0, len(files))
	for _, f := range files {
		var q, integ, tidy, nq, fresh float64
		// 媒体质量：分辨率面积（对数）+ 码率（对数），无媒体信息的文件取 0
		if f.Width > 0 && f.Height > 0 {
			q += clamp01(math.Log2(float64(f.Width*f.Height)+1)/22) * 60
		}
		if f.BitRate > 0 {
			q += clamp01(math.Log2(float64(f.BitRate)+1)/22) * 40
		}
		// 完整性：损坏几乎一票否决
		if f.Broken {
			integ = 0
		} else {
			integ = 100
		}
		// 路径整洁：路径越浅越高；@eaDir/点开头目录重罚
		depth := strings.Count(f.Path, "/")
		tidy = clamp01(1-float64(depth)/12) * 100
		if strings.Contains(f.Path, "/@eaDir/") || containsHiddenDir(f.Path) {
			tidy = 0
		}
		// 文件名质量：含 CJK 描述或字母词加分，噪声（连续下划线/大写串）减分
		nq = nameQuality(f.Name)
		// 新旧：组内相对比较在归一化时处理，这里先存原始（0-100 按 10 年窗）
		if f.Mtime > 0 {
			ageYears := float64(nowUnix()-f.Mtime) / (365.25 * 86400)
			fresh = clamp01(1-ageYears/10) * 100
		}
		w := currentRetainWeights()
		total := w.Quality*q + w.Integrity*integ + w.PathTidy*tidy + w.NameQ*nq + w.Fresh*fresh
		items = append(items, RetainItem{Path: f.Path, Name: f.Name, Total: total,
			Detail: RetainScores{Quality: q, Integrity: integ, PathTidy: tidy, NameQ: nq, Fresh: fresh}})
	}
	// 按总分排序（同分按路径稳定序）
	sort.SliceStable(items, func(i, j int) bool { return items[i].Total > items[j].Total })
	top := items[0]
	sug := RetainSuggestion{Retain: top.Path, Scores: items}

	// 置信分级：第一名显著领先 → high；接近 → low；中间 → medium
	lead := 0.0
	if len(items) > 1 {
		lead = top.Total - items[1].Total
	}
	switch {
	case top.Detail.Integrity == 0 && len(items) > 1:
		sug.Confidence = "high"
		sug.Reason = "候选中有损坏文件，保留完好的那份"
	case lead >= 15:
		sug.Confidence = "high"
		sug.Reason = retainReason(top, items[1])
	case lead >= 5:
		sug.Confidence = "medium"
		sug.Reason = retainReason(top, items[1])
	default:
		sug.Confidence = "low"
		sug.Reason = "各候选得分接近，建议人工确认"
		for _, it := range items[1:] {
			if top.Total-it.Total < 5 {
				sug.Alternates = append(sug.Alternates, it.Path)
			}
		}
	}
	return sug
}

func retainReason(top, second RetainItem) string {
	d := top.Detail
	switch {
	case d.Quality >= second.Detail.Quality+10:
		return "分辨率/码率更高，画质更好"
	case d.Integrity > second.Detail.Integrity:
		return "该文件可正常解码，另一份存在损坏"
	case d.PathTidy >= second.Detail.PathTidy+10:
		return "所在路径更规范"
	case d.NameQ >= second.Detail.NameQ+10:
		return "文件名更规范易识别"
	case d.Fresh >= second.Detail.Fresh+10:
		return "修改时间更新"
	default:
		return "综合质量更优"
	}
}

func nowUnix() int64 { return time.Now().Unix() }

func nameQuality(name string) float64 {
	score := 50.0
	hasCJK, hasLetters := false, false
	for _, r := range name {
		if unicode.Is(unicode.Han, r) {
			hasCJK = true
		}
		if unicode.IsLetter(r) {
			hasLetters = true
		}
	}
	if hasCJK {
		score += 30
	} else if hasLetters {
		score += 15
	}
	if strings.Contains(name, "__") || strings.Contains(name, "  ") {
		score -= 15
	}
	lower := strings.ToLower(name)
	for _, noise := range []string{"copy", "副本", "final", "new_", "(1)", "(2)"} {
		if strings.Contains(lower, noise) {
			score -= 10
		}
	}
	if score < 0 {
		score = 0
	}
	return score
}

func containsHiddenDir(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, ".") && seg != "." && seg != ".." {
			return true
		}
	}
	return false
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// 质量/路径权重由 defaultRetainWeights 与偏好反哺统一提供（v3.3.0）。

// BrokenPathsSet 查询损坏文件集合（完整性信号用，一次全量很小）。
// 放在 engine 是因为它被无状态打分 API 使用。
func (e *Engine) BrokenPathsSet(ctx context.Context) (map[string]bool, error) {
	return e.st.BrokenPathsSet(ctx)
}
