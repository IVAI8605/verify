// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"verify/internal/applog"
	"verify/internal/health"
	"verify/internal/store"
	"verify/internal/taskguard"
)

// runEmptyDirScan 空目录检测主流程（v1.14.0 引入 10.2）：
// 一次后序遍历产出结果，落 health_results（kind=empty_dir，path 存目录路径）。
func (e *Engine) runEmptyDirScan(ctx context.Context, kind health.Kind, taskID int64, roots []string) {
	if err := e.st.ClearHealthResults(ctx, string(kind)); err != nil {
		applog.Warn("清空空目录旧结果失败: %v", err)
	}
	skip := func(p string) bool {
		// 隔离区、排除目录、保护目录及其子树一律跳过（设计文档 10.2.1）；
		// 含这些子目录的父目录会被视为非空，不会被一起清理。
		return e.IsExcludedPath(p) || e.IsProtected(p) || health.IsTrashDirName(filepath.Base(p))
	}
	// v3.10.2：按物理卷并行遍历，不同硬盘同时走树。
	dirs, err := findEmptyDirsParallel(ctx, roots, skip)
	if err != nil {
		e.finishHealth(kind, taskID, HealthFailed, 0, 0, 0, "遍历目录失败: "+err.Error())
		return
	}
	if len(dirs) == 0 {
		e.finishHealth(kind, taskID, HealthDone, 0, 0, 0, "")
		return
	}
	e.setHealthTotal(taskID, int64(len(dirs)))
	const batch = 200
	procssd := 0
	for i := 0; i < len(dirs); i += batch {
		end := i + batch
		if end > len(dirs) {
			end = len(dirs)
		}
		var rows []store.HealthResult
		for _, d := range dirs[i:end] {
			info, err := os.Stat(d)
			var size int64
			if err == nil {
				size = info.Size()
			}
			rows = append(rows, store.HealthResult{
				Kind:    string(kind),
				Path:    d,
				Reason:  "empty_dir",
				Reasons: []string{ReasonEmptyDir},
				Detail:  health.ReasonLabel(ReasonEmptyDir),
				Size:    size,
			})
			procssd++
		}
		if err := e.st.AppendHealthResults(ctx, string(kind), rows); err != nil {
			applog.Warn("写入空目录结果失败: %v", err)
		}
		e.setHealthProgress(taskID, int64(procssd), dirs[end-1])
		e.pushHealth()
	}
	applog.Info("空目录检测完成：命中 %d 个", len(dirs))
	e.finishHealth(kind, taskID, HealthDone, int64(len(dirs)), int64(len(dirs)), 0, "")
}

// ReasonEmptyDir 是空目录的原因标识（health_results.reason）。
const ReasonEmptyDir = "empty_dir"

// EmptyDirResult 是一次空目录清理的结果。
type EmptyDirResult struct {
	Removed []string          `json:"removed"`
	Failed  []string          `json:"failed"`
	Errors  map[string]string `json:"errors"`
	// Conflict 是闸门拒绝原因（v3.25.2 P3-4）：非空表示本次一个目录都没动。
	Conflict string `json:"conflict,omitempty"`
	// Reconcile 是操作后对账结果（v3.10.5）：有差异时前端列路径并给出下一步建议。
	Reconcile *ReconcileReport `json:"reconcile,omitempty"`
}

// EmptyDirClean 批量清理空目录（设计文档 10.2.3/10.2.4）：
// 按**路径深度降序**执行（先删最深的叶子，父目录随之变空但本轮不再重复处理）；
// 每个执行前重验：目录仍存在、仍为空、不在保护目录——预检与执行双拦（同 v1.9 模式）。
// 删除方式：整目录 os.Rename 移入 .verify-trash（跨卷报错跳过该项），可恢复。
func (e *Engine) EmptyDirClean(ctx context.Context, dirs []string) *EmptyDirResult {
	res := &EmptyDirResult{Errors: map[string]string{}}
	if len(dirs) == 0 {
		return res
	}
	// v3.25.2（P3-4）：空目录清理会逐个目录搬到隔离区并写库，与扫描 / 转码同属磁盘写，
	// 原先只拿 lockWrites（进程内互斥），闸门层面是空档，扫描进行中照样能并发搬目录。
	doneGuard, gerr := e.acquireTask(taskguard.KindTrash, "空目录清理", taskguard.GroupIO, taskguard.ResDiskIO)
	if gerr != nil {
		res.Conflict = gerr.Error()
		return res
	}
	defer doneGuard("done")
	unlock := e.lockWrites()
	defer unlock()
	// v3.10.10（第五轮 P1-3）：目录已移走后，记录与索引必须写完。
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	// 深度降序：先处理子目录
	sorted := append([]string{}, dirs...)
	sort.Slice(sorted, func(i, j int) bool {
		return len(strings.Split(filepath.ToSlash(sorted[i]), "/")) > len(strings.Split(filepath.ToSlash(sorted[j]), "/"))
	})

	taskID, err := e.st.NewAuditTaskID(wctx)
	if err != nil {
		applog.Warn("获取审计任务号失败：%v", err)
	}
	// v3.10.5：空目录清理会挪走目录，动手前先按本次目录拍快照（每个仅一次 Lstat）。
	snap := e.beginOpSnapshot(ctx, "emptydir", sorted)
	now := time.Now()
	for _, d := range sorted {
		d = filepath.Clean(d)
		// 重验 1：存在且是目录
		fi, err := os.Lstat(d)
		if err != nil || !fi.IsDir() {
			snap.Skip(d, "目录已不存在")
			res.Failed = append(res.Failed, d)
			res.Errors[d] = "目录已不存在"
			continue
		}
		// 重验 2：仍为空
		entries, rerr := os.ReadDir(d)
		if rerr != nil || len(entries) > 0 {
			snap.Skip(d, "目录已非空（可能被应用重建），跳过")
			res.Failed = append(res.Failed, d)
			res.Errors[d] = "目录已非空（可能被应用重建），跳过"
			continue
		}
		// 重验 3：保护目录（预检之后配置可能变化，执行前必须再拦一次）
		if reason := e.protectedReason(d); reason != "" {
			snap.Skip(d, reason)
			res.Failed = append(res.Failed, d)
			res.Errors[d] = reason
			continue
		}
		// v3.10.6：VolumeOf 只做字符串前缀匹配，父目录软链接可把整棵子树指到卷外；
		// 改用 ValidDirPath（Clean + EvalSymlinks + 卷前缀重判），解析后必须仍是卷内目录。
		if !e.ValidDirPath(d) {
			snap.Skip(d, "路径不在受管存储卷内")
			res.Failed = append(res.Failed, d)
			res.Errors[d] = "路径不在受管存储卷内"
			continue
		}
		vol := e.VolumeOf(d)
		dst, err := e.buildTrashPath(vol, d, now)
		if err != nil {
			snap.Skip(d, err.Error())
			res.Failed = append(res.Failed, d)
			res.Errors[d] = err.Error()
			continue
		}
		// P3-2（2026-11-11）：buildTrashPath 只做 Lstat 预检，预检到改名之间仍可能被外部
		// 进程抢占同名路径（含 1000 次用尽后直接返回时间戳名）；统一走 renameNoOverwrite。
		if err := renameNoOverwrite(d, dst); err != nil {
			snap.Skip(d, "移入隔离区失败："+err.Error())
			res.Failed = append(res.Failed, d)
			res.Errors[d] = "移入隔离区失败：" + err.Error()
			continue
		}
		if err := e.st.AddTrash(wctx, d, dst, vol, "", "", ReasonEmptyDir, 0); err != nil {
			// 回滚失败时目录已在隔离区但没有记录，必须进差异清单。
			// 回滚同样不能覆盖：目录若已被外部进程重新创建，宁可报差异也不要静默替换。
			if rbErr := renameNoOverwrite(dst, d); rbErr != nil {
				snap.MarkProblem(d, opExpectRelocated, dst, "记录隔离区失败且回滚失败，目录可能已在隔离区但没有恢复记录："+rbErr.Error())
			} else {
				snap.Skip(d, "记录隔离区失败，已回滚："+err.Error())
			}
			res.Failed = append(res.Failed, d)
			res.Errors[d] = "记录隔离区失败：" + err.Error()
			continue
		}
		snap.MarkMoved(d, dst, opExpectRelocated)
		if err := e.st.AddAudit(wctx, store.AuditEntry{
			TaskID:       taskID,
			Operation:    "rmdir",
			OriginalPath: d,
			NewPath:      dst,
		}); err != nil {
			applog.Warn("空目录已移入隔离区，但操作记录写入失败：%s（%v）", d, err)
		}
		res.Removed = append(res.Removed, d)
	}
	if len(res.Removed) > 0 {
		// v3.10.10（第五轮 P2-8）：目录已不在原位置，empty_dir 结论要一并删除，
		// 否则健康页刷新后仍列出它们，再点清理只会收到「目录已不存在」。
		if err := e.st.DeleteHealthResultsByPaths(wctx, string(health.KindEmptyDir), res.Removed); err != nil {
			applog.Warn("空目录已清理，但健康结论删除失败（%d 条）：%v", len(res.Removed), err)
		}
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
		applog.Info("空目录清理完成：%d 个移入隔离区", len(res.Removed))
	}
	res.Reconcile = e.finishOpSnapshot(snap)
	return res
}

// EmptyDirValidate 供清理前预览：逐项校验并返回可执行项（不产生变更）。
// v3.9.10 备注：当前全项目零调用，保留是给「空目录清理前预检接口」用的完整实现，
// 接入前不要把它当成已生效的界面能力。
func (e *Engine) EmptyDirValidate(ctx context.Context, dirs []string) *EmptyDirResult {
	res := &EmptyDirResult{Errors: map[string]string{}}
	for _, d := range dirs {
		d = filepath.Clean(d)
		fi, err := os.Lstat(d)
		if err != nil || !fi.IsDir() {
			res.Failed = append(res.Failed, d)
			res.Errors[d] = "目录已不存在"
			continue
		}
		entries, rerr := os.ReadDir(d)
		if rerr != nil || len(entries) > 0 {
			res.Failed = append(res.Failed, d)
			res.Errors[d] = "目录已非空"
			continue
		}
		if reason := e.protectedReason(d); reason != "" {
			res.Failed = append(res.Failed, d)
			res.Errors[d] = reason
			continue
		}
		if e.VolumeOf(d) == "" {
			res.Failed = append(res.Failed, d)
			res.Errors[d] = "路径不在受管卷内"
			continue
		}
		res.Removed = append(res.Removed, d)
	}
	return res
}
