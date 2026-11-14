// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestNormalizePaths 验证路径列表规范化：去空白、去空项、去尾斜杠、去重、统一斜杠、折叠 ./..。
func TestNormalizePaths(t *testing.T) {
	in := []string{" /vol1/a/", "/vol1/a", "", "   ", "\\vol1\\b\\", "/vol1/a/"}
	got := NormalizePaths(in)
	want := []string{"/vol1/a", "/vol1/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizePaths = %v，期望 %v", got, want)
	}
	if got := NormalizePaths(nil); len(got) != 0 {
		t.Fatalf("空输入应返回空列表，实际 %v", got)
	}
	// v3.10.9：带 ../ 的手输路径必须折叠成真实路径，否则保护目录前缀匹配不到扫描结果。
	if got := NormalizePaths([]string{"/vol1/a/../b", "/vol1/./c/"}); !reflect.DeepEqual(got, []string{"/vol1/b", "/vol1/c"}) {
		t.Fatalf("未折叠 ./..：%v", got)
	}
}

// TestNormalizePathMapsLenient 验证读取旧配置时逐条跳过坏映射，而不是整表丢弃（P2-8）。
func TestNormalizePathMapsLenient(t *testing.T) {
	in := PathMaps{
		{Nas: "/vol1/media", Ext: `\\NAS\media`},
		{Nas: `Z:\bad`, Ext: `Z:\x`}, // NAS 侧没有前导 /，非法
		{Nas: "/vol2/b", Ext: ""},    // 外部路径为空，非法
	}
	if _, err := NormalizePathMaps(in); err == nil {
		t.Fatal("严格模式应报错")
	}
	got := NormalizePathMapsLenient(in)
	if len(got) != 1 || got[0].Nas != "/vol1/media" {
		t.Fatalf("宽松模式应只保留合法映射，实际 %v", got)
	}
}

// TestValidateConfigStrictRejectsUnknownRuleBase 验证 strict 先校验原始规则再规范化（P2-9）。
func TestValidateConfigStrictRejectsUnknownRuleBase(t *testing.T) {
	c := Config{SearchRules: &SearchRules{Base: "bogus"}}
	if err := validateConfig(&c, true); err == nil {
		t.Fatal("严格模式应拒绝未知 base")
	}
	c = Config{SearchRules: &SearchRules{Base: "bogus"}}
	if err := validateConfig(&c, false); err != nil {
		t.Fatalf("宽松模式不应报错：%v", err)
	}
	if c.SearchRules == nil || c.SearchRules.Base != RuleBaseAll {
		t.Fatalf("宽松模式应把未知 base 归一为 all，实际 %+v", c.SearchRules)
	}
}

// TestReplaceNormalizesProtectPaths 验证设置页写入的保护目录在落盘前被规范化。
func TestReplaceNormalizesProtectPaths(t *testing.T) {
	dir := t.TempDir()
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := m.Get()
	c.ProtectPaths = []string{filepath.ToSlash(filepath.Join(dir, "keep")) + "/", "", "/other/"}
	if err := m.Replace(c); err != nil {
		t.Fatal(err)
	}
	got := m.Get().ProtectPaths
	want := []string{filepath.ToSlash(filepath.Join(dir, "keep")), "/other"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("规范化结果 %v，期望 %v", got, want)
	}
}
