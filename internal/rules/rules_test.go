// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package rules

import (
	"context"
	"testing"

	"verify/internal/config"
	"verify/internal/store"
)

func mkFile(path, name string, size int64, hash string, mtime int64) store.FileRow {
	ext := ""
	if i := len(name) - 1; i > 0 {
		for j := i; j >= 0; j-- {
			if name[j] == '.' {
				ext = name[j+1:]
				break
			}
		}
	}
	return store.FileRow{Path: path, Name: name, Size: size, Hash: hash, Mtime: mtime, Ext: ext, Volume: "/vol1", Inode: int64(len(path))}
}

func rulesOf(base string, attrs ...string) *config.SearchRules {
	return &config.SearchRules{Base: base, Attrs: attrs, NameSimilarPct: 90, FolderNameMode: "full", FolderNameDepth: 1}
}

func groupNames(groups []store.Group) map[string]int {
	out := map[string]int{}
	for _, g := range groups {
		for _, f := range g.Files {
			out[f.Name]++
		}
	}
	return out
}

// TestSizeOnly：忽略内容 + 相同文件大小，必须按大小成组，且同名不同大小不能合并。
func TestSizeOnly(t *testing.T) {
	files := []store.FileRow{
		mkFile("/vol1/a/one.bin", "one.bin", 100, "", 0),
		mkFile("/vol1/b/two.bin", "two.bin", 100, "", 0),
		mkFile("/vol1/c/three.bin", "three.bin", 200, "", 0),
	}
	groups, _ := Build(context.Background(), files, rulesOf(config.RuleBaseSize), []string{"/vol1"})
	if len(groups) != 1 || groups[0].Count != 2 {
		t.Fatalf("按大小应只成 1 组 2 个文件，实际 %+v", groups)
	}
	for _, f := range groups[0].Files {
		if f.Size != 100 {
			t.Fatalf("组内混入了不同大小的文件：%+v", f)
		}
	}
}

// TestNameAndSize：同名 + 同大小才算一组；同名不同大小要分开。
func TestNameAndSize(t *testing.T) {
	files := []store.FileRow{
		mkFile("/vol1/a/x.mkv", "x.mkv", 100, "", 0),
		mkFile("/vol1/b/x.mkv", "x.mkv", 100, "", 0),
		mkFile("/vol1/c/x.mkv", "x.mkv", 200, "", 0),
	}
	groups, _ := Build(context.Background(), files, rulesOf(config.RuleBaseAttributes, config.RuleAttrName, config.RuleAttrSize), []string{"/vol1"})
	if len(groups) != 1 || groups[0].Count != 2 {
		t.Fatalf("同名同大小应成 1 组 2 个，实际 %+v", groups)
	}
	names := groupNames(groups)
	if names["x.mkv"] != 2 {
		t.Fatalf("组内文件数不对：%+v", names)
	}
}

// TestContentAndExt：同内容但扩展名不同不能成组。
func TestContentAndExt(t *testing.T) {
	files := []store.FileRow{
		mkFile("/vol1/a/x.mkv", "x.mkv", 100, "h1", 0),
		mkFile("/vol1/b/y.mp4", "y.mp4", 100, "h1", 0),
		mkFile("/vol1/c/z.mkv", "z.mkv", 100, "h1", 0),
	}
	groups, _ := Build(context.Background(), files, rulesOf(config.RuleBaseContent, config.RuleAttrExt), []string{"/vol1"})
	if len(groups) != 1 || groups[0].Count != 2 {
		t.Fatalf("同 hash 同扩展名应成 1 组 2 个，实际 %+v", groups)
	}
	for _, f := range groups[0].Files {
		if f.Ext != "mkv" {
			t.Fatalf("扩展名条件没有生效：%+v", f)
		}
	}
}

// TestSizeTolerance：容差内的大小组可合并，但链式区间不能被吞成一组。
func TestSizeTolerance(t *testing.T) {
	files := []store.FileRow{
		mkFile("/vol1/a", "a", 100, "", 0),
		mkFile("/vol1/b", "b", 110, "", 0),
		mkFile("/vol1/c", "c", 1200, "", 0),
	}
	r := rulesOf(config.RuleBaseSize)
	r.SizeToleranceKB = 0 // 0 表示精确；100KB 容差下 100 与 110 才成组
	groups, _ := Build(context.Background(), files, r, []string{"/vol1"})
	if len(groups) != 0 {
		t.Fatalf("精确大小不应成组，实际 %+v", groups)
	}
	r.SizeToleranceKB = 1 // 1KB = 1024 字节
	groups, _ = Build(context.Background(), files, r, []string{"/vol1"})
	if len(groups) != 1 || groups[0].Count != 2 {
		t.Fatalf("1KB 容差下 100/110 应成 1 组，实际 %+v", groups)
	}
}

// TestIgnoreCopyNames：忽略“副本/xx(1)”文件名。
func TestIgnoreCopyNames(t *testing.T) {
	files := []store.FileRow{
		mkFile("/vol1/a/x.mkv", "x.mkv", 100, "h1", 0),
		mkFile("/vol1/b/x 副本.mkv", "x 副本.mkv", 100, "h1", 0),
	}
	r := rulesOf(config.RuleBaseContent)
	r.IgnoreCopyNames = true
	r.CopyPatterns = []string{"副本", "(1)"}
	groups, _ := Build(context.Background(), files, r, []string{"/vol1"})
	if len(groups) != 0 {
		t.Fatalf("带“副本”的文件应被忽略，实际 %+v", groups)
	}
}

// TestIgnoreSameFolder：整组同目录丢弃，跨目录保留。
func TestIgnoreSameFolder(t *testing.T) {
	same := []store.FileRow{
		mkFile("/vol1/a/x.bin", "x.bin", 100, "", 0),
		mkFile("/vol1/a/y.bin", "y.bin", 100, "", 0),
	}
	r := rulesOf(config.RuleBaseSize)
	r.IgnoreSameFolder = true
	if groups, _ := Build(context.Background(), same, r, []string{"/vol1"}); len(groups) != 0 {
		t.Fatalf("同目录组应被忽略，实际 %+v", groups)
	}
	cross := []store.FileRow{
		mkFile("/vol1/a/x.bin", "x.bin", 100, "", 0),
		mkFile("/vol1/b/y.bin", "y.bin", 100, "", 0),
	}
	if groups, _ := Build(context.Background(), cross, r, []string{"/vol1"}); len(groups) != 1 {
		t.Fatalf("跨目录组应保留，实际 %+v", groups)
	}
}

// TestFolderNameModes：完整名 / 顶部深度 / 搜索根三种口径。
func TestFolderNameModes(t *testing.T) {
	files := []store.FileRow{
		mkFile("/vol1/root/A/x/1.mkv", "1.mkv", 100, "", 0),
		mkFile("/vol1/root/A/y/1.mkv", "1.mkv", 100, "", 0),
	}
	r := rulesOf(config.RuleBaseAttributes, config.RuleAttrFolderName)
	r.FolderNameMode = "full"
	if groups, _ := Build(context.Background(), files, r, []string{"/vol1/root"}); len(groups) != 0 {
		t.Fatalf("完整文件夹名 x/y 不同不应成组，实际 %+v", groups)
	}
	r.FolderNameMode = "depth"
	r.FolderNameDepth = 1
	if groups, _ := Build(context.Background(), files, r, []string{"/vol1/root"}); len(groups) != 1 {
		t.Fatalf("第 1 层同为 A，应成组；实际 %+v", groups)
	}
}

// TestSimilarity：相似度百分比计算。
func TestSimilarity(t *testing.T) {
	if Similarity("abc", "abc") != 100 {
		t.Fatal("相同字符串相似度应为 100")
	}
	if got := Similarity("abcd", "abce"); got != 75 {
		t.Fatalf("abcd/abce 期望 75，实际 %d", got)
	}
}
