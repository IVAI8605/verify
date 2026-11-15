// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"
	"sort"
	"strings"

	"verify/internal/store"
)

// 组级排序（v1.14.2 选择助手 §4.2）：会话级偏好，不持久化、不入库。
// 键与方向均为白名单映射，外部输入永远不进入 SQL/比较器字符串。

// groupSortKeys 是允许的排序键。
// v1.42：补入 path/type——sortGroups 早有对应分支，但白名单漏了它们，
// 导致 ?sort=path/type 静默回退 waste（媒体/文件夹/片头等内存排序端点受影响）。
var groupSortKeys = map[string]bool{
	"waste": true, "total_size": true, "count": true, "name": true,
	"path": true, "type": true,
}

// sortParam 从请求读 sort/order（非法值回退默认：waste/desc）。
func sortParam(r *http.Request) (string, string) {
	sortKey := r.URL.Query().Get("sort")
	if !groupSortKeys[sortKey] {
		sortKey = "waste"
	}
	order := r.URL.Query().Get("order")
	if order != "asc" && order != "desc" {
		order = "desc"
	}
	return sortKey, order
}

// sortGroups 内存完整全局排序（在分页切片之前调用，跨页顺序一致）。
// sk/order 直接传 sortParam 的两个返回值；媒体类端点用 sortGroupsQ。
func sortGroups(groups []store.Group, sortKey, order string) []store.Group {
	if len(groups) < 2 {
		return groups
	}
	less := func(a, b store.Group) bool { return false }
	switch sortKey {
	case "total_size":
		less = func(a, b store.Group) bool { return a.TotalSize < b.TotalSize }
	case "count":
		less = func(a, b store.Group) bool { return a.Count < b.Count }
	case "name":
		less = func(a, b store.Group) bool { return a.Name < b.Name }
	case "path":
		// v1.41：按组内首文件路径排序——相同目录的组自然凑堆（DC5 语义）
		less = func(a, b store.Group) bool { return firstPath(a) < firstPath(b) }
	case "type":
		// v1.41：按组内首个扩展名排序（同类文件凑堆）
		less = func(a, b store.Group) bool { return firstExt(a) < firstExt(b) }
	default: // waste（可节省空间）
		less = func(a, b store.Group) bool { return a.Waste < b.Waste }
	}
	out := groups
	sort.SliceStable(out, func(i, j int) bool {
		if order == "asc" {
			return less(out[i], out[j])
		}
		return less(out[j], out[i])
	})
	return out
}

// sortGroupsQ 是 sortGroups 的请求参数便捷版。
func sortGroupsQ(groups []store.Group, r *http.Request) []store.Group {
	sk, od := sortParam(r)
	return sortGroups(groups, sk, od)
}

// firstPath 组内最小路径（Files 未加载时退化为组名）。
func firstPath(g store.Group) string {
	best := ""
	for _, f := range g.Files {
		if best == "" || f.Path < best {
			best = f.Path
		}
	}
	if best == "" {
		return g.Name
	}
	return best
}

// firstExt 组内首个扩展名（小写）。
func firstExt(g store.Group) string {
	for _, f := range g.Files {
		e := strings.ToLower(f.Ext)
		if e != "" {
			return e
		}
	}
	return ""
}
