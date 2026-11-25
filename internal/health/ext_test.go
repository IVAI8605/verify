// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"os"
	"path/filepath"
	"testing"
)

// 各类型的文件头样本，够 Sniff 判定即可。
var (
	headMP4  = append([]byte{0x00, 0x00, 0x00, 0x18}, []byte("ftypisom")...)
	headMOV  = append([]byte{0x00, 0x00, 0x00, 0x14}, []byte("ftypqt  ")...)
	headPNG  = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}
	headJPG  = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	headMKV  = []byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F, 0x42, 0x86, 0x81, 'm', 'a', 't', 'r', 'o', 's', 'k', 'a'}
	headWebM = []byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F, 0x42, 0x86, 0x81, 'w', 'e', 'b', 'm'}
	headPDF  = []byte("%PDF-1.7")
	headZIP  = []byte{'P', 'K', 0x03, 0x04, 0x14, 0x00}
)

// TestSniffKnownTypes 覆盖四类必须支持的格式，外加若干常见格式。
func TestSniffKnownTypes(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"mp4", headMP4, "mp4"},
		{"mov", headMOV, "mov"},
		{"png", headPNG, "png"},
		{"jpg", headJPG, "jpeg"},
		{"mkv", headMKV, "mkv"},
		{"webm", headWebM, "webm"},
		{"pdf", headPDF, "pdf"},
		{"zip", headZIP, "zip"},
		{"空内容", nil, ""},
		{"未知内容", []byte("hello world"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Sniff(c.head).Name; got != c.want {
				t.Fatalf("Sniff=%q，期望 %q", got, c.want)
			}
		})
	}
}

// TestCheckExtMismatch 是核心用例：内容是 PNG，扩展名却是 jpg。
func TestCheckExtMismatch(t *testing.T) {
	f, bad := CheckExt("photo.jpg", headPNG, "off")
	if !bad {
		t.Fatal("内容为 PNG 而扩展名为 jpg，应判定为不匹配")
	}
	if f.SuggestExt != "png" {
		t.Fatalf("建议扩展名应为 png，实际 %q", f.SuggestExt)
	}
	if !hasReason(f, ReasonMismatch) {
		t.Fatalf("应包含 mismatch 原因，实际 %v", f.Reasons)
	}
}

// TestCheckExtUnknownKnownExt 覆盖「已知扩展名但内容认不出」不再静默（v3.15.0）。
// 这类文件作为不确定项报出；未知扩展名（如 txt）继续跳过，避免噪音。
func TestCheckExtUnknownKnownExt(t *testing.T) {
	f, bad := CheckExt("movie.mkv", []byte("hello world"), "off")
	if !bad {
		t.Fatal("已知扩展名但内容认不出，应作为不确定项报出")
	}
	if len(f.Reasons) == 0 || f.Reasons[0] != ReasonUnknown {
		t.Fatalf("首个原因应为 unknown，实际 %v", f.Reasons)
	}
	if f.SuggestExt != "" {
		t.Fatalf("内容认不出时不应给建议扩展名，实际 %q", f.SuggestExt)
	}
	// 大写扩展名也不能把 unknown 挤到后面：不确定项必须优先识别，避免被批量改名
	up, _ := CheckExt("MOVIE.MKV", []byte("hello world"), "off")
	if len(up.Reasons) < 2 || up.Reasons[0] != ReasonUnknown || !hasReason(up, ReasonUpperExt) {
		t.Fatalf("大写 + 内容认不出应保留两类原因且 unknown 在前，实际 %v", up.Reasons)
	}
	if _, bad := CheckExt("notes.txt", []byte("hello world"), "off"); bad {
		t.Fatal("未知扩展名的文本文件不应报问题")
	}
	if !AdvisoryReason(ReasonUnknown) || !AdvisoryReason(ReasonTimeout) || !AdvisoryReason(ReasonOversized) {
		t.Fatal("unknown / timeout / oversized 都应判为无法确定")
	}
	if AdvisoryReason(ReasonMismatch) {
		t.Fatal("内容不符是确定结论，不属于无法确定")
	}
}

// TestCheckExtFamilyAccepted 验证同族格式不误报：m4v 内容是 mp4、mkv 内容是 webm。
func TestCheckExtFamilyAccepted(t *testing.T) {
	if _, bad := CheckExt("a.m4v", headMP4, "off"); bad {
		t.Fatal("m4v 与 mp4 同族，不应报错")
	}
	if _, bad := CheckExt("a.mkv", headWebM, "off"); bad {
		t.Fatal("mkv 与 webm 同族，不应报错")
	}
	if _, bad := CheckExt("a.jpeg", headJPG, "off"); bad {
		t.Fatal("jpeg 与 jpg 同族，不应报错")
	}
}

// TestCheckExtUppercase 验证大写扩展名被单独标记。
func TestCheckExtUppercase(t *testing.T) {
	f, bad := CheckExt("MOVIE.MP4", headMP4, "off")
	if !bad {
		t.Fatal("大写扩展名应被标记")
	}
	if !hasReason(f, ReasonUpperExt) {
		t.Fatalf("应包含 uppercase 原因，实际 %v", f.Reasons)
	}
}

// TestCheckExtMultiExt 验证多重扩展名：video.mp4.avi 的真实内容是 mp4。
func TestCheckExtMultiExt(t *testing.T) {
	f, bad := CheckExt("video.mp4.avi", headMP4, "off")
	if !bad {
		t.Fatal("多重扩展名应被标记")
	}
	if !hasReason(f, ReasonMultiExt) {
		t.Fatalf("应包含 multi_ext 原因，实际 %v", f.Reasons)
	}
	if !hasReason(f, ReasonMismatch) {
		t.Fatalf("avi 与 mp4 不属同族，应同时报 mismatch，实际 %v", f.Reasons)
	}
}

// TestCheckExtUnknownContent 验证未知内容不参与判断，避免把冷门格式当成错误。
func TestCheckExtUnknownContent(t *testing.T) {
	f, bad := CheckExt("notes.txt", []byte("plain text"), "off")
	if bad {
		t.Fatalf("未知内容不应报错，实际 %v", f.Reasons)
	}
	if f.Known() {
		t.Fatal("未知内容不应被标记为已知类型")
	}
}

// TestCheckExtNoExtension 验证无扩展名文件在内容已知时会给出建议。
func TestCheckExtNoExtension(t *testing.T) {
	f, bad := CheckExt("README", headPNG, "off")
	if !bad {
		t.Fatal("无扩展名但内容是 PNG，应给出建议")
	}
	if f.SuggestExt != "png" {
		t.Fatalf("建议扩展名应为 png，实际 %q", f.SuggestExt)
	}
}

// TestHeadReadsPrefix 验证只读文件头，不触碰大文件全文。
func TestHeadReadsPrefix(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.png")
	content := append(append([]byte{}, headPNG...), make([]byte, 1<<20)...)
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatal(err)
	}
	head, err := Head(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(head) != headSize {
		t.Fatalf("应读取 %d 字节，实际 %d", headSize, len(head))
	}
	if Sniff(head).Name != "png" {
		t.Fatal("头部嗅探结果应为 png")
	}
}

// TestHeadShortFile 验证短文件不会报错，返回全部内容。
func TestHeadShortFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tiny.pdf")
	if err := os.WriteFile(p, headPDF, 0o600); err != nil {
		t.Fatal(err)
	}
	head, err := Head(p)
	if err != nil {
		t.Fatalf("短文件不应返回错误：%v", err)
	}
	if len(head) != len(headPDF) {
		t.Fatalf("应返回 %d 字节，实际 %d", len(headPDF), len(head))
	}
}

func hasReason(f ExtFinding, want string) bool {
	for _, r := range f.Reasons {
		if r == want {
			return true
		}
	}
	return false
}

// ftypHead 构造指定 major brand 的 ftyp 文件头。
func ftypHead(brand string) []byte {
	return append([]byte{0x00, 0x00, 0x00, 0x18}, append([]byte("ftyp"), []byte(brand)...)...)
}

// TestSniffFtypBrands 品牌表补全回归（10.3 前置修复：m4a/3gp 误报）。
func TestSniffFtypBrands(t *testing.T) {
	cases := []struct {
		name  string
		brand string
		want  string
	}{
		{"M4A 品牌", "M4A ", "m4a"},
		{"3gp4 品牌", "3gp4", "3gp"},
		{"3gp9 品牌", "3gp9", "3gp"},
		{"isom 仍归 mp4", "isom", "mp4"},
		{"qt 回归", "qt  ", "mov"},
		{"M4V 回归", "M4V ", "m4v"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Sniff(ftypHead(c.brand)).Name; got != c.want {
				t.Fatalf("Sniff(brand=%q)=%q，期望 %q", c.brand, got, c.want)
			}
		})
	}
}

// TestCheckExtM4A3GP 品牌补全后的 CheckExt 行为（设计文档 10.3.6 测试点）。
func TestCheckExtM4A3GP(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		brand    string
		wantFind bool // 是否报告问题
	}{
		{"M4A 品牌 + .m4a 不报", "song.m4a", "M4A ", false},
		// 设计文档 10.3：typeM4A.Exts 含 mp4（m4a 容器可装视频，双向互认）→ 不报
		{"M4A 品牌 + .mp4 不报（双向互认）", "song.mp4", "M4A ", false},
		{"3gp4 品牌 + .3gp 不报", "clip.3gp", "3gp4", false},
		{"isom 品牌 + .m4a 不报（typeMP4.Exts 含 m4a）", "song.m4a", "isom", false},
		{"isom 品牌 + .mp4 回归不报", "video.mp4", "isom", false},
		{"qt 品牌 + .mov 回归不报", "video.mov", "qt  ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			finding, has := CheckExt(c.file, ftypHead(c.brand), "off")
			if has != c.wantFind {
				t.Fatalf("CheckExt(%s, brand=%s) 报告=%v，期望 %v；reasons=%v", c.file, c.brand, has, c.wantFind, finding.Reasons)
			}
		})
	}
}

// TestSniffHTMLMPEGPS 归一新增嗅探（10.7）：html 前缀与 mpeg-ps 起始码。
func TestSniffHTMLMPEGPS(t *testing.T) {
	if got := Sniff([]byte("<!DOCTYPE html><html>")).Name; got != "html" {
		t.Fatalf("doctype html 应识别为 html，实际 %q", got)
	}
	if got := Sniff([]byte("\ufeff<html><body>x")).Name; got != "html" {
		t.Fatalf("BOM+html 应识别为 html，实际 %q", got)
	}
	if got := Sniff([]byte{0x00, 0x00, 0x01, 0xBA, 0x00, 0x00}).Name; got != "mpegps" {
		t.Fatalf("MPEG-PS 起始码应识别为 mpegps，实际 %q", got)
	}
	if got := Sniff([]byte{0x47, 0x40, 0x00, 0x10}).Name; got == "mpegps" {
		t.Fatal("TS 包头不应识别为 mpegps")
	}
}

// TestCheckExtQuietFamilies 回归 v3.24.0：这些扩展名本质是已知容器，不应产生改名建议。
// cbz（漫画包）/ ods / odp / odg（办公）/ whl / ipa / nupkg / vsix / kmz 都是 ZIP；
// m4b 是 MP4 音频书；vob 是 MPEG-PS。
func TestCheckExtQuietFamilies(t *testing.T) {
	zipFamily := []string{"a.cbz", "a.ods", "a.odp", "a.odg", "a.whl", "a.ipa", "a.nupkg", "a.vsix", "a.kmz"}
	for _, name := range zipFamily {
		if f, bad := CheckExt(name, headZIP, "off"); bad {
			t.Fatalf("%s 属 ZIP 族，不应报错：%v", name, f.Reasons)
		}
	}
	if f, bad := CheckExt("a.m4b", headMP4, "off"); bad {
		t.Fatalf("m4b 属 MP4 族，不应报错：%v", f.Reasons)
	}
	if f, bad := CheckExt("a.vob", []byte{0x00, 0x00, 0x01, 0xBA, 0x00, 0x00}, "off"); bad {
		t.Fatalf("vob 属 MPEG-PS 族，不应报错：%v", f.Reasons)
	}
}

// TestIsTransportStreamLeadGarbage 回归 2026-11-25 实战：一批 mpegts 前几字节是 garbage，
// 但 188/376 偏移是标准 TS 包同步字（0x47 40/50 00 10）。旧实现要求首 0x47 在前 8 字节内，
// 把这批文件判成 unknown，无法批量修正。只验后两包同步即可确认。
func TestIsTransportStreamLeadGarbage(t *testing.T) {
	h := make([]byte, 512)
	copy(h, []byte{0x04, 0xAC, 0xC7, 0x19, 0xBB, 0xCC}) // 前导乱码
	copy(h[188:], []byte{0x47, 0x40, 0x00, 0x10})
	copy(h[376:], []byte{0x47, 0x50, 0x00, 0x10})
	if !isTransportStream(h) {
		t.Fatal("188/376 偏移有 TS 包同步字，应识别为 mpegts")
	}
	if got := Sniff(h).Name; got != "mpegts" {
		t.Fatalf("Sniff 应识别为 mpegts，实际 %q", got)
	}
	// 负向：只有首字节 0x47、后续无同步，不应误判
	neg := make([]byte, 512)
	neg[0] = 0x47
	if isTransportStream(neg) {
		t.Fatal("仅首字节 0x47 不足以判定 TS")
	}
	// 回归：标准 TS（首字节即 0x47）仍识别
	std := make([]byte, 512)
	copy(std, []byte{0x47, 0x40, 0x00, 0x10})
	copy(std[188:], []byte{0x47, 0x40, 0x00, 0x10})
	copy(std[376:], []byte{0x47, 0x40, 0x00, 0x10})
	if !isTransportStream(std) {
		t.Fatal("标准 TS 应识别")
	}
}

// TestIsSafeForceExt 回归 2026-11-25 实战建议 4：unknown 类给外部验证过的调用方一条 force 通道。
// 目标扩展名必须落在已知媒体扩展名白名单，防止把任意字符串拼进文件名造成路径注入。
func TestIsSafeForceExt(t *testing.T) {
	for _, ext := range []string{"ts", "mp4", "aac", "MP4", " mkv "} {
		if !IsSafeForceExt(ext) {
			t.Errorf("%q 是合法目标扩展名", ext)
		}
	}
	for _, ext := range []string{"", "../evil", "a/b", "ts/../../x", "exe", "a.b", "ts\n", "tar.gz"} {
		if IsSafeForceExt(ext) {
			t.Errorf("%q 不是安全的目标扩展名，应拒绝", ext)
		}
	}
}

// TestSniffADTSAAC 回归 2026-11-25 实战：.aac 文件（ADTS 同步字 FF F1）被误判成 mp3，
// 批量改名把 86 个 aac 改成 mp3，反而制造错误。ADTS 必须识别为 aac。
func TestSniffADTSAAC(t *testing.T) {
	// 真实样本头：ff f1 4c 80 03 df fc de ...（2026-11-25 aac_misjudged.txt）
	head := []byte{0xFF, 0xF1, 0x4C, 0x80, 0x03, 0xDF, 0xFC, 0xDE}
	if got := Sniff(head).Name; got != "aac" {
		t.Fatalf("ADTS 同步字应识别为 aac，实际 %q", got)
	}
	// MPEG-2 AAC：FF F9
	if got := Sniff([]byte{0xFF, 0xF9, 0x4C, 0x80}).Name; got != "aac" {
		t.Fatalf("MPEG-2 ADTS 应识别为 aac，实际 %q", got)
	}
	// .aac 文件配真 ADTS 头不应被建议改名
	if f, bad := CheckExt("song.aac", head, "off"); bad {
		t.Fatalf("aac 文件配 ADTS 头不应报问题，实际 reasons=%v suggest=%q", f.Reasons, f.SuggestExt)
	}
	// 真 mp3 帧头（FF FB / FF F3）与 ID3 不能回归成 aac
	for _, h := range [][]byte{{0xFF, 0xFB, 0x90, 0x00}, {0xFF, 0xF3, 0x90, 0x00}, []byte("ID3\x04\x00\x00")} {
		if got := Sniff(h).Name; got != "mp3" {
			t.Fatalf("MPEG 音频帧/ID3 应识别为 mp3，实际 %q（头 %x）", got, h)
		}
	}
	// 真 mp3 文件配 aac 头仍报 mismatch，且建议 aac
	if f, bad := CheckExt("song.mp3", head, "off"); !bad || f.SuggestExt != "aac" {
		t.Fatalf("mp3 文件配 ADTS 头应建议改 aac，实际 bad=%v suggest=%q", bad, f.SuggestExt)
	}
}

// TestUnifyTarget 归一映射与开关（10.7.1/10.7.2）。
func TestUnifyTarget(t *testing.T) {
	cases := []struct {
		cur, mode, want string
	}{
		{"jpeg", "jpg", "jpg"},
		{"jpe", "jpg", "jpg"},
		{"jfif", "jpg", "jpg"},
		{"jpg", "jpg", ""}, // 已是目标
		{"jpeg", "jpeg", "jpeg"},
		{"jpg", "jpeg", "jpeg"},
		{"tiff", "jpg", "tif"},
		{"divx", "jpg", "avi"},
		{"dib", "jpg", "bmp"},
		{"htm", "jpg", "html"},
		{"mpeg", "jpg", "mpg"},
		{"png", "jpg", ""},  // 不在纳入族
		{"mkv", "jpg", ""},  // 10.7.2 排除族
		{"m4a", "jpg", ""},  // 排除族
		{"docx", "jpg", ""}, // 排除族
		{"jpeg", "off", ""}, // 关闭
		{"tiff", "off", ""}, // 关闭
	}
	for _, c := range cases {
		if got := unifyTarget(c.cur, c.mode); got != c.want {
			t.Errorf("unifyTarget(%q, %q)=%q，期望 %q", c.cur, c.mode, got, c.want)
		}
	}
}

// TestCheckExtUnify 归一行为：family 原因 + 目标扩展名；内容不符优先于归一。
func TestCheckExtUnify(t *testing.T) {
	// .jpeg 内容是 JPEG → 开归一后报 family，建议 jpg
	f, bad := CheckExt("photo.jpeg", headJPG, "jpg")
	if !bad || f.SuggestExt != "jpg" {
		t.Fatalf(".jpeg 应报归一建议 jpg，实际 bad=%v suggest=%q", bad, f.SuggestExt)
	}
	if f.Reasons[0] != ReasonFamily {
		t.Fatalf("原因应为 family，实际 %v", f.Reasons)
	}
	// 关闭 → 不报
	if _, bad := CheckExt("photo.jpeg", headJPG, "off"); bad {
		t.Fatal("关闭归一后 .jpeg 不应报")
	}
	// .JPG 开 jpeg 模式 → 建议 jpeg（大写参与、目标小写）
	f, bad = CheckExt("photo.JPG", headJPG, "jpeg")
	if !bad || f.SuggestExt != "jpeg" {
		t.Fatalf(".JPG 在 jpeg 模式应建议 jpeg，实际 bad=%v suggest=%q", bad, f.SuggestExt)
	}
	// 内容不符优先：.png 内容是 JPEG 报 mismatch（png 不在 jpg 互认族）
	f, bad = CheckExt("photo.png", headJPG, "jpg")
	if !bad {
		t.Fatal("内容不符应报")
	}
	for _, r := range f.Reasons {
		if r == ReasonFamily {
			t.Fatal("内容不符时不应同时报归一")
		}
	}
	// 排除族不因归一误报：mkv 内容 → .webm 同族，开归一不报
	if _, bad := CheckExt("a.webm", headMKV, "jpg"); bad {
		t.Fatal("mkv/webm 同族不应报")
	}
}
