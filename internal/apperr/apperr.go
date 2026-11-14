// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package apperr 把底层英文错误整理成“中文描述（英文原错误）”，供界面展示。
// 完整原始信息仍由调用方写入日志，界面只保留可读摘要。
package apperr

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"unicode"
)

const maxRaw = 200

// Format 返回中文为主的错误文案，括号内附英文原错误；纯中文描述保持原样。
func Format(err error) string {
	if err == nil {
		return ""
	}
	raw := strings.TrimSpace(err.Error())
	if raw == "" {
		return "操作失败"
	}
	cn := chineseSummary(err, raw)
	if cn == raw {
		return raw
	}
	return cn + "（" + truncate(raw, maxRaw) + "）"
}

func chineseSummary(err error, raw string) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "操作已取消"
	case errors.Is(err, fs.ErrNotExist):
		return "文件或目录不存在"
	case errors.Is(err, fs.ErrPermission):
		return "没有读取权限"
	case errors.Is(err, fs.ErrExist):
		return "文件或目录已存在"
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "database is locked"):
		return "数据库正忙"
	case strings.Contains(lower, "no such file or directory"):
		return "文件或目录不存在"
	case strings.Contains(lower, "permission denied"):
		return "没有读取权限"
	case strings.Contains(lower, "no space left on device"):
		return "存储空间不足"
	case strings.Contains(lower, "context canceled"):
		return "操作已取消"
	}
	if hasHan(raw) {
		return raw
	}
	return "操作失败"
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
