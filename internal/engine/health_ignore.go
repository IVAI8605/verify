// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"os"
	"path/filepath"

	"verify/internal/applog"
	"verify/internal/store"
)

// IgnoreHealthFiles 把用户判定为「误报」的健康条目记进忽略表（v4.2.0）。
//
// 只记录「这个文件的这份内容不再上报」，不做任何磁盘改动：
//   - 同一路径再次标记时以最新 size+mtime 覆盖；
//   - 文件被换掉（大小或修改时间变了）后忽略自动失效，不会被永久掩盖；
//   - 记录后立刻删掉当前的健康结论，页面刷新即可看到它消失。
func (e *Engine) IgnoreHealthFiles(ctx context.Context, kind string, paths []string) *HealthOpResult {
	res := &HealthOpResult{Items: []HealthOpItem{}}
	if len(paths) == 0 {
		return res
	}
	unlock := e.lockWrites()
	defer unlock()
	for _, p := range paths {
		p = filepath.Clean(p)
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			res.Failed++
			res.Items = append(res.Items, HealthOpItem{Path: p, Error: "文件不存在或不是普通文件，无法标记"})
			continue
		}
		if err := e.st.AddHealthIgnore(ctx, store.HealthIgnore{
			Path: p, Kind: kind, Size: fi.Size(), Mtime: fi.ModTime().Unix(), Reason: "user",
		}); err != nil {
			res.Failed++
			res.Items = append(res.Items, HealthOpItem{Path: p, Error: "写入忽略标记失败：" + err.Error()})
			continue
		}
		if derr := e.st.DeleteHealthResult(ctx, kind, p); derr != nil {
			applog.Warn("标记误报后删除健康结论失败（%s）：%v", p, derr)
		}
		res.Done++
		res.Items = append(res.Items, HealthOpItem{Path: p})
	}
	if res.Done > 0 {
		e.pushHealth()
		applog.Info("已标记 %d 条健康结论为误报（不再上报）", res.Done)
	}
	return res
}

// UnignoreHealth 取消若干路径的误报忽略标记。返回取消的条数。
func (e *Engine) UnignoreHealth(ctx context.Context, paths []string) (int64, error) {
	unlock := e.lockWrites()
	defer unlock()
	return e.st.RemoveHealthIgnore(ctx, paths)
}

// filterIgnoredPaths 过滤掉「用户标为误报且文件内容未变」的候选路径。
//
// 逐条 Lstat 只发生在命中忽略表的路径上：忽略表通常只有几条，绝大多数文件
// 只查一次 map 就过，不会给扫描增加可见开销。
func (e *Engine) filterIgnoredPaths(ctx context.Context, paths []string) []string {
	m, err := e.st.HealthIgnoreMap(ctx)
	if err != nil || len(m) == 0 {
		return paths
	}
	out := paths[:0]
	for _, p := range paths {
		if it, ok := m[p]; ok {
			if fi, serr := os.Lstat(p); serr == nil && fi.Size() == it.Size && fi.ModTime().Unix() == it.Mtime {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}
