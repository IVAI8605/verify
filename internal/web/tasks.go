// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"

	"verify/internal/taskguard"
)

// exclusiveTaskView 把当前占用独占槽的任务转成 /api/status 里的展示结构；
// 没有任务时返回 nil（JSON 里是 null），前端按「无独占任务」处理。
func exclusiveTaskView(s *Server) any {
	t, ok := s.e.BusyExclusiveTask()
	if !ok {
		return nil
	}
	group := "占用系统资源"
	switch t.Group {
	case taskguard.GroupIO:
		group = "占用磁盘"
	case taskguard.GroupFFmpeg:
		group = "占用解码/编码资源"
	}
	return map[string]any{
		"kind":       taskguard.KindName(t.Kind),
		"name":       t.Name,
		"group":      t.Group,
		"group_text": group,
		"started_at": t.StartedAt,
	}
}

// handleTasks 返回运行中的长任务与最近 10 条历史（v3.18.0）。
// 只读；任务由 engine 的 taskguard 登记，闸门关闭时两个数组都为空。
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	running, history := s.e.TaskSnapshot()
	writeJSON(w, 200, tasksResponse{Running: running, History: history})
}
