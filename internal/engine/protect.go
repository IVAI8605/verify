// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	gopath "path"
	"path/filepath"
	"strings"
	"verify/internal/applog"
	"verify/internal/config"
)

// pathUnderAny 判断 path 是否位于 roots 中任一目录内（含目录本身）。
// 排除目录与保护目录共用同一套前缀语义，避免两处行为漂移。
// v3.10.9：配置项也做一次 Clean。旧库或手改 config.json 里可能留着
// `/vol1/a/../b` 这类非规范路径，不折叠就永远匹配不上，保护会静默失效。
func pathUnderAny(path string, roots []string) bool {
	p := filepath.ToSlash(path)
	for _, raw := range roots {
		r := strings.TrimSpace(raw)
		if r == "" {
			continue
		}
		r = strings.TrimRight(gopath.Clean(filepath.ToSlash(r)), "/")
		if r != "" && r != "." && (p == r || strings.HasPrefix(p, r+"/")) {
			return true
		}
	}
	return false
}

// IsProtected 判断路径是否受保护：用户配置的保护目录，或隔离区自身。
// 隔离区永远视为受保护——把隔离区里的文件再"清理"一遍没有任何意义。
func (e *Engine) IsProtected(path string) bool {
	if strings.Contains(filepath.ToSlash(path), "/.verify-trash/") {
		return true
	}
	return pathUnderAny(path, e.cfg.Get().ProtectPaths)
}

// protectedReason 返回拦截原因；空串表示不受保护、可以清理。
func (e *Engine) protectedReason(path string) string {
	if !e.IsProtected(path) {
		return ""
	}
	return "受保护目录内的文件禁止清理"
}

// SetProtectPaths 由设置页写入保护目录；规范化后落盘并记日志。
// v3.9.10 备注：当前全项目零调用，设置页改保护目录走的是通用配置保存；
// 保留是为了将来把「保护目录」单独落盘并记录审计时复用这套规范化逻辑。
// 返回错误时配置不变。
func (e *Engine) SetProtectPaths(paths []string) error {
	c := e.cfg.Get()
	c.ProtectPaths = config.NormalizePaths(paths)
	if err := e.cfg.Replace(c); err != nil {
		return err
	}
	applog.Info("保护目录已更新：共 %d 个", len(c.ProtectPaths))
	return nil
}
