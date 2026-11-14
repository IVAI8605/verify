// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"strings"
)

// SearchRules v3.8.0：DC5 式搜索规则组合。
//
// Base 决定"以什么为主分组"，Attrs 是叠加在分组键上的 AND 条件；
// 两者合起来决定最终分组键。规则只影响查询/搜索阶段，不改变扫描期已算好的指纹。
//
// 本结构是"新配置"；旧 config.json 没有 search_rules 字段时保持 nil，
// 由 web 层回退到旧的 match_mode / match_kinds 行为，保证升级不改变现有结果。
type SearchRules struct {
	Base  string   `json:"base"`
	Attrs []string `json:"attrs"`
	// 相同文件大小的容差（KB）；0 表示必须完全相同。
	SizeToleranceKB int `json:"size_tolerance_kb"`
	// 相似文件名阈值（百分比 50–100）；仅当 Attrs 含 name_similar 时生效。
	NameSimilarPct int `json:"name_similar_pct"`
	// 相同文件夹名称的三种口径：full / depth / scan_root。
	FolderNameMode  string `json:"folder_name_mode"`
	FolderNameDepth int    `json:"folder_name_depth"`
	// 忽略文件名里的"副本"类后缀。
	IgnoreCopyNames bool     `json:"ignore_copy_names"`
	CopyPatterns    []string `json:"copy_patterns"`
	// 忽略同一文件夹中的重复组：整组同目录才丢弃。
	IgnoreSameFolder bool `json:"ignore_same_folder"`
	// 文本/文件名匹配是否区分大小写。
	CaseSensitive bool `json:"case_sensitive"`
	// 媒体档位（仅 base=video/intro/audio/photo 时参考）。
	VideoMode            string `json:"video_mode"`
	AudioWindow          string `json:"audio_window"`
	DurationToleranceSec int    `json:"duration_tolerance_sec"`
}

// 搜索规则基值与属性白名单。
const (
	RuleBaseAll        = "all"
	RuleBaseContent    = "content"
	RuleBaseAttributes = "attributes"
	RuleBaseName       = "name"
	RuleBaseSize       = "size"
	RuleBaseVideo      = "video"
	RuleBaseIntro      = "intro"
	RuleBaseAudio      = "audio"
	RuleBasePhoto      = "photo"
	RuleBaseFolder     = "folder"
	RuleBaseFuzzy      = "fuzzy"
	RuleBaseSemantic   = "semantic"

	RuleAttrName        = "name"
	RuleAttrNameSimilar = "name_similar"
	RuleAttrSize        = "size"
	RuleAttrExt         = "ext"
	RuleAttrVolume      = "volume"
	RuleAttrMtimeDate   = "mtime_date"
	RuleAttrFolderName  = "folder_name"
)

// RuleAttrOrder 是属性的固定顺序：分组键按这个顺序拼接，保证同样的选择得到同样的键。
var RuleAttrOrder = []string{
	RuleAttrName, RuleAttrNameSimilar, RuleAttrSize, RuleAttrExt,
	RuleAttrVolume, RuleAttrMtimeDate, RuleAttrFolderName,
}

// SearchRuleBases 是 base 的合法取值（用于前端下拉与校验）。
var SearchRuleBases = []string{
	RuleBaseAll, RuleBaseContent, RuleBaseAttributes, RuleBaseName, RuleBaseSize,
	RuleBaseVideo, RuleBaseIntro, RuleBaseAudio, RuleBasePhoto, RuleBaseFolder,
	RuleBaseFuzzy, RuleBaseSemantic,
}

// DefaultSearchRules 返回一组保守默认值：只按内容精确分组，不叠加额外条件。
func DefaultSearchRules() *SearchRules {
	return &SearchRules{
		Base:            RuleBaseContent,
		Attrs:           nil,
		NameSimilarPct:  90,
		FolderNameMode:  "full",
		FolderNameDepth: 1,
		CopyPatterns:    []string{"副本", "(1)", "(2)", "copy"},
		VideoMode:       "frames",
		AudioWindow:     "15s",
	}
}

// DeriveSearchRules 把旧的 match_mode 迁移成等价的新规则。
// 仅用于没有 search_rules 的旧配置：让新结果页至少能按旧模式出结果。
func DeriveSearchRules(matchMode string) *SearchRules {
	r := DefaultSearchRules()
	switch NormalizeMatchMode(matchMode) {
	case MatchModeExact:
		r.Base = RuleBaseContent
	case MatchModeSimilar:
		r.Base = RuleBaseVideo
	case MatchModeAudio:
		r.Base = RuleBaseAudio
	case MatchModeThumbs:
		r.Base = RuleBaseVideo
		r.VideoMode = "thumbs"
	case MatchModeIgnoreContent:
		r.Base = RuleBaseName
	case MatchModeAll:
		r.Base = RuleBaseAll
	default:
		r.Base = RuleBaseAll
	}
	return r
}

// NormalizeSearchRules 原地规范化规则；nil 原样返回（表示继续沿用旧行为）。
func NormalizeSearchRules(r *SearchRules) *SearchRules {
	if r == nil {
		return nil
	}
	base := strings.TrimSpace(r.Base)
	valid := false
	for _, b := range SearchRuleBases {
		if b == base {
			valid = true
			break
		}
	}
	if !valid {
		base = RuleBaseAll
	}
	r.Base = base

	attrs := make([]string, 0, len(r.Attrs))
	seen := map[string]bool{}
	for _, a := range RuleAttrOrder {
		for _, raw := range r.Attrs {
			if strings.TrimSpace(raw) == a && !seen[a] {
				seen[a] = true
				attrs = append(attrs, a)
			}
		}
	}
	r.Attrs = attrs

	if r.NameSimilarPct < 50 || r.NameSimilarPct > 100 {
		r.NameSimilarPct = 90
	}
	if r.SizeToleranceKB < 0 {
		r.SizeToleranceKB = 0
	}
	if r.SizeToleranceKB > 1<<20 {
		r.SizeToleranceKB = 1 << 20
	}
	switch r.FolderNameMode {
	case "full", "depth", "scan_root":
	default:
		r.FolderNameMode = "full"
	}
	if r.FolderNameDepth < 1 || r.FolderNameDepth > 50 {
		r.FolderNameDepth = 1
	}
	switch r.VideoMode {
	case "frames", "thumbs", "hash":
	default:
		r.VideoMode = "frames"
	}
	switch r.AudioWindow {
	case "15s", "120s", "full":
	default:
		r.AudioWindow = "15s"
	}
	if r.DurationToleranceSec < 0 || r.DurationToleranceSec > 36000 {
		r.DurationToleranceSec = 0
	}
	patterns := make([]string, 0, len(r.CopyPatterns))
	for _, p := range r.CopyPatterns {
		p = strings.TrimSpace(p)
		if p == "" || len([]rune(p)) > 32 {
			continue
		}
		patterns = append(patterns, p)
		if len(patterns) >= 20 {
			break
		}
	}
	if len(patterns) == 0 && r.IgnoreCopyNames {
		patterns = []string{"副本", "(1)", "(2)", "copy"}
	}
	r.CopyPatterns = patterns
	return r
}

// SearchPreset 是一条可复用的搜索规则预设（v3.9.0）。
type SearchPreset struct {
	Name  string      `json:"name"`
	Rules SearchRules `json:"rules"`
}

// NormalizeSearchPresets 规范化预设列表：名称去空白、去重、限制条数与长度。
func NormalizeSearchPresets(in []SearchPreset) []SearchPreset {
	out := make([]SearchPreset, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		name := strings.TrimSpace(p.Name)
		if name == "" || len([]rune(name)) > 64 || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		r := NormalizeSearchRules(&p.Rules)
		if r == nil {
			continue
		}
		out = append(out, SearchPreset{Name: name, Rules: *r})
		if len(out) >= 20 {
			break
		}
	}
	return out
}

// ValidateSearchRules 是严格校验：设置页主动保存时，非法值直接报错，不静默改。
func ValidateSearchRules(r *SearchRules) error {
	if r == nil {
		return nil
	}
	base := strings.TrimSpace(r.Base)
	ok := false
	for _, b := range SearchRuleBases {
		if b == base {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("未知的匹配模式：%s", r.Base)
	}
	if base == RuleBaseAttributes && len(r.Attrs) == 0 {
		return fmt.Errorf("忽略内容模式至少要选择一个比较条件")
	}
	for _, a := range r.Attrs {
		valid := false
		for _, want := range RuleAttrOrder {
			if a == want {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("未知的比较条件：%s", a)
		}
	}
	if r.NameSimilarPct != 0 && (r.NameSimilarPct < 50 || r.NameSimilarPct > 100) {
		return fmt.Errorf("相似文件名阈值应在 50–100 之间")
	}
	if r.FolderNameMode == "depth" && (r.FolderNameDepth < 1 || r.FolderNameDepth > 50) {
		return fmt.Errorf("文件夹层级应在 1–50 之间")
	}
	return nil
}
