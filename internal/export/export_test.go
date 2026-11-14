// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package export

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	"verify/internal/store"
)

// TestWriteCSVMediaFields 验证媒体元数据写入通用可读列。
func TestWriteCSVMediaFields(t *testing.T) {
	groups := []store.Group{{
		Name: "video.mp4",
		Files: []store.FileRow{{
			Name: "video.mp4", Path: "/vol1/video/video.mp4", Size: 1000,
			Ext: "mp4", Duration: 90, MediaMeta: store.MediaMeta{
				Width: 1920, Height: 1080, BitRate: 5000000, SampleRate: 48000,
			},
		}},
	}}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, groups, map[string]bool{}, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`0:01:30`, `"5000"`, `"48000"`, `"1920"`, `"1080"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("CSV 缺少媒体字段 %s:\n%s", want, out)
		}
	}
}

// TestWriteCSVFormulaInjection 覆盖 SEC-4：文件名以 = + - @ 开头时，
// 可读 CSV 里必须前置单引号，避免 Excel/WPS 打开就执行公式。
func TestWriteCSVFormulaInjection(t *testing.T) {
	groups := []store.Group{{
		Name: "=cmd|' /C calc'!A0.mkv",
		Files: []store.FileRow{{
			Name: "=cmd|' /C calc'!A0.mkv", Path: `/vol1/a/=cmd|' /C calc'!A0.mkv`, Size: 10,
		}},
	}}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, groups, map[string]bool{}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"'=cmd`) {
		t.Fatalf("公式前缀未转义:\n%s", buf.String())
	}
}

func csvRows(t *testing.T, data []byte) [][]string {
	t.Helper()
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\uFEFF")))
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func sampleGroups() []store.Group {
	return []store.Group{
		{Key: "h1", Name: "a.mp4", Hash: "hash-1", Files: []store.FileRow{
			{Path: "/vol1/media/a.mp4", Volume: "/vol1", Name: "a.mp4", Size: 100, Ctime: 1700000000e9, Mtime: 1700000100e9, Ext: "mp4", Hash: "hash-1", Quick: "q1", Duration: 61.5, MediaMeta: store.MediaMeta{Width: 640, Height: 480}, Nlink: 1},
			{Path: "/vol2/bak/b.mp4", Volume: "/vol2", Name: "b.mp4", Size: 100, Ctime: 1700000000e9, Mtime: 1700000200e9, Ext: "mp4", Hash: "hash-1", Quick: "q1", Nlink: 2},
			{Path: "/vol1/keep/protected.mp4", Volume: "/vol1", Name: "protected.mp4", Size: 100, Ext: "mp4", Hash: "hash-1"},
		}},
		{Key: "h2", Name: "c.jpg", Hash: "hash-2", Files: []store.FileRow{
			{Path: "/vol1/pic/c.jpg", Volume: "/vol1", Name: "c.jpg", Size: 55, Ext: "jpg", Hash: "hash-2", Quick: "q2"},
			{Path: "/vol1/pic/d.jpg", Volume: "/vol1", Name: "d.jpg", Size: 55, Ext: "jpg", Hash: "hash-2", Quick: "q2"},
		}},
	}
}

// markedSet 模拟用户勾选：每组第二个文件 + 组1 的受保护文件。
func markedSet() map[string]bool {
	return map[string]bool{
		"/vol2/bak/b.mp4":          true,
		"/vol1/keep/protected.mp4": true,
		"/vol1/pic/d.jpg":          true,
	}
}

// TestDC5RequiredColumns 逐字断言 DC5 导出第一行的必需列名（设计文档 6.5：防止以后被人改坏）。
func TestDC5RequiredColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDC5CSV(&buf, sampleGroups(), Options{Marked: markedSet()}); err != nil {
		t.Fatal(err)
	}
	recs := csvRows(t, buf.Bytes())
	if len(recs) < 2 {
		t.Fatalf("导出结果不足两行:\n%s", buf.String())
	}
	got := recs[0]
	want := strings.Split(DC5RequiredColumns, ",")
	if len(got) < len(want) {
		t.Fatalf("DC5 表头列数 %d 少于必需列 %d:\n%v", len(got), len(want), got)
	}
	for i, col := range want {
		if got[i] != col {
			t.Fatalf("DC5 表头第 %d 列 = %q，官方必需列 = %q（整行: %v）", i+1, got[i], col, got)
		}
	}
}

// TestDC5RoundTrip 导出 DC5 → 原样导入：组数、每组文件数、chk/protect 标记必须一致。
func TestDC5RoundTrip(t *testing.T) {
	groups := sampleGroups()
	marked := markedSet()
	var buf bytes.Buffer
	protect := func(p string) bool { return p == "/vol1/keep/protected.mp4" }
	if err := WriteDC5CSV(&buf, groups, Options{Marked: marked, ProtectFn: protect}); err != nil {
		t.Fatal(err)
	}
	rows, source, _, err := ParseCSV(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if source != SourceDC5 {
		t.Fatalf("来源应为 dc5，实际 %s", source)
	}
	if len(rows) != 5 {
		t.Fatalf("应解析出 5 行，实际 %d", len(rows))
	}
	// 按组聚合比对
	byGroup := map[int64][]store.ImportRow{}
	for _, r := range rows {
		byGroup[r.GroupNo] = append(byGroup[r.GroupNo], r)
	}
	if len(byGroup) != 2 {
		t.Fatalf("应有 2 个组号，实际 %d（%v）", len(byGroup), byGroup)
	}
	if len(byGroup[1]) != 3 || len(byGroup[2]) != 2 {
		t.Fatalf("每组文件数不符: 组1=%d 组2=%d", len(byGroup[1]), len(byGroup[2]))
	}
	// chk 与路径重建
	for _, r := range rows {
		wantChk := marked[r.Path]
		if r.Chk != wantChk {
			t.Fatalf("%s chk=%v，期望 %v", r.Path, r.Chk, wantChk)
		}
		wantProtect := protect(r.Path)
		if r.Protect != wantProtect {
			t.Fatalf("%s protect=%v，期望 %v", r.Path, r.Protect, wantProtect)
		}
	}
	// 日期字段往返（秒精度：导出即丢毫秒；CSV 无时区信息，按本机时区解释，
	// 断言用「导出前后的格式化字符串一致」而不是硬编码时刻，保证时区无关）
	got0 := byGroup[1][0]
	if fmtTime(got0.Datec) != fmtTime(1700000000e9) {
		t.Fatalf("datec 往返不符: %s，期望 %s", fmtTime(got0.Datec), fmtTime(1700000000e9))
	}
	// duration 往返：导出毫秒整数 → 导入 /1000 还原秒
	if got0.Path != "/vol1/media/a.mp4" || got0.Hash != "hash-1" || got0.Duration != 61.5 || got0.Width != 640 || got0.Height != 480 {
		t.Fatalf("字段往返不符: %+v", got0)
	}
	// hardlink 往返
	if byGroup[1][1].Hardlink != 2 {
		t.Fatalf("hardlink 应为 2，实际 %d", byGroup[1][1].Hardlink)
	}
}

// TestDC5NativeFormat 对齐 DC5 原生输出风格的格式断言：
// 裸表头、最小引用（含逗号/引号字段才加引号）、duration 毫秒、device_id 写卷。
func TestDC5NativeFormat(t *testing.T) {
	groups := []store.Group{{Key: "k", Name: "g", Files: []store.FileRow{
		{Path: "/vol1/a.mp4", Volume: "/vol1", Name: "plain.mp4", Size: 1, Duration: 61.5},
		{Path: "/vol1/b.mp4", Volume: "/vol1", Name: "has,comma.mp4", Size: 2},
		{Path: "/vol1/c.mp4", Volume: "/vol1", Name: "has\"quote.mp4", Size: 3},
	}}}
	var buf bytes.Buffer
	if err := WriteDC5CSV(&buf, groups, Options{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "\uFEFFfilename,groupno,size,") {
		t.Fatalf("表头应为裸列名（仅 BOM 前缀）:\n%s", out[:120])
	}
	lines := strings.Split(strings.TrimPrefix(out, "\uFEFF"), "\r\n")
	if lines[0] != DC5Header {
		t.Fatalf("表头应与常量逐字一致")
	}
	// 普通字段不加引号；duration=61.5s → 61500ms；device_id=/vol1（在 folder_zip_pointer=-1 之后）
	if !strings.Contains(lines[1], ",61500,") || !strings.Contains(lines[1], ",-1,/vol1,") {
		t.Fatalf("常规行应为弱引用且 duration 毫秒/device_id 卷:\n%s", lines[1])
	}
	if !strings.Contains(lines[1], "plain.mp4,1,1,") {
		t.Fatalf("常规行文件名不应带引号:\n%s", lines[1])
	}
	// 含逗号/引号的字段必须加引号并转义
	if !strings.Contains(lines[2], `"has,comma.mp4"`) {
		t.Fatalf("含逗号字段应加引号:\n%s", lines[2])
	}
	if !strings.Contains(lines[3], `"has""quote.mp4"`) {
		t.Fatalf("含引号字段应加引号并转义:\n%s", lines[3])
	}
}

// TestDC5NoEmptyOptional DC5 Video 模式要求 duration/width/height 与日期列每行非空
// （2026-11-01 实测：空值行导致导入在首个空值行中止并报「CSV 类型/版本不正确」）。
func TestDC5NoEmptyOptional(t *testing.T) {
	groups := []store.Group{{Key: "k", Name: "g", Files: []store.FileRow{
		{Path: "/vol1/a.txt", Volume: "/vol1", Name: "a.txt", Size: 1}, // 无日期、无媒体信息
	}}}
	var buf bytes.Buffer
	if err := WriteDC5CSV(&buf, groups, Options{}); err != nil {
		t.Fatal(err)
	}
	rows := csvRows(t, buf.Bytes())
	if len(rows) < 2 {
		t.Fatal("应有数据行")
	}
	// 列序：0 filename,1 groupno,2 size,3 datec,4 datem,5 chk,6 protect,7 hardlink,
	// 8 folder_name,9 zip,10 device_id,11 device_type,12 hash,13 hashquick,14 duration,15 width,16 height
	for _, col := range []int{3, 4, 14, 15, 16} {
		if rows[1][col] == "" {
			t.Fatalf("第 %d 列不应为空（DC5 Video 模式空值即导入失败）: %v", col, rows[1])
		}
	}
	if rows[1][14] != "0" || rows[1][15] != "0" || rows[1][16] != "0" {
		t.Fatalf("未知媒体信息应填 0: %v", rows[1])
	}
	if rows[1][3] != "1970-01-01 00:00:01" {
		t.Fatalf("双缺失日期应回退占位值: %v", rows[1])
	}
}

// TestDC5PathFn 路径映射仅作用于 DC5 格式的 folder_name（v1.10.3）。
func TestDC5PathFn(t *testing.T) {
	groups := []store.Group{{Key: "k", Name: "g", Files: []store.FileRow{
		{Path: "/vol1/media/abc/x.mp4", Volume: "/vol1", Name: "x.mp4", Size: 1},
	}}}
	pathFn := func(folder string) string {
		// Windows 下 filepath.Dir 返回 \ 分隔（CI 在 linux 无此问题），先归一再匹配
		folder = strings.ReplaceAll(folder, "\\", "/")
		if strings.HasPrefix(folder, "/vol1/media/") {
			rest := strings.ReplaceAll(strings.TrimPrefix(folder, "/vol1/media/"), "/", "\\")
			return `\\NAS\media\` + rest
		}
		return ""
	}
	var buf bytes.Buffer
	if err := WriteDC5CSV(&buf, groups, Options{PathFn: pathFn}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `\\NAS\media\abc\`) {
		t.Fatalf("DC5 导出应应用路径映射:\n%s", buf.String())
	}
	// readable 格式不受 PathFn 影响
	buf.Reset()
	if err := WriteReadableCSV(&buf, groups, Options{PathFn: pathFn}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `\\NAS`) {
		t.Fatalf("通用可读格式不应应用路径映射:\n%s", buf.String())
	}
}

// TestDC5OnlyMarked 仅标记行导出：行数只剩标记行，groupno 保持原组号（孤组合法）。
func TestDC5OnlyMarked(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDC5CSV(&buf, sampleGroups(), Options{Marked: markedSet(), OnlyMarked: true}); err != nil {
		t.Fatal(err)
	}
	rows, _, _, err := ParseCSV(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("仅标记行应导出 3 行，实际 %d:\n%s", len(rows), buf.String())
	}
	counts := map[int64]int{}
	for _, r := range rows {
		if !r.Chk {
			t.Fatalf("仅标记行导出的行 chk 应全为 1: %s", r.Path)
		}
		counts[r.GroupNo]++
	}
	if counts[1] != 2 || counts[2] != 1 {
		t.Fatalf("组号分布不符: %v", counts)
	}
}

// TestReadableRoundTrip 通用可读格式导出 → 导入，标记一致。
func TestReadableRoundTrip(t *testing.T) {
	marked := markedSet()
	var buf bytes.Buffer
	if err := WriteCSV(&buf, sampleGroups(), marked, false); err != nil {
		t.Fatal(err)
	}
	rows, source, _, err := ParseCSV(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if source != SourceReadable {
		t.Fatalf("来源应为 readable，实际 %s", source)
	}
	for _, r := range rows {
		if r.Chk != marked[r.Path] {
			t.Fatalf("%s chk=%v，期望 %v", r.Path, r.Chk, marked[r.Path])
		}
		if r.GroupNo == 0 {
			t.Fatalf("%s 的组号不应为 0", r.Path)
		}
	}
}

// TestReadableCSVHeaderMatchesRows v3.10.8（P2-6）：
// 通用 CSV 表头曾多一个尾逗号，21 列 vs 数据 20 列。
func TestReadableCSVHeaderMatchesRows(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteReadableCSV(&buf, sampleGroups(), Options{Marked: markedSet()}); err != nil {
		t.Fatal(err)
	}
	data := bytes.TrimPrefix(buf.Bytes(), []byte("\xEF\xBB\xBF"))
	recs, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) < 2 {
		t.Fatalf("至少应有表头与一行数据，实际 %d 行", len(recs))
	}
	if len(recs[0]) != len(recs[1]) {
		t.Fatalf("表头 %d 列与数据 %d 列不一致", len(recs[0]), len(recs[1]))
	}
}

// TestDC5DefaultMark 不传 marked 时按默认规则（每组第一个之外都标记）。
func TestDC5DefaultMark(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDC5CSV(&buf, sampleGroups(), Options{}); err != nil {
		t.Fatal(err)
	}
	rows, _, _, err := ParseCSV(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	byGroup := map[int64][]store.ImportRow{}
	for _, r := range rows {
		byGroup[r.GroupNo] = append(byGroup[r.GroupNo], r)
	}
	for g, rs := range byGroup {
		for i, r := range rs {
			want := i > 0
			if r.Chk != want {
				t.Fatalf("组 %d 第 %d 个文件 chk=%v，期望 %v", g, i, r.Chk, want)
			}
		}
	}
}
