// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"verify/internal/applog"
	"verify/internal/health"
	"verify/internal/store"
	"verify/internal/taskguard"
)

// 批量转码 H.265 MP4（v1.17 引入 17.3-17.5）。
//
// 编码器（决策 C6）：启动任务时探测 hevc_qsv（root 上下文试跑），失败回退 libx265，
// 再失败报错。画质三档：高保真 CRF20/slow、均衡 CRF24/medium（默认）、省空间 CRF28/fast；
// 硬编用 -global_quality 20/26/28。音轨全部转 AAC 128k（决策 C3 不丢轨）；
// 文本字幕 mov_text 内封（决策 C4），图形字幕自动丢弃并注明；-movflags +faststart。

// TranscodeTask 是队列中的一个转码任务（内存态，替换完成即终态）。
type TranscodeTask struct {
	ID       int64   `json:"id"`
	Path     string  `json:"path"`
	Name     string  `json:"name"`
	Status   string  `json:"status"`   // waiting|running|done|failed|cancelled
	Progress float64 `json:"progress"` // 0-100（解析 stderr time=）
	Output   string  `json:"output,omitempty"`
	Error    string  `json:"error,omitempty"`
	Replaced bool    `json:"replaced"`
	// v3.23.0：非 .mp4 源扩展名与容器不符。最终文件落在 <主名>.mp4，
	// FinalPath 告诉界面实际路径，RenamedExt 让文案明确「已转为 .mp4」。
	FinalPath  string `json:"final_path,omitempty"`
	RenamedExt bool   `json:"renamed_ext"`
}

// TranscodeQueue 是进程内转码队列（设计文档 17.5.3：不持久化，重启未完成任务自然消失）。
type TranscodeQueue struct {
	mu      sync.Mutex
	tasks   []*TranscodeTask
	running bool
	cancel  context.CancelFunc
	encoder string // 探测缓存：hevc_qsv|libx265|未探测为空
	nextID  int64
	// cancelOne 保存正在运行任务的单独取消函数（v3.10.8 新增）：
	// 以前只有整批 cancel，用户想放弃一条慢任务只能把整队停掉。
	cancelOne map[int64]context.CancelFunc
}

// TranscodeEncoder 探测可用编码器（C1/C6）：hevc_qsv 试跑 1 秒 testsrc，
// 失败回退 libx265（软编必定可用，除非 ffmpeg 缺失）。结果缓存。
func (e *Engine) TranscodeEncoder() string {
	e.tq.mu.Lock()
	defer e.tq.mu.Unlock()
	if e.tq.encoder != "" {
		return e.tq.encoder
	}
	if !e.RunnerAvailable() {
		e.tq.encoder = "none"
		return e.tq.encoder
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tmp := filepath.Join(e.cfg.Get().DataDir, "cache", "qsv_probe.mp4")
	_ = os.MkdirAll(filepath.Dir(tmp), 0o750)
	// 编码器选择（v1.23.2 实测迭代）：
	// ① hevc_qsv —— fnOS ffmpeg 走 oneVPL(libmfx-gen)，仅支持 Gen12+，老世代报 unsupported；
	// ② hevc_vaapi —— VAAPI 原生编码，Kaby Lake 等 Gen9.5 可用（E1 通道），首选替代；
	// ③ libx265 —— 软编兜底。
	tryQSV := func(driver string) bool {
		args := []string{"-v", "error", "-nostdin", "-f", "lavfi", "-i", "testsrc=duration=1:size=640x360:rate=15",
			"-c:v", "hevc_qsv", "-global_quality", "26", "-y", tmp}
		cmd := exec.CommandContext(ctx, e.RunnerSnapshot().FFmpeg, args...) // #nosec G204 -- 参数为常量与受管路径
		if driver != "" {
			cmd.Env = append(os.Environ(), "LIBVA_DRIVER_NAME="+driver)
		}
		var eb bytes.Buffer
		cmd.Stderr = &eb
		err := cmd.Run()
		label := "默认(iHD)"
		if driver != "" {
			label = driver
		}
		if err == nil {
			return true
		}
		applog.Info("QSV 试跑失败（%s）：%v，stderr: %s", label, err, eb.String())
		return false
	}
	tryVAAPI := func() bool {
		node := e.RunnerSnapshot().RenderNode
		if node == "" {
			node = "/dev/dri/renderD128"
		}
		args := []string{"-v", "error", "-nostdin", "-vaapi_device", node,
			"-f", "lavfi", "-i", "testsrc=duration=1:size=640x360:rate=15",
			"-vf", "format=nv12,hwupload", "-c:v", "hevc_vaapi", "-qp", "26", "-y", tmp}
		cmd := exec.CommandContext(ctx, e.RunnerSnapshot().FFmpeg, args...) // #nosec G204 -- 参数为常量与受管路径
		var eb bytes.Buffer
		cmd.Stderr = &eb
		if err := cmd.Run(); err == nil {
			return true
		} else {
			applog.Info("hevc_vaapi 试跑失败：%v，stderr: %s", err, eb.String())
			return false
		}
	}
	switch {
	case tryQSV("") || tryQSV("i965"):
		e.tq.encoder = "hevc_qsv"
	case tryVAAPI():
		e.tq.encoder = "hevc_vaapi"
	default:
		// libx265 兜底试跑（极小分辨率，控制在秒级）
		args2 := []string{"-v", "error", "-nostdin", "-f", "lavfi", "-i", "testsrc=duration=1:size=320x240:rate=15",
			"-c:v", "libx265", "-crf", "28", "-preset", "ultrafast", "-y", tmp}
		cmd2 := exec.CommandContext(ctx, e.RunnerSnapshot().FFmpeg, args2...) // #nosec G204 -- 同上
		var xb bytes.Buffer
		cmd2.Stderr = &xb
		if err2 := cmd2.Run(); err2 == nil {
			e.tq.encoder = "libx265"
		} else {
			applog.Info("libx265 试跑失败：%v，stderr: %s", err2, xb.String())
			e.tq.encoder = "none"
		}
	}
	_ = os.Remove(tmp)
	applog.Info("转码编码器探测：%s", e.tq.encoder)
	return e.tq.encoder
}

// qualityParams 画质三档（设计文档 17.3）：返回 (vcodecArgs)。
func qualityParams(encoder, quality string) []string {
	q := map[string]string{"high": "20", "balanced": "26", "compact": "28"}[quality]
	if q == "" {
		q = "26"
	}
	switch encoder {
	case "hevc_qsv":
		return []string{"-c:v", "hevc_qsv", "-global_quality", q}
	case "hevc_vaapi":
		// VAAPI 原生编码（v1.23.2）：oneVPL 运行时不支持 Gen12 前的 iGPU（如 Kaby Lake），
		// hevc_qsf 不可用；hevc_vaapi 走 VAAPI E1 通道不受限。QP 即恒定质量模式。
		return []string{"-c:v", "hevc_vaapi", "-qp", q}
	}
	preset := map[string]string{"high": "slow", "balanced": "medium", "compact": "fast"}[quality]
	if preset == "" {
		preset = "medium"
	}
	crf := map[string]string{"high": "20", "balanced": "24", "compact": "28"}[quality]
	if crf == "" {
		crf = "24"
	}
	return []string{"-c:v", "libx265", "-crf", crf, "-preset", preset}
}

var transcodeTimeRe = regexp.MustCompile(`time=(\d+):(\d+):(\d+\.\d+)`)

// transcodeOne 转码单个文件；进度经 progress 回调（0-100）。
func (e *Engine) transcodeOne(ctx context.Context, t *TranscodeTask, quality, maxRes, encoder string, progress func(float64)) error {
	fi, err := os.Stat(t.Path)
	if err != nil {
		return fmt.Errorf("源文件不存在")
	}
	// 预期输出时长：用 ffprobe 读源时长（进度分母）
	srcDur := probeDuration(ctx, e.RunnerSnapshot().FFprobe, t.Path)
	dir := filepath.Dir(t.Path)
	ext := filepath.Ext(t.Path)
	stem := strings.TrimSuffix(filepath.Base(t.Path), ext)
	// v3.10.8（P2-15）：固定临时名会让并发转码/上次残留互相覆盖，第二次转码可能直接
	// 读到上一个任务的中间产物。改用唯一临时名；CreateTemp 同时充当目录可写性探测
	//（run-as=package：源目录未获 fnOS 授权或只读时在这里提前给出中文指引，
	// 而不是让用户面对 ffmpeg 的 exit 243）。
	tmpFile, terr := os.CreateTemp(dir, stem+"_h265_tmp*.mp4")
	if terr != nil {
		return fmt.Errorf("输出目录不可写（%s）：请在 fnOS 应用设置中把该目录授权为读写后重试", dir)
	}
	tmp := tmpFile.Name()
	_ = tmpFile.Close()
	_ = os.Remove(tmp)
	defer func() { _ = os.Remove(tmp) }()

	args := []string{"-v", "error", "-nostdin"}
	isVaapi := encoder == "hevc_vaapi"
	if isVaapi {
		// VAAPI 原生编码：绑定渲染节点，帧需上传显存（v1.23.2）
		node := e.RunnerSnapshot().RenderNode
		if node == "" {
			node = "/dev/dri/renderD128"
		}
		args = append(args, "-vaapi_device", node)
	}
	args = append(args, "-i", t.Path, "-map", "0")
	args = append(args, qualityParams(encoder, quality)...)
	if maxRes == "1080" {
		if isVaapi {
			// hw 帧缩放走 scale_vaapi；过滤图逗号需转义
			args = append(args, "-vf", `format=nv12,hwupload,scale_vaapi=w=-2:h=min(1080\,ih)`)
		} else {
			args = append(args, "-vf", "scale=-2:'min(1080,ih)'")
		}
	} else if isVaapi {
		args = append(args, "-vf", "format=nv12,hwupload")
	}
	// 音轨全部转 AAC 128k（决策 C3）；文本字幕 mov_text 内封（决策 C4）
	// v3.23.0：加 -progress pipe:2。原实现靠 stderr 的 time= 行算进度，
	// 但 -v error 下面根本不会输出统计行，进度条永远停在 0。
	args = append(args, "-c:a", "aac", "-b:a", "128k", "-c:s", "mov_text",
		"-progress", "pipe:2", "-nostats",
		"-movflags", "+faststart", "-y", tmp)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(cctx, e.RunnerSnapshot().FFmpeg, args...) // #nosec G204 -- 参数为常量与受管路径
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 ffmpeg 失败：%w", err)
	}
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			// v3.23.0：-progress pipe:2 的 out_time_us 是微秒，不依赖日志级别。
			if v, ok := strings.CutPrefix(line, "out_time_us="); ok {
				us, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
				if srcDur > 0 && us >= 0 {
					p := float64(us) / 1e6 / srcDur * 100
					if p > 100 {
						p = 100
					}
					if p >= 0 {
						progress(p)
					}
				}
				continue
			}
			m := transcodeTimeRe.FindStringSubmatch(line)
			if m == nil || srcDur <= 0 {
				continue
			}
			mm, _ := strconv.Atoi(m[1])
			ss, _ := strconv.Atoi(m[2])
			sec, _ := strconv.ParseFloat(m[3], 64)
			done := float64(mm*3600+ss*60) + sec
			p := done / srcDur * 100
			if p > 100 {
				p = 100
			}
			progress(p)
		}
	}()
	if err := cmd.Wait(); err != nil {
		if cctx.Err() == context.Canceled {
			return fmt.Errorf("已取消")
		}
		return fmt.Errorf("转码失败：%s", friendlyFFmpegMsg(err))
	}
	if srcDur > 0 {
		progress(100)
	}
	_ = fi
	// 验证：编码与时长（设计文档 17.7：ffprobe 确认编码 h265/hevc、时长偏差 <1 秒）
	got := probeCodecAndDuration(ctx, e.RunnerSnapshot().FFprobe, tmp)
	if got.codec != "" && !strings.Contains(strings.ToLower(got.codec), "265") && !strings.Contains(strings.ToLower(got.codec), "hevc") {
		return fmt.Errorf("输出编码为 %s，不是 H.265", got.codec)
	}
	if srcDur > 0 && got.duration > 0 && (got.duration < srcDur-1 || got.duration > srcDur+1) {
		return fmt.Errorf("输出时长偏差超过 1 秒（%.1fs vs %.1fs）", got.duration, srcDur)
	}
	// v3.23.0：编码与时长都正常，仍可能是截断/坏帧。落位前在 10% / 50% / 90%
	// 各解 5 秒，确认输出真的能解码；失败则不替换原文件。
	if err := e.sampleCheckOutput(ctx, tmp, srcDur); err != nil {
		return err
	}
	// 落位：同目录 <主名>.mp4（冲突追加 _1）
	out := health.UniquePath(dir, stem+".mp4", func(p string) bool {
		_, err := os.Lstat(p)
		return err == nil
	})
	// P3-2（2026-11-11）：落位走 renameNoOverwrite，目标被外部进程抢占时宁可报错也不覆盖。
	if err := renameNoOverwrite(tmp, out); err != nil {
		return fmt.Errorf("输出落位失败：%w", err)
	}
	t.Output = out
	return nil
}

// sampleCheckOutput 在 10%、50%、90% 三个位置各解 5 秒，确认输出可解码（v3.23.0）。
// 太短的视频三处会重叠，只校验开头 5 秒。
func (e *Engine) sampleCheckOutput(ctx context.Context, path string, dur float64) error {
	if dur < 15 {
		return e.decodeSegment(ctx, path, 0)
	}
	for _, frac := range []float64{0.1, 0.5, 0.9} {
		if err := e.decodeSegment(ctx, path, dur*frac); err != nil {
			return err
		}
	}
	return nil
}

// decodeSegment 用 ffmpeg 空输出解码一小段；只要解码器报错就视为损坏。
func (e *Engine) decodeSegment(ctx context.Context, path string, at float64) error {
	args := []string{"-v", "error", "-nostdin", "-ss", strconv.FormatFloat(at, 'f', 2, 64),
		"-i", path, "-t", "5", "-f", "null", "-"}
	out, err := exec.CommandContext(ctx, e.RunnerSnapshot().FFmpeg, args...).CombinedOutput() // #nosec G204 -- 参数为常量与受管路径
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("输出解码校验失败（%.0f 秒处）：%s", at, msg)
	}
	return nil
}

// TranscodeStart 建队列并开始执行（设计文档 17.5.3：任务状态存内存）。
func (e *Engine) TranscodeStart(paths []string, quality, maxRes string) (queued int, err error) {
	if len(paths) == 0 {
		return 0, fmt.Errorf("未选择文件")
	}
	encoder := e.TranscodeEncoder()
	if encoder == "none" {
		return 0, fmt.Errorf("无可用的 H.265 编码器（hevc_qsv 与 libx265 均不可用）")
	}
	// v1.30.1 安全修复：入队前三步防线（Clean+软链接解析+卷前缀重判）。
	// 此前只做 filepath.Clean，卷内软链接指向卷外文件时 ffmpeg 会读卷外任意文件。
	valid := make([]string, 0, len(paths))
	var rejectReason string
	for _, p := range paths {
		p = filepath.Clean(p)
		if !e.ValidPath(p) {
			applog.Warn("转码入队拒绝（不在受管卷内或路径无法解析）：%s", p)
			rejectReason = "所选文件不在受管卷内（或路径无法解析）"
			continue
		}
		// v3.23.0：不再只看扩展名。读 512 字节确认是确证视频容器，
		// 把 .txt 改名成 .mp4 这类文件挡在入队之前。
		if reason := e.transcodeInputReject(p); reason != "" {
			applog.Warn("转码入队拒绝（%s）：%s", p, reason)
			rejectReason = reason
			continue
		}
		valid = append(valid, p)
	}
	if len(valid) == 0 {
		if rejectReason == "" {
			rejectReason = "所选文件不在受管卷内（或路径无法解析），已全部拒绝"
		}
		return 0, fmt.Errorf("没有可转码的视频：%s", rejectReason)
	}
	e.tq.mu.Lock()
	for _, p := range valid {
		e.tq.nextID++
		e.tq.tasks = append(e.tq.tasks, &TranscodeTask{ID: e.tq.nextID, Path: p, Name: filepath.Base(p), Status: "waiting"})
	}
	if e.tq.running {
		n := len(e.tq.tasks)
		e.tq.mu.Unlock()
		return n, nil // 已有批次在跑，新任务排队等待当前批结束后的下一轮
	}
	e.tq.running = true
	ctx, cancel := context.WithCancel(context.Background())
	e.tq.cancel = cancel
	e.tq.mu.Unlock()

	// v3.18.0：转码吃解码/编码资源，与扫描 / 健康检测 / 识别竖横屏互斥。
	doneGuard, gerr := e.acquireTask(taskguard.KindTranscode, "", taskguard.GroupFFmpeg,
		taskguard.ResCPU, taskguard.ResDiskIO, taskguard.ResFFmpeg, taskguard.ResDBWrite)
	if gerr != nil {
		e.tq.mu.Lock()
		e.tq.running = false
		e.tq.cancel = nil
		for _, t := range e.tq.tasks {
			if t.Status == "waiting" {
				t.Status = "failed"
				t.Error = gerr.Error()
			}
		}
		e.tq.mu.Unlock()
		cancel()
		return 0, gerr
	}

	go func() {
		defer doneGuard("done")
		// v3.10.8：外层循环负责「运行期间新入队的任务」。异常退出时也必须复位队列，
		// 否则 running 永远为 true，之后所有任务只排队不执行。
		defer func() {
			if r := recover(); r != nil {
				e.tq.mu.Lock()
				e.tq.running = false
				e.tq.cancel = nil
				e.tq.mu.Unlock()
				e.bumpVersion()
				e.Bus.Publish(Event{Type: "update"})
				applog.Warn("转码队列异常退出，已复位：%v", r)
			}
		}()
		taskID, _ := e.st.NewAuditTaskID(ctx)
		for {
			// 整批取消后不再拾取新任务：子 ctx 会继承已取消的父 ctx，
			// 否则后入队的任务会被立刻标成「已取消」，看起来像功能失灵。
			if ctx.Err() != nil {
				e.tq.mu.Lock()
				for _, t := range e.tq.tasks {
					if t.Status == "waiting" {
						t.Status = "cancelled"
						t.Error = "已取消（整批取消）"
					}
				}
				e.tq.running = false
				e.tq.cancel = nil
				e.tq.mu.Unlock()
				e.bumpVersion()
				e.Bus.Publish(Event{Type: "update"})
				return
			}
			// v3.10.8：外层循环让「运行中追加的任务」在本批结束后继续执行。
			// 以前只取一次快照，后入队且状态仍是 waiting 的任务永远没人处理。
			for _, t := range e.snapshotWaiting() {
				// 每个任务一个子 ctx：既能被整批取消，也能被「取消本条」单独中止。
				taskCtx, taskCancel := context.WithCancel(ctx)
				e.tq.mu.Lock()
				if t.Status != "waiting" { // 排队期间已被单任务取消
					e.tq.mu.Unlock()
					taskCancel()
					continue
				}
				if e.tq.cancelOne == nil {
					e.tq.cancelOne = map[int64]context.CancelFunc{}
				}
				e.tq.cancelOne[t.ID] = taskCancel
				t.Status = "running"
				t.Error = ""
				e.tq.mu.Unlock()

				e.waitIfPaused(taskCtx) // v1.21：暂停检查点（任务边界）
				// 磁盘让路（设计文档 18.0：每文件前采样，占用则等待）
				for i := 0; i < 30 && taskCtx.Err() == nil; i++ {
					if !e.diskBusyNow() {
						break
					}
					time.Sleep(10 * time.Second)
				}
				if taskCtx.Err() != nil {
					e.finishTranscodeTask(t.ID)
					e.setStatus(t, "cancelled", "已取消")
					continue
				}
				// v3.12.0：转码是长时 ffmpeg 任务，与损坏探测互斥；取消时按已取消收尾。
				if !e.acquireFFmpegTask(taskCtx) {
					e.finishTranscodeTask(t.ID)
					e.setStatus(t, "cancelled", "已取消")
					continue
				}
				err := e.transcodeOne(taskCtx, t, quality, maxRes, encoder, func(p float64) {
					e.tq.mu.Lock()
					t.Progress = p
					e.tq.mu.Unlock()
					e.Bus.Publish(Event{Type: "transcode_progress", Data: map[string]any{"id": t.ID, "progress": p, "path": t.Path}})
				})
				e.releaseFFmpegTask()
				e.finishTranscodeTask(t.ID)
				if taskCtx.Err() != nil {
					// 单任务取消或整批取消都走这里：报「已取消」而不是「失败」。
					e.setStatus(t, "cancelled", "已取消")
					continue
				}
				if err != nil {
					e.setStatus(t, "failed", err.Error())
					_ = e.st.AddAudit(ctx, store.AuditEntry{TaskID: taskID, Operation: "transcode", OriginalPath: t.Path, NewPath: ""})
					continue
				}
				// 替换：原文件走隔离区（设计文档 17.5.6）——转码产物顶替原位。
				// v3.23.0：非 .mp4 源不能把 MP4 内容塞回 .mkv 扩展名；改为落在
				// <主名>.mp4，原文件进隔离区，索引与任务文案都指向新路径。
				// v3.25.0（P1-5）：旧实现先把任务标 done 再替换。替换因为保护目录、
				// 索引缺失被跳过，或回滚失败时，界面仍显示「完成」，但原文件还在、
				// 新文件留在 _tmp 名下。现在改成替换成功才落 done，否则按失败上报。
				replaceErr := ""
				if reason := e.protectedReasonResolved(t.Path); reason != "" {
					replaceErr = "原文件受保护，未替换（" + reason + "）"
				} else if tr, terr := e.trashFilesLocked(ctx, []string{t.Path}, nil); terr != nil {
					replaceErr = "原文件移入隔离区失败，未替换：" + terr.Error()
				} else if len(tr.Moved) == 0 {
					replaceErr = "原文件移入隔离区失败，未替换"
					if len(tr.Errors) > 0 {
						replaceErr += "：" + tr.Errors[0]
					}
				} else {
					finalPath := t.Output
					renamedExt := !strings.EqualFold(filepath.Ext(t.Path), ".mp4")
					if !renamedExt {
						finalPath = t.Path
					}
					// P3-2（2026-11-11）：顶替原位改用 renameNoOverwrite；非 .mp4 源时
					// finalPath 与 t.Output 相同，由该函数的 SameFile 分支放行，不会误报冲突。
					if err := renameNoOverwrite(t.Output, finalPath); err == nil {
						_ = e.st.AddAudit(ctx, store.AuditEntry{TaskID: taskID, Operation: "transcode", OriginalPath: t.Path, NewPath: finalPath})
						e.UpdatePath(finalPath)
						t.Replaced = true
						t.FinalPath = finalPath
						t.RenamedExt = renamedExt
						t.Output = finalPath
					} else {
						// 回滚失败不能吞掉：恢复动作可能因路径复核或卷状态被拒，
						// 此时原文件仍在隔离区，提示必须给出去处，而不是笼统写「已回滚」。
						rollback := "已回滚"
						if row, ok := e.st.LatestTrashByOrig(ctx, t.Path); ok {
							// v3.25.2（P3-5）：此处已持 FFmpeg 独占组，走无闸门版本，
							// 否则回滚会被自己的闸门判冲突，原文件就回不来了。
							rr := e.restoreLocked(ctx, []int64{row.ID})
							if rr == nil || len(rr.Restored) == 0 {
								rollback = "回滚失败，原文件仍在隔离区，请到隔离区恢复"
								applog.Error("转码替换失败且回滚失败：%s（%v）", t.Path, err)
							}
						} else {
							rollback = "回滚失败，未找到隔离记录，请到隔离区核对"
						}
						replaceErr = "替换失败" + rollback + "：" + err.Error()
					}
				}
				if replaceErr != "" {
					e.setStatus(t, "failed", replaceErr)
					_ = e.st.AddAudit(ctx, store.AuditEntry{TaskID: taskID, Operation: "transcode", OriginalPath: t.Path, NewPath: ""})
					applog.Warn("转码完成但未替换：%s（%s）", t.Path, replaceErr)
					continue
				}
				e.setStatus(t, "done", "")
				applog.Info("转码完成：%s → %s（%s）", t.Path, t.Output, encoder)
			}
			// 收尾与「是否还有新入队任务」放在同一把锁里判断，
			// 否则最后一条刚处理完、新任务恰好入队的瞬间会留下永远 waiting 的任务。
			e.tq.mu.Lock()
			if hasWaiting(e.tq.tasks) {
				e.tq.mu.Unlock()
				continue
			}
			e.tq.running = false
			e.tq.cancel = nil
			e.tq.mu.Unlock()
			e.bumpVersion()
			e.Bus.Publish(Event{Type: "update"})
			return
		}
	}()
	return len(e.tq.tasks), nil
}

// hasWaiting 判断队列里是否还有待执行任务；调用方需持有 tq.mu。
func hasWaiting(tasks []*TranscodeTask) bool {
	for _, t := range tasks {
		if t.Status == "waiting" {
			return true
		}
	}
	return false
}

// transcodeVideoContainers 是允许入队的「确证视频容器」类型（health.Sniff 的 RealType）。
// m4a 虽然也是 MP4 容器，但通常是纯音频，不作为转码输入。
var transcodeVideoContainers = map[string]bool{
	"mp4": true, "mov": true, "m4v": true, "3gp": true,
	"mkv": true, "webm": true, "avi": true,
	"mpegts": true, "flv": true, "wmv": true, "mpegps": true,
}

// transcodeInputReject 读文件头确认真实容器（v3.23.0）。
// 只看扩展名会把「.txt 改名成 .mp4」也放进队列，ffmpeg 跑到一半才失败。
//
// v3.25.0（P1-7）：rmvb/ogv/mxf/m2v 等容器没有稳定魔数，只按文件头判定会误拒。
// 文件头认不出或不是已知视频容器时，再用 ffprobe 确认是否真有视频流。
func (e *Engine) transcodeInputReject(path string) string {
	head, err := health.Head(path)
	if err != nil {
		return "读取文件头失败：" + err.Error()
	}
	finding, _ := health.CheckExt(filepath.Base(path), head, "")
	if finding.RealType == "" || !transcodeVideoContainers[finding.RealType] {
		if e.probeHasVideo(path) {
			return ""
		}
	}
	if finding.RealType == "" {
		return "无法确认是视频容器（文件头未识别，ffprobe 也未找到视频流）"
	}
	if !transcodeVideoContainers[finding.RealType] {
		return "文件内容不是视频容器（识别为 " + finding.RealType + "）"
	}
	return ""
}

// probeHasVideo 用 ffprobe 查第一个视频流（v3.25.0 P1-7，兜底识别无魔数容器）。
func (e *Engine) probeHasVideo(path string) bool {
	ffprobe := e.RunnerSnapshot().FFprobe
	if ffprobe == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_type", "-of", "csv=p=0", path).Output() // #nosec G204 -- 参数为常量与受管路径
	return err == nil && strings.Contains(string(out), "video")
}

// finishTranscodeTask 清理单任务取消函数；调用方不能持锁，避免与取消入口互相等待。
func (e *Engine) finishTranscodeTask(id int64) {
	e.tq.mu.Lock()
	delete(e.tq.cancelOne, id)
	e.tq.mu.Unlock()
}

func (e *Engine) snapshotWaiting() []*TranscodeTask {
	e.tq.mu.Lock()
	defer e.tq.mu.Unlock()
	var out []*TranscodeTask
	for _, t := range e.tq.tasks {
		if t.Status == "waiting" {
			out = append(out, t)
		}
	}
	return out
}

func (e *Engine) setStatus(t *TranscodeTask, status, msg string) {
	e.tq.mu.Lock()
	t.Status = status
	t.Error = msg
	e.tq.mu.Unlock()
}

// diskBusyNow 磁盘让路判定（设计文档 18.0 复用 DiskMonitor）。
func (e *Engine) diskBusyNow() bool {
	now := time.Now()
	e.sampleSectors()
	busy := e.diskBusy.Load()
	if busy {
		return true
	}
	if e.disk != nil && e.disk.AwakeRecently(now) {
		return true
	}
	return false
}

// TranscodeStatus 返回队列快照（设计文档 17.5.3 GET /api/transcode/status）。
//
// v3.25.0（P1-4）：返回结构体副本而不是指针切片。队列里的任务会被 worker
// 持续改写（进度、状态），调用方在锁外做 JSON 序列化时读指针会读到撕裂的字段。
func (e *Engine) TranscodeStatus() (tasks []TranscodeTask, encoder string) {
	e.tq.mu.Lock()
	defer e.tq.mu.Unlock()
	out := make([]TranscodeTask, 0, len(e.tq.tasks))
	for _, t := range e.tq.tasks {
		if t != nil {
			out = append(out, *t)
		}
	}
	return out, e.tq.encoder
}

// TranscodeCancel 取消整批（设计文档 17.5.3 POST /api/transcode/cancel）。
// 当前文件的 ffmpeg 被 ctx 杀掉、临时文件由 defer 清理（设计文档 17.7）。
func (e *Engine) TranscodeCancel() {
	e.tq.mu.Lock()
	cancel := e.tq.cancel
	e.tq.mu.Unlock()
	if cancel != nil {
		applog.Info("转码任务已请求取消")
		cancel()
	}
}

// TranscodeCancelJob 取消单条任务（v3.10.8 新增）：排队中的直接标取消，
// 运行中的只中止它自己的 ffmpeg，队列里其余任务继续。
// 返回 false 表示任务不存在或已处于终态。
func (e *Engine) TranscodeCancelJob(id int64) bool {
	e.tq.mu.Lock()
	var task *TranscodeTask
	for _, t := range e.tq.tasks {
		if t.ID == id {
			task = t
			break
		}
	}
	if task == nil {
		e.tq.mu.Unlock()
		return false
	}
	switch task.Status {
	case "waiting":
		task.Status = "cancelled"
		task.Error = "已取消（未开始）"
		e.tq.mu.Unlock()
		applog.Info("转码任务 %d 已在排队中取消：%s", id, task.Path)
		return true
	case "running":
		cancel := e.tq.cancelOne[id]
		e.tq.mu.Unlock()
		if cancel == nil {
			return false
		}
		applog.Info("转码任务 %d 已请求取消：%s", id, task.Path)
		cancel()
		return true
	default:
		e.tq.mu.Unlock()
		return false
	}
}

// probeDuration ffprobe 读源时长（秒）；失败返回 0（进度退化为不定）。
func probeDuration(ctx context.Context, ffprobe, path string) float64 {
	if ffprobe == "" {
		return 0
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, ffprobe, "-v", "error", "-show_entries",
		"format=duration", "-of", "default=nw=1", path).Output() // #nosec G204 -- 参数为常量与受管路径
	if err != nil {
		return 0
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(ln, "duration=") {
			d, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(ln, "duration=")), 64)
			return d
		}
	}
	return 0
}

type probeInfo struct {
	codec    string
	duration float64
}

// probeCodecAndDuration ffprobe 读视频编码与时长。
func probeCodecAndDuration(ctx context.Context, ffprobe, path string) probeInfo {
	var info probeInfo
	if ffprobe == "" {
		return info
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name", "-show_entries", "format=duration",
		"-of", "default=nw=1", path).Output() // #nosec G204 -- 参数为常量与受管路径
	if err != nil {
		return info
	}
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "codec_name=") {
			info.codec = strings.TrimPrefix(ln, "codec_name=")
		} else if strings.HasPrefix(ln, "duration=") {
			info.duration, _ = strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(ln, "duration=")), 64)
		}
	}
	return info
}
