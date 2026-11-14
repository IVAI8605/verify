// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeRunner 返回预设的 stdout/stderr 与错误，并记录收到的参数。
type fakeRunner struct {
	stdout string
	stderr string
	err    error
	args   []string
	name   string
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, string, error) {
	f.name = name
	f.args = args
	return f.stdout, f.stderr, f.err
}

// TestProbeVideoHealthy 验证退出码 0 且无关键报错时判为正常。
func TestProbeVideoHealthy(t *testing.T) {
	r := &fakeRunner{}
	if got := ProbeVideo(context.Background(), "/usr/bin/ffmpeg", "", "/v/a.mp4", r.run); got != nil {
		t.Fatalf("正常文件不应报损坏，实际 %+v", got)
	}
	joined := strings.Join(r.args, " ")
	if !strings.Contains(joined, "-t 1") || !strings.Contains(joined, "-f null") {
		t.Fatalf("探测参数不符合约定：%v", r.args)
	}
}

// TestProbeVideoReasons 覆盖各类 stderr 特征到原因的映射。
// 这里刻意不传 ffprobe，验证复核不可用时仍沿用严格判定。
func TestProbeVideoReasons(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		err    error
		want   string
	}{
		{"缺 moov", "moov atom not found", errors.New("exit 1"), ReasonNoMoov},
		{"数据无效", "Invalid data found when processing input", errors.New("exit 1"), ReasonInvalidData},
		{"无法识别编码", "could not find codec parameters for stream 0", errors.New("exit 1"), ReasonNoCodecParams},
		{"截断", "Truncated file", errors.New("exit 1"), ReasonTruncated},
		{"未知错误兜底", "some unknown failure", errors.New("exit 1"), ReasonDecodeFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeRunner{stderr: c.stderr, err: c.err}
			got := ProbeVideo(context.Background(), "ffmpeg", "", "/v/a.mp4", r.run)
			if got == nil || got.Reason != c.want {
				t.Fatalf("原因 %v，期望 %s", got, c.want)
			}
		})
	}
}

// TestProbeVideoIgnoresWarningsOnSuccess 验证退出码 0 时零散告警不再判损坏（v3.9.11 误报修复）。
func TestProbeVideoIgnoresWarningsOnSuccess(t *testing.T) {
	r := &fakeRunner{stderr: "Invalid data found when processing input"}
	got := ProbeVideo(context.Background(), "ffmpeg", "ffprobe", "/v/a.mp4", r.run)
	if got != nil {
		t.Fatalf("能解出首段就应判正常，实际 %+v", got)
	}
}

// TestProbeVideoNoDecoderIsEnvIssue 验证缺解码器不再当成文件损坏。
func TestProbeVideoNoDecoderIsEnvIssue(t *testing.T) {
	r := &fakeRunner{stderr: "Decoder (codec h265) not found for input stream", err: errors.New("exit 1")}
	got := ProbeVideo(context.Background(), "ffmpeg", "ffprobe", "/v/a.mp4", r.run)
	if got != nil {
		t.Fatalf("缺解码器属于环境问题，不应判损坏，实际 %+v", got)
	}
}

// TestProbeVideoReviewedByMidFile 验证首段解码失败但 ffprobe 可读、片中能解码时不报损坏。
func TestProbeVideoReviewedByMidFile(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) (string, string, error) {
		switch {
		case name == "ffprobe":
			return "codec_type=video\nduration=120.0\n", "", nil
		case strings.Contains(strings.Join(args, " "), "-ss"):
			return "", "", nil // 片中位置能解出来
		default:
			return "", "Invalid data found when processing input", errors.New("exit 1")
		}
	}
	if got := ProbeVideo(context.Background(), "ffmpeg", "ffprobe", "/v/a.mp4", run); got != nil {
		t.Fatalf("可播放文件不应报损坏，实际 %+v", got)
	}
}

// TestProbeVideoStillBrokenWhenMidFileFails 验证两次解码都失败时仍保留损坏结论。
func TestProbeVideoStillBrokenWhenMidFileFails(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) (string, string, error) {
		if name == "ffprobe" {
			return "codec_type=video\nduration=120.0\n", "", nil
		}
		return "", "Invalid data found when processing input", errors.New("exit 1")
	}
	got := ProbeVideo(context.Background(), "ffmpeg", "ffprobe", "/v/a.mp4", run)
	if got == nil || got.Reason != ReasonInvalidData {
		t.Fatalf("两次都解不出应判损坏，实际 %+v", got)
	}
}

// TestParseStreamSummary 验证 ffprobe 输出解析：只数音视频流，取最大时长。
func TestParseStreamSummary(t *testing.T) {
	out := "codec_type=video\nduration=N/A\ncodec_type=audio\nduration=90.5\ncodec_type=subtitle\ncodec_type=audio\nduration=89.0\n"
	got := parseStreamSummary(out)
	if got.streams != 3 {
		t.Fatalf("音视频流数量 %d，期望 3", got.streams)
	}
	if got.duration != 90.5 {
		t.Fatalf("时长 %v，期望 90.5", got.duration)
	}
}

// TestProbeVideoTimeout 验证超时被记为"疑似损坏"，且不会无限等待。
func TestProbeVideoTimeout(t *testing.T) {
	blocking := func(ctx context.Context, _ string, _ ...string) (string, string, error) {
		<-ctx.Done()
		return "", "", ctx.Err()
	}
	start := time.Now()
	got := probe(context.Background(), "ffmpeg", "ffprobe", "/v/slow.mp4", blocking, 50*time.Millisecond)
	if got == nil || got.Reason != ReasonTimeout {
		t.Fatalf("应判定为超时，实际 %+v", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("超时控制失效，耗时 %v", elapsed)
	}
}

// TestProbeVideoWithoutFFmpeg 验证没有 ffmpeg 时返回 nil，由调用方跳过。
func TestProbeVideoWithoutFFmpeg(t *testing.T) {
	r := &fakeRunner{}
	if got := ProbeVideo(context.Background(), "", "ffprobe", "/v/a.mp4", r.run); got != nil {
		t.Fatalf("无 ffmpeg 时不应给出结论，实际 %+v", got)
	}
	if r.args != nil {
		t.Fatal("无 ffmpeg 时不应调用外部命令")
	}
}

// TestClassifyProbeFailureEmpty 验证空 stderr 不产生结论。
func TestClassifyProbeFailureEmpty(t *testing.T) {
	if got := ClassifyProbeFailure(""); got != "" {
		t.Fatalf("空 stderr 应返回空原因，实际 %q", got)
	}
}

// TestSummarizeKeepsValidUTF8 验证截断不会切坏多字节字符。
func TestSummarizeKeepsValidUTF8(t *testing.T) {
	long := strings.Repeat("解码失败，", 200)
	got := summarize(long)
	if !utf8.ValidString(got) {
		t.Fatal("截断结果不是合法 UTF-8")
	}
	if len(got) > 300 {
		t.Fatalf("摘要长度 %d 超过上限", len(got))
	}
}

// TestProbeVideoRetriesOnceAfterTimeout 覆盖 v3.12.0 决策 C：
// 首次探测超时后重试一次，第二次成功就判正常；两次都超时才记「疑似损坏」。
func TestProbeVideoRetriesOnceAfterTimeout(t *testing.T) {
	var calls int32
	// 第一次拖到超时，第二次正常返回
	run := func(ctx context.Context, name string, args ...string) (string, string, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			<-ctx.Done()
			return "", "", ctx.Err()
		}
		return "", "", nil
	}
	if got := probe(context.Background(), "ffmpeg", "", "/v/a.mp4", run, 20*time.Millisecond); got != nil {
		t.Fatalf("重试成功应判正常，实际 %+v", got)
	}
	if calls != 2 {
		t.Fatalf("期望调用 2 次，实际 %d", calls)
	}

	// 两次都超时：保留「疑似损坏」
	calls = 0
	slow := func(ctx context.Context, name string, args ...string) (string, string, error) {
		atomic.AddInt32(&calls, 1)
		<-ctx.Done()
		return "", "", ctx.Err()
	}
	got := probe(context.Background(), "ffmpeg", "", "/v/a.mp4", slow, 10*time.Millisecond)
	if got == nil || got.Reason != ReasonTimeout {
		t.Fatalf("两次超时应记疑似损坏，实际 %+v", got)
	}
	if calls != 2 {
		t.Fatalf("期望重试一次共 2 次，实际 %d", calls)
	}
}

// TestReasonLabel 验证所有原因都有中文说明，避免界面露出机器标识。
func TestReasonLabel(t *testing.T) {
	reasons := []string{
		ReasonTimeout, ReasonNoMoov, ReasonInvalidData, ReasonNoDecoder, ReasonNoCodecParams,
		ReasonTruncated, ReasonDecodeFailed, ReasonZeroSize, ReasonAllZero, ReasonUnprintable,
		ReasonEmoji, ReasonSpace, ReasonRepeat, ReasonSpaces, ReasonCharset, ReasonNonCJK,
		ReasonEncoding,
		ReasonMismatch, ReasonUpperExt, ReasonMultiExt,
	}
	for _, r := range reasons {
		if label := ReasonLabel(r); label == r {
			t.Fatalf("%s 缺少中文说明", r)
		}
	}
}
