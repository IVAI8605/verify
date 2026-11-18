// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"verify/internal/applog"
	"verify/internal/oplog"
	"verify/internal/store"
	"verify/internal/taskguard"
	"verify/internal/trashvol"
)

// recordOp 写一条操作审计。审计行独立提交，绝不与业务表放进同一事务；
// 失败时返回错误，由调用方决定「拒绝操作」还是「只记警告」。
func (e *Engine) recordOp(ctx context.Context, entry oplog.Entry) error {
	entry.FromContext(ctx)
	limit := oplog.DefaultDetailLimit
	if e.cfg != nil {
		limit = e.cfg.Get().OpLogDetailLimit
	}
	if err := e.st.InsertOp(ctx, entry, limit); err != nil {
		applog.Error("写入操作记录失败（%s）：%v", entry.Action, err)
		return fmt.Errorf("操作记录写入失败：%w", err)
	}
	return nil
}

// TrashResult 描述批量操作结果。
type TrashResult struct {
	Moved  []string `json:"moved"`
	Failed []string `json:"failed"`
	Errors []string `json:"errors"`
	// Unprocessed 是因请求取消而未处理到的条目数（v4.0.1 安全审查 问题2）：
	// 这些条目既没动过、也不算失败，单独计数，避免被误报成「索引中不存在」。
	Unprocessed int `json:"unprocessed,omitempty"`
	// Conflict 是闸门拒绝原因（v3.25.2 P3-5）：非空表示本次没有删除任何文件。
	Conflict string `json:"conflict,omitempty"`
	// Reconcile 是操作后对账结果（v3.10.5）：有差异时前端会列路径并给出下一步建议。
	Reconcile *ReconcileReport `json:"reconcile,omitempty"`
}

// RestoreResult 描述恢复结果。
type RestoreResult struct {
	Restored []string `json:"restored"`
	Failed   []string `json:"failed"`
	Errors   []string `json:"errors"`
	// Unprocessed 是请求取消时剩余的条目数（v4.0.1 安全审查 问题2），不算失败。
	Unprocessed int `json:"unprocessed,omitempty"`
	// Conflict 是闸门拒绝原因（v3.25.2 P3-5）：非空表示一个文件都没恢复。
	Conflict string `json:"conflict,omitempty"`
}

// OperationPreview 是批量操作的逐项预检结果。
type OperationPreview struct {
	Path   string `json:"path"`
	ID     int64  `json:"id,omitempty"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// TrashGroup 描述一次批量操作涉及的一个分组。
// Key 用于审计与隔离区记录；Members 是调用方已解析出的全员（结果页快照），为空时按 Key 重算。
// 结果页会跨多个分组勾选，逐个分组都要满足「至少保留一个」，不能只看第一个键（P1-2）。
type TrashGroup struct {
	Key     string
	Members []string
}

// PreviewTrash 在不移动文件的前提下校验批量移入隔离区的每一项。
func (e *Engine) PreviewTrash(ctx context.Context, paths []string, groups []TrashGroup) ([]OperationPreview, error) {
	items := make([]OperationPreview, 0, len(paths))
	if len(paths) == 0 {
		return items, fmt.Errorf("未选择文件")
	}
	if err := e.checkKeepOne(ctx, groups, paths); err != nil {
		for _, p := range paths {
			items = append(items, OperationPreview{Path: p, OK: false, Reason: err.Error()})
		}
		return items, nil
	}
	for _, p := range paths {
		p = filepath.Clean(p)
		item := OperationPreview{Path: p}
		if reason := e.protectedReason(p); reason != "" {
			item.Reason = reason
			items = append(items, item)
			continue
		}
		if reason := e.checkTrashPath(ctx, p); reason != "" {
			item.Reason = reason
		} else {
			item.OK = true
		}
		items = append(items, item)
	}
	return items, nil
}

// PreviewRestore 校验隔离区恢复的逐项条件。
func (e *Engine) PreviewRestore(ctx context.Context, ids []int64) []OperationPreview {
	items := make([]OperationPreview, 0, len(ids))
	for _, id := range ids {
		item := OperationPreview{ID: id}
		t, err := e.st.GetTrash(ctx, id)
		if err != nil || t == nil {
			item.Reason = "回收站记录不存在"
			items = append(items, item)
			continue
		}
		item.Path = t.OrigPath
		if reason := e.protectedReason(t.OrigPath); reason != "" {
			item.Reason = reason + "（如需处理请先解除保护）"
			items = append(items, item)
			continue
		}
		// v3.7.1：恢复前复核隔离区路径仍在受管卷内（审查报告 §6）
		if reason := e.checkTrashRowPath(*t); reason != "" {
			item.Reason = reason
			items = append(items, item)
			continue
		}
		if _, err := os.Lstat(t.TrashPath); err != nil {
			item.Reason = "隔离区文件已丢失"
			items = append(items, item)
			continue
		}
		item.OK = true
		if _, err := os.Lstat(t.OrigPath); err == nil {
			item.Reason = "原路径已存在，将自动改名恢复"
		}
		items = append(items, item)
	}
	return items
}

// PreviewDelete 校验隔离区永久删除的逐项条件。
func (e *Engine) PreviewDelete(ctx context.Context, ids []int64) []OperationPreview {
	items := make([]OperationPreview, 0, len(ids))
	for _, id := range ids {
		item := OperationPreview{ID: id}
		t, err := e.st.GetTrash(ctx, id)
		if err != nil || t == nil {
			item.Reason = "回收站记录不存在"
			items = append(items, item)
			continue
		}
		item.Path = t.OrigPath
		// v3.7.1：预览与执行同一口径，先复核记录路径仍在受管卷隔离区内（审查报告 §6）
		if reason := e.checkTrashRowPath(*t); reason != "" {
			item.Reason = reason
			items = append(items, item)
			continue
		}
		if _, err := os.Lstat(t.TrashPath); err != nil {
			item.Reason = "隔离区文件已丢失，将仅清理记录"
			item.OK = true
			items = append(items, item)
			continue
		}
		// 预览阶段即探测父目录写权限，避免「预览可执行、执行 permission denied」（P-01）
		if reason := removableReason(t.TrashPath); reason != "" {
			item.Reason = reason
			items = append(items, item)
			continue
		}
		item.OK = true
		items = append(items, item)
	}
	return items
}

// checkTrashPath 返回空字符串表示可移入隔离区，否则返回失败原因。
func (e *Engine) checkTrashPath(ctx context.Context, p string) string {
	// v3.10.6：只看字符串前缀挡不住「父目录软链接指向卷外」，
	// 改用 ValidPath（Clean + EvalSymlinks + 卷前缀重判），解析后必须仍是卷内普通文件。
	if !e.ValidPath(p) || strings.Contains(p, "/.verify-trash/") {
		return "路径不在受管存储卷内"
	}
	row, err := e.st.GetFileByPath(ctx, p)
	if err != nil || row == nil {
		return "索引中不存在该文件"
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return "文件已不存在或不是普通文件"
	}
	if fi.Size() != row.Size || fi.ModTime().UnixNano() != row.Mtime {
		return "文件在索引后被修改，请重新扫描"
	}
	return ""
}

// checkTrashRowPath 复核隔离区记录的 trash_path 是否仍落在受管卷的隔离区内。
// 返回空串表示通过。v3.7.1 新增：永久删除/清空/恢复在执行前逐条复核，
// 防止历史脏记录或卷配置变更后误删卷外文件（审查报告 §6 判定 BLOCK 项）。
// 与 ResolveTrashPath 同一口径：卷前缀 → Clean 重判 → 必须含 /.verify-trash/ → 软链接解析后重判。
func (e *Engine) checkTrashRowPath(t store.TrashRow) string {
	vols := e.Volumes()
	if len(vols) == 0 || volumeOf(t.TrashPath, vols) == "" {
		return "隔离区路径不在受管存储卷内"
	}
	cleaned := filepath.Clean(t.TrashPath)
	if volumeOf(cleaned, vols) == "" || !strings.Contains(filepath.ToSlash(cleaned), "/.verify-trash/") {
		return "隔离区路径不在隔离区目录内"
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	switch {
	case err == nil:
		if volumeOf(resolved, vols) == "" || !strings.Contains(filepath.ToSlash(resolved), "/.verify-trash/") {
			return "隔离区路径经软链接解析后逃逸出隔离区"
		}
	case os.IsNotExist(err):
		// 文件已丢失：删除/清空容忍不存在（仅清记录），恢复动作另行判存在性
	default:
		return "隔离区路径解析失败（" + err.Error() + "）"
	}
	return ""
}

// removableReason 探测删除 path 所需的父目录权限（删除目录项需要父目录写+进入权限）。
// 返回空串表示可删。v3.7.1 新增：让「预览可执行」与「执行」共用同一判定（审查报告 P-01）。
// 实现：在父目录建一个隐藏临时文件后立刻删除，用真实的创建/删除结果当结论。
// 不用 access(2)：飞牛部分卷走 FUSE/ACL，权限位与实际可写性不一致（真机实测过，
// access 报 EACCES 而目录其实是可写的）；也不再用 OpenFile(dir, O_RDWR)，
// Linux 打开目录必须只读，O_RDWR 恒返回 EISDIR，会把所有条目误判成不可写。
func removableReason(path string) string {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".verify-wcheck-*")
	if err != nil {
		return "所在目录不可写（" + err.Error() + "）"
	}
	name := f.Name()
	_ = f.Close()
	if err := os.Remove(name); err != nil {
		// 建得出来却删不掉，说明目录本身异常，同样不该继续执行删除
		return "所在目录不可写（" + err.Error() + "）"
	}
	return ""
}

// TrashFiles 把选中文件移入各卷的隔离区。
func (e *Engine) TrashFiles(ctx context.Context, paths []string, groups []TrashGroup) (*TrashResult, error) {
	// v3.25.0（P1-1）：批量隔离会校验、移动文件并写库，原先完全绕过闸门，
	// 扫描或转码在跑时照样能并发搬文件。接入后与长任务互斥。
	// 清理计划、转码替换、剪除片头这些内部调用已持有别的资源组，
	// 走 trashFilesLocked，避免被自己的闸门记录拦下。
	done, err := e.acquireTask(taskguard.KindTrash, "", taskguard.GroupIO, taskguard.ResDiskIO)
	if err != nil {
		return nil, err
	}
	defer done("done")
	return e.trashFilesLocked(ctx, paths, groups)
}

// trashFilesLocked 是 TrashFiles 的实际实现，不加闸门，供已持有任务的内部流程复用。
func (e *Engine) trashFilesLocked(ctx context.Context, paths []string, groups []TrashGroup) (*TrashResult, error) {
	start := time.Now()
	if len(paths) == 0 {
		return nil, fmt.Errorf("未选择文件")
	}
	// v3.10.10（第五轮 P1-5）：keep-one 校验与移动放进同一临界区，
	// 避免另一个标签的写请求插在「校验通过」与「真正移走」之间。
	unlock := e.lockWrites()
	defer unlock()
	if err := e.checkKeepOne(ctx, groups, paths); err != nil {
		return nil, err
	}
	res := &TrashResult{}
	// v3.10.5：动手前给本次涉及的文件拍快照，结束后核对磁盘实际状态。
	snap := e.beginOpSnapshot(ctx, "trash", paths)
	now := time.Now()
	for i, p := range paths {
		// v4.0.1（安全审查 问题2）：请求被取消（用户关页面/点停止）时，剩余条目根本没处理，
		// 不能继续调 checkTrashPath（它会因 GetFileByPath 失败而误报「索引中不存在该文件」）。
		// 这里主动中断，把剩余项按「未处理」计数，并 SkipPending 避免事后对账误报。
		if ctx.Err() != nil {
			res.Unprocessed = len(paths) - i
			res.Errors = append(res.Errors, fmt.Sprintf("操作已取消，剩余 %d 个未处理", res.Unprocessed))
			snap.SkipPending("操作已取消，未处理")
			break
		}
		p = filepath.Clean(p)
		if reason := e.protectedReason(p); reason != "" {
			// 预检之后配置可能变化，执行前必须再拦一次
			snap.Skip(p, reason)
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, reason)
			continue
		}
		vol := e.VolumeOf(p)
		if reason := e.checkTrashPath(ctx, p); reason != "" {
			snap.Skip(p, reason)
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, reason)
			continue
		}
		row, err := e.st.GetFileByPath(ctx, p)
		if err != nil || row == nil {
			snap.Skip(p, "索引中不存在该文件")
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, "索引中不存在该文件")
			continue
		}
		dst, err := e.buildTrashPath(vol, p, now)
		if err != nil {
			snap.Skip(p, err.Error())
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		if err := moveFile(p, dst); err != nil {
			snap.Skip(p, err.Error())
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		if err := e.st.AddTrash(ctx, p, dst, vol, row.Hash, groupKeyForPath(groups, p), "duplicate", row.Size); err != nil {
			// v4.0.1（安全审查 问题5）：回滚前先判原父目录是否还在。用户可能已把目录删掉，
			// moveFile 里的 MkdirAll 会把它重新建出来，等于悄悄撤销了用户的删除。原目录不在
			// 就不再搬回，文件留在隔离区但无恢复记录，进差异清单交人工处理。
			_, note := e.rollbackTrashRecord(snap, p, dst, err)
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, "记录回收站失败: "+err.Error()+"（"+note+"）")
			continue
		}
		e.dropIndexPath(ctx, snap, p)
		snap.MarkMoved(p, dst, opExpectRelocated)
		res.Moved = append(res.Moved, p)
	}
	entry := oplog.New("trash_add")
	entry.Summary = fmt.Sprintf("移入隔离区 %d 个文件，失败 %d 个", len(res.Moved), len(res.Failed))
	if res.Unprocessed > 0 {
		entry.Summary += fmt.Sprintf("，取消未处理 %d 个", res.Unprocessed)
	}
	entry.TargetCount = len(paths)
	for _, p := range res.Moved {
		entry.Details = append(entry.Details, oplog.Detail{Path: p})
	}
	for i, p := range res.Failed {
		d := oplog.Detail{Path: p}
		if i < len(res.Errors) {
			d.Reason = res.Errors[i]
		}
		entry.Details = append(entry.Details, d)
	}
	entry.SetFailure(res.Errors)
	entry.DurationMS = time.Since(start).Milliseconds()
	// 移入隔离区可恢复，审计失败只记警告，不阻断主流程（fail-open）。
	if err := e.recordOp(ctx, entry); err != nil {
		applog.Warn("移入隔离区已完成，但操作记录写入失败：%v", err)
	}
	e.bumpVersion()
	e.Bus.Publish(Event{Type: "update"})
	res.Reconcile = e.finishOpSnapshot(snap)
	return res, nil
}

// rollbackTrashRecord 在 AddTrash 失败后尝试把已移入隔离区的文件搬回原路径。
// 返回 restored=true 表示已回滚；false 表示文件仍留在隔离区，note 说明原因。
//
// v4.0.1（安全审查 问题5）：回滚前先判原父目录是否存在。用户可能在隔离后又删掉了原目录，
// 此时若直接 moveFile(dst, p)，其内部的 os.MkdirAll 会把已删除的目录重新创建出来，
// 相当于悄悄撤销了用户的一次删除。原目录不在就只记对账问题（record_lost），不重建目录。
func (e *Engine) rollbackTrashRecord(snap *OpSnapshot, p, dst string, addErr error) (restored bool, note string) {
	if fi, err := os.Lstat(filepath.Dir(p)); err != nil || !fi.IsDir() {
		snap.MarkProblem(p, opExpectRelocated, dst,
			"记录隔离区失败且原目录已不存在，文件留在隔离区但无恢复记录，请人工核对："+addErr.Error())
		return false, "原目录已不存在，未回滚"
	}
	if rbErr := moveFile(dst, p); rbErr != nil {
		snap.MarkProblem(p, opExpectRelocated, dst,
			"记录隔离区失败且回滚失败，文件可能已在隔离区但没有恢复记录："+rbErr.Error())
		return false, "回滚失败：" + rbErr.Error()
	}
	snap.Skip(p, "记录回收站失败，已回滚："+addErr.Error())
	return true, "已回滚"
}

func (e *Engine) buildTrashPath(vol, orig string, now time.Time) (string, error) {
	rel, err := filepath.Rel(vol, orig)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("无法确定相对路径")
	}
	dir := filepath.Join(e.TrashRoot(vol), now.Format("2006-01-02"), filepath.Dir(rel))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		// v3.10.12（P2-02）：隔离区所在卷只读 / 权限不足时，原生错误是英文的
		// 「mkdir ...: Access is denied.」，按项目规范补中文描述再返回。
		return "", fmt.Errorf("创建隔离目录失败（%v）", err)
	}
	base := filepath.Base(rel)
	for i := 0; i < 1000; i++ {
		suffix := ""
		if i > 0 {
			suffix = fmt.Sprintf("-%d", i)
		}
		candidate := filepath.Join(dir, fitTrashName(base, suffix))
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return filepath.Join(dir, fitTrashName(base, fmt.Sprintf("-%d", time.Now().UnixNano()))), nil
}

// maxTrashNameBytes 是隔离区文件名的字节上限。
//
// Linux 的 NAME_MAX 通常是 255 字节，隔离区还会追加 “-序号” 去重后缀，
// 因此主干先留出 15 字节余量。原来直接沿用原文件名，255 字节的源文件
// 一旦撞名改后缀就会写入失败（ENAMETOOLONG），整个隔离动作直接报错。
const maxTrashNameBytes = 240

// fitTrashName 在保留扩展名的前提下把文件名截断到隔离区可接受的字节数。
// 截断点优先落在 UTF-8 字符边界，避免产生半个汉字的乱码文件名。
func fitTrashName(base, suffix string) string {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	// 扩展名异常长（或整名被误判为扩展名）时按纯主干处理，避免挤掉全部可用空间。
	if len(ext) > 32 {
		stem, ext = base, ""
	}
	room := maxTrashNameBytes - len(suffix) - len(ext)
	if room < 1 {
		room = 1
	}
	if len(stem) <= room {
		return stem + ext
	}
	cut := room
	for cut > 0 && cut < len(stem) && !utf8.RuneStart(stem[cut]) {
		cut--
	}
	return stem[:cut] + ext
}

// checkKeepOne 逐个分组校验「同组至少保留一个文件」。
// 结果页会跨多个分组勾选，只要有一组被整组清空就拒绝整批（P1-2）。
// Members 非空时直接用调用方解析好的全员（结果页快照，含规则类键），否则按分组键重算。
func (e *Engine) checkKeepOne(ctx context.Context, groups []TrashGroup, paths []string) error {
	for _, g := range groups {
		members := g.Members
		if len(members) > 0 {
			// v3.10.10（第五轮 P1-4）：快照成员可能已被隔离 / 改名 / 外部删除，
			// 拿它们当「组里还在的那一份」会放行整组清空。这里只认索引里仍 active 的成员。
			live, err := e.st.FilterActivePaths(ctx, members)
			if err != nil {
				return fmt.Errorf("校验分组成员失败：%w", err)
			}
			members = live
			if len(members) == 0 {
				return fmt.Errorf("分组成员已变化，请刷新分组后重试")
			}
		}
		if len(members) == 0 {
			if g.Key == "" {
				continue
			}
			m, err := e.groupMemberPaths(ctx, g.Key)
			if err != nil {
				return err
			}
			if len(m) == 0 {
				return fmt.Errorf("未找到该分组的成员，请刷新分组后重试")
			}
			members = m
		}
		if err := KeepOneInGroup(members, paths); err != nil {
			return err
		}
	}
	return nil
}

// groupKeyForPath 取路径所属的分组键，用于回填隔离记录的「来源」列（v3.10.8，P2-9）。
// 快照带成员的组能精确匹配；只给了键的组无法反查，退回第一个键（跨组勾选时来源列仍近似）。
func groupKeyForPath(groups []TrashGroup, path string) string {
	if len(groups) == 0 {
		return ""
	}
	clean := filepath.Clean(path)
	for _, g := range groups {
		for _, m := range g.Members {
			if filepath.Clean(m) == clean {
				return g.Key
			}
		}
	}
	return groups[0].Key
}

// KeepOneInGroup 校验 selected 没有把 members 整组清空。
// members 为空（分组已不存在）同样拒绝，避免把「查不到组」误当成「无需校验」。
func KeepOneInGroup(members, selected []string) error {
	sel := make(map[string]bool, len(selected))
	for _, p := range selected {
		sel[filepath.Clean(p)] = true
	}
	for _, m := range members {
		if !sel[filepath.Clean(m)] {
			return nil
		}
	}
	return fmt.Errorf("该分组必须至少保留一个文件")
}

// ruleGroupPrefixes 是 internal/rules 生成的分组键前缀（kind + ":"）。
// 这类键带属性片段，无法按前缀用 SQL/分组函数重算，必须由结果页快照给出成员。
var ruleGroupPrefixes = []string{"content:", "name:", "size:", "attributes:", "all:"}

// groupMemberPaths 按分组键解析组内全部文件路径。
// 键有两套形态：旧分组接口给裸前缀（v:/intro:/audio:/img:/folder:/模糊裸键），
// 结果页快照给媒体组再加一层类型前缀（video:v:/intro:intro:/audio:audio:/photo:img:/folder:folder:）。
func (e *Engine) groupMemberPaths(ctx context.Context, groupKey string) ([]string, error) {
	switch {
	case strings.HasPrefix(groupKey, "h:"):
		return e.groupPathsBySQL(ctx, `SELECT path FROM files WHERE status='active' AND hash=?`, strings.TrimPrefix(groupKey, "h:"))
	case strings.HasPrefix(groupKey, "n:"):
		// 与旧实现同口径：同名组按小写文件名归组
		return e.groupPathsBySQL(ctx, `SELECT path FROM files WHERE status='active' AND lower(name)=?`, strings.TrimPrefix(groupKey, "n:"))
	case strings.HasPrefix(groupKey, "t:"):
		// v3.10.8（P1-3）：标签分组键 t:{tagID}:{规范化值}，规范化规则在 store 里，这里不重复实现
		return e.tagGroupPaths(ctx, groupKey)
	case strings.HasPrefix(groupKey, "import:"):
		// v3.10.10（第六轮 P1-3）：导入会话的隔离入口以前不带分组键，
		// 「同组至少保留一份」拿到空集合就放行，能整组清空。
		return e.importGroupPaths(ctx, groupKey)
	}
	for _, p := range ruleGroupPrefixes {
		if strings.HasPrefix(groupKey, p) {
			return nil, fmt.Errorf("该分组需要结果页快照才能校验，请返回结果页重新搜索后再试")
		}
	}
	if members, ok, err := e.mediaGroupPaths(ctx, groupKey); err != nil || ok {
		return members, err
	}
	// 模糊/语义分组的键是裸字符串或 fuzzy:/semantic: 前缀，且重算要遍历候选文件；
	// 带冒号的其它键已在媒体分组里试过，不再重复计算。
	if strings.HasPrefix(groupKey, "fuzzy:") || strings.HasPrefix(groupKey, "semantic:") || !strings.Contains(groupKey, ":") {
		if members, ok, err := e.fuzzySemanticPaths(ctx, groupKey); err != nil || ok {
			return members, err
		}
	}
	return nil, fmt.Errorf("无法识别的分组标识（%s），请刷新分组后重试", groupKey)
}

// importGroupPaths 解析导入会话分组键 import:{会话ID}:{组号}（v3.10.10，第六轮 P1-3）。
// 成员只取本机已匹配的行：未匹配的行磁盘上没有文件，不能当作「保留的那一份」。
func (e *Engine) importGroupPaths(ctx context.Context, groupKey string) ([]string, error) {
	idText, noText, ok := strings.Cut(strings.TrimPrefix(groupKey, "import:"), ":")
	sessionID, errID := strconv.ParseInt(idText, 10, 64)
	groupNo, errNo := strconv.ParseInt(noText, 10, 64)
	if !ok || errID != nil || errNo != nil || sessionID <= 0 || groupNo <= 0 {
		// 组号 0 是「唯一/孤组」，本来就没有重复副本，前端不会为它生成键
		return nil, fmt.Errorf("无法识别的分组标识（%s），请刷新后重试", groupKey)
	}
	members, err := e.st.ImportGroupMemberPaths(ctx, sessionID, groupNo)
	if err != nil {
		return nil, fmt.Errorf("解析导入分组成员失败（%v），请刷新后重试", err)
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("未找到该导入分组的成员，请刷新后重试")
	}
	return members, nil
}

// tagGroupPaths 解析标签分组键 t:{tagID}:{规范化值}（v3.10.8，P1-3）。
func (e *Engine) tagGroupPaths(ctx context.Context, groupKey string) ([]string, error) {
	idText, _, ok := strings.Cut(strings.TrimPrefix(groupKey, "t:"), ":")
	id, err := strconv.ParseInt(idText, 10, 64)
	if !ok || err != nil || id <= 0 {
		return nil, fmt.Errorf("无法识别的分组标识（%s），请刷新分组后重试", groupKey)
	}
	members, err := e.st.TagGroupMembers(ctx, id, groupKey)
	if err != nil {
		return nil, fmt.Errorf("解析标签分组成员失败（%v），请刷新分组后重试", err)
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("未找到该标签分组的成员，请刷新分组后重试")
	}
	return members, nil
}

// mediaGroupPaths 依次在视频/片头/音频/图片/文件夹分组里找 key 所属组。
func (e *Engine) mediaGroupPaths(ctx context.Context, groupKey string) ([]string, bool, error) {
	cfg := e.cfg.Get()
	families := []struct {
		kind string
		load func(context.Context) ([]store.Group, error)
	}{
		{"video", func(ctx context.Context) ([]store.Group, error) { return e.VideoGroups(ctx, false) }},
		{"intro", func(ctx context.Context) ([]store.Group, error) { return e.IntroGroups(ctx, cfg.IntroSeconds, false) }},
		{"audio", func(ctx context.Context) ([]store.Group, error) {
			g, _, err := e.st.AudioGroups(ctx, cfg.EffectiveAudioThreshold())
			return g, err
		}},
		{"photo", func(ctx context.Context) ([]store.Group, error) {
			g, _, err := e.st.PhotoGroups(ctx, cfg.ImgHamming)
			return g, err
		}},
		{"folder", func(ctx context.Context) ([]store.Group, error) {
			g, _, err := e.st.FolderGroups(ctx)
			return g, err
		}},
	}
	for _, f := range families {
		groups, err := f.load(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("解析分组成员失败（%v），请刷新分组后重试", err)
		}
		// 键可能是旧接口的裸前缀（intro:12），也可能是快照加过类型的键（intro:intro:12），两种都认
		want := strings.TrimPrefix(groupKey, f.kind+":")
		if members, ok := findGroupPaths(groups, groupKey, want); ok {
			return members, true, nil
		}
	}
	return nil, false, nil
}

// fuzzySemanticPaths 在模糊（同键/近似两套）与语义分组里找 key 所属组。
func (e *Engine) fuzzySemanticPaths(ctx context.Context, groupKey string) ([]string, bool, error) {
	want := strings.TrimPrefix(groupKey, "fuzzy:")
	for _, near := range []bool{false, true} {
		groups, err := e.FuzzyGroups(ctx, near)
		if err != nil {
			return nil, false, fmt.Errorf("解析分组成员失败（%v），请刷新分组后重试", err)
		}
		if members, ok := findGroupPaths(groups, groupKey, want); ok {
			return members, true, nil
		}
	}
	groups, err := e.SemanticGroups(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("解析分组成员失败（%v），请刷新分组后重试", err)
	}
	members, ok := findGroupPaths(groups, groupKey, strings.TrimPrefix(groupKey, "semantic:"))
	return members, ok, nil
}

// findGroupPaths 在候选键里逐组比对，取第一个命中的分组全员；找不到返回 ok=false。
func findGroupPaths(groups []store.Group, wants ...string) ([]string, bool) {
	for _, g := range groups {
		for _, w := range wants {
			if w == "" || g.Key != w {
				continue
			}
			paths := make([]string, 0, len(g.Files))
			for _, f := range g.Files {
				paths = append(paths, f.Path)
			}
			return paths, true
		}
	}
	return nil, false
}

// groupPathsBySQL 用一条只读查询取成员路径。
func (e *Engine) groupPathsBySQL(ctx context.Context, query, arg string) ([]string, error) {
	rows, err := e.st.DB().QueryContext(ctx, query, arg)
	if err != nil {
		return nil, fmt.Errorf("解析分组成员失败（%v），请刷新分组后重试", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// Restore 从隔离区恢复文件。
// v3.25.2（P3-5）：对外入口补闸门。转码替换失败、剪除失败的回滚已持有 FFmpeg 独占组，
// 必须走 restoreLocked，否则回滚会被自己的闸门判成冲突——那比不加闸门更危险。
func (e *Engine) Restore(ctx context.Context, ids []int64) *RestoreResult {
	done, err := e.acquireTask(taskguard.KindTrash, "恢复隔离区", taskguard.GroupIO, taskguard.ResDiskIO)
	if err != nil {
		return &RestoreResult{Conflict: err.Error()}
	}
	defer done("done")
	return e.restoreLocked(ctx, ids)
}

// restoreLocked 是 Restore 的实际实现，不加闸门，供已持有任务的内部回滚复用。
func (e *Engine) restoreLocked(ctx context.Context, ids []int64) *RestoreResult {
	start := time.Now()
	unlock := e.lockWrites()
	defer unlock()
	// v3.10.10（第五轮 P2-1）：文件已恢复后，隔离记录与索引必须写完。
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	res := &RestoreResult{}
	var details []oplog.Detail
	for i, id := range ids {
		// v4.0.1（安全审查 问题2）：请求取消时剩余 id 没处理，不能落到「回收站记录不存在」。
		if ctx.Err() != nil {
			res.Unprocessed = len(ids) - i
			res.Errors = append(res.Errors, fmt.Sprintf("操作已取消，剩余 %d 个未处理", res.Unprocessed))
			break
		}
		t, err := e.st.GetTrash(ctx, id)
		if err != nil || t == nil {
			res.Failed = append(res.Failed, fmt.Sprint(id))
			res.Errors = append(res.Errors, "回收站记录不存在")
			continue
		}
		if _, err := os.Lstat(t.TrashPath); err != nil {
			res.Failed = append(res.Failed, t.OrigPath)
			res.Errors = append(res.Errors, "隔离区文件已丢失")
			continue
		}
		// v3.7.1：执行前复核隔离区路径仍在受管卷内（审查报告 §6 判定 BLOCK 项）
		if reason := e.checkTrashRowPath(*t); reason != "" {
			res.Failed = append(res.Failed, t.OrigPath)
			res.Errors = append(res.Errors, reason)
			continue
		}
		target := t.OrigPath
		// 恢复目标也必须在受管卷内，防止脏记录把文件写出卷外、或借 MkdirAll 在卷外建目录
		if cleaned := filepath.Clean(target); e.VolumeOf(cleaned) == "" {
			res.Failed = append(res.Failed, t.OrigPath)
			res.Errors = append(res.Errors, "原路径不在受管存储卷内")
			continue
		}
		// v3.17.5（F-16）：父目录可能已被删除，EvalSymlinks 会失败；旧逻辑失败时
		// 直接跳过校验，MkdirAll 会顺着路径里的软链接在卷外建目录。
		// 这里上溯到第一个存在的祖先解析并重判卷前缀，再把尚不存在的部分拼回去。
		parent, ok := e.resolveManagedParent(filepath.Dir(target))
		if !ok {
			res.Failed = append(res.Failed, t.OrigPath)
			res.Errors = append(res.Errors, "原路径父目录不在受管存储卷内或无法解析")
			continue
		}
		target = filepath.Join(parent, filepath.Base(target))
		if _, err := os.Lstat(target); err == nil {
			alt, altErr := alternateName(target)
			if altErr != nil {
				res.Failed = append(res.Failed, t.OrigPath)
				res.Errors = append(res.Errors, altErr.Error())
				continue
			}
			target = alt
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			res.Failed = append(res.Failed, t.OrigPath)
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		if err := moveFile(t.TrashPath, target); err != nil {
			res.Failed = append(res.Failed, t.OrigPath)
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		// v3.10.10（第五轮 P2-1）：删行失败会让恢复过的文件在隔离区永远显示「已丢失」，
		// 并让 TrashBytes 虚高；错误必须回到响应里，不能 `_ =` 吞掉。
		if err := e.st.DeleteTrashRows(wctx, []int64{id}); err != nil {
			applog.Warn("恢复已完成，但隔离记录删除失败（id=%d，%s）：%v", id, target, err)
			res.Errors = append(res.Errors, "已恢复，但隔离记录未能删除："+err.Error())
		}
		if fi, err := os.Lstat(target); err == nil && fi.Mode().IsRegular() {
			vol := e.VolumeOf(target)
			if vol != "" {
				info := fileInfoOf(target, vol, fi)
				if _, uerr := e.st.UpsertOne(wctx, info, e.st.CurrentGeneration(wctx)); uerr != nil {
					applog.Warn("恢复后写入索引失败：%s（%v）", target, uerr)
					res.Errors = append(res.Errors, "已恢复，但索引写入失败："+uerr.Error())
				}
			}
		}
		res.Restored = append(res.Restored, target)
		details = append(details, oplog.Detail{
			Path: t.TrashPath, NewPath: target, Size: t.Size, Hash: t.Hash,
		})
		// v3.10.10（第五轮 P2-2）：改为同步补录哈希。以前 go 出去，
		// 进程退出时这次补录就丢了，用户重复恢复会落成「(恢复 1)」副本。
		e.UpdatePath(target)
	}
	entry := oplog.New("trash_restore")
	entry.Summary = fmt.Sprintf("从隔离区恢复 %d 个文件，失败 %d 个", len(res.Restored), len(res.Failed))
	if res.Unprocessed > 0 {
		entry.Summary += fmt.Sprintf("，取消未处理 %d 个", res.Unprocessed)
	}
	entry.TargetCount = len(ids)
	entry.Details = details
	entry.SetFailure(res.Errors)
	entry.DurationMS = time.Since(start).Milliseconds()
	// 恢复同样可回退，审计失败只记警告（fail-open）。
	if err := e.recordOp(ctx, entry); err != nil {
		applog.Warn("隔离区恢复已完成，但操作记录写入失败：%v", err)
	}
	e.bumpVersion()
	e.Bus.Publish(Event{Type: "update"})
	return res
}

// alternateName 为恢复目标找一个不会被占用的名字，找不到时返回错误而不是硬给一个名字。
//
// P3-1（2026-11-11）：旧实现的兜底分支直接返回纳秒时间戳名，没有复核存在性。
// 时间戳撞名、或 Lstat 报「不存在」以外的错误（权限、IO）时，调用方会拿一个
// 可能已存在的路径去改名，把同名文件覆盖且无法恢复。现在兜底名同样逐个复核。
func alternateName(p string) (string, error) {
	return alternateNameAt(p, time.Now())
}

// alternateNameAt 是 alternateName 的可注入时钟版本，便于回归测试构造「时间戳兜底名撞名」。
func alternateNameAt(p string, now time.Time) (string, error) {
	ext := filepath.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	for i := 1; i < 1000; i++ {
		c := fmt.Sprintf("%s (恢复 %d)%s", stem, i, ext)
		if _, err := os.Lstat(c); os.IsNotExist(err) {
			return c, nil
		}
	}
	base := fmt.Sprintf("%s (恢复 %d)", stem, now.UnixNano())
	for i := 0; i < 1000; i++ {
		c := base + ext
		if i > 0 {
			c = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		if _, err := os.Lstat(c); os.IsNotExist(err) {
			return c, nil
		}
	}
	return "", fmt.Errorf("连续 2000 次未找到可用的恢复文件名：%s", p)
}

// DeletePermanent 永久删除隔离区文件。
// 不可逆操作：审计行必须独立提交成功，才允许碰文件（fail-closed）。
// 审计里存的是获准删除的完整清单；隔离区记录随后被清掉，靠审计行还原「删过什么」。
func (e *Engine) DeletePermanent(ctx context.Context, ids []int64) *TrashResult {
	// v3.25.2（P3-5）：永久删除是磁盘写 + 审计写，与扫描 / 转码互斥。
	done, gerr := e.acquireTask(taskguard.KindTrash, "永久删除隔离文件", taskguard.GroupIO, taskguard.ResDiskIO)
	if gerr != nil {
		return &TrashResult{Conflict: gerr.Error()}
	}
	defer done("done")
	start := time.Now()
	unlock := e.lockWrites()
	defer unlock()
	// v3.10.10（第五轮 P2-1）：文件已删掉后，隔离记录的清理不能跟请求取消一起丢。
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	res := &TrashResult{}
	type pending struct {
		id  int64
		row *store.TrashRow
	}
	var pend []pending
	var details []oplog.Detail
	for _, id := range ids {
		t, err := e.st.GetTrash(ctx, id)
		if err != nil || t == nil {
			continue
		}
		if reason := e.protectedReason(t.OrigPath); reason != "" {
			res.Failed = append(res.Failed, t.OrigPath)
			res.Errors = append(res.Errors, reason)
			continue
		}
		// v3.7.1：记账前先复核记录路径仍在受管卷隔离区内（审查报告 §6 判定 BLOCK 项）
		if reason := e.checkTrashRowPath(*t); reason != "" {
			res.Failed = append(res.Failed, t.OrigPath)
			res.Errors = append(res.Errors, reason)
			continue
		}
		pend = append(pend, pending{id: id, row: t})
		details = append(details, oplog.Detail{
			Path: t.OrigPath, NewPath: t.TrashPath, Size: t.Size, Hash: t.Hash,
		})
	}
	entry := oplog.New("trash_delete")
	entry.Summary = fmt.Sprintf("永久删除隔离区文件 %d 个", len(pend))
	entry.TargetCount = len(pend)
	entry.Details = details
	entry.SetFailure(res.Errors)
	entry.DurationMS = time.Since(start).Milliseconds()
	if err := e.recordOp(ctx, entry); err != nil {
		// 记录写不进去，一个文件都不许删。
		for _, p := range pend {
			res.Failed = append(res.Failed, p.row.OrigPath)
			res.Errors = append(res.Errors, err.Error())
		}
		return res
	}
	var removedIDs []int64
	// v3.10.5：审计通过、真正动手前才拍快照；预期这些隔离区文件随后都不存在。
	snapPaths := make([]string, 0, len(pend))
	for _, p := range pend {
		snapPaths = append(snapPaths, p.row.TrashPath)
	}
	snap := e.beginOpSnapshot(ctx, "delete", snapPaths)
	var removeFailDetails []oplog.Detail
	var removeFailErrs []string
	for _, p := range pend {
		t := p.row
		if err := os.Remove(t.TrashPath); err != nil && !os.IsNotExist(err) {
			// 授权审计行已在前面落库且只增不改，这里只收集删除阶段的失败，稍后补写结果审计。
			applog.Warn("永久删除失败（%s）：%v", t.TrashPath, err)
			snap.Skip(t.TrashPath, err.Error())
			res.Failed = append(res.Failed, t.TrashPath)
			res.Errors = append(res.Errors, err.Error())
			removeFailDetails = append(removeFailDetails, oplog.Detail{
				Path: t.OrigPath, NewPath: t.TrashPath, Size: t.Size, Hash: t.Hash, Reason: err.Error(),
			})
			removeFailErrs = append(removeFailErrs, err.Error())
			continue
		}
		removedIDs = append(removedIDs, p.id)
		res.Moved = append(res.Moved, t.OrigPath)
	}
	// v4.0.1（安全审查 问题1）：删除阶段有失败时补写一条结果审计，记录实际成功数/失败数/原因。
	// 授权行只增不改，这里追加第二行，事后追溯才能看到「实际删除清单」而不是「获准清单」。
	e.appendTrashResultAudit(wctx, "trash_delete", "永久删除结果", len(pend), len(removedIDs), removeFailDetails, removeFailErrs, start)
	if err := e.st.DeleteTrashRows(wctx, removedIDs); err != nil {
		applog.Warn("永久删除已完成，但隔离记录清理失败（%d 条）：%v", len(removedIDs), err)
		res.Errors = append(res.Errors, "文件已删除，但隔离记录未能清理："+err.Error())
	}
	// v3.17.4（F-20）：永久删除释放了空间，Counts 聚合缓存与搜索快照必须失效，
	// 否则隔离区大小与可释放空间会滞留到下一次扫描。
	e.bumpVersion()
	e.Bus.Publish(Event{Type: "update"})
	res.Reconcile = e.finishOpSnapshot(snap)
	return res
}

// EmptyTrash 清空全部隔离区。同样先写审计再删文件（fail-closed），一次操作只写一行汇总。
func (e *Engine) EmptyTrash(ctx context.Context) (int64, []string) {
	// v3.25.2（P3-5）：清空隔离区同样是磁盘写，必须在闸门里做。
	done, gerr := e.acquireTask(taskguard.KindTrash, "清空隔离区", taskguard.GroupIO, taskguard.ResDiskIO)
	if gerr != nil {
		return 0, []string{gerr.Error()}
	}
	defer done("done")
	start := time.Now()
	unlock := e.lockWrites()
	defer unlock()
	// v3.10.10（第五轮 P2-1）：清空已落盘，记录清理不跟请求取消一起丢。
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	rows, err := e.st.ListTrash(ctx)
	if err != nil {
		return 0, []string{err.Error()}
	}
	// v3.7.1：逐条复核记录路径仍在受管卷隔离区内，路径异常的条目跳过不动（审查报告 §6）
	// 避免「清空」顺带删除卷外文件
	var skipped []string
	safe := rows[:0]
	for _, t := range rows {
		if reason := e.checkTrashRowPath(t); reason != "" {
			applog.Warn("清空隔离区时跳过异常记录（%s）：%s", t.TrashPath, reason)
			skipped = append(skipped, t.OrigPath+"（"+reason+"）")
			continue
		}
		safe = append(safe, t)
	}
	rows = safe
	// v3.10.8（P2-9）：没有可删条目时不写「共 0 个条目」的审计行，直接返回跳过的明细
	if len(rows) == 0 {
		return 0, skipped
	}
	details := make([]oplog.Detail, 0, len(rows))
	for _, t := range rows {
		details = append(details, oplog.Detail{
			Path: t.OrigPath, NewPath: t.TrashPath, Size: t.Size, Hash: t.Hash,
		})
	}
	entry := oplog.New("trash_empty")
	entry.Summary = fmt.Sprintf("清空隔离区，共 %d 个条目", len(rows))
	entry.TargetCount = len(rows)
	entry.Details = details
	entry.DurationMS = time.Since(start).Milliseconds()
	if err := e.recordOp(ctx, entry); err != nil {
		return 0, []string{err.Error()}
	}
	var ids []int64
	var errs []string
	// v3.10.5：清空同样是不可逆批量删除，先拍快照；该接口只回计数与错误串，
	// 对账结果落库并写日志（不一致会以 Warn 形式出现在 verify.log）。
	snapPaths := make([]string, 0, len(rows))
	for _, t := range rows {
		snapPaths = append(snapPaths, t.TrashPath)
	}
	snap := e.beginOpSnapshot(ctx, "trash_empty", snapPaths)
	var removeFailDetails []oplog.Detail
	var removeFailErrs []string
	for _, t := range rows {
		if err := os.Remove(t.TrashPath); err != nil && !os.IsNotExist(err) {
			applog.Warn("清空隔离区时删除失败（%s）：%v", t.TrashPath, err)
			snap.Skip(t.TrashPath, err.Error())
			errs = append(errs, err.Error())
			removeFailDetails = append(removeFailDetails, oplog.Detail{
				Path: t.OrigPath, NewPath: t.TrashPath, Size: t.Size, Hash: t.Hash, Reason: err.Error(),
			})
			removeFailErrs = append(removeFailErrs, err.Error())
			continue
		}
		ids = append(ids, t.ID)
	}
	errs = append(errs, skipped...)
	// v4.0.1（安全审查 问题1）：清空阶段的失败同样补写结果审计（授权行只增不改）。
	e.appendTrashResultAudit(wctx, "trash_empty", "清空隔离区结果", len(rows), len(ids), removeFailDetails, removeFailErrs, start)
	if err := e.st.DeleteTrashRows(wctx, ids); err != nil {
		applog.Warn("清空隔离区已完成，但记录清理失败（%d 条）：%v", len(ids), err)
		errs = append(errs, "文件已删除，但隔离记录未能清理："+err.Error())
	}
	// v3.17.4（F-20）：与永久删除同口径，清空后失效聚合缓存。
	e.bumpVersion()
	e.Bus.Publish(Event{Type: "update"})
	_ = e.finishOpSnapshot(snap)
	return int64(len(ids)), errs
}

// HealTrashRows 清理幽灵隔离行（v3.10.10，第五轮 P2-1）。
// 隔离文件已不存在、且所在卷当前可访问时删掉对应 quarantine 行：
// 否则隔离区永远显示「已丢失」条目，TrashBytes 与「可释放空间」都是虚的。
// 卷根不可访问（临时卸载、网络卷断开）时一律保留记录，宁可多留也不误删恢复线索。
func (e *Engine) HealTrashRows(ctx context.Context, rows []store.TrashRow) []store.TrashRow {
	if len(rows) == 0 {
		return rows
	}
	vols := e.Volumes()
	kept := make([]store.TrashRow, 0, len(rows))
	dead := make([]int64, 0)
	// v3.25.1：判定收拢到 internal/trashvol，正反例测试可以直接跑。
	// 关键点：卷根 Stat 成功 ≠ 卷真的挂载着。飞牛存储池或外接盘卸载后，
	// 挂载点常残留空目录，此时隔离文件全部 Lstat 不到。隔离目录是应用建在卷根下的，
	// 卷真正挂载时它必然存在；不存在就按「卷不可信」保留，避免整卷恢复线索被清空。
	for _, t := range rows {
		if trashvol.RowRemovable(t.TrashPath, vols) {
			dead = append(dead, t.ID)
			continue
		}
		kept = append(kept, t)
	}
	if len(dead) == 0 {
		return kept
	}
	if err := e.st.DeleteTrashRows(ctx, dead); err != nil {
		applog.Warn("清理幽灵隔离记录失败（%d 条）：%v", len(dead), err)
		return rows
	}
	applog.Info("已清理 %d 条幽灵隔离记录（隔离文件已不存在）", len(dead))
	return kept
}

// appendTrashResultAudit 在删除/清空有失败时补写一条「结果审计」（v4.0.1 安全审查 问题1）。
// 前面的授权行在动手前落库且只增不改；这一行记录实际成功数、失败数与失败原因，
// 事后按「操作记录」追溯时才不会把「获准清单」误当成「实际清单」。无失败则不写，避免噪音。
func (e *Engine) appendTrashResultAudit(ctx context.Context, action, title string, planned, succeeded int, failDetails []oplog.Detail, failErrs []string, start time.Time) {
	if len(failErrs) == 0 {
		return
	}
	entry := oplog.New(action)
	entry.Summary = fmt.Sprintf("%s：计划 %d，成功 %d，失败 %d", title, planned, succeeded, len(failErrs))
	entry.TargetCount = planned
	entry.Details = failDetails
	if succeeded == 0 {
		entry.Result = oplog.ResultFail
	} else {
		entry.Result = oplog.ResultPartial
	}
	entry.Error = oplog.JoinErrors(failErrs)
	entry.DurationMS = time.Since(start).Milliseconds()
	if err := e.recordOp(ctx, entry); err != nil {
		applog.Warn("%s结果审计写入失败：%v", title, err)
	}
}

// HealTrashMaintenance 在写锁内清理幽灵隔离行（v4.0.1 安全审查 问题3）。
// GET /api/trash 及导出退回纯读后，幽灵行改由启动/维护任务清理；放进 lockWrites
// 保证与其它写入口互斥，不再出现「GET 路径偷偷 DELETE」。
func (e *Engine) HealTrashMaintenance(ctx context.Context) {
	unlock := e.lockWrites()
	defer unlock()
	rows, err := e.st.ListTrash(ctx)
	if err != nil {
		applog.Warn("清理幽灵隔离行前读取隔离区失败：%v", err)
		return
	}
	e.HealTrashRows(ctx, rows)
}

// StartTrashMaintenance 在后台启动一次幽灵隔离行清理（v4.0.1 问题3）。
// 由 main 在引擎就绪后调用；测试不自动触发，避免与造幽灵行的用例竞态。
func (e *Engine) StartTrashMaintenance() {
	go e.HealTrashMaintenance(e.baseCtx)
}

// moveFile 把文件搬到目标位置；目标已存在时拒绝，绝不覆盖。
//
// P3-2（2026-11-11）：首段改名改用 renameNoOverwrite。隔离与恢复的落点都靠它，
// 裸 os.Rename 会在「目标检查通过 → 真正改名」的窗口里覆盖外部刚建的同名文件，
// 而被覆盖的文件既不进隔离区、也没有审计，无法恢复。
func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	if err := renameNoOverwrite(src, dst); err == nil {
		return nil
	} else if _, lerr := os.Lstat(dst); lerr == nil {
		// 目标确实被占用：直接返回「已存在」，不能再走下面的复制分支，
		// 否则等于换一种方式覆盖别人的文件。
		return err
	}
	// 跨文件系统时退化为复制 + 删除（v3.10.8，P2-3）：
	// 复制成功后删不掉源文件，会同时留下源与隔离区副本，回滚副本并上抛，避免无记录的孤儿文件。
	if err := copyFile(src, dst); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		if rmErr := os.Remove(dst); rmErr != nil {
			return fmt.Errorf("跨卷移动失败：源文件未删除且隔离区副本无法回滚，请人工核对（%v；%v）", err, rmErr)
		}
		return fmt.Errorf("跨卷移动失败：源文件未删除，已撤销隔离区副本（%v）", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	// src/dst 由 buildTrashPath 与隔离区记录生成，两者都在受管卷内。
	in, err := os.Open(src) // #nosec G304 -- src 为待隔离的已索引文件，路径由索引与隔离区记录给出
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, info.Mode().Perm()) // #nosec G304 -- dst 由 buildTrashPath 在隔离区内生成
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	// v3.10.10（第五轮 P1-6）：跨卷隔离是「先复制、再删源」，副本必须先真正落盘，
	// 否则断电时源已删除、副本还停在页缓存里，等于把文件弄丢。
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("同步隔离区副本失败（%w）", err)
	}
	if err := out.Close(); err != nil {
		// 关闭失败说明数据可能未落盘，已写入的目标文件必须清掉，避免留下半截副本。
		_ = os.Remove(dst)
		return err
	}
	_ = os.Chtimes(dst, time.Now(), info.ModTime())
	// 文件内容已落盘还不够，目录项也要刷盘，否则崩溃后可能看不到这个副本。
	syncDir(filepath.Dir(dst))
	return nil
}

// syncDir 尽力把目录项刷到磁盘。部分文件系统（如容器 overlay）不支持对目录 fsync，
// 失败只记日志不阻断：此时再拒绝隔离会让用户完全无法使用跨卷清理。
func syncDir(dir string) {
	f, err := os.Open(dir) // #nosec G304 -- dir 为隔离区目录，由 buildTrashPath 生成
	if err != nil {
		applog.Warn("打开目录准备同步失败（%s）：%v", dir, err)
		return
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		applog.Warn("目录同步失败（%s）：%v", dir, err)
	}
}
