// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"verify/internal/applog"
	"verify/internal/fname"
	"verify/internal/store"
)

// F5 按文件夹名重命名（v1.36 引入，v1.37 落地）：
// A 类（有汉字/有意义描述）保留原名（可选加 `<文件夹名> - ` 前缀，默认不加）；
// B 类（纯序号/相机码 IMG_/DSC_/纯数字）替换为 `<文件夹名>_001`。
// 复用 v1.15 改名基建：冲突追加 _1、audit 可撤销、保护目录拦截、ValidPath 校验。

// serialRe B 类串行模式：可选相机/设备前缀 + 数字序号（允许 -_ 分隔）。
var serialRe = regexp.MustCompile(`^(?i)(img|dsc|dji|vid|video|mov|photo|p|mvi|gopro)?[\s._-]*[0-9]{1,8}([-_][0-9]{1,4})?$`)

// ClassifyStem 判定主名类别："serial"（B 类，可替换）或 "keep"（A 类，保留）。
func ClassifyStem(stem string) string {
	s := strings.TrimSpace(stem)
	if s == "" {
		return "keep"
	}
	hasCJK := false
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			hasCJK = true
			break
		}
	}
	if hasCJK {
		return "keep"
	}
	if serialRe.MatchString(s) {
		return "serial"
	}
	// 无汉字但含其他字母词（如 "holiday"）→ A 类
	for _, r := range s {
		if unicode.IsLetter(r) {
			return "keep"
		}
	}
	return "keep"
}

// lastDigits 提取主名末尾的连续数字串；无数字返回空。
func lastDigits(stem string) string {
	rs := []rune(strings.TrimSpace(stem))
	i := len(rs)
	for i > 0 && rs[i-1] >= '0' && rs[i-1] <= '9' {
		i--
	}
	if i >= len(rs) {
		return ""
	}
	return string(rs[i:])
}

// FolderRenameItem 预览条目。
type FolderRenameItem struct {
	Path  string `json:"path"`
	Old   string `json:"old"`
	New   string `json:"new"`
	Class string `json:"class"` // serial | keep
	Skip  bool   `json:"skip"`  // keep 类且未开前缀：不改动
	Err   string `json:"error,omitempty"`
}

// FolderRenamePreview 预览结果。
type FolderRenamePreview struct {
	Dir    string             `json:"dir"`
	TaskID int64              `json:"task_id,omitempty"` // 执行后返回，供撤销
	Items  []FolderRenameItem `json:"items"`
	// RenameCount 预览中实际会被改名的条数。
	RenameCount int `json:"rename_count"`
}

// FolderRenameOptions 执行选项。
type FolderRenameOptions struct {
	PrefixA   bool   `json:"prefix_a"`  // A 类也加 `<文件夹名> - ` 前缀（默认否）
	Separator string `json:"separator"` // A 类前缀分隔符，默认 " - "
	MaxFiles  int    `json:"max_files"` // 单次上限（防误操作大目录）
}

// folderRenamePlan 计算目录内文件的改名计划（预览与执行共用）。
func folderRenamePlan(dir string, opt FolderRenameOptions) (*FolderRenamePreview, error) {
	dir = filepath.Clean(dir)
	if opt.MaxFiles <= 0 || opt.MaxFiles > 5000 {
		opt.MaxFiles = 2000
	}
	sep := opt.Separator
	if sep == "" {
		sep = " - "
	}
	folder := filepath.Base(dir)
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	preview := &FolderRenamePreview{Dir: dir}
	used := map[string]bool{} // 新名占用表（含原名，防互撞）
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		used[strings.ToLower(de.Name())] = true
	}
	// B 类顺序编号兜底（原名无数字时）
	serial := 0
	for _, de := range des {
		if de.IsDir() || len(preview.Items) >= opt.MaxFiles {
			break
		}
		name := de.Name()
		p := filepath.Join(dir, name)
		ext := filepath.Ext(name)
		stem := fname.Stem(name)
		class := ClassifyStem(stem)
		it := FolderRenameItem{Path: p, Old: name, Class: class}
		switch class {
		case "keep":
			if !opt.PrefixA {
				it.Skip = true
				it.New = name
			} else {
				it.New = folder + sep + name
			}
		case "serial":
			num := lastDigits(stem)
			if num == "" {
				serial++
				num = strconv.Itoa(serial)
			}
			// 序号宽度至少 3 位
			for len(num) < 3 {
				num = "0" + num
			}
			it.New = folder + "_" + num + ext
		}
		if it.New != name {
			// 撞名追加 _1..（与 UniquePath 同语义，但基于计划表内占用）
			if used[strings.ToLower(it.New)] {
				base := it.New[:len(it.New)-len(filepath.Ext(it.New))]
				ext := filepath.Ext(it.New)
				for i := 1; ; i++ {
					cand := base + "_" + strconv.Itoa(i) + ext
					if !used[strings.ToLower(cand)] {
						it.New = cand
						break
					}
					if i > 999 {
						it.Err = "可用序号耗尽"
						break
					}
				}
			}
		}
		if it.New != name && it.Err == "" {
			used[strings.ToLower(it.New)] = true
			preview.RenameCount++
		}
		preview.Items = append(preview.Items, it)
	}
	return preview, nil
}

// FolderRenamePreviewAPI 预览接口（HTTP 层调用，含路径校验）。
func (e *Engine) FolderRenamePreviewAPI(dir string, opt FolderRenameOptions) (*FolderRenamePreview, error) {
	if !e.ValidDirPath(dir) {
		return nil, errInvalidDir()
	}
	return folderRenamePlan(dir, opt)
}

// FolderRename 执行按文件夹名重命名：逐项校验（ValidPath/保护/存在）、
// 冲突追加、audit 可撤销、UpdatePath 同步索引。
func (e *Engine) FolderRename(ctx context.Context, dir string, opt FolderRenameOptions) (*FolderRenamePreview, error) {
	if !e.ValidDirPath(dir) {
		return nil, errInvalidDir()
	}
	preview, err := folderRenamePlan(dir, opt)
	if err != nil {
		return nil, err
	}
	unlock := e.lockWrites()
	defer unlock()
	// v3.10.10（第五轮 P1-3）：磁盘改名成功后，审计与索引写入不再依赖请求 ctx。
	wctx, cancel := detachCtx(ctx)
	defer cancel()
	taskID, _ := e.st.NewAuditTaskID(wctx)
	done := 0
	for i := range preview.Items {
		it := &preview.Items[i]
		if it.Skip || it.New == it.Old || it.Err != "" {
			continue
		}
		// 双保险：ValidPath + 保护目录（预览与执行之间文件可能被动过）
		if !e.ValidPath(it.Path) {
			it.Err = "文件不在受管卷内或路径无法解析"
			continue
		}
		if reason := e.protectedReasonResolved(it.Path); reason != "" {
			it.Err = reason
			continue
		}
		newPath := filepath.Join(filepath.Dir(it.Path), it.New)
		if _, err := os.Lstat(newPath); err == nil {
			it.Err = "目标名已存在：" + it.New
			continue
		}
		// v3.25.1：上面的 Lstat 只用于给出更具体的原因，最终仍用不覆盖改名兜底，
		// 防止检查到改名之间被外部进程抢占同名文件而静默覆盖。
		if err := renameNoOverwrite(it.Path, newPath); err != nil {
			it.Err = renameFailText(err)
			continue
		}
		if err := e.st.AddAudit(wctx, store.AuditEntry{TaskID: taskID, Operation: "folder_rename", OriginalPath: it.Path, NewPath: newPath}); err != nil {
			applog.Warn("改名已完成，但操作记录写入失败：%s → %s（%v）", it.Path, newPath, err)
		}
		// v3.10.8：同步 path/name/ext 并删旧路径的健康结果；
		// 只调 UpdatePath(newPath) 会留下旧路径的脏行，直到下次全量扫描才消失。
		if _, rerr := e.st.RenameFilePathsBatch(wctx, [][2]string{{it.Path, newPath}}); rerr != nil {
			applog.Warn("重命名已完成，但索引更新失败：%s（%v）", newPath, rerr)
		}
		if err := e.st.DropHealthByPath(wctx, it.Path); err != nil {
			applog.Warn("改名已完成，但旧路径健康记录删除失败：%s（%v）", it.Path, err)
		}
		it.Path = newPath
		done++
	}
	if done > 0 {
		preview.TaskID = taskID
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
		applog.Info("按文件夹名重命名：%s 完成 %d 项（任务 %d）", dir, done, taskID)
	}
	preview.RenameCount = done
	return preview, nil
}

func errInvalidDir() error {
	return &appErr{"目录不在受管卷内或路径无法解析"}
}

// appErr 简单错误类型（本文件本地用）。
type appErr struct{ msg string }

func (e *appErr) Error() string { return e.msg }

// UndoFolderRename 撤销按文件夹名重命名任务（audit 逆序回放）。
func (e *Engine) UndoFolderRename(ctx context.Context, taskID int64) (done, failed int, err error) {
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
		if a.Operation != "folder_rename" {
			continue
		}
		// v3.10.8（P0）：撤销前必须确认原路径没被占用。
		// os.Rename 在 Linux 上会静默覆盖目标，被覆盖的文件既不进隔离区也没有副本，
		// 这里统一走 undoRenameTarget，与繁简/组内重命名/规则引擎三条撤销路径同一口径。
		if reason := undoRenameTarget(a.NewPath, a.OriginalPath); reason != "" {
			failed++
			applog.Warn("撤销文件夹名重命名失败：%s → %s（%s）", a.NewPath, a.OriginalPath, reason)
			continue
		}
		// v3.10.8：撤销同样只回改路径字段，保住挂在 files.id 上的指纹与打标。
		if _, rerr := e.st.RenameFilePathsBatch(wctx, [][2]string{{a.NewPath, a.OriginalPath}}); rerr != nil {
			applog.Warn("撤销改名已完成，但索引更新失败：%s（%v）", a.OriginalPath, rerr)
		}
		if err := e.st.DropHealthByPath(wctx, a.NewPath); err != nil {
			applog.Warn("撤销改名已完成，但健康记录删除失败：%s（%v）", a.NewPath, err)
		}
		e.UpdatePath(filepath.Dir(a.OriginalPath))
		done++
	}
	if done > 0 {
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
	}
	return done, failed, nil
}
