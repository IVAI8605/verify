// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"
	"verify/internal/config"
	"verify/internal/store"
)

// matchModeAllows 判断当前匹配模式是否允许某一类分组查询（v2.1.0）。
// 模式只影响分组/展示入口，不改变扫描期已算好的特征，所以切换模式不需要重扫。
// v3.6.0：自定义组合按 match_kinds 逐项放行。
func matchModeAllows(cfg *config.Config, kind string) bool {
	mode := config.NormalizeMatchMode(cfg.MatchMode)
	switch mode {
	case config.MatchModeExact:
		return kind == "exact" || kind == "folder"
	case config.MatchModeSimilar:
		return kind == "video" || kind == "intro" || kind == "audio"
	case config.MatchModeAudio:
		return kind == "audio"
	case config.MatchModeIgnoreContent:
		return kind == "name" || kind == "fuzzy" || kind == "semantic"
	case config.MatchModeThumbs:
		// 仅缩略图模式只开放相似视频页，分组算法在引擎侧改用 thumbOnly
		return kind == "video"
	case config.MatchModeCustom:
		for _, k := range cfg.MatchKinds {
			if k == kind {
				return true
			}
		}
		return false
	default: // all：沿用 v1.x 多依据并存
		return true
	}
}

// writeModeDisabled 用与「重复文件夹已关闭」相同的响应形状，前端可直接复用提示逻辑。
func writeModeDisabled(w http.ResponseWriter, kind string) {
	writeJSON(w, 200, matchModeDisabledResponse{Groups: []store.Group{}, Total: 0, Disabled: true, Reason: "当前匹配模式未启用" + modeKindLabel(kind)})
}

func modeKindLabel(kind string) string {
	switch kind {
	case "exact":
		return "精确重复"
	case "name":
		return "同名文件"
	case "video":
		return "相似视频"
	case "intro":
		return "相同片头"
	case "audio":
		return "重复音频"
	case "photo":
		return "相似图片"
	case "folder":
		return "重复文件夹"
	case "fuzzy":
		return "模糊文件名"
	case "semantic":
		return "语义分组"
	default:
		return "该结果页"
	}
}
