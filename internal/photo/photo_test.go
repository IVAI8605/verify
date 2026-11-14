// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package photo

import (
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/bmp"
)

// makeTestImage 画一张确定性的测试图（渐变+形状），base 决定图案。
func makeTestImage(w, h int, base int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(int64(base)))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{
				R: uint8((x*255/maxInt(w, 1) + base) % 256),
				G: uint8((y*255/maxInt(h, 1) + base*7) % 256),
				B: uint8(rng.Intn(85) + (x+y)%170),
				A: 255,
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func save(t *testing.T, dir, name string, img image.Image, encode func(*os.File, image.Image) error) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

func encJPEG(f *os.File, img image.Image) error {
	return jpeg.Encode(f, img, &jpeg.Options{Quality: 95})
}
func encJPEG60(f *os.File, img image.Image) error {
	return jpeg.Encode(f, img, &jpeg.Options{Quality: 60})
}
func encPNG(f *os.File, img image.Image) error { return png.Encode(f, img) }
func encGIF(f *os.File, img image.Image) error { return gif.Encode(f, img, nil) }
func encBMP(f *os.File, img image.Image) error { return bmp.Encode(f, img) }

func mustFP(t *testing.T, p string) FP {
	t.Helper()
	fp, err := Fingerprint(p)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return fp
}

// distance 计算两组 4 方向哈希的最小汉明距离（含各自镜像，与 PhotoGroups 比对逻辑一致）。
func distance(a, b FP) int {
	md := 64
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			if d := hamming(a.Hashes[i], b.Hashes[j]); d < md {
				md = d
			}
			if d := hamming(a.Hashes[i], mirror(b.Hashes[j])); d < md {
				md = d
			}
		}
	}
	return md
}

// 直接在测试内实现，避免依赖 store 层
func hamming(a, b uint64) int {
	d, v := 0, a^b
	for v != 0 {
		v &= v - 1
		d++
	}
	return d
}

func mirror(v uint64) uint64 {
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

// TestFourWayInvariance 验收核心：原图/旋转/翻转/缩放/重压缩必须判相似。
func TestFourWayInvariance(t *testing.T) {
	dir := t.TempDir()
	base := makeTestImage(800, 600, 3)

	pOrig := save(t, dir, "orig.jpg", base, encJPEG)

	// 旋转 90°：宽高互换（内存旋转再编码）
	rot90 := image.NewRGBA(image.Rect(0, 0, 600, 800))
	for y := 0; y < 800; y++ {
		for x := 0; x < 600; x++ {
			rot90.Set(x, y, base.At(800-1-y, x))
		}
	}
	pRot90 := save(t, dir, "rot90.jpg", rot90, encJPEG)

	// 水平翻转
	flip := image.NewRGBA(base.Bounds())
	for y := 0; y < 600; y++ {
		for x := 0; x < 800; x++ {
			flip.Set(x, y, base.At(800-1-x, y))
		}
	}
	pFlip := save(t, dir, "flip.png", flip, encPNG)

	// 缩 50%
	small := makeTestImage(400, 300, 3)
	pSmall := save(t, dir, "small.jpg", small, encJPEG)

	// 重压缩（质量 60）
	pQ60 := save(t, dir, "q60.jpg", base, encJPEG60)

	fpOrig := mustFP(t, pOrig)
	for name, p := range map[string]string{"rot90": pRot90, "flip": pFlip, "small": pSmall, "q60": pQ60} {
		fp := mustFP(t, p)
		if d := distance(fpOrig, fp); d > 10 {
			t.Fatalf("%s 应与原图相似（距离 %d > 10）", name, d)
		}
	}
}

// TestDifferentImagesDiffer 构图不同的图必须不同组。
func TestDifferentImagesDiffer(t *testing.T) {
	dir := t.TempDir()
	// A：平滑渐变；B：棋盘格——构图完全不同
	a := makeTestImage(500, 500, 10)
	b := image.NewRGBA(image.Rect(0, 0, 500, 500))
	for y := 0; y < 500; y++ {
		for x := 0; x < 500; x++ {
			if (x/50+y/50)%2 == 0 {
				b.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			} else {
				b.Set(x, y, color.RGBA{A: 255})
			}
		}
	}
	pA := save(t, dir, "a.jpg", a, encJPEG)
	pB := save(t, dir, "b.jpg", b, encJPEG)
	d := distance(mustFP(t, pA), mustFP(t, pB))
	if d <= 10 {
		t.Fatalf("不同构图的图距离 %d 应 > 10", d)
	}
}

// TestFormatSupport 支持的格式表（jpg/png/gif/bmp 解码成功）。
func TestFormatSupport(t *testing.T) {
	dir := t.TempDir()
	img := makeTestImage(64, 64, 1)
	for name, enc := range map[string]func(*os.File, image.Image) error{
		"a.jpg": encJPEG, "a.png": encPNG, "a.gif": encGIF, "a.bmp": encBMP,
	} {
		p := save(t, dir, name, img, enc)
		if _, err := Fingerprint(p); err != nil && !errors.Is(err, ErrLowInfo) {
			// 格式测试关心「能解码」；小测试图可能触发低信息量守卫（审查 C-2），不算失败
			t.Fatalf("%s 应支持: %v", name, err)
		}
	}
	// 非图片文件报错（HEIC 等无解码器的格式同路径）
	p := filepath.Join(dir, "fake.heic")
	if err := os.WriteFile(p, []byte("\x00\x00\x00\x18ftypheic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Fingerprint(p); err == nil {
		t.Fatal("HEIC 应返回错误（不支持）")
	}
}

// TestAnimatedGifSkipped 动画 GIF 跳过（返回错误），静态 GIF 正常。
func TestAnimatedGifSkipped(t *testing.T) {
	dir := t.TempDir()
	anim := &gif.GIF{}
	for i := 0; i < 3; i++ {
		anim.Image = append(anim.Image, image.NewPaletted(image.Rect(0, 0, 32, 32), color.Palette{color.White, color.Black}))
		anim.Delay = append(anim.Delay, 10)
	}
	p := filepath.Join(dir, "anim.gif")
	f, _ := os.Create(p)
	if err := gif.EncodeAll(f, anim); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := Fingerprint(p); err == nil {
		t.Fatal("动画 GIF 应跳过并报错")
	}
}

// TestOversizeSkipped 超大声明尺寸的图片在 DecodeConfig 阶段被拒。
func TestOversizeSkipped(t *testing.T) {
	// 构造 BMP：头里声明超大尺寸但数据截断——DecodeConfig 只读头，能触发 MaxPixels 拒绝。
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 12000, 12000)) // 1.44 亿像素 > 1 亿
	p := save(t, dir, "huge.bmp", img, encBMP)
	if _, err := Fingerprint(p); err == nil {
		t.Fatal("超过像素上限的图片应报错跳过")
	}
}

// TestLowInfoGuard 审查 C-2：两张不同颜色的纯色图必须都拒绝指纹（不误判相似），
// 有梯度的正常图不受影响，棋盘格（少灰度级但梯度丰富）不受影响。
func TestLowInfoGuard(t *testing.T) {
	dir := t.TempDir()
	solid := func(c color.RGBA) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, 100, 100))
		for y := 0; y < 100; y++ {
			for x := 0; x < 100; x++ {
				img.Set(x, y, c)
			}
		}
		return img
	}
	pRed := save(t, dir, "red.jpg", solid(color.RGBA{R: 200, G: 40, B: 40, A: 255}), encJPEG)
	pBlue := save(t, dir, "blue.jpg", solid(color.RGBA{R: 40, G: 40, B: 200, A: 255}), encJPEG)
	for _, p := range []string{pRed, pBlue} {
		if _, err := Fingerprint(p); !errors.Is(err, ErrLowInfo) {
			t.Fatalf("纯色图应报 ErrLowInfo，实际 %v", err)
		}
	}
	// 棋盘格：灰度级少但梯度丰富，dHash 正常不塌缩
	checker := image.NewRGBA(image.Rect(0, 0, 160, 160))
	for y := 0; y < 160; y++ {
		for x := 0; x < 160; x++ {
			if (x/20+y/20)%2 == 0 {
				checker.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			} else {
				checker.Set(x, y, color.RGBA{A: 255})
			}
		}
	}
	pCk := save(t, dir, "ck.jpg", checker, encJPEG)
	if _, err := Fingerprint(pCk); err != nil {
		t.Fatalf("棋盘格不应被低信息量守卫拦截: %v", err)
	}
}
