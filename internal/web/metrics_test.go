// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件回归 v3.26.0 F3：GET /api/metrics 返回进程资源指标，且 CPU 计数单调不减。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestMetricsEndpointFields 校验字段齐全、PID 与版本号正确、协程数为正。
func TestMetricsEndpointFields(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("应返回 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	var m processMetrics
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if m.PID != os.Getpid() {
		t.Fatalf("pid 应为 %d，实际 %d", os.Getpid(), m.PID)
	}
	if m.Version != Version {
		t.Fatalf("version 应为 %s，实际 %s", Version, m.Version)
	}
	if m.Goroutines < 1 {
		t.Fatalf("goroutines 应大于 0，实际 %d", m.Goroutines)
	}
	if m.UptimeSeconds < 0 {
		t.Fatalf("uptime_seconds 不能为负，实际 %d", m.UptimeSeconds)
	}
}

// TestMetricsCPUNotDecreasing 连续采样时 CPU 累计时间只能前进或持平。
func TestMetricsCPUNotDecreasing(t *testing.T) {
	first := collectProcessMetrics().CPUSecondsTotal
	deadline := time.Now().Add(120 * time.Millisecond)
	var spin uint64
	for time.Now().Before(deadline) {
		spin++
	}
	_ = spin
	second := collectProcessMetrics().CPUSecondsTotal
	if second < first {
		t.Fatalf("cpu_seconds_total 不应回退：先 %f 后 %f", first, second)
	}
}

// TestMetricsRejectsPost 指标接口只读，写方法一律 405。
func TestMetricsRejectsPost(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest(http.MethodPost, "/api/metrics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 应返回 405，实际 %d", rec.Code)
	}
}
