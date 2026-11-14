// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package export

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestCrossPathImport 从 Windows DC 导出的 C:\ 路径：解析成功、分隔符统一、分组结构完整。
// 匹配不到当前库不是解析层的错误（matched 由 store 层回填）。
func TestCrossPathImport(t *testing.T) {
	csvData := strings.Join([]string{
		`"filename","groupno","size","datec","datem","chk","protect","hardlink","folder_name","folder_zip_pointer","device_id","device_type","hash","hashquick","duration","width","height"`,
		`"a.jpg","1","100","2024-01-02 03:04:05","2024-01-02 03:04:05","1","0","1","C:\Users\x\pic\","-1","","","abc","q","","",""`,
		`"b.jpg","1","100","2024-01-02 03:04:05","2024-01-02 03:04:05","0","0","1","C:/Users/x/bak//","-1","","","abc","q","","",""`,
		`"unique.jpg","0","50","","","","0","1","C:\Users\x\","-1","","","","","","",""`,
		`"n.jpg","1","10","","","","0","1","\\NAS\share\sub\","-1","","","","","","",""`,
		`"d.jpg","1","100","2024-01-02 03:04:05","2024-01-02 03:04:05","1","0","1","C:\\Users\\x\\pic\\","-1","","","abc","q","","",""`,
	}, "\r\n")
	rows, source, _, err := ParseCSV([]byte(csvData))
	if err != nil {
		t.Fatal(err)
	}
	if source != SourceDC5 {
		t.Fatalf("来源应为 dc5，实际 %s", source)
	}
	if len(rows) != 5 {
		t.Fatalf("应解析出 5 行，实际 %d", len(rows))
	}
	if rows[0].Path != "C:/Users/x/pic/a.jpg" {
		t.Fatalf("反斜杠应统一为 /: %s", rows[0].Path)
	}
	if rows[1].Path != "C:/Users/x/bak/b.jpg" {
		t.Fatalf("正斜杠与重复分隔符应折叠: %s", rows[1].Path)
	}
	if rows[3].Path != "//NAS/share/sub/n.jpg" {
		t.Fatalf("UNC 前缀应保留且内部折叠: %s", rows[3].Path)
	}
	if rows[4].Path != "C:/Users/x/pic/d.jpg" {
		t.Fatalf("双反斜杠应折叠为单斜杠: %s", rows[4].Path)
	}
	if rows[0].GroupNo != 1 || rows[1].GroupNo != 1 {
		t.Fatalf("组号应保持 1: %d / %d", rows[0].GroupNo, rows[1].GroupNo)
	}
	if rows[2].GroupNo != 0 {
		t.Fatalf("groupno=0 应保持 0（唯一文件），实际 %d", rows[2].GroupNo)
	}
	if !rows[0].Chk || rows[1].Chk {
		t.Fatalf("chk 标记不符: %+v", rows[:2])
	}
	// 空日期全失败应置 0，不报错
	if rows[2].Datec != 0 || rows[2].Datem != 0 {
		t.Fatalf("非法日期应置 0")
	}
}

// TestImportBOM Excel 另存的带 BOM 文件能正常读。
func TestImportBOM(t *testing.T) {
	csvData := "\uFEFF" + strings.Join([]string{
		`"Marked","File Name","Folder","Size","Created","Modified","File type","Hash","Group","Hard-links"`,
		`"1","a.mp4","/vol1/media/","100","","","MP4","h1","1","1"`,
	}, "\r\n")
	rows, source, _, err := ParseCSV([]byte(csvData))
	if err != nil {
		t.Fatal(err)
	}
	if source != SourceReadable {
		t.Fatalf("来源应为 readable，实际 %s", source)
	}
	if len(rows) != 1 || rows[0].Path != "/vol1/media/a.mp4" || !rows[0].Chk {
		t.Fatalf("BOM 解析结果不符: %+v", rows)
	}
}

// TestImportRejects 异常输入：二进制垃圾、缺列、空文件、超限。
func TestImportRejects(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		wantErr string
	}{
		{"二进制垃圾", "\x00\x01\x02binary\xFF garbage", "无法识别"},
		{"只有 groupno 没有 filename", `"groupno","size"` + "\r\n" + `"1","100"`, "filename"},
		{"空文件", "", "为空"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, err := ParseCSV([]byte(c.data))
			if err == nil {
				t.Fatalf("应返回错误")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("错误应包含 %q，实际: %v", c.wantErr, err)
			}
		})
	}
}

// TestImportTooLarge：行数上限（50 万行）与字节上限（128MB）同时生效。
// v3.10.10：上限提到 128MB 后，真实 DC5 行（约 60 字节）50 万行约 30MB，
// 行数上限不再必然被字节上限掩盖；这里仍只单独验证字节上限（见 TestParseCSVSizeLimit），
// 行数上限由 TestParseCSVRowsLimit 覆盖。

// TestParseCSVSizeLimit 字节上限。
func TestParseCSVSizeLimit(t *testing.T) {
	big := bytes.Repeat([]byte("x"), MaxImportBytes+1)
	_, _, _, err := ParseCSV(big)
	if err == nil || !strings.Contains(err.Error(), "128MB") {
		t.Fatalf("应报文件超限，实际: %v", err)
	}
}

// TestImportDateFormats 三种日期格式依次尝试。
func TestImportDateFormats(t *testing.T) {
	cases := map[string]bool{
		"2024-01-02 03:04:05": true,
		"2024-01-02 03:04":    true,
		"2024-01-02":          true,
		"garbage":             false,
	}
	for in, ok := range cases {
		csvData := `"filename","groupno","size","datec","datem","chk","protect","hardlink","folder_name","folder_zip_pointer","device_id","device_type"` + "\r\n" +
			`"a.jpg","1","1","` + in + `","` + in + `","0","0","1","/v/","-1","",""`
		rows, _, _, err := ParseCSV([]byte(csvData))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		got := rows[0].Datec != 0
		if got != ok {
			t.Fatalf("日期 %q 解析结果=%v，期望 %v", in, got, ok)
		}
	}
}

// TestImportDC5Dates DC5 实测日期格式（2026-11-01 样本）：本地化星期与斜杠无秒格式。
func TestImportDC5Dates(t *testing.T) {
	want := time.Date(2026, 8, 20, 13, 47, 51, 0, time.Local).UnixNano()
	cases := map[string]int64{
		"2026/8/20 周四 13:47:51":  want, // DC5 本地化格式（带中文星期）
		"2026/8/20 星期四 13:47:51": want, // 星期变体
		"2026/8/12 10:44":        time.Date(2026, 8, 12, 10, 44, 0, 0, time.Local).UnixNano(),
	}
	for in, wantNano := range cases {
		csvData := `"filename","groupno","size","datec","datem","chk","protect","hardlink","folder_name","folder_zip_pointer","device_id","device_type"` + "\r\n" +
			`"a.jpg","1","1","` + in + `","` + in + `","0","0","1","/v/","-1","",""`
		rows, _, _, err := ParseCSV([]byte(csvData))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if rows[0].Datec != wantNano {
			t.Fatalf("日期 %q 解析=%s，期望 %s", in, fmtTime(rows[0].Datec), fmtTime(wantNano))
		}
	}
}

// TestImportDurationUnits 时长列单位：DC5 原生毫秒整数；本工具旧导出小数秒。
func TestImportDurationUnits(t *testing.T) {
	cases := map[string]float64{
		"140015": 140.015, // DC5 原生：毫秒（实测样本）
		"61500":  61.5,
		"61.5":   61.5, // v1.10.0 旧导出：小数秒
		"0":      0,
		"":       0,
		"bad":    0,
	}
	for in, want := range cases {
		csvData := `"filename","groupno","size","datec","datem","chk","protect","hardlink","folder_name","folder_zip_pointer","device_id","device_type","hash","hashquick","duration","width","height"` + "\r\n" +
			`"a.mp4","1","1","","","","0","1","/v/","-1","","","","","` + in + `","",""`
		rows, _, _, err := ParseCSV([]byte(csvData))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if rows[0].Duration != want {
			t.Fatalf("duration %q = %v，期望 %v", in, rows[0].Duration, want)
		}
	}
}

// TestImportIsolatedGroups 仅标记行导出产生的孤组：导入按单文件组处理，不算错误。
func TestImportIsolatedGroups(t *testing.T) {
	csvData := `"filename","groupno","size","datec","datem","chk","protect","hardlink","folder_name","folder_zip_pointer","device_id","device_type"` + "\r\n" +
		`"a.jpg","7","1","","","","1","1","/v/","-1","",""` + "\r\n" +
		`"b.jpg","9","1","","","","0","1","/v/","-1","",""`
	rows, _, warnings, err := ParseCSV([]byte(csvData))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].GroupNo != 7 || rows[1].GroupNo != 9 {
		t.Fatalf("孤组应原样保留: %+v", rows)
	}
	if len(warnings) != 0 {
		t.Fatalf("孤组不应产生告警: %v", warnings)
	}
}
