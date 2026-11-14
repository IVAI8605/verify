// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package hashx 提供精确查重使用的预哈希与 BLAKE3 完整哈希。
package hashx

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"

	"github.com/klauspost/cpuid/v2"
	"lukechampine.com/blake3"
)

// AlgoVersion 同时标识预哈希抽样规则与完整哈希算法。
// 任何一处变化都会让旧缓存失去可比性，升级后必须清空重算，否则会出现“本该重复却不成组”的漏报。
const AlgoVersion = "blake3-q4k-v1"

// DisplayName 用于设置页与报告展示。
const DisplayName = "BLAKE3-256"

// QuickChunk 是预哈希头尾各取的字节数。4KB 已能排除绝大多数不同文件，
// 同时把大文件的预哈希读盘量压到最低。
const QuickChunk = 4 * 1024

// QuickHash 对“文件大小 + 首尾各 4KB”求 BLAKE3，用于廉价地筛掉明显不同的文件。
func QuickHash(path string, size int64) (string, error) {
	// 查重必须能打开任意被索引的文件，路径来自扫描结果而非 HTTP 请求。
	f, err := os.Open(path) // #nosec G304 -- 路径来自扫描索引，是本工具的核心功能
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := blake3.New(32, nil)
	if _, err := fmt.Fprintf(h, "%d:", size); err != nil {
		return "", err
	}
	if size <= QuickChunk*2 {
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
	} else {
		buf := make([]byte, QuickChunk)
		if _, err := io.ReadFull(f, buf); err != nil {
			return "", err
		}
		_, _ = h.Write(buf)
		if _, err := f.Seek(size-QuickChunk, io.SeekStart); err != nil {
			return "", err
		}
		if _, err := io.ReadFull(f, buf); err != nil {
			return "", err
		}
		_, _ = h.Write(buf)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fullHashBufSize 是整文件哈希的读缓冲（v3.10.3）。
// io.Copy 默认只有 32KB，读 4GB 文件要 13 万次 read 系统调用；
// 1MB 缓冲把系统调用降到约 4 千次，且用 sync.Pool 复用，避免每个文件
// 重新分配 1MB 导致 GC 压力。
const fullHashBufSize = 1 << 20

var fullHashBufPool = sync.Pool{
	// 存 *[]byte 而不是 []byte：sync.Pool 的值是 any，放切片会把 24 字节切片头
	// 装箱分配一次；staticcheck SA6002 会直接报错。
	New: func() any {
		buf := make([]byte, fullHashBufSize)
		return &buf
	},
}

// FullHash 计算整文件 BLAKE3-256。
func FullHash(path string) (string, error) {
	// 同上：只读打开被索引文件，用于计算 BLAKE3 全量哈希。
	f, err := os.Open(path) // #nosec G304 -- 路径来自扫描索引，是本工具的核心功能
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	bufp := fullHashBufPool.Get().(*[]byte)
	buf := *bufp
	defer fullHashBufPool.Put(bufp)
	h := blake3.New(32, nil)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Level 返回当前 CPU 上 BLAKE3 会使用的实现级别。
// 库内部按 AVX-512 → AVX2 → 纯 Go 自动分派，这里只用于展示，不参与计算。
func Level() string {
	if runtime.GOARCH != "amd64" {
		return "纯 Go"
	}
	if cpuid.CPU.Supports(cpuid.AVX512F) {
		return "AVX-512"
	}
	if cpuid.CPU.Supports(cpuid.AVX2) {
		return "AVX2"
	}
	return "纯 Go"
}
