// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package health

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// FindEmptyDirs 后序遍历找出空目录树（v1.14.0 引入 10.2）：
// 目录自身无文件、且所有子目录都是空目录或被跳过的重要目录 → 命中。
// skip 返回 true 的目录视为「重要内容」：包含它的父目录不判空，
// 避免把 .verify-trash、@eaDir、保护目录随父目录一起清掉。
// 命中列表按发现顺序返回（父在子前——后序回溯时父目录最后确认）。
func FindEmptyDirs(ctx context.Context, roots []string, skip func(path string) bool) ([]string, error) {
	var out []string
	for _, root := range roots {
		if skip(root) {
			continue
		}
		if _, err := os.Stat(root); err != nil {
			continue
		}
		// 根目录自身不参与命中（卷根永远不该被清理），只递归子树
		_, _ = walkEmptyDirs(ctx, root, skip, &out)
	}
	return out, nil
}

// walkEmptyDirs 返回 dir 是否为空树；命中时把 dir 追加到 out。
func walkEmptyDirs(ctx context.Context, dir string, skip func(string) bool, out *[]string) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// 读不了（权限等）不算空——宁可漏报
		return false, err
	}
	isEmpty := true
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if !e.IsDir() {
			// 非常规条目（软链接等）按内容对待：一律视为非空，宁漏勿误
			isEmpty = false
			continue
		}
		if skip(p) {
			// 重要子目录：父目录视为非空（不能把它一起带走）
			isEmpty = false
			continue
		}
		empty, err := walkEmptyDirs(ctx, p, skip, out)
		if err != nil {
			isEmpty = false
			continue
		}
		if !empty {
			isEmpty = false
		}
	}
	if isEmpty {
		*out = append(*out, dir)
		return true, nil
	}
	return false, nil
}

// IsTrashDirName 判断目录名是否属于隔离区目录（.verify-trash 前缀系列）。
func IsTrashDirName(name string) bool {
	return strings.HasPrefix(name, ".verify-trash")
}
