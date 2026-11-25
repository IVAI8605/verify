// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"verify/internal/applog"
	"verify/internal/health"
	"verify/internal/store"
	"verify/internal/taskguard"
)

// HealthRenameExtra 是健康改名的可选参数（2026-11-25 建议 4）。
// Force 只对「内容无法识别」的扩展名类条目生效：调用方用外部工具（ffprobe）
// 实锤真实类型后，显式指定 ForceExt 执行改名。默认零值保持原有拒绝行为。
type HealthRenameExtra struct {
	Force    bool
	ForceExt string
}

// HealthOpItem 是单条文件操作的结果。
type HealthOpItem struct {
	Path    string `json:"path"`
	NewPath string `json:"new_path,omitempty"`
	Error   string `json:"error,omitempty"`
	// Class 是结果的细分类型（2026-11-25 建议 7）：stale_path 表示源路径已失效
	// （典型为父目录被前一批改名），与真正没改成的 failed 区分，避免脚本误读。
	Class string `json:"class,omitempty"`
}

// classifyRenameFailure 在 rename 失败时区分「路径已失效」与「普通失败」。
// statErr 是失败后再对源路径做 Lstat 的结果：不存在（含父目录消失）即 stale_path。
func classifyRenameFailure(_ string, statErr error) string {
	if statErr != nil && os.IsNotExist(statErr) {
		return "stale_path"
	}
	return ""
}

// HealthOpResult 是一次批量操作的汇总。
type HealthOpResult struct {
	TaskID int64          `json:"task_id"`
	Done   int            `json:"done"`
	Failed int            `json:"failed"`
	Items  []HealthOpItem `json:"items"`
	// Reconcile 是操作后对账结果（v3.10.5）：有差异时前端列路径并给出下一步建议。
	Reconcile *ReconcileReport `json:"reconcile,omitempty"`
}

// healthRenameBatch 是一批并发改名的文件数。
// 一批内并发执行、一次事务写审计；批太小会让 SQLite 频繁开事务，
// 批太大则审计失败时整批回滚的代价高。
const healthRenameBatch = 200

// healthRenameWorkers 是改名并发度。
// 主要开销在读文件头与 rename 系统调用（都是 IO 等待），适度超过核数收益更明显；
// 上限 16 是为了避免把机械盘队列压满反而变慢。
func healthRenameWorkers() int {
	n := runtime.NumCPU() * 2
	if n < 4 {
		n = 4
	}
	if n > 16 {
		n = 16
	}
	return n
}

// healthRenameJob 是单个文件的改名计划与执行结果。
type healthRenameJob struct {
	path   string
	target string
	err    string
	class  string // rename 失败时的细分类型（如 stale_path）
	skip   bool   // 无需改动（如只改大写扩展名时扩展名本来就规范）
	done   bool
}

// RenameHealthFiles 按检测类型批量修正文件名。
//
// 所有目标名都由服务端重新计算，不接受前端传入的目标路径——
// 前端只负责确认，不能决定文件被改成什么。
//
// v3.10.2 性能改造：原实现逐条「计划 → rename → 写审计 → 删健康行」，
// 上万条时几乎全耗在等 IO 与开事务上。现在按 200 个一批：
// 批内并发计划（读文件头、Lstat）与并发 rename，成功后一次事务写审计、
// 一次事务删健康行，索引同步仍走已有的批量接口。
func (e *Engine) RenameHealthFiles(ctx context.Context, kind health.Kind, paths []string, opt health.NameOptions, onlyUppercase bool, extra ...HealthRenameExtra) *HealthOpResult {
	var ex HealthRenameExtra
	if len(extra) > 0 {
		ex = extra[0]
	}
	// v3.25.0（P1-1）：批量改名是长任务，之前没登记闸门，可与扫描/隔离并发改同一批文件。
	done, gerr := e.acquireTask(taskguard.KindRename, "按检测结果改名", taskguard.GroupIO, taskguard.ResDiskIO)
	if gerr != nil {
		return &HealthOpResult{Items: []HealthOpItem{{Error: gerr.Error()}}, Failed: 1}
	}
	defer done("done")
	unlock := e.lockWrites()
	defer unlock()
	// v3.10.10（第五轮 P1-3）：已改名的批次必须把审计与健康行写完，
	// 不能因为请求取消（用户关页面）而丢掉撤销依据。
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	res := &HealthOpResult{Items: []HealthOpItem{}}
	taskID, err := e.st.NewAuditTaskID(wctx)
	if err != nil {
		res.Items = append(res.Items, HealthOpItem{Error: "无法分配操作号: " + err.Error()})
		res.Failed = 1
		return res
	}
	res.TaskID = taskID
	// v3.10.5：改名属于不可逆写操作，动手前先按本次涉及路径拍快照（只采元数据）。
	snap := e.beginOpSnapshot(ctx, "health_rename", paths)
	// v3.10.0 SYNC-01：改名批次结束后统一写回索引，避免每条都跑一次 UpdatePath 重算指纹。
	renamed := make([][2]string, 0, len(paths))
	workers := healthRenameWorkers()

	for start := 0; start < len(paths); start += healthRenameBatch {
		if ctx.Err() != nil {
			break
		}
		end := start + healthRenameBatch
		if end > len(paths) {
			end = len(paths)
		}
		jobs := e.planHealthRenames(paths[start:end], kind, opt, onlyUppercase, workers, ex)
		e.execHealthRenames(ctx, jobs, workers)

		ok := make([]*healthRenameJob, 0, len(jobs))
		for i := range jobs {
			if jobs[i].done {
				ok = append(ok, &jobs[i])
			}
		}
		if len(ok) > 0 {
			entries := make([]store.AuditEntry, 0, len(ok))
			for _, j := range ok {
				entries = append(entries, store.AuditEntry{
					TaskID: taskID, Operation: "health_rename", OriginalPath: j.path, NewPath: j.target,
				})
			}
			if err := e.st.AddAuditBatch(wctx, entries); err != nil {
				// 审计写不进就整批退回，避免出现无法撤销的改动（与原逐条实现同语义）。
				// v3.10.4：回滚结果必须逐条检查——回滚失败的文件实际仍在目标路径，
				// 若像以前一样直接清空 ok，磁盘名已改、files 表与页面却仍指旧路径，
				// 会留下无法访问的死行。
				keep := make([]*healthRenameJob, 0, len(ok))
				for i := len(ok) - 1; i >= 0; i-- {
					j := ok[i]
					// P3-2（2026-11-11）：回滚同样不能裸改名——原位可能已被外部进程
					// 重新建了同名文件，覆盖后既无隔离也无审计。撞名按回滚失败上报。
					if rbErr := renameNoOverwrite(j.target, j.path); rbErr != nil {
						j.done = true
						j.err = "审计记录失败且回滚失败，文件已在 " + j.target + "：" + rbErr.Error()
						keep = append(keep, j)
						continue
					}
					j.done = false
					j.err = "审计记录失败，已回滚: " + err.Error()
				}
				ok = keep
			}
		}
		if len(ok) > 0 {
			olds := make([]string, 0, len(ok))
			for _, j := range ok {
				olds = append(olds, j.path)
				renamed = append(renamed, [2]string{j.path, j.target})
			}
			// v3.10.0 SYNC-03：同一路径可能同时命中多类检测（如大写扩展名 + 文件名不规范），
			// 只删当前类会留下指向旧路径的死行，改名后按路径整体清掉。
			if err := e.st.DropHealthByPaths(wctx, olds); err != nil {
				applog.Warn("改名已完成，但旧路径健康记录删除失败（%d 条，%v）", len(olds), err)
			}
		}

		// 汇总本批结果，保持与输入相同的顺序，前端失败明细才好对照。
		for i := range jobs {
			j := &jobs[i]
			switch {
			case j.skip:
				snap.Skip(j.path, "无需改名")
				continue
			case j.err != "":
				res.Failed++
				res.Items = append(res.Items, HealthOpItem{Path: j.path, Error: j.err, Class: j.class})
				if j.done {
					// 审计失败且回滚失败：文件实际已在目标路径，按改名预期对账。
					snap.MarkMoved(j.path, j.target, opExpectRenamed)
				} else {
					// 没改名成功：文件仍在原路径，不算「与预期不符」。
					snap.Skip(j.path, j.err)
				}
			case j.done:
				res.Done++
				res.Items = append(res.Items, HealthOpItem{Path: j.path, NewPath: j.target})
				snap.MarkMoved(j.path, j.target, opExpectRenamed)
			default:
				snap.Skip(j.path, "未执行")
			}
		}
	}

	// v3.10.8（P1）：用户中途取消时，未处理到的批次压根没进过上面的汇总循环，
	// 它们仍是 pending + absent，直接对账会报成「文件仍在原路径」的假差异。
	snap.SkipPending("任务已取消，未执行")

	// v3.10.4：索引同步用独立 context。用户中途取消时 ctx 已失效，
	// 但此前批次已经落盘改名，索引必须照样跟上，否则磁盘与页面不一致。
	e.syncRenamedIndex(context.Background(), renamed)
	e.bumpVersion()
	e.Bus.Publish(Event{Type: "update"})
	e.pushHealth()
	applog.Info("文件健康重命名完成：成功 %d 个，失败 %d 个（任务 %d）", res.Done, res.Failed, res.TaskID)
	// 索引写完再对账：对账只看磁盘，但此时页面状态已与磁盘对齐，报告可直接展示。
	res.Reconcile = e.finishOpSnapshot(snap)
	return res
}

// planHealthRenames 并发计算一批文件的改名计划，并在批内串行确认目标名唯一。
//
// 并发计划时两个文件可能算出同一个目标名，而 Linux 的 rename 会静默覆盖，
// 因此执行前必须按输入顺序再确认一次唯一性（同批内已占用的名字加 _1 后缀）。
func (e *Engine) planHealthRenames(paths []string, kind health.Kind, opt health.NameOptions, onlyUppercase bool, workers int, ex HealthRenameExtra) []healthRenameJob {
	jobs := make([]healthRenameJob, len(paths))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, p string) {
			defer wg.Done()
			defer func() { <-sem }()
			jobs[i] = e.planHealthRename(p, kind, opt, onlyUppercase, ex)
		}(i, p)
	}
	wg.Wait()

	used := make(map[string]bool, len(jobs))
	for i := range jobs {
		j := &jobs[i]
		if j.err != "" || j.skip || j.target == "" {
			continue
		}
		if used[j.target] {
			j.target = health.UniquePath(filepath.Dir(j.path), filepath.Base(j.target), func(candidate string) bool {
				if used[candidate] {
					return true
				}
				if strings.EqualFold(candidate, j.path) {
					return false
				}
				_, err := os.Lstat(candidate)
				return err == nil
			})
		}
		used[j.target] = true
	}
	return jobs
}

// planHealthRename 计算单个文件的目标路径；target 为空表示无需改动。
func (e *Engine) planHealthRename(p string, kind health.Kind, opt health.NameOptions, onlyUppercase bool, ex HealthRenameExtra) healthRenameJob {
	p = filepath.Clean(p)
	job := healthRenameJob{path: p}
	if reason := e.protectedReasonResolved(p); reason != "" {
		job.err = reason
		return job
	}
	if !e.ValidPath(p) {
		job.err = "路径不在允许的扫描范围内"
		return job
	}
	// 只转小写时，跳过扩展名本来就规范的文件：
	// 那 2900 条大写扩展名里混着真正内容不符的，不能一把梭全改。
	if onlyUppercase && strings.ToLower(filepath.Ext(p)) == filepath.Ext(p) {
		job.skip = true
		return job
	}
	target, errMsg := e.planRename(p, kind, opt, ex)
	if errMsg != "" {
		job.err = errMsg
		return job
	}
	job.target = target
	return job
}

// execHealthRenames 并发执行一批 rename，结果写回各自的 job。
func (e *Engine) execHealthRenames(ctx context.Context, jobs []healthRenameJob, workers int) {
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range jobs {
		j := &jobs[i]
		if j.err != "" || j.skip || j.target == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(j *healthRenameJob) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				j.err = "操作已取消"
				return
			}
			// v3.10.8：方案生成到执行之间，目标路径可能被其它进程占用。
			// POSIX rename 会静默覆盖，这里再判一次，宁可失败也不覆盖别人的文件。
			if j.target != j.path {
				if _, statErr := os.Lstat(j.target); statErr == nil {
					j.err = "目标已存在，已跳过（请重新生成方案）"
					return
				}
			}
			// v3.25.1：Lstat 与 rename 之间仍有窗口，最终落盘改用不覆盖语义，
			// 与规则改名/撤销同一口径（冲突时失败，不静默覆盖）。
			if err := renameNoOverwrite(j.path, j.target); err != nil {
				j.err = renameFailText(err)
				// 2026-11-25 建议 7：源路径已不存在记为 stale_path，供结果里单列。
				if _, statErr := os.Lstat(j.path); statErr != nil {
					j.class = classifyRenameFailure(j.path, statErr)
				}
				return
			}
			j.done = true
		}(j)
	}
	wg.Wait()
}

// syncRenamedIndex 把一批「旧路径 → 新路径」写回 files 索引（v3.10.0 SYNC-01/02）。
//
// 只更新 path/name/ext，保留指纹列；索引里查不到的新路径走 UpdatePath 补进来，
// 否则零字节、尚未扫描的文件改名后会从查重与搜索里彻底消失。
// 目录行不在 files 表内，这里不重复调用 UpdatePath(dir)——那会按文件数触发几百次通知。
func (e *Engine) syncRenamedIndex(ctx context.Context, pairs [][2]string) {
	if len(pairs) == 0 {
		return
	}
	missing, err := e.st.RenameFilePathsBatch(ctx, pairs)
	if err != nil {
		applog.Warn("改名后同步索引失败：%v", err)
		return
	}
	for _, p := range missing {
		e.UpdatePath(p)
	}
}

// planRename 计算目标路径；返回空串且无错误表示无需改动。
func (e *Engine) planRename(p string, kind health.Kind, opt health.NameOptions, ex HealthRenameExtra) (string, string) {
	dir := filepath.Dir(p)
	name := filepath.Base(p)

	suggest := ""
	if kind == health.KindExt {
		// v3.17.6（F-27）：用解析后的真实路径读文件头——校验（ValidPath）与读取之间
		// 若路径被换成指向卷外的软链接，继续用原始串就会读到未校验的文件。
		real, ok := e.ResolveFilePath(p)
		if !ok {
			return "", "路径不在允许的扫描范围内"
		}
		head, err := health.Head(real)
		if err != nil {
			return "", "读取文件头失败: " + err.Error()
		}
		f, _ := health.CheckExt(name, head, e.cfg.Get().UnifyExt)
		// v3.15.0：内容认不出来时拒绝改名。无法确定目标扩展名，猜一个等于制造错误；
		// 2026-11-25 建议 4：调用方用外部工具实锤后可带 force + 目标扩展名显式执行。
		if len(f.Reasons) > 0 && f.Reasons[0] == health.ReasonUnknown {
			if !ex.Force {
				return "", "内容类型无法识别，未做判断，不执行改名"
			}
			// force 的目标扩展名必须在已知媒体白名单里，防止路径注入/瞎猜。
			if !health.IsSafeForceExt(ex.ForceExt) {
				return "", "force 改名的目标扩展名非法或不在已知类型白名单"
			}
			suggest = strings.ToLower(strings.TrimSpace(ex.ForceExt))
		} else {
			suggest = f.SuggestExt
		}
	}

	target, ok := health.BuildTarget(kind, name, suggest, opt)
	if !ok {
		return "", ""
	}
	// 目标名与原文件同名（仅大小写不同）时，Windows 上 rename 会失败，先按需要保留。
	return health.UniquePath(dir, target, func(candidate string) bool {
		if strings.EqualFold(candidate, p) {
			return false
		}
		_, err := os.Lstat(candidate)
		return err == nil
	}), ""
}

// DryRunHealthRename 只计算改名的计划与风险，不落盘、不记审计、不同步索引
// （2026-11-25 建议 8）。返回结构与批量改名一致：每条 NewPath 是计划目标，
// Error 是风险/拒绝原因，Class 固定为 dry_run，Done/Failed 恒为 0。
func (e *Engine) DryRunHealthRename(ctx context.Context, kind health.Kind, paths []string, opt health.NameOptions, onlyUppercase bool, extra ...HealthRenameExtra) *HealthOpResult {
	res := &HealthOpResult{Items: []HealthOpItem{}}
	if ctx.Err() != nil {
		return res
	}
	var ex HealthRenameExtra
	if len(extra) > 0 {
		ex = extra[0]
	}
	jobs := e.planHealthRenames(paths, kind, opt, onlyUppercase, healthRenameWorkers(), ex)
	for _, j := range jobs {
		item := HealthOpItem{Path: j.path, Class: "dry_run"}
		switch {
		case j.err != "":
			item.Error = j.err
		case j.skip:
			item.Error = "无需改名"
		default:
			item.NewPath = j.target
		}
		res.Items = append(res.Items, item)
	}
	return res
}

// TrashHealthFiles 把文件移入隔离区（不永久删除），并记一条审计。
func (e *Engine) TrashHealthFiles(ctx context.Context, paths []string) *HealthOpResult {
	res := &HealthOpResult{Items: []HealthOpItem{}}
	rel, gerr := e.acquireTask(taskguard.KindTrash, "健康页批量隔离", taskguard.GroupIO, taskguard.ResDiskIO)
	if gerr != nil {
		res.Failed = 1
		res.Items = append(res.Items, HealthOpItem{Error: gerr.Error()})
		return res
	}
	defer rel("done")
	unlock := e.lockWrites()
	defer unlock()
	// v3.10.10（第五轮 P1-3）：文件已进隔离区后，记录与索引必须写完。
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	taskID, err := e.st.NewAuditTaskID(wctx)
	if err != nil {
		res.Failed = 1
		res.Items = append(res.Items, HealthOpItem{Error: "无法分配操作号: " + err.Error()})
		return res
	}
	res.TaskID = taskID
	// v3.10.5：健康页的清理同样属于批量写操作，先拍快照再动手。
	snap := e.beginOpSnapshot(ctx, "health_trash", paths)

	now := time.Now()
	for _, p := range paths {
		p = filepath.Clean(p)
		if !e.ValidPath(p) {
			snap.Skip(p, "路径不在允许的扫描范围内")
			res.Failed++
			res.Items = append(res.Items, HealthOpItem{Path: p, Error: "路径不在允许的扫描范围内"})
			continue
		}
		if reason := e.checkHealthTrashPath(ctx, p); reason != "" {
			snap.Skip(p, reason)
			res.Failed++
			res.Items = append(res.Items, HealthOpItem{Path: p, Error: reason})
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			snap.Skip(p, "读取文件信息失败: "+err.Error())
			res.Failed++
			res.Items = append(res.Items, HealthOpItem{Path: p, Error: "读取文件信息失败: " + err.Error()})
			continue
		}
		vol := e.VolumeOf(p)
		dst, err := e.buildTrashPath(vol, p, now)
		if err != nil {
			snap.Skip(p, err.Error())
			res.Failed++
			res.Items = append(res.Items, HealthOpItem{Path: p, Error: err.Error()})
			continue
		}
		if err := moveFile(p, dst); err != nil {
			snap.Skip(p, "移入隔离区失败: "+err.Error())
			res.Failed++
			res.Items = append(res.Items, HealthOpItem{Path: p, Error: "移入隔离区失败: " + err.Error()})
			continue
		}
		if err := e.st.AddTrash(wctx, p, dst, vol, "", "", "health", info.Size()); err != nil {
			// v4.1.2：复用统一回滚——原父目录若已被用户删掉，不能靠 moveFile 里的
			// MkdirAll 把目录悄悄重建出来（等于撤销用户的一次删除）。
			restored, note := e.rollbackTrashRecord(snap, p, dst, err)
			msg := "记录隔离区失败：" + err.Error()
			if restored {
				msg = "记录隔离区失败，已回滚: " + err.Error()
			} else {
				msg = "记录隔离区失败，" + note
			}
			res.Failed++
			res.Items = append(res.Items, HealthOpItem{Path: p, Error: msg})
			continue
		}
		e.dropIndexPath(wctx, snap, p)
		if err := e.st.AddAudit(wctx, store.AuditEntry{TaskID: taskID, Operation: "trash", OriginalPath: p, NewPath: dst}); err != nil {
			applog.Warn("移入隔离区已完成，但操作记录写入失败：%s（%v）", p, err)
		}
		snap.MarkMoved(p, dst, opExpectRelocated)
		res.Done++
		res.Items = append(res.Items, HealthOpItem{Path: p, NewPath: dst})
	}

	e.bumpVersion()
	e.Bus.Publish(Event{Type: "update"})
	e.pushHealth()
	applog.Info("文件健康清理完成：移入隔离区 %d 个，失败 %d 个（任务 %d）", res.Done, res.Failed, res.TaskID)
	res.Reconcile = e.finishOpSnapshot(snap)
	return res
}

// checkHealthTrashPath 是健康检测专用的入隔离区前校验。
//
// 与查重的 checkTrashPath 不同：不要求文件出现在 files 索引里。
// 零字节文件通常不会进入查重索引，而它恰恰是健康检测最想清理的对象；
// 若沿用查重的校验，清理动作会对这类文件永远失败。
// 索引里存在该文件时仍然校验大小与 mtime，避免移走刚被改动过的文件。
func (e *Engine) checkHealthTrashPath(ctx context.Context, p string) string {
	if e.VolumeOf(p) == "" || strings.Contains(p, "/.verify-trash/") {
		return "路径不在受管存储卷内"
	}
	// v1.42.0：健康页清理也必须遵守保护目录（BUG-03）。
	// 其他清理入口都在各自的预检里调用了 protectedReason，这里此前漏掉，
	// 导致保护目录里的 .tmp/.bak 能被成功移入隔离区。
	if reason := e.protectedReasonResolved(p); reason != "" {
		return reason
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return "文件已不存在或不是普通文件"
	}
	row, err := e.st.GetFileByPath(ctx, p)
	if err == nil && row != nil && (fi.Size() != row.Size || fi.ModTime().UnixNano() != row.Mtime) {
		return "文件在索引后被修改，请重新扫描"
	}
	return ""
}

// UndoHealthTask 按任务号撤销重命名；隔离区操作请用隔离区的恢复功能。
//
// 逆序回放：后发生的改名先还原，避免 A→B、B→C 这类链式改名冲突。
func (e *Engine) UndoHealthTask(ctx context.Context, taskID int64) (*HealthOpResult, error) {
	unlock := e.lockWrites()
	defer unlock()
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	entries, err := e.st.ListAuditByTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("没有可撤销的操作记录")
	}

	res := &HealthOpResult{TaskID: taskID, Items: []HealthOpItem{}}
	// v3.10.0 SYNC-02：撤销也要把索引改回原路径，否则磁盘还原了、库里还挂着改后名。
	restored := make([][2]string, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		it := entries[i]
		// fanjian 与 health_rename 同属健康页改名类审计（OriginalPath→NewPath），撤销方式一致；
		// 文件健康页的「转简体」调用的就是 ConvertNames，撤销必须能回放它。
		// rename 仅为兼容老库里健康页写入的历史行；新写入已改用 health_rename，
		// 避免与组内改名/规则引擎的同名 operation 混淆（见 LastAuditTaskID）。
		if it.Operation != "health_rename" && it.Operation != "rename" && it.Operation != "fanjian" {
			res.Items = append(res.Items, HealthOpItem{Path: it.OriginalPath, Error: "该操作不支持在这里撤销，请到隔离区恢复"})
			continue
		}
		if _, err := os.Lstat(it.NewPath); err != nil {
			// 2026-11-25 建议 9：改后路径已不存在，且原路径已恢复 → 说明这条
			// 已经撤销过了，幂等返回「已撤销」，方便脚本重试不误判为失败。
			if _, origErr := os.Lstat(it.OriginalPath); origErr == nil {
				res.Done++
				res.Items = append(res.Items, HealthOpItem{
					Path: it.OriginalPath, NewPath: it.NewPath, Class: "already_undone",
				})
				continue
			}
			res.Failed++
			res.Items = append(res.Items, HealthOpItem{Path: it.OriginalPath, Error: "改动后的文件已不存在"})
			continue
		}
		if origInfo, err := os.Lstat(it.OriginalPath); err == nil {
			// v4.1.2：大小写不敏感卷（CIFS/NTFS）上「只改大小写」的改名，撤销时
			// OriginalPath 与 NewPath 是同一个 inode，Lstat 必然命中，旧逻辑会误判
			// 「原文件名已被占用」直接放弃，撤销功能在这类卷上永远失效。
			// 与 undoRenameTarget 同口径：同 inode 视为未占用，交给 renameNoOverwrite
			// 按 SameFile 放行；只有真被别的文件占了才拒绝。
			newInfo, nerr := os.Lstat(it.NewPath)
			if nerr != nil || !os.SameFile(origInfo, newInfo) {
				res.Failed++
				res.Items = append(res.Items, HealthOpItem{Path: it.OriginalPath, Error: "原文件名已被占用"})
				continue
			}
		}
		if err := renameNoOverwrite(it.NewPath, it.OriginalPath); err != nil {
			res.Failed++
			res.Items = append(res.Items, HealthOpItem{Path: it.OriginalPath, Error: err.Error()})
			continue
		}
		// 改后路径已不存在，健康结论一并作废（与 SYNC-03 同理，按路径清干净）。
		if err := e.st.DropHealthByPath(wctx, it.NewPath); err != nil {
			applog.Warn("撤销改名已完成，但健康记录删除失败：%s（%v）", it.NewPath, err)
		}
		restored = append(restored, [2]string{it.NewPath, it.OriginalPath})
		res.Done++
		res.Items = append(res.Items, HealthOpItem{Path: it.NewPath, NewPath: it.OriginalPath})
	}

	if res.Done > 0 {
		// v3.10.8（P2-7）：磁盘已还原，索引回写不能跟着请求取消，否则会留下旧路径的活跃行
		syncCtx, cancelSync := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		e.syncRenamedIndex(syncCtx, restored)
		cancelSync()
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
		e.pushHealth()
	}
	applog.Info("撤销文件健康操作：还原 %d 个，失败 %d 个（任务 %d）", res.Done, res.Failed, taskID)
	return res, nil
}

// HealthLastAuditTask 返回最近一次可撤销的任务号，0 表示没有。
func (e *Engine) HealthLastAuditTask(ctx context.Context) (int64, error) {
	return e.st.LastAuditTaskID(ctx)
}
