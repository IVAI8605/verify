// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import "testing"

// TestScanStatsSummaryEmpty 没有任何跳过时摘要必须为空，避免界面出现空括号。
func TestScanStatsSummaryEmpty(t *testing.T) {
	var s scanStats
	s.reset()
	if got := s.summary(1024); got != "" {
		t.Fatalf("无跳过时摘要应为空，得到 %q", got)
	}
}

// TestScanStatsSummaryCoversAllReasons 五类原因都要汇总进同一句中文摘要。
func TestScanStatsSummaryCoversAllReasons(t *testing.T) {
	var s scanStats
	s.reset()
	s.nonRegular.Add(2)
	s.readErrors.Add(1)
	s.smallFiles.Add(3)
	s.hashFailures.Add(1)
	s.excludedDirs.Add(2)

	const want = "跳过 7 个文件、2 个文件夹（排除文件夹 2 个、非普通文件 2 个、读取失败 1 个、小于 1 KB 的文件 3 个、校验失败 1 个）"
	if got := s.summary(1024); got != want {
		t.Fatalf("摘要不符\nwant: %s\n got: %s", want, got)
	}
}

// TestScanStatsSummaryOnlyDirs 只有被排除的文件夹时不能写成「跳过 0 个文件」。
func TestScanStatsSummaryOnlyDirs(t *testing.T) {
	var s scanStats
	s.reset()
	s.excludedDirs.Add(2)
	const want = "跳过 2 个文件夹（排除文件夹 2 个）"
	if got := s.summary(1024); got != want {
		t.Fatalf("摘要不符\nwant: %s\n got: %s", want, got)
	}
}

// TestScanStatsApplyToState 汇总结果要完整写进 State，供前端概览卡读取。
func TestScanStatsApplyToState(t *testing.T) {
	var s scanStats
	s.reset()
	s.addScanned("mp4", 100)
	s.addScanned("mkv", 200)
	s.smallFiles.Add(1)

	var st State
	s.applyTo(&st, 1024)
	if st.ScannedFiles != 2 || st.ScannedBytes != 300 {
		t.Fatalf("已扫描文件/大小不符：files=%d bytes=%d", st.ScannedFiles, st.ScannedBytes)
	}
	if st.Skipped != 1 || st.SkipReason == "" {
		t.Fatalf("跳过统计未写入：skipped=%d reason=%q", st.Skipped, st.SkipReason)
	}
}

// TestScanStatsTypeStats 扩展名分布要按占用排序并聚合大小与数量。
func TestScanStatsTypeStats(t *testing.T) {
	var s scanStats
	s.reset()
	s.addScanned(".MP4", 100)
	s.addScanned("mp4", 50)
	s.addScanned(".mkv", 300)
	s.addScanned("", 10)

	got := s.typeStats()
	want := []TypeStat{
		{Ext: "mkv", Files: 1, Bytes: 300},
		{Ext: "mp4", Files: 2, Bytes: 150},
		{Ext: "", Files: 1, Bytes: 10},
	}
	if len(got) != len(want) {
		t.Fatalf("类型数 = %d，want %d：%+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项 = %+v，want %+v", i, got[i], want[i])
		}
	}
}

// TestFormatScanSize 小于阈值的文案要用人能读的单位。
func TestFormatScanSize(t *testing.T) {
	cases := map[int64]string{
		0:                      "0 B",
		1:                      "1 B",
		1024:                   "1 KB",
		1536:                   "1.5 KB",
		1000000:                "976.6 KB",
		1048576:                "1 MB",
		5 * 1024 * 1024 * 1024: "5 GB",
	}
	for in, want := range cases {
		if got := formatScanSize(in); got != want {
			t.Fatalf("formatScanSize(%d) = %q，want %q", in, got, want)
		}
	}
}
