// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"verify/internal/applog"
	"verify/internal/health"
	"verify/internal/store"
)

// probeBrokenHealth 对单个文件做损坏探测，命中时返回结果与缓存条目（v3.9.3 抽出复用）。
// entry 为 nil 表示走了缓存命中或该扩展名不在检测范围，不需要更新探测缓存。
func (e *Engine) probeBrokenHealth(ctx context.Context, path, base string, size, mtime int64, cache map[string]store.ProbeEntry) (*store.HealthResult, *store.ProbeEntry, error) {
	if !health.HasBrokenExt(base) {
		return nil, nil, nil
	}
	// 缓存命中且文件没变过：直接复用结论，不调 ffmpeg
	if ent, ok := cache[path]; ok && ent.Size == size && ent.Mtime == mtime {
		if ent.Status == store.ProbeOK {
			return nil, nil, nil
		}
		return &store.HealthResult{
			Kind:    string(health.KindBroken),
			Path:    path,
			Reason:  ent.Reason,
			Reasons: []string{ent.Reason},
			Detail:  ent.Detail,
			Size:    size,
		}, nil, nil
	}

	f := health.ProbeBroken(ctx, e.RunnerSnapshot().FFmpeg, e.RunnerSnapshot().FFprobe, path, health.DefaultRunner())
	entry := &store.ProbeEntry{Path: path, Size: size, Mtime: mtime, Status: store.ProbeOK}
	if f == nil {
		// 健康结论同样要落缓存，否则下次还得重新解一遍
		return nil, entry, nil
	}
	entry.Status = store.ProbeBroken
	if f.Reason == health.ReasonTimeout {
		entry.Status = store.ProbeTimeout
	}
	entry.Reason = f.Reason
	entry.Detail = health.ReasonLabel(f.Reason)
	return &store.HealthResult{
		Kind:    string(health.KindBroken),
		Path:    path,
		Reason:  f.Reason,
		Reasons: []string{f.Reason},
		Detail:  entry.Detail,
		Size:    size,
	}, entry, nil
}

// runHealthAll 一次遍历同时完成全部 7 类检测（v3.9.3）。
// 与单类型检测的区别：候选是所有文件，每个文件依次跑各类判定，结果按 kind 分组落库。
// 空目录的对象是目录树，复用独立的判定逻辑，但同样只由用户点一次扫描触发。
func (e *Engine) runHealthAll(ctx context.Context, taskID int64, opt HealthOptions) {
	workers := opt.Workers
	if workers <= 0 {
		workers = healthWorkerLimit()
	}
	roots := opt.Roots
	if len(roots) == 0 {
		roots = e.Volumes()
	}
	// v3.10.2：先识别扫描根落在几块物理盘上，再按盘并行遍历（同盘的挂载点合并）。
	applog.Info("文件健康全量检测开始：扫描根 %d 个，按物理卷分 %d 组并行",
		len(roots), deviceGroupCount(roots))

	// v3.10.4：不再在开头清空全部类型——中途取消会把上一轮完整结论抹掉。
	// 改为边扫边 upsert，正常完成且无写库失败时，再删掉本轮开始前的旧行；
	// 取消或写库失败时保留旧行，宁可多留也不丢结论。
	startedAt := time.Now().Unix()

	grouped, walkErr := e.collectHealthCandidateGroups(ctx, roots, health.KindAll)
	// v4.2.0：用户标为误报且文件未变的路径不再上报。
	for gi := range grouped {
		grouped[gi] = e.filterIgnoredPaths(ctx, grouped[gi])
	}
	var files []string
	for _, g := range grouped {
		files = append(files, g...)
	}
	if walkErr != nil && len(files) == 0 {
		e.finishHealth(health.KindAll, taskID, HealthFailed, 0, 0, 0, "遍历目录失败: "+walkErr.Error())
		return
	}
	// v3.9.8 修 F-01：0 候选不能静默成功——files 被排除目录过滤完、dirs 也空时，给明确提示
	if len(files) == 0 {
		dirs, _ := findEmptyDirsParallel(ctx, roots, func(p string) bool {
			return e.IsExcludedPath(p) || e.IsProtected(p) || health.IsTrashDirName(filepath.Base(p))
		})
		if len(dirs) == 0 {
			e.finishHealth(health.KindAll, taskID, HealthDone, 0, 0, 0, "扫描根下没有可检测文件，可能全部被排除目录过滤；请检查 volumes 配置或排除目录名单")
			return
		}
	}

	// 空目录判定要自底向上看整棵子树，复用 health.FindEmptyDirs 保证与单类型检测同口径。
	skip := func(p string) bool {
		return e.IsExcludedPath(p) || e.IsProtected(p) || health.IsTrashDirName(filepath.Base(p))
	}
	dirs, _ := findEmptyDirsParallel(ctx, roots, skip)
	if len(dirs) > 0 {
		rows := make([]store.HealthResult, 0, len(dirs))
		for _, d := range dirs {
			rows = append(rows, store.HealthResult{
				Kind:    string(health.KindEmptyDir),
				Path:    d,
				Reason:  ReasonEmptyDir,
				Reasons: []string{ReasonEmptyDir},
				Detail:  health.ReasonLabel(ReasonEmptyDir),
			})
		}
		if err := e.st.AppendHealthResults(ctx, string(health.KindEmptyDir), rows); err != nil {
			applog.Warn("写入空目录结果失败: %v", err)
		}
	}

	total := int64(len(files) + len(dirs))
	e.setHealthTotal(taskID, total)

	// ffmpeg 不可用时只跳过损坏检测，其它六类照常跑
	var probeCache map[string]store.ProbeEntry
	if e.RunnerAvailable() {
		probeCache, _ = e.st.ProbeCache(context.Background())
	}

	var (
		mu      sync.Mutex
		writeMu sync.Mutex // 串行化落库：多个 worker 同时 flush 时 SQLite 会互相锁住
		results []store.HealthResult
		probed  []store.ProbeEntry
		procssd = int64(len(dirs))
		// v3.10.4：把「检测失败」与「落库失败」分开计数，后者不能算成文件损坏。
		detectErr int64
		storeErr  int64
		lastPub   time.Time
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

	// flush 把已收集的结果按 kind 分组写入；写库放在锁外，避免阻塞其它 worker
	flush := func() {
		mu.Lock()
		batch := results
		results = nil
		cacheBatch := probed
		probed = nil
		mu.Unlock()

		if len(batch) > 0 {
			byKind := make(map[string][]store.HealthResult, len(health.AllKinds()))
			for _, r := range batch {
				byKind[r.Kind] = append(byKind[r.Kind], r)
			}
			writeMu.Lock()
			for kind, rows := range byKind {
				if err := e.st.AppendHealthResults(context.Background(), kind, rows); err != nil {
					atomic.AddInt64(&storeErr, 1)
					applog.Warn("分批写入文件健康结果失败（%s）: %v", kind, err)
				}
			}
			writeMu.Unlock()
		}
		if len(cacheBatch) > 0 {
			writeMu.Lock()
			if err := e.st.SaveProbeCache(context.Background(), cacheBatch); err != nil {
				atomic.AddInt64(&storeErr, 1)
				applog.Warn("写入视频探测缓存失败: %v", err)
			}
			writeMu.Unlock()
		}
	}

	// v4.0.3：按物理卷分组探测，每盘独立队列与独立路数（healthGroupWorkers）。
	// 旧实现所有盘共用一个 jobs 通道 + 全局 worker 池，4 路 worker 会恰好压在同一块盘的
	// 大文件上：该盘 IO 打满、其它盘干等，CPU 反而闲着。分组后每盘都有进度，
	// 总吞吐由「最慢的盘」决定，而不是「最慢的几个文件」。
	counts := make([]int, len(grouped))
	for i, g := range grouped {
		counts[i] = len(g)
	}
	perDisk := healthGroupWorkers(counts, workers)
	applog.Info("健康全量探测：%d 个物理卷分组，各组路数 %v（总 %d）", len(grouped), perDisk, sumInts(perDisk))

	worker := func(jobs <-chan string) {
		for path := range jobs {
			if ctx.Err() != nil {
				return
			}
			res, entry, err := e.checkOneAll(ctx, path, opt, probeCache)
			if err != nil {
				atomic.AddInt64(&detectErr, 1)
			}
			if len(res) > 0 || entry != nil {
				mu.Lock()
				results = append(results, res...)
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

	status := HealthDone
	if ctx.Err() != nil {
		status = HealthCanceled
	}
	flush()
	// 正常完成才清理历史行；取消或落库失败时保留上一轮结论（v3.10.4）。
	if status == HealthDone && atomic.LoadInt64(&storeErr) == 0 {
		if err := e.st.DeleteHealthDetectedBefore(context.Background(), startedAt); err != nil {
			applog.Warn("清理本轮之前的历史健康结果失败: %v", err)
		}
	}
	e.finishHealth(health.KindAll, taskID, status, total, atomic.LoadInt64(&procssd), atomic.LoadInt64(&detectErr), "")
	applog.Info("文件健康全量检测完成：处理 %d 个文件、%d 个空目录，错误 %d 个",
		len(files), len(dirs), atomic.LoadInt64(&detectErr))
}

// checkOneAll 对单个文件依次执行各类判定，一个文件可同时命中多类。
func (e *Engine) checkOneAll(ctx context.Context, path string, opt HealthOptions, cache map[string]store.ProbeEntry) ([]store.HealthResult, *store.ProbeEntry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	base := filepath.Base(path)
	cfg := e.cfg.Get()
	var out []store.HealthResult

	// 空文件
	if f, ok := health.CheckEmpty(path, info.Size(), opt.ContentMode); ok {
		out = append(out, store.HealthResult{
			Kind: string(health.KindEmpty), Path: path, Reason: f.Reason,
			Detail: health.ReasonLabel(f.Reason), Size: f.Size,
		})
	}

	// 扩展名：读一次文件头，失败时不影响其它类型
	if head, herr := health.Head(path); herr == nil {
		if f, bad := health.CheckExt(base, head, cfg.UnifyExt); bad {
			detail := make([]string, 0, len(f.Reasons))
			for _, r := range f.Reasons {
				detail = append(detail, health.ReasonLabel(r))
			}
			out = append(out, store.HealthResult{
				Kind: string(health.KindExt), Path: path, Reason: firstReason(f.Reasons),
				Detail: strings.Join(detail, "、"), Size: info.Size(), Suggest: f.SuggestExt,
			})
		}
	}

	// 文件名不规范
	if f, bad := health.CheckName(base, opt.NameOpts); bad {
		detail := make([]string, 0, len(f.Reasons))
		for _, r := range f.Reasons {
			detail = append(detail, health.ReasonLabel(r))
		}
		out = append(out, store.HealthResult{
			Kind: string(health.KindName), Path: path, Reason: firstReason(f.Reasons),
			Detail: strings.Join(detail, "、"), Size: info.Size(), Suggest: f.Suggested,
		})
	}

	// 繁体文件名（设置页可关）
	if cfg.TraditionalEnabled {
		if has, simp := NameHasTraditional(base); has {
			out = append(out, store.HealthResult{
				Kind: string(health.KindTraditional), Path: path, Reason: "traditional",
				Detail: "文件名含繁体字", Size: info.Size(), Suggest: simp,
			})
		}
	}

	// 临时文件（清单为空表示关闭）
	if hit := health.MatchTemp(base, cfg.TempPatterns); hit != "" {
		out = append(out, store.HealthResult{
			Kind: string(health.KindTemp), Path: path, Reason: "temp",
			Detail: health.TempReason(base, hit), Size: info.Size(),
		})
	}

	// 损坏文件：ffmpeg 不可用时跳过，避免整轮扫描被一个类型拖失败
	if e.RunnerAvailable() && health.HasBrokenExt(base) {
		// v3.12.0：与视频/片头/裁剪/转码互斥。只有可能调 ffmpeg 的媒体文件才进互斥位，
		// 否则整轮扫描会被一个纯文本文件卡在入口。
		if !e.acquireHealthProbe(ctx) {
			return out, nil, ctx.Err()
		}
		res, entry, err := e.probeBrokenHealth(ctx, path, base, info.Size(), info.ModTime().UnixNano(), cache)
		e.releaseHealthProbe()
		if err != nil {
			return out, entry, err
		}
		if res != nil {
			out = append(out, *res)
		}
		if entry != nil {
			return out, entry, nil
		}
	}
	return out, nil, nil
}
