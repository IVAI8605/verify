// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package photo 为静态图片计算 4 方向 dHash 感知指纹，供相似图片查重使用。
//
// 安全模型（x/image 官方建议）：先 DecodeConfig 校验尺寸再 Decode，
// 防止超大声明尺寸的图片在解码阶段吃掉数 GB 内存。
// 格式按文件内容自动识别（标准库与 x/image 的 init 注册），
// 不信任扩展名；无法解码的格式（HEIC/AVIF 等）返回错误由调用方记 img_error。
package photo

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg" // 注册 JPEG 解码器
	_ "image/png"  // 注册 PNG 解码器
	"io"
	"math/bits"
	"os"

	_ "golang.org/x/image/bmp"  // 注册 BMP 解码器
	_ "golang.org/x/image/tiff" // 注册 TIFF 解码器
	_ "golang.org/x/image/webp" // 注册 WebP（静态）解码器

	"golang.org/x/image/draw"

	"verify/internal/phash"
)

// MaxPixels 是单张图片参与指纹计算的最大像素数（约 10000×10000）。
// 超过即视为解码炸弹风险，跳过并记 img_error。
const MaxPixels = 100_000_000

// grayW/grayH 是灰度采样尺寸：dHash 输入 9×8，为支持 4 方向旋转多采样一行/列到 9×9。
const (
	grayW = 9
	grayH = 9
)

// FP 是一张图片的指纹：4 个方向（0°/90°/180°/270°）的 dHash 与原始尺寸。
type FP struct {
	Hashes [4]uint64
	W      int
	H      int
}

// Distance 计算两张图 4 方向指纹的最小汉明距离。
// 比对同时考虑对方的水平镜像形态（位运算现场变换，不占存储），
// 因此「原图 vs 水平翻转」与「原图 vs 旋转」都能得到小距离。
func Distance(a, b [4]uint64) int {
	md := 64
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			if d := phash.Hamming(a[i], b[j]); d < md {
				md = d
			}
			if d := phash.Hamming(a[i], phash.MirrorBits(b[j])); d < md {
				md = d
			}
		}
	}
	return md
}

// ErrLowInfo 表示图片信息量过低，dHash 塌缩为全 0/全 1 常量，
// 与任何其他塌缩图的距离都≈0——把它们判相似会混入「构图完全不同」的图，
// 造成误删风险（审查 C-2）。此类图片不参与相似聚类，由调用方记 img_error。
// 注意判定对象是**哈希熵**而非灰度级数：棋盘格只有 2 个灰度级但梯度丰富，
// dHash 正常；纯色图灰度级少且无梯度，4 方向哈希全为常量。
var ErrLowInfo = errors.New("低信息量图片（纯色或近纯色），不参与相似比对")

// Fingerprint 计算图片指纹。调用方负责控制并发（解码大图内存开销显著）。
func Fingerprint(path string) (FP, error) {
	var fp FP
	f, err := os.Open(path) // #nosec G304 -- 路径来自扫描索引而非用户请求
	if err != nil {
		return fp, err
	}
	defer func() { _ = f.Close() }()

	// 先 DecodeConfig 拿真实尺寸，超限直接拒绝，避免 Decode 吃内存。
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return fp, fmt.Errorf("无法识别的图片格式: %w", err)
	}
	fp.W, fp.H = cfg.Width, cfg.Height
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return fp, fmt.Errorf("非法图片尺寸 %dx%d", cfg.Width, cfg.Height)
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return fp, fmt.Errorf("图片尺寸 %dx%d 超过 %d 像素上限，跳过", cfg.Width, cfg.Height, MaxPixels)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fp, err
	}

	img, err := decodeAnimationSafe(f)
	if err != nil {
		return fp, err
	}
	if img == nil {
		return fp, fmt.Errorf("动画图片不支持指纹计算")
	}

	gray := toGray(img, grayW, grayH)
	fp.Hashes = fourWayHashes(gray)
	// 信息量守卫（审查 C-2）：4 方向哈希全部塌缩（全 0 或全 1）的图无区分能力
	if lowInformationHashes(fp.Hashes) {
		return fp, ErrLowInfo
	}
	return fp, nil
}

// lowInformationHashes 判断 4 方向哈希是否全部塌缩：
// 每个方向哈希的 popcount 都为 0 或 64（无任何梯度信息）。
func lowInformationHashes(hs [4]uint64) bool {
	for _, h := range hs {
		c := bits.OnesCount64(h)
		if c != 0 && c != 64 {
			return false
		}
	}
	return true
}

// decodeAnimationSafe 解码图片；动画 GIF 返回 (nil, nil) 由调用方按不支持处理。
// 动画 WebP 在 x/image/webp 直接报错，同样归入不支持。
func decodeAnimationSafe(f *os.File) (image.Image, error) {
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	// GIF 需要显式判帧数：Decode 只取首帧，动画图首帧与大量截图误判相似，整类跳过。
	if _, format, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && format == "gif" {
		g, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("无法解码: %w", err)
		}
		if len(g.Image) > 1 {
			return nil, nil
		}
		return g.Image[0], nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// 动画 WebP 等解码器直接报错；区分不出来源，统一归为不支持。
		return nil, fmt.Errorf("无法解码: %w", err)
	}
	return img, nil
}

// toGray 把任意图像缩放为 w×h 的 8 位灰度阵（行主序）。
// 使用 ApproxBiLinear：感知哈希对精度不敏感，速度优先。
func toGray(img image.Image, w, h int) []byte {
	dst := image.NewGray(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	return dst.Pix[:w*h]
}

// fourWayHashes 从 9×9 灰度阵计算 0°/90°/180°/270° 四个方向的 dHash。
// 全部在 9×9 小矩阵上做转置/翻转，零额外解码与缩放开销。
func fourWayHashes(gray []byte) [4]uint64 {
	// d0 用左上 9×8 子阵（标准 dHash 输入）。
	g0 := make([]byte, 9*8)
	for r := 0; r < 8; r++ {
		copy(g0[r*9:(r+1)*9], gray[r*grayW:(r+1)*grayW])
	}
	// 90°：转置阵（行主序 grayT[c*9+r] = gray[r*9+c]）。
	g90 := make([]byte, 9*9)
	for r := 0; r < 9; r++ {
		for c := 0; c < 9; c++ {
			g90[c*9+r] = gray[r*9+c]
		}
	}
	// 180°：中心对称。
	g180 := make([]byte, 9*9)
	for r := 0; r < 9; r++ {
		for c := 0; c < 9; c++ {
			g180[r*9+c] = gray[(8-r)*9+(8-c)]
		}
	}
	// 270°：转置 + 中心对称。
	g270 := make([]byte, 9*9)
	for r := 0; r < 9; r++ {
		for c := 0; c < 9; c++ {
			g270[r*9+c] = g90[(8-r)*9+(8-c)]
		}
	}
	return [4]uint64{
		phash.DHash(g0),
		phash.DHash(g90[:9*8]),
		phash.DHash(g180[:9*8]),
		phash.DHash(g270[:9*8]),
	}
}
