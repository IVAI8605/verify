// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// handleOpSnapshots GET /api/op-snapshots — 历史写操作快照与对账结果（v3.11.0）。
//
// 只读接口：只查 op_snapshot_run / op_snapshot_item，不触发任何文件操作。
// 支持 page、page_size、kind、result（all/ok/mismatch）、keyword（匹配原路径与新路径）。
// 每个快照最多返回 200 条明细，差异项优先，避免大操作把响应撑爆。
func (s *Server) handleOpSnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	page := int(intParam(r, "page", 1, 1, 1<<20))
	pageSize := int(intParam(r, "page_size", 20, 1, 100))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	result := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("result")))
	switch result {
	case "ok", "mismatch":
	default:
		result = "all"
	}
	keyword := strings.TrimSpace(r.URL.Query().Get("keyword"))

	total, list, err := s.st.ListOpSnapshots(ctx, page, pageSize, kind, result, keyword, 200)
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: "读取操作快照失败：" + err.Error()})
		return
	}
	snapshots := make([]map[string]any, 0, len(list))
	for _, it := range list {
		items := make([]map[string]any, 0, len(it.Items))
		for _, x := range it.Items {
			items = append(items, map[string]any{
				"path":     x.Path,
				"new_path": x.FinalPath,
				"state":    x.State,
				"error":    x.Detail,
			})
		}
		snapshots = append(snapshots, map[string]any{
			"id":         it.ID,
			"kind":       it.Kind,
			"created_at": it.StartedAt,
			// finished_at 固定契约（v3.11.1 定稿）：Unix 秒整数，字段始终返回，
			// 未完成（进程崩溃/断电）时为 0，不用 null、也不省略；界面判 finished_at === 0 显示「未完成」。
			// 这类记录不会出现在 result=ok 的成功列表里（第十轮 P1）。
			"finished_at": it.FinishedAt,
			"checked":     it.Checked,
			"mismatched":  it.Mismatched,
			"diff": map[string]int{
				"still_exists":  it.Diff.StillExists,
				"missing":       it.Diff.Missing,
				"changed":       it.Diff.Changed,
				"path_mismatch": it.Diff.PathMismatch,
				"record_lost":   it.Diff.RecordLost,
			},
			"items": items,
			// v3.13.0：明细是否被截断由后端判定，前端不再用「条数 == 200」猜测
			"items_truncated": it.ItemsTruncated,
		})
	}
	writeJSON(w, 200, opSnapshotsResponse{Total: total, Page: page, PageSize: pageSize, Snapshots: snapshots})
}
