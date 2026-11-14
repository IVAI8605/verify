// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"net/http"
	"time"

	"verify/internal/applog"
)

// handleUnfinishedOpSnapshots 返回未完成写操作快照的只读对账（v3.10.10，第五轮 P1-1）。
// 只做 Lstat 判断与统计，不改动任何文件；没有未完成 run 时返回空数组。
func (s *Server) handleUnfinishedOpSnapshots(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	// 对账要逐条 Lstat，给一个独立上限，避免超大快照拖住请求
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	runs, err := s.e.ReconcileUnfinishedSnapshots(ctx, 5, 2000)
	if err != nil {
		applog.Warn("读取未完成快照失败：%v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "读取未完成快照失败（" + err.Error() + "）"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}
