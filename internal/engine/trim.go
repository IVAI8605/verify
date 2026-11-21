// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"verify/internal/applog"
	"verify/internal/health"
	"verify/internal/store"
	"verify/internal/taskguard"
)

// 片头剪除（v1.16 引入 16.4/16.5）：双模式 + 多层验证 + 可选替换原文件。
//
// 模式 A 快速（流复制，默认）：秒级无损，起点落最近关键帧，可能残留 1–2 秒。
// 模式 B 精确（重编码）：帧级对齐，耗时长。
// 不自动切换：快速输出异常只标注「建议改用精确模式重试」，按钮给用户（16.4）。

// TrimJob 是一个剪除任务。
type TrimJob struct {
	Path     string  `json:"path"`
	GroupKey string  `json:"group_key"`
	Offset   float64 `json:"offset"`  // 裁剪点（秒）
	Mode     string  `json:"mode"`    // fast|precise
	Replace  bool    `json:"replace"` // 替换原文件（默认关）
	Duration float64 `json:"duration"`
}

// TrimResult 是一批剪除的结果。
type TrimResult struct {
	Done   []TrimDone        `json:"done"`
	Failed []string          `json:"failed"`
	Errors map[string]string `json:"errors"`
}

// TrimDone 是一个完成的条目。
type TrimDone struct {
	Path     string  `json:"path"`
	Output   string  `json:"output"`
	Replaced bool    `json:"replaced"`
	Duration float64 `json:"output_duration"`
}

// trimTimeout 按模式返回超时（设计文档 16.5.4）：快速 30s；精确 max(120s, 原时长×2)。
func trimTimeout(mode string, duration float64) time.Duration {
	if mode == "precise" {
		t := 120 * time.Second
		if d := time.Duration(duration * 2 * float64(time.Second)); d > t {
			t = d
		}
		return t
	}
	return 30 * time.Second
}

// trimArgs 组装 ffmpeg 参数（16.4）：模式 A 流复制 / 模式 B 重编码。
func trimArgs(mode string, offset float64, in, out string) []string {
	if mode == "precise" {
		// -ss 放 -i 之后：帧级精确（重编码必须）
		return []string{"-v", "error", "-nostdin", "-i", in,
			"-ss", strconv.FormatFloat(offset, 'f', 3, 64),
			"-c:v", "libx264", "-crf", "20", "-preset", "veryfast",
			"-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", "-y", out}
	}
	return []string{"-v", "error", "-nostdin", "-ss", strconv.FormatFloat(offset, 'f', 3, 64),
		"-i", in, "-c", "copy", "-avoid_negative_ts", "make_zero", "-y", out}
}

// validateTrim 两层验证（设计文档 16.5.7）：L1 ffprobe 结构校验（必做）；
// L2 首尾各 2 秒解码抽检（必做）。返回 (通过, 明细)。
func (e *Engine) validateTrim(ctx context.Context, out string, expectDuration float64) (bool, string) {
	// L1：ffprobe 时长与视频流存在性
	if e.RunnerSnapshot().FFprobe == "" {
		return false, "未找到 ffprobe"
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	outB, err := exec.CommandContext(cctx, e.RunnerSnapshot().FFprobe, "-v", "error", "-show_entries",
		"format=duration", "-select_streams", "v", "-show_entries", "stream=codec_type",
		"-of", "default=nw=1", out).Output() // #nosec G204 -- 参数为常量与受管路径
	if err != nil {
		return false, "ffprobe 校验失败：" + err.Error()
	}
	s := string(outB)
	if !strings.Contains(s, "codec_type=video") {
		return false, "输出无视频流"
	}
	durLine := ""
	for _, ln := range strings.Split(s, "\n") {
		if strings.HasPrefix(ln, "duration=") {
			durLine = strings.TrimPrefix(ln, "duration=")
		}
	}
	od, _ := strconv.ParseFloat(strings.TrimSpace(durLine), 64)
	if expectDuration > 0 && (od < expectDuration-3 || od > expectDuration+3) {
		return false, fmt.Sprintf("输出时长偏差超过 3 秒（%.1fs，预期 %.1fs），建议改用精确模式重试", od, expectDuration)
	}
	// L2：首尾各 2 秒解码抽检
	for _, args := range [][]string{
		{"-v", "error", "-nostdin", "-i", out, "-t", "2", "-f", "null", "-"},
		{"-v", "error", "-nostdin", "-sseof", "-2", "-i", out, "-t", "2", "-f", "null", "-"},
	} {
		cctx2, cancel2 := context.WithTimeout(ctx, 60*time.Second)
		cmd := exec.CommandContext(cctx2, e.RunnerSnapshot().FFmpeg, args...) // #nosec G204 -- 参数为常量与受管路径
		if err := cmd.Run(); err != nil {
			cancel2()
			return false, "首/尾解码抽检失败，输出可能损坏"
		}
		cancel2()
	}
	return true, fmt.Sprintf("输出 %.1fs", od)
}

// TrimIntros 批量剪除（设计文档 16.5.2）：并发 2，逐项独立（单失败不影响其余）。
func (e *Engine) TrimIntros(ctx context.Context, jobs []TrimJob) *TrimResult {
	res := &TrimResult{Errors: map[string]string{}}
	// v3.18.0：剪除要调 ffmpeg，与扫描 / 转码 / 健康检测互斥。
	doneGuard, gerr := e.acquireTask(taskguard.KindTrim, "", taskguard.GroupFFmpeg,
		taskguard.ResCPU, taskguard.ResDiskIO, taskguard.ResFFmpeg, taskguard.ResDBWrite)
	if gerr != nil {
		res.Errors[""] = gerr.Error()
		return res
	}
	defer doneGuard("done")
	// v3.10.8（P2-15）：同一路径重复出现只处理一次。并发 2 下两条任务会同时写同一份
	// 临时文件与同一份输出，最终可能把一份剪除结果覆盖成另一份的中间产物。
	if len(jobs) > 1 {
		seen := make(map[string]bool, len(jobs))
		uniq := jobs[:0]
		for _, j := range jobs {
			p := filepath.Clean(j.Path)
			if seen[p] {
				continue
			}
			seen[p] = true
			uniq = append(uniq, j)
		}
		if len(uniq) != len(jobs) {
			applog.Warn("剪除任务含 %d 条重复路径，已去重", len(jobs)-len(uniq))
		}
		jobs = uniq
	}
	if !e.RunnerAvailable() {
		for _, j := range jobs {
			res.Failed = append(res.Failed, j.Path)
			res.Errors[j.Path] = "未找到 ffmpeg"
		}
		return res
	}
	jobsC := make(chan TrimJob)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var doneN int64
	total := int64(len(jobs))
	for i := 0; i < 2; i++ { // 设计文档 16.5.2：并发 2
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobsC {
				if ctx.Err() != nil {
					return
				}
				e.waitIfPaused(ctx) // v1.21：暂停检查点（文件边界）
				// v3.12.0：裁剪要调 ffmpeg，与损坏探测互斥（避免探测被抢 CPU 后误判损坏）。
				if !e.acquireFFmpegTask(ctx) {
					return
				}
				d, errMsg := e.trimOne(ctx, j)
				e.releaseFFmpegTask()
				mu.Lock()
				if d != nil {
					res.Done = append(res.Done, *d)
				} else {
					res.Failed = append(res.Failed, j.Path)
					res.Errors[j.Path] = errMsg
				}
				mu.Unlock()
				n := atomic.AddInt64(&doneN, 1)
				e.Bus.Publish(Event{Type: "trim_progress", Data: map[string]any{"done": n, "total": total, "path": j.Path}})
			}
		}()
	}
	// v3.17.4（F-04）：worker 在 ctx 取消后直接 return，不再从 jobsC 取任务。
	// 旧写法是无条件发送，取消后没人接收就会永久阻塞在这里，
	// HTTP handler 的 goroutine 随之泄漏，close(jobsC) 也永远执行不到。
	feedJobs(ctx, jobsC, jobs)
	close(jobsC)
	wg.Wait()
	if len(res.Done) > 0 {
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
	}
	return res
}

// feedJobs 把任务送进无缓冲通道，ctx 取消后立刻停止。
// v3.17.4（F-04）：worker 取消后直接 return，没人再接收；无条件发送会永久
// 阻塞在这里，HTTP handler 的 goroutine 随之泄漏。
func feedJobs(ctx context.Context, ch chan<- TrimJob, jobs []TrimJob) {
	for _, j := range jobs {
		select {
		case ch <- j:
		case <-ctx.Done():
			return
		}
	}
}

// runTrimAttempt 执行一次裁剪并验证，返回 (是否通过, 失败状态, 明细)。
// status 仅在失败时有意义：ffmpeg_failed / empty_output / validation_failed。
func (e *Engine) runTrimAttempt(ctx context.Context, mode string, j TrimJob, tmp string) (bool, string, string) {
	_ = os.Remove(tmp)
	cctx, cancel := context.WithTimeout(ctx, trimTimeout(mode, j.Duration))
	defer cancel()
	cmd := exec.CommandContext(cctx, e.RunnerSnapshot().FFmpeg, trimArgs(mode, j.Offset, j.Path, tmp)...) // #nosec G204 -- 参数为常量与受管路径
	if err := cmd.Run(); err != nil {
		return false, "ffmpeg_failed", "ffmpeg 裁剪失败（可能超时）：" + friendlyFFmpegMsg(err)
	}
	fi, err := os.Stat(tmp)
	if err != nil || fi.Size() == 0 {
		return false, "empty_output", "输出为空"
	}
	ok, detail := e.validateTrim(ctx, tmp, j.Duration-j.Offset)
	if !ok {
		return false, "validation_failed", "验证未通过：" + detail
	}
	return true, "done", detail
}

// trimOne 处理单个文件，成功返回结果条目；失败返回 (nil, 中文原因)。
// 错误经返回值传递（并发安全），不用共享变量。
func (e *Engine) trimOne(ctx context.Context, j TrimJob) (*TrimDone, string) {
	entry := store.TrimLogEntry{
		FilePath: j.Path, GroupKey: j.GroupKey, Mode: j.Mode,
		StartOffset: j.Offset, OriginalDuration: j.Duration, CreatedAt: time.Now().Unix(),
	}
	defer func() { _ = e.st.AddTrimLog(context.Background(), entry) }()

	if reason := e.trimCheck(j); reason != "" {
		entry.Status, entry.ErrorMessage = "rejected", reason
		return nil, reason
	}
	dir := filepath.Dir(j.Path)
	ext := filepath.Ext(j.Path)
	stem := strings.TrimSuffix(filepath.Base(j.Path), ext)
	// v3.10.8（P2-15）：固定临时名 `<主名>_trimmed_tmp.<ext>` 会误删用户同名文件，
	// 并发剪除同一路径时还会互相踩。改成唯一临时名（CreateTemp 顺带探测目录可写）。
	tmpFile, terr := os.CreateTemp(dir, stem+"_trimmed_tmp*"+ext)
	if terr != nil {
		msg := "输出目录不可写（" + dir + "）：请在 fnOS 应用设置中把该目录授权为读写后重试"
		entry.Status, entry.ErrorMessage = "io_failed", msg
		return nil, msg
	}
	tmp := tmpFile.Name()
	_ = tmpFile.Close()
	defer func() { _ = os.Remove(tmp) }() // 超时/失败不留残留（设计文档 16.7）

	ok, status, detail := e.runTrimAttempt(ctx, j.Mode, j, tmp)
	// v1.42.0：流复制只能从最近关键帧起步。GOP 较长时 -ss 会被退回到文件开头，
	// 快速模式输出几乎没变短，验证必然失败（BUG-05）。快速模式失败后自动改用
	// 精确模式重做一次，避免用户每次都要手动切换；精确模式同样带完整验证。
	if !ok && j.Mode != "precise" {
		fastDetail := detail
		applog.Warn("快速剪除未通过验证（%s），自动改用精确模式重试：%s", fastDetail, j.Path)
		ok, status, detail = e.runTrimAttempt(ctx, "precise", j, tmp)
		if ok {
			detail += "（快速模式未达标，已自动精确重编码；快速模式原因：" + fastDetail + "）"
		} else {
			detail = "快速模式：" + fastDetail + "；精确模式：" + detail
		}
	}
	entry.ValidationPassed, entry.ValidationDetail = ok, detail
	if !ok {
		entry.Status, entry.ErrorMessage = status, detail
		return nil, detail
	}
	od := parseProbeDuration(detail)

	// 输出落位：同目录 <主名>_trimmed.<ext>，冲突追加 _1（设计文档 16.5.5）
	out := health.UniquePath(dir, stem+"_trimmed"+ext, func(p string) bool {
		_, err := os.Lstat(p)
		return err == nil
	})
	// P3-2（2026-11-11）：落位统一走 renameNoOverwrite——UniquePath 只能保证「检查那一刻」
	// 不存在，到真正改名之间外部进程抢占同名文件时，裸改名会静默覆盖。
	if err := renameNoOverwrite(tmp, out); err != nil {
		msg := "输出落位失败：" + err.Error()
		entry.Status, entry.ErrorMessage = "rename_failed", msg
		return nil, msg
	}
	entry.OutputPath, entry.OutputDuration, entry.Status = out, od, "done"

	done := &TrimDone{Path: j.Path, Output: out, Duration: od}
	// 替换原文件（设计文档 16.5.6，默认关）：原文件走 trash 通道 → rename 顶替 → 失败回滚
	if j.Replace {
		if reason := e.protectedReasonResolved(j.Path); reason != "" {
			// 决策 B1：保护目录禁止替换，保留 _trimmed 新文件（不算失败）
			return done, ""
		}
		tr, terr := e.trashFilesLocked(ctx, []string{j.Path}, nil)
		if terr != nil || len(tr.Moved) == 0 {
			msg := "原文件隔离失败，替换中止（新文件已生成）"
			entry.Status, entry.ErrorMessage = "replace_failed", msg
			return done, msg
		}
		qp := ""
		if row, ok := e.st.LatestTrashByOrig(ctx, j.Path); ok {
			qp = row.TrashPath
		}
		// P3-2（2026-11-11）：顶替原位同样不能裸改名——原文件刚进隔离区，外部进程
		// 可能已在原位建了同名文件，覆盖后既无隔离也无审计，无法恢复。
		if err := renameNoOverwrite(out, j.Path); err != nil {
			// 回滚是否成功必须如实上报：Restore 可能因卷不可信或路径复核失败而不动文件，
			// 一律写「已回滚」会让用户以为原文件已经回到原位。
			rollback := "已回滚"
			if row, ok := e.st.LatestTrashByOrig(ctx, j.Path); ok {
				// v3.25.2（P3-5）：剪除任务已持 FFmpeg 独占组，回滚走无闸门版本。
				rr := e.restoreLocked(ctx, []int64{row.ID})
				if rr == nil || len(rr.Restored) == 0 {
					rollback = "回滚失败，原文件仍在隔离区，请到隔离区恢复"
					applog.Error("片头剪除替换失败且回滚失败：%s（%v）", j.Path, err)
				}
			} else {
				rollback = "回滚失败，未找到隔离记录，请到隔离区核对"
			}
			msg := "替换失败" + rollback + "：" + err.Error()
			entry.Status, entry.ErrorMessage = "replace_failed", msg
			return nil, msg
		}
		// 替换模式下原文件的索引行已被 trashFilesLocked → dropIndexPath 删掉，
		// 新文件顶替原位后必须先 UpdatePath 重建行，否则 MarkTrimmed 更新的是
		// 不存在的行（RowsAffected=0 且返回 nil），该视频会从结果页/分组/搜索里
		// 一直消失到下次全量扫描，trimmed 标记也丢失。口径与 transcode 替换一致。
		e.UpdatePath(j.Path)
		if err := e.st.MarkTrimmed(ctx, j.Path, j.Offset); err != nil {
			applog.Warn("剪除替换后标记已剪失败：%s（%v）", j.Path, err)
		}
		entry.QuarantinePath = qp
		done.Replaced = true
		done.Output = j.Path
		_ = os.Remove(out) // 已顶替原位，_trimmed 文件不再需要
	}
	applog.Info("片头剪除完成：%s（%.1fs 起，模式 %s）", j.Path, j.Offset, j.Mode)
	return done, ""
}

// trimCheck 剪除前校验。
func (e *Engine) trimCheck(j TrimJob) string {
	// v1.30.1 安全修复：三步防线（Clean+软链接解析+卷前缀重判）。
	// 此前 VolumeOf 只对原始串做前缀匹配，`/vol1/x/../../etc/f` 形式的 `..`
	// 向量可绕过卷校验，让 ffmpeg 读卷外文件并在卷外目录写出临时产物。
	if !e.ValidPath(j.Path) {
		return "文件不在受管卷内或路径无法解析（含软链接指向卷外）"
	}
	fi, err := os.Lstat(j.Path)
	if err != nil || !fi.Mode().IsRegular() {
		return "文件不存在或不是普通文件"
	}
	if e.VolumeOf(j.Path) == "" {
		return "文件不在受管卷内"
	}
	if strings.Contains(filepath.ToSlash(j.Path), "/.verify-trash/") {
		return "隔离区内的文件不支持剪除"
	}
	if j.Offset <= 0 {
		return "裁剪点必须大于 0"
	}
	if j.Duration > 0 && j.Offset >= j.Duration-5 {
		return "裁剪点过于接近片尾"
	}
	if j.Replace {
		if reason := e.protectedReasonResolved(j.Path); reason != "" {
			return reason + "（保护目录内只能生成 _trimmed 新文件，不能替换原文件）"
		}
	}
	return ""
}

func friendlyFFmpegMsg(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		msg := strings.TrimSpace(string(ee.Stderr))
		if len(msg) > 160 {
			msg = msg[:160]
		}
		if msg != "" {
			return msg
		}
	}
	return err.Error()
}

func parseProbeDuration(detail string) float64 {
	// detail 形如 "输出 12.3s"
	var d float64
	_, _ = fmt.Sscanf(detail, "输出 %fs", &d)
	return d
}
