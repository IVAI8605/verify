package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// tableArrayKeys 是对象里「常见承载列表的字段名」的优先顺序。
// 对象命中其中之一且值是对象数组时，就把该数组渲染成表格；都没有才退回 key/value。
var tableArrayKeys = []string{
	"items", "groups", "plans", "batches", "history", "runs", "files",
	"results", "sessions", "peers", "candidates", "jobs", "tasks", "snapshots",
}

// tableCellMaxRunes 单元格最多保留的字符数，超出截断并加省略号，避免宽字段撑破终端。
const tableCellMaxRunes = 48

// tableColumnGap 是列与列之间的固定间隔空格数。
const tableColumnGap = 2

// renderTable 把响应 data（已确认是合法 JSON）渲染成人类可读表格。
// 规则：
//   - 数组对象 → 按并集列名渲染表格；
//   - 对象里含常见数组字段 → 渲染该数组；
//   - 其它对象 → key/value 两列；
//   - 无法表格化（标量、空数组、非对象元素等）→ 退回缩进 JSON。
func renderTable(w io.Writer, data json.RawMessage) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return writeJSONFallback(w, data)
	}
	switch typed := v.(type) {
	case []any:
		if objs, ok := asObjectSlice(typed); ok && len(objs) > 0 {
			return writeObjectTable(w, objs)
		}
		return writeJSONFallback(w, data)
	case map[string]any:
		if arr, ok := findArrayField(typed); ok {
			if objs, ok2 := asObjectSlice(arr); ok2 && len(objs) > 0 {
				return writeObjectTable(w, objs)
			}
		}
		return writeKeyValues(w, typed)
	default:
		return writeJSONFallback(w, data)
	}
}

// asObjectSlice 判断数组是否全是对象，并转成 map 切片；空数组或含非对象元素返回 false。
func asObjectSlice(arr []any) ([]map[string]any, bool) {
	if len(arr) == 0 {
		return nil, false
	}
	objs := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, false
		}
		objs = append(objs, m)
	}
	return objs, true
}

// findArrayField 依次在常见字段名、再在其余字段里找第一个数组值。
func findArrayField(obj map[string]any) ([]any, bool) {
	for _, key := range tableArrayKeys {
		if raw, ok := obj[key]; ok {
			if arr, ok := raw.([]any); ok {
				return arr, true
			}
		}
	}
	// 兜底：按 key 字典序找任意数组字段，保证结果确定。
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if arr, ok := obj[key].([]any); ok {
			return arr, true
		}
	}
	return nil, false
}

// writeObjectTable 把对象数组写成对齐表格：列名取所有对象键的并集（字典序稳定）。
func writeObjectTable(w io.Writer, objs []map[string]any) error {
	colSet := map[string]bool{}
	for _, o := range objs {
		for k := range o {
			colSet[k] = true
		}
	}
	cols := make([]string, 0, len(colSet))
	for k := range colSet {
		cols = append(cols, k)
	}
	sort.Strings(cols)

	rows := make([][]string, 0, len(objs)+1)
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = c
	}
	rows = append(rows, header)
	for _, o := range objs {
		row := make([]string, len(cols))
		for i, c := range cols {
			row[i] = formatCell(o[c])
		}
		rows = append(rows, row)
	}
	return writeAlignedRows(w, rows)
}

// writeKeyValues 把没有列表字段的对象写成「key - value」两列，值非标量时紧凑 JSON 化后截断。
func writeKeyValues(w io.Writer, obj map[string]any) error {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([][]string, 0, len(keys)+1)
	rows = append(rows, []string{"key", "value"})
	for _, k := range keys {
		rows = append(rows, []string{k, formatCell(obj[k])})
	}
	return writeAlignedRows(w, rows)
}

// writeAlignedRows 按「显示宽度」对齐各列后输出。
// 中文/emoji 等双宽字符按 2 列计算，否则中文表格会错位。
// 最后一列不补空格，避免行尾空白。
func writeAlignedRows(w io.Writer, rows [][]string) error {
	if len(rows) == 0 {
		return nil
	}
	widths := make([]int, 0, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			if cw := displayWidth(cell); cw > widths[i] {
				widths[i] = cw
			}
		}
	}

	var buf bytes.Buffer
	for _, row := range rows {
		for i, cell := range row {
			buf.WriteString(cell)
			if i == len(row)-1 {
				continue
			}
			buf.WriteString(strings.Repeat(" ", widths[i]-displayWidth(cell)+tableColumnGap))
		}
		buf.WriteByte('\n')
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// displayWidth 估算字符串在等宽终端里占的列数：东亚宽字符/全角/emoji 记 2，其余记 1。
func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		switch {
		case r == 0:
			continue
		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
			// 组合附加符号不占列。
			continue
		case isWideRune(r):
			width += 2
		default:
			width++
		}
	}
	return width
}

// wideRanges 是常见东亚宽字符/全角/emoji 的码位区间（闭区间）。
// 只覆盖实际会出现在表格里的范围，宁可少算也不误把普通字母当双宽。
var wideRanges = [][2]rune{
	{0x1100, 0x115F},   // 谚文字母
	{0x2E80, 0x303E},   // 中日韩部首、标点
	{0x3041, 0x33FF},   // 平假名、片假名、注音、中日韩兼容
	{0x3400, 0x4DBF},   // 中日韩扩展 A
	{0x4E00, 0x9FFF},   // 中日韩统一表意文字
	{0xA000, 0xA4CF},   // 彝文
	{0xAC00, 0xD7A3},   // 谚文音节
	{0xF900, 0xFAFF},   // 中日韩兼容表意
	{0xFE30, 0xFE6F},   // 中日韩兼容形式、小写变体
	{0xFF00, 0xFF60},   // 全角形式
	{0xFFE0, 0xFFE6},   // 全角符号
	{0x1F300, 0x1F64F}, // 表情符号
	{0x1F900, 0x1F9FF}, // 补充表情符号
	{0x20000, 0x3FFFD}, // 中日韩扩展 B 及以后
}

// isWideRune 判断码位是否属于双宽区间。
func isWideRune(r rune) bool {
	for _, rg := range wideRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// formatCell 把单个 JSON 值转成紧凑、单行的展示文本：标量直出，数组/对象转紧凑 JSON，
// 统一把换行/制表符折成空格并按 rune 截断。
func formatCell(v any) string {
	var s string
	switch x := v.(type) {
	case nil:
		s = ""
	case string:
		s = x
	case bool:
		s = strconv.FormatBool(x)
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			s = strconv.FormatInt(int64(x), 10)
		} else {
			s = strconv.FormatFloat(x, 'g', -1, 64)
		}
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			s = fmt.Sprint(x)
		} else {
			s = string(raw)
		}
	}
	return truncateCell(s)
}

// truncateCell 把单元格收敛成单行并按 rune 截断；ASCII 之外的字符也能安全截。
func truncateCell(s string) string {
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")
	s = replacer.Replace(s)
	runes := []rune(s)
	if len(runes) <= tableCellMaxRunes {
		return s
	}
	return string(runes[:tableCellMaxRunes-1]) + "…"
}

// writeJSONFallback 把原始 JSON 缩进后输出，无法缩进则原样写出，确保不丢数据。
func writeJSONFallback(w io.Writer, data json.RawMessage) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		_, werr := w.Write(data)
		return werr
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

// writeTableResult 是 --format table 的入口：空的或非合法 JSON 原样写出（与信封模式兜底一致），
// 其余交给 renderTable。
func writeTableResult(w io.Writer, raw []byte) error {
	if len(raw) == 0 || !json.Valid(raw) {
		_, err := w.Write(raw)
		return err
	}
	return renderTable(w, json.RawMessage(raw))
}
