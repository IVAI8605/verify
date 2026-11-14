// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

// 本文件是 v3.17.5 的误判回归（F-11）：全不可打印检测必须按字符判定，
// 并且不把文本类扩展名当成候选。

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestUnprintableUsesRunes(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	zh := []byte("这是一个完全正常的中文文本文件，内容可以阅读。\n")

	// 文本类扩展名直接豁免，不参与检测
	txt := write("readme.txt", zh)
	if _, hit := CheckEmpty(txt, int64(len(zh)), ContentUnprintable); hit {
		t.Fatal("纯中文 txt 不应被判为全不可打印")
	}

	// 非文本扩展名但内容是可打印 UTF-8：按 rune 判定同样不命中
	bin := write("readme.bin", zh)
	if _, hit := CheckEmpty(bin, int64(len(zh)), ContentUnprintable); hit {
		t.Fatal("可打印 UTF-8 内容不应被判为全不可打印")
	}

	// 全 0xFF 的二进制：仍然命中
	raw := write("raw.bin", bytes.Repeat([]byte{0xFF}, 512))
	if _, hit := CheckEmpty(raw, 512, ContentUnprintable); !hit {
		t.Fatal("全 0xFF 的二进制应命中全不可打印")
	}

	// 零字节单独归类，不受内容模式影响
	empty := write("empty.jpg", nil)
	if f, hit := CheckEmpty(empty, 0, ContentUnprintable); !hit || f.Reason != ReasonZeroSize {
		t.Fatalf("零字节应判为 zero_size，实际 %+v hit=%v", f, hit)
	}
}
