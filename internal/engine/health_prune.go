// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"verify/internal/applog"
	"verify/internal/health"
)

// HealthPruneResult 是一次「健康结论幽灵行复核」的结果（v4.2.0，2026-11-25 建议 5）。
//
// 背景：health_results 只按路径存结论，文件被外部工具移走/删除后旧结论不会自动消失，
// 健康页会一直列出点不开的条目，用户按它勾选批量操作只会一路失败。
// Removed 是清理掉的行数，Skipped 是拿不准（卷不在线/权限不足）而保留的行数。
type HealthPruneResult struct {
	Checked        int          `json:"checked"`
	Removed        int          `json:"removed"`
	Skipped        int          `json:"skipped"`
	SkippedVolumes []VolumeSkip `json:"skipped_volumes,omitempty"`
}

// PruneMissingHealth 复核某类（kind 为空表示全部）健康结论背后文件是否还存在，
// 把已经不存在的行删掉。只删派生数据，不碰磁盘上的任何文件。
//
// 与索引体检同一口径：卷不在线 / 卷根不可信时整卷跳过，避免把一块离线硬盘的
// 全部结论当成幽灵行删光——那会让用户重扫一遍（小时级）。
func (e *Engine) PruneMissingHealth(ctx context.Context, kind string) (HealthPruneResult, error) {
	var res HealthPruneResult
	if e.HealthRunning() {
		return res, errors.New("健康检测进行中，请等检测结束再复核")
	}
	unlock := e.lockWrites()
	defer unlock()

	vols := e.Volumes()
	type volState struct {
		usable bool
		reason string
		rows   int
	}
	states := map[string]*volState{}

	kinds := []string{kind}
	if kind == "" {
		kinds = nil
		for _, k := range health.AllKinds() {
			kinds = append(kinds, string(k))
		}
	}
	for _, k := range kinds {
		rows, err := e.st.AllHealthRows(ctx, k)
		if err != nil {
			return res, err
		}
		res.Checked += len(rows)
		var gone []int64
		for _, r := range rows {
			vol := volumeOf(r.Path, vols)
			if vol == "" {
				res.Skipped++
				continue
			}
			st := states[vol]
			if st == nil {
				usable, reason := volumeTrustFn(vol)
				st = &volState{usable: usable, reason: reason}
				states[vol] = st
			}
			if !st.usable {
				st.rows++
				res.Skipped++
				continue
			}
			fi, statErr := os.Lstat(r.Path)
			if statErr != nil {
				if errors.Is(statErr, fs.ErrNotExist) {
					gone = append(gone, r.ID)
				} else {
					res.Skipped++
				}
				continue
			}
			// 路径还在但已被换成别的文件（大小变了）：旧结论同样失效。
			if fi.Mode().IsRegular() && fi.Size() != r.Size {
				gone = append(gone, r.ID)
			}
		}
		if len(gone) > 0 {
			n, derr := e.st.DeleteHealthByIDs(ctx, gone)
			if derr != nil {
				return res, derr
			}
			res.Removed += int(n)
		}
	}
	for vol, st := range states {
		if !st.usable {
			res.SkippedVolumes = append(res.SkippedVolumes, VolumeSkip{Volume: vol, Rows: st.rows, Reason: st.reason})
		}
	}
	if res.Removed > 0 {
		applog.Info("健康结论复核：检查 %d 条，清理失效行 %d 条，跳过 %d 条", res.Checked, res.Removed, res.Skipped)
	}
	return res, nil
}
