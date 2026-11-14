// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package volpath 集中判定飞牛存储卷根路径（/vol<数字>）。
//
// v3.17.3（F-02）：此前 fsbrowse、web、config 各写一份前缀判断，
// 口径不一致时扫描根可被配置成 /etc 这类任意目录，路径防线整体失效。
// 统一到这一处后，目录浏览、设置保存与配置加载用同一条规则。
//
// 这里用 path 而不是 filepath：判定对象是 NAS 上的 Linux 路径，
// 在 Windows 上开发时 filepath.Clean 会把 "/vol1/x" 变成 "\vol1\x"，
// 让同一条规则在两端得出不同结论。
package volpath

import (
	"path"
	"strings"
)

// IsVolRoot 判断路径是否以 /vol<数字> 开头（其后是 "/" 或结束）。
// "/volumes" 这类同前缀路径不算卷根（v3.10.10 P2-15 的边界口径）。
func IsVolRoot(p string) bool {
	if !strings.HasPrefix(p, "/vol") {
		return false
	}
	rest := p[len("/vol"):]
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	return i == len(rest) || rest[i] == '/'
}

// IsCleanVolRoot 先去空白并消解 "." / ".." 再判定，供配置校验与设置页保存共用。
func IsCleanVolRoot(p string) bool {
	return IsVolRoot(path.Clean(strings.TrimSpace(p)))
}
