// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package video

import (
	"bytes"
	"fmt"

	"strings"
	"testing"
	"verify/internal/phash"
)

func TestHWFailureNote(t *testing.T) {
	if note := hwFailureNote("Error initializing qsv device"); note == "" || !strings.Contains(note, "qsv") {
		t.Fatalf("应识别 qsv 失败关键字，实际 %q", note)
	}
	if note := hwFailureNote("No such file or directory"); note != "" {
		t.Fatalf("普通错误不应误判为硬解失败: %q", note)
	}
}

func TestContentWindowAndSampleTimes(t *testing.T) {
	start, end := ContentWindow(15, 10, 2)
	if start != 10 || end != 13 {
		t.Fatalf("正片区间错误: start=%v end=%v", start, end)
	}
	if got := len(SampleTimes(start, end, "low")); got != 3 {
		t.Fatalf("正片区应抽 3 帧，实际 %d", got)
	}

	// 极短视频降级为单帧
	start, end = ContentWindow(2, 0, 0)
	if got := len(SampleTimes(start, end, "low")); got != 1 {
		t.Fatalf("短视频应降级为单帧，实际 %d", got)
	}

	// 忽略区间超过时长时仍需保留可采样窗口
	start, end = ContentWindow(5, 100, 100)
	if end <= start || start < 0 || end > 5 {
		t.Fatalf("区间收敛错误: start=%v end=%v", start, end)
	}
}

func gradientFrame() []byte {
	b := make([]byte, contentFrameBytes)
	for y := 0; y < contentFrameSize; y++ {
		for x := 0; x < contentFrameSize; x++ {
			b[y*contentFrameSize+x] = byte(x*8 + y*4)
		}
	}
	return b
}

func TestFrameSSIMAndMirror(t *testing.T) {
	a := gradientFrame()
	if s := FrameSSIM(a, a); s < 0.99 {
		t.Fatalf("相同帧 SSIM 应接近 1，实际 %v", s)
	}
	if !FramesSimilar([][]byte{a}, [][]byte{mirrorFrame(a)}) {
		t.Fatalf("镜像帧应通过二次校验")
	}
	inv := make([]byte, contentFrameBytes)
	for i := range inv {
		inv[i] = 255 - a[i]
	}
	if FramesSimilar([][]byte{a}, [][]byte{inv}) {
		t.Fatalf("差异明显的帧不应通过二次校验")
	}
}

func TestSampleTimesPositions(t *testing.T) {
	times := SampleTimes(0, 10, "low")
	want := []float64{2, 5, 8}
	if len(times) != len(want) {
		t.Fatalf("帧数错误: %v", times)
	}
	for i := range want {
		if times[i] != want[i] {
			t.Fatalf("抽帧位置错误: got=%v want=%v", times, want)
		}
	}
}

func TestMirrorHashAndHamming(t *testing.T) {
	const h = "0123456789abcdef"
	if d := MinHamming(h, h); d != 0 {
		t.Fatalf("相同哈希距离应为 0，实际 %d", d)
	}
	if d := MinHamming(h, fmt.Sprintf("%016x", phash.MirrorBits(0x0123456789abcdef))); d != 0 {
		t.Fatalf("镜像哈希距离应为 0，实际 %d", d)
	}
}

func TestMirrorFrameKeepsSize(t *testing.T) {
	a := gradientFrame()
	m := mirrorFrame(a)
	if len(m) != contentFrameBytes {
		t.Fatalf("镜像帧尺寸错误: %d", len(m))
	}
	if bytes.Equal(a, m) {
		t.Fatalf("渐变帧镜像后不应完全相同")
	}
}

// TestSampleTimesDensity v1.38 帧密度档位。
func TestSampleTimesDensity(t *testing.T) {
	if got := len(SampleTimes(0, 100, "low")); got != 3 {
		t.Fatalf("low 应 3 帧，实际 %d", got)
	}
	if got := len(SampleTimes(0, 100, "medium")); got != 6 {
		t.Fatalf("medium 应 6 帧，实际 %d", got)
	}
	if got := len(SampleTimes(0, 100, "high")); got != 10 {
		t.Fatalf("high 应 10 帧，实际 %d", got)
	}
	if got := len(SampleTimes(0, 100, "unknown")); got != 6 {
		t.Fatalf("未知档位应按 medium，实际 %d", got)
	}
}
