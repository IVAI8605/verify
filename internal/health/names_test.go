// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import "testing"

// TestCheckNameGluedExt 覆盖 v4.2.0 的「末尾扩展名缺点号」规则。
func TestCheckNameGluedExt(t *testing.T) {
	cases := []struct {
		name    string
		wantBad bool
		want    string
	}{
		// 真实样本：相机导出时把点号吃掉了
		{"VID_20240115_150825_676_545mp4", true, "VID_20240115_150825_676_545.mp4"},
		{"IMG_0001jpg", true, "IMG_0001.jpg"},
		{"录音001m4a", true, "录音001.m4a"},
		{"clip12m2ts", true, "clip12.m2ts"},
		// 已经有扩展名的不判，交给扩展名检测
		{"video.mp4", false, ""},
		{"video.mp4.avi", false, ""},
		// 前面不是数字：人起的名字，不能当缺点号
		{"songmp3", false, ""},
		{"报告pdf", false, ""},
		// 整个名字就是扩展名，补点会得到空主干
		{"mp4", false, ""},
	}
	for _, c := range cases {
		f, bad := CheckName(c.name, NameOptions{})
		if bad != c.wantBad {
			t.Fatalf("%s 判定应为 %v，实际 %v（原因 %v）", c.name, c.wantBad, bad, f.Reasons)
		}
		if c.wantBad && f.Suggested != c.want {
			t.Fatalf("%s 建议名应为 %q，实际 %q", c.name, c.want, f.Suggested)
		}
	}
}

// TestCheckNameGluedExtPreferLongest 确认 .m2ts 不会被 .ts 抢先吃掉。
func TestCheckNameGluedExtPreferLongest(t *testing.T) {
	if got := gluedExt("clip12m2ts"); got != "m2ts" {
		t.Fatalf("应优先匹配最长的 m2ts，实际 %q", got)
	}
	if got := gluedExt("clip12ts"); got != "ts" {
		t.Fatalf("应匹配 ts，实际 %q", got)
	}
}
