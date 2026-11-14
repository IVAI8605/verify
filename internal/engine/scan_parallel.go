// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"errors"
	"sync"
	"syscall"

	"verify/internal/health"
)

// deviceGroups 把扫描根按物理设备分组（v3.10.2）。
//
// 「几个卷」不等于「几个物理卷」：飞牛的 /vol1、/vol2 可能只是同一块盘上的
// 多个挂载点（Btrfs 子卷 / 绑定挂载），也可能落在不同硬盘上。只有真正位于
// 不同设备的根才值得并行——同盘并行只会让磁头与请求队列互相争抢，反而更慢。
// 用 st_dev 判断最直接，不需要解析 /proc/mounts，也不会被软链接骗到。
//
// 返回顺序保持「首次出现」的顺序，便于日志与结果稳定复现。
//
// 已知局限（v3.10.3 复核）：同一块盘的不同分区 st_dev 不同，会被当成两组并行。
// 不能简单改成「按主设备号合并」——NVMe 的动态主设备号可能被多块盘共用，
// 那会把不同硬盘错误串行化。飞牛常规部署是整盘单分区，这里保持保守语义。
func deviceGroups(roots []string) [][]string {
	if len(roots) <= 1 {
		return [][]string{roots}
	}
	devs := make([]uint64, 0, len(roots))
	byDev := make(map[uint64][]string, len(roots))
	// 拿不到设备号的根单独归一组：无法判断是否同盘，串行处理比冒险并行稳妥。
	var unknown []string
	for _, root := range roots {
		var st syscall.Stat_t
		if err := syscall.Stat(root, &st); err != nil {
			unknown = append(unknown, root)
			continue
		}
		dev := uint64(st.Dev)
		if _, ok := byDev[dev]; !ok {
			devs = append(devs, dev)
		}
		byDev[dev] = append(byDev[dev], root)
	}
	groups := make([][]string, 0, len(devs)+1)
	for _, dev := range devs {
		groups = append(groups, byDev[dev])
	}
	if len(unknown) > 0 {
		groups = append(groups, unknown)
	}
	return groups
}

// deviceGroupCount 返回物理卷数量，供日志与界面提示使用。
func deviceGroupCount(roots []string) int {
	return len(deviceGroups(roots))
}

// findEmptyDirsParallel 按物理卷并行找出空目录（v3.10.2）。
//
// 空目录检测是纯读遍历、按根目录互不依赖，天然可并行；
// 每组内部仍由一个 goroutine 串行走树，避免同盘自己跟自己抢 IO。
func findEmptyDirsParallel(ctx context.Context, roots []string, skip func(string) bool) ([]string, error) {
	groups := deviceGroups(roots)
	parts := make([][]string, len(groups))
	errs := make([]error, len(groups))
	var wg sync.WaitGroup
	for i, group := range groups {
		if len(group) == 0 {
			continue
		}
		wg.Add(1)
		go func(i int, group []string) {
			defer wg.Done()
			dirs, err := health.FindEmptyDirs(ctx, group, skip)
			if err != nil {
				// v3.10.3：以前这里吞掉错误恒返回 nil，某块盘遍历失败会被
				// 当成「没有空目录」，用户看到的结论是错的。保留部分结果并
				// 把各组错误聚合上抛，让调用方显式失败。
				errs[i] = err
				return
			}
			parts[i] = dirs
		}(i, group)
	}
	wg.Wait()

	var out []string
	for _, part := range parts {
		out = append(out, part...)
	}
	return out, errors.Join(errs...)
}
