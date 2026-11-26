package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTableArrayOfObjects 验证 data 是对象数组时渲染成带表头的对齐表格。
func TestTableArrayOfObjects(t *testing.T) {
	raw := json.RawMessage(`[{"id":1,"name":"a.txt","size":100},{"id":2,"name":"b.txt","size":200}]`)
	var buf bytes.Buffer
	if err := writeTableResult(&buf, raw); err != nil {
		t.Fatalf("渲染表格失败：%v", err)
	}
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("期望表头+2 行数据，实际 %d 行：\n%s", len(lines), out)
	}
	// 列名取并集并按字典序：id / name / size
	for _, col := range []string{"id", "name", "size"} {
		if !strings.Contains(lines[0], col) {
			t.Errorf("表头缺列 %s：%q", col, lines[0])
		}
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "200") {
		t.Fatalf("表格丢了单元格数据：\n%s", out)
	}
	// 对齐：两行数据的 name 列应落在同一列起点。
	if strings.Index(lines[1], "a.txt") != strings.Index(lines[2], "b.txt") {
		t.Errorf("列未对齐：\n%s", out)
	}
}

// TestTableNestedArrayField 验证对象里含常见列表字段时渲染该数组，而不是整体 key/value。
func TestTableNestedArrayField(t *testing.T) {
	raw := json.RawMessage(`{"ok":true,"total":2,"groups":[{"key":"g1","count":3},{"key":"g2","count":5}]}`)
	var buf bytes.Buffer
	if err := writeTableResult(&buf, raw); err != nil {
		t.Fatalf("渲染嵌套表格失败：%v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "key") || !strings.Contains(out, "count") {
		t.Fatalf("嵌套数组未渲染成表格：\n%s", out)
	}
	if !strings.Contains(out, "g1") || !strings.Contains(out, "g2") {
		t.Fatalf("嵌套数组数据缺失：\n%s", out)
	}
	// 外壳字段 total 不应出现在表头（我们渲染的是数组，不是外壳）。
	if strings.Contains(strings.SplitN(out, "\n", 2)[0], "total") {
		t.Errorf("表头误把外壳字段当列：%q", strings.SplitN(out, "\n", 2)[0])
	}
}

// TestTableKeyValueObject 验证没有数组字段的对象退回 key/value 两列。
func TestTableKeyValueObject(t *testing.T) {
	raw := json.RawMessage(`{"ok":true,"version":"5.0.0","pid":1234}`)
	var buf bytes.Buffer
	if err := writeTableResult(&buf, raw); err != nil {
		t.Fatalf("渲染 key/value 失败：%v", err)
	}
	out := buf.String()
	for _, want := range []string{"key", "value", "version", "5.0.0", "pid", "1234"} {
		if !strings.Contains(out, want) {
			t.Errorf("key/value 输出缺 %q：\n%s", want, out)
		}
	}
}

// TestTableJSONFallback 验证无法表格化的数据（标量 / 空数组 / 混合数组）退回缩进 JSON。
func TestTableJSONFallback(t *testing.T) {
	cases := map[string]string{
		"标量":   `42`,
		"字符串":  `"hello"`,
		"空数组":  `[]`,
		"混合数组": `[1,"a",{"b":2}]`,
		"null": `null`,
		"布尔标量": `true`,
	}
	for name, raw := range cases {
		var buf bytes.Buffer
		if err := writeTableResult(&buf, json.RawMessage(raw)); err != nil {
			t.Fatalf("%s：渲染失败 %v", name, err)
		}
		if !json.Valid(buf.Bytes()) {
			t.Errorf("%s：回退结果不是合法 JSON：%q", name, buf.String())
		}
	}
}

// TestTableTruncatesLongCell 验证超长单元格被截断成单行，不撑破表格。
func TestTableTruncatesLongCell(t *testing.T) {
	long := strings.Repeat("长", tableCellMaxRunes+20)
	raw, err := json.Marshal([]map[string]any{{"path": long}})
	if err != nil {
		t.Fatalf("构造测试数据失败：%v", err)
	}
	var buf bytes.Buffer
	if err := writeTableResult(&buf, raw); err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 2 {
		t.Fatalf("截断后应仍只有表头+1 行：\n%s", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("超长单元格未加省略号：\n%s", out)
	}
	if len([]rune(out)) > tableCellMaxRunes*2 {
		t.Fatalf("单元格未被有效截断，输出过长（%d runes）", len([]rune(out)))
	}
}

// TestTableFormatWiring 验证 --format table 走表格而不是 JSON 信封，且错误仍是 JSON。
func TestTableFormatWiring(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"items":[{"key":"g1","count":2},{"key":"g2","count":3}]}`))
	}))
	defer srv.Close()

	out, code := runOut(t, "--url", srv.URL, "--format", "table", "oplog")
	if code != 0 {
		t.Fatalf("--format table 退出码=%d；输出=%q", code, out)
	}
	if strings.Contains(out, `"ok"`) || strings.Contains(out, `"data"`) {
		t.Fatalf("--format table 不应输出 JSON 信封：%q", out)
	}
	if !strings.Contains(out, "g1") || !strings.Contains(out, "count") {
		t.Fatalf("--format table 未渲染表格：%q", out)
	}

	// 默认（无 --format）仍是 JSON 信封。
	def, code := runOut(t, "--url", srv.URL, "oplog")
	if code != 0 {
		t.Fatalf("默认格式退出码=%d；输出=%q", code, def)
	}
	if !json.Valid([]byte(def)) || !strings.Contains(def, `"data"`) {
		t.Fatalf("默认格式应是 JSON 信封：%q", def)
	}
}

// TestTableWideCharAlignment 验证中文/全角字符按双宽计算列宽，表格不错位。
func TestTableWideCharAlignment(t *testing.T) {
	raw := json.RawMessage(`[{"name":"电影A","n":1},{"name":"剧集BB","n":2}]`)
	var buf bytes.Buffer
	if err := writeTableResult(&buf, raw); err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("期望表头+2 行，实际 %d 行：\n%s", len(lines), buf.String())
	}
	// 「电影A」显示宽 5、「剧集BB」显示宽 6，故第二行要比第一行多补 1 个空格才对齐 n 列。
	idxA := displayWidth(lines[1][:strings.Index(lines[1], "1")])
	idxB := displayWidth(lines[2][:strings.Index(lines[2], "2")])
	if idxA != idxB {
		t.Fatalf("中文列未按显示宽度对齐：第一行 %d 列，第二行 %d 列\n%s", idxA, idxB, buf.String())
	}
}

// TestDisplayWidth 验证宽字符/窄字符的列宽估算。
func TestDisplayWidth(t *testing.T) {
	cases := map[string]int{
		"abc":   3,
		"电影":    4,
		"电影abc": 7,
		"":      0,
	}
	for in, want := range cases {
		if got := displayWidth(in); got != want {
			t.Errorf("displayWidth(%q)=%d，期望 %d", in, got, want)
		}
	}
}

// TestTableFormatErrorStaysJSON 验证 --format table 下错误仍输出 JSON 错误信封。
func TestTableFormatErrorStaysJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"扫描进行中，请稍后再试"}`))
	}))
	defer srv.Close()

	out, code := runOut(t, "--url", srv.URL, "--format", "table", "status")
	if code != 5 {
		t.Fatalf("409 退出码=%d，期望 5；输出=%q", code, out)
	}
	var env Envelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("错误输出不是 JSON：%v（%q）", err, out)
	}
	if env.OK || env.Error == nil || env.Error.Message != "扫描进行中，请稍后再试" {
		t.Fatalf("错误信封内容错误：%+v", env)
	}
}

// TestTableFormatIgnoredForBinary 验证二进制/导出类响应忽略 --format，仍原样输出。
func TestTableFormatIgnoredForBinary(t *testing.T) {
	const csv = "\ufefftime,action\n1,scan\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(csv))
	}))
	defer srv.Close()

	out, code := runOut(t, "--url", srv.URL, "--format", "table", "trash", "export", "--all=true")
	if code != 0 {
		t.Fatalf("导出类退出码=%d；输出=%q", code, out)
	}
	if out != csv {
		t.Fatalf("导出类响应被 --format 改写：%q", out)
	}
}
