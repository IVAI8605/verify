// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
	"time"
)

// TestVideoCandidatesUsesVideoAt 回归 BUG-01（v1.42.0）：
// 无音轨视频分析成功后 audio 仍是 NULL，旧条件 `audio IS NULL` 会把它当成
// 未分析，导致全量扫描无限重试同一批文件。候选判定必须以 video_at 为准。
func TestVideoCandidatesUsesVideoAt(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	files := []struct{ path, ext string }{
		{"/v/new.mp4", "mp4"},
		{"/v/noaudio.mkv", "mkv"},
		{"/v/broken.avi", "avi"},
	}
	ids := make([]int64, len(files))
	for i, f := range files {
		id, err := s.UpsertOne(ctx, FileInfo{
			Path: f.path, Volume: "/v", Name: f.path, Ext: f.ext,
			Size: 10, Mtime: 1, Ctime: 1,
		}, 1)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}

	now := time.Now().Unix()
	// 第二个文件模拟"无音轨但已分析"：audio 为空，video_at 有值。
	if _, err := s.DB().Exec(`UPDATE files SET video_at=123 WHERE id=?`, ids[1]); err != nil {
		t.Fatal(err)
	}
	// 第三个模拟分析失败：v3.10.4 起失败后有 7 天冷却期，冷却期内不应重复选中。
	if _, err := s.DB().Exec(`UPDATE files SET video_at=?, video_error='解码失败' WHERE id=?`, now, ids[2]); err != nil {
		t.Fatal(err)
	}

	total, err := s.CountVideoCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("只有真正未分析的文件应成为候选，实际 %d", total)
	}
	cands, err := s.VideoCandidates(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].ID != ids[0] {
		t.Fatalf("候选应为 new.mp4（id=%d），实际 %+v", ids[0], cands)
	}

	// 模拟无音轨视频成功落库：Audio 为 nil，但 SetVideoBatch 必须写 video_at。
	// v3.10.4：批量写带 size/mtime 乐观条件，测试必须传入算哈希时读到的大小与时间。
	if err := s.SetVideoBatch(ctx, []VideoResult{{ID: ids[0], Size: 10, Mtime: 1, Thumb: "aa,bb", Duration: 1.5}}); err != nil {
		t.Fatal(err)
	}
	total, err = s.CountVideoCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("无音轨文件成功落库后不应再成为候选，实际 %d（BUG-01 回潮）", total)
	}
	if ok, err := s.HasVideoState(ctx, ids[0]); err != nil || !ok {
		t.Fatalf("无音轨文件应被视为已分析，实际 ok=%v err=%v", ok, err)
	}

	// v3.10.4：失败超过冷却期后重新成为候选，避免一次 ffmpeg 失败永久缺指纹。
	if _, err := s.DB().Exec(`UPDATE files SET video_at=? WHERE id=?`, now-int64(videoRetryAfter.Seconds())-10, ids[2]); err != nil {
		t.Fatal(err)
	}
	total, err = s.CountVideoCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("失败文件冷却期过后应重新成为候选，实际 %d", total)
	}
}
