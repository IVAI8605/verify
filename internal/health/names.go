// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// 文件名问题的判定原因。
const (
	// ReasonEmoji 文件名含 emoji。
	ReasonEmoji = "emoji"
	// ReasonSpace 文件名以空格开头或结尾。
	ReasonSpace = "space"
	// ReasonRepeat 连续出现 3 个及以上相同的非字母数字字符。
	ReasonRepeat = "repeat"
	// ReasonSpaces 连续出现 2 个及以上空格。
	// 空格单独成一类：它在文件名里最常见，也最容易批量修好，
	// 与"___""..." 这类标点重复混在一起会让结果失去可操作性。
	ReasonSpaces = "multi_space"
	// ReasonCharset 含字符集白名单之外的字符（默认关闭）。
	ReasonCharset = "charset"
	// ReasonNonCJK 含非 ASCII 且非中日韩字符（默认关闭）。
	ReasonNonCJK = "non_cjk"
	// ReasonEncoding 文件名不是合法 UTF-8（v3.12.0）。
	// 这类名字入库后，JSON 会把非法字节换成 U+FFFD，前端回传的路径永远对不上原文件，
	// 因此在「文件名不规范」里单独成一类，由服务端重新计算目标名来修。
	ReasonEncoding = "invalid_encoding"
	// ReasonMissingDot 末尾粘连了已知扩展名但缺分隔点（v4.2.0）。
	// 典型如相机/手机导出的 VID_20240115_150825_676_545mp4，
	// 后缀名归进了文件名主干，按扩展名筛选/播放器识别都会失效。
	ReasonMissingDot = "missing_dot"
)

// NameOptions 控制文件名检测中默认关闭的两条规则。
//
// 默认全部关闭是有意为之：中文文件名合法，字符集白名单一旦默认开启，
// 所有中文名都会被标记，工具立刻变成噪音源。
type NameOptions struct {
	// RestrictCharset 启用字符集白名单检查。
	RestrictCharset bool
	// NonCJKNonASCII 启用"非 ASCII 且非中日韩"检查。
	NonCJKNonASCII bool
}

// NameFinding 是一次文件名检测的结论。
type NameFinding struct {
	// Original 是原始文件名（不含目录）。
	Original string
	// Suggested 是建议的文件名；无法生成不同名字时为空。
	Suggested string
	// Reasons 记录命中的原因。
	Reasons []string
}

// CheckName 检测文件名是否符合规范。
//
// 只做字符串分析，不访问文件系统：因此对十万个文件也是内存遍历级别的开销。
func CheckName(name string, opt NameOptions) (NameFinding, bool) {
	f := NameFinding{Original: name}
	if name == "" {
		return f, false
	}
	base, _ := splitName(name)

	// v3.12.0：非 UTF-8 文件名单独判定并直接给修复建议。
	// 后面的 emoji/空格/重复标点都在 rune 上做判断，对非法字节只会得到一串 U+FFFD，
	// 既判断不准，也不该把这些噪音混进「原因」里。
	if !utf8.ValidString(name) {
		f.Reasons = append(f.Reasons, ReasonEncoding)
		// 用 NormalizeName 生成建议，保证预览名与真正改名时的目标名完全一致
		//（它内部会先做编码修复，再统一扩展名大小写）。
		f.Suggested = NormalizeName(name, opt)
		if f.Suggested == name {
			f.Suggested = ""
		}
		return f, true
	}

	// 首尾空格：既包括整个文件名的首尾，也包括主干与扩展名之间的残留空格（"note .txt"）。
	if strings.HasPrefix(name, " ") || strings.HasSuffix(name, " ") ||
		strings.HasPrefix(base, " ") || strings.HasSuffix(base, " ") {
		f.Reasons = append(f.Reasons, ReasonSpace)
	}
	if hasEmoji(base) {
		f.Reasons = append(f.Reasons, ReasonEmoji)
	}
	if hasMultiSpace(base) {
		f.Reasons = append(f.Reasons, ReasonSpaces)
	}
	if hasRepeatedPunct(base) {
		f.Reasons = append(f.Reasons, ReasonRepeat)
	}
	// v4.2.0：末尾粘连已知扩展名却缺点号（VID_..._545mp4）。
	// 放在扩展名类判定之外——它是文件名主干的问题，修法是补一个点。
	if gluedExt(name) != "" {
		f.Reasons = append(f.Reasons, ReasonMissingDot)
	}
	if opt.RestrictCharset && hasDisallowed(base) {
		f.Reasons = append(f.Reasons, ReasonCharset)
	}
	if opt.NonCJKNonASCII && hasNonCJKNonASCII(base) {
		f.Reasons = append(f.Reasons, ReasonNonCJK)
	}

	if len(f.Reasons) == 0 {
		return f, false
	}
	f.Suggested = NormalizeName(name, opt)
	if f.Suggested == name {
		f.Suggested = ""
	}
	return f, true
}

// NormalizeName 依据命中的规则生成建议文件名。
//
// 只修明确的问题：去 emoji、去首尾空格、合并重复字符、按需替换受限字符，
// 并把扩展名统一成小写。中文、空格以外的正文一律不动。
func NormalizeName(name string, opt NameOptions) string {
	// v3.12.0：先把非 UTF-8 名字转成合法 UTF-8，再做常规清理；
	// 否则下面的 rune 处理会把非法字节变成 U+FFFD 写进目标名，等于改出一个坏名字。
	if fixed, changed := RepairEncoding(name); changed {
		name = fixed
	}
	base, ext := splitName(name)
	// v4.2.0：末尾粘连的扩展名先拆出来，否则下面的主干清理会把
	// VID_..._545mp4 当成一个整体，补不回那个点。
	if ext == "" {
		if glued := gluedExt(base); glued != "" {
			ext = glued
			base = strings.TrimSuffix(base, glued)
		}
	}

	base = stripEmoji(base)
	base = strings.TrimSpace(base)
	if opt.RestrictCharset {
		base = replaceDisallowed(base)
	}
	base = collapseRepeats(base)
	if base == "" {
		base = "unnamed"
	}
	// 扩展名统一小写：大写扩展名的修正在这里落地，检测归扩展名检测负责。
	ext = strings.ToLower(ext)
	if ext == "" {
		return base
	}
	return base + "." + ext
}

// RepairEncoding 把非 UTF-8 文件名转成合法 UTF-8（v3.12.0）。
//
// 顺序：先按 GB18030 解码——中文环境里的乱码名绝大多数来自这个编码；
// 解不出来时把非法字节转成 %XX。后者虽然不好看，但逐字节可逆，
// 用户能据此还原原名，不会因为「猜错编码」把原始信息丢掉。
//
// 解码结果里只要出现 U+FFFD 就按「解不出来」处理：那说明码表对这个字节序列
// 也没有确定答案，把替换字符写进新文件名等于制造第二个坏名字。
//
// changed=false 表示名字本来就是合法 UTF-8，调用方不需要改名。
func RepairEncoding(name string) (string, bool) {
	if utf8.ValidString(name) {
		return name, false
	}
	if out, err := simplifiedchinese.GB18030.NewDecoder().String(name); err == nil &&
		utf8.ValidString(out) && out != name && !strings.ContainsRune(out, utf8.RuneError) {
		return out, true
	}
	return escapeInvalidBytes(name), true
}

// escapeInvalidBytes 把非法 UTF-8 字节替换成 %XX，合法片段原样保留。
func escapeInvalidBytes(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteByte('%')
			b.WriteByte(hex[s[i]>>4])
			b.WriteByte(hex[s[i]&0x0F])
			i++
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

// gluedExt 判断文件名末尾是否粘连了一个已知扩展名却缺点号（v4.2.0）。
//
// 只认「扩展名前面紧跟数字」这一种形态：真实样本 VID_20240115_150825_676_545mp4
// 是相机/手机导出时把点号吃掉了。规则放宽到任意字母边界会把 songmp3、
// 报告pdf 这类本来就叫这个名字的文件全部误报——改错名字比漏报更糟。
//
// 已经有扩展名的文件一律不判：那是扩展名检测的职责，两类结论不能互相覆盖。
func gluedExt(name string) string {
	base, ext := splitName(name)
	if ext != "" || base == "" {
		return ""
	}
	lower := strings.ToLower(base)
	// 按长度倒序匹配，避免 .ts 抢先吃掉 .mts 这类同尾扩展名。
	for _, e := range gluedExts {
		if !strings.HasSuffix(lower, e) {
			continue
		}
		cut := len(base) - len(e)
		if cut <= 0 {
			continue
		}
		r, size := utf8.DecodeLastRuneInString(base[:cut])
		if size == 0 || !unicode.IsDigit(r) {
			continue
		}
		return base[cut:]
	}
	return ""
}

// gluedExts 是允许「缺点号」形态的扩展名，按长度倒序，全部小写。
// 只收常见的相机/手机导出容器；txt、doc 这类不靠扩展名识别内容的格式不收，
// 否则人写的文件名（如 说明1txt）会被当成问题。
var gluedExts = func() []string {
	list := []string{
		"mp4", "m4v", "mov", "3gp", "mkv", "webm", "avi", "wmv", "flv",
		"mts", "m2ts", "ts", "mpg", "mpeg", "vob", "rm", "rmvb",
		"jpg", "jpeg", "png", "gif", "webp", "bmp", "tif", "tiff",
		"mp3", "m4a", "aac", "flac", "wav",
	}
	sort.Slice(list, func(i, j int) bool {
		if len(list[i]) != len(list[j]) {
			return len(list[i]) > len(list[j])
		}
		return list[i] < list[j]
	})
	return list
}()

// splitName 拆出主干与扩展名（扩展名不含点）。
func splitName(name string) (string, string) {
	e := filepath.Ext(name)
	if e == "" {
		return name, ""
	}
	return strings.TrimSuffix(name, e), strings.TrimPrefix(e, ".")
}

// stripEmoji 删除 emoji 及其变体选择符。
func stripEmoji(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isEmoji(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// hasEmoji 判断字符串是否含 emoji。
func hasEmoji(s string) bool {
	for _, r := range s {
		if isEmoji(r) {
			return true
		}
	}
	return false
}

// isEmoji 覆盖常见 emoji 区段与变体选择符。
// 不追求覆盖全部 Unicode 符号，只覆盖用户会往文件名里放的那些。
func isEmoji(r rune) bool {
	switch {
	case r == 0xFE0F, r == 0xFE0E: // 变体选择符
		return true
	case r >= 0x1F000 && r <= 0x1FAFF: // 表情、旗帜、补充符号
		return true
	case r >= 0x2600 && r <= 0x27BF: // 杂项符号与装饰符
		return true
	case r >= 0x2B00 && r <= 0x2BFF: // 箭头与星形
		return true
	case r >= 0x2190 && r <= 0x21FF: // 箭头
		return true
	}
	return false
}

// isCJK 判断是否中日韩字符或全角标点。
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // 汉字基本区
		return true
	case r >= 0x3400 && r <= 0x4DBF: // 扩展 A
		return true
	case r >= 0xF900 && r <= 0xFAFF: // 兼容汉字
		return true
	case r >= 0x3040 && r <= 0x30FF: // 平假名、片假名
		return true
	case r >= 0xAC00 && r <= 0xD7AF: // 谚文
		return true
	case r >= 0x3000 && r <= 0x303F: // 全角标点
		return true
	}
	return false
}

// hasMultiSpace 判断是否出现连续 2 个及以上空格。
// 单个空格是正常写法，两个以上基本是粘贴或批量重命名带进来的。
func hasMultiSpace(s string) bool {
	run := 0
	for _, r := range s {
		if r != ' ' {
			run = 0
			continue
		}
		run++
		if run >= 2 {
			return true
		}
	}
	return false
}

// hasRepeatedPunct 判断是否出现 3 个及以上相同的非字母数字字符（不含空格）。
func hasRepeatedPunct(s string) bool {
	var prev rune
	run := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			prev, run = 0, 0
			continue
		}
		if r == prev {
			run++
		} else {
			prev, run = r, 1
		}
		if run >= 3 {
			return true
		}
	}
	return false
}

// collapseRepeats 把 3 个及以上相同的非字母数字字符压成一个；两个以内保持原样。
func collapseRepeats(s string) string {
	rs := []rune(s)
	out := make([]rune, 0, len(rs))
	for i := 0; i < len(rs); {
		j := i
		for j < len(rs) && rs[j] == rs[i] {
			j++
		}
		n := j - i
		r := rs[i]
		isPunct := !unicode.IsLetter(r) && !unicode.IsDigit(r)
		if isPunct && (n >= 3 || (r == ' ' && n >= 2)) {
			out = append(out, r)
		} else {
			for k := 0; k < n; k++ {
				out = append(out, r)
			}
		}
		i = j
	}
	return string(out)
}

// isAllowedRune 是字符集白名单：字母、数字、下划线、连字符、点，以及所有中日韩字符。
func isAllowedRune(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	switch r {
	case '_', '-', '.':
		return true
	}
	return isCJK(r)
}

// hasDisallowed 判断是否含白名单外的字符。
func hasDisallowed(s string) bool {
	for _, r := range s {
		if !isAllowedRune(r) {
			return true
		}
	}
	return false
}

// replaceDisallowed 把白名单外的字符替换为下划线。
func replaceDisallowed(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isAllowedRune(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return b.String()
}

// hasNonCJKNonASCII 判断是否含非 ASCII 且非中日韩的字符（例如西里尔、阿拉伯文）。
func hasNonCJKNonASCII(s string) bool {
	for _, r := range s {
		if r <= unicode.MaxASCII || isCJK(r) {
			continue
		}
		return true
	}
	return false
}
