package engine

import "testing"

// 引擎层接线测试：健康检测判定「文件名含繁体」走的是内嵌词典转换器。
// 大量转换样本（76 条，含词组歧义、重复键、超长词条、非 BMP 字符）在
// internal/engine/data 的 dict_test.go 里覆盖，这里只确认接线与判定口径没变。
//
// 注意：engine 包依赖 syscall.Stat_t / Statfs，Windows 下无法编译，本测试在 Linux CI 执行；
// 本机等价证据见 dict_test.go（可在 Windows 直接跑）。
func TestNameHasTraditional(t *testing.T) {
	cases := []struct {
		in       string
		wantHas  bool
		wantSimp string
	}{
		{"測試視頻.mp4", true, "测试视频.mp4"},
		{"這個視頻.mp4", true, "这个视频.mp4"},
		{"學習資料_第1集.mkv", true, "学习资料_第1集.mkv"},
		{"测试视频.mp4", false, "测试视频.mp4"},
		{"test.mp4", false, "test.mp4"},
		{"abc.MP4", false, "abc.MP4"},
		{"", false, ""},
		// t2s 后无差异的同形字（皇后）不算繁体
		{"皇后.mp4", false, "皇后.mp4"},
		// 非 UTF-8 字节序列：无法可靠转换，一律判为「不含」
		{string([]byte{0xff, 0xfe, '.', 'm', 'p', '4'}), false, string([]byte{0xff, 0xfe, '.', 'm', 'p', '4'})},
	}
	for _, tc := range cases {
		has, simp := NameHasTraditional(tc.in)
		if has != tc.wantHas {
			t.Errorf("NameHasTraditional(%q) 的 has = %t，期望 %t", tc.in, has, tc.wantHas)
		}
		if simp != tc.wantSimp {
			t.Errorf("NameHasTraditional(%q) 的 simp = %q，期望 %q", tc.in, simp, tc.wantSimp)
		}
	}
}
