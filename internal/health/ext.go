// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// headSize 是嗅探真实类型时读取的字节数。
// 512 字节足以覆盖 ftyp、EBML DocType、ASF GUID 等需要前移判断的格式，
// 又远小于任何大文件，1GB 视频也只会读这一段。
const headSize = 512

// mediaType 描述一种可识别的文件类型。
type mediaType struct {
	// Name 是便于阅读的类型名，例如 "mp4"。
	Name string
	// Exts 是该类型可接受的扩展名集合，全部小写且不含点。
	// 同族格式互相接受，避免把 m4v 误报成扩展名错误。
	Exts []string
	// Suggest 是修正时优先建议的扩展名。
	Suggest string
}

var (
	typeMP4 = mediaType{Name: "mp4", Exts: []string{"mp4", "m4v", "mov", "f4v", "m4a", "m4b"}, Suggest: "mp4"}
	// typeM4A 是 M4A 品牌（iTunes 音频容器）。.m4a 被音乐库按扩展名分流，
	// 误报「建议改 .mp4」会破坏音乐库归类（2026-11-01 审查 10.3 前置修复）。
	typeM4A = mediaType{Name: "m4a", Exts: []string{"m4a", "mp4"}, Suggest: "m4a"}
	// type3GP 覆盖 3gp4–3gp9 品牌（3GPP 手机录像）。
	type3GP  = mediaType{Name: "3gp", Exts: []string{"3gp"}, Suggest: "3gp"}
	typeMOV  = mediaType{Name: "mov", Exts: []string{"mov", "qt"}, Suggest: "mov"}
	typeM4V  = mediaType{Name: "m4v", Exts: []string{"m4v", "mp4"}, Suggest: "m4v"}
	typeMKV  = mediaType{Name: "mkv", Exts: []string{"mkv", "webm"}, Suggest: "mkv"}
	typeWebM = mediaType{Name: "webm", Exts: []string{"webm", "mkv"}, Suggest: "webm"}
	typeAVI  = mediaType{Name: "avi", Exts: []string{"avi", "divx"}, Suggest: "avi"}
	typeTS   = mediaType{Name: "mpegts", Exts: []string{"ts", "m2ts", "mts"}, Suggest: "ts"}
	typeFLV  = mediaType{Name: "flv", Exts: []string{"flv"}, Suggest: "flv"}
	typeWMV  = mediaType{Name: "wmv", Exts: []string{"wmv", "asf"}, Suggest: "wmv"}
	typeJPG  = mediaType{Name: "jpeg", Exts: []string{"jpg", "jpeg", "jpe"}, Suggest: "jpg"}
	typePNG  = mediaType{Name: "png", Exts: []string{"png"}, Suggest: "png"}
	typeGIF  = mediaType{Name: "gif", Exts: []string{"gif"}, Suggest: "gif"}
	typeWEBP = mediaType{Name: "webp", Exts: []string{"webp"}, Suggest: "webp"}
	typeBMP  = mediaType{Name: "bmp", Exts: []string{"bmp", "dib"}, Suggest: "bmp"}
	typeTIFF = mediaType{Name: "tiff", Exts: []string{"tif", "tiff"}, Suggest: "tif"}
	typePDF  = mediaType{Name: "pdf", Exts: []string{"pdf"}, Suggest: "pdf"}
	// v3.24.0：漫画/办公/包/地图等本质是 ZIP 容器，补进来避免被建议改成 .zip。
	typeZIP = mediaType{Name: "zip", Exts: []string{"zip", "docx", "xlsx", "pptx", "epub", "jar", "apk", "odt",
		"cbz", "ods", "odp", "odg", "whl", "ipa", "nupkg", "vsix", "kmz"}, Suggest: "zip"}
	typeRAR  = mediaType{Name: "rar", Exts: []string{"rar"}, Suggest: "rar"}
	type7Z   = mediaType{Name: "7z", Exts: []string{"7z"}, Suggest: "7z"}
	typeFLAC = mediaType{Name: "flac", Exts: []string{"flac"}, Suggest: "flac"}
	typeMP3  = mediaType{Name: "mp3", Exts: []string{"mp3", "mp2"}, Suggest: "mp3"}
	// typeAAC 是 ADTS 裸流（2026-11-25）：同步字 FF F1 / FF F9，layer 位为 00。
	// 旧实现把它归进 MPEG 音频帧 → 建议改成 mp3，实际 aac 内容改成 mp3 是制造错误。
	// 只对 ADTS 裸流生效；MP4 容器里的 AAC（ftyp M4A ）由 sniffFtyp 单独处理。
	typeAAC = mediaType{Name: "aac", Exts: []string{"aac", "adts"}, Suggest: "aac"}
	typeWAV = mediaType{Name: "wav", Exts: []string{"wav"}, Suggest: "wav"}
	// typeHTML/typeMPEGPS 为同族归一（10.7）新增嗅探：html 前缀找 <html；mpeg-ps 起始码。
	typeHTML   = mediaType{Name: "html", Exts: []string{"html", "htm"}, Suggest: "html"}
	typeMPEGPS = mediaType{Name: "mpegps", Exts: []string{"mpg", "mpeg", "vob"}, Suggest: "mpg"}
	// typeRM 是 RealMedia 容器（2026-11-25）：头 4 字节是 ".RM" + 0x46。
	// ffprobe 报 format_name=rm；旧实现归进 unknown，需单独识别并建议 rm。
	typeRM = mediaType{Name: "rm", Exts: []string{"rm", "rmvb"}, Suggest: "rm"}
)

// ReasonFamily 同族扩展名归一（10.7）：内容确认后把罕见写法改成事实标准。
// 这是偏好不是错误，默认关闭，由设置页 unify_ext 开关控制。
const ReasonFamily = "family"

// unifyTarget 返回 cur 扩展名在 mode（off|jpg|jpeg）下的归一目标，空串表示不归一。
// 只纳入「改名后不影响程序识别、打开、解析」的族（10.7.1）；mkv/mp4/ts/m4a 等
// 语义分化族明确排除（10.7.2），宁可缺毋滥。
func unifyTarget(cur, mode string) string {
	if mode == "off" || cur == "" {
		return ""
	}
	if cur == "jpg" {
		if mode == "jpeg" {
			return "jpeg"
		}
		return ""
	}
	if cur == "jpeg" || cur == "jpe" || cur == "jfif" {
		if mode == "jpeg" {
			return "jpeg"
		}
		return "jpg"
	}
	switch cur {
	case "tiff":
		return "tif"
	case "divx":
		return "avi"
	case "dib":
		return "bmp"
	case "htm":
		return "html"
	case "mpeg":
		return "mpg"
	}
	return ""
}

// 扩展名问题的原因，多个原因可以同时成立。
const (
	// ReasonMismatch 内容与扩展名不属于同一族。
	ReasonMismatch = "mismatch"
	// ReasonUpperExt 扩展名为大写形式。
	ReasonUpperExt = "uppercase"
	// ReasonMultiExt 主干里还残留着一个已知扩展名，典型如 video.mp4.avi。
	ReasonMultiExt = "multi_ext"
	// ReasonUnknown 扩展名是已知媒体类型，但文件头嗅探不出对应格式（v3.15.0）。
	// 这类文件不做扩展名判断，只作为「无法确定」列出：不能自动改名，
	// 也不混进可操作列表，避免把冷门编码或加密容器误判成扩展名错误。
	ReasonUnknown = "unknown"
)

// ExtFinding 是一次扩展名检测的结论。
type ExtFinding struct {
	// RealType 是嗅探到的真实类型名，未识别时为空。
	RealType string
	// CurrentExt 是文件当前的扩展名（小写，不含点）。
	CurrentExt string
	// SuggestExt 是建议的扩展名（不含点），无法给出建议时为空。
	SuggestExt string
	// Reasons 记录命中的原因，按优先级排列。
	Reasons []string
	// FixName 是完整的建议文件名（不含目录，v4.2.0）。
	//
	// 旧实现只给扩展名，多重扩展名（video.mp4.avi）改名时会算出
	// video.mp4.mp4 —— 于是「识别得出但改不了」。这里在检测阶段就把
	// 主干里残留的已知扩展名剥掉，直接给出可执行的目标名。
	FixName string
}

// Known 表示文件内容是否落入已知类型表。
// 未知类型不做扩展名判断，避免把冷门格式误判成问题文件。
func (f ExtFinding) Known() bool { return f.RealType != "" }

// Head 读取文件头若干字节；文件短于 headSize 时返回全部内容。
func Head(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- 路径来自扫描根目录内的遍历结果
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, headSize)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return buf[:n], nil
}

// Sniff 依据文件头判定真实类型，未识别返回零值。
func Sniff(head []byte) mediaType {
	if len(head) < 4 {
		return mediaType{}
	}
	switch {
	case len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")):
		return sniffFtyp(head[8:12])
	case bytes.HasPrefix(head, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		// EBML 容器，用 DocType 区分 webm 与 matroska
		if bytes.Contains(head, []byte("webm")) {
			return typeWebM
		}
		return typeMKV
	case bytes.HasPrefix(head, []byte("RIFF")) && len(head) >= 12:
		switch string(head[8:12]) {
		case "AVI ":
			return typeAVI
		case "WAVE":
			return typeWAV
		case "WEBP":
			return typeWEBP
		case "CDXA":
			// VCD 的 MPEG-1 数据封装（2026-11-25）：RIFF....CDXAfmt ，
			// ffprobe 报 format_name=mpeg。归入 MPEG-PS 族，建议 mpg。
			return typeMPEGPS
		}
		return mediaType{}
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return typeJPG
	case bytes.HasPrefix(head, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return typePNG
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return typeGIF
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return typePDF
	case bytes.HasPrefix(head, []byte{'P', 'K', 0x03, 0x04}):
		return typeZIP
	case bytes.HasPrefix(head, []byte("Rar!\x1a\x07")):
		return typeRAR
	case bytes.HasPrefix(head, []byte{'7', 'z', 0xBC, 0xAF, 0x27, 0x1C}):
		return type7Z
	case bytes.HasPrefix(head, []byte("fLaC")):
		return typeFLAC
	case bytes.HasPrefix(head, []byte("FLV")):
		return typeFLV
	case bytes.HasPrefix(head, []byte{0x30, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11}):
		return typeWMV
	case bytes.HasPrefix(head, []byte("ID3")):
		return typeMP3
	case bytes.HasPrefix(head, []byte("BM")) && len(head) >= 14:
		return typeBMP
	case bytes.HasPrefix(head, []byte{'I', 'I', 0x2A, 0x00}), bytes.HasPrefix(head, []byte{'M', 'M', 0x00, 0x2A}):
		return typeTIFF
	case isTransportStream(head):
		return typeTS
	case head[0] == 0xFF && len(head) > 1 && (head[1]&0xE0) == 0xE0:
		// MPEG 音频帧同步头（11 位全 1）。第二字节的 layer 位（bit1-2）区分：
		//   layer=00 → ADTS 裸流 AAC（FF F1 / FF F9），建议 aac（2026-11-25 实战误判修复）
		//   layer≠00 → MP3 帧头（FF FB / FF F3 / FF F2 等），仍建议 mp3
		if head[1]&0x06 == 0 {
			return typeAAC
		}
		return typeMP3
	case len(head) >= 4 && head[0] == 0x00 && head[1] == 0x00 && head[2] == 0x01 &&
		(head[3] == 0xBA || head[3] == 0xB3):
		// MPEG-PS 起始码（与 TS 的 0x47 不冲突）：
		//   BA = pack 起始码；B3 = 视频序列头（2026-11-25，裸 MPEG 视频文件）
		return typeMPEGPS
	case bytes.HasPrefix(head, []byte{0x2E, 0x52, 0x4D, 0x46}):
		return typeRM
	case looksLikeHTML(head):
		return typeHTML
	}
	return mediaType{}
}

// isTransportStream 识别 TS 的三种包长（v3.25.0 P1-7）：
// 188 标准 TS、192 m2ts（每包前 4 字节时间码）、204 带 16 字节 RS 校验。
// 旧实现只认 head[0]==0x47 && head[188]==0x47，m2ts/204 一律识别失败，
// 在健康检测里会被标成扩展名不符、在转码入队时被误拒。
// 做法是允许首个 0x47 在前 8 字节内，再按三种包长各验证后两包同步字节。
func isTransportStream(head []byte) bool {
	// 常规路径：首个 0x47 在前 8 字节内，再按三种包长验证后两包同步字节。
	limit := 8
	if len(head) < limit {
		limit = len(head)
	}
	for start := 0; start < limit; start++ {
		if head[start] != 0x47 {
			continue
		}
		for _, size := range []int{188, 192, 204} {
			if start+2*size < len(head) && head[start+size] == 0x47 && head[start+2*size] == 0x47 {
				return true
			}
		}
	}
	// 2026-11-25 实战：一批 ts 文件头部有前导乱码（首字节非 0x47），
	// 但标准 188 包长下 188/376 偏移仍是 0x47 同步字。只验后两包，
	// 不要求首包对齐，避免把 ffprobe 实锤的 mpegts 判成 unknown。
	if len(head) > 2*188 && head[188] == 0x47 && head[376] == 0x47 {
		return true
	}
	return false
}

// looksLikeHTML 在前 512 字节里找 <html 或 <!doctype html（大小写不敏感）。
// HTML 无二进制魔数，这是能拿到的最可靠嗅探；误判面小（二进制文件几乎不会
// 恰好在头部出现该字面量），且归一目标 html 有标准 MIME。
func looksLikeHTML(head []byte) bool {
	lower := strings.ToLower(string(head))
	trimmed := strings.TrimLeft(lower, "\ufeff \t\r\n")
	return strings.HasPrefix(trimmed, "<html") || strings.HasPrefix(trimmed, "<!doctype html")
}

// sniffFtyp 依据 ftyp 的 major brand 细分 mov / m4v / mp4。
func sniffFtyp(brand []byte) mediaType {
	switch string(brand) {
	case "qt  ":
		return typeMOV
	case "M4V ", "M4VH", "M4VP":
		return typeM4V
	case "M4A ":
		return typeM4A
	}
	// 3gp4–3gp9 品牌（3GPP 手机录像）；isom-major 的 3gp 极罕见，仍落 typeMP4，接受此边界。
	if strings.HasPrefix(string(brand), "3gp") {
		return type3GP
	}
	return typeMP4
}

// CheckExt 判断文件名扩展名与文件内容是否一致。
//
// 返回 false 表示没有发现问题。内容无法识别时不做判断——宁可漏报，
// 也不要把冷门格式或加密容器误标成"扩展名错误"。
func CheckExt(fileName string, head []byte, unify string) (ExtFinding, bool) {
	raw := strings.TrimPrefix(filepath.Ext(fileName), ".")
	cur := strings.ToLower(raw)
	f := ExtFinding{CurrentExt: cur}
	// 扩展名含大写字母时单独记一条原因，修正时统一转小写。
	if raw != "" && raw != cur {
		f.Reasons = append(f.Reasons, ReasonUpperExt)
	}

	mt := Sniff(head)
	if mt.Name == "" {
		// v3.15.0：扩展名在已知类型表里、内容却认不出来时不再静默跳过。
		// 只对「本该能按文件头识别」的文件给提示；txt/doc 这类本来就不靠魔数
		// 判断的格式继续跳过，否则每个文本文件都会变成一条噪音。
		// unknown 放在原因首位：它表示「无法确定」，不能被当成可操作的大写扩展名。
		if isKnownExt(cur) {
			f.Reasons = append([]string{ReasonUnknown}, f.Reasons...)
			return f, true
		}
		return f, len(f.Reasons) > 0
	}
	f.RealType = mt.Name
	f.SuggestExt = mt.Suggest

	if !containsExt(mt.Exts, cur) {
		f.Reasons = append(f.Reasons, ReasonMismatch)
	} else if target := unifyTarget(cur, unify); target != "" {
		// 同族归一（10.7）：内容不符优先于归一——同一文件不重复报；
		// 大写扩展名（.JPG）视为同族参与，目标统一小写。
		f.Reasons = append(f.Reasons, ReasonFamily)
		f.SuggestExt = target
	}

	// 多重扩展名：去掉最后一个扩展名后，主干里仍带着一个已知扩展名，
	// 例如 video.mp4.avi 或 report.pdf.jpg。
	stem := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	if inner := strings.ToLower(strings.TrimPrefix(filepath.Ext(stem), ".")); inner != "" && isKnownExt(inner) {
		f.Reasons = append(f.Reasons, ReasonMultiExt)
	}

	if len(f.Reasons) > 0 {
		f.FixName = fixName(fileName, f.SuggestExt, f.Reasons)
	}
	return f, len(f.Reasons) > 0
}

// fixName 生成完整的目标文件名（v4.2.0，2026-11-25 建议 2）。
//
// 规则：
//   - 多重扩展名：从主干尾部逐层剥掉已知扩展名（.x264.aac.mp4 → .x264.mp4），
//     再接真实类型的扩展名，改名后不再残留名不副实的后缀；
//   - 其余情况（内容不符、大写、同族归一）：把最后一个扩展名换成建议值并转小写。
//
// 内容认不出来（RealType 为空）时返回空串：没有真实类型就没有目标名，
// 调用方必须拒绝改名，不能猜一个。
func fixName(original, suggest string, reasons []string) string {
	if suggest == "" {
		return ""
	}
	base := strings.TrimSuffix(original, filepath.Ext(original))
	if containsReason(reasons, ReasonMultiExt) {
		for {
			inner := filepath.Ext(base)
			if inner == "" {
				break
			}
			if !isKnownExt(strings.ToLower(strings.TrimPrefix(inner, "."))) {
				break
			}
			base = strings.TrimSuffix(base, inner)
		}
	}
	if base == "" {
		return ""
	}
	target := base + "." + strings.ToLower(suggest)
	if target == original {
		return ""
	}
	return target
}

// containsReason 判断原因列表里是否命中指定原因。
func containsReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}

func containsExt(exts []string, ext string) bool {
	for _, e := range exts {
		if e == ext {
			return true
		}
	}
	return false
}

// knownExts 汇总所有已知扩展名，用于多重扩展名判断。
var knownExts = func() map[string]bool {
	m := map[string]bool{}
	for _, mt := range []mediaType{
		typeMP4, typeMOV, typeM4V, typeM4A, type3GP, typeMKV, typeWebM, typeAVI, typeTS,
		typeFLV, typeWMV, typeJPG, typePNG, typeGIF, typeWEBP, typeBMP, typeTIFF,
		typePDF, typeZIP, typeRAR, type7Z, typeFLAC, typeMP3, typeWAV, typeAAC, typeRM,
		typeHTML, typeMPEGPS,
	} {
		for _, e := range mt.Exts {
			m[e] = true
		}
	}
	return m
}()

func isKnownExt(ext string) bool { return knownExts[ext] }

// IsSafeForceExt 校验 force 改名时调用方指定的目标扩展名（2026-11-25 建议 4）。
// force 只在内容嗅探不出来时由外部工具（ffprobe）实锤后使用，服务端无法自证，
// 因此目标扩展名必须落在已知媒体类型白名单里，且不含路径分隔符/点/控制字符，
// 防止把任意字符串拼进文件名造成越界或路径注入。
func IsSafeForceExt(ext string) bool {
	e := strings.ToLower(strings.Trim(ext, " \t"))
	if e == "" || strings.ContainsAny(e, "./\\") {
		return false
	}
	for _, r := range e {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return isKnownExt(e)
}
