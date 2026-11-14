// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPublicErrorText 覆盖 SEC-8 的收敛口径：中文保留、英文底层错误转摘要、
// 内部细节（SQLite / 源码位置 / 纯英文）一律不回显。
func TestPublicErrorText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"中文原样保留", "分组数量超限（最多 200 组）", "分组数量超限（最多 200 组）"},
		{"保留中文前缀", "移入隔离区失败：permission denied", "移入隔离区失败：没有权限访问该文件或目录"},
		{"纯英文 errno", "no such file or directory", "文件或目录已不存在"},
		{"SQLite 原文", "SQL logic error: no such table: files (1)", "数据表缺失，请升级应用或重新初始化数据"},
		{"数据库表锁", "database table is locked: op_log", "数据库表正忙，请稍后重试"},
		{"源码位置", "open /vol1/x: panic: runtime error", "操作失败，请查看服务端日志"},
		{"无法归类的英文", "something went wrong", "操作失败，请查看服务端日志"},
		{"空错误", "   ", "操作失败，请查看服务端日志"},
	}
	for _, c := range cases {
		if got := publicErrorText(c.in); got != c.want {
			t.Errorf("%s：publicErrorText(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}

// TestWriteJSONSanitizesError 确认收敛只作用于失败响应，且不丢其它字段。
func TestWriteJSONSanitizesError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, 500, map[string]any{"error": "删除失败：input/output error", "n": 3})

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if got := body["error"]; got != "删除失败：磁盘读写出错，请检查存储设备" {
		t.Errorf("error 字段未收敛，实际 %v", got)
	}
	if got := body["n"]; got != float64(3) {
		t.Errorf("附加字段被丢弃，实际 %v", got)
	}

	// 成功响应不参与收敛
	rec = httptest.NewRecorder()
	writeJSON(rec, 200, map[string]any{"error": "permission denied"})
	if !strings.Contains(rec.Body.String(), "permission denied") {
		t.Errorf("成功响应不应被改写，实际 %s", rec.Body.String())
	}
}
