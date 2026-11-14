// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import "strings"

// DefaultTempExts 默认临时文件清单（v1.34 引入 F1，设置页可编辑）。
// 以 '.' 开头按扩展名后缀匹配（x.tar.tmp 命中 .tmp）；"~" 匹配以 ~ 结尾的
// 编辑器备份；其余按文件名精确匹配（不区分大小写）。
// 浏览器 "(1)/(2) 副本" 类按用户决策**不进默认清单**（误删风险高，用户可自加）。
// v1.42.0：补 .DS_Store（macOS 在每个目录留下的显示配置文件，NAS 上没有用）。
// 此前文案早已存在，但默认清单漏了这一项，默认设置下永远抓不到（BUG-06）。
var DefaultTempExts = []string{
	".tmp", ".bak", ".crdownload", ".part", ".download", ".temp",
	".swp", ".swo", "~", "Thumbs.db", "desktop.ini", ".DS_Store",
}

// CheckTemp 判断文件名是否命中临时文件清单；命中返回 true。
// patterns 为空时不判任何文件（清单清空 = 关闭该检测）。
func CheckTemp(base string, patterns []string) bool {
	return MatchTemp(base, patterns) != ""
}

// MatchTemp 返回命中的清单项，未命中返回空串。
// 带上命中项才能给出具体原因（是浏览器缓存、编辑器备份还是系统缩略图库）。
func MatchTemp(base string, patterns []string) string {
	if len(patterns) == 0 {
		return ""
	}
	lower := strings.ToLower(base)
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		switch {
		case p == "~":
			if strings.HasSuffix(base, "~") {
				return p
			}
		case strings.HasPrefix(p, "."):
			// 只写一个 "." 会匹配到所有文件，直接忽略
			if p == "." {
				continue
			}
			// .DS_Store 这类隐藏文件的整个名字就是清单项，不再要求比清单项更长
			if strings.HasSuffix(lower, strings.ToLower(p)) {
				return p
			}
		default:
			if lower == strings.ToLower(p) {
				return p
			}
		}
	}
	return ""
}

// tempExplain 把命中项翻译成一句人话：这是谁生成的、为什么留在这里。
var tempExplain = map[string]string{
	".tmp":        "临时文件（.tmp）：程序写到一半的中间产物，写完会自己删，留着就是没写完",
	".temp":       "临时文件（.temp）：程序写到一半的中间产物",
	".bak":        "备份文件（.bak）：改文件之前留下的旧版本，确认新版本没问题就可以删",
	".old":        "旧版本备份（.old）：更新时留下的上一版",
	".crdownload": "浏览器下载未完成（.crdownload）：Chrome / Edge 下到一半中断会留下",
	".part":       "下载未完成（.part）：Firefox、下载工具下到一半的中间文件",
	".partial":    "下载未完成（.partial）：下载工具的中间文件",
	".download":   "下载未完成（.download）：下载工具的中间文件",
	".opdownload": "浏览器下载未完成（.opdownload）：Opera 下到一半会留下",
	".td":         "下载未完成（.td）：迅雷的临时文件",
	".!ut":        "下载未完成（.!ut）：uTorrent 的临时文件",
	".!qb":        "下载未完成（.!qb）：qBittorrent 的临时文件",
	".aria2":      "下载未完成（.aria2）：aria2 的控制文件，对应文件下完会自动删",
	"thumbs.db":   "Windows 缩略图缓存（Thumbs.db）：系统给文件夹里的图片视频生成的小图",
	"desktop.ini": "Windows 文件夹配置（desktop.ini）：记录文件夹图标和显示方式，删了会重建",
	".ds_store":   "macOS 文件夹记录（.DS_Store）：记录图标位置等显示设置，在 NAS 上没有用",
	"._":          "macOS 资源分支文件（._开头）：拷贝时带出来的元数据，在 NAS 上没有用",
}

// TempReason 给出命中临时文件的具体说明；用户自定义的清单项没有对应文案时，
// 退回"命中清单项 X"，至少让人知道是按哪条规则抓到的。
func TempReason(base, pattern string) string {
	if s, ok := tempExplain[strings.ToLower(pattern)]; ok {
		return s
	}
	if strings.HasPrefix(base, "._") {
		return tempExplain["._"]
	}
	if pattern == "~" {
		return "编辑器备份（以 ~ 结尾）：改文件时自动留的旧版本"
	}
	return "命中临时文件清单项 " + pattern
}

// SanitizeTempPatterns 清洗用户编辑的清单：去空白、去重、截断超长项。
func SanitizeTempPatterns(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		p := strings.TrimSpace(raw)
		if p == "" || len(p) > 120 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
