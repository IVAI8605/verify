// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"verify/internal/applog"
	"verify/internal/oplog"
	"verify/internal/taskguard"
)

// TranscodeResidue 是一个超过保留期的转码/剪除临时文件（v3.23.0）。
//
// transcodeResidueMinAge 是「残留」的最小年龄；预览与执行都用这个值，
// 执行侧必须自己复核（v4.1.2），不能只信前端传来的清单。
const transcodeResidueMinAge = 24 * time.Hour

type TranscodeResidue struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

// isTranscodeResidueName 判断文件名是否是转码/剪除中断留下的临时产物。
func isTranscodeResidueName(name string) bool {
	return strings.Contains(name, "_h265_tmp") || strings.Contains(name, "_trimmed_tmp")
}

// FindTranscodeResidue 在索引里的视频目录做一层只读扫描。
//
// 只扫目录本身、不递归；最多 maxDirs 个目录，避免大库启动/点击时读满磁盘。
// 命中 `_h265_tmp` 与 `_trimmed_tmp` 且修改时间早于 olderThan 的文件。
func (e *Engine) FindTranscodeResidue(ctx context.Context, olderThan time.Duration, maxDirs int) ([]TranscodeResidue, int, error) {
	if maxDirs <= 0 {
		maxDirs = 5000
	}
	paths, err := e.st.ActiveVideoPaths(ctx, maxDirs*4)
	if err != nil {
		return nil, 0, err
	}
	seen := make(map[string]bool, len(paths))
	cutoff := time.Now().Add(-olderThan)
	out := make([]TranscodeResidue, 0, 16)
	scanned := 0
	for _, p := range paths {
		if scanned >= maxDirs || ctx.Err() != nil {
			break
		}
		dir := filepath.Dir(p)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		scanned++
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			continue
		}
		for _, ent := range entries {
			name := ent.Name()
			if !isTranscodeResidueName(name) {
				continue
			}
			info, ierr := ent.Info()
			if ierr != nil || !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
				continue
			}
			out = append(out, TranscodeResidue{
				Path: filepath.Join(dir, name), Size: info.Size(), Mtime: info.ModTime().Unix(),
			})
		}
	}
	return out, scanned, nil
}

// CleanTranscodeResidue 把残留移入隔离区；只移动不删除，误判也能恢复。
//
// v3.25.0（P0-2）：原实现复用 trashFilesLocked，而它的 checkTrashPath 要求
// 文件在索引里。转码/剪除写到一半中断的 _tmp 文件从来不会入库，
// 所以这条清理路径对真正的残留必然逐条失败。这里改为独立的搬运流程：
// 不查索引、不依赖 hash，只接受残留命名的文件，隔离区记录照常写，
// 恢复入口与普通隔离完全一致。
func (e *Engine) CleanTranscodeResidue(ctx context.Context, paths []string) (*TrashResult, error) {
	if len(paths) == 0 {
		return &TrashResult{}, nil
	}
	// v3.25.0（P1-1/P2-12）：与扫描、转码、剪除互斥。特别是转码正在写临时文件时，
	// 这里必须被拒绝，不能把人家写到一半的产物搬进隔离区。
	done, err := e.acquireTask(taskguard.KindTrash, "清理转码残留", taskguard.GroupIO, taskguard.ResDiskIO)
	if err != nil {
		return nil, err
	}
	defer done("done")
	start := time.Now()
	unlock := e.lockWrites()
	defer unlock()

	res := &TrashResult{}
	snap := e.beginOpSnapshot(ctx, "transcode_residue", paths)
	now := time.Now()
	for _, p := range paths {
		p = filepath.Clean(p)
		if !isTranscodeResidueName(filepath.Base(p)) {
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, "不是转码/剪除临时文件")
			snap.Skip(p, "不是转码/剪除临时文件")
			continue
		}
		if !e.ValidPath(p) || strings.Contains(filepath.ToSlash(p), "/.verify-trash/") {
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, "路径不在受管存储卷内")
			snap.Skip(p, "路径不在受管存储卷内")
			continue
		}
		if reason := e.protectedReasonResolved(p); reason != "" {
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, reason)
			snap.Skip(p, reason)
			continue
		}
		fi, statErr := os.Lstat(p)
		if statErr != nil || !fi.Mode().IsRegular() {
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, "文件已不存在或不是普通文件")
			snap.Skip(p, "文件已不存在或不是普通文件")
			continue
		}
		// v4.1.2：服务端执行前必须复核 24 小时阈值。预览清单可能过期，或调用方
		// 直接 POST 任意满足命名规则的路径——刚写好、正在被别的进程使用的临时文件
		// 若被搬进隔离区，等于把人家的数据挪走。mtime 未满阈值的一律拒绝。
		if time.Since(fi.ModTime()) < transcodeResidueMinAge {
			reason := "临时文件未满 24 小时，拒绝清理（可能仍在使用）"
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, reason)
			snap.Skip(p, reason)
			continue
		}
		vol := e.VolumeOf(p)
		dst, berr := e.buildTrashPath(vol, p, now)
		if berr != nil {
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, berr.Error())
			snap.Skip(p, berr.Error())
			continue
		}
		if merr := moveFile(p, dst); merr != nil {
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, merr.Error())
			snap.Skip(p, merr.Error())
			continue
		}
		// 残留没有 hash 与分组，记录里留空；reason 单独标记，便于隔离区按来源筛选。
		if aerr := e.st.AddTrash(ctx, p, dst, vol, "", "", "transcode_residue", fi.Size()); aerr != nil {
			// v4.1.2：复用统一回滚，原父目录已不存在时不重建（见 rollbackTrashRecord）。
			restored, note := e.rollbackTrashRecord(snap, p, dst, aerr)
			var msg string
			if restored {
				msg = "记录隔离区失败，已回滚：" + aerr.Error()
			} else {
				msg = "记录隔离区失败，" + note
			}
			res.Failed = append(res.Failed, p)
			res.Errors = append(res.Errors, msg)
			continue
		}
		// 实时监控有可能把临时文件也索引进去，顺手清掉，避免留下幽灵行。
		e.dropIndexPath(ctx, snap, p)
		snap.MarkMoved(p, dst, opExpectRelocated)
		res.Moved = append(res.Moved, p)
	}

	entry := oplog.New("trash_add")
	entry.Summary = fmt.Sprintf("清理转码/剪除残留 %d 个，失败 %d 个", len(res.Moved), len(res.Failed))
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
	if err := e.recordOp(ctx, entry); err != nil {
		applog.Warn("清理转码残留已完成，但操作记录写入失败：%v", err)
	}
	e.bumpVersion()
	e.Bus.Publish(Event{Type: "update"})
	res.Reconcile = e.finishOpSnapshot(snap)
	return res, nil
}
