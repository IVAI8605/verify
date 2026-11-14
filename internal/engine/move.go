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
)

// MoveResult 是一次批量移动的结果。
type MoveResult struct {
	TaskID int64             `json:"task_id,omitempty"`
	Done   []MoveDone        `json:"done"`
	Failed []string          `json:"failed"`
	Errors map[string]string `json:"errors"`
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
	// 第一版只做同卷 os.Rename（设计文档 10.4：跨卷明确不支持，预检即报）
	if e.VolumeOf(src) != e.VolumeOf(targetDir) {
		return "跨卷移动暂不支持"
	}
	// 保护目录内禁止移出（决策 6）
	if reason := e.protectedReason(src); reason != "" {
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
// 落库同步复用 UpdatePath（旧路径行删除 + 新路径按现存文件补录）。
func (e *Engine) MoveFiles(ctx context.Context, targetDir string, paths []string) *MoveResult {
	unlock := e.lockWrites()
	defer unlock()
	// v3.10.10（第五轮 P1-3）：同批移动共用一份脱离请求取消的上下文。
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	res := &MoveResult{Errors: map[string]string{}}
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
		// v3.25.1：UniquePath 只保证「检查那一刻不存在」，到 rename 之间外部进程
		// 仍可能创建同名文件；裸 os.Rename 会静默覆盖，与上面「绝不覆盖」的承诺相反。
		if err := renameNoOverwrite(src, dst); err != nil {
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
		// 索引同步：旧路径删除，新路径由 UpdatePath 按现存文件补录并触发目录重扫
		e.dropIndexPath(wctx, nil, src)
		e.UpdatePath(dst)
		e.UpdatePath(filepath.Dir(src))
		res.Done = append(res.Done, MoveDone{From: src, To: dst})
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
	unlock := e.lockWrites()
	defer unlock()
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	entries, err := e.st.ListAuditByTask(ctx, taskID)
	if err != nil {
		return 0, 0, err
	}
	// 逆序回放
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
		if err := renameNoOverwrite(a.NewPath, dst); err != nil {
			failed++
			continue
		}
		e.dropIndexPath(wctx, nil, a.NewPath)
		e.UpdatePath(dst)
		done++
	}
	if done > 0 {
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
	}
	return done, failed, nil
}
