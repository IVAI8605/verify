// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"verify/internal/applog"
	"verify/internal/health"
	"verify/internal/store"
	"verify/internal/taskguard"
)

// HealthOptions 控制一次文件健康检测。
type HealthOptions struct {
	// Roots 指定扫描根目录；为空时使用引擎识别出的卷。
	Roots []string
	// ContentMode 仅对空文件检测生效：是否深挖全零/全不可打印。
	ContentMode health.ContentMode
	// NameOpts 仅对文件名检测生效：默认关闭的两条规则。
	NameOpts health.NameOptions
	// Workers 并发数，<=0 时用 4。
	Workers int
	// Force 为真时忽略已有结果重新探测（损坏视频检测用）。
	// 默认 false：视频探测开销大，续跑比全量重来更实用。
	Force bool
	// Limit 限制本轮最多处理多少个文件，0 表示不限制。
	// 闲时巡检按批执行，靠它保证"跑一段就退出来"。
	Limit int
	// Auto 标记这是闲时自动任务，仅用于界面区分。
	Auto bool
}

// HealthProgress 是一次检测任务的进度，通过 SSE 与状态接口暴露。
type HealthProgress struct {
	TaskID    int64  `json:"task_id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Processed int64  `json:"processed"`
	Total     int64  `json:"total"`
	Current   string `json:"current"`
	Errors    int64  `json:"errors"`
	Message   string `json:"message,omitempty"`
	Auto      bool   `json:"auto,omitempty"`
	StartedAt int64  `json:"started_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// 任务状态。
const (
	HealthIdle     = "idle"
	HealthRunning  = "running"
	HealthDone     = "done"
	HealthCanceled = "canceled"
	HealthFailed   = "failed"
)

// healthWorkerLimit 返回健康检测的默认并发数，与查重的视频池相互独立。
// v3.10.2：从固定 4 改为按 CPU 核数取（至少 4、最多 8）。检测大部分是读文件头
// 与文件名判定（IO + 轻 CPU），核数富余时多开能明显缩短总时长；上限 8 防止
// 机械盘上开太多反而互相拖慢。
func healthWorkerLimit() int {
	n := runtime.NumCPU()
	if n < 4 {
		n = 4
	}
	if n > 8 {
		n = 8
	}
	return n
}

// healthFlushBatch 是边扫边落库的批大小。
// 太小会让 SQLite 频繁开事务，太大又会让界面迟迟看不到新命中。
const healthFlushBatch = 100

// healthState 把健康检测的运行态和查重状态完全分开，互不影响。
type healthState struct {
	mu       sync.Mutex
	progress HealthProgress
	cancel   context.CancelFunc
	seq      int64
}

// HealthState 返回当前或最近一次健康检测的进度。
func (e *Engine) HealthState() HealthProgress {
	e.hs.mu.Lock()
	defer e.hs.mu.Unlock()
	return e.hs.progress
}

// HealthRunning 表示是否已有检测在跑；同一时刻只允许一个，避免同时压满磁盘。
func (e *Engine) HealthRunning() bool {
	e.hs.mu.Lock()
	defer e.hs.mu.Unlock()
	return e.hs.progress.Status == HealthRunning
}

// CancelHealth 取消正在进行的检测；已处理的结果仍会落库。
func (e *Engine) CancelHealth() {
	e.hs.mu.Lock()
	cancel := e.hs.cancel
	e.hs.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// StartHealth 启动一次检测。方法立即返回，进度通过事件总线推送。
// kind 为 health.KindAll 时一次遍历同时完成全部类型（v3.9.3）。
func (e *Engine) StartHealth(kind health.Kind, opt HealthOptions) error {
	if !kind.Valid() && !health.IsAll(kind) {
		return fmt.Errorf("未知的检测类型: %s", kind)
	}
	// v3.10.8（P1-2/P2-8）：与扫描、清理计划双向互斥。
	// 判断放在取 hs.mu 之前，避免与 StartScanMode 形成锁序嵌套。
	if e.cleanupRun.Load() {
		return errors.New("清理计划正在执行，稍后再试")
	}
	if e.IsRunning() {
		return errors.New("扫描进行中，稍后再试")
	}
	// v3.10.13（P1）：体检会按磁盘现状删索引行，健康检测同时写索引会让两边结论打架。
	if e.indexRepair.Load() {
		return errors.New("索引体检进行中，稍后再试")
	}
	e.hs.mu.Lock()
	if e.hs.progress.Status == HealthRunning {
		e.hs.mu.Unlock()
		return errors.New("已有文件健康检测在进行中")
	}
	root := e.baseCtx
	if root == nil {
		root = context.Background()
	}
	ctx, cancel := context.WithCancel(root)
	e.hs.cancel = cancel
	e.hs.seq++
	taskID := e.hs.seq
	e.hs.progress = HealthProgress{
		TaskID:    taskID,
		Kind:      string(kind),
		Status:    HealthRunning,
		Auto:      opt.Auto,
		StartedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	e.hs.mu.Unlock()

	// 与 StartScanMode 对称的反向复查（v3.10.8）：扫描恰好在前面两次检查之间启动时，
	// 让扫描赢，本次健康检测回滚，避免两者同时压满磁盘。
	if e.IsRunning() {
		e.hs.mu.Lock()
		if e.hs.progress.TaskID == taskID {
			e.hs.progress.Status = HealthIdle
			e.hs.progress.UpdatedAt = time.Now().Unix()
		}
		e.hs.cancel = nil
		e.hs.mu.Unlock()
		cancel()
		return errors.New("扫描进行中，稍后再试")
	}

	// v3.18.0：健康检测同样独占磁盘；转码 / 识别竖横屏在跑时直接拒绝。
	doneGuard, gerr := e.acquireTask(taskguard.KindHealth, "", taskguard.GroupIO,
		taskguard.ResCPU, taskguard.ResDiskIO, taskguard.ResDBWrite)
	if gerr != nil {
		e.hs.mu.Lock()
		if e.hs.progress.TaskID == taskID {
			e.hs.progress.Status = HealthIdle
			e.hs.progress.UpdatedAt = time.Now().Unix()
		}
		e.hs.cancel = nil
		e.hs.mu.Unlock()
		cancel()
		return gerr
	}
	go func() {
		defer cancel()
		defer doneGuard("done")
		if health.IsAll(kind) {
			e.runHealthAll(ctx, taskID, opt)
			return
		}
		e.runHealth(ctx, kind, taskID, opt)
	}()
	return nil
}

// runHealth 是检测主流程：遍历 → 并发判定 → 落库 → 推送完成事件。
func (e *Engine) runHealth(ctx context.Context, kind health.Kind, taskID int64, opt HealthOptions) {
	workers := opt.Workers
	if workers <= 0 {
		workers = healthWorkerLimit()
	}
	roots := opt.Roots
	if len(roots) == 0 {
		roots = e.Volumes()
	}
	// v3.10.2：先识别扫描根落在几块物理盘上，再按盘并行遍历（同盘的挂载点合并）。
	applog.Info("文件健康检测开始：%s，扫描根 %d 个，按物理卷分 %d 组并行",
		kind.Label(), len(roots), deviceGroupCount(roots))

	// ffmpeg 不可用时直接失败，避免遍历完一圈才发现没法探测
	if kind == health.KindBroken {
		if !e.RunnerAvailable() {
			e.finishHealth(kind, taskID, HealthFailed, 0, 0, 0, "未找到 ffmpeg，无法检测损坏视频")
			return
		}
	}

	// 空目录的检测形态不同（对象是目录树，一次遍历出结果），走专用路径
	if kind == health.KindEmptyDir {
		e.runEmptyDirScan(ctx, kind, taskID, roots)
		return
	}

	// 损坏视频探测单文件可能几秒，全量跑不完是常态，因此默认续跑：
	// 先记住已有结论的路径，清理之后再从候选里剔掉，避免重复调用 ffmpeg。
	var (
		done     map[string]bool
		keepRows []store.HealthResult
	)
	if kind == health.KindBroken && !opt.Force {
		// 已有结论必须原样保留：清空只是为了让本轮进度从零开始，
		// 不是要把上一轮的成果丢掉。
		// v3.10.3：以前只取 2000 条，超过的部分会被下面的 ClearHealthResults 全清掉，
		// 第 2001 条以后凭空消失且下轮还要重复 probe。这里分页取完。
		const keepPage = 5000
		for off := 0; ; off += keepPage {
			rows, err := e.st.ListHealthResults(context.Background(), string(kind), keepPage, off)
			if err != nil {
				applog.Warn("读取已有损坏视频结论失败（第 %d 条起）：%v", off, err)
				break
			}
			keepRows = append(keepRows, rows...)
			if len(rows) < keepPage {
				break
			}
		}
		if len(keepRows) > 0 {
			// v3.10.8：旧结论必须回验磁盘再回写。文件已被删除、改名或大小变了，
			// 原样回写就会产生点不开的幽灵行，并让同路径的新文件永远跳过探测。
			fresh := keepRows[:0]
			for _, r := range keepRows {
				fi, statErr := os.Lstat(r.Path)
				if statErr != nil || !fi.Mode().IsRegular() || fi.Size() != r.Size {
					continue
				}
				fresh = append(fresh, r)
			}
			if dropped := len(keepRows) - len(fresh); dropped > 0 {
				applog.Info("损坏视频检测续跑：丢弃 %d 条与磁盘不一致的旧结论", dropped)
			}
			keepRows = fresh
			// 用户已标为误报且文件未变的不再回写（v4.2.1：按 kind+size+mtime 判定，
			// 旧实现只看 path，会把同尺寸换过的文件也一并藏掉）。
			if im, ierr := e.st.HealthIgnoreMap(ctx); ierr == nil && len(im) > 0 {
				kept := keepRows[:0]
				for _, r := range keepRows {
					if fi, serr := os.Lstat(r.Path); serr == nil &&
						ignoredNow(im, string(kind), r.Path, fi.Size(), fi.ModTime().Unix()) {
						continue
					}
					kept = append(kept, r)
				}
				keepRows = kept
			}
		}
		if len(keepRows) > 0 {
			done = make(map[string]bool, len(keepRows))
			for _, r := range keepRows {
				done[r.Path] = true
			}
		}
	}

	// 先清空本轮类型的旧结果：遍历本身可能耗时几十秒，
	// 若等到遍历结束才清，界面会在这段时间里挂着上一轮的条目，看起来像没刷新。
	// 续跑模式下清理会导致已完成的条目消失，因此逐个重新入库。
	if err := e.st.ClearHealthResults(ctx, string(kind)); err != nil {
		applog.Warn("清空文件健康旧结果失败（%s）: %v", kind.Label(), err)
	}
	if len(keepRows) > 0 {
		if err := e.st.AppendHealthResults(context.Background(), string(kind), keepRows); err != nil {
			applog.Warn("回写已完成结果失败（%s）: %v", kind.Label(), err)
			// v4.1.1：旧行已被 ClearHealthResults 删掉，回写又失败。若仍保留 done，
			// 本轮会把这些路径全部跳过 → 旧结论没了、新结论没写、这轮也不探测，
			// 用户结论凭空消失。清空 done 让本轮重新探测，宁可多算也别丢结论。
			done = nil
		}
	}

	grouped, walkErr := e.collectHealthCandidateGroups(ctx, roots, kind)
	// v4.2.0：用户标为误报且文件未变的路径不再上报（2026-11-25 建议 4）。
	for gi := range grouped {
		grouped[gi] = e.filterIgnoredPaths(ctx, string(kind), grouped[gi])
	}
	var candidates []string
	for _, g := range grouped {
		candidates = append(candidates, g...)
	}
	if walkErr != nil && len(candidates) == 0 {
		e.finishHealth(kind, taskID, HealthFailed, 0, 0, 0, "遍历目录失败: "+walkErr.Error())
		return
	}
	// v3.9.8 修 F-01：单类型检测 0 候选也不能静默成功（全被排除目录过滤或根目录本身没文件）
	if walkErr == nil && len(candidates) == 0 {
		e.finishHealth(kind, taskID, HealthDone, 0, 0, 0, "扫描根下没有可检测文件，可能全部被排除目录过滤；请检查 volumes 配置或排除目录名单")
		return
	}

	if len(done) > 0 {
		kept := candidates[:0]
		for _, p := range candidates {
			if !done[p] {
				kept = append(kept, p)
			}
		}
		skipped := len(candidates) - len(kept)
		candidates = kept
		// 续跑过滤后同步重建分组，保证按盘并行的候选与 candidates 一致。
		for gi := range grouped {
			gk := grouped[gi][:0]
			for _, p := range grouped[gi] {
				if !done[p] {
					gk = append(gk, p)
				}
			}
			grouped[gi] = gk
		}
		if skipped > 0 {
			applog.Info("损坏视频检测续跑：跳过已出结论的 %d 个文件", skipped)
		}
	}
	if opt.Limit > 0 && len(candidates) > opt.Limit {
		candidates = candidates[:opt.Limit]
		// v4.1.1：按展平前缀顺序切分组，而不是用路径集合过滤。
		// 多个扫描根存在包含关系且落在不同 st_dev 时，同一路径会出现在两个组；
		// 用 keep[p] 集合匹配会让两组副本都留下，使探测数超过 total、进度 >100%。
		grouped = truncateHealthGroups(grouped, opt.Limit)
	}
	e.setHealthTotal(taskID, int64(len(candidates)))

	// 视频探测结论缓存：命中且文件未变时不再调 ffmpeg
	var probeCache map[string]store.ProbeEntry
	if kind == health.KindBroken {
		probeCache, _ = e.st.ProbeCache(context.Background())
	}

	var (
		mu      sync.Mutex
		results []store.HealthResult
		probed  []store.ProbeEntry
		procssd int64
		hitCnt  int64
		errCnt  int64
		lastPub time.Time
	)
	publish := func(force bool) {
		mu.Lock()
		if !force && time.Since(lastPub) < 400*time.Millisecond {
			mu.Unlock()
			return
		}
		lastPub = time.Now()
		mu.Unlock()
		e.pushHealth()
	}

	// flush 把已收集的结果成批写入；写库放在锁外，避免阻塞其它 worker
	flush := func() {
		mu.Lock()
		batch := results
		results = nil
		cacheBatch := probed
		probed = nil
		mu.Unlock()

		if len(batch) > 0 {
			if err := e.st.AppendHealthResults(context.Background(), string(kind), batch); err != nil {
				atomic.AddInt64(&errCnt, 1)
				applog.Warn("分批写入文件健康结果失败（%s）: %v", kind.Label(), err)
			}
		}
		if len(cacheBatch) > 0 {
			if err := e.st.SaveProbeCache(context.Background(), cacheBatch); err != nil {
				atomic.AddInt64(&errCnt, 1)
				applog.Warn("写入视频探测缓存失败: %v", err)
			}
		}
	}

	// v4.0.3：与全量检测同口径，按物理卷分组探测，每盘独立队列与路数。
	counts := make([]int, len(grouped))
	for i, g := range grouped {
		counts[i] = len(g)
	}
	perDisk := healthGroupWorkers(counts, workers)
	applog.Info("健康探测：%d 个物理卷分组，各组路数 %v（总 %d）", len(grouped), perDisk, sumInts(perDisk))

	worker := func(jobs <-chan string) {
		for path := range jobs {
			if ctx.Err() != nil {
				return
			}
			// v3.10.10（第七轮 P1-5）：健康探测也走全局槽位。
			// 排队时间不计入探测超时，避免 CPU 被视频分析饿住后把文件误判成「疑似损坏」。
			// v3.12.0：损坏探测会调 ffmpeg，先拿健康侧互斥位（视频/片头/裁剪/转码让路），
			// 再拿重任务槽位；顺序反了会和 ffmpeg 任务互相等（一边握槽位等对方，一边握互斥位等槽位）。
			probe := kind == health.KindBroken
			if probe && !e.acquireHealthProbe(ctx) {
				return
			}
			if !e.acquireHeavy(ctx) {
				if probe {
					e.releaseHealthProbe()
				}
				return
			}
			res, entry, err := e.checkOne(ctx, kind, path, opt, probeCache)
			e.releaseHeavy()
			if probe {
				e.releaseHealthProbe()
			}
			if err != nil {
				atomic.AddInt64(&errCnt, 1)
			}
			if res != nil || entry != nil {
				mu.Lock()
				if res != nil {
					results = append(results, *res)
					atomic.AddInt64(&hitCnt, 1)
				}
				if entry != nil {
					probed = append(probed, *entry)
				}
				full := len(results)+len(probed) >= healthFlushBatch
				mu.Unlock()
				if full {
					flush()
				}
			}
			n := atomic.AddInt64(&procssd, 1)
			e.setHealthProgress(taskID, n, path)
			publish(false)
		}
	}

	var wg sync.WaitGroup
	for gi, list := range grouped {
		if len(list) == 0 || perDisk[gi] <= 0 {
			continue
		}
		jobs := make(chan string)
		for k := 0; k < perDisk[gi]; k++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				worker(jobs)
			}()
		}
		// 每盘一个喂料协程：该盘喂完即关闭自己的通道，不影响其它盘。
		wg.Add(1)
		go func(list []string, jobs chan string) {
			defer wg.Done()
			defer close(jobs)
			for _, p := range list {
				select {
				case <-ctx.Done():
					return
				case jobs <- p:
				}
			}
		}(list, jobs)
	}
	wg.Wait()

	canceled := ctx.Err() != nil
	status := HealthDone
	if canceled {
		status = HealthCanceled
	}

	// 取消时也要把剩余批次刷进库：用户已经看到的中间结果不应该凭空消失
	flush()
	e.finishHealth(kind, taskID, status, int64(len(candidates)), atomic.LoadInt64(&procssd), atomic.LoadInt64(&errCnt), "")
	// v3.10.3：命中数在 flush 前用计数器取，flush 会把 results 置 nil，
	// 旧代码在 flush 之后读 len(results) 永远打印 0。
	applog.Info("文件健康检测完成：%s，命中 %d 个，处理 %d 个", kind.Label(), atomic.LoadInt64(&hitCnt), atomic.LoadInt64(&procssd))
}

// checkOne 对单个文件执行对应类型的判定。
func (e *Engine) checkOne(ctx context.Context, kind health.Kind, path string, opt HealthOptions, cache map[string]store.ProbeEntry) (*store.HealthResult, *store.ProbeEntry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	base := filepath.Base(path)

	switch kind {
	case health.KindEmpty:
		f, ok := health.CheckEmpty(path, info.Size(), opt.ContentMode)
		if !ok {
			return nil, nil, nil
		}
		return &store.HealthResult{
			Kind:    string(kind),
			Path:    path,
			Reason:  f.Reason,
			Reasons: []string{f.Reason},
			Detail:  health.ReasonLabel(f.Reason),
			Size:    f.Size,
		}, nil, nil

	case health.KindExt:
		head, err := health.Head(path)
		if err != nil {
			return nil, nil, err
		}
		f, bad := health.CheckExt(base, head, e.cfg.Get().UnifyExt)
		if !bad {
			return nil, nil, nil
		}
		detail := make([]string, 0, len(f.Reasons))
		for _, r := range f.Reasons {
			detail = append(detail, health.ReasonLabel(r))
		}
		return &store.HealthResult{
			Kind:    string(kind),
			Path:    path,
			Reason:  firstReason(f.Reasons),
			Reasons: f.Reasons,
			Detail:  strings.Join(detail, "、"),
			Size:    info.Size(),
			Suggest: f.SuggestExt,
		}, nil, nil

	case health.KindName:
		f, bad := health.CheckName(base, opt.NameOpts)
		if !bad {
			return nil, nil, nil
		}
		detail := make([]string, 0, len(f.Reasons))
		for _, r := range f.Reasons {
			detail = append(detail, health.ReasonLabel(r))
		}
		return &store.HealthResult{
			Kind:    string(kind),
			Path:    path,
			Reason:  firstReason(f.Reasons),
			Reasons: f.Reasons,
			Detail:  strings.Join(detail, "、"),
			Size:    info.Size(),
			Suggest: f.Suggested,
		}, nil, nil

	case health.KindTraditional:
		// v1.27.0：文件名含繁体字检测（t2s 转换后有差异即含）；设置页可关
		if !e.cfg.Get().TraditionalEnabled {
			return nil, nil, nil
		}
		has, simp := NameHasTraditional(base)
		if !has {
			return nil, nil, nil
		}
		return &store.HealthResult{
			Kind:    string(kind),
			Path:    path,
			Reason:  "traditional",
			Reasons: []string{"traditional"},
			Detail:  "文件名含繁体字",
			Size:    info.Size(),
			Suggest: simp,
		}, nil, nil

	case health.KindTemp:
		// v1.34.0 F1：临时文件（清单可编辑，清空=关闭）；只报告，清理走隔离区可恢复
		hit := health.MatchTemp(base, e.cfg.Get().TempPatterns)
		if hit == "" {
			return nil, nil, nil
		}
		return &store.HealthResult{
			Kind: string(kind),
			Path: path,
			// v3.15.0：把命中的清单项一并记进原因，临时文件页才能按 .tmp / .bak /
			// Thumbs.db 等细分筛选；清单可由用户自定义，前端因此从结果里动态收集。
			Reason:  "temp",
			Reasons: []string{"temp", hit},
			Detail:  health.TempReason(base, hit),
			Size:    info.Size(),
		}, nil, nil

	case health.KindBroken:
		return e.probeBrokenHealth(ctx, path, base, info.Size(), info.ModTime().UnixNano(), cache)
	}
	return nil, nil, nil
}

// collectHealthCandidates 遍历根目录收集待检文件，跳过排除项与隔离区。
//
// v3.10.2：按物理卷并行遍历。不同硬盘各自一个 goroutine，同盘上的多个挂载点
// 合并成一组串行，避免同盘争抢；结果按分组顺序合并，保证同一份数据每次顺序一致。
// collectHealthCandidateGroups 按物理卷分组返回候选（v4.0.3）。
//
// 与展平版 collectHealthCandidates 同一套遍历逻辑，但**保留分组**：
// 每块物理盘一个列表，供调用方给每盘独立分配并发。原因（真机实测）：
// 全局共享一个 worker 池时，4 路 worker 可能恰好都压在同一块盘的大文件上，
// 该盘 IO 打满、其它盘干等（CPU 占用反而很低）。按盘分组后每盘都有独立队列，
// 多盘同时推进，总吞吐由「最慢的盘」而不是「最慢的 4 个文件」决定。
func (e *Engine) collectHealthCandidateGroups(ctx context.Context, roots []string, kind health.Kind) ([][]string, error) {
	groups := deviceGroups(roots)
	parts := make([][]string, len(groups))
	errs := make([]error, len(groups))
	var wg sync.WaitGroup
	for i, group := range groups {
		if len(group) == 0 {
			continue
		}
		wg.Add(1)
		go func(i int, group []string) {
			defer wg.Done()
			var out []string
			var firstErr error
			for _, root := range group {
				err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if walkErr != nil {
						if firstErr == nil {
							firstErr = walkErr
						}
						return nil
					}
					if d.IsDir() {
						if path == root {
							return nil
						}
						if strings.HasPrefix(d.Name(), ".verify-trash") || e.IsExcludedPath(path) {
							return filepath.SkipDir
						}
						return nil
					}
					if kind == health.KindBroken && !health.HasBrokenExt(d.Name()) {
						return nil
					}
					out = append(out, path)
					return nil
				})
				if err != nil && !errors.Is(err, context.Canceled) && firstErr == nil {
					firstErr = err
				}
			}
			parts[i], errs[i] = out, firstErr
		}(i, group)
	}
	wg.Wait()

	var firstErr error
	for i := range parts {
		if firstErr == nil && errs[i] != nil {
			firstErr = errs[i]
		}
	}
	return parts, firstErr
}

// sumInts 求和，仅用于日志展示各组路数总量。
func sumInts(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

// truncateHealthGroups 按「展平后的前缀」截断分组，只保留前 limit 个候选。
//
// 分组是按物理卷收集的有序列表，展平顺序就是 candidates 的顺序。截断必须按这个
// 顺序切：顺序遍历各分组，每组取 min(len(g), remaining)，remaining 归零后清空后续组。
// 不能用「路径集合」去匹配——多个扫描根重叠（如 /vol1 与其下的 /vol1/usb 挂载点）
// 且落在不同 st_dev 时，同一路径会在两个组各出现一次，集合匹配会把两个副本都留下，
// 导致实际探测数 > total，进度条超过 100%。
func truncateHealthGroups(grouped [][]string, limit int) [][]string {
	if limit <= 0 {
		return grouped
	}
	remaining := limit
	out := make([][]string, len(grouped))
	for i, g := range grouped {
		if remaining <= 0 {
			out[i] = g[:0]
			continue
		}
		if len(g) > remaining {
			g = g[:remaining]
		}
		out[i] = g
		remaining -= len(g)
	}
	return out
}

// collectHealthCandidates 返回展平后的候选列表（保留给只需要一份列表的调用方）。
func (e *Engine) collectHealthCandidates(ctx context.Context, roots []string, kind health.Kind) ([]string, error) {
	groups, err := e.collectHealthCandidateGroups(ctx, roots, kind)
	var out []string
	for _, g := range groups {
		out = append(out, g...)
	}
	return out, err
}

// healthGroupWorkers 给每个物理卷分组分配探测路数（v4.0.3）。
//
// 策略：每组至少 1 路（保证每块盘都在扫，不会被别的盘抢光）；
// 余下路数按各组候选量比例分配；盘数多于总路数时每盘 1 路（宁可超订也要每盘都有进度）。
func healthGroupWorkers(counts []int, base int) []int {
	out := make([]int, len(counts))
	if len(out) == 0 {
		return out
	}
	for i := range out {
		out[i] = 1
	}
	remain := base - len(out)
	if remain <= 0 {
		return out
	}
	total := 0
	for _, c := range counts {
		total += c
	}
	if total <= 0 {
		for i := 0; remain > 0; i = (i + 1) % len(out) {
			out[i]++
			remain--
		}
		return out
	}
	for i, c := range counts {
		out[i] += remain * c / total
	}
	used := 0
	for _, n := range out {
		used += n
	}
	for used < base {
		idx := 0
		for i, c := range counts {
			if c > counts[idx] {
				idx = i
			}
		}
		out[idx]++
		used++
	}
	return out
}

// firstReason 取首个原因作为机器标识，detail 里保留全部原因的说明。
func firstReason(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	return reasons[0]
}

// setHealthTotal 在遍历结束后写入总数。
func (e *Engine) setHealthTotal(taskID, total int64) {
	e.hs.mu.Lock()
	if e.hs.progress.TaskID == taskID {
		e.hs.progress.Total = total
		e.hs.progress.UpdatedAt = time.Now().Unix()
	}
	e.hs.mu.Unlock()
	e.pushHealth()
}

// setHealthProgress 更新已处理数量与当前文件。
func (e *Engine) setHealthProgress(taskID, processed int64, current string) {
	e.hs.mu.Lock()
	if e.hs.progress.TaskID == taskID {
		e.hs.progress.Processed = processed
		e.hs.progress.Current = current
		e.hs.progress.UpdatedAt = time.Now().Unix()
	}
	e.hs.mu.Unlock()
}

// finishHealth 写入最终状态并补齐任务摘要。
func (e *Engine) finishHealth(kind health.Kind, taskID int64, status string, total, processed, errs int64, message string) {
	now := time.Now().Unix()
	e.hs.mu.Lock()
	if e.hs.progress.TaskID == taskID {
		e.hs.progress.Status = status
		e.hs.progress.Total = total
		e.hs.progress.Processed = processed
		e.hs.progress.Errors = errs
		e.hs.progress.Current = ""
		e.hs.progress.Message = message
		e.hs.progress.UpdatedAt = now
	}
	started := e.hs.progress.StartedAt
	e.hs.mu.Unlock()

	if _, err := e.st.SaveHealthTask(context.Background(), store.HealthTask{
		Kind: string(kind), Status: status, Total: total, Processed: processed,
		Errors: errs, StartedAt: started, FinishedAt: now,
	}); err != nil {
		applog.Warn("文件健康任务摘要写入失败（%s）: %v", kind.Label(), err)
	}
	e.pushHealth()
}

// pushHealth 把进度推给前端，频率由调用方控制。
func (e *Engine) pushHealth() {
	e.Bus.Publish(Event{Type: "health", Data: e.HealthState()})
}
