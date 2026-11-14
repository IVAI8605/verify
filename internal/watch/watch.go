// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package watch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"verify/internal/applog"
	"verify/internal/config"
	"verify/internal/engine"

	"github.com/fsnotify/fsnotify"
)

// Watcher 递归监控所有存储卷，实时更新查重索引。
type Watcher struct {
	e   *engine.Engine
	cfg *config.Manager
	fw  *fsnotify.Watcher

	mu       sync.Mutex
	roots    map[string]bool
	pending  map[string]time.Time
	pollOnly bool
	closed   bool
	// dirtyAll 表示有事件被丢弃（待处理表溢出或 inotify 队列溢出），
	// 明细已不可得，只能靠一次全量扫描补回；只有补偿扫描真正启动后才清除。
	dirtyAll bool
	// lastCompWarn 限制补偿失败日志的频率，避免扫描长期不可用刷屏。
	lastCompWarn time.Time
}

// maxWatchDirs 限制单卷初始递归监控的目录数量；超过后改用轮询兜底，
// 避免在超大容量卷上耗尽 inotify 配额。
const maxWatchDirs = 200000

func New(e *engine.Engine, cfg *config.Manager) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		e:       e,
		cfg:     cfg,
		fw:      fw,
		roots:   map[string]bool{},
		pending: map[string]time.Time{},
	}
	// 初始递归挂载可能耗时数分钟甚至更久（大容量卷目录数量巨大），
	// 必须放到后台，否则会阻塞 Web UI 启动。
	go w.watchInitialRoots()
	go w.loop()
	go w.debounceLoop()
	go w.periodicLoop()
	return w, nil
}

func (w *Watcher) watchInitialRoots() {
	// 关闭监控时不做递归挂载；之后用户重新启用时由 periodicLoop 补挂。
	if !w.cfg.Get().Watch {
		return
	}
	for _, vol := range w.e.Volumes() {
		if w.isClosed() {
			return
		}
		w.watchRoot(vol)
	}
}

func (w *Watcher) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

func (w *Watcher) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	// inotify 句柄关闭失败无可补救动作，只影响退出时的资源释放。
	_ = w.fw.Close()
}

func (w *Watcher) isExcluded(path string) bool {
	return w.e.IsExcludedPath(path)
}

func (w *Watcher) watchRoot(root string) bool {
	w.mu.Lock()
	// 关闭后不再新增监控：此时 fw 已关闭，Add 会一直失败并空跑整棵目录树。
	if w.closed {
		w.mu.Unlock()
		return false
	}
	if w.roots[root] {
		w.mu.Unlock()
		return true
	}
	w.roots[root] = true
	w.mu.Unlock()

	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && w.isExcluded(path) {
			return filepath.SkipDir
		}
		// 边遍历边检查上限：inotify 配额通常在遍历完成前就会耗尽。
		if count >= maxWatchDirs {
			w.setPollOnly()
			return filepath.SkipAll
		}
		if err := w.fw.Add(path); err == nil {
			count++
		} else if errors.Is(err, syscall.ENOSPC) {
			w.setPollOnly()
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		// 递归中部分目录无法访问（权限）属预期，逐条记录但不中断整卷挂载
		applog.Warn("监控递归跳过不可访问目录（%s 下）：%v", root, err)
	}
	return true
}

func (w *Watcher) setPollOnly() {
	w.mu.Lock()
	w.pollOnly = true
	w.mu.Unlock()
}

func (w *Watcher) loop() {
	for {
		select {
		case ev, ok := <-w.fw.Events:
			if !ok {
				return
			}
			// 关闭监控时持续排空事件队列，避免 fsnotify 缓冲积压；
			// 配置无需重建 watcher 即可动态生效。
			if !w.cfg.Get().Watch {
				continue
			}
			if w.isExcluded(ev.Name) {
				continue
			}
			switch ev.Op {
			case fsnotify.Create:
				if fi, err := os.Lstat(ev.Name); err == nil && fi.IsDir() {
					// 递归挂载一棵新目录可能要遍历很久，放后台避免阻塞事件循环：
					// 同步执行期间所有卷的事件都堵在 fsnotify 缓冲里，容易直接溢出。
					go w.watchRoot(ev.Name)
				}
				w.queue(ev.Name)
			case fsnotify.Write, fsnotify.Rename, fsnotify.Remove:
				w.queue(ev.Name)
			}
		case err, ok := <-w.fw.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				// inotify 内核队列溢出意味着有事件根本没送到这里，
				// 只能整库补扫；不能像以前那样只把错误丢掉。
				applog.Warn("目录监控事件队列溢出，将补一次全量扫描：%v", err)
				w.mu.Lock()
				w.dirtyAll = true
				w.mu.Unlock()
				continue
			}
			applog.Warn("目录监控出错（继续运行）：%v", err)
		}
	}
}

// maxPending 限制待处理事件数量，超出后改为触发一次全量扫描，避免事件风暴积压。
const maxPending = 3000

func (w *Watcher) queue(path string) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	if len(w.pending) >= maxPending {
		// 明细丢弃换取内存有界，但必须记下「有事件被丢」：
		// 否则溢出期间的增删改永远不会进索引（P1-3）。
		w.pending = map[string]time.Time{}
		w.dirtyAll = true
		w.mu.Unlock()
		return
	}
	w.pending[path] = time.Now().Add(1500 * time.Millisecond)
	w.mu.Unlock()
}

func (w *Watcher) debounceLoop() {
	t := time.NewTicker(400 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		w.mu.Lock()
		closed := w.closed
		dirty := w.dirtyAll
		var due []string
		now := time.Now()
		for p, at := range w.pending {
			if now.After(at) {
				due = append(due, p)
			}
		}
		for _, p := range due {
			delete(w.pending, p)
		}
		w.mu.Unlock()
		if closed {
			return
		}
		if dirty {
			w.compensate()
		}
		for _, p := range due {
			w.e.UpdatePath(p)
		}
	}
}

// compensate 在事件被丢弃后补一次全量扫描。
// 扫描正在跑时不重复触发——那一次扫描读的是当前磁盘状态，已经涵盖被丢的事件；
// 启动失败则保留 dirtyAll，等下一轮 tick 重试，不能像旧代码那样一次失败就永久丢失。
func (w *Watcher) compensate() {
	if !w.cfg.Get().Watch {
		// 用户已关闭实时监控，补偿扫描也失去意义。
		w.mu.Lock()
		w.dirtyAll = false
		w.mu.Unlock()
		return
	}
	// 用 IsRunning 而不是 DuringScan：State.Phase 的零值是空串而非 "idle"，
	// 进程刚启动、还没扫过任何一次时 DuringScan 会误判为「扫描中」，补偿永远不触发。
	if w.e.IsRunning() {
		return
	}
	if err := w.e.StartScan(); err != nil {
		w.mu.Lock()
		warn := time.Since(w.lastCompWarn) >= 30*time.Second
		if warn {
			w.lastCompWarn = time.Now()
		}
		w.mu.Unlock()
		if warn {
			applog.Warn("监控事件丢失后的补扫启动失败，稍后重试：%v", err)
		}
		return
	}
	w.mu.Lock()
	w.dirtyAll = false
	w.mu.Unlock()
	applog.Info("监控事件曾被丢弃，已补一次全量扫描")
}

func (w *Watcher) periodicLoop() {
	// 卷探测只是发现新挂载的卷；休眠友好模式下把频率从 60 秒降到 10 分钟，
	// 避免为了一个几乎不会变化的事实反复唤醒磁盘。
	// v2.5.0：引擎正在扫描时临时解除休眠友好，让卷探测及时响应新挂载。
	volEvery := 60 * time.Second
	if w.cfg.Get().SleepFriendly && !w.e.DuringScan() {
		volEvery = 10 * time.Minute
	}
	volTicker := time.NewTicker(volEvery)
	defer volTicker.Stop()
	pollEvery := time.Duration(w.cfg.Get().PollMinutes) * time.Minute
	if pollEvery <= 0 {
		pollEvery = 10 * time.Minute
	}
	pollTicker := time.NewTicker(pollEvery)
	defer pollTicker.Stop()

	for {
		select {
		case <-volTicker.C:
			if w.isClosed() {
				return
			}
			if !w.cfg.Get().Watch {
				continue
			}
			vols := w.e.Volumes()
			// 卷卸载后把根从已挂载集合里移除，重新挂载时才会再走一遍递归 Add；
			// 只在卷列表非空时清理，避免 Volumes() 临时取空把全部根清掉。
			if len(vols) > 0 {
				live := make(map[string]bool, len(vols))
				for _, v := range vols {
					live[v] = true
				}
				w.mu.Lock()
				for r := range w.roots {
					if !live[r] {
						delete(w.roots, r)
					}
				}
				w.mu.Unlock()
			}
			for _, v := range vols {
				w.mu.Lock()
				known := w.roots[v]
				w.mu.Unlock()
				if !known {
					w.watchRoot(v)
					// 休眠友好/静默时段不主动扫描（v1.18 修复②，照 pollTicker 门禁写法）
					if cfg := w.cfg.Get(); cfg.AutoScanAllowed(time.Now()) {
						_ = w.e.StartScan()
					} else {
						applog.Info("新挂载卷跳过扫描：休眠友好模式或处于静默时段")
					}
				}
			}
		case <-pollTicker.C:
			w.mu.Lock()
			pollOnly := w.pollOnly
			closed := w.closed
			w.mu.Unlock()
			if closed {
				return
			}
			if pollOnly && w.cfg.Get().Watch {
				cfg := w.cfg.Get()
				if !cfg.AutoScanAllowed(time.Now()) {
					// 休眠友好或静默时段：跳过兜底轮询触发的全量扫描
					continue
				}
				_ = w.e.StartScan()
			}
		}
	}
}
