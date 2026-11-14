// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestAudioCandidatesSQL 回归 BUG-02（v1.42.0）：
// SQL 字符串里曾经混入 Go 注释，SQLite 报 near "/" 语法错误，
// 候选统计错误又被 `total, _ :=` 吞掉，重复音频自 v1.19 起静默不运行。
func TestAudioCandidatesSQL(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	files := []struct{ path, ext string }{
		{"/v/a.mp3", "mp3"},
		{"/v/b.flac", "flac"},
		{"/v/c.txt", "txt"},
	}
	for _, f := range files {
		if _, err := s.UpsertOne(ctx, FileInfo{
			Path: f.path, Volume: "/v", Name: f.path, Ext: f.ext,
			Size: 10, Mtime: 1, Ctime: 1,
		}, 1); err != nil {
			t.Fatal(err)
		}
	}

	total, err := s.CountAudioCandidates(ctx)
	if err != nil {
		t.Fatalf("统计音频候选不应报错（BUG-02 回潮）：%v", err)
	}
	if total != 2 {
		t.Fatalf("音频候选应为 2（mp3 + flac），实际 %d", total)
	}

	cands, err := s.AudioCandidates(ctx, 10)
	if err != nil {
		t.Fatalf("拉取音频候选不应报错（BUG-02 回潮）：%v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("音频候选应为 2，实际 %d", len(cands))
	}

	// 写入指纹后该文件不再进入候选。
	if err := s.SetAudioBatch(ctx, []AudioResult{{ID: cands[0].ID, Audio: []byte{1, 2, 3}}}); err != nil {
		t.Fatal(err)
	}
	left, err := s.CountAudioCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatalf("写入指纹后应剩 1 个候选，实际 %d", left)
	}
}

// TestAudioGroupsDurationTolerance 回归 P2-7（v3.10.10）：
// 指纹只取开头一段频谱，恒定频谱素材（测试音/白噪声/底噪）的 Cosine 会接近 1，
// 没有时长约束时不同用途的文件会被并组。要求指纹相似 **且** 时长差 ≤2.5 秒。
func TestAudioGroupsDurationTolerance(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	var ids []int64
	for _, p := range []string{"/v/tone-a.mp3", "/v/tone-b.mp3"} {
		id, err := s.UpsertOne(ctx, FileInfo{
			Path: p, Volume: "/v", Name: p, Ext: "mp3",
			Size: 100, Mtime: 1, Ctime: 1,
		}, 1)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// 完全相同的指纹字节，Cosine = 1；先用相差 90 秒的时长写库。
	same := []byte{9, 9, 9, 9}
	if err := s.SetAudioBatch(ctx, []AudioResult{
		{ID: ids[0], Audio: same, Duration: 10},
		{ID: ids[1], Audio: same, Duration: 100},
	}); err != nil {
		t.Fatal(err)
	}
	groups, _, err := s.AudioGroups(ctx, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("时长相差 90 秒不应成组，实际 %d 组", len(groups))
	}

	// 时长拉近到容差内应恢复成组，确认不是把音频分组整体关掉。
	if err := s.SetAudioBatch(ctx, []AudioResult{{ID: ids[1], Audio: same, Duration: 11}}); err != nil {
		t.Fatal(err)
	}
	groups, _, err = s.AudioGroups(ctx, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Count != 2 {
		t.Fatalf("时长差 1 秒应成 1 组 2 个文件，实际 %d 组", len(groups))
	}
}
