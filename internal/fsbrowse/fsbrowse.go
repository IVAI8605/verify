// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package fsbrowse 为设置页提供受限的目录浏览能力：只允许在 /vol* 或数据目录内操作。
package fsbrowse

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
	"verify/internal/cache"
	"verify/internal/volpath"
)

// Entry 是目录列表中的一个子目录。
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Locked v1.24.1：trimacl（fnOS 应用目录授权）只放行被授权子树，祖先路径
	// 对应用用户是 permission denied——不可读的目录照样列出（显示锁标），
	// 让用户看得到结构并直接手输已授权路径，而不是整页报错。
	Locked bool   `json:"locked,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Listing 是目录浏览结果；Parent 为空表示已到允许范围的顶层。
type Listing struct {
	Path    string   `json:"path"`
	Parent  string   `json:"parent"`
	Roots   []string `json:"roots"`
	Entries []Entry  `json:"entries"`
	// Locked v1.24.1：当前目录本身不可读（未授权/无权限）——前端显示锁标与原因，
	// 但返回结构完整（含 roots），供「手动输入路径」继续操作。
	Locked bool   `json:"locked,omitempty"`
	Reason string `json:"reason,omitempty"`
	// HiddenLocked v1.33：入口视图中被收起的未授权卷根数量——顶层不再固定列出
	// 全部 /vol*（未授权的卷对用户只是噪音），仅提示"另有 N 个未授权卷根"。
	HiddenLocked int `json:"hidden_locked,omitempty"`
	// Truncated v3.10.10（P2-14）：子目录数量超过上限时只返回前 N 条并置位，
	// 防止单目录几十万条目把响应体与内存同时打满。
	Truncated bool `json:"truncated,omitempty"`
}

// browseMaxEntries 是单次列举返回的子目录条数上限。
// 截断不影响继续浏览：用户仍可手动输入更深的完整路径。
const browseMaxEntries = 5000

// browseScanNames 是排序前最多收集的目录名数量。
// 读取量比返回量高一档，让「按名称排序后截断」的结果稳定；
// 超过后按目录读取顺序截断，避免单个超大目录把内存吃满。
const browseScanNames = 50000

// Roots 返回可浏览的顶层目录。优先使用飞牛挂载的 /vol*；其他平台退回数据目录。
func Roots(dataDir string) []string {
	var out []string
	matches, _ := filepath.Glob("/vol*")
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.IsDir() {
			out = append(out, filepath.Clean(m))
		}
	}
	if len(out) == 0 && strings.TrimSpace(dataDir) != "" {
		out = append(out, filepath.Clean(dataDir))
	}
	sort.Strings(out)
	return dedupe(out)
}

func dedupe(in []string) []string {
	out := in[:0]
	var last string
	for _, v := range in {
		if v != last {
			out = append(out, v)
			last = v
		}
	}
	return out
}

// Normalize 校验路径是否位于允许范围内，并返回清理后的绝对路径。
func Normalize(roots []string, p string) (string, bool) {
	p = filepath.Clean(strings.TrimSpace(p))
	if p == "" || p == "." || strings.ContainsRune(p, 0) {
		return "", false
	}
	for _, r := range roots {
		r = filepath.Clean(r)
		if p == r || strings.HasPrefix(p, r+string(os.PathSeparator)) {
			return p, true
		}
	}
	return "", false
}

// resolveWithin 在路径校验后再解析符号链接，防止卷内软链接指向卷外目录。
func resolveWithin(roots []string, p string) (string, bool) {
	clean, ok := Normalize(roots, p)
	if !ok {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", false
	}
	if _, ok := Normalize(roots, resolved); !ok {
		return "", false
	}
	return resolved, true
}

// List 列出指定目录下的子目录；path 为空时返回顶层入口视图（卷根 + 快捷入口）。
// shortcuts 是应用已知的用户目录（已配置扫描根/保护/排除等）——trimacl 授权只放行
// 被授权子树，卷根通常不可穿越，这些已知路径是用户能直接进入的入口（v1.24.1）。
func List(roots, shortcuts []string, path string) (Listing, error) {
	if len(roots) == 0 {
		// v1.41.5：没有配置扫描目录时返回空视图（前端提示去设置添加），不报错
		return Listing{Path: "", Entries: []Entry{}}, nil
	}
	if strings.TrimSpace(path) == "" {
		// 顶层入口视图（v1.41.1 安全修复）：**只列出用户在应用内添加或浏览过的目录**
		// （shortcuts，由调用方保证已并入数据目录兜底）。
		// 卷根 /vol* 不再自动列出——无论是否"可读"：WebDAV 等全局可读挂载
		// 会让 readable 探测误判为已授权，必须由用户明确添加才展示。
		// roots 仍作为 Normalize 白名单（手动粘贴完整路径仍可进入已授权子树）。
		entries := make([]Entry, 0, len(shortcuts))
		seen := map[string]bool{}
		hiddenLocked := 0
		add := func(p string) {
			p = filepath.Clean(p)
			if p == "" || seen[p] {
				return
			}
			seen[p] = true
			// v1.41.2：未授权/不受管辖的配置目录直接收起（只显示已授权目录）
			if err := readable(p); err != nil {
				hiddenLocked++
				return
			}
			// v3.5.3：入口按“最下级文件夹名”显示（如 子目录），完整路径放 Path
			// 供前端悬停查看；授权自动发现出来的深层目录不再显示成整串 /volN/...
			entries = append(entries, Entry{Name: filepath.Base(p), Path: p})
		}
		for _, sc := range shortcuts {
			add(sc)
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Locked != entries[j].Locked {
				return !entries[i].Locked
			}
			return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
		})
		return Listing{Path: "", Roots: roots, Entries: entries, HiddenLocked: hiddenLocked}, nil
	}
	dir, ok := resolveWithin(roots, path)
	if !ok {
		return Listing{}, fmt.Errorf("目录不在允许范围内")
	}
	st, err := os.Stat(dir)
	if err != nil {
		return Listing{Path: dir, Roots: roots,
			Locked: true, Reason: fmt.Sprintf("无法访问该目录（%v）——可能未在飞牛系统设置中授权本应用", err)}, nil
	}
	if !st.IsDir() {
		return Listing{}, fmt.Errorf("不是目录")
	}
	// v3.10.9：进入子目录后也要校验挂载类型。此前只在入口视图与手动输入路径时校验，
	// 已授权根下再进入 WebDAV/云盘等 fuse 挂载点可以照常列举（allow_other 下 os.ReadDir 会成功）。
	// 只在能确定是非本地挂载时拒绝：Windows 本地开发没有 /proc/self/mounts，无法判断时保持旧行为。
	if _, ok := cache.FSType(dir); ok && !cache.ManagedMountFS(dir) {
		return Listing{Path: dir, Roots: roots, Locked: true, Reason: "该路径位于 WebDAV/云盘等挂载上，不受飞牛授权机制保护，禁止浏览"}, nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return Listing{Path: dir, Roots: roots,
			Locked: true, Reason: fmt.Sprintf("无法读取目录（%v）——可能未在飞牛系统设置中授权本应用", err)}, nil
	}
	defer func() { _ = f.Close() }()
	names, truncated := listSubdirs(f, browseScanNames)
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	// v3.10.10（P2-14）：返回条数设上限，超出部分置 Truncated 让前端提示；
	// 手动输入更深路径仍可继续浏览，不会因为截断而进不去。
	if len(names) > browseMaxEntries {
		names = names[:browseMaxEntries]
		truncated = true
	}
	entries := make([]Entry, 0, len(names))
	for _, name := range names {
		entries = append(entries, Entry{Name: name, Path: filepath.Join(dir, name)})
	}

	parent := ""
	if up := filepath.Dir(dir); up != dir {
		if p, ok := Normalize(roots, up); ok {
			parent = p
		}
	}
	return Listing{Path: dir, Parent: parent, Roots: roots, Entries: entries, Truncated: truncated}, nil
}

// listSubdirs 分批读取子目录名（只保留目录），最多收集 max 个。
// 分批而不是一次性 os.ReadDir，避免几十万子目录的目录把内存顶到尖峰。
func listSubdirs(f *os.File, max int) ([]string, bool) {
	names := make([]string, 0, 256)
	for {
		batch, err := f.ReadDir(256)
		for _, de := range batch {
			if !de.IsDir() {
				continue
			}
			names = append(names, de.Name())
			if len(names) >= max {
				return names, true
			}
		}
		if err != nil {
			return names, false
		}
	}
}

// AuthorizedPath 判断用户手动输入的路径是否是可浏览的本地授权目录。
// 只接受 /vol* 下的真实目录，并复用 readable 的 trimacl 与挂载类型检查；
// 用于「已授权但未被自动发现」的子目录（如 /vol3/1000/<共享目录>/<子目录>）。
func AuthorizedPath(path string) bool {
	p := filepath.Clean(strings.TrimSpace(path))
	// v3.10.10（P2-15）：按 /vol<数字> 边界判断，避免 /volumes 这类同前缀路径
	// 混过第一道检查，与 Normalize 的目录边界口径保持一致。
	// v3.17.3（F-02）：判定逻辑收进 volpath，与设置页保存、配置加载共用一份。
	if !volpath.IsVolRoot(p) {
		return false
	}
	st, err := os.Stat(p)
	if err != nil || !st.IsDir() {
		return false
	}
	return readable(p) == nil
}

// readable 探测目录是否可读（trimacl 授权探测）。
func readable(dir string) error {
	// v1.41.2：WebDAV/云盘等 fuse 挂载全局可读（allow_other），"能打开"不代表
	// 已授权——非本地盘挂载一律视为不可浏览（v1.41.1 漏洞的根治）。
	if !cache.ManagedMountFS(dir) {
		return fmt.Errorf("该路径位于 WebDAV/云盘等挂载上，不受飞牛授权机制保护，禁止浏览")
	}
	f, err := os.Open(dir) // #nosec G304 -- 目录路径来自卷根枚举或应用自身配置，非请求拼接
	if err != nil {
		return fmt.Errorf("无访问权限（未在飞牛系统设置中授权？）")
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Readdirnames(1); err != nil && err != io.EOF {
		return fmt.Errorf("无访问权限（未在飞牛系统设置中授权？）")
	}
	return nil
}

// Mkdir 在指定父目录下新建文件夹，并校验新路径仍在允许范围内。
func Mkdir(roots []string, parent, name string) (string, error) {
	parent, ok := resolveWithin(roots, parent)
	if !ok {
		return "", fmt.Errorf("父目录不在允许范围内")
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("文件夹名不能为空")
	}
	if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("文件夹名不能包含路径分隔符")
	}
	if utf8.RuneCountInString(name) > 100 {
		return "", fmt.Errorf("文件夹名过长")
	}
	target := filepath.Join(parent, name)
	if _, ok := Normalize(roots, target); !ok {
		return "", fmt.Errorf("目标目录不在允许范围内")
	}
	// v3.10.9：新建前同样校验父目录挂载类型，避免在已授权根下的云盘挂载点里建目录。
	// 与 List 一致，无法判断挂载类型的平台（Windows 本地开发）保持放行。
	if _, ok := cache.FSType(parent); ok && !cache.ManagedMountFS(parent) {
		return "", fmt.Errorf("该路径位于 WebDAV/云盘等挂载上，不受飞牛授权机制保护，禁止操作")
	}
	// 新建的是用户在 NAS 共享卷里可见的目录，与文件管理器一致使用 0755；
	// 收窄到 0750 会让其他账号无法访问刚建好的目录。
	if err := os.Mkdir(target, 0o755); err != nil { // #nosec G301 -- 用户可见的共享目录，需保持 0755
		if os.IsExist(err) {
			return "", fmt.Errorf("文件夹已存在")
		}
		return "", fmt.Errorf("新建文件夹失败（%v）", err)
	}
	return target, nil
}
