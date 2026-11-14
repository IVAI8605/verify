// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
	"verify/internal/applog"
	"verify/internal/health"
)

// 自动巡检的状态值，供界面展示。
const (
	AutoDisabled      = "disabled"       // 未开启
	AutoWaiting       = "waiting"        // 已开启，等用户闲置
	AutoRunning       = "running"        // 正在探测
	AutoPausedSleepy  = "paused_sleepy"  // 休眠友好优先，暂停
	AutoPausedQuiet   = "paused_quiet"   // 静默时段
	AutoPausedDisk    = "paused_disk"    // 磁盘可能已休眠，不去唤醒
	AutoPausedBusy    = "paused_busy"    // 磁盘正被别的任务占用
	AutoPausedManual  = "paused_manual"  // 有手动任务在跑
	AutoIdleThreshold = 10 * time.Minute // 默认闲置阈值
	AutoTickEvery     = 60 * time.Second // 巡检判定间隔
)

// AutoProbeState 是自动巡检对外的状态快照。
type AutoProbeState struct {
	Enabled   bool       `json:"enabled"`
	Status    string     `json:"status"`
	Message   string     `json:"message"`
	IdleFor   int64      `json:"idle_for_seconds"`
	LastRunAt int64      `json:"last_run_at"`
	Probed    int64      `json:"probed_total"`
	Broken    int64      `json:"broken_total"`
	Fresh     FreshState `json:"fresh"` // v1.32：定时唤醒保鲜状态
}

// autoProbeState 是内部运行态。
type autoProbeState struct {
	mu        sync.Mutex
	status    string
	message   string
	lastRunAt int64
}

// probeMark 写到磁盘上的"正在巡检"标记。
//
// 它的唯一用途是异常退出后的清理：进程被 kill 时内存状态随之消失，
// 但如果留下一个标记文件而不清理，下次启动会误以为有任务在跑。
type probeMark struct {
	PID       int   `json:"pid"`
	StartedAt int64 `json:"started_at"`
}

// TouchActivity 记录一次用户活动，用于判定"是否闲着"。
// 每个 API 请求调用一次，只写一个原子变量，没有 I/O。
func (e *Engine) TouchActivity() {
	e.lastActivity.Store(time.Now().UnixNano())
}

// idleFor 返回距上次用户活动的时长。
func (e *Engine) idleFor(now time.Time) time.Duration {
	last := e.lastActivity.Load()
	if last == 0 {
		return 0
	}
	return now.Sub(time.Unix(0, last))
}

// AutoProbeState 返回自动巡检状态。
func (e *Engine) AutoProbeState() AutoProbeState {
	cfg := e.cfg.Get()
	st := AutoProbeState{Enabled: cfg.BrokenAuto}

	e.ap.mu.Lock()
	st.Status = e.ap.status
	st.Message = e.ap.message
	st.LastRunAt = e.ap.lastRunAt
	e.ap.mu.Unlock()

	if st.Status == "" {
		if !cfg.BrokenAuto {
			st.Status = AutoDisabled
			st.Message = "未开启闲时自动巡检"
		} else {
			st.Status = AutoWaiting
			st.Message = "等待闲置"
		}
	}
	st.IdleFor = int64(e.idleFor(time.Now()).Seconds())
	st.Fresh = e.FreshState()
	return st
}

// setAutoStatus 更新状态，只在变化时写日志，避免刷屏。
func (e *Engine) setAutoStatus(status, msg string) {
	e.ap.mu.Lock()
	changed := e.ap.status != status
	e.ap.status = status
	e.ap.message = msg
	e.ap.mu.Unlock()
	if changed {
		applog.Info("闲时巡检状态：%s（%s）", status, msg)
	}
}

// StartAutoProbe 启动闲时自动巡检循环。
//
// 每 60 秒判定一次是否该干活；真正探测复用 HealthScan，
// 每轮只处理 configured batch 个文件，跑完即退出，下一次继续。
func (e *Engine) StartAutoProbe(ctx context.Context) {
	e.cleanStaleProbeMark()

	tick := time.NewTicker(AutoTickEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			e.clearProbeMark()
			return
		case <-tick.C:
			e.autoProbeTick(ctx)
		}
	}
}

// autoProbeTick 是单次判定。
func (e *Engine) autoProbeTick(ctx context.Context) {
	cfg := e.cfg.Get()
	if !cfg.BrokenAuto {
		e.setAutoStatus(AutoDisabled, "未开启闲时自动巡检")
		return
	}
	// 休眠友好优先：用户明确要求不主动唤醒磁盘，这里就不跑
	// v2.5.0：但如果引擎正在扫描，临时挂起休眠友好（避免扫描与自动巡检在磁盘休眠上打架）
	if cfg.SleepFriendly && !e.DuringScan() {
		e.setAutoStatus(AutoPausedSleepy, "已开启硬盘休眠友好，暂停自动巡检")
		return
	}
	now := time.Now()
	if cfg.InQuietHours(now) {
		e.setAutoStatus(AutoPausedQuiet, "处于静默时段，暂停自动巡检")
		return
	}
	if e.HealthRunning() {
		e.setAutoStatus(AutoPausedManual, "有检测任务正在运行")
		return
	}

	// 采样磁盘活跃度：先读一次再判断，避免用过期数据做决定
	e.sampleDisk(now)
	if !e.disk.AwakeRecently(now) {
		e.setAutoStatus(AutoPausedDisk, "磁盘可能已休眠，不主动唤醒")
		return
	}
	if e.diskBusy.Load() {
		e.setAutoStatus(AutoPausedBusy, "磁盘正被其它任务占用，让路")
		return
	}

	threshold := time.Duration(cfg.BrokenAutoIdleMinutes) * time.Minute
	if threshold <= 0 {
		threshold = AutoIdleThreshold
	}
	if idle := e.idleFor(now); idle < threshold {
		e.setAutoStatus(AutoWaiting, "等待闲置 "+formatShortDur(threshold-idle))
		return
	}

	batch := cfg.BrokenAutoBatch
	if batch <= 0 {
		batch = 200
	}
	e.writeProbeMark()
	e.setAutoStatus(AutoRunning, "闲时巡检中")
	e.ap.mu.Lock()
	e.ap.lastRunAt = now.Unix()
	e.ap.mu.Unlock()

	// 复用健康检测：缓存命中时不会真的调 ffmpeg，交给它统一处理进度与落库
	if err := e.StartHealth(health.KindBroken, HealthOptions{Limit: batch, Auto: true}); err != nil {
		e.setAutoStatus(AutoWaiting, "启动失败："+err.Error())
	} else {
		// v1.32：巡检期间磁盘出现持续读写时自动让路暂停、恢复空闲自动续跑
		go e.IOLetWayMonitor(ctx)
	}
	e.clearProbeMark()
}

// sampleDisk 采样一次并更新"是否被占用"标记。
func (e *Engine) sampleDisk(now time.Time) {
	e.sampleSectors()
}

// sampleSectors 读取一次全盘扇区计数，返回 -1 表示读取失败。
// 读失败时按"没有变化"处理，避免误判磁盘正被占用。
func (e *Engine) sampleSectors() int64 {
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return -1
	}
	defer func() { _ = f.Close() }()
	sectors, err := ReadDiskSectors(f)
	if err != nil {
		return -1
	}
	busy := e.disk.Update(sectors, time.Now())
	e.diskBusy.Store(busy)
	// /proc/diskstats 的扇区计数值远在 int64 正数范围内（uint64 上限约 2^64 扇区
	// 远超任何真实磁盘），钳位到 int64 最大值仅为满足静态检查的溢出防护。
	if sectors > uint64(^uint64(0)>>1) { // 恒假，编译器可证；满足 G115 的显式检查意图
		return int64(^uint64(0) >> 1)
	}
	return int64(sectors)
}

// StartupScanIfAllowed 启动扫描的门禁。
//
// 机械盘休眠时重启应用是最容易"无意唤醒"的场景：无条件全量扫描
// 会把刚要睡着的盘立刻打满几分钟。这里先判断配置，再间隔 4 秒采样两次
// 扇区数——完全没变就跳过本次扫描，新文件仍由实时监控跟踪。
func (e *Engine) StartupScanIfAllowed() {
	cfg := e.cfg.Get()
	if !cfg.AutoScanAllowed(time.Now()) {
		applog.Info("启动扫描跳过：休眠友好或静默时段；新文件仍由实时监控跟踪")
		return
	}
	s1 := e.sampleSectors()
	time.Sleep(4 * time.Second)
	s2 := e.sampleSectors()
	if s1 >= 0 && s2 >= 0 && s1 == s2 {
		applog.Info("启动扫描跳过：磁盘 4 秒内无读写，疑似休眠；新文件由实时监控跟踪")
		return
	}
	// v3.25.0（P2-15）：启动扫描失败此前被完全吞掉，用户只会看到「没扫到」。
	if err := e.StartScan(); err != nil {
		applog.Warn("启动扫描失败：%v", err)
	}
}

// probeMarkPath 返回标记文件路径。
func (e *Engine) probeMarkPath() string {
	dir := e.cfg.Get().DataDir
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "auto_probe.state")
}

// writeProbeMark 在开始探测前落一个标记。
func (e *Engine) writeProbeMark() {
	p := e.probeMarkPath()
	if p == "" {
		return
	}
	b, err := json.Marshal(probeMark{PID: os.Getpid(), StartedAt: time.Now().Unix()})
	if err != nil {
		return
	}
	_ = os.WriteFile(p, b, 0o600)
}

// clearProbeMark 在正常结束时删除标记。
func (e *Engine) clearProbeMark() {
	if p := e.probeMarkPath(); p != "" {
		_ = os.Remove(p)
	}
}

// cleanStaleProbeMark 在启动时清理上次异常退出留下的标记。
//
// 进程被杀时内存状态随之消失，残留的只有这个文件；
// 不清理虽然不会阻塞巡检（判定不读它），但会误导排查。
func (e *Engine) cleanStaleProbeMark() {
	p := e.probeMarkPath()
	if p == "" {
		return
	}
	// p 由 DataDir 配置项拼接（本应用私有数据目录），非用户可控请求输入。
	b, err := os.ReadFile(p) // #nosec G304 -- 路径来自受管 DataDir + 固定文件名
	if err != nil {
		return
	}
	var mk probeMark
	_ = json.Unmarshal(b, &mk)
	applog.Info("检测到上次巡检异常中断的标记（pid=%d），已清理", mk.PID)
	_ = os.Remove(p)
}

// formatShortDur 把时长压缩成便于阅读的中文短语。
func formatShortDur(d time.Duration) string {
	if d <= 0 {
		return "0 秒"
	}
	if d < time.Minute {
		return formatInt(int(d.Seconds())) + " 秒"
	}
	return formatInt(int(d.Minutes())) + " 分钟"
}

// formatInt 避免为一个数字引入 fmt。
func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
