// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package fname 提供文件名解析共享基建（v1.34 引入，F3/F5/F7 复用）：
// 规范化 → 音序键（汉字转无调拼音）→ 编辑距离近邻分组。
// 纯函数、不碰库与 HTTP；拼音依赖 mozillazg/go-pinyin（纯 Go、MIT、词典内嵌）。
package fname

import (
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"
)

// pinyinArgs 无调拼音、常用音（Heteronym=false）；非 CJK 字符原样保留。
var pinyinArgs = pinyin.Args{Style: pinyin.Normal, Heteronym: false}

func init() {
	// 没有拼音的字符（符号/拉丁/数字）：原样保留进音序键
	pinyinArgs.Fallback = func(r rune, a pinyin.Args) []string {
		return []string{string(unicode.ToLower(r))}
	}
}

// Normalize 规范化文件名（不含扩展名）：转小写、分隔符折叠为单个空格、去首尾。
func Normalize(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	b.Grow(len(lower))
	lastSpace := false
	for _, r := range lower {
		switch r {
		case ' ', '.', '_', '-', '·', '　':
			if !lastSpace {
				b.WriteByte(' ')
			}
			lastSpace = true
		default:
			b.WriteRune(r)
			lastSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// PinyinKey 音序键：汉字逐字转无调拼音（常用音），其余字符规范化后保留。
// "沙丘2" 与 "shaqiu2" 得到相同键——音序域天然消除拼音↔汉字的歧义。
func PinyinKey(name string) string {
	norm := Normalize(name)
	if norm == "" {
		return ""
	}
	parts := pinyin.LazyPinyin(norm, pinyinArgs)
	return strings.Join(parts, "")
}

// HasLetters 判断规范化后是否含字母或汉字（过滤纯数字/纯符号文件名，如 "001"）。
func HasLetters(name string) bool {
	for _, r := range name {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// Stem 返回去掉最后一个扩展名的主名（不含点）。
func Stem(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[:i]
	}
	return name
}

// editDistanceLE1 判断两串编辑距离是否 ≤1（插入/删除/替换）。
// 长度差 >1 直接假；等长时数不同字符数。O(n)。
func editDistanceLE1(a, b string) bool {
	la, lb := len([]rune(a)), len([]rune(b))
	if la > lb {
		a, b = b, a
		la, lb = lb, la
	}
	if lb-la > 1 {
		return false
	}
	ra, rb := []rune(a), []rune(b)
	diff := 0
	i, j := 0, 0
	for i < la && j < lb {
		if ra[i] == rb[j] {
			i++
			j++
			continue
		}
		diff++
		if diff > 1 {
			return false
		}
		if la == lb {
			i++
			j++
		} else {
			j++ // 长串跳过一个（插入/删除）
		}
	}
	// 尾部剩余算一次差异
	if j < lb || i < la {
		diff++
	}
	return diff <= 1
}

// variantHasher 把每个键的「删一字符」变体算成 64 位哈希，复用缓冲区避免逐键分配。
//
// v3.10.11（第七轮 P2-7）：原实现为每个键生成 L+1 个真实字符串并全部留在
// map[string]int 里，16.8 万个键常驻约 670 万个字符串（数百 MB）。
// 改为只保留哈希后，内存大约降到 1/4～1/5；哈希碰撞只会多一次
// editDistanceLE1 校验，不会把不同的键错误合并。
type variantHasher struct {
	offsets []int
	hashes  []uint64
}

const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

// of 返回键本身与逐字符删除一个字符后的全部变体哈希。
// 下标按 rune 计算，与 editDistanceLE1 的口径一致（含非法 UTF-8 时按单字节处理）。
func (vh *variantHasher) of(k string) []uint64 {
	vh.offsets = vh.offsets[:0]
	for i := range k { // range 字符串给出每个 rune 的起始字节下标
		vh.offsets = append(vh.offsets, i)
	}
	vh.offsets = append(vh.offsets, len(k))
	vh.hashes = vh.hashes[:0]
	// 变体 0 是键本身，保持与原实现一致（同长度只差一个字符的命中靠它）。
	vh.hashes = append(vh.hashes, fnv64(k))
	for i := 0; i+1 < len(vh.offsets); i++ {
		vh.hashes = append(vh.hashes, fnv64Skip(k, vh.offsets[i], vh.offsets[i+1]))
	}
	return vh.hashes
}

func fnv64(s string) uint64 {
	h := uint64(fnvOffset64)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * fnvPrime64
	}
	return h
}

// fnv64Skip 计算跳过 s[from:to] 后的哈希，不生成中间字符串。
func fnv64Skip(s string, from, to int) uint64 {
	h := uint64(fnvOffset64)
	for i := 0; i < from; i++ {
		h = (h ^ uint64(s[i])) * fnvPrime64
	}
	for i := to; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * fnvPrime64
	}
	return h
}

// NearGroups 把键相同或编辑距离 ≤1 的索引并查集分组。
// 算法：删字签名桶（编辑距离 ≤1 的两串必共享一个删字变体）预过滤，
// 命中后用 O(n) 真实距离校验，杜绝误并。整体近似 O(n·L)（L=键长），
// 18 万文件规模可接受，无 O(n²) 配对。
// 返回只含 ≥2 个成员的组（组内为原始下标）。
func NearGroups(keys []string) [][]int {
	n := len(keys)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	exact := make(map[string]int, n)
	sig := make(map[uint64]int, n*8)
	var vh variantHasher
	for i, k := range keys {
		if k == "" {
			continue
		}
		if j, ok := exact[k]; ok {
			union(i, j)
		} else {
			exact[k] = i
		}
		// 删字签名桶：共享签名只说明距离 ≤2，必须真实校验
		for _, h := range vh.of(k) {
			if j, ok := sig[h]; ok && j != i {
				if editDistanceLE1(k, keys[j]) {
					union(i, j)
				}
			} else if !ok {
				sig[h] = i
			}
		}
	}
	groups := map[int][]int{}
	for i := 0; i < n; i++ {
		if keys[i] == "" {
			continue
		}
		r := find(i)
		groups[r] = append(groups[r], i)
	}
	out := make([][]int, 0, len(groups))
	for _, g := range groups {
		if len(g) >= 2 {
			out = append(out, g)
		}
	}
	return out
}
