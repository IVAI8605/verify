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

// TestTableNonTabularObjectFallsBackToJSON 守 C1：没有白名单数组字段的对象必须回退缩进 JSON，
// 不得自造 key/value 表（那会截断 48 rune 以上的值、exit 0 无提示丢数据）。
func TestTableNonTabularObjectFallsBackToJSON(t *testing.T) {
	raw := json.RawMessage(`{"ok":true,"version":"5.0.0","pid":1234}`)
	var buf bytes.Buffer
	if err := writeTableResult(&buf, raw); err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	out := buf.String()
	if strings.Contains(out, "key") && strings.Contains(out, "value") {
		t.Fatalf("非表格化对象不得自造 key/value 表：\n%s", out)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("回退结果不是合法 JSON：%q", out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("回退 JSON 解析失败：%v", err)
	}
	// 所有原始字段必须完整保留（含数值类型与字符串原文）。
	if got["version"] != "5.0.0" {
		t.Errorf("version 丢失或被改写：%#v", got["version"])
	}
	if n, ok := got["pid"].(float64); !ok || n != 1234 {
		t.Errorf("pid 丢失或被改写：%#v", got["pid"])
	}
	if got["ok"] != true {
		t.Errorf("ok 丢失或被改写：%#v", got["ok"])
	}
}

// TestTableNonTabularLongValueNotTruncated 守 C1 的数据完整性：长值在 JSON 回退里一字不少。
func TestTableNonTabularLongValueNotTruncated(t *testing.T) {
	long := strings.Repeat("长", tableCellMaxRunes+200)
	raw, err := json.Marshal(map[string]any{"ok": true, "detail": long})
	if err != nil {
		t.Fatalf("构造测试数据失败：%v", err)
	}
	var buf bytes.Buffer
	if err := writeTableResult(&buf, raw); err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(buf.String(), long) {
		t.Fatalf("非表格化对象的长值被截断了（长度 %d）：\n%s", len([]rune(long)), buf.String())
	}
	if strings.Contains(buf.String(), "…") {
		t.Fatalf("JSON 回退不应出现截断省略号：\n%s", buf.String())
	}
}

// TestTableWhitelistOnlyArrayField 守 I2：只认 tableArrayKeys 白名单，不得按字典序瞎挑数组。
// status 响应里 volumes 是字典序靠前的数组，旧实现会把 state/counts/version 全吞掉。
func TestTableWhitelistOnlyArrayField(t *testing.T) {
	raw := json.RawMessage(`{"ok":true,"state":"idle","version":"5.0.0","counts":{"files":10},"volumes":[{"path":"/vol1"},{"path":"/vol2"}]}`)
	var buf bytes.Buffer
	if err := writeTableResult(&buf, raw); err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	out := buf.String()
	if !json.Valid([]byte(out)) {
		t.Fatalf("白名单外数组应回退 JSON，实际输出：%q", out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("回退 JSON 解析失败：%v", err)
	}
	// 一个字段都不能丢：这是 I2 的实测失败点（910 字节 → 33 字节）。
	for _, key := range []string{"ok", "state", "version", "counts", "volumes"} {
		if _, ok := got[key]; !ok {
			t.Errorf("字段 %s 被吞掉了：%#v", key, got)
		}
	}
	if len(out) < 100 {
		t.Fatalf("输出被压缩到 %d 字节，疑似又按字典序挑了 volumes：%q", len(out), out)
	}
}

// TestTableBigIntegerPrecision 守 I3：int64 级数值（纳秒 mtime、>2^53 大整数）不得变科学计数法或丢精度。
func TestTableBigIntegerPrecision(t *testing.T) {
	const mtime = "1764297600123456789" // ~1.76e18 纳秒
	const bigSize = "9007199254740993"  // 2^53+1，float64 无法精确表示
	raw := json.RawMessage(`[{"path":"/a","mtime":` + mtime + `,"size":` + bigSize + `}]`)
	var buf bytes.Buffer
	if err := writeTableResult(&buf, raw); err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	out := buf.String()
	for _, want := range []string{mtime, bigSize} {
		if !strings.Contains(out, want) {
			t.Errorf("大整数 %s 未原样输出：\n%s", want, out)
		}
	}
	for _, bad := range []string{"e+", "E+", "1.7642976001234568e", "9007199254740992"} {
		if strings.Contains(out, bad) {
			t.Errorf("大整数出现科学计数法/精度丢失（含 %q）：\n%s", bad, out)
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
		_, _ = w.Write([]byte(`{"ok":true,"items":[{"key":"g1","count":2},{"key":"g2","count":3}]}`))
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

// TestTableWideCharAlignment 验证中文/全角字符按双宽计算列宽，后续列不错位。
//
// 刻意用「golden 行文本 + 独立数空格」两种断言，而不用 displayWidth 去量输出：
// 旧版拿 displayWidth 量自己，一旦 displayWidth 退化成 rune 计数，误差两边抵消、
// 测试恒 PASS（空转）。这里数的是字面空格个数，与被测函数无耦合。
func TestTableWideCharAlignment(t *testing.T) {
	raw := json.RawMessage(`[{"name":"电影","size":1},{"name":"abc","size":2}]`)
	var buf bytes.Buffer
	if err := writeTableResult(&buf, raw); err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("期望表头+2 行，实际 %d 行：\n%s", len(lines), out)
	}

	// name 列显示宽 = max("name"=4, "电影"=4, "abc"=3) = 4，加 2 列间隔 → size 起于第 6 列。
	// 「电影」已占 4 列，故只补 2 个空格；「abc」占 3 列，需补 3 个空格。
	// 按 rune 数补空的错误实现会给「电影」补 4 个空格，下面两条 golden 立刻红。
	wantLines := []string{
		"name  size",
		"电影  1",
		"abc   2",
	}
	for i, want := range wantLines {
		if lines[i] != want {
			t.Errorf("第 %d 行不符：得到 %q，期望 %q\n完整输出：\n%s", i, lines[i], want, out)
		}
	}

	// 独立复核：CJK 单元格后必须恰好 2 个空格（不是按 rune 数算出的 4 个）。
	afterCJK := strings.TrimPrefix(lines[1], "电影")
	if pad := len(afterCJK) - len(strings.TrimLeft(afterCJK, " ")); pad != 2 {
		t.Errorf("「电影」后补了 %d 个空格，期望 2（双宽字符应按 2 列计）", pad)
	}
	// 窄字符单元格后必须恰好 3 个空格。
	afterASCII := strings.TrimPrefix(lines[2], "abc")
	if pad := len(afterASCII) - len(strings.TrimLeft(afterASCII, " ")); pad != 3 {
		t.Errorf("「abc」后补了 %d 个空格，期望 3", pad)
	}
}

// TestTableEmptyResponse 守 #7：空响应体补 "{}"，不写 0 字节。
func TestTableEmptyResponse(t *testing.T) {
	var buf bytes.Buffer
	if err := writeTableResult(&buf, nil); err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("空响应不应写出 0 字节")
	}
	if !json.Valid(buf.Bytes()) {
		t.Fatalf("空响应占位不是合法 JSON：%q", buf.String())
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
		_, _ = w.Write([]byte(`{"error":"扫描进行中，请稍后再试"}`))
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
		_, _ = w.Write([]byte(csv))
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

// TestTableListKeysRenderTables 守回归：白名单补齐后，原本被漏的确定性列表键
// （log / tags / entries 等）必须真的出表，而不是静默退回 JSON。
func TestTableListKeysRenderTables(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantHdr []string // 表头应包含的列名
		wantVal string   // 表体应出现的值
	}{
		{
			name:    "trim log → log",
			raw:     `{"log":[{"path":"/a.mp4","offset":10},{"path":"/b.mp4","offset":20}]}`,
			wantHdr: []string{"path", "offset"},
			wantVal: "/b.mp4",
		},
		{
			name:    "video_tags → tags",
			raw:     `{"tags":[{"id":1,"name":"动作"},{"id":2,"name":"喜剧"}]}`,
			wantHdr: []string{"id", "name"},
			wantVal: "喜剧",
		},
		{
			// fs list 同时带 roots（[]string）与 entries（对象数组）：
			// 必须选 entries，且字符串数组 roots 不能抢先命中。
			name:    "fs list → entries（跳过 roots 字符串数组）",
			raw:     `{"path":"/","roots":["/vol1","/vol2"],"entries":[{"name":"a","size":1},{"name":"b","size":2}]}`,
			wantHdr: []string{"name", "size"},
			wantVal: "b",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := writeTableResult(&buf, json.RawMessage(c.raw)); err != nil {
				t.Fatalf("渲染失败：%v", err)
			}
			out := buf.String()
			if json.Valid([]byte(out)) && strings.HasPrefix(strings.TrimSpace(out), "{") {
				t.Fatalf("应出表却退回了 JSON：\n%s", out)
			}
			header := strings.SplitN(out, "\n", 2)[0]
			for _, h := range c.wantHdr {
				if !strings.Contains(header, h) {
					t.Errorf("表头缺列 %q：%q", h, header)
				}
			}
			if !strings.Contains(out, c.wantVal) {
				t.Errorf("表体缺值 %q：\n%s", c.wantVal, out)
			}
		})
	}
}

// TestTableAllWhitelistKeysRecognized 守白名单完整性：每个 tableArrayKeys 里的键，
// 只要值是对象数组就必须被认成表格（防止以后再加键忘了或写错名字）。
func TestTableAllWhitelistKeysRecognized(t *testing.T) {
	raw := `[{"k":"v1"},{"k":"v2"}]`
	for _, key := range tableArrayKeys {
		body := `{"` + key + `":` + raw + `}`
		var buf bytes.Buffer
		if err := writeTableResult(&buf, json.RawMessage(body)); err != nil {
			t.Fatalf("%s：渲染失败 %v", key, err)
		}
		out := buf.String()
		if !strings.Contains(out, "k") || !strings.Contains(out, "v2") {
			t.Errorf("白名单键 %s 未被识别为表格：\n%s", key, out)
		}
	}
}

// TestFindArrayFieldSkipsStringArrays 单测 findArrayField 的加固：
//   - 只有字符串数组（如 roots []string）时，必须返回未命中（跳过），而不是把它当表格；
//     否则 renderTable 会对字符串数组错误地走到 asObjectSlice 失败→JSON 回退前先「假命中」。
//   - 字符串数组与对象数组并存时，取对象数组。
func TestFindArrayFieldSkipsStringArrays(t *testing.T) {
	// 只有字符串数组：白名单键 roots 命中但应被跳过 → 未命中。
	if arr, ok := findArrayField(map[string]any{"roots": []any{"/vol1", "/vol2"}}); ok {
		t.Fatalf("字符串数组不应被当成表格：%#v", arr)
	}
	// 字符串数组 + 对象数组并存：必须取对象数组（entries）。
	obj := map[string]any{
		"roots":   []any{"/vol1", "/vol2"},
		"entries": []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}},
	}
	arr, ok := findArrayField(obj)
	if !ok {
		t.Fatal("应选中 entries 对象数组")
	}
	if objs, ok := asObjectSlice(arr); !ok || len(objs) != 2 {
		t.Fatalf("选中的不是 entries 对象数组：%#v", arr)
	}
}
