// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package config

// 本文件是 v3.0.0 新增的配置校验单点（设计文档 §1.4）。
// Load（从磁盘读）与 Replace（设置页保存）此前各自维护一份字段范围校验，
// 新增字段容易漏改其中一处。现在两条路径统一走 validateConfig，
// 后续加字段只需要改这里一次。
//
// 两个入口的差异只有一处：PathMaps 格式非法时，
// Load 属于读旧配置、要防御性丢弃坏条目；Replace 是用户主动保存、要报错拦住。
// 用 strict 参数区分，避免把两种语义混在一起。

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"verify/internal/fname"
	"verify/internal/health"
	"verify/internal/volpath"
)

// ValidationError 表示配置值本身不合法（用户输入问题），与落盘 / 序列化失败区分。
// v3.17.3（F-02）：扫描根校验失败此前落到 handler 的 500 分支，接口看着像服务端故障，
// 实际是请求参数问题；调用方据此返回 400。
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

// asValidationError 把校验错误包装成 ValidationError，保留原始中文提示。
func asValidationError(err error) error {
	if err == nil {
		return nil
	}
	return &ValidationError{msg: err.Error()}
}

// validateConfig 集中所有字段范围校验和规范化，原地修改传入的 Config。
// strict=false（Load）遇到坏的 PathMaps 静默丢弃；strict=true（Replace）返回错误。
func validateConfig(c *Config, strict bool) error {
	if c.HashWorkers < 1 || c.HashWorkers > 32 {
		c.HashWorkers = 4
	}
	// v3.7.0：操作记录配置范围。旧 config.json 缺这些字段时保留 Default 里的值。
	if c.OpLogRetentionDays < 0 || c.OpLogRetentionDays > 3650 {
		c.OpLogRetentionDays = 180
	}
	if c.OpLogDetailLimit < 0 || c.OpLogDetailLimit > 1000 {
		c.OpLogDetailLimit = 200
	}
	c.ExcludePaths = NormalizePaths(c.ExcludePaths)
	c.ProtectPaths = NormalizePaths(c.ProtectPaths)
	// v3.17.3（F-02）：扫描根必须是 /vol<数字> 下的路径。此前 Volumes 直接来自
	// 设置页与 config.json，填 /etc 就能把任意目录变成受管卷，ValidPath 的卷前缀
	// 判定随之失效（改名、隔离、永久删除都会对系统目录开放）。
	// 空列表表示自动发现 /vol*，保持合法。
	if len(c.Volumes) > 0 {
		vols := make([]string, 0, len(c.Volumes))
		for _, v := range c.Volumes {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if !volpath.IsCleanVolRoot(v) {
				if strict {
					return fmt.Errorf("扫描根必须是 /vol 下的目录：%s", v)
				}
				continue
			}
			vols = append(vols, path.Clean(v))
		}
		c.Volumes = vols
	}
	c.MatchMode = NormalizeMatchMode(c.MatchMode)
	c.MatchKinds = NormalizeMatchKinds(c.MatchKinds)
	// v3.8.0：搜索规则按固定顺序归一化；严格模式下非法值直接报错。
	// v3.10.10（P2-9）：strict 必须先校验原始值再规范化。NormalizeSearchRules 会把
	// 未知 base 静默改成 all，先规范化会让「保存非法值要报错」的承诺永不触发。
	if strict && c.SearchRules != nil {
		if err := ValidateSearchRules(c.SearchRules); err != nil {
			return err
		}
	}
	c.SearchRules = NormalizeSearchRules(c.SearchRules)
	if c.SearchRules != nil {
		if err := ValidateSearchRules(c.SearchRules); err != nil {
			if strict {
				return err
			}
			c.SearchRules = nil
		}
	}
	// 预设同理：先校验原始规则，避免被 NormalizeSearchRules 抹成 all 后漏判。
	// 无名预设会被 NormalizeSearchPresets 丢弃，无需校验。
	if strict {
		for i := range c.SearchPresets {
			if strings.TrimSpace(c.SearchPresets[i].Name) == "" {
				continue
			}
			if err := ValidateSearchRules(&c.SearchPresets[i].Rules); err != nil {
				return err
			}
		}
	}
	c.SearchPresets = NormalizeSearchPresets(c.SearchPresets)
	// v3.6.0：自定义组合没勾任何类型时退回全部，避免结果页全空且无提示
	if c.MatchMode == MatchModeCustom && len(c.MatchKinds) == 0 {
		c.MatchMode = MatchModeAll
	}
	// v3.10.10（P2-8）：宽松模式逐条跳过坏映射，而不是把整张表清空——
	// 一条手输错误的路径映射不该让其余映射全部失效。
	pm := NormalizePathMapsLenient(c.PathMaps)
	if strict {
		strictPM, err := NormalizePathMaps(c.PathMaps)
		if err != nil {
			return err
		}
		pm = strictPM
	}
	c.PathMaps = pm
	if c.AudioThreshold <= 0 || c.AudioThreshold > 1 {
		c.AudioThreshold = 0.82
	}
	if c.PHashHamming < 0 || c.PHashHamming > 64 {
		c.PHashHamming = 10
	}
	if c.MinSize < 0 {
		c.MinSize = 1
	}
	if c.VideoWorkers < 1 || c.VideoWorkers > 4 {
		c.VideoWorkers = 2
	}
	if c.IntroSeconds < 1 || c.IntroSeconds > 300 {
		c.IntroSeconds = 10
	}
	if c.ImgHamming < 0 || c.ImgHamming > 64 {
		c.ImgHamming = 10
	}
	if c.IgnoreIntroSeconds < 0 || c.IgnoreIntroSeconds > 3600 {
		c.IgnoreIntroSeconds = 0
	}
	if c.IgnoreOutroSeconds < 0 || c.IgnoreOutroSeconds > 3600 {
		c.IgnoreOutroSeconds = 0
	}
	c.AudioWindowSeconds = normalizeAudioWindow(c.AudioWindowSeconds)
	c.MemBudgetMB = clampInt(c.MemBudgetMB, 0, 64, 65536)
	c.SearchMemLimitMB = clampInt(c.SearchMemLimitMB, 0, 64, 4096)
	c.ThumbCacheMaxMB = clampInt(c.ThumbCacheMaxMB, 0, 64, 4096)
	c.TempPatterns = health.SanitizeTempPatterns(c.TempPatterns)
	c.NoiseWords = fname.SanitizeNoiseWords(c.NoiseWords)
	switch c.VideoDensity {
	case "low", "medium", "high":
	default:
		c.VideoDensity = "medium"
	}
	// v3.3.0：AI 连接器只接受 http/https 地址；非法地址直接清空并关闭，
	// 避免保存后在请求时才发现不可用。模型名留空回退默认值。
	c.AIEndpoint = strings.TrimRight(strings.TrimSpace(c.AIEndpoint), "/")
	if c.AIEndpoint != "" {
		u, err := url.Parse(c.AIEndpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			c.AIEndpoint = ""
			c.AIEnabled = false
		}
	}
	c.AIModel = strings.TrimSpace(c.AIModel)
	if c.AIModel == "" {
		c.AIModel = "llama3.2"
	}
	return nil
}
