// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"verify/internal/applog"
	"verify/internal/health"
	"verify/internal/store"
	"verify/internal/taskguard"
)

// MoveResult 是一次批量移动的结果。
type MoveResult struct {
	TaskID int64             `json:"task_id,omitempty"`
	Done   []MoveDone        `json:"done"`
	Failed []string          `json:"failed"`
	Errors map[string]string `json:"errors"`
	// CrossVol 是其中走「跨卷复制+校验+删源」的条数（v4.2.0）。
	// 跨卷比同卷 rename 慢很多，界面据此提示用户耐心等待。
	CrossVol int `json:"cross_vol,omitempty"`
}

// MoveDone 是一个成功移动的条目。
type MoveDone struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// moveTargetDir 校验目标目录合法性并返回（设计文档 10.4.2）。
func (e *Engine) moveTargetDir(targetDir string) string {
	if targetDir == "" {
		return "缺少目标目录"
	}
	// 必须在受管卷内且是目录（三步防线：Clean+EvalSymlinks+卷前缀重判）
	if !e.ValidDirPath(targetDir) {
		return "目标目录不存在或不在受管卷内"
	}
	fi, err := os.Stat(targetDir)
	if err != nil || !fi.IsDir() {
		return "目标目录不存在"
	}
	return ""
}

// moveCheck 单项预检（设计文档 10.4.2），返回空串表示通过。
func (e *Engine) moveCheck(src, targetDir string) string {
	fi, err := os.Lstat(src)
	if err != nil || !fi.Mode().IsRegular() {
		return "源文件不存在或不是普通文件"
	}
	if e.VolumeOf(src) == "" {
		return "源文件不在受管卷内"
	}
	// v4.2.0：跨卷不再拒绝，执行时降级为「复制+校验+删源」（见 copy_verify.go）。
	// 保护目录内禁止移出（决策 6）
	if reason := e.protectedReasonResolved(src); reason != "" {
		return reason
	}
	// 隔离区内的文件不参与移动
	if strings.Contains(filepath.ToSlash(src), "/.verify-trash/") {
		return "隔离区内的文件不支持移动"
	}
	// 目标与源同目录时无意义
	if filepath.Dir(src) == targetDir {
		return "源文件已在目标目录内"
	}
	return ""
}

// MovePreview 逐项预检，不产生变更。
func (e *Engine) MovePreview(ctx context.Context, targetDir string, paths []string) *MoveResult {
	res := &MoveResult{Errors: map[string]string{}}
	if msg := e.moveTargetDir(targetDir); msg != "" {
		for _, p := range paths {
			res.Failed = append(res.Failed, p)
			res.Errors[p] = msg
		}
		return res
	}
	for _, p := range paths {
		if reason := e.moveCheck(p, targetDir); reason != "" {
			res.Failed = append(res.Failed, p)
			res.Errors[p] = reason
			continue
		}
		res.Done = append(res.Done, MoveDone{From: p, To: filepath.Join(targetDir, filepath.Base(p))})
	}
	return res
}

// MoveFiles 批量移动文件到指定目录（设计文档 10.4，第一版仅同卷 os.Rename）：
// 冲突追加 _1/_2 绝不覆盖（复用 UniquePath）；audit_log 按 task 可撤销（逆序 rename 回原位）；
// 落库同步走 syncMovedIndex（只改 path/name/ext，不删行——删行会丢掉用户打标与媒体指纹）。
func (e *Engine) MoveFiles(ctx context.Context, targetDir string, paths []string) *MoveResult {
	unlock := e.lockWrites()
	defer unlock()
	// v3.10.10（第五轮 P1-3）：同批移动共用一份脱离请求取消的上下文。
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	res := &MoveResult{Errors: map[string]string{}}
	pairs := make([][2]string, 0, len(paths))
	rel, gerr := e.acquireTask(taskguard.KindRename, "批量移动", taskguard.GroupIO, taskguard.ResDiskIO)
	if gerr != nil {
		for _, p := range paths {
			res.Failed = append(res.Failed, p)
			res.Errors[p] = gerr.Error()
		}
		return res
	}
	defer rel("done")
	if msg := e.moveTargetDir(targetDir); msg != "" {
		for _, p := range paths {
			res.Failed = append(res.Failed, p)
			res.Errors[p] = msg
		}
		return res
	}
	taskID, err := e.st.NewAuditTaskID(ctx)
	if err != nil {
		applog.Warn("获取审计任务号失败：%v", err)
	}
	for _, src := range paths {
		src = filepath.Clean(src)
		if reason := e.moveCheck(src, targetDir); reason != "" {
			res.Failed = append(res.Failed, src)
			res.Errors[src] = reason
			continue
		}
		// 冲突：目标同名存在 → 追加 _1、_2，绝不覆盖（设计文档 10.4.3）
		dst := health.UniquePath(targetDir, filepath.Base(src), func(p string) bool {
			_, err := os.Lstat(p)
			return err == nil
		})
		crossVol := e.VolumeOf(src) != e.VolumeOf(targetDir)
		// v3.25.1：UniquePath 只保证「检查那一刻不存在」，到 rename 之间外部进程
		// 仍可能创建同名文件；裸 os.Rename 会静默覆盖，与上面「绝不覆盖」的承诺相反。
		// v4.2.0：moveFileAcross 在同卷仍是 renameNoOverwrite，跨卷自动降级复制+校验+删源。
		if err := moveFileAcross(src, dst); err != nil {
			res.Failed = append(res.Failed, src)
			res.Errors[src] = "移动失败：" + renameFailText(err)
			continue
		}
		// v3.10.10（第五轮 P1-3）：移动已经成功，审计与索引不能因请求取消而丢失。
		if err := e.st.AddAudit(wctx, store.AuditEntry{
			TaskID:       taskID,
			Operation:    "move",
			OriginalPath: src,
			NewPath:      dst,
		}); err != nil {
			applog.Warn("移动已完成，但操作记录写入失败：%s → %s（%v）", src, dst, err)
		}
		// 索引同步：只改路径不删行（见 syncMovedIndex）。
		pairs = append(pairs, [2]string{src, dst})
		e.UpdatePath(filepath.Dir(src))
		if crossVol {
			res.CrossVol++
		}
		res.Done = append(res.Done, MoveDone{From: src, To: dst})
	}
	if len(pairs) > 0 {
		e.syncMovedIndex(wctx, pairs)
	}
	if len(res.Done) > 0 {
		res.TaskID = taskID
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
		applog.Info("移动完成：%d 个文件 → %s", len(res.Done), targetDir)
	}
	return res
}

// UndoMove 撤销一次移动任务：按审计逆序 rename 回原位（设计文档 10.4.4）。
// 目标位置已存在同名文件时不覆盖，记失败。
func (e *Engine) UndoMove(ctx context.Context, taskID int64) (done, failed int, err error) {
	rel, gerr := e.acquireTask(taskguard.KindRename, "撤销移动", taskguard.GroupIO, taskguard.ResDiskIO)
	if gerr != nil {
		return 0, 0, gerr
	}
	defer rel("done")
	unlock := e.lockWrites()
	defer unlock()
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	entries, err := e.st.ListAuditByTask(ctx, taskID)
	if err != nil {
		return 0, 0, err
	}
	// 逆序回放
	pairs := make([][2]string, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		a := entries[i]
		if a.Operation != "move" {
			continue
		}
		if _, err := os.Lstat(a.NewPath); err != nil {
			failed++
			continue
		}
		origDir := filepath.Dir(a.OriginalPath)
		if err := os.MkdirAll(origDir, 0o750); err != nil {
			failed++
			continue
		}
		dst := health.UniquePath(origDir, filepath.Base(a.OriginalPath), func(p string) bool {
			_, lex := os.Lstat(p)
			return lex == nil
		})
		// v3.25.1：撤销移动与正向移动同一口径，冲突时失败而不是静默覆盖。
		// v4.2.0：跨卷同样支持（复制+校验+删源）。
		if err := moveFileAcross(a.NewPath, dst); err != nil {
			failed++
			continue
		}
		pairs = append(pairs, [2]string{a.NewPath, dst})
		done++
	}
	if len(pairs) > 0 {
		e.syncMovedIndex(wctx, pairs)
	}
	if done > 0 {
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
	}
	return done, failed, nil
}

// syncMovedIndex 移动/撤销移动后同步索引：只改 files 的 path/name/ext，不删行。
//
// 为什么不用 dropIndexPath + UpdatePath：DeleteByPath 在同一个事务里连 health_results、
// file_tags、probe_cache 一起删，再 UpdatePath 会按新路径建一条新 id 的行——挂在
// files.id 上的用户打标与音视频指纹随之丢失，文件会被当成新文件重新排队分析。
// store.RenameFilePathsBatch 正是为「名字不变、位置变」写的，此前全仓零调用。
//
// 旧路径的健康结论按路径存放（不随 files 行消失），必须一并清掉，否则健康页会
// 继续列出已经不存在的文件。
func (e *Engine) syncMovedIndex(ctx context.Context, pairs [][2]string) {
	if len(pairs) == 0 {
		return
	}
	moved := make(map[string]string, len(pairs))
	olds := make([]string, 0, len(pairs))
	for _, pr := range pairs {
		moved[pr[0]] = pr[1]
		olds = append(olds, pr[0])
	}
	missing, err := e.st.RenameFilePathsBatch(ctx, pairs)
	if err != nil {
		// 磁盘已经移动完成，索引写失败不能回滚文件；下次全量扫描会纠正。
		applog.Warn("移动后批量更新索引失败（%d 条）：%v", len(pairs), err)
		return
	}
	// 旧路径不在索引里（零字节或尚未扫描）：沿用旧行为按新路径补录，避免文件从列表里消失。
	for _, old := range missing {
		if dst, ok := moved[old]; ok {
			e.UpdatePath(dst)
		}
	}
	if herr := e.st.DropHealthByPaths(ctx, olds); herr != nil {
		applog.Warn("移动后清理旧路径健康结论失败（%d 条）：%v", len(olds), herr)
	}
	// 探测缓存按 path 存主键，也要跟着走：结论是 ffmpeg 跑出来的，重探测很贵。
	if perr := e.st.UpdateProbePathsBatch(ctx, pairs); perr != nil {
		applog.Warn("移动后改写探测缓存路径失败（%d 条）：%v", len(pairs), perr)
	}
}
