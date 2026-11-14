// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package video

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"verify/internal/phash"
)

// AlgoVersion 标记视频指纹算法版本；采样区间或元数据采集变化后需让旧缓存失效并重新分析。
// AlgoVersion 4（v1.38）：帧密度档位化，旧指纹全清重建一次。
const AlgoVersion = 4

// 短视频降级阈值与二次校验帧尺寸。
const (
	shortVideoSeconds   = 3.0
	contentFrameSize    = 16
	contentFrameBytes   = contentFrameSize * contentFrameSize
	verifySSIMThreshold = 0.55
)

// Runner 封装 ffmpeg / ffprobe。
type Runner struct {
	FFmpeg  string
	FFprobe string

	// UseHW 表示是否优先尝试核显解码（QSV）；失败自动回退软解。
	UseHW bool
	// HWMode 为探测到的硬件解码方式，目前支持 "qsv"，空表示不可用。
	HWMode string
	// RenderNode 为探测到的 /dev/dri/renderD128 等渲染节点。
	RenderNode string
}

func NewRunner(ffmpeg, ffprobe string) *Runner {
	return &Runner{FFmpeg: ffmpeg, FFprobe: ffprobe}
}

// Resolve 自动探测可用的 ffmpeg / ffprobe，优先使用系统安装版本。
func (r *Runner) Resolve(appDest string) {
	if r.FFmpeg == "" {
		r.FFmpeg = findBin("ffmpeg", appDest)
	}
	if r.FFprobe == "" {
		r.FFprobe = findBin("ffprobe", appDest)
	}
	r.DetectHW()
}

// DetectHW 探测核显（Intel QSV）解码能力。
func (r *Runner) DetectHW() {
	r.HWMode = ""
	r.RenderNode = ""
	for _, n := range []string{"/dev/dri/renderD128", "/dev/dri/renderD129"} {
		if st, err := os.Stat(n); err == nil && !st.IsDir() {
			r.RenderNode = n
			break
		}
	}
	if r.FFmpeg == "" || r.RenderNode == "" {
		return
	}
	out, err := exec.Command(r.FFmpeg, "-hide_banner", "-hwaccels").Output() // #nosec G204 -- 可执行文件为探测到的 ffmpeg 绝对路径，参数为常量
	if err != nil {
		return
	}
	if strings.Contains(string(out), "qsv") {
		r.HWMode = "qsv"
	}
}

func findBin(name, appDest string) string {
	candidates := []string{}
	// v1.30.1：统一 VERIFY_ 前缀；DUP_CLEAN_ 为改名前的旧前缀，继续兼容
	if env := os.Getenv("VERIFY_" + strings.ToUpper(name)); env != "" {
		candidates = append(candidates, env)
	} else if env := os.Getenv("DUP_CLEAN_" + strings.ToUpper(name)); env != "" {
		candidates = append(candidates, env)
	}
	// 优先使用飞牛系统自带（动态链接）的 ffmpeg，便于继承驱动与核显配置
	candidates = append(candidates, "/usr/bin/"+name, "/usr/local/bin/"+name, "/usr/share/verify/"+name)
	if appDest != "" {
		candidates = append(candidates, filepath.Join(appDest, "bin", name))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), name))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() { // #nosec G703 -- 候选路径来自环境变量与固定目录，不经过 HTTP 请求
			return c
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// Available 是否具备视频分析能力。
func (r *Runner) Available() bool {
	return r.FFmpeg != "" && r.FFprobe != ""
}

// maxFullAudioSeconds 是「全片」模式下单次提取的时长上限（30 分钟）。
// v3.17.5（F-13）：整段 PCM 会随片长线性吃内存（2 小时 8kHz 单声道约 115MB），
// 多个分析 worker 并发就会把 NAS 内存打满；30 分钟约 29MB，主体内容足够。
const maxFullAudioSeconds = 1800

func (r *Runner) ExtractAudio(ctx context.Context, path string, start float64, seconds int) ([]int16, error) {
	if r.FFmpeg == "" {
		return nil, fmt.Errorf("未找到 ffmpeg")
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if seconds <= 0 {
		// seconds=0 表示全片（v2.3.0）：不能传 -t 0，但也不能真的读完整片。
		seconds = maxFullAudioSeconds
	}
	args := []string{"-v", "error", "-nostdin"}
	if start > 0 {
		// 输入侧 seek 可跳过共享片头，避免只比较开头音频
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
	}
	args = append(args, "-t", strconv.Itoa(seconds))
	args = append(args, "-i", path,
		"-vn", "-ac", "1", "-ar", "8000", "-f", "s16le", "-acodec", "pcm_s16le", "pipe:1")
	cmd := exec.CommandContext(cctx, r.FFmpeg, args...) // #nosec G204 -- 可执行文件为探测到的 ffmpeg 绝对路径，参数由常量与数值格式化生成
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stdout.Len() == 0 {
			logFFmpegStderr("提取音频", path, stderr.String())
			return nil, fmt.Errorf("提取音频失败：%s", friendlyFFmpegError(stderr.String()))
		}
	}
	raw := stdout.Bytes()
	if len(raw) < 2 {
		return nil, fmt.Errorf("音频为空")
	}
	samples := make([]int16, len(raw)/2)
	for i := range samples {
		samples[i] = int16(raw[2*i]) | int16(raw[2*i+1])<<8
	}
	return samples, nil
}

// ContentWindow 计算正片采样区间；片头/片尾忽略值按实际时长收敛。
func ContentWindow(duration float64, ignoreIntro, ignoreOutro int) (start, end float64) {
	if duration <= 0 {
		return 0, 0
	}
	start = float64(ignoreIntro)
	if start < 0 {
		start = 0
	}
	if start > duration-1 {
		start = math.Max(0, duration-1)
	}
	end = duration - float64(ignoreOutro)
	if end <= start {
		end = start + 1
	}
	if end > duration {
		end = duration
	}
	if end-start < 1 && duration > 1 {
		start = math.Max(0, duration-1)
		end = duration
	}
	return start, end
}

// SampleTimes 返回正片区间内的抽帧时间；不足 3 秒时降级为单帧。
// density 为 v1.38 F2 帧密度档位："low"=3 帧（v1.37 前行为）、"medium"=6 帧、
// "high"=10 帧（全片均匀分散，含首尾附近）；其余值按 medium。
// MinHamming 对不同帧数天然兼容（取全部配对最小汉明距离），档位切换无需清缓存。
func SampleTimes(start, end float64, density string) []float64 {
	span := end - start
	if span <= 0 {
		return []float64{start}
	}
	if span < shortVideoSeconds {
		return []float64{start + span/2}
	}
	var fracs []float64
	switch density {
	case "low":
		fracs = []float64{0.2, 0.5, 0.8}
	case "high":
		fracs = []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.95}
	default: // medium
		fracs = []float64{0.1, 0.26, 0.42, 0.58, 0.74, 0.9}
	}
	out := make([]float64, 0, len(fracs))
	for _, frac := range fracs {
		out = append(out, start+span*frac)
	}
	return out
}

// IntroFingerprint 提取开头 seconds 秒的音频指纹、帧 dHash 以及媒体元数据。
// 用于“相同片头”检测：比较区间固定为前 N 秒，画面取 N 秒内 20%/50%/80% 处。
func (r *Runner) IntroFingerprint(ctx context.Context, path string, seconds int) (*FileFingerprint, error) {
	if seconds < 1 {
		seconds = 1
	}
	if seconds > 300 {
		seconds = 300
	}
	info, err := r.ProbeInfo(ctx, path)
	if err != nil {
		return nil, err
	}
	samples, aerr := r.ExtractAudio(ctx, path, 0, seconds)
	var audio []byte
	if aerr == nil {
		audio = AudioFingerprint(samples, 8000)
	}
	span := float64(seconds)
	if info.Duration > 0 && info.Duration < span {
		span = info.Duration
	}
	hashes := make([]string, 0, 3)
	for _, t := range SampleTimes(0, span, "low") {
		if t < 0.05 {
			t = 0.05
		}
		if info.Duration > 0 && t > info.Duration-0.2 {
			t = info.Duration - 0.2
		}
		if t < 0.05 {
			t = 0.05
		}
		h, ferr := r.frameHash(ctx, path, t)
		if ferr == nil {
			hashes = append(hashes, hex.EncodeToString(encodeU64(h)))
		}
	}
	if aerr != nil && len(hashes) == 0 {
		return nil, fmt.Errorf("无法解析视频: %v", aerr)
	}
	if len(hashes) == 0 {
		return nil, fmt.Errorf("无法提取画面帧")
	}
	return &FileFingerprint{Audio: audio, Thumbs: strings.Join(hashes, ","), Media: info}, nil
}

// ThumbsAllClose 判断两组缩略图是否逐帧都足够接近（用于识别整段重复）。
// 与 MinHamming 不同，这里要求每一帧都匹配，避免“只有片头帧相同”被误判为整段重复。
func ThumbsAllClose(a, b string, maxHamb int) bool {
	ha, hb := ParseThumbs(a), ParseThumbs(b)
	n := len(ha)
	if len(hb) < n {
		n = len(hb)
	}
	if n == 0 {
		return false
	}
	for i := 0; i < n; i++ {
		if phash.Hamming(ha[i], hb[i]) > maxHamb {
			return false
		}
	}
	return true
}

// ThumbsMostlyClose 判断两组缩略图是否「多数帧一致」（v3.10.10，P1-1）。
// 无音轨的配对没有音频这第二重证据；若沿用 MinHamming（任意一帧命中即可），
// 只有片头/台标相同的两个视频就会被并成一组。这里要求命中帧数达到
// max(2, ⌈2n/3⌉)；只有一帧可比时退化为该帧匹配（与旧行为一致）。
func ThumbsMostlyClose(a, b string, maxHamb int) bool {
	ha, hb := ParseThumbs(a), ParseThumbs(b)
	n := len(ha)
	if len(hb) < n {
		n = len(hb)
	}
	if n == 0 {
		return false
	}
	hit := 0
	for i := 0; i < n; i++ {
		d := phash.Hamming(ha[i], hb[i])
		if md := phash.Hamming(ha[i], phash.MirrorBits(hb[i])); md < d {
			d = md
		}
		if d <= maxHamb {
			hit++
		}
	}
	if n == 1 {
		return hit == 1
	}
	need := (2*n + 2) / 3
	if need < 2 {
		need = 2
	}
	return hit >= need
}

// Fingerprint 提取正片区间的音频指纹、dHash 以及媒体元数据。
// ignoreIntro/ignoreOutro 让采样避开共享片头片尾；短视频自动降级为单帧。
// FingerprintAudio 提取音频片段并计算频谱指纹（纯音频查重与视频音轨共用）。
// 返回 (nil, nil) 表示音频过短（不足 AudioFingerprint 最低采样要求，约 0.5 秒）
// 或基本静音——对无声视频这是正常情况，调用方按无指纹处理，不算错误。
func (r *Runner) FingerprintAudio(ctx context.Context, path string, start float64, seconds int) ([]byte, error) {
	samples, err := r.ExtractAudio(ctx, path, start, seconds)
	if err != nil {
		return nil, err
	}
	return AudioFingerprint(samples, 8000), nil
}

func (r *Runner) Fingerprint(ctx context.Context, path string, ignoreIntro, ignoreOutro int, density string) (*FileFingerprint, error) {
	info, err := r.ProbeInfo(ctx, path)
	if err != nil {
		return nil, err
	}
	start, end := ContentWindow(info.Duration, ignoreIntro, ignoreOutro)
	audio, aerr := r.FingerprintAudio(ctx, path, start, 5)
	hashes := make([]string, 0, 3)
	var lastFrameErr error
	for _, t := range SampleTimes(start, end, density) {
		h, ferr := r.frameHash(ctx, path, t)
		if ferr != nil {
			lastFrameErr = ferr
			continue
		}
		hashes = append(hashes, hex.EncodeToString(encodeU64(h)))
	}
	if aerr != nil && len(hashes) == 0 {
		return nil, fmt.Errorf("无法解析视频: %v", aerr)
	}
	if len(hashes) == 0 {
		// 音频可解但画面完全抽不出时按无法解码处理，进入失败列表等待重试。
		return nil, fmt.Errorf("无法提取画面帧: %v", lastFrameErr)
	}
	return &FileFingerprint{Audio: audio, Thumbs: strings.Join(hashes, ","), Media: info}, nil
}

func (r *Runner) frameHash(ctx context.Context, path string, at float64) (uint64, error) {
	b, err := r.runFrame(ctx, path, at, "scale=9:8,format=gray", "rawvideo")
	if err != nil {
		return 0, err
	}
	if len(b) < 72 {
		return 0, fmt.Errorf("帧数据不完整")
	}
	return phash.DHash(b[:72]), nil
}

// runFrame 抽取单帧；核显失败时显式以 -hwaccel none 重试一次软解。
// frameAttempt 描述一次抽帧尝试：是否用核显、是否只取真实视频流。
// videoOnly 用 -map 0:V:0 跳过 MKV 里内嵌的封面图（attached_pic），
// 否则封面会被当成"最佳视频流"抽出来，缩略图就成了海报。
type frameAttempt struct{ hw, videoOnly bool }

func (r *Runner) runFrame(ctx context.Context, path string, at float64, vf, format string) ([]byte, error) {
	return r.runFrameEx(ctx, path, at, vf, format, false)
}

// runFrameEx 是 runFrame 的可配置版本。softOnly=true 时完全不尝试核显：
// 抽单帧缩略图用软解只要 0.1–0.2 秒，核显初始化加下载回传反而更慢，
// 遇到 10bit HEVC 之类还会先失败一次再回退。
func (r *Runner) runFrameEx(ctx context.Context, path string, at float64, vf, format string, softOnly bool) ([]byte, error) {
	if at <= 0 {
		at = 1
	}
	return r.runFrameWith(ctx, path, "-ss", strconv.FormatFloat(at, 'f', 3, 64), vf, format, softOnly)
}

// ThumbnailFromEnd 取「结尾前 back 秒」的一帧。
// 片头黑场很长时（片头曲之前压了几十秒黑），往前跳会一直落在黑场里，
// 从结尾倒着取更稳，且不需要先探测时长。
func (r *Runner) ThumbnailFromEnd(ctx context.Context, path string, back float64, width int) ([]byte, error) {
	if r.FFmpeg == "" {
		return nil, fmt.Errorf("未找到 ffmpeg")
	}
	if width <= 0 || width > 640 {
		width = 320
	}
	if back <= 0 {
		back = 10
	}
	b, err := r.runFrameWith(ctx, path, "-sseof", "-"+strconv.FormatFloat(back, 'f', 3, 64),
		fmt.Sprintf("scale=%d:-2,format=yuv420p", width), "mjpeg", true)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("缩略图为空")
	}
	return b, nil
}

// runFrameWith 按给定的定位参数（-ss 或 -sseof）抽一帧。
func (r *Runner) runFrameWith(ctx context.Context, path, seekFlag, seekVal, vf, format string, softOnly bool) ([]byte, error) {
	var attempts []frameAttempt
	if !softOnly && r.UseHW && r.HWMode == "qsv" {
		attempts = append(attempts, frameAttempt{true, true})
	}
	attempts = append(attempts, frameAttempt{false, true}, frameAttempt{false, false})

	var lastErr error
	hwNote := ""
	for _, att := range attempts {
		hw := att.hw
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		args := []string{"-v", "error", "-nostdin"}
		filter := vf
		if hw {
			args = append(args, "-hwaccel", "qsv")
			filter = "hwdownload,format=nv12," + vf
		} else {
			args = append(args, "-hwaccel", "none")
		}
		args = append(args, seekFlag, seekVal, "-i", path)
		if att.videoOnly {
			// 大写 V = 排除 attached_pic；没有真实视频流（纯音频带封面）时会失败，
			// 由最后一次不限流的尝试兜底。
			args = append(args, "-map", "0:V:0")
		}
		args = append(args,
			"-frames:v", "1", "-vf", filter)
		if format == "mjpeg" {
			args = append(args, "-q:v", "4")
		}
		args = append(args, "-f", format, "pipe:1")
		cmd := exec.CommandContext(cctx, r.FFmpeg, args...) // #nosec G204 -- 可执行文件为探测到的 ffmpeg 绝对路径，参数由常量与数值格式化生成
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		cancel()
		if runErr == nil && stdout.Len() > 0 {
			return stdout.Bytes(), nil
		}
		if hw && runErr != nil {
			hwNote = hwFailureNote(stderr.String())
		}
		if runErr != nil {
			if !hw {
				logFFmpegStderr("抽帧", path, stderr.String())
			}
			lastErr = fmt.Errorf("%s", friendlyFFmpegError(stderr.String()))
		} else {
			lastErr = fmt.Errorf("帧数据为空")
		}
	}
	if lastErr != nil && hwNote != "" {
		lastErr = fmt.Errorf("%w；%s", lastErr, hwNote)
	}
	return nil, lastErr
}

// hwFailureNote 识别硬件解码失败关键字，便于在最终错误里保留软解重试上下文。
func hwFailureNote(stderr string) string {
	s := strings.ToLower(stderr)
	for _, kw := range []string{"qsv", "hwaccel", "hardware", "device", "vaapi", "cuda", "d3d11", "dxva", "render"} {
		if strings.Contains(s, kw) {
			return "核显解码失败(" + kw + "): " + truncate(stderr, 120)
		}
	}
	return ""
}

// ContentFrames 抽取正片区间内的低分辨率灰度帧，供阈值附近的轻量二次校验。
func (r *Runner) ContentFrames(ctx context.Context, path string, duration float64, ignoreIntro, ignoreOutro int) ([][]byte, error) {
	start, end := ContentWindow(duration, ignoreIntro, ignoreOutro)
	var frames [][]byte
	var lastErr error
	for _, t := range SampleTimes(start, end, "low") {
		b, err := r.runFrame(ctx, path, t, fmt.Sprintf("scale=%d:%d,format=gray", contentFrameSize, contentFrameSize), "rawvideo")
		if err != nil {
			lastErr = err
			continue
		}
		if len(b) < contentFrameBytes {
			lastErr = fmt.Errorf("校验帧数据不完整")
			continue
		}
		frames = append(frames, b[:contentFrameBytes])
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("无法提取校验帧: %v", lastErr)
	}
	return frames, nil
}

// FramesSimilar 使用简化 SSIM 判断两组正片帧是否足够相似；镜像帧取较高分，
// 以兼容简单的水平翻转二次剪辑。
func FramesSimilar(a, b [][]byte) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n == 0 {
		return true
	}
	var total float64
	for i := 0; i < n; i++ {
		score := FrameSSIM(a[i], b[i])
		if m := FrameSSIM(a[i], mirrorFrame(b[i])); m > score {
			score = m
		}
		total += score
	}
	return total/float64(n) >= verifySSIMThreshold
}

// FrameSSIM 计算 16x16 灰度帧的简化全局 SSIM。
func FrameSSIM(a, b []byte) float64 {
	if len(a) < contentFrameBytes || len(b) < contentFrameBytes {
		return 0
	}
	n := float64(contentFrameBytes)
	var ma, mb float64
	for i := 0; i < contentFrameBytes; i++ {
		ma += float64(a[i])
		mb += float64(b[i])
	}
	ma /= n
	mb /= n
	var va, vb, cov float64
	for i := 0; i < contentFrameBytes; i++ {
		da := float64(a[i]) - ma
		db := float64(b[i]) - mb
		va += da * da
		vb += db * db
		cov += da * db
	}
	va /= n
	vb /= n
	cov /= n
	c1 := math.Pow(0.01*255, 2)
	c2 := math.Pow(0.03*255, 2)
	num := (2*ma*mb + c1) * (2*cov + c2)
	den := (ma*ma + mb*mb + c1) * (va + vb + c2)
	if den == 0 {
		return 0
	}
	return num / den
}

func mirrorFrame(f []byte) []byte {
	if len(f) < contentFrameBytes {
		return f
	}
	out := make([]byte, contentFrameBytes)
	for y := 0; y < contentFrameSize; y++ {
		for x := 0; x < contentFrameSize; x++ {
			out[y*contentFrameSize+x] = f[y*contentFrameSize+(contentFrameSize-1-x)]
		}
	}
	return out
}

// Thumbnail 生成 JPEG 缩略图，用于网页预览。
func (r *Runner) Thumbnail(ctx context.Context, path string, at float64, width int) ([]byte, error) {
	if r.FFmpeg == "" {
		return nil, fmt.Errorf("未找到 ffmpeg")
	}
	if width <= 0 || width > 640 {
		width = 320
	}
	if at <= 0 {
		at = 1
	}
	// format=yuv420p 兜住 10bit 源（HEVC 10bit 的 MKV 很常见），否则 mjpeg 编码器会拒绝
	b, err := r.runFrameEx(ctx, path, at, fmt.Sprintf("scale=%d:-2,format=yuv420p", width), "mjpeg", true)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("缩略图为空")
	}
	return b, nil
}

func encodeU64(v uint64) []byte {
	b := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return b
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ParseThumbs 解析存放于数据库的 "hex,hex,hex" 缩略图哈希。
func ParseThumbs(s string) []uint64 {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]uint64, 0, len(parts))
	for _, p := range parts {
		b, err := hex.DecodeString(strings.TrimSpace(p))
		if err != nil || len(b) != 8 {
			continue
		}
		var v uint64
		for _, x := range b {
			v = v<<8 | uint64(x)
		}
		out = append(out, v)
	}
	return out
}

// MinHamming 返回两组缩略图哈希的最小汉明距离；-1 表示无法比较。
// 同时比较水平镜像后的哈希，避免简单翻转的视频被预筛直接拒绝。
func MinHamming(a, b string) int {
	ha, hb := ParseThumbs(a), ParseThumbs(b)
	if len(ha) == 0 || len(hb) == 0 {
		return -1
	}
	best := 64
	for _, x := range ha {
		for _, y := range hb {
			d := phash.Hamming(x, y)
			if md := phash.Hamming(x, phash.MirrorBits(y)); md < d {
				d = md
			}
			if d < best {
				best = d
			}
		}
	}
	return best
}
