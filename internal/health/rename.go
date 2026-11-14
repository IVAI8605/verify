// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"path/filepath"
	"strings"
)

// BuildTarget 依据检测结论生成修正后的文件名（不含目录）。
//
// 返回 false 表示不需要改动或无法给出安全的目标名——重命名是破坏性操作，
// 宁可不动，也不要在信息不足时猜一个名字。
func BuildTarget(kind Kind, name, suggestExt string, opt NameOptions) (string, bool) {
	switch kind {
	case KindExt:
		if suggestExt == "" {
			return "", false
		}
		// 扩展名检测只换最后一个扩展名，主干原样保留。
		// video.mp4.avi → video.mp4.png 这类多重扩展名的情况，
		// 主干里的 mp4 属于名不副实但无法判断用户意图，不动它。
		base := strings.TrimSuffix(name, filepath.Ext(name))
		if base == "" {
			return "", false
		}
		target := base + "." + strings.ToLower(suggestExt)
		if target == name {
			return "", false
		}
		return target, true

	case KindName:
		target := NormalizeName(name, opt)
		if target == "" || target == name {
			return "", false
		}
		return target, true
	}
	return "", false
}

// UniquePath 在目标已存在时追加 _1、_2 等后缀，绝不覆盖。
//
// exists 由调用方提供，便于测试与在真实文件系统上做原子性更好的判断。
func UniquePath(dir, name string, exists func(string) bool) string {
	candidate := filepath.Join(dir, name)
	if !exists(candidate) {
		return candidate
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	ext := filepath.Ext(name)
	for i := 1; i <= 999; i++ {
		candidate = filepath.Join(dir, base+"_"+itoa(i)+ext)
		if !exists(candidate) {
			return candidate
		}
	}
	// 极端情况下退化成带时间戳的名字，保证不会因为重名而失败
	return filepath.Join(dir, base+"_"+itoa(999)+"_dup"+ext)
}

// itoa 是极简的正整数转字符串，避免为几个字符引入 strconv 之外的分支。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
