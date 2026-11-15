// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 本文件是 v3.2.0 的预设清理计划执行器（设计文档 §3）。
//
// 设计红线：
//   - 只把多余文件移入隔离区，绝不永久删除；真正删除由用户在隔离区确认；
//   - 每一组至少保留一个文件；保护目录由 TrashFiles 逐项二次拦截；
//   - 与扫描/健康检测互斥，扫描在跑时计划顺延到下一次 tick；
//   - 计划执行错误写入 last_error，不阻断其他计划。

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"verify/internal/applog"
	"verify/internal/config"
	"verify/internal/rules"
	"verify/internal/store"
	"verify/internal/taskguard"
)

// CleanupRunResult 是一次计划执行的结果摘要。
type CleanupRunResult struct {
	PlanID  int64    `json:"plan_id"`
	Groups  int      `json:"groups"`
	Trashed int      `json:"trashed"`
	Freed   int64    `json:"freed"` // 移入隔离区的文件总大小，不是真正释放
	Skipped int      `json:"skipped"`
	Errors  []string `json:"errors,omitempty"`
}

// ValidateCleanupSchedule 供设置页保存前校验时间表达式（web 层不重复实现解析）。
func ValidateCleanupSchedule(raw string) error {
	_, err := parseCleanupSchedule(raw)
	return err
}

// cleanupSchedule 是解析后的计划时间表达式。
type cleanupSchedule struct {
	kind     string // daily / weekly / interval
	weekday  time.Weekday
	hour     int
	minute   int
	interval time.Duration
}

var cleanupWeekdays = map[string]time.Weekday{
	"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
}

// parseCleanupSchedule 支持三种格式：
//   - daily HH:MM
//   - weekly <mon..sun> HH:MM
//   - interval N（秒，最小 60）
func parseCleanupSchedule(raw string) (cleanupSchedule, error) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(raw)))
	if len(fields) < 2 {
		return cleanupSchedule{}, fmt.Errorf("时间格式应为 daily HH:MM、weekly fri HH:MM 或 interval 秒数")
	}
	switch fields[0] {
	case "daily":
		h, m, err := parseHHMM(fields[1])
		if err != nil {
			return cleanupSchedule{}, err
		}
		return cleanupSchedule{kind: "daily", hour: h, minute: m}, nil
	case "weekly":
		if len(fields) < 3 {
			return cleanupSchedule{}, fmt.Errorf("weekly 格式应为 weekly fri HH:MM")
		}
		wd, ok := cleanupWeekdays[fields[1]]
		if !ok {
			return cleanupSchedule{}, fmt.Errorf("不认识星期「%s」，用 mon/tue/wed/thu/fri/sat/sun", fields[1])
		}
		h, m, err := parseHHMM(fields[2])
		if err != nil {
			return cleanupSchedule{}, err
		}
		return cleanupSchedule{kind: "weekly", weekday: wd, hour: h, minute: m}, nil
	case "interval":
		sec, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || sec < 60 {
			return cleanupSchedule{}, fmt.Errorf("interval 的秒数必须 ≥60")
		}
		return cleanupSchedule{kind: "interval", interval: time.Duration(sec) * time.Second}, nil
	default:
		return cleanupSchedule{}, fmt.Errorf("不认识的时间类型「%s」", fields[0])
	}
}

func parseHHMM(s string) (int, int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("时间应为 HH:MM")
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("时间应为 00:00–23:59")
	}
	return h, m, nil
}

// planDue 判断计划是否已到执行时间。
func planDue(p store.CleanupPlan, now time.Time) bool {
	sc, err := parseCleanupSchedule(p.Schedule)
	if err != nil {
		return false
	}
	switch sc.kind {
	case "interval":
		return p.LastRun == 0 || now.Unix()-p.LastRun >= int64(sc.interval.Seconds())
	case "daily":
		occ := time.Date(now.Year(), now.Month(), now.Day(), sc.hour, sc.minute, 0, 0, now.Location())
		if now.Before(occ) {
			occ = occ.AddDate(0, 0, -1)
		}
		return p.LastRun < occ.Unix()
	case "weekly":
		occ := mostRecentWeekly(now, sc)
		return p.LastRun < occ.Unix()
	}
	return false
}

// mostRecentWeekly 返回不晚于 now 的最近一次「每周 weekday HH:MM」时间点。
func mostRecentWeekly(now time.Time, sc cleanupSchedule) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), sc.hour, sc.minute, 0, 0, now.Location())
	days := (int(now.Weekday()) - int(sc.weekday) + 7) % 7
	if days == 0 && now.Before(t) {
		days = 7
	}
	return t.AddDate(0, 0, -days)
}

// StartCleanupLoop 每分钟检查一次是否有计划到点。
func (e *Engine) StartCleanupLoop(ctx context.Context) {
	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.cleanupTick(ctx)
		}
	}
}

// cleanupTick 在系统空闲时按顺序执行到点的计划。
func (e *Engine) cleanupTick(ctx context.Context) {
	if e.cleanupRun.Load() || e.IsRunning() || e.HealthRunning() || e.userPaused.Load() {
		return
	}
	plans, err := e.st.ListCleanupPlans(ctx)
	if err != nil {
		applog.Warn("读取清理计划失败：%s", err.Error())
		return
	}
	now := time.Now()
	for _, p := range plans {
		if !p.Enabled || !planDue(p, now) {
			continue
		}
		// 每执行一条前重新确认互斥条件，避免前一条刚启动扫描。
		if e.IsRunning() || e.HealthRunning() || e.userPaused.Load() || e.cleanupRun.Load() {
			return
		}
		if _, err := e.RunCleanupPlan(ctx, p.ID); err != nil {
			applog.Warn("清理计划「%s」执行失败：%s", p.Name, err.Error())
		}
	}
}

// RunCleanupPlan 执行一条计划（手动与定时共用）。执行结束会把结果写回计划表。
func (e *Engine) RunCleanupPlan(ctx context.Context, id int64) (*CleanupRunResult, error) {
	if !e.cleanupRun.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("已有清理计划正在执行")
	}
	defer e.cleanupRun.Store(false)
	// v3.18.0：清理计划要移文件，独占磁盘。
	doneGuard, gerr := e.acquireTask(taskguard.KindCleanup, "", taskguard.GroupIO,
		taskguard.ResDiskIO, taskguard.ResDBWrite)
	if gerr != nil {
		return nil, gerr
	}
	defer doneGuard("done")
	p, err := e.st.GetCleanupPlan(ctx, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("清理计划不存在")
	}
	// v3.10.8（P1-2）：走 IsRunning 而不是直接读字段；StartScanMode 已加 cleanupRun 判断，
	// 与这里形成双向互斥。
	if e.IsRunning() {
		return nil, fmt.Errorf("扫描进行中，稍后再试")
	}
	// v3.10.10（第五轮 P2-3）：开始执行就先落 last_run。以前只在结束时写，
	// 中途崩溃/重启会让下次调度以为「从没跑过」，立刻重跑同一计划。
	if err := e.st.UpdateCleanupPlanResult(ctx, id, time.Now().Unix(), "执行中", ""); err != nil {
		applog.Warn("记录清理计划开始时间失败（%d）：%v", id, err)
	}
	res, runErr := e.executeCleanupPlan(ctx, p)
	now := time.Now().Unix()
	result := fmt.Sprintf("移入隔离区 %d 个文件，可回收 %s", res.Trashed, cleanupSize(res.Freed))
	errText := ""
	if runErr != nil {
		result = "执行失败"
		errText = runErr.Error()
		applog.Warn("清理计划「%s」失败：%s", p.Name, runErr.Error())
	}
	if err := e.st.UpdateCleanupPlanResult(ctx, id, now, result, errText); err != nil {
		applog.Warn("写入清理计划结果失败：%s", err.Error())
	}
	if runErr != nil {
		return res, runErr
	}
	applog.Info("清理计划「%s」完成：%s", p.Name, result)
	return res, nil
}

// executeCleanupPlan 收集分组 → 每组保留一个 → 其余移入隔离区。
func (e *Engine) executeCleanupPlan(ctx context.Context, p *store.CleanupPlan) (*CleanupRunResult, error) {
	groups, err := e.collectCleanupGroups(ctx, p)
	if err != nil {
		return &CleanupRunResult{PlanID: p.ID}, err
	}
	res := &CleanupRunResult{PlanID: p.ID}
	minCount := p.MinCount
	if minCount < 2 {
		minCount = 2
	}
	// v3.17.3（F-06）：MaxKeepGB 历史上直接来自 JSON，超过 int64 可容纳的 GB 数时
	// 乘法会溢出成负数，`capBytes > 0` 失效等于「不限」。溢出时按不限处理并留日志，
	// 宁可少拦也不要把上限算成一个负数去误拦。
	const gbBytes = int64(1024 * 1024 * 1024)
	capBytes := int64(0)
	if p.MaxKeepGB > 0 {
		if p.MaxKeepGB > math.MaxInt64/gbBytes {
			applog.Warn("清理计划「%s」的容量上限 %d GB 超出可计算范围，本次按不限处理", p.Name, p.MaxKeepGB)
		} else {
			capBytes = p.MaxKeepGB * gbBytes
		}
	}
	planned := map[string]bool{}
	for _, g := range groups {
		if ctx.Err() != nil {
			res.Errors = append(res.Errors, "已取消")
			break
		}
		if capBytes > 0 && res.Freed >= capBytes {
			break
		}
		keep, ok := pickCleanupKeep(g.Files, p.RetainStrategy)
		if !ok {
			continue
		}
		var paths []string
		var sizes int64
		for i := range g.Files {
			if i == keep || planned[g.Files[i].Path] {
				continue
			}
			if reason := e.protectedReason(g.Files[i].Path); reason != "" {
				res.Skipped++
				continue
			}
			if capBytes > 0 && res.Freed+sizes+g.Files[i].Size > capBytes {
				continue
			}
			paths = append(paths, g.Files[i].Path)
			sizes += g.Files[i].Size
		}
		if len(paths) < 1 {
			continue
		}
		if len(g.Files) < minCount {
			continue
		}
		// v3.10.10（第五轮 P1-5）：以前传 nil 会整体跳过「同组至少保留一份」。
		// 计划算好 keep 之后、真正移走之前，用户可能在另一个标签把 keep 隔离掉，
		// 于是计划把剩下的全清空。这里把整组交给引擎按实时索引复核。
		members := make([]string, 0, len(g.Files))
		for i := range g.Files {
			members = append(members, g.Files[i].Path)
		}
		tr, terr := e.trashFilesLocked(ctx, paths, []TrashGroup{{Key: g.Key, Members: members}})
		if terr != nil {
			res.Errors = append(res.Errors, terr.Error())
			continue
		}
		// 只有真正成功的路径才计数量与容量，失败项保留在原处。
		success := map[string]bool{}
		for _, moved := range tr.Moved {
			success[moved] = true
			planned[moved] = true
		}
		for _, f := range g.Files {
			if success[f.Path] {
				res.Trashed++
				res.Freed += f.Size
			}
		}
		res.Skipped += len(tr.Failed)
		res.Groups++
		res.Errors = append(res.Errors, tr.Errors...)
	}
	return res, nil
}

// ruleCleanupGroups 用与结果页相同的规则引擎为清理计划分组。
// 媒体规则（视频/音频/图片等）暂不支持，返回错误让调用方回退旧模式。
func (e *Engine) ruleCleanupGroups(ctx context.Context, p *store.CleanupPlan, r *config.SearchRules, cfg *config.Config) ([]store.Group, error) {
	switch r.Base {
	case config.RuleBaseVideo, config.RuleBaseIntro, config.RuleBaseAudio,
		config.RuleBasePhoto, config.RuleBaseFolder, config.RuleBaseFuzzy, config.RuleBaseSemantic:
		return nil, fmt.Errorf("清理计划暂不支持媒体/文件类型规则")
	}
	files := make([]store.FileRow, 0, 4096)
	if err := e.st.WalkActiveFiles(ctx, p.MinSize, func(f store.FileRow) error {
		files = append(files, f)
		return nil
	}); err != nil {
		return nil, err
	}
	groups, _ := rules.Build(ctx, files, r, cfg.Volumes)
	if p.Extensions != "" {
		exts := map[string]bool{}
		for _, x := range strings.Split(p.Extensions, ",") {
			if x = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(x, "."))); x != "" {
				exts[x] = true
			}
		}
		kept := groups[:0]
		for _, g := range groups {
			for _, f := range g.Files {
				if exts[strings.ToLower(strings.TrimPrefix(f.Ext, "."))] {
					kept = append(kept, g)
					break
				}
			}
		}
		groups = kept
	}
	return groups, nil
}

// collectCleanupGroups 按计划模式收集候选分组。
// all = 精确 + 同名 + 相似视频 + 重复音频，全部只作为候选，执行时仍只移入隔离区。
func (e *Engine) collectCleanupGroups(ctx context.Context, p *store.CleanupPlan) ([]store.Group, error) {
	cfg := e.cfg.Get()
	// v3.9.0：计划里保存了搜索规则时，优先按规则在内存中分组（与结果页同一套逻辑）。
	if strings.TrimSpace(p.RulesJSON) != "" {
		var r config.SearchRules
		if err := json.Unmarshal([]byte(p.RulesJSON), &r); err != nil {
			applog.Warn("清理计划「%s」的规则解析失败，回退旧模式：%s", p.Name, err.Error())
		} else if groups, err := e.ruleCleanupGroups(ctx, p, &r, cfg); err == nil {
			return groups, nil
		} else {
			applog.Warn("清理计划「%s」按规则分组失败，回退旧模式：%s", p.Name, err.Error())
		}
	}
	q := store.GroupQuery{
		MinSize:  p.MinSize,
		Roots:    cfg.Volumes,
		Page:     1,
		PageSize: 200,
		Sort:     "waste",
		Order:    "desc",
	}
	if p.Extensions != "" {
		for _, e2 := range strings.Split(p.Extensions, ",") {
			if e2 = strings.TrimSpace(strings.TrimPrefix(e2, ".")); e2 != "" {
				q.Exts = append(q.Exts, e2)
			}
		}
	}
	var out []store.Group
	mode := p.MatchMode
	if mode == "" {
		mode = "all"
	}
	if mode == "all" || mode == "exact" {
		g, err := e.pagedGroups(ctx, q, true)
		if err != nil {
			return nil, err
		}
		out = append(out, g...)
	}
	if mode == "all" || mode == "name" {
		g, err := e.pagedGroups(ctx, q, false)
		if err != nil {
			return nil, err
		}
		out = append(out, g...)
	}
	if mode == "all" || mode == "video" {
		g, err := e.VideoGroups(ctx, true)
		if err != nil {
			return nil, err
		}
		out = append(out, filterCleanupGroups(g, p, cfg.Volumes)...)
	}
	if mode == "all" || mode == "audio" {
		g, _, err := e.st.AudioGroups(ctx, cfg.EffectiveAudioThreshold())
		if err != nil {
			return nil, err
		}
		out = append(out, filterCleanupGroups(g, p, cfg.Volumes)...)
	}
	return out, nil
}

// pagedGroups 翻页取精确/同名分组；单次最多 10000 组，防止计划执行时无限占内存。
func (e *Engine) pagedGroups(ctx context.Context, q store.GroupQuery, exact bool) ([]store.Group, error) {
	var out []store.Group
	for page := 1; page <= 50; page++ {
		q.Page = page
		var gs []store.Group
		var err error
		if exact {
			gs, _, err = e.st.ExactGroups(ctx, q)
		} else {
			gs, _, err = e.st.NameGroups(ctx, q)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, gs...)
		if len(gs) < q.PageSize {
			break
		}
	}
	return out, nil
}

// filterCleanupGroups 对内存分组（视频/音频）按计划条件与扫描根裁剪。
func filterCleanupGroups(groups []store.Group, p *store.CleanupPlan, roots []string) []store.Group {
	minCount := p.MinCount
	if minCount < 2 {
		minCount = 2
	}
	exts := map[string]bool{}
	for _, x := range strings.Split(p.Extensions, ",") {
		if x = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(x, "."))); x != "" {
			exts[x] = true
		}
	}
	out := make([]store.Group, 0, len(groups))
	for _, g := range groups {
		var files []store.FileRow
		for _, f := range g.Files {
			if p.MinSize > 0 && f.Size < p.MinSize {
				continue
			}
			if len(exts) > 0 && !exts[f.Ext] {
				continue
			}
			if !underRoots(f.Path, roots) {
				continue
			}
			files = append(files, f)
		}
		if len(files) < minCount {
			continue
		}
		g.Files = files
		g.Count = len(files)
		var total, max int64
		for _, f := range files {
			total += f.Size
			if f.Size > max {
				max = f.Size
			}
		}
		g.TotalSize = total
		g.Waste = total - max
		out = append(out, g)
	}
	return out
}

// underRoots 判断文件是否落在任一扫描根内；空 roots 表示不限（全部通过）。
// v1.42：前缀匹配逻辑收敛到 pathUnderAny（会折叠 ./.. 并统一分隔符），
// 这里只补「空 roots=true」这层语义，避免与 protect 各写一份前缀判定而漂移。
func underRoots(path string, roots []string) bool {
	if len(roots) == 0 {
		return true
	}
	return pathUnderAny(path, roots)
}

// pickCleanupKeep 按保留策略返回应保留的文件下标。
func pickCleanupKeep(files []store.FileRow, strategy string) (int, bool) {
	if len(files) == 0 {
		return 0, false
	}
	best := 0
	score := func(f store.FileRow) int64 {
		switch strategy {
		case "largest_size":
			return f.Size
		case "best_codec":
			// 分辨率优先，其次码率，再次大小；同分用更新时间。
			return int64(f.Width)*int64(f.Height)*1_000_000 + f.BitRate
		default: // modify_newest
			return f.Mtime
		}
	}
	for i := 1; i < len(files); i++ {
		if score(files[i]) > score(files[best]) ||
			(score(files[i]) == score(files[best]) && files[i].Mtime > files[best].Mtime) {
			best = i
		}
	}
	return best, true
}

// cleanupSize 把字节数转成便于阅读的容量描述。
func cleanupSize(n int64) string {
	const gb = int64(1) << 30
	const mb = int64(1) << 20
	switch {
	case n >= gb:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	default:
		return fmt.Sprintf("%d KB", n/1024)
	}
}
