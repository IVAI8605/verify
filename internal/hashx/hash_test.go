// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package hashx

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestFullHashKnownVector 用 BLAKE3 官方空输入向量，防止底层实现被误替换。
func TestFullHashKnownVector(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.bin")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := FullHash(p)
	if err != nil {
		t.Fatal(err)
	}
	const want = "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"
	if got != want {
		t.Fatalf("BLAKE3 空文件哈希错误: got=%s want=%s", got, want)
	}
}

// TestQuickHashSampling 验证预哈希只看头尾：改中间不影响预哈希，但完整哈希会变。
func TestQuickHashSampling(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sample.bin")
	head := bytes.Repeat([]byte{0xAA}, QuickChunk)
	middle := bytes.Repeat([]byte{0xBB}, QuickChunk)
	tail := bytes.Repeat([]byte{0xCC}, QuickChunk)
	if err := os.WriteFile(p, append(append(head, middle...), tail...), 0o644); err != nil {
		t.Fatal(err)
	}
	q1, err := QuickHash(p, int64(len(head)+len(middle)+len(tail)))
	if err != nil {
		t.Fatal(err)
	}
	f1, err := FullHash(p)
	if err != nil {
		t.Fatal(err)
	}

	// 中间字节变化对预哈希不可见，完整哈希必须变化
	changed := bytes.Repeat([]byte{0xDD}, QuickChunk)
	data := append(append(append([]byte{}, head...), changed...), tail...)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// 文件大小不变；先按原大小计算预哈希
	q2, err := QuickHash(p, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if q2 != q1 {
		t.Fatalf("中间内容变化不应改变预哈希: %s != %s", q2, q1)
	}
	f2, err := FullHash(p)
	if err != nil {
		t.Fatal(err)
	}
	if f2 == f1 {
		t.Fatal("中间内容变化后完整哈希应变化")
	}

	// 尾部字节变化必须改变预哈希
	data[len(data)-1] = 0xEE
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	q3, err := QuickHash(p, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if q3 == q1 {
		t.Fatal("尾部变化后预哈希应变化")
	}
}

// TestQuickHashSmallFile 小文件直接哈希全文，任何字节变化都应影响预哈希。
func TestQuickHashSmallFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "small.bin")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	q1, _ := QuickHash(p, 5)
	if err := os.WriteFile(p, []byte("hellO"), 0o644); err != nil {
		t.Fatal(err)
	}
	q2, _ := QuickHash(p, 5)
	if q1 == q2 {
		t.Fatal("小文件内容变化后预哈希应变化")
	}
}

func TestLevelAndDisplayName(t *testing.T) {
	if DisplayName != "BLAKE3-256" {
		t.Fatalf("展示名称错误: %s", DisplayName)
	}
	switch Level() {
	case "AVX-512", "AVX2", "纯 Go":
	default:
		t.Fatalf("未知的 BLAKE3 加速级别: %s", Level())
	}
	if QuickChunk != 4096 {
		t.Fatalf("预哈希块大小应为 4KB，实际 %d", QuickChunk)
	}
}
