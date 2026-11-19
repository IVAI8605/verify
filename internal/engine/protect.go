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

// protectedReasonResolved 是写操作专用的保护判定：同时检查原始路径「及其软链接
// 解析后的真实路径」是否落在保护目录内。
//
// v4.1.2：写入口此前用 ValidPath（Clean + EvalSymlinks + 卷前缀重判）判卷，却用
// 原始字符串判保护，两者口径不一致。卷内建一个软链接 /vol1/link → /vol1/protected，
// 传 /vol1/link/x 时：解析后在卷内（过卷校验），原始串不匹配保护根（过保护校验），
// 于是受保护文件被搬走/清理。保护配置路径本身也可能是软链接，因此两边都解析、双向
// 比对：只要任一组合命中就拦下。
//
// 必须与 ValidPath / Lstat 确认文件存在之后调用——EvalSymlinks 对不存在的路径会失败。
func (e *Engine) protectedReasonResolved(path string) string {
	if e.IsProtected(path) {
		return "受保护目录内的文件禁止清理"
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved == "" || filepath.Clean(resolved) == filepath.Clean(path) {
		return ""
	}
	// 真实路径命中保护根，或保护根自身是软链接时命中其解析结果 —— 都算受保护。
	if e.IsProtected(resolved) {
		return "受保护目录内的文件禁止清理"
	}
	for _, r := range e.cfg.Get().ProtectPaths {
		if real, rerr := filepath.EvalSymlinks(filepath.Clean(strings.TrimSpace(r))); rerr == nil && pathUnderAny(resolved, []string{real}) {
			return "受保护目录内的文件禁止清理"
		}
	}
	return ""
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
