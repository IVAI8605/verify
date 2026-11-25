// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"verify/internal/applog"
	"verify/internal/store"
	"verify/internal/taskguard"
)

// 本文件实现「目录级移动」（v3.14.0）。
//
// 与 MoveFiles 的差别：移动对象是目录本身，一次 rename 搬走整棵子树。
// 索引同步因此必须按前缀改写（待定问题 #6）：files 与 health_results 里
// 所有位于旧目录下的行都要跟着换前缀，等下次扫描收敛会留几小时死行。
//
// 边界（v4.2.0 起更新）：
//   - 同卷 os.Rename；跨卷走递归复制+校验+删源（与 MoveFiles 同口径）
//   - 目标必须在受管卷内（三步防线：Clean + EvalSymlinks + 卷前缀重判）
//   - 不允许移进自己或自己的子目录（会形成环）
//   - 目标位置已存在同名目录时不覆盖，报错收场
//   - 移动前写审计与写前快照；审计写不进就不动手（fail-closed）
//   - 撤销走与 UndoMove 相同的审计逆序回放，目录级用前缀改写同步索引

// MoveDirResult 是一次目录移动的结果。
type MoveDirResult struct {
	TaskID  int64  `json:"task_id,omitempty"`
	From    string `json:"from"`
	To      string `json:"to"`
	Files   int    `json:"files"`   // 索引里同步改写前缀的文件数
	Health  int64  `json:"health"`  // 健康结论同步改写数
	Missing int    `json:"missing"` // 索引里没有、也没随移动补录的文件（移动前就不在库）
	Error   string `json:"error,omitempty"`
}

// moveDirCheck 目录移动预检，返回空串表示通过。
func (e *Engine) moveDirCheck(srcDir, dstDir string) string {
	fi, err := os.Stat(srcDir)
	if err != nil || !fi.IsDir() {
		return "源目录不存在或不是目录"
	}
	// 源必须在受管卷内（三步防线），且不能在隔离区里
	if !e.ValidDirPath(srcDir) {
		return "源目录不在受管卷内"
	}
	if strings.Contains(filepath.ToSlash(srcDir), "/.verify-trash/") {
		return "隔离区内的目录不支持移动"
	}
	if reason := e.protectedReasonDir(srcDir); reason != "" {
		return reason
	}
	// 目标父目录必须存在且在受管卷内；目标目录本身必须不存在（绝不覆盖）
	dstParent := filepath.Dir(dstDir)
	if !e.ValidDirPath(dstParent) {
		return "目标位置不在受管卷内"
	}
	if pfi, perr := os.Stat(dstParent); perr != nil || !pfi.IsDir() {
		return "目标位置的上级目录不存在"
	}
	if _, derr := os.Lstat(dstDir); derr == nil {
		return "目标位置已存在同名目录"
	}
	// v4.2.0：跨卷不再拒绝，执行时降级为递归复制+校验+删源（见 copy_verify.go）。
	// 禁止移进自己或自己的子目录：src == dst 或 dst 在 src 下面会形成环
	if dstDir == srcDir || strings.HasPrefix(dstDir, srcDir+string(os.PathSeparator)) {
		return "不能移动到自身或自己的子目录内"
	}
	return ""
}

// protectedReasonDir 目录级保护判定：整棵子树里有保护文件就不许动。
// 与文件级 protectedReason 的差别：目录可能包含几千个文件，逐个查表太慢，
// 这里只判保护目录本身（源目录在保护目录下、或源目录就是保护目录）。
func (e *Engine) protectedReasonDir(dir string) string {
	// 复用文件级判定：把目录当路径查，命中保护目录前缀即拒绝
	if reason := e.protectedReasonResolved(dir); reason != "" {
		return reason
	}
	return ""
}

// MoveDirPreview 预检目录移动，不产生任何变更。
func (e *Engine) MoveDirPreview(ctx context.Context, srcDir, dstDir string) *MoveDirResult {
	srcDir, dstDir = filepath.Clean(srcDir), filepath.Clean(dstDir)
	res := &MoveDirResult{From: srcDir, To: dstDir}
	if msg := e.moveDirCheck(srcDir, dstDir); msg != "" {
		res.Error = msg
		return res
	}
	// 预览给出将受影响的索引行数，界面据此提示规模
	if sum, err := e.st.CountUnderPrefix(ctx, srcDir); err == nil {
		res.Files = sum
	}
	return res
}

// MoveDir 把 srcDir 整体移动为 dstDir（同卷 rename）。
// 顺序：预检 → 审计 → 快照 → rename → 索引前缀改写 → 快照对账。
// 审计写不进就不动手：目录移动影响面大，没有撤销依据的操作不能执行。
func (e *Engine) MoveDir(ctx context.Context, srcDir, dstDir string) *MoveDirResult {
	srcDir, dstDir = filepath.Clean(srcDir), filepath.Clean(dstDir)
	res := &MoveDirResult{From: srcDir, To: dstDir}
	// v3.25.0（P1-1）：目录移动是 io-exclusive 的长任务，此前只拿写锁没登记闸门，
	// 扫描或隔离在跑时仍可启动。这里补上，冲突时直接拒绝并说明占用者。
	done, gerr := e.acquireTask(taskguard.KindRename, "目录移动", taskguard.GroupIO, taskguard.ResDiskIO)
	if gerr != nil {
		res.Error = gerr.Error()
		return res
	}
	defer done("done")
	if msg := e.moveDirCheck(srcDir, dstDir); msg != "" {
		res.Error = msg
		return res
	}
	unlock := e.lockWrites()
	defer unlock()
	wctx, cancel := detachCtx(ctx)
	defer cancel()

	// 先记账再动手：审计行独立提交，业务回滚不能带走审计（AGENTS 安全与权限）
	taskID, err := e.st.NewAuditTaskID(wctx)
	if err != nil {
		res.Error = "无法分配操作号：" + err.Error()
		return res
	}
	if err := e.st.AddAudit(wctx, store.AuditEntry{
		TaskID:       taskID,
		Operation:    "move_dir",
		OriginalPath: srcDir,
		NewPath:      dstDir,
	}); err != nil {
		res.Error = "操作记录写入失败，已取消移动：" + err.Error()
		return res
	}
	res.TaskID = taskID

	// 快照按「目录」记录：对账只验证目录本身搬走即可，成员行靠前缀改写保证一致
	snap := e.beginOpSnapshot(ctx, "move_dir", []string{srcDir})

	// v3.25.2（只读排查 P3-6）：与其余落位统一走 renameNoOverwrite。
	// 前面的 Lstat 只说明「检查那一刻目标不存在」，外部进程仍可能在这之后建同名空目录，
	// 裸 os.Rename 会把它静默替换掉。
	// v4.2.0：moveDirAcross 同卷仍是 renameNoOverwrite，跨卷自动降级递归复制+校验+删源。
	if err := moveDirAcross(srcDir, dstDir); err != nil {
		snap.Skip(srcDir, "移动失败："+err.Error())
		_ = e.finishOpSnapshot(snap)
		res.Error = "移动失败：" + err.Error()
		// 移动没发生，审计行留着只会让撤销空跑；补一条说明后保留，撤销入口会安全跳过
		return res
	}
	snap.MarkMoved(srcDir, dstDir, opExpectRenamed)

	// 索引同步：files 与 health_results 都按前缀改写（待定问题 #6 的硬要求）
	sum, err := e.st.RewriteDirPrefix(wctx, srcDir, dstDir)
	if err != nil {
		// rename 已成功：索引改写失败时磁盘与库不一致，必须留告警；撤销入口可以救回来
		applog.Warn("目录已移动但索引改写失败（%s → %s）：%v", srcDir, dstDir, err)
		res.Error = "目录已移动，但索引改写失败，请用「撤销」恢复：" + err.Error()
		_ = e.finishOpSnapshot(snap)
		return res
	}
	res.Files = sum.Files
	res.Missing = sum.Missing
	if n, herr := e.st.RewriteHealthDirPrefix(wctx, srcDir, dstDir); herr == nil {
		res.Health = n
	} else {
		applog.Warn("健康结论前缀改写失败（%s → %s）：%v", srcDir, dstDir, herr)
	}

	_ = e.finishOpSnapshot(snap)
	e.bumpVersion()
	e.Bus.Publish(Event{Type: "update"})
	applog.Info("目录移动完成：%s → %s（索引 %d 行改写，%d 行不在库）", srcDir, dstDir, res.Files, res.Missing)
	return res
}

// UndoMoveDir 撤销一次目录移动：把目录 rename 回原位，索引前缀再改回去。
// 与 UndoMove 一样按审计逆序回放；目录级任务在审计里只有一行（原目录 → 新目录）。
func (e *Engine) UndoMoveDir(ctx context.Context, taskID int64) (done, failed int, err error) {
	rel, gerr := e.acquireTask(taskguard.KindRename, "撤销目录移动", taskguard.GroupIO, taskguard.ResDiskIO)
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
	for i := len(entries) - 1; i >= 0; i-- {
		a := entries[i]
		if a.Operation != "move_dir" {
			continue
		}
		// 新位置必须有目录才能撤销；原位置已存在同名时不覆盖（记录失败，用户自行处理）
		if _, serr := os.Stat(a.NewPath); serr != nil {
			failed++
			continue
		}
		if _, oerr := os.Lstat(a.OriginalPath); oerr == nil {
			failed++
			continue
		}
		// v3.25.2：撤销目录移动同样不能裸改名，避免抢占外部刚创建的同名目录。
		// v4.2.0：跨卷同样支持。
		if err := moveDirAcross(a.NewPath, a.OriginalPath); err != nil {
			failed++
			continue
		}
		// 索引改回原前缀
		if _, rerr := e.st.RewriteDirPrefix(wctx, a.NewPath, a.OriginalPath); rerr != nil {
			applog.Warn("撤销目录移动后索引改写失败（%s → %s）：%v", a.NewPath, a.OriginalPath, rerr)
		}
		if _, herr := e.st.RewriteHealthDirPrefix(wctx, a.NewPath, a.OriginalPath); herr != nil {
			applog.Warn("撤销目录移动后健康结论改写失败：%v", herr)
		}
		done++
	}
	if done > 0 {
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
	}
	return done, failed, nil
}
