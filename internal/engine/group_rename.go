// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"verify/internal/applog"
	"verify/internal/health"
	"verify/internal/store"
	"verify/internal/taskguard"
)

// RenameResult 是一次组级统一命名 / 单文件改名的结果（v1.15 引入 15.4）。
type RenameResult struct {
	TaskID int64             `json:"task_id,omitempty"`
	Done   []RenameDone      `json:"done"`
	Failed []string          `json:"failed"`
	Errors map[string]string `json:"errors"`
	// Reconcile 是操作后对账结果（v3.10.5）：有差异时前端列路径并给出下一步建议。
	Reconcile *ReconcileReport `json:"reconcile,omitempty"`
}

// RenameDone 是一个成功改名条目（改前 → 改后对照）。
type RenameDone struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RenameGroupTo 组内统一命名（设计文档 15.4.1）：以 basePath 的主名（不含扩展名）为标杆，
// 组内其他文件改名为主名 + 各自扩展名（决策 A1：扩展名不跟标杆走，避免容器错配）。
// 前置校验逐项给原因（设计文档 15.4.2）；冲突追加 _1 绝不覆盖（决策 A3）；
// audit_log 记 operation='group_rename'，可按任务整体撤销；索引走 UpdatePath 同步。
func (e *Engine) RenameGroupTo(ctx context.Context, basePath string, targets []string) *RenameResult {
	res := &RenameResult{Errors: map[string]string{}}
	// v3.25.0（P1-1）：组内批量改名纳入闸门，与扫描/隔离/转码互斥。
	done, gerr := e.acquireTask(taskguard.KindRename, "组内统一命名", taskguard.GroupIO, taskguard.ResDiskIO)
	if gerr != nil {
		for _, t := range targets {
			res.Failed = append(res.Failed, t)
			res.Errors[t] = gerr.Error()
		}
		return res
	}
	defer done("done")
	basePath = filepath.Clean(basePath)
	// 标杆必须是存在的普通文件
	if fi, err := os.Lstat(basePath); err != nil || !fi.Mode().IsRegular() {
		for _, t := range targets {
			res.Failed = append(res.Failed, t)
			res.Errors[t] = "标杆文件不存在或不是普通文件"
		}
		return res
	}
	baseName := filepath.Base(basePath)
	ext := filepath.Ext(baseName)
	stem := strings.TrimSuffix(baseName, ext)
	baseDir := filepath.Dir(basePath)
	// 标杆自身若受保护，只影响它自己（标杆不改名，无需校验）

	unlock := e.lockWrites()
	defer unlock()
	// v3.10.10（第五轮 P1-3）：改名成功后写审计与索引不能再依赖请求 ctx，
	// 用户中途关页面会让这些写入直接失败，留下「磁盘已改、记录没写」。
	wctx, cancel := detachCtx(ctx)
	defer cancel()

	taskID, err := e.st.NewAuditTaskID(wctx)
	if err != nil {
		applog.Warn("获取审计任务号失败：%v", err)
	}
	// v3.10.5：组内统一改名是批量写操作，先按本次目标路径拍快照。
	snap := e.beginOpSnapshot(ctx, "group_rename", targets)
	used := map[string]bool{} // 组内同批次冲突追踪（同目录双目标）
	for _, t := range targets {
		t = filepath.Clean(t)
		if reason := e.groupRenameCheck(t, basePath, baseDir); reason != "" {
			snap.Skip(t, reason)
			res.Failed = append(res.Failed, t)
			res.Errors[t] = reason
			continue
		}
		newPath := health.UniquePath(filepath.Dir(t), stem+filepath.Ext(t), func(p string) bool {
			if used[p] {
				return true
			}
			_, err := os.Lstat(p)
			return err == nil
		})
		used[newPath] = true
		// v3.25.2：UniquePath 只能保证「检查那一刻不存在」，到真正改名之间外部进程
		// 仍可能创建同名文件；裸 os.Rename 会静默覆盖，改用原子不覆盖版本。
		if err := renameNoOverwrite(t, newPath); err != nil {
			snap.Skip(t, renameFailText(err))
			res.Failed = append(res.Failed, t)
			res.Errors[t] = renameFailText(err)
			continue
		}
		// v4.1.1：改名只动 files 的 path/name/ext，不再「删旧行 + UpdatePath 建新行」。
		// 重建会丢掉挂在 files.id 上的用户打标、探测缓存与媒体指纹，还会让内容
		// 没变的文件被重新排队分析。与 RenameOne 同一口径（AGENTS.md 改名四步模板）。
		// snap.MarkMoved 仍要在前，索引更新失败时对账才不会误判磁盘移动成功。
		snap.MarkMoved(t, newPath, opExpectRenamed)
		if missing, ierr := e.st.RenameFilePathsBatch(wctx, [][2]string{{t, newPath}}); ierr != nil {
			applog.Warn("改名已完成，但索引更新失败：%s（%v）", newPath, ierr)
		} else if len(missing) > 0 {
			applog.Info("改名后的文件不在索引中（零字节或尚未扫描）：%s", newPath)
		}
		if err := e.st.AddAudit(wctx, store.AuditEntry{TaskID: taskID, Operation: "group_rename", OriginalPath: t, NewPath: newPath}); err != nil {
			applog.Warn("改名已完成，但操作记录写入失败：%s → %s（%v）", t, newPath, err)
		}
		if err := e.st.DropHealthByPath(wctx, t); err != nil {
			applog.Warn("改名已完成，但旧路径健康记录删除失败：%s（%v）", t, err)
		}
		e.UpdatePath(filepath.Dir(t))
		res.Done = append(res.Done, RenameDone{From: t, To: newPath})
	}
	if len(res.Done) > 0 {
		res.TaskID = taskID
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
		applog.Info("组内统一命名完成：%d 个文件（标杆 %s）", len(res.Done), baseName)
	}
	// v3.10.1：批量失败留一条汇总日志，权限类错误才能被事后定位。
	if len(res.Failed) > 0 {
		first := res.Failed[0]
		applog.Warn("组内统一命名失败 %d 个；首个路径 %s：%s", len(res.Failed), first, res.Errors[first])
	}
	res.Reconcile = e.finishOpSnapshot(snap)
	return res
}

// groupRenameCheck 组级统一命名的单项校验（设计文档 15.4.2）。
func (e *Engine) groupRenameCheck(t, basePath, baseDir string) string {
	if t == basePath {
		return "标杆文件自身不改名"
	}
	fi, err := os.Lstat(t)
	if err != nil || !fi.Mode().IsRegular() {
		return "目标不存在或不是普通文件"
	}
	// v3.10.6：与 RenameOne 同口径，父目录软链接会把路径指到卷外，仅判字符串前缀不够。
	if !e.ValidPath(t) {
		return "目标不在受管存储卷内"
	}
	// 同目录内没有改名意义（设计文档 15.4.2）
	if filepath.Dir(t) == baseDir {
		return "与标杆同目录，无需改名"
	}
	if strings.Contains(filepath.ToSlash(t), "/.verify-trash/") {
		return "隔离区内的文件不支持改名"
	}
	// 保护目录内禁改（决策 A2）
	if reason := e.protectedReasonResolved(t); reason != "" {
		return reason
	}
	return ""
}

// RenameOne 单文件改名（设计文档 15.4.5）：newName 只含文件名（可含扩展名）。
// 服务端校验非法字符；与现存文件冲突追加序号；audit operation='rename'。
func (e *Engine) RenameOne(ctx context.Context, path, newName string) (string, string, error) {
	rel, gerr := e.acquireTask(taskguard.KindRename, "单文件改名", taskguard.GroupIO, taskguard.ResDiskIO)
	if gerr != nil {
		return "", "", gerr
	}
	defer rel("done")
	path = filepath.Clean(path)
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return "", "", fmt.Errorf("新文件名不能为空")
	}
	if strings.ContainsAny(newName, "/\\") || strings.Contains(newName, "\x00") || newName == "." || newName == ".." {
		return "", "", fmt.Errorf("文件名含非法字符")
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return "", "", fmt.Errorf("文件不存在或不是普通文件")
	}
	// v3.10.6：VolumeOf 只做字符串前缀匹配，「父目录软链接指向卷外」时原始串
	// 仍带卷前缀却会操作卷外文件；改用 ValidPath（Clean + EvalSymlinks + 卷前缀重判）。
	if !e.ValidPath(path) {
		return "", "", fmt.Errorf("文件不在受管存储卷内")
	}
	if strings.Contains(filepath.ToSlash(path), "/.verify-trash/") {
		return "", "", fmt.Errorf("隔离区内的文件不支持改名")
	}
	if reason := e.protectedReasonResolved(path); reason != "" {
		return "", "", fmt.Errorf("%s", reason)
	}
	dir := filepath.Dir(path)
	if filepath.Base(path) == newName {
		return "", "", fmt.Errorf("新名字与当前相同")
	}
	newPath := health.UniquePath(dir, newName, func(p string) bool {
		_, err := os.Lstat(p)
		return err == nil
	})
	unlock := e.lockWrites()
	defer unlock()
	if err := renameNoOverwrite(path, newPath); err != nil {
		// v3.25.1：与撤销/规则改名同一口径。UniquePath 只保证「检查那一刻不存在」，
		// 到 rename 之间外部进程（SMB、其他应用）仍可能抢占同名文件；
		// 裸 os.Rename 会静默覆盖，被覆盖文件无隔离、无审计、不可撤销。
		return "", "", fmt.Errorf("%s", renameFailText(err))
	}
	// v3.10.10（第五轮 P1-3）：磁盘已改名，审计与索引必须写完。
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	taskID, _ := e.st.NewAuditTaskID(wctx)
	if err := e.st.AddAudit(wctx, store.AuditEntry{TaskID: taskID, Operation: "rename", OriginalPath: path, NewPath: newPath}); err != nil {
		applog.Warn("改名已完成，但操作记录写入失败：%s → %s（%v）", path, newPath, err)
	}
	// v3.10.8：改名只动路径字段，不再「删旧行 + 重建新行」。
	// 重建会丢掉视频/音频/缩略图指纹与打标记录（都挂在 files.id 上），
	// 还要为一次改名重算预哈希、甚至重新排队分析。
	// 这里同步 path/name/ext 三列并删掉旧路径的健康结果（AGENTS.md 的改名四步模板）。
	if missing, err := e.st.RenameFilePathsBatch(wctx, [][2]string{{path, newPath}}); err != nil {
		applog.Warn("改名已完成，但索引更新失败：%s（%v）", newPath, err)
	} else if len(missing) > 0 {
		applog.Info("改名后的文件不在索引中（零字节或尚未扫描）：%s", newPath)
	}
	if err := e.st.DropHealthByPath(wctx, path); err != nil {
		applog.Warn("改名已完成，但旧路径健康记录删除失败：%s（%v）", path, err)
	}
	e.UpdatePath(dir)
	e.bumpVersion()
	e.Bus.Publish(Event{Type: "update"})
	return newPath, fmt.Sprintf("%d", taskID), nil
}

// undoRenameTarget 把已改名的文件还原到原路径，返回失败原因（空串表示成功）。
//
// v3.10.4：三条撤销路径（繁简转换、组内统一命名、规则引擎批次）共用同一口径。
// Linux 的 rename 在目标存在时会静默覆盖，被覆盖的文件不进隔离区、无法恢复，
// 所以必须先确认原路径没被占用；这与 UndoHealthTask 的处理保持一致。
func undoRenameTarget(from, to string) string {
	if _, err := os.Lstat(from); err != nil {
		return "改动后的文件已不存在"
	}
	if toInfo, err := os.Lstat(to); err == nil {
		// v3.25.0（P2-7 兜底）：大小写不敏感卷上，撤销「只改大小写」的改名时
		// from/to 是同一个文件，不算占用；其余情况仍拒绝，避免覆盖。
		if fromInfo, ferr := os.Lstat(from); ferr == nil && os.SameFile(fromInfo, toInfo) {
			// P3-2（2026-11-11）：同一 inode 的改名仍走统一入口，
			// renameNoOverwrite 内部按 SameFile 放行，不会误判成「目标被占用」。
			if rerr := renameNoOverwrite(from, to); rerr != nil {
				return renameFailText(rerr)
			}
			return ""
		}
		return "原文件名已被占用，未覆盖"
	}
	// v3.25.0（P1-3）：上面的 Lstat 与 rename 之间有窗口，Linux rename 会静默覆盖。
	// 改用带 O_EXCL 语义的 renameNoOverwrite，和规则引擎改名/移动保持同一口径。
	if err := renameNoOverwrite(from, to); err != nil {
		return renameFailText(err)
	}
	return ""
}

// UndoGroupRename 撤销一次组级统一命名 / 单文件改名任务（audit 逆序回放）。
// failures 回传失败原因（v3.10.4）：原路径被占用时明确告知，不再静默覆盖。
func (e *Engine) UndoGroupRename(ctx context.Context, taskID int64) (done, failed int, failures []HealthOpItem, err error) {
	unlock := e.lockWrites()
	defer unlock()
	return e.undoGroupRename(ctx, taskID)
}

// undoGroupRename 是不加锁的回放实现，供已持有写锁的批量撤销复用，
// 避免 UndoRenameBatch 与 UndoGroupRename 之间出现同锁重入死锁。
func (e *Engine) undoGroupRename(ctx context.Context, taskID int64) (done, failed int, failures []HealthOpItem, err error) {
	entries, err := e.st.ListAuditByTask(ctx, taskID)
	if err != nil {
		return 0, 0, nil, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		a := entries[i]
		if a.Operation != "group_rename" && a.Operation != "rename" {
			continue
		}
		// v3.10.13：撤销总预算用尽后停止回放，剩余条目如实计入 failed，
		// 让批次落 undone_partial 并可重试，而不是无限占用写锁。
		if cerr := ctx.Err(); cerr != nil {
			remaining := i + 1
			failed += remaining
			failures = append(failures, HealthOpItem{Error: fmt.Sprintf("撤销总时长超限，剩余 %d 条未处理，可重试继续", remaining)})
			applog.Warn("撤销改名提前结束：剩余 %d 条未处理（%v）", remaining, cerr)
			break
		}
		// v3.10.13：重复撤销或部分撤销重试时，已经改回原名的条目（新路径没了、原路径在）
		// 说明上一轮已经成功。计成成功而不是「改动后的文件已不存在」，
		// 否则批次永远收不到 undone，用户点几次撤销也消不掉提示条。
		if !fileExists(a.NewPath) && fileExists(a.OriginalPath) {
			done++
			continue
		}
		if reason := undoRenameTarget(a.NewPath, a.OriginalPath); reason != "" {
			failed++
			failures = append(failures, HealthOpItem{Path: a.OriginalPath, NewPath: a.NewPath, Error: reason})
			continue
		}
		// v3.10.11（P1-01）：整批共用一个 30 秒 detached ctx 时，撤销上万文件会在中途到期，
		// 之后的索引与健康记录更新全部失败却仍按成功计数，接口还报「全部成功」。
		// 改为每条独立续期，并把索引/健康记录失败计入 failed，让批次落到部分完成。
		ectx, cancel := detachCtx(ctx)
		reason := ""
		// v3.10.8：撤销同样只回改路径字段，避免丢掉挂在 files.id 上的指纹与打标；
		// 旧实现「删新行 + UpdatePath 原路径」等于把文件当新文件重扫一遍。
		if _, rerr := e.st.RenameFilePathsBatch(ectx, [][2]string{{a.NewPath, a.OriginalPath}}); rerr != nil {
			reason = "文件已改回原名，但索引更新失败：" + rerr.Error()
		} else if herr := e.st.DropHealthByPath(ectx, a.NewPath); herr != nil {
			reason = "文件已改回原名，但旧路径健康记录删除失败：" + herr.Error()
		}
		cancel()
		if reason != "" {
			failed++
			failures = append(failures, HealthOpItem{Path: a.OriginalPath, NewPath: a.NewPath, Error: reason})
			applog.Warn("撤销改名后的索引更新失败：%s（%s）", a.OriginalPath, reason)
			continue
		}
		e.UpdatePath(filepath.Dir(a.OriginalPath))
		done++
	}
	if len(failures) > 0 {
		applog.Warn("撤销改名失败 %d 个；首个路径 %s：%s", len(failures), failures[0].Path, failures[0].Error)
	}
	if done > 0 {
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
	}
	return done, failed, failures, nil
}
