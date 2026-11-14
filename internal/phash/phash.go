// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package phash 提供感知哈希（dHash）的基础运算，供视频与图片指纹共用。
// 图片方向哈希、旋转与镜像的矩阵级处理见 internal/photo。
package phash

import "math"

// DHash 从 9x8 灰度图计算差异哈希（64 位）。
// 行主序 gray[r*9+c]，比较相邻列亮度得 64 位水平梯度。
func DHash(gray []byte) uint64 {
	if len(gray) < 9*8 {
		return 0
	}
	var h uint64
	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			if gray[r*9+c] > gray[r*9+c+1] {
				h |= 1 << uint(r*8+c)
			}
		}
	}
	return h
}

// Hamming 返回两个 64 位哈希的汉明距离（不同比特数）。
func Hamming(a, b uint64) int {
	d := 0
	v := a ^ b
	for v != 0 {
		v &= v - 1
		d++
	}
	return d
}

// MirrorBits 逐行反转 8x8 dHash 的比特序，得到水平镜像后的哈希。
// 全程在 uint64 上做位运算，不经过 byte 截断，避免整数溢出转换。
func MirrorBits(v uint64) uint64 {
	var out uint64
	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			if v&(uint64(1)<<uint(r*8+c)) != 0 {
				out |= 1 << uint(r*8+7-c)
			}
		}
	}
	return out
}

// toSigned 把指纹字节按 int8 语义还原为有符号数值，避免 byte -> int8 的溢出转换。
func toSigned(v byte) float64 {
	if v >= 128 {
		return float64(int(v) - 256)
	}
	return float64(int(v))
}

// Cosine 计算两个 int8 指纹（byte 承载）的余弦相似度。
func Cosine(a, b []byte) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := 0; i < len(a); i++ {
		x := toSigned(a[i])
		y := toSigned(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
