// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"sort"
	"strings"
)

// PathMap 是一条「NAS 内部路径 → 外部设备路径」映射，用于 DC5 兼容导出：
// 导出时把 folder_name 的 NAS 前缀替换为 SMB 共享/映射盘符路径，
// DC5（Windows）才能定位到真实文件（缩略图、直接操作）。
type PathMap struct {
	Nas string `json:"nas"` // NAS 内部路径，如 /vol1/media（无尾斜杠）
	Ext string `json:"ext"` // 外部路径，如 \\NAS\media 或 Z:\media（无尾分隔符）
}

// PathMaps 是映射表。转换规则见 ToExt / ToNas。
type PathMaps []PathMap

// ParsePathMaps 把设置页的多行文本解析成映射表。
// 每行格式「NAS路径=外部路径」，以第一个「=」分割；推荐「 = 」两侧留空提高可读性。
func ParsePathMaps(text string) (PathMaps, error) {
	var out PathMaps
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sep := strings.Index(line, "=")
		if sep <= 0 || sep == len(line)-1 {
			return nil, fmt.Errorf("路径映射第 %d 行格式应为「NAS路径=外部路径」，如 /vol1/media = \\\\NAS\\media", i+1)
		}
		out = append(out, PathMap{
			Nas: strings.TrimSpace(line[:sep]),
			Ext: strings.TrimSpace(line[sep+1:]),
		})
	}
	return NormalizePathMaps(out)
}

// NormalizePathMaps 清洗映射表：去空白、去尾分隔符、校验两端合法，并按
// NAS 前缀长度降序排列（最长前缀优先，让更具体的映射赢过宽泛的）。
// 任一条非法即整体报错——设置页保存时需要把错误告诉用户。
func NormalizePathMaps(in PathMaps) (PathMaps, error) {
	return normalizePathMaps(in, false)
}

// NormalizePathMapsLenient 与 NormalizePathMaps 规则相同，但跳过非法条目而非整体失败。
// v3.10.10（P2-8）：读取旧 config.json 时，一条手输错误的映射不应让其余映射
// 全部失效——旧实现会把整张表清空，DC5 导出/导入从此静默不转换路径。
func NormalizePathMapsLenient(in PathMaps) PathMaps {
	out, _ := normalizePathMaps(in, true)
	return out
}

func normalizePathMaps(in PathMaps, skipBad bool) (PathMaps, error) {
	out := make(PathMaps, 0, len(in))
	for _, m := range in {
		// NAS 侧按 Linux 路径归一：反斜杠视为分隔符、去尾斜杠。
		nas := strings.TrimRight(strings.TrimSpace(strings.ReplaceAll(m.Nas, "\\", "/")), "/")
		// 外部侧保留反斜杠（UNC/盘符是 Windows 形态），去尾分隔符；
		// UNC 头（\\）保留，内部的重复反斜杠折叠为单个（从资源管理器
		// 复制路径或多层转义时容易出现）。
		ext := strings.TrimRight(strings.TrimSpace(m.Ext), "\\/")
		if strings.HasPrefix(ext, `\\`) {
			ext = `\\` + strings.ReplaceAll(ext[2:], `\\`, `\`)
		} else {
			ext = strings.ReplaceAll(ext, `\\`, `\`)
		}
		if nas == "" && ext == "" {
			continue
		}
		if !strings.HasPrefix(nas, "/") {
			if skipBad {
				continue
			}
			return nil, fmt.Errorf("路径映射的 NAS 侧必须以 / 开头（如 /vol1/media），当前为「%s」", m.Nas)
		}
		if ext == "" {
			if skipBad {
				continue
			}
			return nil, fmt.Errorf("路径映射 %s 的外部路径不能为空", nas)
		}
		out = append(out, PathMap{Nas: nas, Ext: ext})
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Nas) > len(out[j].Nas) })
	return out, nil
}

// ToExt 把 NAS 路径转换为外部路径；未命中返回 ""（调用方保持原路径，不猜）。
// 目录边界匹配：nas 前缀之后必须是 / 或路径恰好就是 nas 本身，
// 因此 /vol1/media 不会误配 /vol1/media2。结果统一为 \ 分隔符；
// 输入带尾斜杠则输出也带（folder_name 惯例）。
func (ms PathMaps) ToExt(nasPath string) string {
	if len(ms) == 0 {
		return ""
	}
	hadSlash := strings.HasSuffix(nasPath, "/")
	core := strings.TrimRight(nasPath, "/")
	if core == "" {
		return ""
	}
	for _, m := range ms {
		if core != m.Nas && !strings.HasPrefix(core, m.Nas+"/") {
			continue
		}
		rest := strings.ReplaceAll(core[len(m.Nas):], "/", "\\")
		// 外部前缀里用户可能写正斜杠（Z:/电影），统一归一为 Windows 的 \。
		res := strings.ReplaceAll(m.Ext+rest, "/", "\\")
		if hadSlash && !strings.HasSuffix(res, "\\") {
			res += "\\"
		}
		return res
	}
	return ""
}

// ToNas 把外部路径转换回 NAS 路径（导入反向映射）；未命中返回 ""。
// 匹配大小写不敏感（Windows 路径习惯），\ 与 / 分隔符都接受；
// NAS 侧输出统一为 / 分隔符。
func (ms PathMaps) ToNas(extPath string) string {
	if len(ms) == 0 {
		return ""
	}
	core := strings.ReplaceAll(strings.TrimSpace(extPath), "\\", "/")
	for _, m := range ms {
		extSlash := strings.ReplaceAll(m.Ext, "\\", "/")
		n := len(extSlash)
		if len(core) < n || !strings.EqualFold(core[:n], extSlash) {
			continue
		}
		// 目录边界：前缀之后必须是 / 或恰好相等
		if len(core) != n && core[n] != '/' {
			continue
		}
		return m.Nas + core[n:]
	}
	return ""
}
