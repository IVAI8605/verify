// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package video

import (
	"math"
)

const (
	// Frames 对应音频开头约 5 秒（8kHz 采样、2048 点/帧）
	Frames   = 20
	Bands    = 32
	Segments = 8
	// Dim 为静态频谱（32）+ 分段动态频谱（32×8）的总维度
	Dim    = Bands + Bands*Segments
	frameN = 2048
)

// AudioFingerprint 计算音频开头 5 秒的频谱指纹：
// 32 个对数频带的静态频谱轮廓（权重 0.75）+ 8 段动态频谱（权重 0.25），int8 量化。
// 该组合对二次编码较鲁棒，同时保留一定的时序区分能力。
// samples 为 8kHz 单声道 PCM。返回 nil 表示音频过短或基本为静音。
func AudioFingerprint(samples []int16, sampleRate int) []byte {
	if sampleRate <= 0 || len(samples) < frameN*2 {
		return nil
	}
	n := len(samples)
	hop := 1
	if n > frameN {
		hop = (n - frameN) / (Frames - 1)
		if hop < 1 {
			hop = 1
		}
	}

	// 频带边界（Hz -> bin）；用 append 逐个追加，避开对同一切片的往回索引。
	edges := make([]int, 0, Bands+1)
	lo, hi := 80.0, 3600.0
	if float64(sampleRate)/2 < hi {
		hi = float64(sampleRate)/2 - 50
	}
	prev := 0
	for i := 0; i <= Bands; i++ {
		f := lo * math.Pow(hi/lo, float64(i)/float64(Bands))
		b := int(math.Round(f * float64(frameN) / float64(sampleRate)))
		if b < 1 {
			b = 1
		}
		if b > frameN/2 {
			b = frameN / 2
		}
		// 保证边界严格递增，否则某些频带宽度会退化到 0
		if i > 0 && b <= prev {
			b = prev + 1
		}
		edges = append(edges, b)
		prev = b
	}

	// mat[b*Frames+f] 为第 b 个频带、第 f 帧的能量（dB）
	mat := make([]float64, Bands*Frames)
	re := make([]float64, frameN)
	im := make([]float64, frameN)
	win := make([]float64, frameN)
	for i := 0; i < frameN; i++ {
		win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(frameN-1))
	}

	for f := 0; f < Frames; f++ {
		off := f * hop
		if off+frameN > n {
			off = n - frameN
		}
		for i := 0; i < frameN; i++ {
			re[i] = float64(samples[off+i]) * win[i]
			im[i] = 0
		}
		fft(re, im)
		for b := 0; b < Bands; b++ {
			sum := 0.0
			for k := edges[b]; k < edges[b+1]; k++ {
				sum += math.Hypot(re[k], im[k])
			}
			mat[b*Frames+f] = 20 * math.Log10(sum+1e-6)
		}
	}

	// 消除每帧整体响度差异
	for f := 0; f < Frames; f++ {
		var mean float64
		for b := 0; b < Bands; b++ {
			mean += mat[b*Frames+f]
		}
		mean /= float64(Bands)
		for b := 0; b < Bands; b++ {
			mat[b*Frames+f] -= mean
		}
	}

	// 静态频谱轮廓
	static := make([]float64, Bands)
	for b := 0; b < Bands; b++ {
		var sum float64
		for f := 0; f < Frames; f++ {
			sum += mat[b*Frames+f]
		}
		static[b] = sum / float64(Frames)
	}
	var smean float64
	for b := 0; b < Bands; b++ {
		smean += static[b]
	}
	smean /= float64(Bands)
	for b := 0; b < Bands; b++ {
		static[b] -= smean
	}
	staticNorm := norm(static)

	// 动态（分段）频谱
	dyn := make([]float64, Bands*Segments)
	for b := 0; b < Bands; b++ {
		var bmean float64
		for s := 0; s < Segments; s++ {
			start := s * Frames / Segments
			end := (s + 1) * Frames / Segments
			var sum float64
			for k := start; k < end; k++ {
				sum += mat[b*Frames+k]
			}
			v := sum / float64(end-start)
			dyn[b*Segments+s] = v
			bmean += v
		}
		bmean /= float64(Segments)
		for s := 0; s < Segments; s++ {
			dyn[b*Segments+s] -= bmean
		}
	}
	dynNorm := norm(dyn)

	if staticNorm == 0 && dynNorm == 0 {
		return nil
	}

	out := make([]byte, Dim)
	// 静态部分权重 0.75，动态部分权重 0.25
	sw, dw := 0.0, 0.0
	if staticNorm > 0 {
		sw = math.Sqrt(0.75)
	}
	if dynNorm > 0 {
		dw = math.Sqrt(0.25)
	}
	total := math.Sqrt(sw*sw*staticNorm*staticNorm + dw*dw*dynNorm*dynNorm)
	for b := 0; b < Bands; b++ {
		v := 0.0
		if staticNorm > 0 {
			v += sw * static[b] / staticNorm
		}
		out[b] = quant(v / total)
	}
	for i, v := range dyn {
		q := 0.0
		if dynNorm > 0 {
			q = dw * v / dynNorm / total
		}
		// dyn 的长度由 Segments 决定，这里显式兜底，防止常量关系被改动后越界写。
		if idx := Bands + i; idx < len(out) {
			out[idx] = quant(q)
		}
	}
	return out
}

func norm(v []float64) float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	return math.Sqrt(sum)
}

// quant 把 [-1,1] 区间的分量量化成 int8 语义的字节。
// 指纹按 int8 解释，负数用补码表示；先取整到 -127..127 再按 8 位截断，
// 避免经 int8 中间类型转换时触发溢出告警。
func quant(v float64) byte {
	q := math.Round(v * 127)
	if q > 127 {
		q = 127
	}
	if q < -127 {
		q = -127
	}
	return byte(int(q) & 0xff)
}

func fft(re, im []float64) {
	n := len(re)
	j := 0
	for i := 1; i < n; i++ {
		bit := n >> 1
		for j&bit != 0 {
			j ^= bit
			bit >>= 1
		}
		j |= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		ang := -2 * math.Pi / float64(length)
		wpr, wpi := math.Cos(ang), math.Sin(ang)
		for i := 0; i < n; i += length {
			wr, wi := 1.0, 0.0
			for k := 0; k < length/2; k++ {
				ur := re[i+k]
				ui := im[i+k]
				vr := re[i+k+length/2]*wr - im[i+k+length/2]*wi
				vi := re[i+k+length/2]*wi + im[i+k+length/2]*wr
				re[i+k] = ur + vr
				im[i+k] = ui + vi
				re[i+k+length/2] = ur - vr
				im[i+k+length/2] = ui - vi
				nwr := wr*wpr - wi*wpi
				wi = wr*wpi + wi*wpr
				wr = nwr
			}
		}
	}
}
