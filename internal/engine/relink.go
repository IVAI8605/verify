// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"strings"
	"syscall"

	"verify/internal/applog"
	"verify/internal/hashx"
	"verify/internal/store"
)

// relinkTmpSuffix 是硬链接替换用的临时文件后缀（同目录保证同文件系统）。
const relinkTmpSuffix = ".duplink.tmp"

// RelinkItem 是一个待合并目标的预检/执行结果。
type RelinkItem struct {
	Path   string `json:"path"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// RelinkResult 是一次硬链接合并的完整结果。
type RelinkResult struct {
	Keep    string       `json:"keep"`
	Items   []RelinkItem `json:"items"`
	OKCount int          `json:"ok_count"`
	Bytes   int64        `json:"bytes"` // 实际释放（成功目标的文件大小之和）
}

// relinkCheck 对单个目标做前置校验（设计文档 9.2.2），返回空串表示通过。
// verifyContent=true 时当场重算全量哈希（预览用）；执行阶段传 false，
// 只复核 Lstat 三元组，避免在写锁内按文件大小做 O(n) 次哈希（v3.17.5 F-14）。
func (e *Engine) relinkCheck(keepPath, targetPath string, verifyContent bool) string {
	if targetPath == keepPath {
		return "与保留者是同一路径"
	}
	// v1.30.1 安全修复：三步防线——此前只靠 VolumeOf 原始前缀匹配，
	// `..` 形态路径可通过同卷检查对卷外文件建链并原子替换。
	if !e.ValidPath(keepPath) {
		return "保留者不在受管卷内或路径无法解析"
	}
	if !e.ValidPath(targetPath) {
		return "目标不在受管卷内或路径无法解析"
	}
	if info, err := os.Lstat(targetPath); err != nil || info.IsDir() {
		return "目标不存在或不是普通文件"
	}
	if info, err := os.Lstat(keepPath); err != nil || info.IsDir() {
		return "保留者不存在或不是普通文件"
	} // 跨卷：硬链接不能跨文件系统（EXDEV 在 os.Link 阶段也会兜底）
	if e.VolumeOf(keepPath) != e.VolumeOf(targetPath) {
		return "跨卷无法硬链接"
	}
	// 保护文件夹与隔离区（设计文档 9.2.2：两侧都不得在保护目录/隔离区内）
	if r := e.protectedReasonResolved(keepPath); r != "" {
		return "保留者受保护：" + r
	}
	if r := e.protectedReasonResolved(targetPath); r != "" {
		return "目标受保护：" + r
	}
	// 已链接识别（设计文档 9.2.6）：inode 相同说明已是同一份实体，无需也不应重复合并
	tinfo, err := os.Lstat(targetPath)
	if err != nil {
		return "目标不存在"
	}
	kinfo, err := os.Lstat(keepPath)
	if err != nil {
		return "保留者不存在"
	}
	ti, _ := tinfo.Sys().(*syscall.Stat_t)
	ki, _ := kinfo.Sys().(*syscall.Stat_t)
	if ti != nil && ki != nil && ti.Ino == ki.Ino {
		return "已与保留者是同一个文件（已链接）"
	}
	// v3.17.5（F-14）：执行阶段不再重算全量哈希——预览已算过，锁内重算会让写锁
	// 按文件大小线性占住（一批 500 个 4K 影片就是几百 GB 读）。这里只复核
	// Lstat 三元组，文件在「预览 → 执行」之间被改动就拒绝，宁可不合并。
	if !verifyContent {
		if tinfo.Size() != kinfo.Size() || !tinfo.ModTime().Equal(kinfo.ModTime()) {
			return "文件在预览后发生变化，请重新预览"
		}
		return ""
	}
	// 内容必须当场重算相等（设计文档 9.2.2：不用库里旧值——文件可能已被改动）
	th, err := hashx.FullHash(targetPath)
	if err != nil {
		return "无法读取目标文件：" + err.Error()
	}
	kh, err := hashx.FullHash(keepPath)
	if err != nil {
		return "无法读取保留者文件：" + err.Error()
	}
	if th != kh {
		return "内容已与保留者不一致（文件可能被修改），拒绝合并"
	}
	return ""
}

// RelinkPreview 对全部目标做预检，不产生任何变更。
func (e *Engine) RelinkPreview(ctx context.Context, keepPath string, targets []string) *RelinkResult {
	res := &RelinkResult{Keep: keepPath}
	for _, t := range targets {
		item := RelinkItem{Path: t}
		if r := e.relinkCheck(keepPath, t, true); r != "" {
			item.Reason = r
		} else {
			item.OK = true
		}
		res.Items = append(res.Items, item)
	}
	return res
}

// RelinkFiles 用硬链接替代删除（设计文档 9.1 关键顺序）：
//
//  1. os.Link(keepPath, target+".duplink.tmp")   先在同目录建临时硬链接
//  2. os.Rename(tmp, target)                     POSIX 原子替换，全程目标路径有效
//
// 绝不使用 os.Remove + os.Link——中间窗口期 link 失败会丢文件。
// 成功后写审计（operation=relink）并回写 files 表的 inode/nlink。
func (e *Engine) RelinkFiles(ctx context.Context, keepPath string, targets []string) *RelinkResult {
	unlock := e.lockWrites()
	defer unlock()
	// v3.10.10（第五轮 P1-3）：硬链接替换完成后，审计与索引不能因请求取消而丢失。
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	res := &RelinkResult{Keep: keepPath}
	taskID, err := e.st.NewAuditTaskID(wctx)
	if err != nil {
		applog.Warn("获取审计任务号失败：%v", err)
	}
	var linked int
	var freed int64
	for _, t := range targets {
		item := RelinkItem{Path: t}
		if r := e.relinkCheck(keepPath, t, false); r != "" {
			item.Reason = r
			res.Items = append(res.Items, item)
			continue
		}
		tmp := t + relinkTmpSuffix
		_ = os.Remove(tmp) // 残留清理（上次中断可能遗留）
		if err := os.Link(keepPath, tmp); err != nil {
			item.Reason = friendlyLinkError(err)
			res.Items = append(res.Items, item)
			continue
		}
		if err := os.Rename(tmp, t); err != nil {
			_ = os.Remove(tmp) // 回收临时链接，目标原文件未被触碰
			item.Reason = "原子替换失败：" + err.Error()
			res.Items = append(res.Items, item)
			continue
		}
		// 成功：审计 + 索引回写（目标行 inode 变为保留者 inode，两侧 nlink 以实际 stat 为准）
		if err := e.st.AddAudit(wctx, store.AuditEntry{
			TaskID:       taskID,
			Operation:    "relink",
			OriginalPath: t,
			NewPath:      keepPath,
		}); err != nil {
			applog.Warn("硬链接替换已完成，但操作记录写入失败：%s → %s（%v）", t, keepPath, err)
		}
		e.refreshLinkMeta(keepPath, t)
		if info, err := os.Stat(t); err == nil {
			freed += info.Size()
		}
		linked++
		item.OK = true
		res.Items = append(res.Items, item)
	}
	res.OKCount = linked
	res.Bytes = freed
	if linked > 0 {
		applog.Info("硬链接合并完成：%d 个目标并入 %s，实际释放 %d 字节", linked, keepPath, freed)
		e.bumpVersion()
	}
	return res
}

// refreshLinkMeta 合并后按实际 stat 回写 files 表的 inode/nlink（两侧都要刷新）。
func (e *Engine) refreshLinkMeta(keepPath, targetPath string) {
	for _, p := range []string{keepPath, targetPath} {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		// #nosec G115 -- 真实文件系统（ext4/btrfs/xfs）inode 号恒小于 2^63，转换无溢出
		ino, nlink := int64(st.Ino), int64(st.Nlink)
		_ = e.st.UpdateLinkMeta(context.Background(), p, ino, nlink)
	}
}

// friendlyLinkError 把 os.Link 的典型错误翻译成中文操作提示。
func friendlyLinkError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "cross device link") || strings.Contains(msg, "invalid cross-device link") {
		return "跨卷无法硬链接"
	}
	if strings.Contains(msg, syscall.EPERM.Error()) {
		return "文件系统不允许硬链接（权限或文件系统类型限制）：" + msg
	}
	return "创建硬链接失败：" + msg
}

// v3.9.10：原 RelinkTargetSize 已删除——它只是 os.Stat 取体积的薄封装，
// 全项目零调用；释放空间改用组内文件大小直接计算，避免多一次磁盘探测。
