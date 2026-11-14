// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"unicode/utf8"

	"verify/internal/applog"
	"verify/internal/health"
)

// FixInvalidEncodingNames 修复库里所有非 UTF-8 文件名（v3.12.0）。
//
// 为什么不接受前端传路径：这类路径经 JSON 编码后，原始字节已被替换成 U+FFFD，
// 前端手里只有替换后的字符串，回传必然对不上磁盘上的文件（写入口护栏也会直接 400）。
// 所以由服务端从索引里自己找出候选，再复用 RenameHealthFiles 的
// 计划 → 改名 → 审计 → 快照 → 索引同步链路，目标名也由服务端重新计算。
func (e *Engine) FixInvalidEncodingNames(ctx context.Context, opt health.NameOptions) *HealthOpResult {
	var targets []string
	var after int64
	for ctx.Err() == nil {
		rows, err := e.st.ListActiveFilePathsAfter(ctx, after, 5000)
		if err != nil {
			return &HealthOpResult{
				Failed: 1,
				Items:  []HealthOpItem{{Error: "读取文件索引失败: " + err.Error()}},
			}
		}
		for _, r := range rows {
			after = r.ID
			if !utf8.ValidString(r.Path) {
				targets = append(targets, r.Path)
			}
		}
		if len(rows) < 5000 {
			break
		}
	}
	if len(targets) == 0 {
		applog.Info("修复编码异常文件名：未发现需要处理的文件")
		return &HealthOpResult{Items: []HealthOpItem{}}
	}
	applog.Info("修复编码异常文件名：共 %d 个候选", len(targets))
	return e.RenameHealthFiles(ctx, health.KindName, targets, opt, false)
}
