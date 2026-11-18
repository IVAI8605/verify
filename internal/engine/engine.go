// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
	"verify/internal/apperr"
	"verify/internal/applog"
	"verify/internal/config"
	"verify/internal/fname"
	"verify/internal/hashx"
	"verify/internal/store"
	"verify/internal/taskguard"
	"verify/internal/trashvol"
	"verify/internal/video"
)

// State 是扫描/分析进度。
type State struct {
	Phase      string `json:"phase"`
	Mode       string `json:"mode"`
	Running    bool   `json:"running"`
	Total      int64  `json:"total"`
	Done       int64  `json:"done"`
	Current    string `json:"current"`
	Message    string `json:"message"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at"`
	LastScan   int64  `json:"last_scan"`
	LastError  string `json:"last_error"`
	ErrCount   int64  `json:"err_count"`

	// v2.0.0：本次扫描的枚举规模与跳过明细（内存态，进程重启即清空）。
	ScannedFiles int64  `json:"scanned_files"`
	ScannedBytes int64  `json:"scanned_bytes"`
	Skipped      int64  `json:"skipped"`
	SkippedDirs  int64  `json:"skipped_dirs"`
	SkipReason   string `json:"skip_reason"`
	// v2.2.0：本次扫描范围的文件类型分布（按扩展名聚合，内存态）。
	Types []TypeStat `json:"types"`
}

type Engine struct {
	st  *store.Store
	cfg *config.Manager
	Bus *Bus
	// runner 用原子指针持有视频运行器（v3.10.4）：保存设置时整体替换快照，
	// 检测/扫描/转码等读方通过 RunnerSnapshot 取，不再原地改字段造成 data race。
	runner  atomic.Pointer[video.Runner]
	AppDest string

	mu          sync.Mutex
	state       State
	running     bool
	cancel      context.CancelFunc
	lastPublish time.Time

	vmu         sync.Mutex
	videoGroups []store.Group
	videoAt     time.Time
	videoKey    string
	ver         int64

	imu         sync.Mutex
	introGroups []store.Group
	introAt     time.Time
	introKey    string

	videoJobs   chan int64
	videoSetMu  sync.Mutex
	videoQueued map[int64]bool
	workerStop  chan struct{}

	// baseCtx 是后台任务（文件监控、视频队列）的根上下文，进程退出时统一取消。
	baseCtx    context.Context
	baseCancel context.CancelFunc

	// hs 是文件健康检测的运行态，与查重状态完全隔离。
	hs healthState

	// stats 是本次扫描的跳过统计（v2.0.0），每次 runScan 开始时清零。
	stats scanStats

	// 闲时巡检相关：最后活动时间、磁盘活跃度、对外状态
	lastActivity atomic.Int64
	disk         *DiskMonitor
	tq           TranscodeQueue

	// userPaused 用户手动暂停标志（v1.21 自由暂停）：置位后各长任务在
	// 批/文件边界协作式暂停（不再起新批，当前批跑完冻结），恢复后从断点继续。
	userPaused atomic.Bool
	// manualPause 区分"用户手动暂停"与"闲时任务磁盘让路自动暂停"（v1.32）：
	// 让路自动恢复不得覆盖用户的手动暂停意图。
	manualPause atomic.Bool
	// ioAuto 当前暂停是否由磁盘让路触发（v1.32）。
	ioAuto    atomic.Bool
	pauseWake chan struct{}
	// pauseMu 保护 pauseWake 的关闭与重建（v3.10.3 改成广播唤醒）。
	pauseMu  sync.Mutex
	diskBusy atomic.Bool
	ap       autoProbeState
	fw       freshnessState
	// cleanupRun 保证同一时间只执行一条清理计划（v3.2.0）。
	cleanupRun atomic.Bool
	// indexRepair 标记索引体检正在运行（v3.10.13）：体检会删索引行，
	// 必须与扫描 / 健康检测 / 所有改文件写操作互斥，否则会删掉改名窗口里的旧行。
	indexRepair atomic.Bool
	// opMu 串行化所有会改动文件系统的写操作（v3.10.10，第五轮 P1-5）：
	// 批量改名（含 P0-1 的两份预览计划互插）、隔离 / 恢复 / 永久删除 / 移动。
	// 没有它时，两个标签各勾同组一个文件，各自的「至少保留一份」都看到对方还在，
	// 于是整组两份都被移走。
	opMu sync.Mutex
	fz   fuzzyCache
	sm   semanticCache
	// heavySem 限制全进程同时进行的重任务单元（v3.10.10，第七轮 P1-5）：
	// 扫描哈希、视频/片头分析、健康探测各有自己的 worker 池，最坏叠加 20+ 路。
	// 共享槽位压到 CPU 核数；健康探测拿不到槽位时等待，而不是被 CPU 饿到超时误报。
	heavySem chan struct{}
	// ffGate 让 ffmpeg 类任务与损坏探测互斥（v3.12.0）：槽位只限总并发，
	// 管不住已经跑起来的 ffmpeg 吃满 CPU，这里让两方不同时开工。
	ffGate *ffmpegGate
	// guard 全局任务闸门（v3.18.0）：长任务启动前声明资源与独占组，
	// 冲突时拒绝启动并说明占用者；/api/tasks 从这里读运行中与最近历史。
	guard *taskguard.Guard
}

func New(st *store.Store, cfg *config.Manager, bus *Bus, appDest string) *Engine {
	e := &Engine{
		st:          st,
		cfg:         cfg,
		Bus:         bus,
		AppDest:     appDest,
		videoJobs:   make(chan int64, 2048),
		videoQueued: map[int64]bool{},
		workerStop:  make(chan struct{}),
		pauseWake:   make(chan struct{}),
		heavySem:    make(chan struct{}, heavySlots()),
		ffGate:      newFFmpegGate(),
	}
	e.baseCtx, e.baseCancel = context.WithCancel(context.Background())
	e.guard = taskguard.New(func() bool { return e.cfg.Get().TaskGuardEnabled })
	e.disk = NewDiskMonitor()
	c := cfg.Get()
	r := video.NewRunner(c.FFmpeg, c.FFprobe)
	r.UseHW = c.HardwareDecode
	r.Resolve(appDest)
	e.runner.Store(r)
	// v3.10.10（第六轮 P1-4）：以前这四个迁移的错误被 `_ =` 吞掉，升级后
	// 指纹维度/窗口/算法没换成新口径时，日志和界面都看不出来，旧结果会继续参与分组。
	if err := st.MigrateFingerprintDim(e.baseCtx, video.Dim); err != nil {
		e.migrateFailed("音频指纹维度", err)
	}
	if err := st.MigrateAudioWindow(e.baseCtx, c.AudioWindowSeconds); err != nil {
		e.migrateFailed("音频指纹窗口", err)
	}
	if err := st.MigrateVideoAlgo(e.baseCtx, video.AlgoVersion); err != nil {
		e.migrateFailed("视频指纹算法", err)
	}
	// v3.9.11：损坏判定规则升级后作废旧误报缓存，否则健康页会继续显示旧结论
	if n, err := st.MigrateProbeAlgo(e.baseCtx, store.ProbeAlgoVersion); err != nil {
		e.migrateFailed("损坏探测规则", err)
	} else if n > 0 {
		applog.Info("损坏探测规则升级：已作废旧缓存 %d 条（只清 broken/timeout，健康结论保留）", n)
	}
	// v3.3.0：启动时读取偏好反哺开关并重算权重（记录很小，同步读完再启动调度器）。
	e.LoadRetainProfile(e.baseCtx)
	go e.videoWorker()
	// v3.10.10（第五轮 P1-1）：快照以前只写不读，崩溃留下的未完成 run 没人识别。
	// v3.10.12（P1-03）：先按 pending 意图行校正崩溃残留的改名，再统一报告其他未完成项。
	go e.startupRecover()
	return e
}

func (e *Engine) Close() {
	e.baseCancel()
	close(e.workerStop)
}

// lockWrites 获取全进程写锁，返回释放函数（v3.10.10，第五轮 P1-5）。
// 「同组至少保留一份」的校验与随后的移动必须在同一临界区内，
// 否则另一个标签的写请求可以插在两者之间，把同组最后一份也移走。
func (e *Engine) lockWrites() func() {
	e.opMu.Lock()
	return e.opMu.Unlock
}

// acquireTask 登记一个长任务（v3.18.0）。闸门关闭时返回空释放函数，
// 行为完全回到 v3.15。返回的 done 必须且只能调用一次（建议 defer），
// 否则独占槽会一直占着，之后所有独占任务都会被拒绝。
func (e *Engine) acquireTask(kind taskguard.Kind, name, group string, res ...taskguard.Resource) (func(string), error) {
	if e.guard == nil {
		return func(string) {}, nil
	}
	return e.guard.Acquire(taskguard.Task{Kind: kind, Name: name, Group: group, Resources: res})
}

// TaskSnapshot 返回运行中任务与最近历史，供 /api/tasks 与状态区使用。
func (e *Engine) TaskSnapshot() (running []taskguard.Task, history []taskguard.Record) {
	if e.guard == nil {
		return nil, nil
	}
	return e.guard.Running(), e.guard.History()
}

// BusyExclusiveTask 返回当前占用独占槽的任务（没有则 false）。
func (e *Engine) BusyExclusiveTask() (taskguard.Task, bool) {
	if e.guard == nil {
		return taskguard.Task{}, false
	}
	return e.guard.BusyGroup()
}

// IndexRepairRunning 表示索引体检是否正在运行（v3.10.13）。
// 供 Web 入口与健康检测 / 扫描启动前判互斥，避免体检期间索引被并发改写。
func (e *Engine) IndexRepairRunning() bool {
	return e.indexRepair.Load()
}

// detachCtx 返回不受请求取消影响的上下文（v3.10.10，第五轮 P1-3）。
// 磁盘改名 / 移动已经成功后才写审计与索引：此时用户关页面、断网都会取消请求 ctx，
// 若继续用它，审计会写不进去（失去撤销入口），索引也停在旧路径。
// 30 秒是这类小事务的兜底上限，避免数据库异常时 goroutine 永久挂住。
func detachCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
}

// migrateFailed 记录启动迁移失败（v3.10.10，第六轮 P1-4）。
// 迁移没成功意味着本次启动仍用旧口径，结果不可信，必须让日志与状态页都能看到。
func (e *Engine) migrateFailed(name string, err error) {
	msg := fmt.Sprintf("%s迁移失败（%v），本次启动沿用旧口径，结果可能不可信", name, err)
	applog.Warn("%s", msg)
	e.updateState(func(s *State) { s.LastError = msg })
}

// RunnerReload 按当前配置重建视频运行器并原子替换。
// v3.10.4：不再原地修改 Runner 字段——检测/扫描/转码正在进行时保存设置
// 会触发 data race；整体换快照后，正在跑的旧任务继续用旧值，新任务拿新值。
func (e *Engine) RunnerReload() {
	c := e.cfg.Get()
	next := video.NewRunner(c.FFmpeg, c.FFprobe)
	next.UseHW = c.HardwareDecode
	next.Resolve(e.AppDest)
	e.runner.Store(next)
}

// RunnerSnapshot 返回当前视频运行器快照，可安全并发读取。
func (e *Engine) RunnerSnapshot() *video.Runner {
	return e.runner.Load()
}

// RunnerAvailable 表示当前已定位到可用的 ffmpeg。
func (e *Engine) RunnerAvailable() bool {
	r := e.runner.Load()
	return r != nil && r.FFmpeg != ""
}

func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

// IsRunning 并发安全地返回扫描是否在进行中（v3.10.8）。
// e.running 由 e.mu 保护，清理计划、定时保鲜等其它协程不得直接读字段，否则是 data race。
func (e *Engine) IsRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

func (e *Engine) updateState(fn func(*State)) {
	e.mu.Lock()
	fn(&e.state)
	e.mu.Unlock()
}

func (e *Engine) publishProgress(force bool) {
	now := time.Now()
	e.mu.Lock()
	if !force && now.Sub(e.lastPublish) < 300*time.Millisecond {
		e.mu.Unlock()
		return
	}
	e.lastPublish = now
	st := e.state
	e.mu.Unlock()
	e.Bus.Publish(Event{Type: "progress", Data: st})
}

func (e *Engine) bumpVersion() {
	e.st.InvalidateCounts()
	e.vmu.Lock()
	e.ver++
	e.videoGroups = nil
	e.videoAt = time.Time{}
	e.videoKey = ""
	e.vmu.Unlock()
	e.imu.Lock()
	e.introGroups = nil
	e.introAt = time.Time{}
	e.introKey = ""
	e.imu.Unlock()
	e.fz.mu.Lock()
	e.fz.groups = nil
	e.fz.ok = false
	e.fz.mu.Unlock()
	e.sm.mu.Lock()
	e.sm.groups = nil
	e.sm.ok = false
	e.sm.mu.Unlock()
}

// NotifyUpdate 供外部（如缓存清理后）触发计数失效与前端刷新通知。
func (e *Engine) NotifyUpdate() {
	e.bumpVersion()
	e.Bus.Publish(Event{Type: "update"})
}

// Volumes 返回参与扫描的卷列表。
func (e *Engine) Volumes() []string {
	c := e.cfg.Get()
	if len(c.Volumes) > 0 {
		return cleanVolumes(c.Volumes)
	}
	// v1.30.1：统一 VERIFY_ 前缀；DUP_CLEAN_ 为改名前的旧前缀，继续兼容
	env := os.Getenv("VERIFY_SCAN_ROOTS")
	if env == "" {
		env = os.Getenv("DUP_CLEAN_SCAN_ROOTS")
	}
	if env != "" {
		return cleanVolumes(strings.Split(env, string(os.PathListSeparator)))
	}
	matches, _ := filepath.Glob("/vol*")
	return cleanVolumes(matches)
}

func cleanVolumes(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		if st, err := os.Stat(v); err != nil || !st.IsDir() { // #nosec G703 -- v 来自 DUP_CLEAN_SCAN_ROOTS 或 /vol* 通配，均在受管卷范围内
			continue
		}
		seen[v] = true
		out = append(out, filepath.Clean(v))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// IsExcludedPath 判断路径是否位于默认或用户配置的跳过目录中。
func (e *Engine) IsExcludedPath(path string) bool {
	cfg := e.cfg.Get()
	if excludedByPath(path, cfg.ExcludePaths) {
		return true
	}
	excl := excludedDirs(cfg)
	for _, p := range strings.Split(filepath.ToSlash(path), "/") {
		if excl[p] {
			return true
		}
	}
	return false
}

// excludedByPath 判断路径是否位于用户指定的排除目录内（含目录本身）。
// 前缀语义在 pathUnderAny，与保护目录共用。
func excludedByPath(path string, roots []string) bool {
	return pathUnderAny(path, roots)
}

// VolumeOf 返回路径所属的卷根目录。
func (e *Engine) VolumeOf(path string) string {
	return volumeOf(path, e.Volumes())
}

// volumeOf 是 VolumeOf 的纯函数核心：对原始串做最长前缀匹配。
// 注意：只做字符串前缀匹配，不解析 `..`、不解析软链接——
// 面向请求的路径必须经 resolveManagedPath 三步防线，不得直接信任本函数。
func volumeOf(path string, vols []string) string {
	// v3.25.1：前缀匹配逻辑与隔离区卷可信判定共用一份实现，避免两处漂移。
	return trashvol.VolumeOf(path, vols)
}

// scanRootTrusted 判断扫描根是否可信（v3.25.2）：卷未挂载或根不可访问时，
// 本次扫描「没见到文件」不能当作文件已被删除，否则 DeleteMissing 会连标签一起清掉。
// 配置允许把扫描根写成子目录（如 /vol1/media），这类目录与上级同设备是正常的，
// 因此只对顶层 /volN 做挂载点判定；子目录根则上溯到它所在的 /volN 再判。
func scanRootTrusted(root string) (bool, string) {
	// 走 volumeTrustFn 而不是 volumeTrust：与索引体检共用同一个可替换入口，
	// 「卷已卸载」这类状态在临时目录里造不出来，只能靠桩在 CI 上覆盖。
	if ok, reason := volumeTrustFn(root); !ok {
		return false, reason
	}
	vol := topVolume(root)
	if vol == "" || vol == root {
		return true, ""
	}
	if ok, reason := volumeTrustFn(vol); !ok {
		return false, "上级卷 " + vol + " " + reason
	}
	return true, ""
}

// declaredVolumes 返回配置里声明的卷，不做存在性过滤。
// v3.25.2（只读排查 P2-4）：Volumes() 会用 os.Stat 把「目录已消失」的卷剔掉，
// 信任门禁只看 Volumes() 就看不到这类卷，DeleteMissing 仍会删掉它全部索引与标签。
// 这里保留原始声明，供 missingDeclaredVolume 把「声明了但这次扫不到」的卷判成不可信。
func (e *Engine) declaredVolumes() []string {
	c := e.cfg.Get()
	if len(c.Volumes) > 0 {
		return c.Volumes
	}
	env := os.Getenv("VERIFY_SCAN_ROOTS")
	if env == "" {
		env = os.Getenv("DUP_CLEAN_SCAN_ROOTS")
	}
	if env == "" {
		return nil
	}
	return strings.Split(env, string(os.PathListSeparator))
}

// missingDeclaredVolume 返回第一个「配置里声明、本次扫描却没有」且磁盘上已不可访问的卷。
// 返回非空即表示不可信：卷目录整个消失（卸载后挂载点被删、盘被拔掉）时，
// 本次扫描「没见到文件」不能当作文件已被删除，必须跳过 DeleteMissing。
func (e *Engine) missingDeclaredVolume(scanned []string) string {
	for _, raw := range e.declaredVolumes() {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		v = filepath.Clean(v)
		seen := false
		for _, s := range scanned {
			if s == v {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		if _, err := os.Stat(v); err != nil {
			return v + "：卷目录不存在或不可访问"
		}
	}
	return ""
}

// topVolume 从 /vol1/media/x 这类路径取顶层 /volN；不是 /volN 布局时返回空。
func topVolume(p string) string {
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) {
		return ""
	}
	parts := strings.Split(clean, string(os.PathSeparator))
	// 绝对路径切分后首段为空，第二段才是卷名，至少要有 /volN/xx 三段。
	if len(parts) < 3 || !strings.HasPrefix(parts[1], "vol") {
		return ""
	}
	return string(os.PathSeparator) + parts[1]
}

// ValidPath 判断路径是否位于受管卷内且为普通文件。
//
// v1.13.1 安全修复（审查发现 #1，双向量真机确认可利用）：
//   - `..` 遍历向量：VolumeOf 只对原始串做前缀匹配，`/vol1/x/../../etc/shadow`
//     的原始串仍带卷前缀，而 os.Stat 解析 `..` 后落在卷外 → 未授权读取系统文件。
//     防线：filepath.Clean 消解 `..` 后重判前缀。
//   - 软链接逃逸向量：卷内软链接指向卷外文件，os.Stat 跟随目标。
//     防线：EvalSymlinks 解析真实路径后再次重判前缀。
func (e *Engine) ValidPath(path string) bool {
	if path == "" {
		return false
	}
	resolved, ok := resolveManagedPath(path, e.Volumes())
	if !ok {
		return false
	}
	st, err := os.Stat(resolved)
	return err == nil && st.Mode().IsRegular()
}

// ResolveFilePath 与 ValidPath 判定相同，但返回解析后的真实路径（v3.10.10，P2-10）。
// 调用方必须用返回的路径去 open / 生成缓存键：继续用原始串会留下 TOCTOU 窗口，
// 卷内可写进程可在校验与打开之间把路径换成指向卷外的软链接。
func (e *Engine) ResolveFilePath(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	resolved, ok := resolveManagedPath(path, e.Volumes())
	if !ok {
		return "", false
	}
	st, err := os.Stat(resolved)
	if err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	return resolved, true
}

// ValidDirPath 判断路径是否位于受管卷内且为目录（同三步防线）。
// 供「目标目录」类入口使用（如移动文件的目标目录校验）。
func (e *Engine) ValidDirPath(path string) bool {
	if path == "" {
		return false
	}
	resolved, ok := resolveManagedPath(path, e.Volumes())
	if !ok {
		return false
	}
	fi, err := os.Stat(resolved)
	return err == nil && fi.IsDir()
}

// resolveManagedParent 把 dir 解析为「受管卷内的真实目录」，用于目标父目录可能
// 还不存在的场景（恢复隔离文件）。逐级上溯到第一个 Lstat 成功的祖先，
// EvalSymlinks 后重判卷前缀，再把尚不存在的尾部拼回去。
// v3.17.5（F-16）：以前只在 EvalSymlinks 成功时校验父目录，失败（目录已删）
// 直接放行，MkdirAll 会顺着路径里的软链接在卷外建目录。
func (e *Engine) resolveManagedParent(dir string) (string, bool) {
	dir = filepath.Clean(dir)
	var tail []string
	cur := dir
	for {
		if _, err := os.Lstat(cur); err == nil {
			resolved, rerr := filepath.EvalSymlinks(cur)
			if rerr != nil {
				return "", false
			}
			if e.VolumeOf(resolved) == "" {
				return "", false
			}
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// ResolveTrashPath 校验隔离区文件并返回可用于读取的真实路径（v3.1.0）。
// 与 ValidPath 的区别只在于「允许 .verify-trash」：隔离区导出必须读这些文件。
// 仍然保持三道防线（Clean 消解 ..、EvalSymlinks 解析软链接、卷前缀重判），
// 并额外要求路径确实位于 .verify-trash 下，防止借导出接口读取普通文件。
func (e *Engine) ResolveTrashPath(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	vols := e.Volumes()
	if len(vols) == 0 {
		return "", false
	}
	if volumeOf(path, vols) == "" {
		return "", false
	}
	cleaned := filepath.Clean(path)
	if volumeOf(cleaned, vols) == "" || !strings.Contains(cleaned, "/.verify-trash/") {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", false
	}
	if volumeOf(resolved, vols) == "" || !strings.Contains(resolved, "/.verify-trash/") {
		return "", false
	}
	st, err := os.Stat(resolved)
	if err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	return resolved, true
}

// resolveManagedPath 校验 path 指向受管卷内的真实位置，返回解析后的绝对路径；
// 卷外、不存在或逃逸一律返回 ("", false)。面向请求的文件路径入口统一走本函数。
func resolveManagedPath(path string, vols []string) (string, bool) {
	if path == "" || len(vols) == 0 {
		return "", false
	}
	// 第一道：原始串快速拒绝明显卷外请求
	if volumeOf(path, vols) == "" {
		return "", false
	}
	cleaned := filepath.Clean(path)
	// 第二道：Clean 消解 `..` 与多余分隔符后，必须仍在受管卷内
	if volumeOf(cleaned, vols) == "" {
		return "", false
	}
	// 第三道：解析软链接到真实路径（目标不存在即拒绝），真实路径必须仍在受管卷内
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", false
	}
	if volumeOf(resolved, vols) == "" {
		return "", false
	}
	if strings.Contains(resolved, "/.verify-trash/") {
		return "", false
	}
	return resolved, true
}

func excludedDirs(c *config.Config) map[string]bool {
	m := map[string]bool{
		"@eaDir":                    true,
		"#recycle":                  true,
		".git":                      true,
		"node_modules":              true,
		".verify-trash":             true,
		".Trash":                    true,
		"System Volume Information": true,
		"@appdata":                  true,
		"@appconf":                  true,
		"@appcenter":                true,
		"@apphome":                  true,
		"@appmeta":                  true,
		"@appshare":                 true,
		"@apptemp":                  true,
		"@docker":                   true,
	}
	for _, d := range c.ExcludeDirs {
		if d = strings.TrimSpace(d); d != "" {
			m[d] = true
		}
	}
	return m
}

func isVideoExt(ext string) bool {
	for _, v := range videoExtsPublic {
		if ext == v {
			return true
		}
	}
	return false
}

var videoExtsPublic = []string{"mp4", "mkv", "avi", "mov", "wmv", "flv", "m4v", "ts", "mts", "m2ts", "mpg", "mpeg", "rmvb", "rm", "3gp", "webm", "vob", "ogv", "asf", "f4v", "divx", "m2v", "mxf", "dat"}

// ScanMode 区分完整扫描与只找完全相同文件的快速模式。
type ScanMode string

const (
	ScanModeFull  ScanMode = "full"
	ScanModeQuick ScanMode = "quick"
)

func (m ScanMode) Label() string {
	if m == ScanModeQuick {
		return "快速查重"
	}
	return "完整扫描"
}

// StartScan 启动一次完整扫描（枚举 → 预哈希 → BLAKE3 → 视频指纹）。
// PauseTasks 用户手动暂停（v1.21 引入 10.2）：协作式，批/文件边界生效。
func (e *Engine) PauseTasks() {
	if e.userPaused.Swap(true) {
		return
	}
	e.manualPause.Store(true)
	e.ioAuto.Store(false) // 手动暂停接管，让路状态复位
	// 暂停不需要唤醒：正在等待的 goroutine 本来就在 waitIfPaused 里，
	// 它们会在恢复时统一被广播叫醒（v3.10.3）。
	// v3.25.2（P3-7）：把暂停态同步给闸门，让「被占槽拒绝」的提示能说清是「已暂停」而不是「正在运行」。
	e.guard.SetPaused(true)
	applog.Info("任务已手动暂停（当前批完成后冻结）")
	e.Bus.Publish(Event{Type: "update"})
}

// ResumeTasks 恢复：从断点继续（扫描/哈希按 size+mtime 失效机制跳过已完成项）。
func (e *Engine) ResumeTasks() {
	if !e.userPaused.Swap(false) {
		return
	}
	e.manualPause.Store(false)
	e.ioAuto.Store(false)
	// v3.10.3：恢复时广播唤醒所有等待者。旧实现只发一个信号，
	// 并行枚举的其余 goroutine 只能靠 2 秒兜底轮询，恢复最多慢 2 秒。
	e.guard.SetPaused(false)
	e.wakePaused()
	applog.Info("任务已恢复")
	e.Bus.Publish(Event{Type: "update"})
}

// Paused 返回暂停状态（/api/status 用）。
func (e *Engine) Paused() bool { return e.userPaused.Load() }

// DuringScan 返回引擎是否正在扫描/分析（v2.5.0）。
// 用于让 autoprobe 和实时监控在扫描期间临时解除"休眠友好"限制。
// v3.10.9：改用 IsRunning 判定。原先用 Phase != "idle"，但 State 零值 Phase 是空串，
// 进程刚启动、还没扫描过时会被当成「正在扫描」，导致休眠友好被长期绕过。
func (e *Engine) DuringScan() bool {
	return e.IsRunning()
}

// wakePaused 关闭当前唤醒通道并换一条新的，让所有阻塞在 waitIfPaused
// 的 goroutine 同时被唤醒（close 是广播，发送只能叫醒一个）。
func (e *Engine) wakePaused() {
	e.pauseMu.Lock()
	close(e.pauseWake)
	e.pauseWake = make(chan struct{})
	e.pauseMu.Unlock()
}

// waitIfPaused 在批/文件边界调用：暂停期间阻塞，恢复或 ctx 取消后返回。
func (e *Engine) waitIfPaused(ctx context.Context) {
	for e.userPaused.Load() && ctx.Err() == nil {
		e.pauseMu.Lock()
		wake := e.pauseWake
		e.pauseMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-time.After(2 * time.Second): // 兜底轮询，防 wake 丢失
		}
	}
}

func (e *Engine) StartScan() error {
	return e.StartScanMode(ScanModeFull)
}

// StartScanMode 启动指定模式的扫描。
// 快速模式只走预哈希与 BLAKE3 完整校验，跳过 FFmpeg 视频/片头分析，适合“只找完全相同文件”。
func (e *Engine) StartScanMode(mode ScanMode) error {
	if mode != ScanModeQuick {
		mode = ScanModeFull
	}
	// v3.10.8（P1-2）：扫描与清理计划、健康检测双向互斥。
	// 这几项判断放在取 e.mu 之前——HealthRunning 要取 hs.mu，持 e.mu 再取会形成跨锁嵌套。
	if e.cleanupRun.Load() {
		return fmt.Errorf("清理计划正在执行，稍后再试")
	}
	if e.HealthRunning() {
		return fmt.Errorf("文件健康检测进行中，稍后再试")
	}
	// v3.10.13（P1）：索引体检会批量删索引行，扫描收尾会批量 Upsert，
	// 两者并发会让索引与磁盘互相覆盖，这里直接拒绝启动。
	if e.indexRepair.Load() {
		return fmt.Errorf("索引体检进行中，稍后再试")
	}
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return fmt.Errorf("扫描正在进行中")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.running = true
	e.state = State{
		Phase:     "scan",
		Mode:      string(mode),
		Running:   true,
		StartedAt: time.Now().Unix(),
		Message:   "正在枚举文件…",
	}
	e.mu.Unlock()
	// v3.10.11（第七轮 P2-1）：扫描期间冻结概览聚合，避免写路径每批失效缓存后
	// 界面 15 秒轮询就重跑一次全表聚合（NAS 上单次 1.5–2.5 秒）。
	e.st.SetCountsFrozen(true)
	// 先查后置跨了两把锁，反向再查一遍收窄竞态：健康检测恰好在两次检查之间启动时，
	// 这里让它赢，扫描回滚，避免扫描与健康检测同时压满磁盘（v3.10.8）。
	if e.HealthRunning() {
		e.mu.Lock()
		e.running = false
		e.cancel = nil
		// 回滚要整体复位：只清 Running/Phase 会留下 StartedAt 与「正在枚举文件…」，
		// 前端会把一次根本没启动的扫描显示成「已结束的本次扫描」（v3.10.9）。
		e.state = State{Phase: "idle"}
		e.mu.Unlock()
		e.st.SetCountsFrozen(false)
		cancel()
		return fmt.Errorf("文件健康检测进行中，稍后再试")
	}
	// v3.18.0：全局闸门——扫描独占磁盘，转码 / 识别竖横屏 / 剪除在跑时直接拒绝。
	doneGuard, gerr := e.acquireTask(taskguard.KindScan, "", taskguard.GroupIO,
		taskguard.ResCPU, taskguard.ResDiskIO, taskguard.ResDBWrite)
	if gerr != nil {
		e.mu.Lock()
		e.running = false
		e.cancel = nil
		e.state = State{Phase: "idle"}
		e.mu.Unlock()
		e.st.SetCountsFrozen(false)
		cancel()
		return gerr
	}
	go func() {
		defer doneGuard("done")
		e.runScan(ctx, mode)
	}()
	return nil
}

// recomputeGroupTags 重算所有参与分组的标签值（v2.7.0）。
// 单个标签失败只记日志：标签是辅助功能，不能因为它出错让整次扫描看起来失败。
func (e *Engine) recomputeGroupTags() {
	tags, err := e.st.GetVideoTags(e.baseCtx)
	if err != nil {
		applog.Warn("标签重算读取定义失败：%s", apperr.Format(err))
		return
	}
	changed := false
	for _, t := range tags {
		// join_group=false 表示用户明确选了"仅显示"，不用算值
		if !t.JoinGroup {
			continue
		}
		if _, err := e.st.ComputeFileTags(e.baseCtx, t.ID); err != nil {
			applog.Warn("标签「%s」重算失败：%s", t.Name, apperr.Format(err))
			continue
		}
		changed = true
	}
	if changed {
		// 命中数变了，让已经打开的设置页刷新一次
		e.Bus.Publish(Event{Type: "update"})
	}
}

// StopScan 协作式停止当前扫描。
// v2.5.0：点停止后立即把 Phase 置为 cancelling，让 UI 显示"正在停止…"；
// goroutine 真正退出后 defer 里再置为 idle。
func (e *Engine) StopScan() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Phase == "cancelling" || e.state.Phase == "idle" {
		return
	}
	e.state.Phase = "cancelling"
	e.state.Message = "正在停止…"
	cancel := e.cancel
	if cancel != nil {
		cancel()
	}
}

func (e *Engine) runScan(ctx context.Context, mode ScanMode) {
	started := time.Now()
	minSize := e.cfg.Get().MinSize
	e.stats.reset()
	finalMsg := "扫描完成"
	if mode == ScanModeQuick {
		finalMsg = "快速查重完成"
	}
	defer func() {
		e.mu.Lock()
		e.running = false
		e.cancel = nil
		e.state.Running = false
		e.state.Phase = "idle"
		e.state.Current = ""
		e.state.FinishedAt = time.Now().Unix()
		// v1.42.0：取消与正常结束要分开说。用户点「停止」后此前也显示
		// 「扫描完成」，看起来像扫描成功跑完（T084 警告）。
		cancelled := ctx.Err() != nil
		switch {
		case cancelled:
			e.state.Message = "扫描已停止"
		case e.state.LastError != "":
			e.state.Message = mode.Label() + "失败"
		default:
			e.state.Message = finalMsg
		}
		e.stats.applyTo(&e.state, minSize)
		lastErr := e.state.LastError
		e.mu.Unlock()
		// 解冻并失效一次，让界面轮询拿到最终数字（第七轮 P2-1）。
		e.st.SetCountsFrozen(false)
		e.bumpVersion()
		e.publishProgress(true)
		e.Bus.Publish(Event{Type: "update"})
		switch {
		case cancelled:
			applog.Info("%s已停止，用时 %s", mode.Label(), time.Since(started).Round(time.Second))
		case lastErr != "":
			applog.Error("%s结束，存在错误：%s（用时 %s）", mode.Label(), lastErr, time.Since(started).Round(time.Second))
		default:
			applog.Info("%s结束，用时 %s", mode.Label(), time.Since(started).Round(time.Second))
		}
		// v3.11.0：编码异常只在扫描结束时汇总一条，避免逐文件刷日志。
		// 这些文件仍正常入库与参与分组，只是界面会标记「编码异常」并禁用写操作。
		if n := e.stats.badNames.Load(); n > 0 {
			applog.Warn("%s发现 %d 个文件名不是合法 UTF-8（已照常入库，界面会标记为编码异常）", mode.Label(), n)
		}
		// v2.7.0：正常结束才重算标签值，让设置页的命中数跟上这次扫描的结果。
		// 取消或出错时文件表是半更新的，重算没有意义还白占 IO。
		if !cancelled && lastErr == "" {
			e.recomputeGroupTags()
		}
		// v3.10.11（第七轮 P2-4）：扫描是最大的一批写，收尾显式截断 WAL，
		// 否则全量扫描后 verify.db-wal 会长期停在数百 MB。被读事务挡住时
		// 只记日志、不重试，交给 SQLite 下一次自动 checkpoint。
		ckCtx, ckCancel := context.WithTimeout(context.Background(), 15*time.Second)
		busy, ckErr := e.st.CheckpointWAL(ckCtx)
		ckCancel()
		switch {
		case ckErr != nil:
			applog.Warn("扫描结束收尾 WAL 失败：%v", ckErr)
		case busy:
			applog.Info("扫描结束收尾 WAL：仍有未结束的读事务，本次跳过截断")
		}
	}()

	cfg := e.cfg.Get()
	gen, err := e.st.NextGeneration(ctx)
	if err != nil {
		e.fail(fmt.Errorf("初始化扫描失败：%s", apperr.Format(err)))
		return
	}

	vols := e.Volumes()
	if len(vols) == 0 {
		e.fail(fmt.Errorf("未发现可扫描的存储卷（/vol*）"))
		return
	}
	excl := excludedDirs(cfg)
	var batch []store.FileInfo
	var done, errCount int64
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := e.st.UpsertBatch(ctx, batch, gen); err != nil {
			atomic.AddInt64(&errCount, 1)
			e.updateState(func(s *State) { s.LastError = apperr.Format(err) })
		}
		batch = nil
	}

	// v3.10.2：按物理卷并行枚举。不同硬盘各自一个 goroutine，同盘上的多个
	// 挂载点合并成一组串行遍历，避免自己跟自己抢 IO；入库仍由主 goroutine
	// 单线程批量写，SQLite 只有一个写者，并发写只会互相等锁。
	type walkItem struct {
		path string
		info store.FileInfo
	}
	groups := deviceGroups(vols)
	applog.Info("开始%s，扫描根目录 %d 个，按物理卷分 %d 组并行：%s",
		mode.Label(), len(vols), len(groups), strings.Join(vols, "、"))

	items := make(chan walkItem, 4096)
	var walkWG sync.WaitGroup
	for _, group := range groups {
		if len(group) == 0 {
			continue
		}
		walkWG.Add(1)
		go func(group []string) {
			defer walkWG.Done()
			for _, root := range group {
				if ctx.Err() != nil {
					return
				}
				err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if d.IsDir() {
						e.waitIfPaused(ctx) // v1.21：暂停检查点（目录边界）
					}
					if walkErr != nil {
						atomic.AddInt64(&errCount, 1)
						e.stats.readErrors.Add(1)
						return nil
					}
					name := d.Name()
					if d.IsDir() {
						if path != root && excl[name] {
							e.stats.excludedDirs.Add(1)
							return filepath.SkipDir
						}
						// 扫描目录本身也被排除时直接跳过：同一目录同时加入扫描和排除时，以排除为准。
						if excludedByPath(path, cfg.ExcludePaths) {
							e.stats.excludedDirs.Add(1)
							return filepath.SkipDir
						}
						if path != root && strings.HasPrefix(name, ".verify-trash") {
							e.stats.excludedDirs.Add(1)
							return filepath.SkipDir
						}
						return nil
					}
					if !d.Type().IsRegular() {
						e.stats.nonRegular.Add(1)
						return nil
					}
					info, err := d.Info()
					if err != nil {
						atomic.AddInt64(&errCount, 1)
						e.stats.readErrors.Add(1)
						return nil
					}
					e.stats.addScanned(filepath.Ext(path), info.Size())
					// v3.11.0：非法 UTF-8 文件名照常入库，只计数，扫描结束统一报一条。
					if !utf8.ValidString(path) {
						e.stats.badNames.Add(1)
					}
					if info.Size() < cfg.MinSize {
						// 仍入库（与 v1 一致），只是不参与查重；单独计入跳过。
						e.stats.smallFiles.Add(1)
					}
					select {
					case items <- walkItem{path: path, info: fileInfoOf(path, root, info)}:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
				if err != nil && ctx.Err() == nil {
					atomic.AddInt64(&errCount, 1)
					e.updateState(func(s *State) { s.LastError = apperr.Format(err) })
				}
			}
		}(group)
	}
	go func() {
		walkWG.Wait()
		close(items)
	}()

	for it := range items {
		batch = append(batch, it.info)
		done++
		if len(batch) >= 500 {
			flush()
			e.updateState(func(s *State) {
				s.Done = done
				s.Current = it.path
				s.ErrCount = atomic.LoadInt64(&errCount)
				s.ScannedFiles = e.stats.scannedFiles.Load()
				s.ScannedBytes = e.stats.scannedBytes.Load()
				s.Types = e.stats.typeStats()
			})
			e.publishProgress(false)
		}
	}
	flush()
	if ctx.Err() != nil {
		return
	}

	// v3.25.2（只读排查 P1）：卷不可信或本次有读错误时，不能按「这次没见到」清失效索引。
	// 挂载点目录还在、卷已卸载时 WalkDir 会正常返回 0 个文件；DeleteMissing 会连
	// file_tags / probe_cache 一起删掉，连续两次扫描就会永久丢掉用户标签。
	unsafeReason := ""
	for _, vol := range vols {
		if ok, reason := scanRootTrusted(vol); !ok {
			unsafeReason = vol + "：" + reason
			break
		}
	}
	// v3.25.2（只读排查 P2-4）：卷目录整个消失时它根本不在 vols 里，
	// 上面的逐卷判定看不到它，必须再对照配置声明补一刀。
	if unsafeReason == "" {
		unsafeReason = e.missingDeclaredVolume(vols)
	}
	if unsafeReason == "" {
		if bad := e.stats.readErrors.Load(); bad > 0 {
			unsafeReason = fmt.Sprintf("有 %d 个目录或文件读取失败", bad)
		}
	}
	var removed int64
	if unsafeReason != "" {
		applog.Warn("跳过失效索引清理：%s（避免把暂时读不到的文件连同标签一起删掉）", unsafeReason)
	} else {
		removed, _ = e.st.DeleteMissing(ctx, gen)
	}
	e.updateState(func(s *State) {
		s.Done = done
		s.Total = done
		s.Phase = "quick"
		s.Message = "正在计算预哈希…"
		s.LastError = ""
		s.ErrCount = atomic.LoadInt64(&errCount)
		s.ScannedFiles = e.stats.scannedFiles.Load()
		s.ScannedBytes = e.stats.scannedBytes.Load()
		s.Types = e.stats.typeStats()
	})
	e.publishProgress(true)
	// v3.10.3：枚举也纳入分阶段耗时，后续比较「枚举 vs 哈希 vs 视频」才有依据。
	enumElapsed := time.Since(started)
	enumBytes := e.stats.scannedBytes.Load()
	enumRate := 0.0
	if sec := enumElapsed.Seconds(); sec > 0 {
		enumRate = float64(enumBytes) / (1 << 20) / sec
	}
	applog.Info("文件枚举完成：%d 个，%.1f MiB，用时 %s，平均 %.1f MiB/s，本次移除失效记录 %d 个",
		done, float64(enumBytes)/(1<<20), enumElapsed.Round(time.Millisecond), enumRate, removed)

	e.runQuickPass(ctx, cfg)
	if ctx.Err() != nil {
		return
	}
	e.runFullPass(ctx, cfg)
	if ctx.Err() != nil {
		return
	}
	if mode == ScanModeQuick {
		// 快速模式到此为止，不再调用 FFmpeg；结果直接由 BLAKE3 分组。
		e.mu.Lock()
		e.state.LastScan = time.Now().Unix()
		e.mu.Unlock()
		e.publishProgress(true)
		return
	}
	// 图片指纹 pass 在视频分析之前：纯 Go 解码、无 ffmpeg 依赖，失败面小。
	e.RunPhotoPass(ctx, cfg)
	if ctx.Err() != nil {
		return
	}
	e.runVideoPass(ctx, cfg)
	if ctx.Err() != nil {
		return
	}
	e.RunAudioPass(ctx, cfg)
	if ctx.Err() != nil {
		return
	}
	e.runIntroPass(ctx, cfg)

	e.mu.Lock()
	e.state.LastScan = time.Now().Unix()
	e.mu.Unlock()
	e.publishProgress(true)
}

func (e *Engine) fail(err error) {
	e.updateState(func(s *State) {
		s.LastError = apperr.Format(err)
		s.Message = "扫描失败"
	})
	e.publishProgress(true)
}

// saturateInt64 把无符号计数收敛到 int64 可表示范围。
// inode 号与硬链接数理论上可能超出 int64，直接强转会回绕成负数，
// 而负数 inode 会被数据库当作“无意义值”，因此这里饱和到上限。
func saturateInt64(v uint64) int64 {
	const maxInt64 = uint64(1<<63 - 1)
	if v > maxInt64 {
		return int64(maxInt64)
	}
	return int64(v)
}

func fileInfoOf(path, root string, info fs.FileInfo) store.FileInfo {
	mt := info.ModTime().UnixNano()
	nlink := int64(1)
	var inode, dev int64
	ct := mt
	// 默认按逻辑大小，拿不到 st_blocks 时（部分 FUSE 挂载返回 0）至少不会显示成 0 占用。
	alloc := info.Size()
	if s, ok := info.Sys().(*syscall.Stat_t); ok {
		nlink = saturateInt64(s.Nlink)
		inode = saturateInt64(s.Ino)
		// v3.10.10：设备号用于跨扫描根的硬链接去重（同一文件可能挂在两个根下）。
		dev = saturateInt64(s.Dev)
		ct = s.Ctim.Sec*1e9 + s.Ctim.Nsec
		// v3.10.8：稀疏文件的实际占用远小于逻辑大小，可回收空间必须按块数算。
		if b := s.Blocks * 512; b > 0 {
			alloc = b
		}
	}
	return store.FileInfo{
		Path:   path,
		Volume: root,
		Name:   info.Name(),
		Size:   info.Size(),
		Alloc:  alloc,
		Mtime:  mt,
		Ctime:  ct,
		Nlink:  nlink,
		Inode:  inode,
		Dev:    dev,
		Ext:    fname.Ext(info.Name()),
	}
}

// hashSnapshotStillValid 复核文件在「选中→落库」窗口内是否未被改动（v4.0.1 安全审查 问题4）。
// 内容被替换时 mtime 可被回拨，ctime 由内核维护不能伪造。磁盘能给出 ctime 时，库里 ctime
// 必须与之相等——库里 ctime=0（旧行/未采集）同样判为不一致，即「放弃乐观写入」；
// 仅当磁盘也拿不到 ctime（个别 FUSE 挂载）时才退回 size+mtime。返回 false 则不得落库。
func hashSnapshotStillValid(f store.FileRow) bool {
	fi, err := os.Lstat(f.Path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if fi.Size() != f.Size || fi.ModTime().UnixNano() != f.Mtime {
		return false
	}
	if cur, ok := fileCtime(fi); ok && cur > 0 {
		if cur != f.Ctime {
			return false
		}
	}
	return true
}

// fileCtime 取 inode 变更时间（纳秒）；平台不提供 syscall.Stat_t 时 ok=false。
func fileCtime(fi fs.FileInfo) (int64, bool) {
	if s, ok := fi.Sys().(*syscall.Stat_t); ok {
		return s.Ctim.Sec*1e9 + s.Ctim.Nsec, true
	}
	return 0, false
}

// stageMeter 统计单个扫描阶段的文件数、字节数与耗时（v3.10.3）。
//
// 以前只有整次扫描的总用时，看不出枚举、预哈希、全量、视频、片头各占多少，
// 优化只能靠猜。这里统一在阶段结束打一行固定格式日志，方便跨版本与跨磁盘对比。
type stageMeter struct {
	name  string
	start time.Time
	files int64
	bytes int64
}

func newStageMeter(name string) *stageMeter {
	return &stageMeter{name: name, start: time.Now()}
}

func (m *stageMeter) add(size int64) {
	m.files++
	if size > 0 {
		m.bytes += size
	}
}

// log 输出「阶段、文件数、字节数、用时、平均 MiB/s」。太短的阶段按毫秒显示，
// 避免出现 0s 看不出差异。
func (m *stageMeter) log() {
	elapsed := time.Since(m.start)
	mib := float64(m.bytes) / (1 << 20)
	rate := 0.0
	if sec := elapsed.Seconds(); sec > 0 {
		rate = mib / sec
	}
	applog.Info("%s完成：%d 个文件，%.1f MiB，用时 %s，平均 %.1f MiB/s",
		m.name, m.files, mib, elapsed.Round(time.Millisecond), rate)
}

// quickReadBytes 返回预哈希实际读取的字节数（整文件或头尾各 4KB）。
func quickReadBytes(size int64) int64 {
	if size <= hashx.QuickChunk*2 {
		return size
	}
	return hashx.QuickChunk * 2
}

func (e *Engine) runQuickPass(ctx context.Context, cfg *config.Config) {
	if !cfg.ContentEnabled {
		return
	}
	total, _ := e.st.CountNeedingQuick(ctx, cfg.MinSize)
	e.updateState(func(s *State) { s.Phase = "quick"; s.Total = total; s.Done = 0 })
	e.publishProgress(true)
	applog.Info("开始预哈希：待计算 %d 个文件（大小 + 头尾各 4KB）", total)

	meter := newStageMeter("预哈希阶段")
	var done int64
	// v3.10.3：按 path 续扫。以前每批都从头重跑同一条 SQL，文件越多越慢；
	// 续扫后已处理的前缀不再参与，SQL 成本基本恒定。
	after := ""
	retry := 0
	for {
		if ctx.Err() != nil {
			break
		}
		files, err := e.st.FilesNeedingQuick(ctx, cfg.MinSize, after, 300)
		if err != nil {
			e.fail(err)
			return
		}
		if len(files) == 0 {
			break
		}
		prev := after
		after = files[len(files)-1].Path
		for _, f := range files {
			meter.add(quickReadBytes(f.Size))
		}
		if lost := e.hashBatch(ctx, files, true, &done, total); lost > 0 && retry < 2 && ctx.Err() == nil {
			// 落库失败的条目仍是 NULL，回退游标后会被重新选中；已成功的不再入选。
			retry++
			after = prev
			applog.Warn("预哈希落库失败 %d 条，回退游标重试（第 %d 次）", lost, retry)
			continue
		} else if lost == 0 {
			retry = 0
		}
		e.publishProgress(false)
	}
	meter.log()
	if ctx.Err() != nil {
		return
	}
	e.updateState(func(s *State) { s.Done = atomic.LoadInt64(&done) })
	e.publishProgress(true)
	applog.Info("预哈希完成：%d 个文件", atomic.LoadInt64(&done))
}

func (e *Engine) runFullPass(ctx context.Context, cfg *config.Config) {
	if !cfg.ContentEnabled {
		return
	}
	if ctx.Err() != nil {
		return
	}
	// v3.10.3：先把「预哈希重复」集合物化一次，后续每批候选只做主键匹配，
	// 不再每批重跑 GROUP BY HAVING COUNT(*)>1。
	if _, err := e.st.RefreshDupQuick(ctx); err != nil {
		e.fail(fmt.Errorf("准备全量哈希候选失败：%s", apperr.Format(err)))
		return
	}
	total, _ := e.st.CountNeedingFull(ctx, cfg.MinSize)
	e.updateState(func(s *State) {
		s.Phase = "full"
		s.Total = total
		s.Done = 0
		s.Message = "正在计算 BLAKE3 完整校验…"
	})
	e.publishProgress(true)
	applog.Info("开始 BLAKE3 完整校验：预哈希重复候选 %d 个文件", total)

	meter := newStageMeter("BLAKE3 完整校验阶段")
	var done int64
	after := ""
	retry := 0
	for {
		if ctx.Err() != nil {
			break
		}
		files, err := e.st.FilesNeedingFullHash(ctx, cfg.MinSize, after, 300)
		if err != nil {
			e.fail(err)
			return
		}
		if len(files) == 0 {
			break
		}
		prev := after
		after = files[len(files)-1].Path
		for _, f := range files {
			meter.add(f.Size)
		}
		if lost := e.hashBatch(ctx, files, false, &done, total); lost > 0 && retry < 2 && ctx.Err() == nil {
			retry++
			after = prev
			applog.Warn("BLAKE3 落库失败 %d 条，回退游标重试（第 %d 次）", lost, retry)
			continue
		} else if lost == 0 {
			retry = 0
		}
		e.publishProgress(false)
	}
	meter.log()
	if ctx.Err() != nil {
		return
	}
	e.updateState(func(s *State) { s.Done = atomic.LoadInt64(&done) })
	e.publishProgress(true)
	applog.Info("BLAKE3 完整校验完成：%d 个文件", atomic.LoadInt64(&done))
}

func hashStageName(quick bool) string {
	if quick {
		return "预哈希"
	}
	return "BLAKE3 校验"
}

// hashBatch 计算一批文件的哈希并落库，返回落库失败的条数。
//
// v3.10.4：落库失败时把对应条数从 done 里扣回，并让调用方回退游标重试，
// 避免「写库失败但进度照走、游标已越过」导致这一批永远漏算。
func (e *Engine) hashBatch(ctx context.Context, files []store.FileRow, quick bool, done *int64, total int64) int {
	workers := e.cfg.Get().HashWorkers
	if workers < 1 {
		workers = 4
	}
	sem := make(chan struct{}, workers)
	resultCh := make(chan store.HashUpdate, workers*2)
	var wg sync.WaitGroup
	// v3.2.0（设计文档 §10.1）：worker 只算哈希并投递结果，落库由单独的 flush 协程
	// 每 300 条合并成一个事务；用独立 WaitGroup 保证函数返回前最后一批已提交。
	var flushWg sync.WaitGroup
	var lost int64
	flushWg.Add(1)
	go func() {
		defer flushWg.Done()
		batch := make([]store.HashUpdate, 0, 300)
		flush := func() {
			if len(batch) == 0 {
				return
			}
			var err error
			if quick {
				err = e.st.SetQuickBatch(ctx, batch)
			} else {
				err = e.st.SetHashBatch(ctx, batch)
			}
			// 单批失败只记日志不中断，保持尽力而为语义；条数要扣回并交给调用方重试。
			if err != nil {
				applog.Warn("批量写入哈希失败（%d 条）：%v", len(batch), err)
				atomic.AddInt64(&lost, int64(len(batch)))
				atomic.AddInt64(done, -int64(len(batch)))
			}
			batch = batch[:0]
		}
		for u := range resultCh {
			batch = append(batch, u)
			if len(batch) >= 300 {
				flush()
			}
		}
		flush()
	}()
outerHash:
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		e.waitIfPaused(ctx) // v1.21：暂停检查点（文件边界）
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break outerHash
		}
		wg.Add(1)
		go func(f store.FileRow) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			// v3.10.10（第七轮 P1-5）：哈希是重 IO，先抢全局槽位，避免与视频/健康探测叠加
			if !e.acquireHeavy(ctx) {
				return
			}
			defer e.releaseHeavy()
			var sum string
			var err error
			if quick {
				sum, err = hashx.QuickHash(f.Path, f.Size)
			} else {
				sum, err = hashx.FullHash(f.Path)
			}
			if err != nil {
				applog.Warn("%s失败：%s（%v）", hashStageName(quick), f.Path, err)
				e.stats.hashFailures.Add(1)
				e.updateState(func(s *State) { s.LastError = apperr.Format(err) })
				// v3.10.3：失败不再写 'ERR'。旧写法会让候选查询的 IS NULL 永远
				// 匹配不到它，文件再也不会重算；保留 NULL + 本轮 path 续扫，
				// 既不会在本次扫描里反复选中，也能在下一轮自动重试。
				n := atomic.AddInt64(done, 1)
				e.updateState(func(s *State) {
					s.Done = n
					s.Total = total
					s.Current = f.Path
				})
				return
			}
			// v4.0.1（安全审查 问题4）：落库前重新 Lstat 复核一次。文件若在「选中→落库」窗口内
			// 被换成同大小、且 mtime 被回拨的内容，旧哈希不能写到新内容上，否则候选查询会把
			// 两个不同内容的文件判成同一组，进而被误删。复核不通过就跳过写库，留 NULL 下轮重算。
			if !hashSnapshotStillValid(f) {
				applog.Warn("哈希落库前复核发现文件已改动，跳过写入（下轮重算）：%s", f.Path)
				n := atomic.AddInt64(done, 1)
				e.updateState(func(s *State) {
					s.Done = n
					s.Total = total
					s.Current = f.Path
				})
				return
			}
			// 成功结果投递，由 flush 协程统一落库；ctx 取消时丢弃本条，
			// 下次扫描会重新计算，避免在取消后继续写库。
			select {
			case resultCh <- store.HashUpdate{ID: f.ID, Size: f.Size, Mtime: f.Mtime, Ctime: f.Ctime, Value: sum}:
			case <-ctx.Done():
				return
			}
			n := atomic.AddInt64(done, 1)
			e.updateState(func(s *State) {
				s.Done = n
				s.Total = total
				s.Current = f.Path
			})
			e.publishProgress(false)
		}(f)
	}
	wg.Wait()
	close(resultCh) // 必须先等所有 worker 退出再关闭，否则会向已关闭通道发送
	flushWg.Wait()
	return int(atomic.LoadInt64(&lost))
}

func (e *Engine) runVideoPass(ctx context.Context, cfg *config.Config) {
	if !cfg.VideoEnabled {
		return
	}
	e.RunnerReload()
	if !e.RunnerSnapshot().Available() {
		e.updateState(func(s *State) { s.Message = "未找到 ffmpeg/ffprobe，已跳过视频分析" })
		e.publishProgress(true)
		return
	}
	total, err := e.st.CountVideoCandidates(ctx)
	if err != nil {
		// 计数失败不能当成「没有候选」静默跳过，否则整段视频分析会无声消失。
		e.fail(fmt.Errorf("统计视频候选失败：%s", apperr.Format(err)))
		return
	}
	if total == 0 {
		return
	}
	e.updateState(func(s *State) {
		s.Phase = "video"
		s.Total = total
		s.Done = 0
		s.Message = "正在分析视频指纹…"
	})
	e.publishProgress(true)

	workers := cfg.VideoWorkers
	if workers < 1 || workers > 4 {
		workers = 2
	}
	results := make(chan store.VideoResult, 128)
	flushCh := make(chan chan struct{})
	writerDone := make(chan struct{})
	var done, lost int64
	go e.videoBatchWriter(results, flushCh, writerDone, &done, &lost)
	closed := false
	flushAndClose := func() {
		if closed {
			return
		}
		closed = true
		close(results)
		<-writerDone
	}
	defer flushAndClose()

	// v3.10.3：按 path 续扫，已分析的前缀不再重复查询。
	after := ""
	retry := 0
	meter := newStageMeter("视频指纹阶段")
	for {
		if ctx.Err() != nil {
			break
		}
		cands, err := e.st.VideoCandidates(ctx, after, 40)
		if err != nil {
			e.fail(err)
			return
		}
		if len(cands) == 0 {
			break
		}
		prev := after
		after = cands[len(cands)-1].Path
		lostBefore := atomic.LoadInt64(&lost)
		for _, f := range cands {
			meter.add(f.Size)
		}
		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup
	outerVideo:
		for _, f := range cands {
			if ctx.Err() != nil {
				break
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				break outerVideo
			}
			wg.Add(1)
			go func(f store.FileRow) {
				defer wg.Done()
				defer func() { <-sem }()
				// 等待槽位不计入 4 分钟分析超时，宁可排队也不要因 CPU 争抢而失败
				// v3.12.0：先拿 ffmpeg 互斥位，再拿重任务槽位。顺序不能反——
				// 健康探测若握着槽位等 ffmpeg 结束，反过来 ffmpeg 又在等槽位，就会死锁。
				if !e.acquireFFmpegTask(ctx) {
					return
				}
				defer e.releaseFFmpegTask()
				if !e.acquireHeavy(ctx) {
					return
				}
				defer e.releaseHeavy()
				cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
				fp, ferr := e.RunnerSnapshot().Fingerprint(cctx, f.Path, cfg.IgnoreIntroSeconds, cfg.IgnoreOutroSeconds, cfg.VideoDensity)
				cancel()
				res := store.VideoResult{ID: f.ID, Size: f.Size, Mtime: f.Mtime, Ctime: f.Ctime}
				if ferr != nil {
					res.Err = ferr.Error()
				} else {
					res.Audio = fp.Audio
					res.Thumb = fp.Thumbs
					res.Duration = fp.Media.Duration
					res.MediaMeta = mediaMeta(fp.Media)
				}
				select {
				case results <- res:
				case <-ctx.Done():
				}
				n := atomic.AddInt64(&done, 1)
				e.updateState(func(s *State) {
					s.Done = n
					s.Current = f.Path
				})
				e.publishProgress(false)
			}(f)
		}
		wg.Wait()
		// 等待本批结果落库后再查询下一批，避免已分析文件因未及时落库被重复选中。
		waitFlush := make(chan struct{})
		select {
		case flushCh <- waitFlush:
			<-waitFlush
		case <-ctx.Done():
			return
		}
		if atomic.LoadInt64(&lost) > lostBefore && retry < 2 && ctx.Err() == nil {
			// 落库失败的条目没写 video_at，回退游标后会被重新选中。
			retry++
			after = prev
			applog.Warn("视频分析结果落库失败，回退游标重试（第 %d 次）", retry)
			continue
		}
		retry = 0
		e.bumpVersion()
	}
	flushAndClose()
	meter.log()
	e.updateState(func(s *State) { s.Done = atomic.LoadInt64(&done) })
	e.bumpVersion()
	e.publishProgress(true)
}

// runIntroPass 按当前片头长度 N 计算开头 N 秒的音频 + 帧指纹。
func (e *Engine) runIntroPass(ctx context.Context, cfg *config.Config) {
	if !cfg.IntroEnabled {
		return
	}
	e.RunnerReload()
	if !e.RunnerSnapshot().Available() {
		e.updateState(func(s *State) { s.Message = "未找到 ffmpeg/ffprobe，已跳过片头分析" })
		e.publishProgress(true)
		return
	}
	n := cfg.IntroSeconds
	if n < 1 || n > 300 {
		n = 10
	}
	total, err := e.st.CountIntroCandidates(ctx, n)
	if err != nil {
		e.fail(fmt.Errorf("统计片头候选失败：%s", apperr.Format(err)))
		return
	}
	if total == 0 {
		return
	}
	e.updateState(func(s *State) {
		s.Phase = "intro"
		s.Total = total
		s.Done = 0
		s.Message = "正在分析相同片头…"
	})
	e.publishProgress(true)

	workers := cfg.VideoWorkers
	if workers < 1 || workers > 4 {
		workers = 2
	}
	results := make(chan store.IntroResult, 128)
	flushCh := make(chan chan struct{})
	writerDone := make(chan struct{})
	var done, lost int64
	go e.introBatchWriter(results, flushCh, writerDone, &done, &lost)
	closed := false
	flushAndClose := func() {
		if closed {
			return
		}
		closed = true
		close(results)
		<-writerDone
	}
	defer flushAndClose()

	// v3.10.3：按 path 续扫，已分析的前缀不再重复查询。
	after := ""
	retry := 0
	meter := newStageMeter("片头分析阶段")
	for {
		if ctx.Err() != nil {
			break
		}
		cands, err := e.st.IntroCandidates(ctx, n, after, 40)
		if err != nil {
			e.fail(err)
			return
		}
		if len(cands) == 0 {
			break
		}
		prev := after
		after = cands[len(cands)-1].Path
		lostBefore := atomic.LoadInt64(&lost)
		for _, f := range cands {
			meter.add(f.Size)
		}
		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup
	outerIntro:
		for _, f := range cands {
			if ctx.Err() != nil {
				break
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				break outerIntro
			}
			wg.Add(1)
			go func(f store.FileRow) {
				defer wg.Done()
				defer func() { <-sem }()
				// 同上：排队时间不算进片头分析超时
				// v3.12.0：与视频分析一致，先拿 ffmpeg 互斥位再拿槽位。
				if !e.acquireFFmpegTask(ctx) {
					return
				}
				defer e.releaseFFmpegTask()
				if !e.acquireHeavy(ctx) {
					return
				}
				defer e.releaseHeavy()
				cctx, cancel := context.WithTimeout(ctx, introTimeout(n))
				fp, ferr := e.RunnerSnapshot().IntroFingerprint(cctx, f.Path, n)
				cancel()
				res := store.IntroResult{ID: f.ID, Seconds: n, Size: f.Size, Mtime: f.Mtime, Ctime: f.Ctime}
				if ferr != nil {
					res.Err = ferr.Error()
				} else {
					res.Audio = fp.Audio
					res.Thumbs = fp.Thumbs
					res.Duration = fp.Media.Duration
					res.MediaMeta = mediaMeta(fp.Media)
				}
				select {
				case results <- res:
				case <-ctx.Done():
				}
				doneN := atomic.AddInt64(&done, 1)
				e.updateState(func(s *State) {
					s.Done = doneN
					s.Current = f.Path
				})
				e.publishProgress(false)
			}(f)
		}
		wg.Wait()
		waitFlush := make(chan struct{})
		select {
		case flushCh <- waitFlush:
			<-waitFlush
		case <-ctx.Done():
			return
		}
		if atomic.LoadInt64(&lost) > lostBefore && retry < 2 && ctx.Err() == nil {
			// 落库失败的条目没写 intro_at，回退游标后会被重新选中。
			retry++
			after = prev
			applog.Warn("片头分析结果落库失败，回退游标重试（第 %d 次）", retry)
			continue
		}
		retry = 0
		e.bumpVersion()
	}
	flushAndClose()
	meter.log()
	e.updateState(func(s *State) { s.Done = atomic.LoadInt64(&done) })
	e.bumpVersion()
	e.publishProgress(true)
}

// introTimeout 为片头分析设置超时：N 越大（最多 300 秒）解码耗时越长。
func introTimeout(seconds int) time.Duration {
	d := time.Duration(seconds)*2*time.Second + 3*time.Minute
	if d > 15*time.Minute {
		d = 15 * time.Minute
	}
	return d
}

// introBatchWriter 批量落库片头分析结果。
// processed/lost 用于记录落库失败：失败条数会从处理进度里扣回，交给调用方回退重试。
func (e *Engine) introBatchWriter(in <-chan store.IntroResult, flushCh <-chan chan struct{}, done chan<- struct{}, processed, lost *int64) {
	const batchSize = 100
	full := make([]store.IntroResult, 0, batchSize)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	defer close(done)
	flush := func() {
		if len(full) == 0 {
			return
		}
		if err := e.st.SetIntroBatch(e.baseCtx, full); err != nil {
			applog.Warn("片头分析结果落库失败（%d 条）：%v", len(full), err)
			atomic.AddInt64(lost, int64(len(full)))
			atomic.AddInt64(processed, -int64(len(full)))
		}
		full = full[:0]
	}
	for {
		select {
		case r, ok := <-in:
			if !ok {
				flush()
				return
			}
			full = append(full, r)
			if len(full) >= batchSize {
				flush()
			}
		case req := <-flushCh:
			flush()
			close(req)
		case <-ticker.C:
			flush()
		}
	}
}

// videoBatchWriter 批量落库视频分析结果，减少 SQLite 事务与 WAL 刷写开销。
// flushCh 用于让调用方同步等待缓冲区落库，再继续取下一批候选文件。
// processed/lost 的语义与 introBatchWriter 相同（v3.10.4）。
func (e *Engine) videoBatchWriter(in <-chan store.VideoResult, flushCh <-chan chan struct{}, done chan<- struct{}, processed, lost *int64) {
	const batchSize = 100
	full := make([]store.VideoResult, 0, batchSize)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	defer close(done)
	flush := func() {
		if len(full) == 0 {
			return
		}
		if err := e.st.SetVideoBatch(e.baseCtx, full); err != nil {
			applog.Warn("视频分析结果落库失败（%d 条）：%v", len(full), err)
			atomic.AddInt64(lost, int64(len(full)))
			atomic.AddInt64(processed, -int64(len(full)))
		}
		full = full[:0]
	}
	for {
		select {
		case r, ok := <-in:
			if !ok {
				flush()
				return
			}
			full = append(full, r)
			if len(full) >= batchSize {
				flush()
			}
		case req := <-flushCh:
			flush()
			close(req)
		case <-ticker.C:
			flush()
		}
	}
}

func (e *Engine) MarkVideoDirty() { e.bumpVersion() }

// VideoFailures 返回视频分析失败的文件列表。
func (e *Engine) VideoFailures(ctx context.Context) ([]store.VideoFailure, error) {
	return e.st.VideoFailures(ctx)
}

// ReanalyzeVideos 清空视频分析结果并重新分析；ids 为空表示全部视频。
func (e *Engine) ReanalyzeVideos(ctx context.Context, ids []int64) error {
	if _, err := e.st.ResetVideoAnalysis(ctx, ids); err != nil {
		return err
	}
	e.bumpVersion()
	if len(ids) == 0 {
		return e.StartScan()
	}
	for _, id := range ids {
		e.queueVideo(id)
	}
	e.Bus.Publish(Event{Type: "update"})
	return nil
}

// copyGroups 返回分组切片的浅拷贝：组结构逐个复制，Files 切片也各自复制一层。
// v3.10.4：VideoGroups / IntroGroups 有 2 分钟缓存，旧实现直接把内部切片交出去，
// 上层排序、附加标签、写 Peer 都会改到缓存本体，并发请求还会互相打架。
func copyGroups(groups []store.Group) []store.Group {
	if groups == nil {
		return nil
	}
	out := make([]store.Group, len(groups))
	copy(out, groups)
	for i := range out {
		if out[i].Files != nil {
			files := make([]store.FileRow, len(out[i].Files))
			copy(files, out[i].Files)
			out[i].Files = files
		}
	}
	return out
}

// VideoGroups 返回相似视频分组（带缓存）。
func (e *Engine) VideoGroups(ctx context.Context, force bool) ([]store.Group, error) {
	c := e.cfg.Get()
	// v3.10.3：缓存键带上阈值与匹配模式。旧实现只看 2 分钟 TTL，
	// 用户改完阈值后仍会拿到旧分组，最长要等 2 分钟才刷新；
	// 与 IntroGroups 用同一套口径，键变了立即重算。
	key := fmt.Sprintf("%.4f|%d|%s", c.AudioThreshold, c.PHashHamming, config.NormalizeMatchMode(c.MatchMode))
	e.vmu.Lock()
	defer e.vmu.Unlock()
	if !force && e.videoGroups != nil && e.videoKey == key && time.Since(e.videoAt) < 2*time.Minute {
		return copyGroups(e.videoGroups), nil
	}
	items, err := e.st.VideoFingerprints(ctx)
	if err != nil {
		return nil, err
	}
	g := video.FindGroupsMode(items, c.AudioThreshold, c.PHashHamming, e.secondPassVerifier(c),
		config.NormalizeMatchMode(c.MatchMode) == config.MatchModeThumbs)
	e.videoGroups = g
	e.videoAt = time.Now()
	e.videoKey = key
	return copyGroups(g), nil
}

type contentSigKey struct {
	path        string
	size        int64
	mtime       int64
	ignoreIntro int
	ignoreOutro int
}

func mediaMeta(m video.MediaInfo) store.MediaMeta {
	return store.MediaMeta{
		Width: m.Width, Height: m.Height, Codec: m.Codec, BitRate: m.BitRate, SampleRate: m.SampleRate,
	}
}

// secondPassVerifier 只为阈值附近的配对抽取正片灰度帧做简化 SSIM 校验；
// 同一轮分组内按文件缓存，避免重复解码。
func (e *Engine) secondPassVerifier(cfg *config.Config) video.VerifyFunc {
	if !e.RunnerSnapshot().Available() {
		return nil
	}
	budget := int64(64)
	deadline := time.Now().Add(30 * time.Second)
	frames := map[contentSigKey][][]byte{}
	failed := map[contentSigKey]bool{}
	load := func(v store.VideoFP) [][]byte {
		key := contentSigKey{v.Path, v.Size, v.Mtime, cfg.IgnoreIntroSeconds, cfg.IgnoreOutroSeconds}
		if f, ok := frames[key]; ok {
			return f
		}
		if failed[key] {
			return nil
		}
		// 二次校验只做轻量补强，超时后保留初始判定，避免阻塞分组请求。
		remaining := time.Until(deadline)
		if budget <= 0 || remaining <= 0 {
			return nil
		}
		budget--
		ctx, cancel := context.WithTimeout(context.Background(), remaining)
		defer cancel()
		f, err := e.RunnerSnapshot().ContentFrames(ctx, v.Path, v.Duration, cfg.IgnoreIntroSeconds, cfg.IgnoreOutroSeconds)
		if err != nil {
			failed[key] = true
			return nil
		}
		frames[key] = f
		return f
	}
	return func(a, b store.VideoFP) bool {
		fa, fb := load(a), load(b)
		// 校验帧不可得时保留初始判定，避免把可解码文件直接判为不相似。
		if len(fa) == 0 || len(fb) == 0 {
			return true
		}
		res := video.FramesSimilar(fa, fb)
		return res
	}
}

// IntroGroups 返回“相同片头”分组（带缓存）。
func (e *Engine) IntroGroups(ctx context.Context, n int, force bool) ([]store.Group, error) {
	c := e.cfg.Get()
	if n < 1 || n > 300 {
		n = c.IntroSeconds
	}
	if n < 1 {
		n = 10
	}
	key := fmt.Sprintf("%d|%.4f|%d", n, c.AudioThreshold, c.PHashHamming)
	e.imu.Lock()
	defer e.imu.Unlock()
	if !force && e.introGroups != nil && e.introKey == key && time.Since(e.introAt) < 2*time.Minute {
		return copyGroups(e.introGroups), nil
	}
	items, err := e.st.IntroFingerprints(ctx, n)
	if err != nil {
		return nil, err
	}
	g := video.FindIntroGroups(items, n, c.AudioThreshold, c.PHashHamming)
	e.introGroups = g
	e.introAt = time.Now()
	e.introKey = key
	return copyGroups(g), nil
}

// ReanalyzeIntro 按指定 N 重置片头指纹并启动扫描。
func (e *Engine) ReanalyzeIntro(ctx context.Context, seconds int) error {
	c := e.cfg.Get()
	if seconds >= 1 && seconds <= 300 {
		c.IntroSeconds = seconds
	}
	c.IntroEnabled = true
	if err := e.cfg.Replace(c); err != nil {
		return err
	}
	if _, err := e.st.ResetIntroAnalysis(ctx); err != nil {
		return err
	}
	e.bumpVersion()
	return e.StartScan()
}

// ---- 增量更新 ----

func (e *Engine) UpdatePath(path string) {
	ctx := e.baseCtx
	vol := e.VolumeOf(path)
	if vol == "" || strings.Contains(path, "/.verify-trash/") || e.IsExcludedPath(path) {
		return
	}
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() {
		if os.IsNotExist(err) || (err == nil && !st.Mode().IsRegular()) {
			e.dropIndexPath(ctx, nil, path)
			e.Bus.Publish(Event{Type: "update"})
			e.bumpVersion()
		}
		return
	}
	info := fileInfoOf(path, vol, st)
	cfg := e.cfg.Get()
	cur := e.st.CurrentGeneration(ctx)
	old, _ := e.st.GetFileByPath(ctx, path)
	id, err := e.st.UpsertOne(ctx, info, cur)
	if err != nil {
		return
	}
	e.bumpVersion()

	if info.Size >= cfg.MinSize {
		quick, qerr := hashx.QuickHash(path, info.Size)
		if qerr != nil {
			// v3.10.3：保留 NULL 等下一轮扫描重试；写 'ERR' 会被候选查询
			// 的 IS NULL 条件永久排除，等于放弃这个文件。
			applog.Warn("预哈希失败：%s（%v）", path, qerr)
		} else {
			_ = e.st.SetQuick(ctx, id, quick)
			if n, _ := e.st.CountQuickGroup(ctx, quick); n > 1 {
				files, _ := e.st.FullHashForQuick(ctx, quick, 64)
				for _, f := range files {
					h, ferr := hashx.FullHash(f.Path)
					if ferr != nil {
						applog.Warn("BLAKE3 校验失败：%s（%v）", f.Path, ferr)
					} else {
						_ = e.st.SetHash(ctx, f.ID, h)
					}
				}
			}
		}
	}
	// v3.10.10（P2-5）：ctime 也参与改动判定——内容替换后 mtime 可被回拨，ctime 不能。
	// 旧库 ctime=0 时跳过该项，避免升级后把全部视频重新排队分析。
	changed := old == nil || old.Mtime != info.Mtime || old.Size != info.Size ||
		(old.Ctime > 0 && info.Ctime > 0 && old.Ctime != info.Ctime)
	if (cfg.VideoEnabled || cfg.IntroEnabled) && isVideoExt(info.Ext) {
		analyzed, _ := e.st.HasVideoState(ctx, id)
		if changed || old == nil || !analyzed {
			e.queueVideo(id)
		}
	}
	e.Bus.Publish(Event{Type: "update"})
}

func (e *Engine) queueVideo(id int64) {
	e.videoSetMu.Lock()
	if e.videoQueued[id] {
		e.videoSetMu.Unlock()
		return
	}
	e.videoQueued[id] = true
	e.videoSetMu.Unlock()
	select {
	case e.videoJobs <- id:
	default:
		e.videoSetMu.Lock()
		delete(e.videoQueued, id)
		e.videoSetMu.Unlock()
	}
}

func (e *Engine) videoWorker() {
	// v3.25.0（P1-1）：pending 保存「已出队但还没拿到闸门」的 id。
	// 扫描/转码/剪除占着独占组时，这里不能丢队列项，等占用者结束再继续。
	var pending int64
	for {
		var id int64
		if pending != 0 {
			id, pending = pending, 0
		} else {
			select {
			case <-e.workerStop:
				return
			case id = <-e.videoJobs:
			}
		}
		cfg := e.cfg.Get()
		e.RunnerReload()
		if (!cfg.VideoEnabled && !cfg.IntroEnabled) || !e.RunnerSnapshot().Available() {
			e.clearQueued(id)
			continue
		}
		f, err := e.st.GetFileByID(e.baseCtx, id)
		if err != nil || f == nil {
			e.clearQueued(id)
			continue
		}
		// v3.25.0（P1-1）：视频/片头指纹是逐文件起 ffmpeg 的长任务，此前完全绕过闸门，
		// 扫描或转码在跑时照样开进程。这里按文件登记 ffmpeg 独占组。
		doneGuard, gerr := e.acquireTask(taskguard.KindVideo, f.Path, taskguard.GroupFFmpeg,
			taskguard.ResCPU, taskguard.ResDiskIO, taskguard.ResFFmpeg)
		if gerr != nil {
			pending = id
			select {
			case <-e.workerStop:
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		// 用户暂停优先于一切：暂停期间不跑 ffmpeg，但保留已登记的闸门位置。
		e.waitIfPaused(e.baseCtx)
		func() {
			defer doneGuard("done")
			if cfg.VideoEnabled {
				requeued := false
				ctx, cancel := context.WithTimeout(e.baseCtx, 4*time.Minute)
				fp, ferr := e.RunnerSnapshot().Fingerprint(ctx, f.Path, cfg.IgnoreIntroSeconds, cfg.IgnoreOutroSeconds, cfg.VideoDensity)
				cancel()
				if ferr != nil {
					applied, err := e.st.SetVideoError(e.baseCtx, id, f.Size, f.Mtime, ferr.Error())
					if err != nil {
						applog.Warn("写入视频失败结果失败（%s）：%v", f.Path, err)
					} else if !applied {
						// 分析期间文件被覆盖：旧错误会压住新文件的重试窗口，重新入队。
						e.clearQueued(id)
						e.queueVideo(id)
						requeued = true
					}
				} else {
					applied, err := e.st.SetVideo(e.baseCtx, id, f.Size, f.Mtime, f.Ctime, fp.Audio, fp.Thumbs, fp.Media.Duration, mediaMeta(fp.Media))
					if err != nil {
						applog.Warn("写入视频指纹失败（%s）：%v", f.Path, err)
					} else if !applied {
						// 同上：旧指纹不能挂在新内容上，按当前 stat 重新分析。
						e.clearQueued(id)
						e.queueVideo(id)
						requeued = true
					}
				}
				if requeued {
					// 已重新入队，保留队列标记，交给下一轮处理
					e.bumpVersion()
					e.Bus.Publish(Event{Type: "update"})
					return
				}
			}
			if cfg.IntroEnabled {
				n := cfg.IntroSeconds
				if n < 1 || n > 300 {
					n = 10
				}
				ctx, cancel := context.WithTimeout(e.baseCtx, introTimeout(n))
				fp, ferr := e.RunnerSnapshot().IntroFingerprint(ctx, f.Path, n)
				cancel()
				if ferr != nil {
					// v3.10.4：写库失败此前完全静默，错误结果没落库会导致同一文件反复重试。
					if err := e.st.SetIntroBatch(e.baseCtx, []store.IntroResult{{ID: id, Size: f.Size, Mtime: f.Mtime, Ctime: f.Ctime, Seconds: n, Err: ferr.Error()}}); err != nil {
						applog.Warn("写入片头失败结果失败（%s）：%v", f.Path, err)
					}
				} else {
					if err := e.st.SetIntro(e.baseCtx, id, f.Size, f.Mtime, f.Ctime, fp.Audio, fp.Thumbs, fp.Media.Duration, n, mediaMeta(fp.Media)); err != nil {
						applog.Warn("写入片头指纹失败（%s）：%v", f.Path, err)
					}
				}
			}
			e.clearQueued(id)
			e.bumpVersion()
			e.Bus.Publish(Event{Type: "update"})
		}()
	}
}

func (e *Engine) clearQueued(id int64) {
	e.videoSetMu.Lock()
	delete(e.videoQueued, id)
	e.videoSetMu.Unlock()
}

// ScheduleFullRescan 定时全量重扫。
func (e *Engine) ScheduleFullRescan(ctx context.Context) {
	c := e.cfg.Get()
	if c.FullRescanHours <= 0 {
		return
	}
	t := time.NewTicker(time.Duration(c.FullRescanHours) * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// 休眠友好或静默时段内跳过：这类主动扫描会唤醒已经休眠的磁盘
			if cfg := e.cfg.Get(); !cfg.AutoScanAllowed(time.Now()) {
				applog.Info("定时重扫跳过：休眠友好模式或处于静默时段")
				continue
			}
			_ = e.StartScan()
		}
	}
}

// TrashRoot 在移动文件时提供同卷回收站路径。
func (e *Engine) TrashRoot(vol string) string {
	return filepath.Join(vol, ".verify-trash")
}
