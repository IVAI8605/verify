// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"verify/internal/applog"
	"verify/internal/config"
	"verify/internal/engine"
	"verify/internal/hashx"
	"verify/internal/oplog"
	"verify/internal/perm"
	"verify/internal/store"
	"verify/internal/watch"
	"verify/internal/web"
)

func defaultDataDir() string {
	if v := os.Getenv("TRIM_PKGVAR"); v != "" {
		return filepath.Join(v, "data")
	}
	if v := os.Getenv("VERIFY_DATA"); v != "" {
		return v
	}
	if runtime.GOOS == "linux" {
		return "/var/lib/verify"
	}
	return "./data"
}

func defaultAppDest() string {
	if v := os.Getenv("TRIM_APPDEST"); v != "" {
		return v
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// migrateLegacyDataDir 项目改名（dupclean→verify，v1.19）一次性数据迁移：
// 旧数据目录存在且新目录不存在时整体 mv（同卷原子）；db/log 文件名一并改；
// 失败仅日志报错绝不静默删数据；新目录已存在则跳过防覆盖。
func migrateLegacyDataDir(newDir string) {
	appdataFolder := filepath.Dir(newDir)
	legacyDir := filepath.Join(filepath.Dir(appdataFolder), "dupclean", "data")
	if legacyDir == newDir {
		return
	}
	if _, err := os.Stat(newDir); err == nil && !newDirUninitialized(newDir) {
		// 新目录已有**初始化过**的数据（用户配过卷/用过）：跳过防覆盖。
		// v1.20.1/1.20.2 实测：cmd/main 预建目录 + 首次启动生成的空库/默认配置
		// 都不算「已初始化」，应允许接管。
		return
	}
	if _, err := os.Stat(legacyDir); err != nil {
		return
	}
	// 先清掉新目录里「未初始化」的生成物（v1.20.2 实测：rename 到非空目录会 ENOTEMPTY，
	// 必须先清空 newDir 再接管——仅当 newDirUninitialized 已判定安全）
	_ = os.Remove(filepath.Join(newDir, "verify.db"))
	_ = os.Remove(filepath.Join(newDir, "verify.db-shm"))
	_ = os.Remove(filepath.Join(newDir, "verify.db-wal"))
	_ = os.Remove(filepath.Join(newDir, "config.json"))
	_ = os.RemoveAll(filepath.Join(newDir, "logs"))
	_ = os.RemoveAll(filepath.Join(newDir, "cache"))
	// 逐项 move（文件级 rename 目标不存在必成功；整目录 rename 到已存在目录
	// 在部分内核/挂载下报 EEXIST——v1.20.3 真机 /tmp 复现）
	entries, rerr := os.ReadDir(legacyDir)
	if rerr != nil {
		log.Printf("读取旧数据目录失败：%v", rerr)
		return
	}
	for _, en := range entries {
		from := filepath.Join(legacyDir, en.Name())
		to := filepath.Join(newDir, en.Name())
		if err := os.Rename(from, to); err != nil {
			log.Printf("迁移 %s 失败：%v", en.Name(), err)
		}
	}
	_ = os.Remove(legacyDir) // 移空后删壳（非空则保留供排查）
	oldDB := filepath.Join(newDir, "dupclean.db")
	if _, err := os.Stat(oldDB); err == nil {
		_ = os.Rename(oldDB, filepath.Join(newDir, "verify.db"))
	}
	oldLog := filepath.Join(newDir, "logs", "dupclean.log")
	if _, err := os.Stat(oldLog); err == nil {
		_ = os.Rename(oldLog, filepath.Join(newDir, "logs", "verify.log"))
	}
	log.Printf("已从 %s 迁移数据目录到 %s", legacyDir, newDir)
}

// migrateLegacyTrashDirs 用户卷隔离区一次性迁移：.dupclean-trash → .verify-trash
// （已确认决策）。只迁移当前已配置扫描根所在卷；未引用卷由文档提示手动 mv。
func migrateLegacyTrashDirs(roots []string) {
	for _, root := range roots {
		vol := filepath.VolumeName(root)
		if vol == "" {
			// Linux 绝对路径：取根前缀段
			if parts := strings.SplitN(strings.TrimPrefix(root, "/"), "/", 2); len(parts) > 0 && parts[0] != "" {
				vol = "/" + parts[0]
			} else {
				continue
			}
		}
		oldTrash := filepath.Join(vol, ".dupclean-trash")
		newTrash := filepath.Join(vol, ".verify-trash")
		if _, err := os.Stat(oldTrash); err != nil {
			continue
		}
		if _, err := os.Stat(newTrash); err == nil {
			continue
		}
		if err := os.Rename(oldTrash, newTrash); err != nil {
			log.Printf("隔离区迁移失败（%s -> %s）：%v", oldTrash, newTrash, err)
			continue
		}
		log.Printf("隔离区已迁移：%s -> %s", oldTrash, newTrash)
	}
}

// newDirUninitialized 判断新数据目录是否从未被用户初始化：
// config.json 不存在，或其中 volumes 为空数组（用户尚未配置任何扫描根）。
func newDirUninitialized(newDir string) bool {
	b, err := os.ReadFile(filepath.Join(newDir, "config.json")) // #nosec G304 -- 固定文件名，目录来自启动参数而非请求
	if err != nil {
		return true
	}
	var cfg struct {
		Volumes []string `json:"volumes"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return false
	}
	return len(cfg.Volumes) == 0
}

func main() {
	dataDir := flag.String("data", defaultDataDir(), "数据目录")
	addr := flag.String("addr", "", "监听地址，如 :18898")
	appDest := flag.String("app-dest", defaultAppDest(), "应用安装目录（用于查找 ffmpeg）")
	flag.Parse()

	// 改名迁移（v1.19）：旧 dupclean 数据目录存在且新目录不存在时整体接管
	migrateLegacyDataDir(*dataDir)

	if err := os.MkdirAll(*dataDir, 0o750); err != nil {
		// v1.23 run-as=package：存量安装的数据目录属主是 root，应用用户（verify）
		// 无法写入时给出精确修复指引，而不是让用户面对晦涩的 Permission denied。
		log.Fatalf("无法创建数据目录 %s: %v。\n"+
			"若为 v1.23 权限切换后的首次启动，请在 NAS 终端以 root 执行一次：\n"+
			"  chown -R verify:verify %s && setfacl -R -m u:verify:rwX %s\n"+
			"然后重启应用即可（数据不会丢失）。", *dataDir, err, *dataDir, *dataDir)
	}
	// MkdirAll 不会修改已存在的目录：升级上来的安装仍是旧的 0755，必须显式收紧。
	perm.Tighten(*dataDir, 0o750)

	cfgMgr, err := config.Load(*dataDir)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	// 日志在读取配置后才初始化，这样用户自定义目录能立即生效。
	if err := applog.Init(applog.DefaultDir(*dataDir), cfgMgr.Get().LogDir); err != nil {
		log.Printf("初始化日志文件失败: %v（仅输出到标准输出）", err)
	}
	log.SetFlags(0)
	log.SetOutput(applog.Out())
	defer func() { _ = applog.Close() }()
	applog.Info("查重助手 Verify %s 启动，数据目录 %s，日志文件 %s", web.Version, *dataDir, applog.Path())
	applog.Info("哈希算法：%s（%s 加速），预哈希取头尾各 4KB", hashx.DisplayName, hashx.Level())

	// v4.5.4：Web 登录改为首次启动设置访问密码，不再自动生成访问令牌。
	// 已有 access_token 的旧配置保留，作为脚本/API 凭据与升级期登录兜底。

	if *addr != "" {
		c := cfgMgr.Get()
		c.Addr = *addr
		_ = cfgMgr.Replace(c)
	}

	st, err := store.Open(context.Background(), *dataDir)
	if err != nil {
		// v3.10.9：先写日志再退出，且日志要走 applog 并显式 Flush。
		// 此前用 log.Fatalf：它写入 applog 的缓冲后立刻 os.Exit，那行原因会被丢掉。
		applog.Error("打开数据库失败: %v", err)
		applog.Flush()
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()
	if reset, err := st.EnsureHashAlgo(context.Background(), hashx.AlgoVersion); err != nil {
		applog.Error("检查哈希算法版本失败：%v", err)
	} else if reset {
		applog.Warn("哈希算法已切换为 BLAKE3（预哈希头尾各 4KB），旧哈希缓存已清空，本次扫描将重新计算")
	}

	bus := engine.NewBus()
	eng := engine.New(st, cfgMgr, bus, *appDest)
	defer eng.Close()
	// v4.0.1（安全审查 问题3）：GET 路径不再写库，幽灵隔离行改到启动时在写锁内清理一次。
	eng.StartTrashMaintenance()

	if r := eng.RunnerSnapshot(); !r.Available() {
		applog.Warn("未找到系统 ffmpeg/ffprobe，视频相似检测不可用")
	} else if r.HWMode == "qsv" {
		applog.Info("视频分析使用 %s，核显 QSV 解码（%s）", r.FFmpeg, r.RenderNode)
	} else {
		applog.Info("视频分析使用 %s，软件解码（未检测到可用核显）", r.FFmpeg)
	}

	// 始终创建 watcher，让设置页的监控开关无需重启即可生效；
	// 关闭监控时 watcher 自身不挂载目录并丢弃事件。
	w, werr := watch.New(eng, cfgMgr)
	if werr != nil {
		applog.Warn("启动实时监控失败：%v（将依赖定时扫描）", werr)
	} else {
		defer w.Close()
	}

	// 隔离区一次性迁移（v1.19 已确认决策 .verify-trash）
	go migrateLegacyTrashDirs(cfgMgr.Get().Volumes)

	go eng.ScheduleFullRescan(context.Background())
	go eng.StartAutoProbe(context.Background())
	go eng.StartFreshnessLoop(context.Background())
	// v3.2.0：预设清理计划调度器，与保鲜循环共享同一套暂停/让路语义。
	go eng.StartCleanupLoop(context.Background())
	go eng.StartupScanIfAllowed()

	srv := web.New(eng, st, cfgMgr, *dataDir)
	httpSrv := &http.Server{
		Addr:              cfgMgr.Get().Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		// v3.10.9：补 ReadTimeout，防止慢速/半截请求长期占用连接。
		// 只限读：报告生成、导出与 SSE 都是长时间写，不能设 WriteTimeout。
		ReadTimeout: 60 * time.Second,
		IdleTimeout: 120 * time.Second,
	}
	go func() {
		applog.Info("Web UI 监听 %s", cfgMgr.Get().Addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// v3.10.9：同数据库失败路径，先落盘日志再退出，避免原因随缓冲一起丢失。
			applog.Error("HTTP 服务失败: %v", err)
			applog.Flush()
			os.Exit(1)
		}
	}()

	// v3.7.0：操作记录保留期清理，启动跑一次，之后每 24 小时跑一次。
	retentionCtx, stopRetention := context.WithCancel(context.Background())
	defer stopRetention()
	startOpLogRetention(retentionCtx, st, cfgMgr)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	applog.Info("收到退出信号，正在关闭服务")
	eng.StopScan()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

// startOpLogRetention 按配置定期清理过期操作记录；0 表示永久保留。
// 清理动作本身也留一条记录，避免「谁把记录清了」查不到。
func startOpLogRetention(ctx context.Context, st *store.Store, cfg *config.Manager) {
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			days := cfg.Get().OpLogRetentionDays
			if days > 0 {
				before := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
				if n, err := st.PurgeOpsBefore(ctx, before); err != nil {
					applog.Warn("清理过期操作记录失败：%v", err)
				} else if n > 0 {
					applog.Info("清理过期操作记录 %d 条（保留 %d 天）", n, days)
					e := oplog.New("retention_purge")
					e.Source = oplog.SourceSystem
					e.Summary = fmt.Sprintf("自动清理 %d 条过期操作记录（保留 %d 天）", n, days)
					if err := st.InsertOp(ctx, e, 0); err != nil {
						applog.Warn("写入保留期清理记录失败：%v", err)
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
