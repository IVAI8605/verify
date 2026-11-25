// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ProbeTimeout 是单文件探测的硬超时。
// 超过这个时间无法在 1 秒内解出帧，基本可以认为文件有问题或存储异常。
const ProbeTimeout = 10 * time.Second

// 损坏原因的机器标识，落库与筛选都依赖它。
const (
	// ReasonTimeout 探测超时，属于"疑似损坏"。
	ReasonTimeout = "timeout"
	// ReasonNoMoov MP4 缺少 moov 原子，常见于未写入完成或下载中断。
	ReasonNoMoov = "no_moov"
	// ReasonInvalidData 解码过程中报数据无效。
	ReasonInvalidData = "invalid_data"
	// ReasonNoDecoder 缺少对应编码的解码器。
	ReasonNoDecoder = "no_decoder"
	// ReasonNoCodecParams 无法识别编码参数。
	ReasonNoCodecParams = "no_codec_params"
	// ReasonTruncated 文件被截断。
	ReasonTruncated = "truncated"
	// ReasonDecodeFailed 其余解码失败的兜底原因。
	ReasonDecodeFailed = "decode_failed"
)

// CmdRunner 执行外部命令并返回 stdout 与 stderr。
// v3.9.11：ffprobe 的结论写在 stdout，二次复核必须同时取两路输出。
// 抽成函数类型是为了让测试不依赖真实 ffmpeg/ffprobe。
type CmdRunner func(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)

// DefaultRunner 用 os/exec 执行命令。
func DefaultRunner() CmdRunner {
	return func(ctx context.Context, name string, args ...string) (string, string, error) {
		cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- 可执行文件为探测到的 ffmpeg/ffprobe 绝对路径，参数为常量与目标路径
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
}

// BrokenFinding 是一个视频的探测结论。
type BrokenFinding struct {
	// Reason 为空表示文件正常。
	Reason string
	// Detail 保留原始 stderr 摘要，便于用户判断。
	Detail string
}

// ProbeVideo 用 ffmpeg 快速解码前 1 秒，判定文件是否损坏。
//
// v3.9.11 起判定分两步，专门消除「报数据无效/解码失败但实际能播放」的误报：
//  1. 先解前 1 秒。退出码 0 即视为可播放——stderr 里的零散告警（坏帧跳过、
//     时间戳跳变等）不代表整部片子不能用，不再据此报损坏。
//  2. 退出码非零时用 ffprobe 复核：容器能解析且至少有一条音/视频流，再换到
//     片中位置解 1 秒；能解出就说明只是局部坏帧/尾部截断，仍可播放，不报损坏。
//
// 只有容器不可解析、无有效流、或两次解码都失败才保留「损坏」结论。
// 超时按"疑似损坏"记录，不阻塞其它文件；缺解码器属于本机环境问题，不判损坏。
func ProbeVideo(ctx context.Context, ffmpeg, ffprobe, path string, run CmdRunner) *BrokenFinding {
	return probe(ctx, ffmpeg, ffprobe, path, run, ProbeTimeout)
}

// probe 是 ProbeVideo 的内部实现，超时可注入以便测试。
func probe(ctx context.Context, ffmpeg, ffprobe, path string, run CmdRunner, timeout time.Duration) *BrokenFinding {
	if ffmpeg == "" {
		// 没有 ffmpeg 时无法判断，交给调用方决定是否整体跳过本类检测
		return nil
	}
	first := decodeOnce(ctx, ffmpeg, path, 0, run, timeout)
	if first == nil {
		return nil
	}
	if first.Reason == ReasonTimeout {
		// v3.12.0（第十一轮 P2-10 决策 C）：首次超时多为同机其它 ffmpeg 抢 CPU 的偶发超时，
		// 直接记「疑似损坏」会把好文件报坏。重试一次，仍超时才保留结论。
		if ctx.Err() != nil {
			return first
		}
		again := decodeOnce(ctx, ffmpeg, path, 0, run, timeout)
		if again == nil {
			return nil
		}
		if again.Reason == ReasonTimeout {
			return again
		}
		// 重试拿到的是确定性失败（如数据无效），用它继续走后面的复核流程。
		first = again
	}
	// 缺解码器说明探测环境解不了这个编码，文件在别的播放器上未必坏
	if first.Reason == ReasonNoDecoder {
		return nil
	}
	// 没有 ffprobe 就无法复核，沿用严格的旧结论
	if ffprobe == "" {
		return confirmByLenientDecode(ctx, ffmpeg, path, first, run, timeout)
	}
	info, ok := probeStreams(ctx, ffprobe, path, run, timeout)
	if !ok || info.streams == 0 {
		return confirmByLenientDecode(ctx, ffmpeg, path, first, run, timeout)
	}
	// 有些流式封装没有时长：只要容器与编码参数都能解析，就按可播放处理
	if info.duration <= 0 {
		return nil
	}
	// 换到片中位置再解一次，避开反复踩开头同一个坏帧
	start := 0.0
	if info.duration > 4 {
		start = math.Min(info.duration/2, 600)
	} else if info.duration > 1 {
		start = info.duration / 2
	}
	if start > 0 {
		if again := decodeOnce(ctx, ffmpeg, path, start, run, timeout); again == nil {
			return nil
		}
	}
	return confirmByLenientDecode(ctx, ffmpeg, path, first, run, timeout)
}

// confirmByLenientDecode 是 v4.2.0 新增的二次确认（2026-11-25 建议 4）。
//
// 背景：实战中 108 条被判损坏的文件里有 2 条实际能正常播放。严格解码
// （`-v error` 默认 error 级即中止）遇到少量坏帧就整段失败，而播放器会跳过坏帧继续。
// 这里再跑一次「容错解码」：打开解码器的 ignore_err 与丢包选项，
// 只要这段能跑完（退出码 0）就说明文件可播放，不报损坏。
//
// 只对「数据无效 / 解码失败 / 截断」这三类可容忍原因做确认：
// 缺少 moov 原子、无有效流、缺解码器是结构性或环境问题，宽松解码也救不回来，
// 继续走严格结论（或直接不报），不做无意义的第二次 ffmpeg。
func confirmByLenientDecode(ctx context.Context, ffmpeg, path string, strict *BrokenFinding, run CmdRunner, timeout time.Duration) *BrokenFinding {
	if strict == nil {
		return nil
	}
	switch strict.Reason {
	case ReasonInvalidData, ReasonDecodeFailed, ReasonTruncated:
	default:
		return strict
	}
	if ctx.Err() != nil {
		return strict
	}
	if lenientDecodeOnce(ctx, ffmpeg, path, run, timeout) {
		return nil
	}
	return strict
}

// lenientDecodeOnce 用容错参数解 3 秒，返回 true 表示这段能跑完。
//
// 参数说明：
//   - `-err_detect ignore_err`：把解码错误降级为可忽略，播放器同款行为；
//   - `-fflags +discardcorrupt`：丢弃损坏的包而不是中止；
//   - `-xerror` 不开：它会在第一个错误处中止，与容错目标相反。
//
// 3 秒而不是 1 秒：坏帧往往集中在某一段，多解一点能覆盖更多实际情况；
// 超时仍按 ProbeTimeout，不额外放宽。
func lenientDecodeOnce(ctx context.Context, ffmpeg, path string, run CmdRunner, timeout time.Duration) bool {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{
		"-v", "error", "-nostdin",
		"-err_detect", "ignore_err",
		"-fflags", "+discardcorrupt",
		"-i", path, "-t", "3", "-f", "null", "-",
	}
	_, _, err := run(cctx, ffmpeg, args...)
	if err != nil {
		return false
	}
	return cctx.Err() == nil
}

// decodeOnce 解码指定起点后 1 秒；返回 nil 表示这一段能正常解出。
func decodeOnce(ctx context.Context, ffmpeg, path string, start float64, run CmdRunner, timeout time.Duration) *BrokenFinding {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"-v", "error", "-nostdin"}
	if start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
	}
	args = append(args, "-i", path, "-t", "1", "-f", "null", "-")
	_, stderr, err := run(cctx, ffmpeg, args...)

	if errors.Is(cctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &BrokenFinding{Reason: ReasonTimeout, Detail: "探测超过 " + timeout.String()}
	}
	if err == nil {
		// 退出码 0 表示这一段确实解出来了，stderr 的零散告警不改变结论
		return nil
	}
	reason := ClassifyProbeFailure(stderr)
	if reason == "" {
		// 退出码非零但 stderr 没有特征串，仍按损坏处理
		reason = ReasonDecodeFailed
	}
	return &BrokenFinding{Reason: reason, Detail: summarize(stderr)}
}

// streamSummary 是 ffprobe 复核拿到的关键信息。
type streamSummary struct {
	streams  int     // 音/视频流数量，0 表示容器里没有可播放的流
	duration float64 // 时长秒数，部分封装返回 N/A 时为 0
}

// probeStreams 用 ffprobe 验证容器是否可解析、是否含有效音视频流。
// 返回 ok=false 表示 ffprobe 自己都读不了文件，可判定为损坏。
func probeStreams(ctx context.Context, ffprobe, path string, run CmdRunner, timeout time.Duration) (streamSummary, bool) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"-v", "error", "-show_entries", "format=duration:stream=codec_type,duration", "-of", "default=nw=1", path}
	stdout, _, err := run(cctx, ffprobe, args...)
	if err != nil {
		return streamSummary{}, false
	}
	return parseStreamSummary(stdout), true
}

// parseStreamSummary 解析 ffprobe 的 key=value 输出。
// 只统计 audio/video 流：字幕流单独存在并不代表文件能播放。
func parseStreamSummary(stdout string) streamSummary {
	var s streamSummary
	for _, line := range strings.Split(stdout, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "codec_type":
			if val == "audio" || val == "video" {
				s.streams++
			}
		case "duration":
			if sec, err := strconv.ParseFloat(val, 64); err == nil && sec > s.duration {
				s.duration = sec
			}
		}
	}
	return s
}

// ClassifyProbeFailure 把 stderr 归类成固定原因。
// 未命中任何特征时返回空字符串，表示没有可判定的失败信号。
func ClassifyProbeFailure(stderr string) string {
	s := strings.ToLower(stderr)
	if s == "" {
		return ""
	}
	switch {
	case strings.Contains(s, "moov atom not found"):
		return ReasonNoMoov
	case strings.Contains(s, "invalid data"):
		return ReasonInvalidData
	case strings.Contains(s, "decoder (codec") && strings.Contains(s, "not found"):
		return ReasonNoDecoder
	case strings.Contains(s, "could not find codec parameters"):
		return ReasonNoCodecParams
	case strings.Contains(s, "truncated"), strings.Contains(s, "partial file"):
		return ReasonTruncated
	case strings.Contains(s, "error while decoding"), strings.Contains(s, "conversion failed"):
		return ReasonDecodeFailed
	}
	return ""
}

// ReasonLabel 把机器标识转成界面可读的中文。
func ReasonLabel(reason string) string {
	switch reason {
	case ReasonTimeout:
		return "探测超时（疑似损坏）"
	case ReasonNoMoov:
		return "缺少 moov 原子，文件可能未写入完成"
	case ReasonInvalidData:
		return "解码报数据无效"
	case ReasonNoDecoder:
		return "缺少对应解码器"
	case ReasonNoCodecParams:
		return "无法识别编码参数"
	case ReasonTruncated:
		return "文件被截断"
	case ReasonDecodeFailed:
		return "解码失败"
	case ReasonOversized:
		return "过大未检测"
	case ReasonImageBroken:
		return "图片无法解码"
	case ReasonZipBroken:
		return "zip 容器损坏"
	case ReasonJSONBroken:
		return "JSON 语法错误"
	case ReasonXMLBroken:
		return "XML 结构错误"
	case ReasonZeroSize:
		return "文件大小为 0"
	case ReasonAllZero:
		return "内容全为 0"
	case ReasonUnprintable:
		return "内容全为不可打印字符"
	case ReasonEmoji:
		return "含 emoji"
	case ReasonSpace:
		return "首尾有空格"
	case ReasonRepeat:
		return "含重复标点"
	case ReasonSpaces:
		return "含多余空格"
	case ReasonCharset:
		return "含受限字符"
	case ReasonNonCJK:
		return "含非中日韩的特殊字符"
	case ReasonEncoding:
		return "文件名编码异常（非 UTF-8）"
	case ReasonMissingDot:
		return "末尾扩展名缺点号"
	case ReasonMismatch:
		return "扩展名与内容不符"
	case ReasonUpperExt:
		return "扩展名为大写"
	case ReasonMultiExt:
		return "多重扩展名"
	case ReasonFamily:
		return "同族扩展名归一"
	case ReasonUnknown:
		return "内容类型无法识别（仅提示，未做判断）"
	}
	return reason
}

// summarize 截断 stderr，避免把整段输出塞进数据库。
func summarize(stderr string) string {
	s := strings.TrimSpace(stderr)
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	const limit = 300
	if len(s) > limit {
		s = s[:limit]
		// 按字节截断可能切坏多字节字符，回退到最近的合法边界
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
