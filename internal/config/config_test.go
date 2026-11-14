// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func hasPattern(list []string, want string) bool {
	for _, p := range list {
		if p == want {
			return true
		}
	}
	return false
}

// TestComputeDupFoldersDefaultTrue 默认要计算重复文件夹，保持 v1.x 行为。
func TestComputeDupFoldersDefaultTrue(t *testing.T) {
	if c := Default(t.TempDir()); !c.ComputeDupFolders {
		t.Fatal("默认应开启 compute_dup_folders")
	}
}

// TestLoadMissingComputeDupFoldersKeepsTrue 旧 config.json 没有该字段时保持开启。
func TestLoadMissingComputeDupFoldersKeepsTrue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"min_size":1024}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Get().ComputeDupFolders {
		t.Fatal("旧配置缺字段时应默认开启")
	}
}

// TestLoadExplicitFalseComputeDupFolders 用户显式关闭后加载不能被默认值覆盖。
func TestLoadExplicitFalseComputeDupFolders(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"compute_dup_folders":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Get().ComputeDupFolders {
		t.Fatal("显式 false 应保持关闭")
	}
}

// TestReplacePersistsComputeDupFoldersFalse 保存设置后必须落盘为 false。
func TestReplacePersistsComputeDupFoldersFalse(t *testing.T) {
	dir := t.TempDir()
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := m.Get()
	c.ComputeDupFolders = false
	if err := m.Replace(c); err != nil {
		t.Fatal(err)
	}
	if m.Get().ComputeDupFolders {
		t.Fatal("Replace 后内存配置应保持 false")
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if v, ok := raw["compute_dup_folders"]; !ok || v != false {
		t.Fatalf("config.json 应写入 compute_dup_folders=false，实际 %v（存在=%v）", v, ok)
	}
}

// TestMatchModeDefaultAll 默认沿用 v1.x 的多依据并存行为。
func TestMatchModeDefaultAll(t *testing.T) {
	if c := Default(t.TempDir()); c.MatchMode != MatchModeAll {
		t.Fatalf("默认匹配模式应为 all，实际 %q", c.MatchMode)
	}
}

// TestNormalizeMatchKinds 自定义组合只保留受支持类型并去重。
func TestNormalizeMatchKinds(t *testing.T) {
	in := []string{"exact", "bogus", " exact ", "video", "video", ""}
	got := NormalizeMatchKinds(in)
	want := []string{"exact", "video"}
	if len(got) != len(want) {
		t.Fatalf("归一化结果 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("归一化结果 = %v，期望 %v", got, want)
		}
	}
}

// TestCustomWithoutKindsFallsBackAll 自定义组合没勾任何类型时退回 all，避免结果页全空。
func TestCustomWithoutKindsFallsBackAll(t *testing.T) {
	m, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := m.Get()
	c.MatchMode = MatchModeCustom
	c.MatchKinds = nil
	if err := m.Replace(c); err != nil {
		t.Fatal(err)
	}
	if got := m.Get().MatchMode; got != MatchModeAll {
		t.Fatalf("自定义模式空组合应回退 all，实际 %q", got)
	}
}

// TestLoadMatchModeInvalidFallsBackAll 旧配置或手写坏值不能打开不存在的模式。
func TestLoadMatchModeInvalidFallsBackAll(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"match_mode":"bogus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Get().MatchMode; got != MatchModeAll {
		t.Fatalf("非法模式应回退 all，实际 %q", got)
	}
}

// TestReplacePersistsMatchMode 保存后内存与落盘都要保留用户选中的模式。
func TestReplacePersistsMatchMode(t *testing.T) {
	dir := t.TempDir()
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := m.Get()
	c.MatchMode = MatchModeSimilar
	if err := m.Replace(c); err != nil {
		t.Fatal(err)
	}
	if got := m.Get().MatchMode; got != MatchModeSimilar {
		t.Fatalf("内存匹配模式 = %q，want %q", got, MatchModeSimilar)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["match_mode"] != MatchModeSimilar {
		t.Fatalf("config.json 中 match_mode = %v，want %q", raw["match_mode"], MatchModeSimilar)
	}
}

// TestAudioWindowDefaultsAndNormalize 音频比较时长只接受 15/120/0，默认 15 秒。
func TestAudioWindowDefaultsAndNormalize(t *testing.T) {
	if c := Default(t.TempDir()); c.AudioWindowSeconds != 15 {
		t.Fatalf("默认音频比较时长 = %d，want 15", c.AudioWindowSeconds)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"audio_window_seconds":999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Get().AudioWindowSeconds; got != 15 {
		t.Fatalf("非法档位应回退 15，实际 %d", got)
	}
	c := m.Get()
	c.AudioWindowSeconds = 120
	if err := m.Replace(c); err != nil {
		t.Fatal(err)
	}
	if got := m.Get().AudioWindowSeconds; got != 120 {
		t.Fatalf("保存后档位 = %d，want 120", got)
	}
}

// TestEffectiveAudioThreshold 长指纹档要自动收紧阈值。
func TestEffectiveAudioThreshold(t *testing.T) {
	c := Default(t.TempDir())
	c.AudioThreshold = 0.82
	if got := c.EffectiveAudioThreshold(); got != 0.82 {
		t.Fatalf("15 秒档阈值 = %v，want 0.82", got)
	}
	c.AudioWindowSeconds = 120
	if got := c.EffectiveAudioThreshold(); got != 0.86 {
		t.Fatalf("2 分钟档阈值 = %v，want 0.86", got)
	}
	c.AudioWindowSeconds = 0
	if got := c.EffectiveAudioThreshold(); got != 0.88 {
		t.Fatalf("全片档阈值 = %v，want 0.88", got)
	}
}

func TestDefaultTempPatternsIncludeDSStore(t *testing.T) {
	c := Default(t.TempDir())
	if !hasPattern(c.TempPatterns, ".DS_Store") {
		t.Fatalf("默认临时清单缺 .DS_Store：%v", c.TempPatterns)
	}
}

// TestLoadMigratesLegacyDefaultTempPatterns 旧版本落盘的默认清单要自动补一项。
func TestLoadMigratesLegacyDefaultTempPatterns(t *testing.T) {
	dir := t.TempDir()
	b, err := json.Marshal(map[string]any{"temp_patterns": legacyDefaultTempPatterns})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPattern(m.Get().TempPatterns, ".DS_Store") {
		t.Fatalf("旧默认清单应在加载时补上 .DS_Store：%v", m.Get().TempPatterns)
	}
}

// TestLoadKeepsUserTempPatterns 用户手动增删过的清单不能被迁移逻辑改回去。
func TestLoadKeepsUserTempPatterns(t *testing.T) {
	dir := t.TempDir()
	b, err := json.Marshal(map[string]any{"temp_patterns": []string{".tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := m.Get().TempPatterns
	if len(got) != 1 || got[0] != ".tmp" {
		t.Fatalf("用户自定义清单不应被改写：%v", got)
	}
}

// TestLoadMissingTempPatternsUsesDefault 旧配置没有该字段时沿用新默认值。
func TestLoadMissingTempPatternsUsesDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPattern(m.Get().TempPatterns, ".DS_Store") {
		t.Fatalf("缺失字段时应使用新默认清单：%v", m.Get().TempPatterns)
	}
}
