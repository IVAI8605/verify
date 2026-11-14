// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode"
	"unicode/utf8"
)

// ContentMode 是扩展模式的检查方式。
type ContentMode int

const (
	// ContentNone 只查零字节，不读内容。
	ContentNone ContentMode = iota
	// ContentZeroed 额外查找"大小非零但内容全为 0x00"的文件。
	ContentZeroed
	// ContentUnprintable 额外查找"内容全为不可打印字符"的文件。
	ContentUnprintable
)

// chunkSize 是内容检查的分块大小；一旦发现反例立即返回，不等读完整个文件。
const chunkSize = 1 << 20

// EmptyFinding 是一个空文件的判定结论。
type EmptyFinding struct {
	// Size 是文件大小，基础模式下恒为 0。
	Size int64
	// Reason 为空表示不是空文件。
	Reason string
}

// 空文件的判定原因。
const (
	// ReasonZeroSize 文件大小为 0。
	ReasonZeroSize = "zero_size"
	// ReasonAllZero 大小非零但内容全为 0x00。
	ReasonAllZero = "all_zero"
	// ReasonUnprintable 大小非零但内容全为不可打印字符。
	ReasonUnprintable = "unprintable"
)

// unprintableExemptExts 是文本类扩展名：内容天然以可打印字符为主，
// 不参与「全不可打印」检测。v3.17.5（F-11）：纯中文 txt 与编码异常的
// 字幕/日志此前会被整批判成空文件，健康页可以把它们一起移入隔离区。
var unprintableExemptExts = map[string]bool{
	"txt": true, "srt": true, "ass": true, "ssa": true, "vtt": true, "nfo": true,
	"log": true, "json": true, "xml": true, "csv": true, "md": true, "yaml": true, "yml": true,
}

// CheckEmpty 依据大小与（可选的）内容判定文件是否属于空文件类问题。
//
// 基础模式只看 fileInfo.Size()，零 I/O；扩展模式才读内容，
// 且读取是分块短路式的，一旦发现有效内容立刻放弃该文件。
func CheckEmpty(path string, size int64, mode ContentMode) (EmptyFinding, bool) {
	if size == 0 {
		return EmptyFinding{Size: 0, Reason: ReasonZeroSize}, true
	}
	if mode == ContentNone {
		return EmptyFinding{}, false
	}
	if mode == ContentUnprintable && unprintableExemptExts[lowerExt(filepath.Base(path))] {
		return EmptyFinding{}, false
	}
	ok, err := scanContent(path, mode)
	if err != nil || !ok {
		return EmptyFinding{}, false
	}
	reason := ReasonAllZero
	if mode == ContentUnprintable {
		reason = ReasonUnprintable
	}
	return EmptyFinding{Size: size, Reason: reason}, true
}

// scanContent 分块读取文件，判断是否满足指定的内容条件。
func scanContent(path string, mode ContentMode) (bool, error) {
	f, err := os.Open(path) // #nosec G304 -- 路径来自扫描根目录内的遍历结果
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	// v3.17.5（F-11）：不可打印检测按 rune 走，不能按字节——UTF-8 中文的
	// 每个字节都 >126，按字节判会把纯中文文本整批判成「全不可打印」。
	if mode == ContentUnprintable {
		return scanUnprintable(f)
	}
	buf := make([]byte, chunkSize)
	for {
		n, err := f.Read(buf)
		if n > 0 && !chunkMatches(buf[:n], mode) {
			return false, nil
		}
		if errors.Is(err, io.EOF) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
	}
}

// scanUnprintable 判断内容是否全为不可打印字符（按 rune 判定，遇可打印字符立即返回）。
func scanUnprintable(f *os.File) (bool, error) {
	br := bufio.NewReaderSize(f, chunkSize)
	for {
		r, size, err := br.ReadRune()
		if errors.Is(err, io.EOF) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		// 无效 UTF-8 字节会被解成 U+FFFD，而 U+FFFD 本身是可打印符号；
		// 这里按「size==1 + RuneError」识别原始坏字节，归入不可打印。
		if r == utf8.RuneError && size == 1 {
			continue
		}
		if unicode.IsPrint(r) || r == '\n' || r == '\r' || r == '\t' || r == '\f' || r == '\v' {
			return false, nil
		}
	}
}

// chunkMatches 判断一个分块是否全部满足条件。
func chunkMatches(b []byte, mode ContentMode) bool {
	switch mode {
	case ContentZeroed:
		for _, c := range b {
			if c != 0 {
				return false
			}
		}
		return true
	}
	return false
}

// HasVideoExt 判断扩展名是否属于需要 FFmpeg 探测的视频格式。
// 与 store 里的视频白名单保持同源思路，但这里只关心能否解码。
func HasVideoExt(name string) bool {
	ext := lowerExt(name)
	switch ext {
	case "mp4", "mkv", "avi", "mov", "wmv", "flv", "m4v", "ts", "mts", "m2ts",
		"mpg", "mpeg", "rmvb", "rm", "3gp", "webm", "vob", "ogv", "asf", "f4v", "divx", "m2v", "mxf", "dat":
		return true
	}
	return false
}

func lowerExt(name string) string {
	e := filepath.Ext(name)
	if len(e) < 2 {
		return ""
	}
	return toLowerASCII(e[1:])
}

// toLowerASCII 只处理 ASCII 字母，避免为一点小事引入 unicode 依赖分支。
func toLowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
