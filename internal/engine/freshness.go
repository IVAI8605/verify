// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"sync"
	"time"

	"verify/internal/applog"
	"verify/internal/config"
	"verify/internal/health"
)

// 每日定时唤醒与数据保鲜（v1.32 引入 §1.2）。
//
// 语义（红线修正）：休眠友好 = "默认不唤醒、授权可定时唤醒"。
// 用户开启 wake_enabled 后，在授权时段（默认 04:00–08:00）内允许唤醒磁盘，
// 跑一次"数据保鲜"任务清单：增量索引对账（扫描含指纹/缩略图预计算）→ 健康巡检。
// 目标：首次全盘扫描后，仅靠实时监控 + 定时保鲜，打开软件即可直接勾选。
// 时段结束或任务完成即放手让睡；窗口结束会强制停止未完成的扫描。

// FreshState 是定时唤醒对外状态快照（并入 /api/health/status 的 auto 节点）。
type FreshState struct {
	Enabled    bool   `json:"wake_enabled"`
	Window     string `json:"wake_window"`
	Running    bool   `json:"wake_running"`
	LastDate   string `json:"wake_last_date"`
	LastRunAt  int64  `json:"wake_last_run_at"`
	LastResult string `json:"wake_last_result"`
}

// freshnessState 内部运行态。
type freshnessState struct {
	mu         sync.Mutex
	lastDate   string
	lastRunAt  int64
	lastResult string
	running    bool
}

// FreshState 返回定时唤醒状态。
func (e *Engine) FreshState() FreshState {
	cfg := e.cfg.Get()
	e.fw.mu.Lock()
	defer e.fw.mu.Unlock()
	return FreshState{
		Enabled:    cfg.WakeEnabled,
		Window:     cfg.WakeWindow,
		Running:    e.fw.running,
		LastDate:   e.fw.lastDate,
		LastRunAt:  e.fw.lastRunAt,
		LastResult: e.fw.lastResult,
	}
}

// setFreshState 更新保鲜运行态。
func (e *Engine) setFreshState(date string, at int64, result string, running bool) {
	e.fw.mu.Lock()
	e.fw.lastDate = date
	e.fw.lastRunAt = at
	e.fw.lastResult = result
	e.fw.running = running
	e.fw.mu.Unlock()
}

// StartFreshnessLoop 定时唤醒循环：每 60 秒判定一次是否进入授权保鲜窗口。
func (e *Engine) StartFreshnessLoop(ctx context.Context) {
	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.freshnessTick(ctx)
		}
	}
}

// freshnessTick 单次判定：在授权窗口内、今天还没跑过、没有扫描在跑 → 启动保鲜。
func (e *Engine) freshnessTick(ctx context.Context) {
	cfg := e.cfg.Get()
	if !cfg.WakeEnabled {
		return
	}
	r := config.ParseQuietHours(cfg.WakeWindow)
	if !r.Valid {
		// 配置坏了按"没授权"处理，宁可不做也不乱唤醒
		e.setFreshState("", 0, "唤醒时段配置无效，未执行", false)
		return
	}
	now := time.Now()
	if !r.Contains(now) {
		return
	}
	today := now.Format("2006-01-02")
	if e.IsRunning() {
		return // 有手动/启动扫描在跑，保鲜不叠加
	}
	e.fw.mu.Lock()
	done := e.fw.lastDate == today
	e.fw.mu.Unlock()
	if done {
		return
	}

	applog.Info("定时唤醒保鲜：进入授权时段 %s，启动增量对账（含指纹/缩略图预计算）", cfg.WakeWindow)
	if err := e.StartScanMode(ScanModeFull); err != nil {
		applog.Warn("定时唤醒保鲜：启动扫描失败：%s", err.Error())
		e.setFreshState(today, now.Unix(), "启动失败："+err.Error(), false)
		return
	}
	e.setFreshState(today, now.Unix(), "增量对账进行中", true)
	go e.freshnessWatch(ctx, r, today)
}

// freshnessWatch 盯住保鲜任务：窗口结束强制停止；扫描完成后接健康巡检（限批）。
func (e *Engine) freshnessWatch(ctx context.Context, r config.QuietRange, today string) {
	poll := time.NewTicker(30 * time.Second)
	defer poll.Stop()
	for e.IsRunning() {
		select {
		case <-ctx.Done():
			e.StopScan()
			return
		case <-poll.C:
			if !r.Contains(time.Now()) {
				applog.Info("定时唤醒保鲜：授权时段结束，停止扫描，放手让睡")
				e.StopScan()
				e.setFreshState(today, time.Now().Unix(), "授权时段结束，扫描已停止（下次窗口继续）", false)
				return
			}
		}
	}
	// 扫描收尾（完成或被停止）。窗口还有余量就补一轮小批量健康巡检。
	now := time.Now()
	if !r.Contains(now) {
		e.setFreshState(today, now.Unix(), "授权时段结束，健康巡检顺延至明天", false)
		return
	}
	e.setFreshState(today, now.Unix(), "健康巡检进行中", true)
	batch := e.cfg.Get().BrokenAutoBatch
	if batch <= 0 {
		batch = 200
	}
	if err := e.StartHealth(health.KindBroken, HealthOptions{Limit: batch, Auto: true}); err != nil {
		e.setFreshState(today, now.Unix(), "健康巡检未启动："+err.Error(), false)
		return
	}
	e.setFreshState(today, now.Unix(), "保鲜完成", false)
}

// ioAutoPause 闲时任务磁盘让路自动暂停（v1.32）。
// 仅当当前未暂停时生效；用户手动暂停优先级更高，不会被自动恢复覆盖。
func (e *Engine) ioAutoPause() {
	if e.userPaused.Load() {
		return
	}
	e.ioAuto.Store(true)
	e.userPaused.Store(true)
	e.Bus.Publish(Event{Type: "update"})
	applog.Info("闲时任务自动让路：磁盘出现持续读写，暂停至恢复空闲")
}

// ioAutoResume 磁盘恢复空闲，继续闲时任务。手动暂停不被覆盖。
func (e *Engine) ioAutoResume() {
	if !e.ioAuto.Swap(false) {
		return
	}
	if e.manualPause.Load() {
		return // 用户在让路期间手动暂停，保持暂停
	}
	e.userPaused.Store(false)
	e.wakePaused()
	applog.Info("磁盘恢复空闲，闲时任务继续")
}

// IOLetWayMonitor 盯住闲时健康巡检期间的磁盘活跃度：
// 出现持续读写立即自动暂停（任务在检查点冻结），恢复空闲自动续跑。
func (e *Engine) IOLetWayMonitor(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for e.HealthRunning() {
		select {
		case <-ctx.Done():
			e.ioAutoResume()
			return
		case <-t.C:
		}
		if e.diskBusy.Load() {
			e.ioAutoPause()
		} else {
			e.ioAutoResume()
		}
	}
	e.ioAutoResume()
}
