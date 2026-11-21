// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"

	"verify/internal/engine"
)

// 本文件收口 server.go / metrics.go 直出结果的 HTTP 响应契约（隔离、恢复、删除、诊断、指标）。
//
// 背景（v4.0.0 契约规则，与 contracts_engine.go / contracts_engine_ops.go 同源）：
// handler 直接把 engine 的 *TrashResult / *RestoreResult、fsbrowse.Diagnose() 的 map，
// 或 metrics.go 内的 processMetrics 回给前端时，字段名真相源散落在 engine / fsbrowse / web 里，
// 而 tools/gen_types.py 只扫 contracts*.go。engine 或 fsbrowse 改一个键名：
// Go 编译通过、门禁全绿、前端静默拿到 undefined。
// 这里为每个响应定义具名契约 struct + 显式转换函数：
// 前者是前端字段名的唯一真相源，后者保证新增字段不会无意泄露到接口上。
//
// 两处已知生成器限制，在此说明而非偷偷退化：
//   - Reconcile 保留 *reconcileReportResp + omitempty（与 contracts_engine_ops.go 一致）：
//     改成值类型会让 encoding/json 不再省略 nil 分支，凭空多出一个键、改变 JSON 键集合；
//     tools/gen_types.py 对指针输出 TS any，但 Go 侧仍是具名契约 struct，形状未退化。
//   - fsbrowse.Diagnose() 的 probes / fs_view 元素是该包函数内的局部类型，
//     web 包无法按名字引用。转换时用 JSON 往返落到本文件的具名契约类型，
//     既保住原类型的 omitempty 形状，也不把 any 泄漏给前端。
// 其余嵌套结构（reconcile 的 item）复用 contracts_engine_ops.go 的具名契约类型。

// trashResultResp 是 /api/trash（移入隔离区）与 /api/trash/delete（永久删除）的响应。
// 两个接口共用 engine.TrashResult，键集合一一对应。
type trashResultResp struct {
	Moved       []string             `json:"moved"`
	Failed      []string             `json:"failed"`
	Errors      []string             `json:"errors"`
	Unprocessed int                  `json:"unprocessed,omitempty"`
	Conflict    string               `json:"conflict,omitempty"`
	Reconcile   *reconcileReportResp `json:"reconcile,omitempty"`
}

// toTrashResultResp 把 engine 结果转成契约 struct，nil 时返回带空集合的零值。
// Moved / Failed / Errors 一律给非 nil 空切片：前端直接读 .length，null 会炸。
func toTrashResultResp(src *engine.TrashResult) trashResultResp {
	out := trashResultResp{Moved: []string{}, Failed: []string{}, Errors: []string{}}
	if src == nil {
		return out
	}
	out.Moved = make([]string, 0, len(src.Moved))
	out.Moved = append(out.Moved, src.Moved...)
	out.Failed = make([]string, 0, len(src.Failed))
	out.Failed = append(out.Failed, src.Failed...)
	out.Errors = make([]string, 0, len(src.Errors))
	out.Errors = append(out.Errors, src.Errors...)
	out.Unprocessed, out.Conflict = src.Unprocessed, src.Conflict
	if src.Reconcile != nil {
		rc := toReconcileReportResp(src.Reconcile)
		out.Reconcile = &rc
	}
	return out
}

// restoreResultResp 是 /api/trash/restore 的响应（对应 engine.RestoreResult）。
type restoreResultResp struct {
	Restored    []string `json:"restored"`
	Failed      []string `json:"failed"`
	Errors      []string `json:"errors"`
	Unprocessed int      `json:"unprocessed,omitempty"`
	Conflict    string   `json:"conflict,omitempty"`
}

// toRestoreResultResp 把 engine 结果转成契约 struct，nil 时返回带空集合的零值。
// Restored / Failed / Errors 一律给非 nil 空切片：前端直接读 .length，null 会炸。
func toRestoreResultResp(src *engine.RestoreResult) restoreResultResp {
	out := restoreResultResp{Restored: []string{}, Failed: []string{}, Errors: []string{}}
	if src == nil {
		return out
	}
	out.Restored = make([]string, 0, len(src.Restored))
	out.Restored = append(out.Restored, src.Restored...)
	out.Failed = make([]string, 0, len(src.Failed))
	out.Failed = append(out.Failed, src.Failed...)
	out.Errors = make([]string, 0, len(src.Errors))
	out.Errors = append(out.Errors, src.Errors...)
	out.Unprocessed, out.Conflict = src.Unprocessed, src.Conflict
	return out
}

// metricsResp 是 GET /api/metrics 的响应。
// 与 agent.md 契约一致，只有 8 个键：pid / uptime_seconds / cpu_seconds_total /
// rss_bytes / io_read_bytes / io_write_bytes / goroutines / version。
// 把定义从 metrics.go 挪到 contracts*.go：生成器只扫后者，
// 留在 handler 文件里前端就拿不到 MetricsResp 这个类型。
type metricsResp struct {
	PID             int     `json:"pid"`
	UptimeSeconds   int64   `json:"uptime_seconds"`
	CPUSecondsTotal float64 `json:"cpu_seconds_total"`
	RSSBytes        uint64  `json:"rss_bytes"`
	IOReadBytes     uint64  `json:"io_read_bytes"`
	IOWriteBytes    uint64  `json:"io_write_bytes"`
	Goroutines      int     `json:"goroutines"`
	Version         string  `json:"version"`
}

// toMetricsResp 把 metrics.go 的采样结果转成契约 struct（值类型，无切片/地图字段）。
func toMetricsResp(src processMetrics) metricsResp {
	return metricsResp{
		PID:             src.PID,
		UptimeSeconds:   src.UptimeSeconds,
		CPUSecondsTotal: src.CPUSecondsTotal,
		RSSBytes:        src.RSSBytes,
		IOReadBytes:     src.IOReadBytes,
		IOWriteBytes:    src.IOWriteBytes,
		Goroutines:      src.Goroutines,
		Version:         src.Version,
	}
}

// diagProbeResp 是诊断里每个候选路径的探测结果（对应 fsbrowse.Diagnose 的局部 probe 类型）。
type diagProbeResp struct {
	Path  string `json:"path"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	FS    string `json:"fs,omitempty"`
}

// diagViewProbeResp 是 /fs 视图列子目录的探测结果（对应 fsbrowse.Diagnose 的局部 viewProbe 类型）。
type diagViewProbeResp struct {
	View  string `json:"view"`
	OK    bool   `json:"ok"`
	Count int    `json:"count"`
	Error string `json:"error,omitempty"`
}

// diagAuthzResp 是 GET /api/diag/authz 的响应（对应 fsbrowse.Diagnose 的 7 个键）。
type diagAuthzResp struct {
	FSUIDs      []string            `json:"fs_uids"`
	ShareNames  []string            `json:"share_names"`
	Probes      []diagProbeResp     `json:"probes"`
	Authorized  []string            `json:"authorized"`
	Discovered  []string            `json:"discovered"`
	FSView      []diagViewProbeResp `json:"fs_view"`
	DiscoverDir string              `json:"discover_dir"`
}

// toDiagAuthzResp 把 fsbrowse.Diagnose() 的 map 转成契约 struct。
// map 里每个键都显式取出并落到对应类型；未知键一律不进入响应。
// 所有切片字段给非 nil 空切片，前端直接读 .length 不会炸。
func toDiagAuthzResp(src map[string]any) diagAuthzResp {
	out := diagAuthzResp{
		FSUIDs: []string{}, ShareNames: []string{}, Probes: []diagProbeResp{},
		Authorized: []string{}, Discovered: []string{}, FSView: []diagViewProbeResp{},
	}
	if src == nil {
		return out
	}
	out.FSUIDs = diagStringSlice(src["fs_uids"])
	out.ShareNames = diagStringSlice(src["share_names"])
	out.Authorized = diagStringSlice(src["authorized"])
	out.Discovered = diagStringSlice(src["discovered"])
	out.Probes = diagStructSlice[diagProbeResp](src["probes"])
	out.FSView = diagStructSlice[diagViewProbeResp](src["fs_view"])
	out.DiscoverDir, _ = src["discover_dir"].(string)
	return out
}

// diagStringSlice 从 map 的 any 值里取字符串切片，nil/类型不符都给非 nil 空切片。
func diagStringSlice(v any) []string {
	xs, ok := v.([]string)
	if !ok || xs == nil {
		return []string{}
	}
	out := make([]string, 0, len(xs))
	return append(out, xs...)
}

// diagStructSlice 用 JSON 往返把 fsbrowse 的局部结构体切片落到本文件的具名契约类型。
// 原类型是函数内私有的，Go 侧无法按名字引用；往返同时保住原 struct 的 omitempty 形状。
func diagStructSlice[T any](v any) []T {
	out := []T{}
	if v == nil {
		return out
	}
	b, err := json.Marshal(v)
	if err != nil {
		return out
	}
	if err := json.Unmarshal(b, &out); err != nil || out == nil {
		return []T{}
	}
	return out
}
