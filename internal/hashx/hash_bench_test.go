// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package hashx

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"lukechampine.com/blake3"
)

// benchFile 生成指定大小的临时文件，用于比较读缓冲大小对整文件哈希的影响。
func benchFile(b *testing.B, size int64) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "bench.bin")
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		b.Fatal(err)
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	return path
}

// BenchmarkHashBuffer 对比 32KB（io.Copy 默认）与 1MB 缓冲的吞吐。
// 1MB 把 read 系统调用次数降到 1/32，机械盘与远端挂载上差异更明显。
func BenchmarkHashBuffer(b *testing.B) {
	const size = 64 << 20
	path := benchFile(b, size)
	for _, bufSize := range []int{32 << 10, 1 << 20} {
		bufSize := bufSize
		b.Run(map[int]string{32 << 10: "32KB", 1 << 20: "1MB"}[bufSize], func(b *testing.B) {
			b.SetBytes(size)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				f, err := os.Open(path)
				if err != nil {
					b.Fatal(err)
				}
				h := blake3.New(32, nil)
				if _, err := io.CopyBuffer(h, f, make([]byte, bufSize)); err != nil {
					_ = f.Close()
					b.Fatal(err)
				}
				_ = f.Close()
			}
		})
	}
}
