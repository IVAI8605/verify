// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import "verify/internal/engine"

// 本文件收口 engine 结果类型的 HTTP 响应契约。
//
// 背景（v4.0.0 契约规则）：所有 writeJSON 的响应体必须来自 contracts*.go 的具名 struct，
// 因为字段名写在 struct tag 上编译器能查出来。此前 handler 直接把 engine 的
// *RelinkResult 等结构体回给前端，而 tools/gen_types.py 只扫 contracts*.go，
// 于是 engine 改一个 json tag：Go 编译通过、门禁全绿、前端静默拿到 undefined。
// 这里为每个 engine 结果类型定义一份契约 struct + 显式转换函数：
// 前者是前端字段名的唯一真相源，后者保证新增字段不会无意泄露到接口上。

// relinkItemResp 是硬链接合并结果里的单个目标（对应 engine.RelinkItem）。
type relinkItemResp struct {
	Path   string `json:"path"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// relinkResultResp 是 /api/relink 与 /api/relink/preview 的响应。
// 两个接口共用同一形状，与 engine.RelinkResult 的键集合一一对应。
type relinkResultResp struct {
	Keep    string           `json:"keep"`
	Items   []relinkItemResp `json:"items"`
	OKCount int              `json:"ok_count"`
	Bytes   int64            `json:"bytes"`
}

// toRelinkResultResp 把 engine 结果转成契约 struct。
// Items 一律给非 nil 空切片：前端直接读 r.items.length，null 会炸。
func toRelinkResultResp(src *engine.RelinkResult) relinkResultResp {
	out := relinkResultResp{Items: []relinkItemResp{}}
	if src == nil {
		return out
	}
	out.Keep, out.OKCount, out.Bytes = src.Keep, src.OKCount, src.Bytes
	out.Items = make([]relinkItemResp, 0, len(src.Items))
	for _, it := range src.Items {
		out.Items = append(out.Items, relinkItemResp{
			Path: it.Path, OK: it.OK, Reason: it.Reason,
		})
	}
	return out
}
