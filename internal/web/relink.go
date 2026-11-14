// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"net/http"
)

type relinkReq struct {
	Keep  string   `json:"keep"`
	Paths []string `json:"paths"`
}

func (r *relinkReq) validate() string {
	if r.Keep == "" {
		return "缺少保留者路径"
	}
	if len(r.Paths) == 0 {
		return "缺少目标文件"
	}
	// v3.17.5（F-14）：单批最多 500 个目标。执行阶段要持写锁逐个建链替换，
	// 数量不设上限时一次请求就能把写锁占住几分钟，其它写操作全部排队。
	if len(r.Paths) > 500 {
		return "单次最多合并 500 个文件，请分批提交"
	}
	return ""
}

// handleRelinkPreview 对硬链接合并做预检（不产生任何变更）。
func (s *Server) handleRelinkPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var body relinkReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if msg := body.validate(); msg != "" {
		writeJSON(w, 400, map[string]any{"error": msg})
		return
	}
	writeJSON(w, 200, s.e.RelinkPreview(r.Context(), body.Keep, body.Paths))
}

// handleRelink 执行硬链接合并（设计文档 9.1：os.Link 临时文件 + rename 原子替换）。
func (s *Server) handleRelink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
		return
	}
	var body relinkReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"error": friendlyDecodeError(err)})
		return
	}
	if msg := body.validate(); msg != "" {
		writeJSON(w, 400, map[string]any{"error": msg})
		return
	}
	// v3.10.9：硬链接合并会替换磁盘上的文件，扫描进行中执行会让索引与磁盘不一致。
	if s.scanBusy(w) {
		return
	}
	res := s.e.RelinkFiles(r.Context(), body.Keep, body.Paths)
	if res == nil {
		writeJSON(w, 500, map[string]any{"error": "执行失败"})
		return
	}
	writeJSON(w, 200, res)
}
