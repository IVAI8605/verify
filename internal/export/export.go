// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package export

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"verify/internal/store"
)

// DC5RequiredColumns 是 Duplicate Cleaner 5 CSV 的必需列，逐字来自官方手册。
// 测试会与这里逐字比对导出文件的第一行，改坏会挂 CI。
const DC5RequiredColumns = "filename,groupno,size,datec,datem,chk,protect,hardlink,folder_name,folder_zip_pointer,device_id,device_type"

// DC5Header = 12 必需列 + 5 可选列（hash,hashquick,duration,width,height）。
// 「Width/Height 与 Duration 列是否存在」决定 DC 以 Image/Audio/Video 哪种模式加载。
// 实测 DC 原生导出的表头是裸列名（无引号），这里对齐；必须与 DC5RequiredColumns 逐字对齐。
const DC5Header = "filename,groupno,size,datec,datem,chk,protect,hardlink,folder_name,folder_zip_pointer,device_id,device_type,hash,hashquick,duration,width,height"

// Header 是通用可读 CSV 的表头（本仓库历史格式，非 DC 契约）。
// v3.10.8（P2-6）：末尾曾多一个逗号，表头 21 列而数据 20 列，Excel 会多出空列。
const Header = `"Marked","File Name","Folder","Size","Created","Modified","File type","Hash","Group","Hard-links","Artist","Title","Album","Length","Bit rate","Sample rate","Image Width","Image Height","Unused","ZipFlag"`

// Options 控制导出行为，两种格式共用。
type Options struct {
	// Marked 为 nil 时按默认规则生成标记（每组保留第一个、其余标记）；
	// 非 nil 时以集合为准。
	Marked map[string]bool
	// OnlyMarked 为 true 时只导出标记行（对应 DC 的「导出已标记文件」）。
	// 这会让 groupno 出现孤组（某组只导出一个文件），导入端按单文件组处理，不是错误。
	OnlyMarked bool
	// ProtectFn 返回路径是否落在保护文件夹内（v1.9.0 功能）。
	// 由调用方注入（web 层传 Engine.IsProtected），export 包不反向依赖 engine；nil 视为全部未保护。
	ProtectFn func(string) bool
	// PathFn 把 folder_name 的 NAS 路径转换为外部设备路径（SMB/映射盘符，v1.10.3）。
	// 仅 DC5 格式使用；返回 "" 表示未命中映射，保持 NAS 原路径。nil 视为不转换。
	PathFn func(string) string
}

// WriteDC5CSV 输出 Duplicate Cleaner 5 兼容的 CSV（UTF-8 BOM + CRLF）。
//
// 格式以真实 DC v5.23.0 的导出文件为黄金标准对齐（2026-11-01 用户实测样本）：
//   - 引号：最小引用——表头裸列名，字段仅在含逗号/引号/换行时加引号（DC5 原生风格；
//     实测 DC 也能读全字段强引号的旧格式，改弱引用是为了与其自身输出一致）
//   - duration：毫秒整数（实测样本 max=23501444≈6.5h，全部落在视频行；此前写秒数 DC 不识别）
//   - device_id：卷标识（DC5 原生写盘符如 W:\，NAS 上对应写 /vol1）
//   - BOM：DC5 自身不写，但实测它能读带 BOM 的文件；保留 BOM 让 Excel 打开中文不乱码
//
// 兼容性边界（务必如实传达，不许写成「已验证兼容」）：
// 列名与语义按 DC 官方手册对齐，并经真实 DC v5.23 导入实测（用户于 2026-11-01
// 将本工具导出的文件导入 DC 成功）。下列字段是**按列名/样本推断**的：
//   - chk（checked，标记状态）：按 1/0 处理
//   - protect（保护状态）：取 v1.9.0 保护文件夹判定结果
//   - hardlink：取 inode 链接数（DC5 原生导出写 0，写 1 实测可正常导入）
//   - datec/datem 的日期格式手册未说明，输出 "2006-01-02 15:04:05"（实测 DC 可读；
//     DC 自身输出为本地化格式如「2026/8/20 周四 13:47:51」，随其设置可变）
func WriteDC5CSV(w io.Writer, groups []store.Group, opt Options) error {
	bw := bufio.NewWriterSize(w, 64*1024)
	_, _ = bw.WriteString("\xEF\xBB\xBF")
	_, _ = bw.WriteString(DC5Header)
	_, _ = bw.WriteString("\r\n")
	err := walkRows(groups, opt, func(gi int, f store.FileRow, marked bool) error {
		chk, protect := "0", "0"
		if marked {
			chk = "1"
		}
		if opt.ProtectFn != nil && opt.ProtectFn(f.Path) {
			protect = "1"
		}
		folder := filepath.Dir(f.Path)
		if folder != "" && !strings.HasSuffix(folder, "/") {
			folder += "/"
		}
		if opt.PathFn != nil {
			if mapped := opt.PathFn(folder); mapped != "" {
				folder = mapped
			}
		}
		// duration 按 DC5 语义输出毫秒整数；DC5 在 Video 模式下要求每行非空，
		// 空值会导致导入在首个空值行中止并报「CSV 类型/版本不正确」（2026-11-01 实测），
		// 因此未知时长/尺寸一律填 0。
		duration := "0"
		if f.Duration > 0 {
			duration = strconv.FormatInt(int64(f.Duration*1000), 10)
		}
		width, height := "0", "0"
		if f.Width > 0 && f.Height > 0 {
			width = strconv.Itoa(f.Width)
			height = strconv.Itoa(f.Height)
		}
		// DC5 同样不接受空日期（实测空日期行触发同样报错）；创建/修改时间互补回退，
		// 两者皆缺时给固定占位，保证字段永远非空。
		datec, datem := fmtTime(f.Ctime), fmtTime(f.Mtime)
		if datec == "" {
			datec = datem
		}
		if datem == "" {
			datem = datec
		}
		if datec == "" {
			datec, datem = "1970-01-01 00:00:01", "1970-01-01 00:00:01"
		}
		// bufio 的错误是粘性的，这里提前失败可以避免把残缺的 CSV 当作成功导出。
		line := strings.Join([]string{
			dc5Field(f.Name),
			dc5Field(strconv.Itoa(gi + 1)),
			dc5Field(strconv.FormatInt(f.Size, 10)),
			dc5Field(datec),
			dc5Field(datem),
			dc5Field(chk),
			dc5Field(protect),
			dc5Field(strconv.FormatInt(hardLinks(f.Nlink), 10)),
			dc5Field(folder),
			dc5Field("-1"),
			dc5Field(f.Volume),
			dc5Field(""),
			dc5Field(f.Hash),
			dc5Field(f.Quick),
			dc5Field(duration),
			dc5Field(width),
			dc5Field(height),
		}, ",") + "\r\n"
		_, err := bw.WriteString(line)
		return err
	})
	if err != nil {
		return err
	}
	return bw.Flush()
}

// dc5Field 按 DC5 原生风格输出字段：仅在含逗号/引号/换行时加引号并转义内部引号。
func dc5Field(s string) string {
	if strings.ContainsAny(s, ",\"\r\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// WriteReadableCSV 输出通用可读 CSV（UTF-8 BOM + CRLF），给人看 / Excel 分析用。
// 这不是 DC 的导入格式——旧注释里「与 DC 兼容」的说法是错的，别再传下去。
func WriteReadableCSV(w io.Writer, groups []store.Group, opt Options) error {
	bw := bufio.NewWriterSize(w, 64*1024)
	_, _ = bw.WriteString("\xEF\xBB\xBF")
	_, _ = bw.WriteString(Header)
	_, _ = bw.WriteString("\r\n")
	err := walkRows(groups, opt, func(gi int, f store.FileRow, marked bool) error {
		mark := "0"
		if marked {
			mark = "1"
		}
		folder := filepath.Dir(f.Path)
		if folder != "" && !strings.HasSuffix(folder, "/") {
			folder += "/"
		}
		length := ""
		if f.Duration > 0 {
			d := int(f.Duration)
			length = fmt.Sprintf("%d:%02d:%02d", d/3600, (d/60)%60, d%60)
		}
		bitrate := ""
		if f.BitRate > 0 {
			bitrate = fmt.Sprintf("%d", f.BitRate/1000)
		}
		sampleRate := ""
		if f.SampleRate > 0 {
			sampleRate = fmt.Sprintf("%d", f.SampleRate)
		}
		width, height := "", ""
		if f.Width > 0 && f.Height > 0 {
			width = fmt.Sprintf("%d", f.Width)
			height = fmt.Sprintf("%d", f.Height)
		}
		_, err := fmt.Fprintf(bw, `"%s","%s","%s","%d","%s","%s","%s","%s","%d","%d","","","",%s,%s,%s,%s,%s,%s,%s`,
			mark,
			esc(f.Name),
			esc(folder),
			f.Size,
			fmtTime(f.Ctime),
			fmtTime(f.Mtime),
			esc(strings.ToUpper(f.Ext)),
			esc(f.Hash),
			gi+1,
			hardLinks(f.Nlink),
			quoteIf(length),
			quoteIf(bitrate),
			quoteIf(sampleRate),
			quoteIf(width),
			quoteIf(height),
			`"0"`, `"0"`,
		)
		if err != nil {
			return err
		}
		_, _ = bw.WriteString("\r\n")
		return nil
	})
	if err != nil {
		return err
	}
	return bw.Flush()
}

// WriteCSV 是旧入口，等价于 WriteReadableCSV（带旧签名），保留给历史调用方。
func WriteCSV(w io.Writer, groups []store.Group, marked map[string]bool, defaultMark bool) error {
	opt := Options{Marked: marked}
	if defaultMark {
		// Marked 置 nil 走默认规则：每组保留第一个、其余标记。
		opt.Marked = nil
	}
	return WriteReadableCSV(w, groups, opt)
}

// walkRows 遍历分组行并应用标记与范围规则，是两种导出格式共用的行级逻辑。
func walkRows(groups []store.Group, opt Options, fn func(gi int, f store.FileRow, marked bool) error) error {
	for gi, g := range groups {
		for i, f := range g.Files {
			var m bool
			if opt.Marked == nil {
				m = i > 0
			} else {
				m = opt.Marked[f.Path]
			}
			if opt.OnlyMarked && !m {
				continue
			}
			if err := fn(gi, f, m); err != nil {
				return err
			}
		}
	}
	return nil
}

func quoteIf(s string) string {
	if s == "" {
		return `""`
	}
	return `"` + s + `"`
}

func hardLinks(n int64) int64 {
	if n < 1 {
		return 1
	}
	return n
}

func esc(s string) string {
	return strings.ReplaceAll(csvSafe(s), `"`, `""`)
}

// csvSafe 防 CSV 公式注入：文件名/目录名可能以 = + - @ 或制表符开头，
// Excel、WPS、Sheets 打开时会当公式执行；前置单引号让它按纯文本显示。
// 只用于给人看的可读 CSV；DC5 格式要保持文件名原样，否则导入 Duplicate Cleaner 后匹配不上。
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func fmtTime(ns int64) string {
	if ns <= 0 {
		return ""
	}
	return time.Unix(0, ns).Format("2006-01-02 15:04:05")
}
