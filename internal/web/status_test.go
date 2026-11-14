// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"reflect"
	"strings"
	"testing"

	"verify/internal/config"
)

// 设置页表单按 statusConfigView 的键回显，键名与结构体 json tag 不一致的在此登记。
var statusKeyAliases = map[string]string{
	"broken_auto_idle_minutes": "broken_idle_minutes",
	"broken_auto_batch":        "broken_batch",
}

// 明确不向前端下发的字段：内部路径、监听地址、目录浏览历史、访问令牌。
var statusKeySkip = map[string]bool{
	"addr":         true,
	"data_dir":     true,
	"browse_roots": true,
	"access_token": true,
}

// TestStatusConfigViewCoversConfig 防止配置结构体新增字段后忘记加进 /api/status：
// 漏字段会让设置页回显默认值，保存时再把真实配置覆盖掉（v3.7.0 的 op_log_* 即此问题）。
func TestStatusConfigViewCoversConfig(t *testing.T) {
	view := statusConfigView(config.Default(t.TempDir()))
	typ := reflect.TypeOf(config.Config{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" || statusKeySkip[tag] {
			continue
		}
		key := tag
		if alias, ok := statusKeyAliases[tag]; ok {
			key = alias
		}
		if _, ok := view[key]; !ok {
			t.Errorf("配置字段 %s（json:%s）未出现在 /api/status 的 config 里，键名 %s", f.Name, tag, key)
		}
	}
}

// TestStatusConfigViewValues 确认下发的值取自当前配置，而不是零值。
func TestStatusConfigViewValues(t *testing.T) {
	cfg := config.Default(t.TempDir())
	cfg.OpLogEnabled = false
	cfg.OpLogRetentionDays = 30
	cfg.OpLogReads = true
	cfg.OpLogDetailLimit = 500
	cfg.MemBudgetMB = 2048
	cfg.ThumbCacheMaxMB = 512

	view := statusConfigView(cfg)
	cases := map[string]any{
		"op_log_enabled":        false,
		"op_log_retention_days": 30,
		"op_log_reads":          true,
		"op_log_detail_limit":   500,
		"mem_budget_mb":         2048,
		"thumb_cache_max_mb":    512,
	}
	for k, want := range cases {
		if got := view[k]; got != want {
			t.Errorf("%s 下发值 %v，期望 %v", k, got, want)
		}
	}
}
