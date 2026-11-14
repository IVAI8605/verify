// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"archive/zip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"image"
	"io"
	"os"
	"path/filepath"

	_ "golang.org/x/image/bmp"  // 注册 BMP 解码器（损坏检测完整解码用）
	_ "golang.org/x/image/tiff" // 注册 TIFF 解码器
	_ "golang.org/x/image/webp" // 注册 WebP（静态）解码器

	_ "image/gif"  // 注册 GIF 解码器
	_ "image/jpeg" // 注册 JPEG 解码器
	_ "image/png"  // 注册 PNG 解码器

	"verify/internal/photo"
)

// 损坏文件扩展检测（v1.14.0 引入 10.3）：在原「损坏视频」基础上把候选集扩到
// 音频 / 图片 / zip 系 / JSON / XML，按扩展名分流检测方法。
//
// 原则：所有新增检测**只报告、不推荐删除**——图片与文本格式误报率高于视频，
// 判断权留给用户。超限文件记 ReasonOversized（过大未检），不判损坏。

// 大文件检测上限：JSON 全量校验 10MB、XML 流式 50MB、图片像素上限复用 photo 包常量。
const (
	jsonMaxBytes = 10 << 20
	xmlMaxBytes  = 50 << 20
)

// 损坏原因：新增子类。
const (
	// ReasonOversized 文件超过检测上限，跳过不判（避免吃内存/拖慢扫描）。
	ReasonOversized = "oversized"
	// ReasonImageBroken 图片无法解码（含截断、非图片内容）。
	ReasonImageBroken = "image_broken"
	// ReasonZipBroken zip 系容器中央目录损坏。
	ReasonZipBroken = "zip_broken"
	// ReasonJSONBroken JSON 语法错误。
	ReasonJSONBroken = "json_broken"
	// ReasonXMLBroken XML / SVG 结构错误。
	ReasonXMLBroken = "xml_broken"
)

// brokenAudioExts 是损坏检测的音频候选（与查重的 AudioExts 一致）。
var brokenAudioExts = map[string]bool{
	"mp3": true, "flac": true, "m4a": true, "aac": true,
	"ogg": true, "wav": true, "wma": true, "ape": true,
}

// brokenImageExts 是损坏检测的图片候选。
var brokenImageExts = map[string]bool{
	"jpg": true, "jpeg": true, "jpe": true, "png": true, "gif": true,
	"bmp": true, "dib": true, "tif": true, "tiff": true, "webp": true,
}

// brokenZipExts 是 zip 系容器候选（加密包能读目录即通过，不解密内容）。
var brokenZipExts = map[string]bool{
	"zip": true, "docx": true, "xlsx": true, "pptx": true,
	"epub": true, "jar": true, "apk": true, "odt": true,
}

// HasBrokenExt 判断文件是否属于「损坏文件」检测范围（视频 ∪ 音频 ∪ 图片 ∪ zip ∪ json ∪ xml）。
func HasBrokenExt(name string) bool {
	ext := lowerExt(name)
	return HasVideoExt(name) || brokenAudioExts[ext] || brokenImageExts[ext] ||
		brokenZipExts[ext] || ext == "json" || ext == "xml" || ext == "svg"
}

// ProbeBroken 按扩展名分流检测文件是否损坏。
// 返回 nil 表示正常（或无法判定——无 ffmpeg 且非纯 Go 可检类型）。
// v3.9.11：音视频探测多了 ffprobe 复核，用于区分"局部坏帧/能播放"与"真正损坏"。
func ProbeBroken(ctx context.Context, ffmpeg, ffprobe, path string, run CmdRunner) *BrokenFinding {
	ext := lowerExt(filepath.Base(path))
	switch {
	case HasVideoExt(path) || brokenAudioExts[ext]:
		// 音频与视频同用 ffmpeg 快速解码探测（现有实现，无新代码路径）
		return ProbeVideo(ctx, ffmpeg, ffprobe, path, run)

	case brokenImageExts[ext]:
		return probeImage(path)

	case brokenZipExts[ext]:
		return probeZip(path)

	case ext == "json":
		return probeJSON(path)

	case ext == "xml" || ext == "svg":
		return probeXML(path)
	}
	return nil
}

// probeImage 两步检测：DecodeConfig 先行防解压炸弹（超限记过大未检），再完整解码。
func probeImage(path string) *BrokenFinding {
	f, err := os.Open(path) // #nosec G304 -- 路径来自扫描根目录内的遍历结果
	if err != nil {
		return &BrokenFinding{Reason: ReasonImageBroken, Detail: "无法打开：" + err.Error()}
	}
	defer func() { _ = f.Close() }()

	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return &BrokenFinding{Reason: ReasonImageBroken, Detail: "头部无法解析（" + err.Error() + "）"}
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return &BrokenFinding{Reason: ReasonImageBroken, Detail: "非法尺寸"}
	}
	if int64(cfg.Width)*int64(cfg.Height) > photo.MaxPixels {
		return &BrokenFinding{Reason: ReasonOversized, Detail: "图片尺寸过大，未检测（" + format + "）"}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return &BrokenFinding{Reason: ReasonOversized, Detail: "文件读取受限，未检测"}
	}
	// 完整解码：绝大多数截断/损坏在这一步暴露
	if _, _, err := image.Decode(f); err != nil {
		return &BrokenFinding{Reason: ReasonImageBroken, Detail: "解码失败（" + err.Error() + "）"}
	}
	return nil
}

// probeZip 读 zip 中央目录：能列出即通过；加密包不解密内容，目录可读就算完好。
func probeZip(path string) *BrokenFinding {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return &BrokenFinding{Reason: ReasonZipBroken, Detail: "中央目录损坏（" + err.Error() + "）"}
	}
	defer func() { _ = zr.Close() }()
	return nil
}

// probeJSON 全量校验；超过 10MB 记过大未检（json.Valid 需要整文件进内存）。
func probeJSON(path string) *BrokenFinding {
	info, err := os.Stat(path)
	if err != nil {
		return &BrokenFinding{Reason: ReasonJSONBroken, Detail: "无法读取：" + err.Error()}
	}
	if info.Size() > jsonMaxBytes {
		return &BrokenFinding{Reason: ReasonOversized, Detail: "JSON 超过 10MB，未检测"}
	}
	data, err := os.ReadFile(path) // #nosec G304 -- 路径来自扫描根目录内的遍历结果，且已限 10MB
	if err != nil {
		return &BrokenFinding{Reason: ReasonJSONBroken, Detail: "无法读取：" + err.Error()}
	}
	if !json.Valid(data) {
		return &BrokenFinding{Reason: ReasonJSONBroken, Detail: "JSON 语法错误"}
	}
	return nil
}

// probeXML 流式解析到 EOF；超过 50MB 跳过（保持内存有界）。
func probeXML(path string) *BrokenFinding {
	f, err := os.Open(path) // #nosec G304 -- 路径来自扫描根目录内的遍历结果
	if err != nil {
		return &BrokenFinding{Reason: ReasonXMLBroken, Detail: "无法打开：" + err.Error()}
	}
	defer func() { _ = f.Close() }()

	dec := xml.NewDecoder(io.LimitReader(f, xmlMaxBytes))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			// LimitReader 截断时 Decode 返回 unexpected EOF——区分大小即可
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return &BrokenFinding{Reason: ReasonOversized, Detail: "XML 超过 50MB，未检测"}
			}
			return &BrokenFinding{Reason: ReasonXMLBroken, Detail: "XML 结构错误（" + summarize(err.Error()) + "）"}
		}
		_ = tok // 消费全部 token：能解到 EOF 即结构正确
	}
}
