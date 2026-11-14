// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package fname

import (
	"strings"
	"unicode"
)

// F7 语义核心键（v1.36 引入，轻量词典版）：文件名去噪后提取语义核心键。
// 仅辅助分组，不参与删除判定（用户决策）。

// DefaultNoiseWords 默认噪声词（规范化后的小写形态）：
// 分辨率 / 编码 / 片源 / 音轨 / 语言字幕 / 常见发布组与水印词。
// 纯数字序号（第01集、S01E01）与 8 位以上十六进制 hash 由规则处理，不进词典。
var DefaultNoiseWords = []string{
	// 分辨率/画质
	"480p", "576p", "720p", "1080p", "1080i", "2160p", "4k", "8k", "sd", "hd", "fhd", "uhd",
	"hdr", "hdr10", "hdr10plus", "dv", "dolby", "vision", "sdr", "remux",
	// 编码
	"h264", "h265", "hevc", "avc", "x264", "x265", "av1", "vp9", "mpeg2", "mpeg4", "divx", "xvid",
	"10bit", "8bit", "aac", "ac3", "eac3", "dts", "dtshd", "truehd", "atmos", "flac", "mp3", "opus",
	// 片源
	"webdl", "web", "dl", "webrip", "bluray", "blu", "ray", "bdrip", "brrip", "dvdrip", "hdtv",
	"dvd", "bd", "hdrip", "cam", "ts", "tc", "remastered", "repack", "extended", "uncut",
	"internal", "limited", "complete", "proper", "real", "fixed",
	// 语言/字幕/音轨
	"chs", "cht", "eng", "chi", "gb", "big5", "mandarin", "cantonese", "dual", "multi", "subs",
	"sub", "nobsub", "dubbed", "简中", "繁中", "国语", "粤语", "中字", "双语", "简繁", "外挂",
	// 发布形态/常见水印词
	"vip", "gbdvd", "ghd", "hdweb", "et", "ymzt", ".team", "patch", "crack", "keygen",
	// 杂项
	"final", "update", "copy", "backup", "new", "old", "temp",
}

// SanitizeNoiseWords 清洗用户编辑的噪声词典（同 TempPatterns 风格）。
func SanitizeNoiseWords(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		w := strings.ToLower(strings.TrimSpace(raw))
		if w == "" || len(w) > 60 || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// isNoiseToken 规则类噪声：纯数字 token、SxxExx、8 位以上十六进制 hash。
func isNoiseToken(tok string) bool {
	if tok == "" {
		return true
	}
	// S01E01 / s1e2 剧集编号
	lower := strings.ToLower(tok)
	if len(lower) >= 4 && lower[0] == 's' && strings.Contains(lower, "e") {
		digitsOnly := true
		for _, c := range lower[1:] {
			if c != 'e' && (c < '0' || c > '9') {
				digitsOnly = false
				break
			}
		}
		if digitsOnly {
			return true
		}
	}
	// 8 位以上纯十六进制（疑似 hash/哈希片段）
	if len(tok) >= 8 {
		hexish := true
		for _, c := range tok {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				hexish = false
				break
			}
		}
		if hexish {
			return true
		}
	}
	// 纯数字（序号/年份除外：4 位年份 19xx/20xx 保留——常是有效语义）
	if len(tok) < 8 {
		allDigits := true
		for _, c := range tok {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits && !(len(tok) == 4 && (strings.HasPrefix(tok, "19") || strings.HasPrefix(tok, "20"))) {
			return true
		}
	}
	return false
}

// CoreKey 语义核心键：规范化 → 按分隔符分词 → 剥离噪声（词典+规则）→
// 存留词转音序拼接。先分词再转拼音，保证 CJK 噪声词（如"简中"）整词命中词典。
// "沙丘.2016.1080p" 与 "shaqiu" 核心键相同。
func CoreKey(name string, noise map[string]bool) string {
	norm := Normalize(name)
	if norm == "" {
		return ""
	}
	var b strings.Builder
	for _, tok := range strings.Fields(norm) {
		if noise[tok] || isNoiseToken(tok) {
			continue
		}
		b.WriteString(PinyinKey(tok))
	}
	return b.String()
}

// HasMeaningful 判断核心键是否含字母/汉字（全噪声名不参与分组）。
func HasMeaningful(key string) bool {
	for _, r := range key {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// NoiseSet 把词典切片转集合（复用 Normalize 归一）。
func NoiseSet(words []string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" {
			m[w] = true
		}
	}
	return m
}
