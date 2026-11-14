// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRunningRenameBatchAlias 覆盖 v3.11.0 的过渡字段：
// 新前端读 started_at/total，已发布的 3.10.13 前端仍读 created_at/files_count，
// 两套必须同时出现且值一致，跨版本混用才不会解析不到。
func TestRunningRenameBatchAlias(t *testing.T) {
	raw, err := json.Marshal(RunningRenameBatch{ID: 1, FilesCount: 7, CreatedAt: 1700000000, Total: 7, StartedAt: 1700000000})
	if err != nil {
		t.Fatalf("序列化批次失败：%v", err)
	}
	s := string(raw)
	for _, key := range []string{`"created_at":1700000000`, `"started_at":1700000000`, `"files_count":7`, `"total":7`} {
		if !strings.Contains(s, key) {
			t.Fatalf("缺少过渡字段 %s，实际 %s", key, s)
		}
	}
}
