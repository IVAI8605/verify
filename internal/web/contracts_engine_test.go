// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"testing"

	"verify/internal/engine"
)

// TestRelinkResultRespJSONShape 锁死硬链接合并响应的键集合。
//
// 这个接口以前直接回 engine.RelinkResult，gen_types.py 扫不到 engine 包，
// 改 json tag 后 Go 编译、门禁、tsc 全绿而前端静默拿到 undefined。
// 现在 tag 写在 contracts_engine.go 的具名 struct 上，这里再锁一层键集合：
// 谁改了 tag 或漏了字段，本测试直接红。
func TestRelinkResultRespJSONShape(t *testing.T) {
	got := jsonOf(t, relinkResultResp{
		Keep: "/vol1/a.mkv",
		Items: []relinkItemResp{
			{Path: "/vol1/b.mkv", OK: true, Reason: "重复文件"},
		},
		OKCount: 1,
		Bytes:   4096,
	})
	for _, key := range []string{"keep", "items", "ok_count", "bytes"} {
		if _, ok := got[key]; !ok {
			t.Errorf("响应缺少键 %q，实际: %v", key, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("响应应只有 4 个键，实际: %v", got)
	}
	items, ok := got["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items 结构不对: %v", got["items"])
	}
	item := items[0].(map[string]any)
	for _, key := range []string{"path", "ok", "reason"} {
		if _, ok := item[key]; !ok {
			t.Errorf("item 缺少键 %q，实际: %v", key, item)
		}
	}
}

// TestRelinkResultRespNilItems 保证空结果的 items 是 [] 而不是 null。
// 前端直接读 r.items.length，null 会炸；前端原有的 `|| []` 兜底已清理。
func TestRelinkResultRespNilItems(t *testing.T) {
	got := jsonOf(t, toRelinkResultResp(nil))
	items, ok := got["items"].([]any)
	if !ok {
		t.Fatalf("nil 结果的 items 必须是空数组，实际: %v", got["items"])
	}
	if len(items) != 0 {
		t.Fatalf("nil 结果的 items 应为空，实际 %d 条", len(items))
	}

	// 非空结果里 Reason 为空时省略（与原 engine.RelinkResult 的 omitempty 一致）。
	got = jsonOf(t, toRelinkResultResp(&engine.RelinkResult{
		Items: []engine.RelinkItem{{Path: "/vol1/b.mkv", OK: true}},
	}))
	item := got["items"].([]any)[0].(map[string]any)
	if _, exists := item["reason"]; exists {
		t.Errorf("reason 为空时应省略，实际: %v", item)
	}
}
