// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package taskguard 统一登记长任务占用的资源与互斥关系（v3.18.0）。
//
// 背景：此前有三套并行机制——heavySem 只限总并发、ffmpegGate 只管
// 「ffmpeg 与损坏探测」、各入口又各自查 IsRunning / HealthRunning / cleanupRun。
// 结果是扫描与转码可以同时启动，两边都把 CPU 与磁盘吃满；界面也说不清
// 「现在到底在跑什么」。这里把「谁在跑、占什么、能不能再起」收成一处。
//
// 互斥口径（与版本规划一致）：
//   - io-exclusive：扫描 / 索引体检 / 清理计划 / 批量隔离 / 目录移动 / 空目录清理 / 导入
//   - ffmpeg-exclusive：视频与片头分析 / 裁剪 / 转码 / cropdetect
//   - 两个独占组之间也互斥：任一独占任务在跑时，另一个独占任务一律拒绝。
//   - 非独占任务只登记资源，不参与拒绝，允许与读并行。
//
// 优先级（写进注释，避免以后再各写一套）：
// userPaused（用户暂停）> taskguard 拒绝 > diskBusyNow 等待 > 正常执行。
//
// 本包保持纯 Go、无外部依赖，也不反向依赖 engine / config：
// 开关通过构造函数注入，测试可直接传固定值。
package taskguard

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Resource 是任务占用的资源类型，用于界面展示与后续调度扩展。
type Resource string

const (
	ResCPU     Resource = "cpu"
	ResDiskIO  Resource = "disk-io"
	ResFFmpeg  Resource = "ffmpeg"
	ResDBWrite Resource = "db-write"
)

// 独占组。空字符串表示非独占（只登记，不拒绝别人）。
const (
	GroupIO     = "io-exclusive"
	GroupFFmpeg = "ffmpeg-exclusive"
)

// Kind 是任务类型。
type Kind string

const (
	KindScan        Kind = "scan"
	KindHealth      Kind = "health"
	KindVideo       Kind = "video"
	KindTrim        Kind = "trim"
	KindTranscode   Kind = "transcode"
	KindCrop        Kind = "crop"
	KindIndexRepair Kind = "index_repair"
	KindCleanup     Kind = "cleanup"
	KindTrash       Kind = "trash"
	KindRename      Kind = "rename"
	KindImport      Kind = "import"
)

// maxHistory 是 /api/tasks 保留的最近历史条数。
const maxHistory = 10

// Task 描述一个正在运行的长任务。
type Task struct {
	ID        string     `json:"id"`
	Kind      Kind       `json:"kind"`
	Name      string     `json:"name"`
	Resources []Resource `json:"resources,omitempty"`
	Group     string     `json:"group,omitempty"`
	StartedAt int64      `json:"started_at"`
}

// Record 是一条已结束的任务历史。
type Record struct {
	Task
	FinishedAt int64  `json:"finished_at"`
	Result     string `json:"result"` // done / canceled
}

// ConflictError 表示与正在运行的独占任务冲突。
type ConflictError struct {
	Occupant Task
	// Paused 表示占用者正处于用户暂停态（v3.25.2 P3-7）。
	// 暂停任务仍占独占槽是刻意设计：它并没有退出运行循环，
	// 释放槽会让「暂停扫描时启动转码」可行，恢复后两个独占任务会并行。
	// 所以这里不改互斥判定，只把原因说清楚，免得用户以为界面卡死。
	Paused bool
}

func (e *ConflictError) Error() string {
	if e.Paused {
		return fmt.Sprintf("「%s」已暂停（%s），请先继续或停止后再试", KindName(e.Occupant.Kind), groupText(e.Occupant.Group))
	}
	return fmt.Sprintf("「%s」正在运行（%s），请等它结束后再试", KindName(e.Occupant.Kind), groupText(e.Occupant.Group))
}

// KindName 返回任务类型的中文名，供接口与提示复用。
func KindName(k Kind) string {
	switch k {
	case KindScan:
		return "扫描"
	case KindHealth:
		return "文件健康检测"
	case KindVideo:
		return "视频分析"
	case KindTrim:
		return "剪除片头"
	case KindTranscode:
		return "转码"
	case KindCrop:
		return "识别竖横屏"
	case KindIndexRepair:
		return "索引体检"
	case KindCleanup:
		return "清理计划"
	case KindTrash:
		return "批量隔离"
	case KindRename:
		return "批量改名"
	case KindImport:
		return "导入"
	default:
		return string(k)
	}
}

// groupText 把独占组翻译成人话，用在拒绝提示里。
func groupText(group string) string {
	switch group {
	case GroupIO:
		return "占用磁盘"
	case GroupFFmpeg:
		return "占用解码/编码资源"
	default:
		return "占用系统资源"
	}
}

// Guard 是任务登记表。零值不可用，必须经 New 构造。
type Guard struct {
	mu      sync.Mutex
	seq     int64
	running map[string]Task
	hist    []Record
	enabled func() bool
	// paused 是用户暂停态，只影响冲突提示的措辞，不参与互斥判定（v3.25.2 P3-7）。
	paused bool
}

// New 构造登记表；enabled 为 nil 时视为始终启用。
func New(enabled func() bool) *Guard {
	return &Guard{running: map[string]Task{}, enabled: enabled}
}

func (g *Guard) on() bool {
	if g.enabled == nil {
		return true
	}
	return g.enabled()
}

// Acquire 登记一个任务；与正在运行的独占任务冲突时返回 *ConflictError。
//
// 返回的 done 必须调用且只调用一次（建议 defer），用于释放并记一条历史。
// 开关关闭时返回空操作的 done，行为完全回到 v3.15。
func (g *Guard) Acquire(t Task) (func(result string), error) {
	if g == nil || !g.on() {
		return func(string) {}, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if t.Group != "" {
		for _, cur := range g.running {
			if cur.Group != "" {
				return nil, &ConflictError{Occupant: cur, Paused: g.paused}
			}
		}
	}
	g.seq++
	if t.ID == "" {
		t.ID = fmt.Sprintf("%s-%d", t.Kind, g.seq)
	}
	t.StartedAt = time.Now().Unix()
	g.running[t.ID] = t
	id := t.ID
	return func(result string) { g.release(id, result) }, nil
}

func (g *Guard) release(id, result string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	t, ok := g.running[id]
	if !ok {
		return
	}
	delete(g.running, id)
	if result == "" {
		result = "done"
	}
	g.hist = append(g.hist, Record{Task: t, FinishedAt: time.Now().Unix(), Result: result})
	if len(g.hist) > maxHistory {
		g.hist = g.hist[len(g.hist)-maxHistory:]
	}
}

// Paused 查询用户暂停态。
func (g *Guard) Paused() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

// SetPaused 由 engine 的暂停 / 恢复入口同步状态（v3.25.2 P3-7）。
// 只用来把「为什么不让我启动」说清楚，不释放任何独占槽：
// 暂停中的任务仍在自己的运行循环里，提前释放会让恢复后的两个独占任务并行。
func (g *Guard) SetPaused(p bool) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.paused = p
	g.mu.Unlock()
}

// Running 返回当前运行中的任务快照（按开始时间升序）。
func (g *Guard) Running() []Task {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Task, 0, len(g.running))
	for _, t := range g.running {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out
}

// History 返回最近结束的任务（按结束时间升序，最多 10 条）。
func (g *Guard) History() []Record {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Record, len(g.hist))
	copy(out, g.hist)
	return out
}

// BusyGroup 返回当前占用独占槽的任务（没有则返回零值）。
// 供状态接口展示「正在运行：扫描（磁盘独占）」。
func (g *Guard) BusyGroup() (Task, bool) {
	if g == nil {
		return Task{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, t := range g.running {
		if t.Group != "" {
			return t, true
		}
	}
	return Task{}, false
}
