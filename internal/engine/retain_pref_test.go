// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 本文件是 v3.17.6 的偏好统计口径回归（F-22）：显式选择建议项应算采纳，
// 与 UserPrefStats 的 SQL 判定保持一致。

import (
	"context"
	"testing"

	"verify/internal/store"
)

func TestRefreshRetainProfileCountsExplicitAdopt(t *testing.T) {
	e, st, _ := newMoveDirEngine(t)
	ctx := context.Background()
	if err := st.RecordUserPref(ctx, []store.UserPrefEntry{
		// chosen 与 suggested 相同：用户明确点了建议项，应算采纳
		{GroupKey: "g1", Suggested: "/vol1/电影/a.mkv", Chosen: "/vol1/电影/a.mkv"},
		// chosen 与 suggested 不同：改选
		{GroupKey: "g2", Suggested: "/vol1/电影/b.mkv", Chosen: "/vol1/剧集/c.mkv"},
	}); err != nil {
		t.Fatal(err)
	}
	profile, err := e.RefreshRetainProfile(ctx, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Learned != 2 || profile.Adopt != 1 || profile.Changed != 1 {
		t.Fatalf("2 条记录应为采纳 1、改选 1，实际 %+v", profile)
	}
}
