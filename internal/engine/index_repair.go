// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"verify/internal/applog"
	"verify/internal/oplog"
	"verify/internal/taskguard"
)

// VolumeSkip 记录一个被整体跳过的卷：卷不可信时该卷下所有行都不判幽灵（v3.10.13 P0）。
type VolumeSkip struct {
	Volume string `json:"volume"`
	Rows   int    `json:"rows"`
	Reason string `json:"reason"`
}

// IndexRepairResult 是一次索引体检的结果（v3.10.12 P1-02）。
// Skipped 是「拿不准」的行数：权限不足、卷不可信等，这类行一律保留，宁可留着也不误删。
// WouldRemove 只在 dry_run 时有值，表示「如果真跑会清理多少行」，避免预览恒为 0。
type IndexRepairResult struct {
	Checked        int          `json:"checked"`
	Removed        int          `json:"removed"`
	WouldRemove    int          `json:"would_remove"`
	Skipped        int          `json:"skipped"`
	SkippedVolumes []VolumeSkip `json:"skipped_volumes,omitempty"`
	DryRun         bool         `json:"dry_run"`
}

// indexRepairBatch 是每批巡检的索引行数：2000 个 Lstat 约几十毫秒，
// 批内再分块提交，避免十几万行攒成一个大事务。
const indexRepairBatch = 2000

// RepairIndex 巡检 files 表：磁盘上已经不存在的行（改名撤销 bug 的遗留）
// 连同健康结论一起清掉。只删索引行，不碰磁盘上的任何文件。
//
// dryRun=true 时只统计不删，给界面一个「先看看有多少」的入口。
//
// v3.10.13（P0）：不再用「路径前缀命中配置卷」代替在线判断。卷根不可访问、
// 文件系统状态读不出、或顶层卷根与上级同设备（疑似未挂载）时，整卷一律跳过，
// 否则一块离线硬盘的所有索引会被当成幽灵行删光，重扫是小时到天级代价。
func (e *Engine) RepairIndex(ctx context.Context, dryRun bool) (IndexRepairResult, error) {
	start := time.Now()
	res := IndexRepairResult{DryRun: dryRun}
	// 先立标志再查其他任务：健康检测 / 扫描启动时都会检查该标志，
	// 反过来的顺序会留下「两边都以为对方没跑」的窗口。
	if !e.indexRepair.CompareAndSwap(false, true) {
		return res, fmt.Errorf("索引体检已在进行中")
	}
	defer e.indexRepair.Store(false)
	// v3.18.0：体检独占磁盘索引，与扫描 / 转码 / 健康检测互斥。
	doneGuard, gerr := e.acquireTask(taskguard.KindIndexRepair, "", taskguard.GroupIO,
		taskguard.ResDiskIO, taskguard.ResDBWrite)
	if gerr != nil {
		return res, gerr
	}
	defer doneGuard("done")
	if e.HealthRunning() {
		return res, fmt.Errorf("健康检测进行中，请等检测结束再操作")
	}
	if e.IsRunning() {
		return res, fmt.Errorf("扫描进行中，请等扫描结束再操作")
	}
	// 体检期间禁止任何改文件 / 改索引的写操作：否则会删掉「磁盘已改名、索引还没 flush」
	// 窗口里的旧行，让新路径失去索引（v3.10.13 P1）。
	unlock := e.lockWrites()
	defer unlock()

	vols := e.Volumes()
	states := map[string]*volumeSkipState{}
	var after int64
	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		batch, err := e.st.ListFilePathsAfter(ctx, after, indexRepairBatch)
		if err != nil {
			return res, fmt.Errorf("读取索引行失败（%v）", err)
		}
		if len(batch) == 0 {
			break
		}
		after = batch[len(batch)-1].ID
		res.Checked += len(batch)

		ghosts := make([]string, 0, len(batch))
		for _, row := range batch {
			vol := volumeOf(row.Path, vols)
			if vol == "" {
				// 不在任何受管卷内：判定不了是「文件没了」还是「盘没挂」，
				// 宁可留着索引行，等卷回来再体检。
				res.Skipped++
				continue
			}
			st := states[vol]
			if st == nil {
				usable, reason := volumeTrustFn(vol)
				st = &volumeSkipState{usable: usable, reason: reason}
				states[vol] = st
			}
			if !st.usable {
				// 整卷不可信：该卷下的行全部保留。只按卷计数，逐行跳过不单独报原因。
				st.rows++
				res.Skipped++
				continue
			}
			if _, statErr := os.Lstat(row.Path); statErr != nil {
				if errors.Is(statErr, fs.ErrNotExist) {
					ghosts = append(ghosts, row.Path)
				} else {
					res.Skipped++
				}
			}
		}
		if dryRun {
			res.WouldRemove += len(ghosts)
		} else if len(ghosts) > 0 {
			n, derr := e.st.DeleteGhostPaths(ctx, ghosts)
			if derr != nil {
				return res, fmt.Errorf("清理幽灵索引行失败（%v）", derr)
			}
			res.Removed += int(n)
		}
		if len(batch) < indexRepairBatch {
			break
		}
	}
	for vol, st := range states {
		if st.usable {
			continue
		}
		res.SkippedVolumes = append(res.SkippedVolumes, VolumeSkip{Volume: vol, Rows: st.rows, Reason: st.reason})
		applog.Warn("索引体检：卷 %s 不可信（%s），已跳过 %d 行", vol, st.reason, st.rows)
	}

	if !dryRun && res.Removed > 0 {
		entry := oplog.New("index_repair")
		entry.Summary = fmt.Sprintf("清理幽灵索引行 %d 条（巡检 %d 条，跳过 %d 条无法判断的行）", res.Removed, res.Checked, res.Skipped)
		entry.TargetCount = res.Removed
		entry.DurationMS = time.Since(start).Milliseconds()
		if err := e.recordOp(ctx, entry); err != nil {
			// 审计失败不阻断：删掉的只是索引行，磁盘文件没有变化；
			// 但仍要留一条错误日志，避免「没人知道清理发生过」。
			applog.Warn("索引体检结果写入操作记录失败：%v", err)
		}
		applog.Info("索引体检：巡检 %d 条，清理幽灵行 %d 条，跳过 %d 条，耗时 %s",
			res.Checked, res.Removed, res.Skipped, time.Since(start).Round(time.Millisecond))
	}
	return res, nil
}

// volumeSkipState 是单卷的可信度缓存：同一轮体检里一个卷只探测一次。
type volumeSkipState struct {
	usable bool
	reason string
	rows   int
}

// volumeTrustFn 是卷可信度探测入口。抽成变量是为了让用例能模拟
// 「挂载点还在、但整卷已经不可读」这类无法在临时目录里复现的状态。
var volumeTrustFn = volumeTrust

// volumeTrust 判断卷根是否可信；不可信时返回中文原因（v3.10.13 P0）。
//
// 只做「卷根能不能代表整卷在线」这一件事：卷根不在 / 不是目录 / statfs 失败 /
// 顶层卷根与上级同设备，任一命中就认为该卷下所有 ENOENT 都不可作为删除依据。
func volumeTrust(root string) (bool, string) {
	fi, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, "卷根不存在，可能未挂载"
		}
		return false, "卷根不可访问（" + err.Error() + "）"
	}
	if !fi.IsDir() {
		return false, "卷根不是目录"
	}
	// statfs 失败说明文件系统层已不可用，此时 Lstat 的 ENOENT 不能当作「文件被删」。
	var sf syscall.Statfs_t
	if err := syscall.Statfs(root, &sf); err != nil {
		return false, "无法读取文件系统状态（" + err.Error() + "）"
	}
	if mounted, checked := isTopLevelMount(root, fi); checked && !mounted {
		return false, "卷根与上级目录在同一设备，疑似未挂载"
	}
	return true, ""
}

// isTopLevelMount 只对挂在文件系统根下的卷（/vol1 这类）做挂载点判定。
//
// 扫描根允许配置成子目录（如 /vol1/media），这类目录与父目录同设备是正常状态；
// 测试用临时目录同理。若对所有卷都要求「不同设备」，这些合法配置会被整卷跳过，
// 体检功能等于失效，所以只对父目录是 / 的顶层卷启用该判定。
func isTopLevelMount(root string, fi os.FileInfo) (mounted, checked bool) {
	parent := filepath.Dir(root)
	if parent == root || parent != string(os.PathSeparator) {
		return false, false
	}
	pfi, err := os.Stat(parent)
	if err != nil {
		return false, false
	}
	rdev, rok := deviceOf(fi)
	pdev, pok := deviceOf(pfi)
	if !rok || !pok {
		return false, false
	}
	return rdev != pdev, true
}

// deviceOf 取文件的 st_dev；取不到时让调用方放弃挂载点判定，而不是猜一个结论。
func deviceOf(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}
