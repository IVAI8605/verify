// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package video

import (
	"fmt"
	"testing"
	"verify/internal/store"
)

func nearAudio() []byte {
	b := make([]byte, Dim)
	b[0] = 100
	b[1] = 60
	return b
}

// TestFindGroupsSecondPassOnlyNearThreshold 审查 C-1 修复后语义：
// 主规则已接受的配对（音频强匹配 + thumbD 在阈值内）不再被二次校验「降级」排除，
// 也不再触发额外抽帧（calls=0）；二次校验只保留「晋级」语义（音频达标但缩略图
// 明显不符的配对，见 TestFindGroupsVerifyPromote 类用例）。
func TestFindGroupsSecondPassOnlyNearThreshold(t *testing.T) {
	audio := nearAudio()
	weak := []store.VideoFP{
		{ID: 1, Path: "/a.mp4", Name: "a.mp4", Size: 1000, Duration: 15, Audio: audio, Thumb: "0000000000000000"},
		{ID: 2, Path: "/b.mp4", Name: "b.mp4", Size: 1000, Duration: 15.01, Audio: audio, Thumb: fmt.Sprintf("%016x", uint64((1<<9)-1))},
	}
	calls := 0
	groups := FindGroups(weak, 0.82, 10, func(a, b store.VideoFP) bool {
		calls++
		return false
	})
	if len(groups) != 1 || calls != 0 {
		t.Fatalf("主规则已接受的配对不应被降级排除: groups=%d calls=%d", len(groups), calls)
	}

	calls = 0
	groups = FindGroups(weak, 0.82, 10, func(a, b store.VideoFP) bool {
		calls++
		return true
	})
	if len(groups) != 1 || calls != 0 {
		t.Fatalf("主规则已接受的配对应直接成组: groups=%d calls=%d", len(groups), calls)
	}

	strong := []store.VideoFP{
		{ID: 1, Path: "/a.mp4", Name: "a.mp4", Size: 1000, Duration: 15, Audio: audio, Thumb: "0000000000000000"},
		{ID: 2, Path: "/b.mp4", Name: "b.mp4", Size: 1000, Duration: 15.01, Audio: audio, Thumb: "0000000000000000"},
	}
	calls = 0
	groups = FindGroups(strong, 0.82, 10, func(a, b store.VideoFP) bool {
		calls++
		return false
	})
	if len(groups) != 1 || calls != 0 {
		t.Fatalf("强证据不应触发二次校验: groups=%d calls=%d", len(groups), calls)
	}
}

// TestFindGroupsAllowsHighThumbDiffWithStrongAudio 验证镜像/裁剪导致的 dHash 大差异
// 在音频强匹配时仍可进入二次校验，而不是被预筛丢弃。
func TestFindGroupsAllowsHighThumbDiffWithStrongAudio(t *testing.T) {
	audio := nearAudio()
	items := []store.VideoFP{
		{ID: 1, Path: "/a.mp4", Name: "a.mp4", Size: 1000, Duration: 15, Audio: audio, Thumb: "0000000000000000"},
		{ID: 2, Path: "/b.mp4", Name: "b.mp4", Size: 1000, Duration: 15.01, Audio: audio, Thumb: "ffffffffffffffff"},
	}
	calls := 0
	groups := FindGroups(items, 0.82, 10, func(a, b store.VideoFP) bool {
		calls++
		return true
	})
	if len(groups) != 1 || calls != 1 {
		t.Fatalf("音频强匹配应允许二次校验并成组: groups=%d calls=%d", len(groups), calls)
	}
}

// TestFindGroupsAcrossBuckets v3.10.6 P0-5：
// 分桶宽度必须覆盖 2.5 秒的时长容差。旧宽度 2.0 时，0.9s 与 3.3s 落在相隔两格的桶里，
// 这对同指纹视频永远不会被比较，属于漏组。
func TestFindGroupsAcrossBuckets(t *testing.T) {
	audio := nearAudio()
	items := []store.VideoFP{
		{ID: 1, Path: "/a.mp4", Name: "a.mp4", Size: 1000, Duration: 0.9, Audio: audio, Thumb: "0000000000000000"},
		{ID: 2, Path: "/b.mp4", Name: "b.mp4", Size: 1000, Duration: 3.3, Audio: audio, Thumb: "0000000000000000"},
	}
	groups := FindGroupsMode(items, 0.82, 10, nil, false)
	if len(groups) != 1 || len(groups[0].Files) != 2 {
		t.Fatalf("相差 2.4 秒的同指纹视频应归为一组，实际 %d 组", len(groups))
	}
}

// TestIsFullDuplicateNeedsMissingAudio v3.10.6 P0-4：
// 两侧都有整段指纹且缩略图不符时，不能用「大小相同 + 时长接近」推翻指纹判定；
// 只有至少一侧缺指纹时才保留原来的保守兜底。
func TestIsFullDuplicateNeedsMissingAudio(t *testing.T) {
	a := store.IntroFP{Size: 1000, Duration: 60, Audio: nearAudio(), Thumb: "0000000000000000"}
	b := store.IntroFP{Size: 1000, Duration: 60.4, Audio: nearAudio(), Thumb: "ffffffffffffffff"}
	if isFullDuplicate(a, b, 10, 0.82, 0) {
		t.Fatalf("指纹已判定不相似时不应由大小/时长兜底判为整段重复")
	}
	c := store.IntroFP{Size: 1000, Duration: 60, Thumb: "0000000000000000"}
	d := store.IntroFP{Size: 1000, Duration: 60.4, Thumb: "ffffffffffffffff"}
	if !isFullDuplicate(c, d, 10, 0.82, 0) {
		t.Fatalf("缺少整段指纹时应保留大小/时长兜底")
	}
}

// TestFindGroupsThumbOnly 验证仅缩略图模式（v2.8.0）：
// 缩略图相近但音频完全不匹配的配对，常规模式不成组，thumbOnly 模式成组。
func TestFindGroupsThumbOnly(t *testing.T) {
	a1 := make([]byte, Dim)
	a1[0] = 100
	a1[1] = 60
	// 音频与 a1 正交，余弦 0，远低于 0.82 门槛
	a2 := make([]byte, Dim)
	a2[2] = 100

	items := []store.VideoFP{
		{ID: 1, Path: "/a.mp4", Name: "a.mp4", Size: 1000, Duration: 15, Audio: a1, Thumb: "0000000000000000"},
		{ID: 2, Path: "/b.mp4", Name: "b.mp4", Size: 1000, Duration: 15.01, Audio: a2, Thumb: fmt.Sprintf("%016x", uint64((1<<9)-1))},
	}

	if g := FindGroups(items, 0.82, 10, nil); len(g) != 0 {
		t.Fatalf("常规模式下音频不匹配不应成组，实际 %d 组", len(g))
	}
	g := FindGroupsMode(items, 0.82, 10, nil, true)
	if len(g) != 1 {
		t.Fatalf("仅缩略图模式应成 1 组，实际 %d 组", len(g))
	}
}

// TestFindGroupsSilentVideo 回归 P1-1（v3.10.10）：无音轨视频只看画面，
// 多数帧一致时能成组。
func TestFindGroupsSilentVideo(t *testing.T) {
	thumbs := "0000000000000000,0000000000000000,0000000000000000"
	items := []store.VideoFP{
		{ID: 1, Path: "/a.mp4", Name: "a.mp4", Size: 1000, Duration: 15, Thumb: thumbs},
		{ID: 2, Path: "/b.mp4", Name: "b.mp4", Size: 1000, Duration: 15.01, Thumb: thumbs},
	}
	if g := FindGroups(items, 0.82, 10, nil); len(g) != 1 {
		t.Fatalf("无音轨但画面一致应成 1 组，实际 %d 组", len(g))
	}
}

// TestFindGroupsSilentVideoSharedIntroOnly 回归 P1-1：只有首帧（片头/台标）相同时
// 不得成组——放开无音轨查询后，这条是防止误组的关键。
func TestFindGroupsSilentVideoSharedIntroOnly(t *testing.T) {
	same := "0000000000000000,0000000000000000,0000000000000000"
	diff := "0000000000000000,ffffffffffffffff,ffffffffffffffff"
	items := []store.VideoFP{
		{ID: 1, Path: "/a.mp4", Name: "a.mp4", Size: 1000, Duration: 15, Thumb: same},
		{ID: 2, Path: "/b.mp4", Name: "b.mp4", Size: 1000, Duration: 15.01, Thumb: diff},
	}
	if g := FindGroups(items, 0.82, 10, nil); len(g) != 0 {
		t.Fatalf("仅首帧相同不应成组（任意一帧命中即算相似的旧口径），实际 %d 组", len(g))
	}
	if g := FindGroupsMode(items, 0.82, 10, nil, true); len(g) != 0 {
		t.Fatalf("仅缩略图模式同样要求多数帧一致，实际 %d 组", len(g))
	}
}

// TestFindIntroGroupsExcludesShortClip 验证整段短于片头窗口的文件不会与长视频成组。
func TestFindIntroGroupsExcludesShortClip(t *testing.T) {
	const thumb = "0000000000000001"
	items := []store.IntroFP{
		{ID: 1, Path: "/long-a.mp4", Name: "long-a.mp4", Size: 1000, Hash: "h1", Duration: 15, IntroThumb: thumb},
		{ID: 2, Path: "/long-b.mp4", Name: "long-b.mp4", Size: 600, Hash: "h2", Duration: 15, IntroThumb: thumb},
		{ID: 3, Path: "/short.mp4", Name: "short.mp4", Size: 100, Hash: "h3", Duration: 1.5, IntroThumb: thumb},
	}
	groups := FindIntroGroups(items, 10, 0.82, 10)
	if len(groups) != 1 || groups[0].Count != 2 {
		t.Fatalf("短视频不应加入长视频片头组: %+v", groups)
	}
	for _, f := range groups[0].Files {
		if f.ID == 3 {
			t.Fatalf("短视频不应出现在片头组: %+v", groups[0].Files)
		}
	}
}

// TestFindIntroGroupsKeepsRepresentativeAfterFullDuplicate 验证整段重复副本被剔除后，
// 仍保留一个代表参与“片头相同、正片不同”的分组。
func TestFindIntroGroupsKeepsRepresentativeAfterFullDuplicate(t *testing.T) {
	const thumb = "0000000000000001"
	items := []store.IntroFP{
		{ID: 1, Path: "/a.mp4", Name: "a.mp4", Size: 1000, Hash: "h1", Duration: 15, IntroThumb: thumb},
		{ID: 2, Path: "/b.mp4", Name: "b.mp4", Size: 1000, Hash: "h1", Duration: 15, IntroThumb: thumb},
		{ID: 3, Path: "/c.mp4", Name: "c.mp4", Size: 600, Hash: "h2", Duration: 15.01, IntroThumb: thumb},
		{ID: 4, Path: "/d.mp4", Name: "d.mp4", Size: 700, Hash: "h3", Duration: 15, IntroThumb: "ffffffffffffffff"},
	}

	groups := FindIntroGroups(items, 10, 0.82, 10)
	if len(groups) != 1 {
		t.Fatalf("期望 1 个片头分组，实际 %d 个: %+v", len(groups), groups)
	}
	g := groups[0]
	if g.Count != 2 {
		t.Fatalf("期望每组 2 个文件，实际 %d: %+v", g.Count, g.Files)
	}
	got := map[int64]bool{}
	for _, f := range g.Files {
		got[f.ID] = true
	}
	if !got[1] || !got[3] || got[2] || got[4] {
		t.Fatalf("分组文件不符合预期: %+v", g.Files)
	}
}
