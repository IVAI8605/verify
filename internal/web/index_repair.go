// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// indexRepairTimeout 是整轮索引体检的兜底时限：十几万行的 Lstat 在 NAS 上
// 走机械盘时可能超过几分钟，但绝不能无限占着请求。
const indexRepairTimeout = 10 * time.Minute

// handleIndexRepair 清理磁盘上已不存在的索引行（v3.10.12 P1-02）。
// POST /api/index/repair {"dry_run": true|false}；body 可为空，视为 false。
// 只删索引行，不动磁盘文件；删除后写一条 index_repair 操作记录。
func (s *Server) handleIndexRepair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	// 扫描收尾会批量 Upsert 索引；体检同时删行会与它互相覆盖，入口直接挡住。
	if s.scanBusy(w) {
		return
	}
	if s.e.IndexRepairRunning() {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "索引体检正在进行中，请等这次结束再操作"})
		return
	}
	if s.e.HealthRunning() {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "健康检测进行中，请等检测结束再操作"})
		return
	}
	var body struct {
		DryRun bool `json:"dry_run"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), indexRepairTimeout)
	defer cancel()
	res, err := s.e.RepairIndex(ctx, body.DryRun)
	if err != nil {
		// 体检自身的互斥失败（健康检测 / 扫描刚启动）属于「现在不能做」而不是服务端故障，
		// 回 409 让界面提示稍后重试，避免用户以为是索引坏了。
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "进行中") {
			status = http.StatusConflict
		}
		writeJSON(w, status, errorResponse{Error: "索引体检失败（" + err.Error() + "）"})
		return
	}
	writeJSON(w, http.StatusOK, res)
}
