// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package export

import (
	"encoding/csv"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"verify/internal/store"
)

// 导入上限：POST /api/import 的文件大小限制在 HTTP 层做（http.MaxBytesReader），
// 这里再兜一道行数与字节数，防止内存耗尽入口。
const (
	// MaxImportBytes 单次导入的 CSV 字节上限。
	// v3.10.10（第七轮 P0）：16MB 装不下真实规模的 DC5 CSV（17 万行约 25–42MB），
	// 用户点导入只会收到「文件过大」。先提到 128MB 解锁，流式解析另排中期项。
	MaxImportBytes = 128 << 20 // 128MB
	// MaxImportRows 单次导入的行数上限。
	MaxImportRows = 500000
)

// 来源标识，写入 import_session.source。
const (
	SourceDC5      = "dc5"      // Duplicate Cleaner 5 兼容格式
	SourceReadable = "readable" // 本工具通用可读 CSV
)

// ErrTooLarge 行数超限。
var ErrTooLarge = errors.New("CSV 行数超过上限（50 万行），请拆分后导入")

// ParseCSV 把 CSV 字节流解析成统一的导入行，自动识别两种格式：
//   - DC5：表头含 filename + groupno（必需列缺失时报错说明缺哪列）
//   - 通用可读：表头含 Marked + File Name（本工具 WriteReadableCSV 的输出）
//
// 路径匹配不上不是错误：从 Windows 的 DC 导入时路径全是 C:\...，在 NAS 上
// 一个都匹配不到，但这不影响「恢复当初的分组和标记」。matched 由上层查库后回填。
func ParseCSV(data []byte) ([]store.ImportRow, string, []string, error) {
	if len(data) > MaxImportBytes {
		return nil, "", nil, fmt.Errorf("文件超过 128MB 上限，请拆分后导入")
	}
	// encoding/csv 不会自动去 UTF-8 BOM（Excel 另存的文件必带），手工剥。
	payload := string(data)
	payload = strings.TrimPrefix(payload, "\uFEFF")
	r := csv.NewReader(strings.NewReader(payload))
	r.FieldsPerRecord = -1 // 列数容错：允许行内列数不齐，缺的按空处理
	records, err := r.ReadAll()
	if err != nil {
		return nil, "", nil, fmt.Errorf("CSV 解析失败（%v）", err)
	}
	if len(records) < 1 {
		return nil, "", nil, errors.New("文件为空或不是有效的 CSV")
	}
	header := normalizeHeader(records[0])
	var rows []store.ImportRow
	var warnings []string
	switch {
	// 有 groupno 就按 DC5 解析：缺其它必需列时 parseDC5 会逐列点名（含缺 filename 的情况）。
	case hasAll(header, "groupno"):
		rows, warnings, err = parseDC5(header, records)
		if err != nil {
			return nil, "", nil, err
		}
		return rows, SourceDC5, warnings, nil
	// 有 Marked 就按通用可读格式解析：缺 file name 等必需列时 parseReadable 逐列点名。
	case hasAll(header, "marked"):
		rows, warnings, err = parseReadable(header, records)
		if err != nil {
			return nil, "", nil, err
		}
		return rows, SourceReadable, warnings, nil
	default:
		return nil, "", nil, errors.New("无法识别的 CSV 格式：表头需含 filename+groupno（DC5）或 Marked+File Name（通用格式）")
	}
}

// parseDC5 解析 DC5 格式。groupno=0 表示唯一/剩余文件（按单文件组处理）。
func parseDC5(header map[string]int, records [][]string) ([]store.ImportRow, []string, error) {
	// 必需列逐项点名：只差 filename 也要说清楚缺的是哪一列，不要含糊。
	var missing []string
	for _, col := range strings.Split(DC5RequiredColumns, ",") {
		if _, ok := header[col]; !ok {
			missing = append(missing, col)
		}
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("DC5 格式缺少必需列：%s", strings.Join(missing, ", "))
	}
	col := header
	rows := make([]store.ImportRow, 0, len(records)-1)
	var warnings []string
	for i, rec := range records[1:] {
		empty := true
		for _, cell := range rec {
			if strings.TrimSpace(cell) != "" {
				empty = false
				break
			}
		}
		if empty {
			continue
		}
		line := i + 2 // 表头占第 1 行，csv.Reader 的第 i 条记录对应文件第 i+2 行
		size, err := strconv.ParseInt(cell(rec, col["size"]), 10, 64)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("第 %d 行 size 无法解析，按 0 处理", line))
			size = 0
		}
		groupNo, err := strconv.ParseInt(cell(rec, col["groupno"]), 10, 64)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("第 %d 行 groupno 无法解析，按 0（唯一文件）处理", line))
			groupNo = 0
		}
		name := strings.TrimSpace(cell(rec, col["filename"]))
		if name == "" {
			warnings = append(warnings, fmt.Sprintf("第 %d 行 filename 为空，已跳过", line))
			continue
		}
		row := store.ImportRow{
			GroupNo:  groupNo,
			Name:     name,
			Size:     size,
			Hash:     strings.TrimSpace(cell(rec, col["hash"])),
			Chk:      flag(cell(rec, col["chk"])),
			Protect:  flag(cell(rec, col["protect"])),
			Hardlink: clampHardlink(cell(rec, col["hardlink"])),
			Datec:    parseDate(cell(rec, col["datec"])),
			Datem:    parseDate(cell(rec, col["datem"])),
			Duration: parseDuration(cell(rec, col["duration"])),
			Width:    int(parseFloat(cell(rec, col["width"]))),
			Height:   int(parseFloat(cell(rec, col["height"]))),
		}
		row.Path = joinPath(cell(rec, col["folder_name"]), name)
		rows = append(rows, row)
		if len(rows) > MaxImportRows {
			return nil, nil, ErrTooLarge
		}
	}
	return rows, warnings, nil
}

// parseReadable 解析通用可读格式（本工具 WriteReadableCSV 的输出）。
// Folder 列导出时已带尾斜杠，joinPath 会统一处理，不会重复加。
// Marked 列写入 chk（标记状态）；Group 列即 groupno（1 起，0/空=唯一）。
func parseReadable(header map[string]int, records [][]string) ([]store.ImportRow, []string, error) {
	var missing []string
	for _, c := range []string{"file name", "size", "folder"} {
		if _, ok := header[c]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("通用格式缺少必需列：%s", strings.Join(missing, ", "))
	}
	col := header
	rows := make([]store.ImportRow, 0, len(records)-1)
	var warnings []string
	for i, rec := range records[1:] {
		empty := true
		for _, cellv := range rec {
			if strings.TrimSpace(cellv) != "" {
				empty = false
				break
			}
		}
		if empty {
			continue
		}
		line := i + 2
		name := strings.TrimSpace(cell(rec, col["file name"]))
		if name == "" {
			warnings = append(warnings, fmt.Sprintf("第 %d 行 File Name 为空，已跳过", line))
			continue
		}
		size, err := strconv.ParseInt(cell(rec, col["size"]), 10, 64)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("第 %d 行 size 无法解析，按 0 处理", line))
			size = 0
		}
		groupNo, _ := strconv.ParseInt(strings.TrimSpace(cell(rec, col["group"])), 10, 64)
		hl, _ := strconv.ParseInt(strings.TrimSpace(cell(rec, col["hard-links"])), 10, 64)
		row := store.ImportRow{
			GroupNo:  groupNo,
			Name:     name,
			Size:     size,
			Hash:     strings.TrimSpace(cell(rec, col["hash"])),
			Chk:      flag(cell(rec, col["marked"])),
			Hardlink: clampHardlink(strconv.FormatInt(hl, 10)),
			Datec:    parseDate(cell(rec, col["created"])),
			Datem:    parseDate(cell(rec, col["modified"])),
			Width:    int(parseFloat(cell(rec, col["image width"]))),
			Height:   int(parseFloat(cell(rec, col["image height"]))),
		}
		row.Path = joinPath(cell(rec, col["folder"]), name)
		rows = append(rows, row)
		if len(rows) > MaxImportRows {
			return nil, nil, ErrTooLarge
		}
	}
	return rows, warnings, nil
}

// normalizeHeader 把表头统一成小写去空白，方便按名取列。
func normalizeHeader(rec []string) map[string]int {
	m := make(map[string]int, len(rec))
	for i, h := range rec {
		key := strings.ToLower(strings.TrimSpace(h))
		if _, dup := m[key]; !dup {
			m[key] = i
		}
	}
	return m
}

func hasAll(header map[string]int, cols ...string) bool {
	for _, c := range cols {
		if _, ok := header[c]; !ok {
			return false
		}
	}
	return true
}

// cell 取第 idx 列；越界返回空串（FieldsPerRecord=-1 时短行是合法输入）。
func cell(rec []string, idx int) string {
	if idx < 0 || idx >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[idx])
}

// flag 解析标记列：按 1/0 处理，其他非空值一律视为已标记（手册未定义取值）。
func flag(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return false
	}
	return true
}

// clampHardlink 解析硬链接数：非法或 <1 一律按 1。
func clampHardlink(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// parseFloat 解析可选数值列：空/非法按 0。
func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

// joinPath 拼目录与文件名。DC 是 Windows 软件，\ 和 / 都可能出现，统一转 /；
// 目录尾斜杠两种情况都接受，去尾后拼一个 /（Folder 列本身带尾斜杠也不会重复加）。
// 手工编辑过的 CSV 常出现重复分隔符（C:\\Users 或 C://Users），内部折叠成单个；
// UNC 前缀（//server/share）保留，不做折叠。
func joinPath(folder, name string) string {
	folder = strings.ReplaceAll(strings.TrimSpace(folder), `\`, `/`)
	head := ""
	if strings.HasPrefix(folder, "//") {
		head = "//"
		folder = folder[2:]
	}
	for strings.Contains(folder, "//") {
		folder = strings.ReplaceAll(folder, "//", "/")
	}
	folder = head + folder
	folder = strings.TrimSuffix(folder, "/")
	if folder == "" {
		return name
	}
	return folder + "/" + name
}

// dateFormats 覆盖本工具导出格式与 DC5 实测格式；全失败置 0（手册未定义日期格式）。
// DC5 的日期格式随其导出设置可变，实测两种（2026-11-01 用户样本）：
// 「2026/8/20 周四 13:47:51」（本地化星期）与「2026/8/12 10:44」。
var dateFormats = []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02", "2006/1/2 15:04:05", "2006/1/2 15:04", "2006/1/2"}

// weekDayRe 匹配 DC5 本地化日期里的中文星期段（周四/周日/星期四等），解析前剥掉。
var weekDayRe = regexp.MustCompile(`(周|星期)[一二三四五六日天]`)

// parseDate 尝试全部已知格式，全失败返回 0。CSV 无时区信息，按本机时区解释。
func parseDate(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	s = strings.TrimSpace(weekDayRe.ReplaceAllString(s, " "))
	for _, layout := range dateFormats {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UnixNano()
		}
	}
	return 0
}

// parseDuration 解析时长列。DC5 原生导出为毫秒整数（实测样本 140015=140.015 秒）；
// 本工具 v1.10.0 旧导出为小数秒（带小数点，如 61.5）。以「含小数点=秒、纯整数=毫秒」区分。
func parseDuration(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	if strings.Contains(s, ".") {
		return f // 旧格式：小数秒
	}
	return f / 1000 // DC5 原生：毫秒
}
