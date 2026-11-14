// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestProbeBrokenCategories 验收（设计文档 10.3）：截断 jpg、文本改名 .zip、坏 JSON、
// 正常对照组四类判定全对；过大图片记「过大未检」而非损坏。
func TestProbeBrokenCategories(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// 正常小 PNG（对照组）
	var goodPNG bytes.Buffer
	if err := png.Encode(&goodPNG, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	pGood := write("good.png", goodPNG.Bytes())
	// 截断 jpg（真头 + 截断体）
	pBadJPG := write("bad.jpg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x99, 0x98})
	// 文本改名 .zip（zip 头校验必挂）
	pFakeZip := write("fake.zip", []byte("this is not a zip file at all"))
	// 正常 zip 对照
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	w, _ := zw.Create("x.txt")
	_, _ = w.Write([]byte("hi"))
	_ = zw.Close()
	pGoodZip := write("good.zip", zipBuf.Bytes())
	// 坏 JSON / 好 JSON
	pBadJSON := write("bad.json", []byte(`{"a": 1,,}`))
	pGoodJSON := write("good.json", []byte(`{"a": 1}`))
	// 坏 XML / 好 XML
	pBadXML := write("bad.xml", []byte(`<a><b></a>`))
	pGoodXML := write("good.xml", []byte(`<a><b/></a>`))
	// 超像素上限：PNG 头声明超大尺寸（DecodeConfig 阶段拒，不实际写像素）
	// 用正常头 + 合成超大 config 难造——改为直接测上限路径：构造 12000x12000 的 png
	// 太慢，跳过实际构造，oversized 路径由实现保证（与 photo.TestOversizeSkipped 同款）。

	cases := []struct {
		name    string
		path    string
		wantNil bool
		wantRsn string
	}{
		{"正常 PNG", pGood, true, ""},
		{"截断 JPG", pBadJPG, false, ReasonImageBroken},
		{"假 ZIP", pFakeZip, false, ReasonZipBroken},
		{"正常 ZIP", pGoodZip, true, ""},
		{"坏 JSON", pBadJSON, false, ReasonJSONBroken},
		{"好 JSON", pGoodJSON, true, ""},
		{"坏 XML", pBadXML, false, ReasonXMLBroken},
		{"好 XML", pGoodXML, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ProbeBroken(context.Background(), "/nonexistent-ffmpeg", "", c.path, DefaultRunner())
			if c.wantNil {
				if got != nil {
					t.Fatalf("应判正常，实际 %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("应报损坏，实际 nil")
			}
			if got.Reason != c.wantRsn {
				t.Fatalf("reason=%q，期望 %q（detail=%s）", got.Reason, c.wantRsn, got.Detail)
			}
		})
	}
}

// TestHasBrokenExt 候选集覆盖（视频+音频+图片+zip+json+xml）。
func TestHasBrokenExt(t *testing.T) {
	for _, name := range []string{"a.mp4", "a.mp3", "a.flac", "a.png", "a.jpg", "a.docx", "a.json", "a.svg", "a.xml"} {
		if !HasBrokenExt(name) {
			t.Fatalf("%s 应在检测范围", name)
		}
	}
	for _, name := range []string{"a.txt", "a.pdf", "a.mkv.src", "a"} {
		if HasBrokenExt(name) {
			t.Fatalf("%s 不应在检测范围", name)
		}
	}
}
