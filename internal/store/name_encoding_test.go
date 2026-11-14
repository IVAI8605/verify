// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFileRowNameEncoding 覆盖 v3.11.0 的编码异常标注：
// 非法 UTF-8 的路径在 JSON 里会被替换成 U+FFFD，必须靠 name_encoding 才能识别；
// 正常行不能多出这个字段，避免所有响应无谓变大。
func TestFileRowNameEncoding(t *testing.T) {
	bad := FileRow{Path: "/vol1/bad-\xff.mkv", Name: "bad-\xff.mkv", Size: 1}
	raw, err := json.Marshal(bad)
	if err != nil {
		t.Fatalf("序列化异常行失败：%v", err)
	}
	if !strings.Contains(string(raw), `"name_encoding":"invalid"`) {
		t.Fatalf("异常行应带 name_encoding=invalid，实际 %s", raw)
	}

	good := FileRow{Path: "/vol1/ok.mkv", Name: "ok.mkv", Size: 1}
	raw, err = json.Marshal(good)
	if err != nil {
		t.Fatalf("序列化正常行失败：%v", err)
	}
	if strings.Contains(string(raw), "name_encoding") {
		t.Fatalf("正常行不应出现 name_encoding，实际 %s", raw)
	}
}
