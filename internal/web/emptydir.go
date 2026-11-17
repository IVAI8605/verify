// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"net/http"
)

// handleEmptyDirClean 批量清理空目录（v1.14.0 引入 10.2）：
// 深度降序 + 执行前逐项重验（存在/仍空/非保护），整目录移入隔离区。
func (s *Server) handleEmptyDirClean(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	// v3.10.8（P1-1）：清理空目录会移走目录，扫描进行中执行会让索引与磁盘错位
	if s.scanBusy(w) {
		return
	}
	var body struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	if len(body.Paths) == 0 {
		writeJSON(w, 400, errorResponse{Error: "缺少目录列表"})
		return
	}
	res := s.e.EmptyDirClean(r.Context(), body.Paths)
	// v3.25.2（P3-4）：闸门拒绝时一个目录都没动，按 409 回给前端，
	// 免得界面把「被拒绝」显示成「0 个成功」。
	if res.Conflict != "" {
		writeJSON(w, http.StatusConflict, errorResponse{Error: res.Conflict})
		return
	}
	writeJSON(w, 200, res)
}
