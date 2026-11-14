// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package config

import "testing"

// TestValidateConfigRejectsNonVolumeRoot 回归 v3.17.3 F-02：
// 扫描根必须是 /vol<数字> 下的路径；设置页保存（strict）直接报错，
// 旧配置加载（宽松）只丢坏条目，不因一条脏数据拒绝整份配置。
func TestValidateConfigRejectsNonVolumeRoot(t *testing.T) {
	c := Config{Volumes: []string{"/etc"}}
	if err := validateConfig(&c, true); err == nil {
		t.Fatal("严格模式应拒绝 /etc 作为扫描根")
	}
	c = Config{Volumes: []string{"/etc", "/vol1/1000/media"}}
	if err := validateConfig(&c, true); err == nil {
		t.Fatal("混入非法扫描根时严格模式应整次拒绝")
	}
	c = Config{Volumes: []string{"/etc", "  /vol2/media  "}}
	if err := validateConfig(&c, false); err != nil {
		t.Fatalf("宽松模式不应报错：%v", err)
	}
	if len(c.Volumes) != 1 || c.Volumes[0] != "/vol2/media" {
		t.Fatalf("宽松模式应只保留合法卷根，实际 %v", c.Volumes)
	}
}

// TestValidateConfigKeepsEmptyVolumes 空列表表示自动发现 /vol*，必须保持合法。
func TestValidateConfigKeepsEmptyVolumes(t *testing.T) {
	c := Config{}
	if err := validateConfig(&c, true); err != nil {
		t.Fatalf("空扫描根应合法：%v", err)
	}
	if c.Volumes != nil {
		t.Fatalf("空扫描根不应被改写，实际 %v", c.Volumes)
	}
}
