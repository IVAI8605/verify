// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件是 v3.0.0 从 server.go 抽出的目录浏览授权逻辑（设计文档 §1.3）。
// browseRoots/authzRoots/browseShortcuts 决定「目录选择器能看到哪些根」，
// 是路径安全边界的一部分，单独成文件便于审计；逻辑本身未改动。

import (
	"path/filepath"

	"verify/internal/fsbrowse"
)

// browseRoots v1.41.7：访问白名单 = 显式配置目录 ∪ trimacl 授权探测目录。
// v1.41.5 曾误将探测回退（把"零授权仍可读"当作 trimacl 失效），实际是**卸载重装
// 会重置飞牛授权**导致观测被污染；恢复"可读=已授权"的探测口径。
func (s *Server) browseRoots() []string {
	return s.authzRoots()
}

// authzRoots 授权根集合：软件内配置 ∪ /fs 视图探测到的 trimacl 授权目录。
func (s *Server) authzRoots() []string {
	out := s.browseShortcuts()
	for _, r := range fsbrowse.DiscoverAuthorizedRoots() {
		dup := false
		for _, x := range out {
			if x == r {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, r)
		}
	}
	return out
}

// browseShortcuts v1.24.1：用户在应用内显式配置过的目录，作为目录选择器/目录树的
// 唯一入口来源——扫描根/保护/排除绝对路径/日志与缓存目录，空则兜底数据目录。
// v1.41.1 安全收紧：移除 v1.33 的"浏览成功自动常驻"——WebDAV 等全局可读挂载
// 会让未经授权的路径也能浏览成功并进入记录，违背"只显示授权目录"的预期；
// 授权子树请通过"添加扫描根/保护"显式保存，或每次粘贴路径进入。
func (s *Server) browseShortcuts() []string {
	cfg := s.cfg.Get()
	out := make([]string, 0, len(cfg.Volumes)+len(cfg.ProtectPaths)+len(cfg.ExcludeDirs)+len(cfg.BrowseRoots)+4)
	out = append(out, cfg.Volumes...)
	// v3.5.3：用户粘贴验证过的授权目录也作为入口（飞牛只授权子目录时无法自动枚举）
	out = append(out, cfg.BrowseRoots...)
	out = append(out, cfg.ProtectPaths...)
	// 排除目录可能是相对名（如 #recycle），只收绝对路径
	for _, d := range cfg.ExcludeDirs {
		if filepath.IsAbs(d) {
			out = append(out, d)
		}
	}
	if filepath.IsAbs(cfg.LogDir) {
		out = append(out, cfg.LogDir)
	}
	if filepath.IsAbs(cfg.CacheDir) {
		out = append(out, cfg.CacheDir)
	}
	return out
}
