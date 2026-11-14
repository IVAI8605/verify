// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package fsbrowse

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"verify/internal/cache"
)

// 授权目录自动发现（v1.41.3）。
//
// 原理：飞牛 trimafs 在 /fs/<uid>/ftp/ 提供文件服务统一视图，其中列出全部
// 共享文件夹名；每个共享是否可读由 trimacl（内核态授权）决定——应用的
// POSIX 权限被覆盖（授权子树即使 0000 权限也可读，未授权则 EACCES）。
// 因此：枚举共享名 → 映射回真实路径（/volN/1000/<名> 或 /volN/<名>）→
// readable 探测 → 通过者即为"trimacl 实际授权的目录"。
// fnOS 未提供授权清单接口，这是不臆造前提下的"自动获取授权目录"实现。

const (
	discoverTTL = 60 * time.Second
	discoverDir = "/fs"
	// discoverMaxDepth 限制向下探测的层数：飞牛只授权共享下的某个子目录时
	// （如 /vol3/1000/<共享目录>/<子目录>），共享根本身不可读，只能逐层试。
	// 2 层足以覆盖常见授权层级，同时避免在大共享里遍历成千上万个子目录。
	discoverMaxDepth = 2
	// discoverProbeBudget 单次发现的目录探测上限，防止超大共享拖慢首个请求。
	discoverProbeBudget = 2000
)

var (
	discoverMu    sync.Mutex
	discoverCache []string
	discoverAt    time.Time
)

// DiscoverAuthorizedRoots 返回当前 trimacl 实际授权的目录（真实路径）。
// 结果缓存 60 秒；非 fnOS 环境（无 /fs）返回空。
func DiscoverAuthorizedRoots() []string {
	discoverMu.Lock()
	defer discoverMu.Unlock()
	if time.Since(discoverAt) < discoverTTL {
		return discoverCache
	}
	discoverCache = discoverOnce()
	discoverAt = time.Now()
	return discoverCache
}

// discoverOnce 执行一次发现：/fs/<uid>/ftp 枚举共享名 → 映射真实路径 → 探测。
// 共享根不可读时（飞牛只授权了共享下的某个子目录），继续向下探测子目录。
func discoverOnce() []string {
	uids, ftpViews := collectFTPViews()
	if len(uids) == 0 {
		return nil
	}
	// 1) 收集全部共享文件夹名（目录名清单本身无内容泄露，仅用于探测）
	nameSet := map[string]bool{}
	for _, view := range ftpViews {
		des, err := os.ReadDir(view)
		if err != nil {
			continue // 视图不可列，跳过
		}
		for _, d := range des {
			if d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
				nameSet[d.Name()] = true
			}
		}
	}
	// 2) 名字 → 真实路径映射 + trimacl 探测（readable 含挂载管辖校验）。
	// 共享根不可读时用 /fs 视图逐层下探，找到"最上层可读目录"即为授权根。
	budget := discoverProbeBudget
	var out []string
	probed := map[string]bool{} // 已探测过的共享根，避免两个前缀重复下探
	seen := map[string]bool{}   // 已加入结果的目录
	for _, name := range sortedKeys(nameSet) {
		viewsForName := make([]string, 0, len(ftpViews))
		for _, v := range ftpViews {
			viewsForName = append(viewsForName, filepath.Join(v, name))
		}
		for vol := 1; vol <= 9; vol++ {
			for _, prefix := range []string{
				"/vol" + strconv.Itoa(vol) + "/1000/",
				"/vol" + strconv.Itoa(vol) + "/",
			} {
				p := prefix + name
				if probed[p] {
					continue
				}
				st, err := os.Stat(p)
				if err != nil || !st.IsDir() {
					continue // 该卷没有这个共享，或共享名已被删除
				}
				probed[p] = true
				for _, r := range probeAuthorized(p, viewsForName, 0, &budget) {
					if !seen[r] {
						seen[r] = true
						out = append(out, r)
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// collectFTPViews 返回 /fs 下全部数字 uid 目录与对应的 ftp 视图路径。
// ftp 视图是飞牛文件服务的统一入口；真实路径受 trimacl 限制时，只能用视图列子目录。
func collectFTPViews() ([]string, []string) {
	des, err := os.ReadDir(discoverDir)
	if err != nil {
		return nil, nil
	}
	var uids, views []string
	for _, u := range des {
		if !u.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(u.Name()); err != nil {
			continue // 只处理 uid 数字目录
		}
		uids = append(uids, u.Name())
		views = append(views, filepath.Join(discoverDir, u.Name(), "ftp"))
	}
	return uids, views
}

// probeAuthorized 从 realPath 向下找最上层可读目录：可读即返回；
// 不可读则借助 /fs 视图列出子目录继续试，最多 discoverMaxDepth 层。
// budget 是本次发现的剩余探测次数，耗尽后停止，避免超大共享拖慢首个请求。
func probeAuthorized(realPath string, views []string, depth int, budget *int) []string {
	if *budget <= 0 {
		return nil
	}
	*budget--
	if readable(realPath) == nil {
		return []string{realPath}
	}
	if depth >= discoverMaxDepth {
		return nil
	}
	var out []string
	for _, name := range listChildDirs(realPath, views, budget) {
		childViews := make([]string, 0, len(views))
		for _, v := range views {
			childViews = append(childViews, filepath.Join(v, name))
		}
		out = append(out, probeAuthorized(filepath.Join(realPath, name), childViews, depth+1, budget)...)
	}
	return out
}

// listChildDirs 收集目录名：先试真实路径，再试各 uid 的 /fs 视图。
// 视图可能按 trimacl 过滤（只显示已授权子目录），也可能整体不可列，均按空处理。
func listChildDirs(realPath string, views []string, budget *int) []string {
	set := map[string]bool{}
	collect := func(dir string) {
		if *budget <= 0 || len(set) >= childDirLimit {
			return
		}
		*budget--
		des, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, d := range des {
			if d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
				set[d.Name()] = true
				// v3.10.10（P2-14）：每层设上限，避免单目录几十万子目录时
				// 在全局锁内既撑爆内存又把其它浏览请求拖住。
				if len(set) >= childDirLimit {
					return
				}
			}
		}
	}
	collect(realPath)
	for _, v := range views {
		collect(v)
	}
	return sortedKeys(set)
}

// childDirLimit 是自动发现时单层最多收集的子目录数量。
const childDirLimit = 2000

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Diagnose 授权诊断：返回 /fs 枚举到的共享名、每个候选路径的探测结果与挂载类型，
// 便于核对飞牛授权与应用可读性的对应关系（只读，含错误原因）。
func Diagnose() map[string]any {
	type probe struct {
		Path  string `json:"path"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
		FS    string `json:"fs,omitempty"`
	}
	// v3.5.3：记录 /fs 视图能否列出共享的子目录——自动发现向下探测依赖它，
	// 只读探测第一个 uid 的视图，避免诊断结果过长。
	type viewProbe struct {
		View  string `json:"view"`
		OK    bool   `json:"ok"`
		Count int    `json:"count"`
		Error string `json:"error,omitempty"`
	}
	names := map[string]bool{}
	var uids []string
	var firstView string
	if des, err := os.ReadDir(discoverDir); err == nil {
		for _, u := range des {
			if !u.IsDir() {
				continue
			}
			if _, err := strconv.Atoi(u.Name()); err != nil {
				continue
			}
			uids = append(uids, u.Name())
			v := filepath.Join(discoverDir, u.Name(), "ftp")
			if firstView == "" {
				firstView = v
			}
			if sub, err := os.ReadDir(v); err == nil {
				for _, d := range sub {
					if d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
						names[d.Name()] = true
					}
				}
			}
		}
	}
	viewProbes := []viewProbe{}
	if firstView != "" {
		for _, name := range sortedKeys(names) {
			vp := viewProbe{View: filepath.Join(firstView, name)}
			des, err := os.ReadDir(vp.View)
			if err != nil {
				vp.Error = err.Error()
			} else {
				vp.OK = true
				vp.Count = len(des)
			}
			viewProbes = append(viewProbes, vp)
		}
	}
	probes := []probe{}
	for _, name := range sortedKeys(names) {
		for vol := 1; vol <= 9; vol++ {
			for _, prefix := range []string{
				"/vol" + strconv.Itoa(vol) + "/1000/",
				"/vol" + strconv.Itoa(vol) + "/",
			} {
				p := prefix + name
				pr := probe{Path: p}
				if fs, ok := cache.FSType(p); ok {
					pr.FS = fs
				}
				if err := readable(p); err != nil {
					pr.Error = err.Error()
				} else {
					pr.OK = true
				}
				probes = append(probes, pr)
			}
		}
	}
	authz := make([]string, 0)
	for _, p := range probes {
		if p.OK {
			authz = append(authz, p.Path)
		}
	}
	return map[string]any{
		"fs_uids":      uids,
		"share_names":  sortedKeys(names),
		"probes":       probes,
		"authorized":   authz,
		"discovered":   discoverOnce(), // 含向下探测结果，便于核对“最下级授权目录”
		"fs_view":      viewProbes,     // /fs 视图列子目录能力（向下探测的前提）
		"discover_dir": discoverDir,
	}
}
