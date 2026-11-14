// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"verify/internal/store"
)

// TestCollectGroupsRejectsUnknownMode v3.10.6 P0-3：
// 未知页面模式必须显式拒绝，不能静默降级成精确重复（导出会张冠李戴）。
func TestCollectGroupsRejectsUnknownMode(t *testing.T) {
	s := &Server{}
	for _, mode := range []string{"fuzzy", "semantic", "folder", "audio", "results", "bogus"} {
		if _, _, err := s.collectGroups(context.Background(), mode, store.GroupQuery{}, 10, 100); !errors.Is(err, errUnsupportedReportMode) {
			t.Fatalf("模式 %s 应被拒绝，实际 err=%v", mode, err)
		}
	}
}

// TestFilterGroupsBySize v3.10.6 P0-3：视频类分组补上大小筛选后，组员与统计同步更新。
func TestFilterGroupsBySize(t *testing.T) {
	groups := []store.Group{{
		Name: "g",
		Files: []store.FileRow{
			{Path: "/vol1/a.mp4", Size: 100},
			{Path: "/vol1/b.mp4", Size: 200},
			{Path: "/vol1/c.mp4", Size: 300},
		},
	}}
	out := filterGroupsBySize(groups, 150, 0)
	if len(out) != 1 {
		t.Fatalf("应保留 1 组，实际 %d", len(out))
	}
	if out[0].Count != 2 || out[0].TotalSize != 500 || out[0].Waste != 200 {
		t.Fatalf("组统计未按筛选重算: count=%d total=%d waste=%d", out[0].Count, out[0].TotalSize, out[0].Waste)
	}
	if got := filterGroupsBySize(groups, 1000, 0); len(got) != 0 {
		t.Fatalf("全部文件小于下限时应丢弃整组，实际 %d 组", len(got))
	}
}

// TestReportVideoExtList 覆盖「相同视频」页复用精确查重通道所需的扩展名清单。
func TestReportVideoExtList(t *testing.T) {
	list := reportVideoExtList()
	if len(list) == 0 {
		t.Fatalf("视频扩展名清单为空")
	}
	found := false
	for _, ext := range list {
		if ext == "mp4" {
			found = true
		}
	}
	if !found {
		t.Fatalf("视频扩展名清单缺少 mp4")
	}
}

// TestBuildReportHTML 验证报告对文件名/路径做 HTML 转义，并包含全标记警告。
func TestBuildReportHTML(t *testing.T) {
	s := &Server{}
	groups := []store.Group{{
		Name:      `<script>alert(1)</script>`,
		Count:     2,
		TotalSize: 200,
		Waste:     100,
		Files: []store.FileRow{
			{Name: `a<b>.txt`, Path: `/vol1/x/a<b>.txt`, Size: 100},
			{Name: `c>d.txt`, Path: `/vol1/x/c>d.txt`, Size: 100},
		},
	}}
	marked := map[string]bool{"/vol1/x/a<b>.txt": true, "/vol1/x/c>d.txt": true}
	var buf strings.Builder
	s.buildReportHTML(&buf, groups, marked, "exact", 0, false)
	html := buf.String()
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Fatalf("报告未转义分组名")
	}
	if !strings.Contains(html, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("报告缺少转义后的分组名")
	}
	if !strings.Contains(html, "危险") || !strings.Contains(html, "#1") {
		t.Fatalf("全标记警告缺失")
	}
	if !strings.Contains(html, "a&lt;b&gt;.txt") {
		t.Fatalf("报告未转义文件名")
	}
}

// TestReportThumbRespectsByteBudget v3.10.8 P1-6：
// 只限张数不限总字节时，大图库能把报告生成过程的内存打满；
// 超预算的缩略图必须退回占位，而不是照读不误。
func TestReportThumbRespectsByteBudget(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.jpg")
	if err := os.WriteFile(p, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	f := store.FileRow{Name: "big.jpg", Path: p}

	budget, left := 10, int64(50)
	if got := s.reportThumb(f, &budget, &left, time.Now().Add(time.Minute)); got != "" {
		t.Fatalf("超出字节预算仍生成了缩略图：len=%d", len(got))
	}

	budget, left = 10, int64(1000)
	if got := s.reportThumb(f, &budget, &left, time.Now().Add(time.Minute)); !strings.HasPrefix(got, "data:image/") {
		t.Fatalf("预算充足时应生成 data URI，实际 %q", got)
	}
	if left != 900 {
		t.Fatalf("预算扣减不正确：剩余 %d，期望 900", left)
	}
}
