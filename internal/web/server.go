// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"verify/internal/applog"
	"verify/internal/config"
	"verify/internal/engine"
	"verify/internal/export"
	"verify/internal/fname"
	"verify/internal/fsbrowse"
	"verify/internal/hashx"
	"verify/internal/health"
	"verify/internal/lrucache"
	"verify/internal/oplog"
	"verify/internal/perm"
	"verify/internal/search"
	"verify/internal/store"
	"verify/internal/taskguard"
	"verify/internal/webauth"
)

//go:embed ui
var uiFS embed.FS

const Version = "4.0.2"

type Server struct {
	e       *engine.Engine
	st      *store.Store
	cfg     *config.Manager
	dataDir string

	// thumbs 是缩略图内存缓存：虚拟列表滚动时同一批图反复请求，
	// 驻内存后不再逐个读 cache 目录，机械盘负载明显下降。
	// 上限 256 张 / 8MiB，约等于一屏到两屏的可见缩略图。
	thumbs *lrucache.Cache

	// thumbPruneMu/thumbLastPrune 是磁盘缓存淘汰的节流状态（审查 D-2）。
	thumbPruneMu   sync.Mutex
	thumbLastPrune time.Time

	// 登录失败限速（v1.30.1）：按来源 IP 记录失败次数，超过阈值后锁定一段时间，
	// 防止局域网内对访问令牌的无限暴力尝试。成功登录即清零。
	loginMu    sync.Mutex
	loginFails map[string]*loginFail

	// searchIndex 是内存文件名索引（v2.10.0），启动后后台构建，扫描后按需自动重建。
	searchIndex *search.Index

	// searchSel 是全选凭据表（v3.20.0）：只存查询条件与计数，
	// 具体路径按需分批解析，避免几十万条路径来回搬。
	searchSel *selectionStore

	// renameCancel 记录用户请求取消的改名计划（v3.22.0）。
	// 取消只能停在片边界：正在执行的 2000 条会跑完，剩余 pending 保留可续跑。
	renameCancel *renameCancelStore

	// snap 是 v3.8.0 的搜索结果快照（全量内存态）。
	snap *snapshotManager

	// peerIdx 是对端指纹索引的进程内缓存（v3.10.10，P2-15）。
	// 索引只读，导入/删除对端时置空，下次请求重新加载。
	peerIdxMu sync.Mutex
	peerIdx   *store.PeerIndex
}

func New(e *engine.Engine, st *store.Store, cfg *config.Manager, dataDir string) *Server {
	_ = os.MkdirAll(filepath.Join(dataDir, "cache"), 0o750)
	// 升级上来的安装里 cache 目录是 0755，MkdirAll 不改已存在目录，这里补一次收紧。
	perm.Tighten(filepath.Join(dataDir, "cache"), 0o750)
	s := &Server{e: e, st: st, cfg: cfg, dataDir: dataDir, thumbs: lrucache.New(256, 8<<20), searchIndex: search.New(3000000), searchSel: newSelectionStore(), renameCancel: newRenameCancelStore()}
	s.snap = newSnapshotManager(s)
	s.applyCacheBudget() // v1.31：按内存预算配置调整 LRU 容量
	// 索引构建放后台，不阻塞启动；构建完成前搜索接口会明确返回未就绪
	go func() {
		if _, err := s.searchIndex.Build(context.Background(), st.DB()); err != nil {
			applog.Warn("文件搜索索引构建失败：%s", err.Error())
		}
	}()
	// v3.23.0：启动后延迟扫一遍转码/剪除残留（只扫索引里的视频目录一层），
	// 有超过 24 小时的临时文件就告警；真正移动由界面确认后执行，启动不自动改文件。
	go func() {
		time.Sleep(10 * time.Second)
		files, dirs, err := s.e.FindTranscodeResidue(context.Background(), 24*time.Hour, 2000)
		if err == nil && len(files) > 0 {
			applog.Warn("发现 %d 个超过 24 小时的转码残留（扫描 %d 个目录），可在「视频转码」页一键移入隔离区", len(files), dirs)
		}
	}()
	return s
}

// cacheDir 每次读取配置，设置页修改缓存目录后无需重启即可生效。
func (s *Server) cacheDir() string {
	if d := strings.TrimSpace(s.cfg.Get().CacheDir); d != "" {
		return d
	}
	return filepath.Join(s.dataDir, "cache")
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.auth(s.handleLogout))
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/tasks", s.auth(s.handleTasks))
	mux.HandleFunc("/api/scan", s.auth(s.handleScan))
	mux.HandleFunc("/api/scan/stop", s.auth(s.handleScanStop))
	mux.HandleFunc("/api/groups", s.auth(s.handleGroups))
	mux.HandleFunc("/api/folder/stats", s.auth(s.handleFolderStats))
	mux.HandleFunc("/api/groups/video", s.auth(s.handleVideoGroups))
	mux.HandleFunc("/api/groups/intro", s.auth(s.handleIntroGroups))
	mux.HandleFunc("/api/groups/photo", s.auth(s.handlePhotoGroups))
	mux.HandleFunc("/api/relink/preview", s.auth(s.handleRelinkPreview))
	mux.HandleFunc("/api/relink", s.auth(s.handleRelink))
	mux.HandleFunc("/api/big", s.auth(s.handleBig))
	mux.HandleFunc("/api/emptydir/clean", s.auth(s.handleEmptyDirClean))
	mux.HandleFunc("/api/move/preview", s.auth(s.handleMovePreview))
	mux.HandleFunc("/api/move", s.auth(s.handleMove))
	mux.HandleFunc("/api/move/undo", s.auth(s.handleMoveUndo))
	// v3.14.0：目录级移动（同卷 rename，索引按前缀改写，审计可撤销）。
	mux.HandleFunc("/api/move/dir/preview", s.auth(s.handleMoveDirPreview))
	mux.HandleFunc("/api/move/dir", s.auth(s.handleMoveDir))
	mux.HandleFunc("/api/move/dir/undo", s.auth(s.handleMoveDirUndo))
	mux.HandleFunc("/api/groups/rename", s.auth(s.handleGroupRename))
	mux.HandleFunc("/api/groups/rename/undo", s.auth(s.handleGroupRenameUndo))
	mux.HandleFunc("/api/file/rename", s.auth(s.handleFileRename))
	mux.HandleFunc("/api/trim", s.auth(s.handleTrim))
	mux.HandleFunc("/api/trim/log", s.auth(s.handleTrimLog))
	mux.HandleFunc("/api/trim/probe", s.auth(s.handleTrimProbe))
	mux.HandleFunc("/api/transcode", s.auth(s.handleTranscode))
	mux.HandleFunc("/api/transcode/status", s.auth(s.handleTranscodeStatus))
	mux.HandleFunc("/ui/", s.handleUIAsset)
	mux.HandleFunc("/api/tasks/pause", s.auth(s.handleTasksPause))
	mux.HandleFunc("/api/tasks/resume", s.auth(s.handleTasksResume))
	mux.HandleFunc("/api/fanjian", s.auth(s.handleFanjian))
	mux.HandleFunc("/api/fanjian/undo", s.auth(s.handleFanjianUndo))
	mux.HandleFunc("/api/transcode/cancel", s.auth(s.handleTranscodeCancel))
	// v3.23.0：转码/剪除残留的只读预览与移入隔离区。
	mux.HandleFunc("/api/transcode/residue", s.auth(s.handleTranscodeResidue))
	mux.HandleFunc("/api/transcode/residue/clean", s.auth(s.handleTranscodeResidueClean))
	mux.HandleFunc("/api/groups/audio", s.auth(s.handleAudioGroups))
	mux.HandleFunc("/api/groups/folder", s.auth(s.handleFolderGroups))
	mux.HandleFunc("/api/groups/fuzzy", s.auth(s.handleFuzzyGroups))
	mux.HandleFunc("/api/folder_rename/preview", s.auth(s.handleFolderRenamePreview))
	mux.HandleFunc("/api/folder_rename", s.auth(s.handleFolderRename))
	mux.HandleFunc("/api/folder_rename/undo", s.auth(s.handleFolderRenameUndo))
	mux.HandleFunc("/api/groups/semantic", s.auth(s.handleSemanticGroups))
	// v2.6.0：元数据标签规则引擎 CRUD
	mux.HandleFunc("/api/video_tags", s.auth(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.handleVideoTags(w, r)
		case http.MethodPost:
			s.handleVideoTagsPost(w, r)
		case http.MethodDelete:
			s.handleVideoTagsDelete(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		}
	}))
	// v2.7.0：标签值重算与按标签辅助分组
	mux.HandleFunc("/api/video_tags/recompute", s.auth(s.handleVideoTagsRecompute))
	mux.HandleFunc("/api/groups/tag", s.auth(s.handleTagGroups))
	// v2.10.0：Everything 式文件搜索
	mux.HandleFunc("/api/search", s.auth(s.handleFileSearch))
	// v3.20.0：全选凭据与按需解析（路径不落前端内存）
	mux.HandleFunc("/api/search/selection", s.auth(s.handleSearchSelection))
	mux.HandleFunc("/api/search/selection/items", s.auth(s.handleSearchSelectionItems))
	mux.HandleFunc("/api/search/status", s.auth(s.handleSearchStatus))
	mux.HandleFunc("/api/search/rebuild", s.auth(s.handleSearchRebuild))
	// v2.10.0：清单式改名（V3.5 只扩展方法，不改接口）
	mux.HandleFunc("/api/rename/preview", s.auth(s.handleRenamePreview))
	mux.HandleFunc("/api/rename/execute", s.auth(s.handleRenameExecute))
	// v3.21.0：两阶段改名（计划落库 + 分片执行），40 万条也不会一次顶满请求体与 WAL。
	mux.HandleFunc("/api/rename/plan", s.auth(s.handleRenamePlan))
	mux.HandleFunc("/api/rename/plan/status", s.auth(s.handleRenamePlanStatus))
	mux.HandleFunc("/api/rename/execute-chunk", s.auth(s.handleRenameExecuteChunk))
	mux.HandleFunc("/api/rename/cancel", s.auth(s.handleRenameCancel))
	mux.HandleFunc("/api/rename/undo-batch", s.auth(s.handleRenameUndoBatch))
	mux.HandleFunc("/api/rename/batches/recent", s.auth(s.handleRecentRenameBatches))
	mux.HandleFunc("/api/rename/undo/", s.auth(s.handleRenameUndo))
	// v3.10.10（第五轮 P1-2）：崩溃残留的 running 改名批次，供界面提示条一键撤销。
	mux.HandleFunc("/api/rename/batches/unfinished", s.auth(s.handleUnfinishedRenameBatches))
	// v3.5.0：重命名方法预设
	mux.HandleFunc("/api/rename/presets", s.auth(s.handleRenamePresets))
	mux.HandleFunc("/api/rename/presets/", s.auth(s.handleRenamePresetByID))
	mux.HandleFunc("/api/video/cropdetect", s.auth(s.handleVideoCropDetect))
	mux.HandleFunc("/api/retain/score", s.auth(s.handleRetainScore))
	mux.HandleFunc("/api/retain/pref", s.auth(s.handleRetainPref))
	mux.HandleFunc("/api/retain/pref/stats", s.auth(s.handleRetainPrefStats))
	mux.HandleFunc("/api/retain/pref/clear", s.auth(s.handleRetainPrefClear))
	// v3.3.0：偏好反哺（学习结果影响打分权重，可随时停用）
	mux.HandleFunc("/api/retain/pref/profile", s.auth(s.handleRetainPrefProfile))
	mux.HandleFunc("/api/retain/pref/apply", s.auth(s.handleRetainPrefApply))
	// v3.3.0：可选 AI 解释（只生成理由，不参与判定）
	mux.HandleFunc("/api/retain/explain", s.auth(s.handleRetainExplain))
	// v3.4.0：跨设备指纹包（只交换指纹，不传文件与凭据）
	mux.HandleFunc("/api/sync/export", s.auth(s.handleSyncExport))
	mux.HandleFunc("/api/sync/import", s.auth(s.handleSyncImport))
	mux.HandleFunc("/api/sync/peers", s.auth(s.handleSyncPeers))
	mux.HandleFunc("/api/intro/reanalyze", s.auth(s.handleIntroReanalyze))
	mux.HandleFunc("/api/video/failures", s.auth(s.handleVideoFailures))
	mux.HandleFunc("/api/video/reanalyze", s.auth(s.handleVideoReanalyze))
	mux.HandleFunc("/api/trash", s.auth(s.handleTrash))
	mux.HandleFunc("/api/trash/list", s.auth(s.handleTrashList))
	mux.HandleFunc("/api/trash/export", s.auth(s.handleTrashExport))
	mux.HandleFunc("/api/trash/preview", s.auth(s.handleTrashPreview))
	mux.HandleFunc("/api/trash/restore", s.auth(s.handleRestore))
	mux.HandleFunc("/api/trash/delete", s.auth(s.handleDelete))
	mux.HandleFunc("/api/trash/empty", s.auth(s.handleEmptyTrash))
	// v3.2.0：预设清理计划（只移入隔离区，不永久删除）
	mux.HandleFunc("/api/cleanup/plans", s.auth(s.handleCleanupPlans))
	mux.HandleFunc("/api/cleanup/plans/delete", s.auth(s.handleCleanupDelete))
	mux.HandleFunc("/api/cleanup/run", s.auth(s.handleCleanupRun))
	mux.HandleFunc("/api/export", s.auth(s.handleExport))
	mux.HandleFunc("/api/import", s.auth(s.handleImport))
	mux.HandleFunc("/api/import/sessions", s.auth(s.handleImportSessions))
	mux.HandleFunc("/api/import/groups", s.auth(s.handleImportGroups))
	mux.HandleFunc("/api/report", s.auth(s.handleReport))
	// v3.11.0：历史写操作快照（只读，不触发文件操作）。
	mux.HandleFunc("/api/op-snapshots", s.auth(s.handleOpSnapshots))
	mux.HandleFunc("/api/fs/list", s.auth(s.handleFSList))
	mux.HandleFunc("/api/diag/authz", s.auth(s.handleDiagAuthz))
	mux.HandleFunc("/api/fs/mkdir", s.auth(s.handleFSMkdir))
	mux.HandleFunc("/api/metric", s.auth(s.handleMetric))
	mux.HandleFunc("/api/metrics", s.auth(s.handleMetrics))
	mux.HandleFunc("/api/raw", s.auth(s.handleRaw))
	mux.HandleFunc("/api/thumb", s.auth(s.handleThumb))
	mux.HandleFunc("/api/cache/stats", s.auth(s.handleCacheStats))
	mux.HandleFunc("/api/cache/clear", s.auth(s.handleCacheClear))
	mux.HandleFunc("/api/settings", s.auth(s.handleSettings))
	// v3.7.2：设置页「查看当前令牌」；只对已登录请求返回，且禁止缓存。
	mux.HandleFunc("/api/auth/token", s.auth(s.handleAuthToken))
	// v3.8.0：搜索阶段与全量快照（与文件搜索 /api/search/* 区分开）。
	mux.HandleFunc("/api/rule-search/run", s.auth(s.handleRuleSearchRun))
	mux.HandleFunc("/api/rule-search/status", s.auth(s.handleRuleSearchStatus))
	mux.HandleFunc("/api/rule-search/cancel", s.auth(s.handleRuleSearchCancel))
	mux.HandleFunc("/api/rule-search/results", s.auth(s.handleRuleSearchResults))
	mux.HandleFunc("/api/rule-search/facets", s.auth(s.handleRuleSearchFacets))
	mux.HandleFunc("/api/rule-search/export", s.auth(s.handleRuleSearchExport))
	mux.HandleFunc("/api/events", s.auth(s.handleEvents))
	mux.HandleFunc("/api/health/scan", s.auth(s.handleHealthScan))
	mux.HandleFunc("/api/health/cancel", s.auth(s.handleHealthCancel))
	mux.HandleFunc("/api/health/status", s.auth(s.handleHealthStatus))
	mux.HandleFunc("/api/health/results", s.auth(s.handleHealthResults))
	mux.HandleFunc("/api/health/rename", s.auth(s.handleHealthRename))
	// v3.12.0：修复非 UTF-8 文件名（路径由服务端从索引里找，前端只触发）。
	mux.HandleFunc("/api/health/fix-encoding", s.auth(s.handleHealthFixEncoding))
	mux.HandleFunc("/api/health/trash", s.auth(s.handleHealthTrash))
	mux.HandleFunc("/api/health/undo", s.auth(s.handleHealthUndo))
	// v3.10.12（P1-02）：索引体检，清理磁盘上已不存在的幽灵索引行。
	mux.HandleFunc("/api/index/repair", s.auth(s.handleIndexRepair))
	// v3.7.0：操作记录查询、导出与清理
	mux.HandleFunc("/api/oplog", s.auth(s.handleOpLog))
	mux.HandleFunc("/api/oplog/export", s.auth(s.handleOpLogExport))
	mux.HandleFunc("/api/oplog/clear", s.auth(s.handleOpLogClear))
	// v3.10.10（第五轮 P1-1）：未完成写操作快照的只读对账，只报告不动文件
	mux.HandleFunc("/api/op-snapshots/unfinished", s.auth(s.handleUnfinishedOpSnapshots))
	// 记录用户活动时间供闲时巡检判定；只写一个原子变量，不产生 I/O。
	// 放在最外层，静态资源请求也算"有人在用"，避免用户正在浏览时自动任务抢盘。
	// opLogMiddleware 注入请求 ID / 来源 IP，并为写请求补一条汇总记录。
	// csrfGuard 在最外层：安全头覆盖所有响应（含静态资源与 401），CSRF 判定先于鉴权，避免无谓的令牌比对。
	// limitBody 再包一层：所有写请求统一加体积上限，避免未启用令牌时单个大 JSON 就打满内存。
	return s.limitBody(s.csrfGuard(s.opLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.e.TouchActivity()
		mux.ServeHTTP(w, r)
	}))))
}

// 请求体上限（v3.10.9）。多数 handler 直接 json.NewDecoder(r.Body)，
// 不设上限时未启用访问令牌的部署可被局域网内单个大请求打满内存；
// 若此时正在执行删除，OOM 会留下「磁盘已动、索引与审计未写」的中间态。
const (
	defaultBodyLimit = 8 << 20   // 普通 JSON 接口
	importBodyLimit  = 128 << 20 // CSV 导入（与 export.MaxImportBytes 保持一致）
	syncBodyLimit    = 96 << 20  // 跨设备指纹包，单个包可能上万行
)

// limitBody 给带请求体的方法统一套 MaxBytesReader。
// 已单独设上限的 handler 会在此之上再包一层，取两者较小值，原有更严格的限制不受影响。
func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			limit := int64(defaultBodyLimit)
			switch r.URL.Path {
			case "/api/import":
				limit = importBodyLimit
			case "/api/sync/import":
				limit = syncBodyLimit
			case "/api/rename/plan":
				// v3.21.0：40 万条显式清单约 32MB，留一倍余量；正常走搜索凭据时只有几百字节。
				limit = renamePlanBodyLimit
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// handleTasksPause / handleTasksResume 任务手动暂停与恢复（v1.21）。
func (s *Server) handleTasksPause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	s.e.PauseTasks()
	writeJSON(w, 200, pausedResponse{OK: true, Paused: true})
}

func (s *Server) handleTasksResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	s.e.ResumeTasks()
	writeJSON(w, 200, pausedResponse{OK: true, Paused: false})
}

// uiAssets 是允许通过 /ui/<name> 访问的内嵌资源白名单。
// v3.0.0 起 app.js 按职责拆成六个脚本，白名单同步扩容；
// 只认文件名（filepath.Base），任何目录片段都会被拒绝。
var uiAssets = map[string]bool{
	"helper.js": true, "api.js": true, "settings.js": true,
	"view.js": true, "modal.js": true, "app.js": true, "styles.css": true,
}

// handleUIAsset 返回拆分后的前端资源（v1.20：ui/app.js、ui/styles.css）。
// 单二进制不变：资源来自 go:embed ui 目录，零 CDN、零运行时构建。
func (s *Server) handleUIAsset(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.URL.Path) // 只允许文件名，防目录穿越
	if !uiAssets[name] {
		http.NotFound(w, r)
		return
	}
	body, err := uiFS.ReadFile("ui/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctype := "application/javascript"
	if strings.HasSuffix(name, ".css") {
		ctype = "text/css; charset=utf-8"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body) // #nosec G705 -- body 来自 go:embed 的固定前端资源（app.js/styles.css），不含用户输入
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	body, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "UI 未嵌入", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

// securityHeaders 给所有响应加安全头（v3.10.6 SEC-2）。
// style-src 必须放行 inline，因为进度条显隐与弹层位置直接用 style 属性写。
// v3.10.7 曾为兼容飞牛应用中心 iframe（同主机不同端口）放开 host:*；v3.10.8 起入口改为 type=url
// 独立新开标签页，不再需要被任何页面嵌入，v3.10.9 收紧回 DENY + frame-ancestors 'self'。
// 这样即使将来有人把入口改回 iframe，也能第一时间从「白屏」暴露出来，而不是静默保留放宽面。
func securityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; media-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'self'; object-src 'none'; base-uri 'self'; form-action 'self'")
}

// stateChangingMethod 判断请求是否会改动服务端状态（GET/HEAD/OPTIONS 视为只读）。
func stateChangingMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// sameSiteRequest 判断写请求是否来自同源页面（v3.10.6 SEC-1）。
// 优先用 Sec-Fetch-Site（浏览器强制写入、脚本无法伪造）；缺失时回退 Origin/Referer 与 Host 比对。
// 三者都没有说明不是浏览器发起的（curl、本机脚本），按放行处理。
func sameSiteRequest(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "":
		// 继续走 Origin/Referer 回退判断
	case "same-origin", "none":
		return true
	default:
		// same-site / cross-site：同站不同端口也算 same-site，正是要挡的场景
		return false
	}
	ref := r.Header.Get("Origin")
	if ref == "" {
		ref = r.Header.Get("Referer")
	}
	if ref == "" {
		return true
	}
	u, err := url.Parse(ref)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// cookieAuthed 只判断当前请求是否靠 Cookie 通过鉴权（令牌在 URL 或头里的请求不受 CSRF 影响）。
func (s *Server) cookieAuthed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	token := s.cfg.Get().AccessToken
	if token == "" {
		return false
	}
	return webauth.TokenEqual(c.Value, webauth.CookieValue(token))
}

// csrfGuard 是全站写请求的 CSRF 防线（v3.10.6 SEC-1）。
// 之前只有 SameSite=Lax：同一台 NAS 上另一个端口的服务（Jellyfin、下载器）发起的
// 表单 POST 仍会带上 Cookie，用 text/plain 发送即属简单请求、不触发预检。
// 这里两道判定：来源必须是同源页面；Cookie 会话还必须带自定义头 X-Requested-With
// （跨站表单设不了自定义头）。Bearer / ?token= 客户端不受影响。
func (s *Server) csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w)
		if !stateChangingMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		if !sameSiteRequest(r) {
			// v3.17.4（F-21）：403 也留档，写清是跨站来源还是缺请求标记。
			s.recordDenied(r, "跨站写请求被拒")
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "跨站写请求已被拒绝"})
			return
		}
		if s.cookieAuthed(r) && r.Header.Get("X-Requested-With") != "verify" {
			s.recordDenied(r, "写请求缺少 X-Requested-With 标记")
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "缺少请求标记，跨站写请求已被拒绝"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

const cookieName = "verify_token"

func (s *Server) authed(r *http.Request) bool {
	token := s.cfg.Get().AccessToken
	if token == "" {
		return true
	}
	// Cookie 里存的是令牌派生值，不是令牌本身。
	want := webauth.CookieValue(token)
	if c, err := r.Cookie(cookieName); err == nil && webauth.TokenEqual(c.Value, want) {
		return true
	}
	// SSE 允许通过查询参数携带，EventSource 无法设置请求头。
	// v3.10.8（SEC-6）：只对 /api/events 放行，避免令牌出现在其它接口的 URL/日志/Referer 里。
	if q := r.URL.Query().Get("token"); q != "" && r.URL.Path == "/api/events" && webauth.TokenEqual(q, token) {
		return true
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") && webauth.TokenEqual(strings.TrimPrefix(h, "Bearer "), token) {
		return true
	}
	return false
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			s.recordDenied(r, "无有效访问令牌")
			writeJSON(w, http.StatusUnauthorized, authErrorResponse{Error: "需要访问令牌", Auth: true})
			return
		}
		if !guardReplacementChars(w, r) {
			return
		}
		next(w, r)
	}
}

// writePathPrefixes 是真正会改动文件的写接口（v3.11.0）。
// 护栏只挂这些入口：只读的预览/评分/解释接口即使带了编码异常路径也不该整包 400，
// 否则「智能保留建议」「选择助手」会因为一个坏文件名而整批失败（第十轮 P1）。
var writePathPrefixes = []string{
	"/api/trash", "/api/trash/restore", "/api/trash/delete", "/api/trash/empty",
	"/api/groups/rename", "/api/file/rename", "/api/rename/execute", "/api/rename/undo", "/api/rename/plan",
	"/api/move", "/api/move/dir", "/api/relink", "/api/trim", "/api/transcode", "/api/fanjian",
	"/api/folder_rename", "/api/health/rename", "/api/health/fix-encoding", "/api/health/trash", "/api/health/undo",
	"/api/emptydir/clean", "/api/fs/mkdir", "/api/cleanup/run",
}

// isWritePath 判断请求是否落在写操作入口。
// /api/trash/preview、/api/move/preview、/api/trim/probe、/api/folder_rename/preview
// 等只读接口不在列表里，保持放行。
func isWritePath(path string) bool {
	switch path {
	case "/api/trash/preview", "/api/trash/export", "/api/trash/list",
		"/api/move/preview", "/api/move/dir/preview", "/api/trim/probe", "/api/folder_rename/preview",
		"/api/rename/preview", "/api/relink/preview":
		return false
	}
	for _, p := range writePathPrefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// guardReplacementChars 拦截写请求体里的 U+FFFD（v3.11.0）。
//
// 非 UTF-8 文件名入库后，前端回传路径时原始字节已被 JSON 编解码替换成 U+FFFD，
// 后端按路径查库只会得到「索引中不存在」，用户看不出真正原因。
// 只检查写操作入口；不依赖 Content-Length，分块传输与大批量请求同样读全量校验
// （第十一轮 P2-3：原实现按 ContentLength 判断，chunked 或超过 1MiB 的批量请求会被直接放行）。
func guardReplacementChars(w http.ResponseWriter, r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if !isWritePath(r.URL.Path) {
		return true
	}
	if r.Body == nil {
		return true
	}
	// v3.21.0：改名计划允许 64MiB 请求体，整包读进内存会多占一份峰值。
	// 这里改为流式扫描：读取时顺带检查，发现替换字符立即返回哨兵错误，
	// 由 handleRenamePlan 翻译成具体的 400 提示。
	if r.URL.Path == "/api/rename/plan" {
		r.Body = &fffdGuardReader{r: r.Body}
		return true
	}
	// 写接口的请求体是路径清单，数千条批量也只有几百 KB；上限与普通 JSON 接口一致（8MiB），
	// 超过时 MaxBytesReader 会先报错，这里统一翻译成 413，提示调用方分批提交。
	const maxWriteBody = defaultBodyLimit
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWriteBody+1))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "请求体过大，请分批提交"})
			return false
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "读取请求失败，请重试"})
		return false
	}
	if len(body) > maxWriteBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "请求体过大，请分批提交"})
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	// 两种形态都要拦：前端 JSON.stringify 输出的是真实 U+FFFD 字节；
	// 脚本/外部调用可能写成 JSON 转义 \ufffd，此时原始字节里看不到替换字符。
	if bytes.ContainsRune(body, '\uFFFD') || bytes.Contains(bytes.ToLower(body), []byte(`\ufffd`)) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "路径包含无法识别的文件名字符，请从文件列表重新选择"})
		return false
	}
	return true
}

// fffdGuardReader 是 U+FFFD 的流式护栏（v3.21.0）。
//
// 为什么不用整包读入：改名计划允许 64MiB，如果先读满再交给 json.Decoder，
// 内存峰值会翻倍。这里在每次 Read 时顺带扫描，保留末尾 5 字节以覆盖跨块的
// `\ufffd` 转义序列，发现替换字符就返回哨兵错误。
type fffdGuardReader struct {
	r    io.ReadCloser
	tail []byte
	bad  bool
}

func (g *fffdGuardReader) Read(p []byte) (int, error) {
	if g.bad {
		return 0, errReplacementChar
	}
	n, err := g.r.Read(p)
	if n > 0 {
		chunk := p[:n]
		joined := chunk
		if len(g.tail) > 0 {
			joined = append(append([]byte{}, g.tail...), chunk...)
		}
		if bytes.ContainsRune(joined, '\uFFFD') || bytes.Contains(bytes.ToLower(joined), []byte(`\ufffd`)) {
			g.bad = true
			return 0, errReplacementChar
		}
		// 保留末尾最多 5 字节：`\ufffd` 共 6 字节，跨块时靠它补齐。
		keep := 5
		if len(joined) < keep {
			keep = len(joined)
		}
		if keep > 0 {
			g.tail = append(g.tail[:0], joined[len(joined)-keep:]...)
		}
	}
	return n, err
}

func (g *fffdGuardReader) Close() error { return g.r.Close() }

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	// v1.30.1：登录失败限速（按来源 IP，成功后清零）
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	if s.loginThrottleCheck(ip) {
		writeJSON(w, http.StatusTooManyRequests, errorResponse{Error: "尝试过于频繁，请 5 分钟后再试"})
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	// v3.10.8（SEC-5）：登录体只需一个令牌字段，限制大小防止超大请求体占内存。
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	token := s.cfg.Get().AccessToken
	if token == "" || webauth.TokenEqual(body.Token, token) {
		s.loginThrottleReset(ip)
		if token != "" {
			http.SetCookie(w, &http.Cookie{ // #nosec G124 -- 局域网内以 HTTP 提供服务，设置 Secure 会导致浏览器直接丢弃 Cookie；已启用 HttpOnly 与 SameSite
				Name: cookieName, Value: webauth.CookieValue(token), Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
				MaxAge: 30 * 24 * 3600,
			})
			e := oplog.New("login_ok")
			e.Summary = "登录成功"
			s.recordOp(r, e)
		}
		writeJSON(w, 200, okResponse{OK: true})
		return
	}
	s.loginThrottleFail(ip)
	fail := oplog.New("login_fail")
	fail.Result = oplog.ResultFail
	fail.Summary = "访问令牌不正确"
	s.recordOp(r, fail)
	writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "访问令牌不正确"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// v3.10.8（SEC-7）：登出会清 Cookie 并留痕，只接受 POST；
	// 之前 GET 也能触发，任意 <img>/链接即可把用户踢下线（同站跨端口更易命中）。
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	// 与登录 Cookie 保持一致：同样带上 HttpOnly 与 SameSite，Secure 同因 HTTP 部署不启用。
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- 局域网 HTTP 部署，原因同上
		Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	e := oplog.New("logout")
	e.Summary = "退出登录"
	s.recordOp(r, e)
	writeJSON(w, 200, okResponse{OK: true})
}

type volInfo struct {
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	cfg := s.cfg.Get()
	// v3.10.8（SEC-8 延伸）：未登录时只下发登录页需要的字段。
	// 原来无论是否登录都返回数据目录、日志路径、ffmpeg 路径与完整配置，
	// 等于给局域网里未授权的访问者一份内部结构清单；配置本身还有别的接口可读。
	if !s.authed(r) {
		out := statusUnauthResponse{
			Version: Version,
			Auth:    map[string]any{"required": cfg.AccessToken != "", "ok": false},
		}
		if s.e != nil {
			out.State = s.e.State()
		}
		writeJSON(w, 200, out)
		return
	}
	counts, err := s.st.Counts(r.Context(), cfg.MinSize)
	if err != nil {
		counts = store.Counts{}
	}
	// v1.41.7：概览磁盘只显示授权/配置目录所在卷（trimacl 探测 + 软件内配置）。
	allowedVols := map[string]bool{}
	for _, root := range s.authzRoots() {
		if v := volumePrefix(root); v != "" {
			allowedVols[v] = true
		}
	}
	vols := []volInfo{}
	seenVol := map[string]bool{}
	for _, root := range s.e.Volumes() {
		// e.Volumes() 返回的是扫描目录（可能是 /vol6/1000/xxx 这种子目录），
		// allowedVols 的键是卷前缀（/vol6）；必须先归一，否则永远匹配不上。
		v := volumePrefix(root)
		// 空集合 = 一个卷都不显示（未配置扫描目录时概览不列任何卷）
		if v == "" || seenVol[v] || !allowedVols[v] {
			continue
		}
		seenVol[v] = true
		vi := volInfo{Path: v}
		var fs syscall.Statfs_t
		if err := syscall.Statfs(v, &fs); err == nil {
			if bs := blockSizeUint64(fs.Bsize); bs > 0 {
				vi.Total = fs.Blocks * bs
				vi.Free = fs.Bavail * bs
			}
		}
		vols = append(vols, vi)
	}
	writeJSON(w, 200, statusResponse{
		State:  s.e.State(),
		Paused: s.e.Paused(),
		// v3.10.8（P2-3）：健康检测是否在跑由状态接口一并下发。
		// 以前前端只在自己访问过健康页后才知道，其它页面扫描期间仍能点写操作。
		HealthRunning: s.e.HealthRunning(),
		// v3.12.0：视频/片头/裁剪/转码是否正在使用 ffmpeg。健康探测会等它们结束才开始，
		// 界面据此在用户点「健康扫描」时给一次二次确认，避免把排队等待当成卡死。
		FFmpegRunning: s.e.FFmpegBusy(),
		// v3.18.0：当前占用独占槽的任务（扫描 / 转码 / 健康检测等）。
		// 顶部状态区据此显示「正在运行：扫描（占用磁盘）」，无需再轮询 /api/tasks。
		ExclusiveTask: exclusiveTaskView(s),
		Counts:        counts,
		Volumes:       vols,
		Version:       Version,
		Cleanup:       s.cleanupSummary(r.Context()),
		Auth: map[string]any{
			"required": cfg.AccessToken != "",
			"ok":       s.authed(r),
		},
		Config: statusConfigView(cfg),
		Hash: map[string]any{
			"algo":        hashx.DisplayName,
			"level":       hashx.Level(),
			"quick_chunk": hashx.QuickChunk,
		},
		Paths: map[string]any{
			"data_dir":  s.dataDir,
			"cache_dir": s.cacheDir(),
			"log_file":  applog.Path(),
		},
		FFmpeg: map[string]any{
			"available":   s.e.RunnerSnapshot().Available(),
			"ffmpeg":      s.e.RunnerSnapshot().FFmpeg,
			"ffprobe":     s.e.RunnerSnapshot().FFprobe,
			"hw_mode":     s.e.RunnerSnapshot().HWMode,
			"render_node": s.e.RunnerSnapshot().RenderNode,
			"guc":         gucParam(),
		},
	})
}

// statusConfigView 返回 /api/status 里下发给前端的配置项。
// 前端设置页直接以这份数据回显表单（web/ui/settings.js），因此凡是设置页可编辑的
// 字段都必须在这里出现；漏掉就会表现为“改完刷新弹回默认值”，保存时还会把真实
// 配置覆盖成默认值。字段增删请同步 internal/web/status_test.go。
func statusConfigView(cfg *config.Config) map[string]any {
	return map[string]any{
		"min_size":              cfg.MinSize,
		"watch":                 cfg.Watch,
		"content_enabled":       cfg.ContentEnabled,
		"name_enabled":          cfg.NameEnabled,
		"compute_dup_folders":   cfg.ComputeDupFolders,
		"match_mode":            cfg.MatchMode,
		"match_kinds":           cfg.MatchKinds,
		"search_rules":          cfg.SearchRules,
		"search_mem_limit_mb":   cfg.SearchMemLimitMB,
		"search_presets":        cfg.SearchPresets,
		"search_token_enabled":  cfg.SearchTokenEnabled,
		"rename_undo_minutes":   cfg.RenameUndoMinutes,
		"traditional_enabled":   cfg.TraditionalEnabled,
		"temp_patterns":         cfg.TempPatterns,
		"noise_words":           cfg.NoiseWords,
		"video_enabled":         cfg.VideoEnabled,
		"intro_enabled":         cfg.IntroEnabled,
		"intro_seconds":         cfg.IntroSeconds,
		"task_guard_enabled":    cfg.TaskGuardEnabled,
		"video_workers":         cfg.VideoWorkers,
		"ignore_intro_seconds":  cfg.IgnoreIntroSeconds,
		"ignore_outro_seconds":  cfg.IgnoreOutroSeconds,
		"hardware_decode":       cfg.HardwareDecode,
		"audio_threshold":       cfg.AudioThreshold,
		"audio_window_seconds":  cfg.AudioWindowSeconds,
		"phash_hamming":         cfg.PHashHamming,
		"full_rescan_hours":     cfg.FullRescanHours,
		"poll_minutes":          cfg.PollMinutes,
		"hash_workers":          cfg.HashWorkers,
		"exclude_dirs":          cfg.ExcludeDirs,
		"exclude_paths":         cfg.ExcludePaths,
		"protect_paths":         cfg.ProtectPaths,
		"path_maps":             cfg.PathMaps,
		"photo_enabled":         cfg.PhotoEnabled,
		"img_hamming":           cfg.ImgHamming,
		"audio_enabled":         cfg.AudioEnabled,
		"unify_ext":             cfg.UnifyExt,
		"volumes":               cfg.Volumes,
		"ffmpeg":                cfg.FFmpeg,
		"ffprobe":               cfg.FFprobe,
		"log_dir":               cfg.LogDir,
		"cache_dir":             cfg.CacheDir,
		"sleep_friendly":        cfg.SleepFriendly,
		"quiet_hours":           cfg.QuietHours,
		"broken_auto":           cfg.BrokenAuto,
		"broken_idle_minutes":   cfg.BrokenAutoIdleMinutes,
		"broken_batch":          cfg.BrokenAutoBatch,
		"video_density":         cfg.VideoDensity,
		"wake_enabled":          cfg.WakeEnabled,
		"wake_window":           cfg.WakeWindow,
		"ai_enabled":            cfg.AIEnabled,
		"ai_endpoint":           cfg.AIEndpoint,
		"ai_model":              cfg.AIModel,
		"mem_budget_mb":         cfg.MemBudgetMB,
		"thumb_cache_max_mb":    cfg.ThumbCacheMaxMB,
		"op_log_enabled":        cfg.OpLogEnabled,
		"op_log_retention_days": cfg.OpLogRetentionDays,
		"op_log_reads":          cfg.OpLogReads,
		"op_log_detail_limit":   cfg.OpLogDetailLimit,
	}
}

// isValidHTTPURL 判断字符串是否为带主机名的 http/https 地址。
// v3.7.1 新增：配置校验用，不做网络访问，只看写法。
func isValidHTTPURL(s string) bool {
	for _, prefix := range []string{"http://", "https://"} {
		rest, ok := strings.CutPrefix(s, prefix)
		if !ok {
			continue
		}
		host := rest
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
		return host != "" && !strings.ContainsAny(host, " \t")
	}
	return false
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	// v3.10.10：必须显式给出扫描模式。此前解码失败被忽略，空 body 或超大 body
	// 会静默退化成全量扫描——一次探测请求就能把整盘扫描拉起来。
	var body struct {
		Mode  string `json:"mode"`
		Quick bool   `json:"quick"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "扫描请求需要 JSON 参数：mode 为 full 或 quick"})
		return
	}
	var mode engine.ScanMode
	switch body.Mode {
	case string(engine.ScanModeFull):
		mode = engine.ScanModeFull
	case string(engine.ScanModeQuick):
		mode = engine.ScanModeQuick
	case "":
		if !body.Quick {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "扫描请求需要 JSON 参数：mode 为 full 或 quick"})
			return
		}
		mode = engine.ScanModeQuick
	default:
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "扫描模式只能是 full 或 quick"})
		return
	}
	if err := s.e.StartScanMode(mode); err != nil {
		writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, modeResponse{OK: true, Mode: string(mode)})
}

func (s *Server) handleScanStop(w http.ResponseWriter, r *http.Request) {
	// v3.10.6：停止扫描会让写操作恢复可用，同样只接受 POST，避免跨站 GET 打断扫描。
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "停止扫描仅支持 POST"})
		return
	}
	s.e.StopScan()
	writeJSON(w, 200, okResponse{OK: true})
}

// boolParam 解析 URL 布尔参数；空值或非法值返回默认值。
// 接受 "1"/"true"/"on"/"yes"（不区分大小写）作为 true。
func boolParam(r *http.Request, name string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(name)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return def
}

// attachTagsForPage 给当前页分组补上标签值（v2.8.0），并按标签筛选条件裁剪（v2.9.0）。
// 标签查询失败只记日志，不影响主结果返回——标签是辅助信息。
func (s *Server) attachTagsForPage(r *http.Request, groups []store.Group) []store.Group {
	if len(groups) == 0 {
		return groups
	}
	if err := s.st.AttachFileTags(r.Context(), groups); err != nil {
		applog.Warn("附加标签值失败：%s", err.Error())
		return groups
	}
	q := groupQuery(r, s.cfg.Get())
	if q.TagName == "" {
		return groups
	}
	return filterGroupsByTag(groups, q.TagName, q.TagValue)
}

// filterGroupsByTag 只保留命中指定标签（可选指定值）的文件（v2.9.0）。
// 用于筛选器「按标签值过滤」；组内不足 2 个文件时丢弃整组，并重算统计。
func filterGroupsByTag(groups []store.Group, name, value string) []store.Group {
	out := make([]store.Group, 0, len(groups))
	for _, g := range groups {
		files := make([]store.FileRow, 0, len(g.Files))
		for _, f := range g.Files {
			for _, t := range f.Tags {
				if t.Name != name {
					continue
				}
				if value == "" || t.Value == value {
					files = append(files, f)
					break
				}
			}
		}
		if len(files) < 2 {
			continue
		}
		// v3.10.10（P2-10）：重算统计走 store.RecomputeStats，与实际占用、
		// 硬链接去重的口径保持一致，不再按逻辑大小简单相加。
		g.Files = files
		g.RecomputeStats()
		out = append(out, g)
	}
	return out
}

// tagCopySuffixRe 匹配文件名末尾的常见副本标记（copy/副本/备份/bak + 可选数字）。
// 相似名过滤用它归一，避免「某剧集.mkv」与「某剧集 copy.mkv」被判成不相似。
var tagCopySuffixRe = regexp.MustCompile(`(?i)\s*(?:copy|副本|备份|bak|duplicate)\s*\d*$`)

// folderMatches 判断文件所在目录是否满足"文件夹名 + 层级"过滤（v2.8.0）。
// depth=0 表示任意层级只要出现该目录名即可；depth>0 时从卷根算第 depth 段，
// 例如 /vol6/1000/<共享目录>/<子目录>/剧集/a.mkv 的 depth=5 是「剧集」。
func folderMatches(p, name string, depth int) bool {
	segs := strings.Split(strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/"), "/")
	if depth > 0 {
		if len(segs) <= depth {
			return false
		}
		return segs[depth-1] == name
	}
	for _, s := range segs {
		if s == name {
			return true
		}
	}
	return false
}

// filterGroupsByQuery 把引擎缓存的全量分组按 GroupQuery 过滤。
// 用于 video/intro/audio/fuzzy/semantic 这些不走 Store SQL 层的分组接口。
// 过滤范围：扩展名、关键词、大小上下限、修改时间（含 DateOnly）、SameDrive、
// MaxDurDiff（只影响 video/intro/audio 的候选对）、IgnoreSameFolder、
// FolderName/FolderDepth、SizeTolKB、SimilarName（v2.8.0）。
// 过滤后组内文件不足 2 个的组会被剔除。
func filterGroupsByQuery(groups []store.Group, q store.GroupQuery) []store.Group {
	// 无过滤条件直接返回
	hasFilter := q.MinSize > 0 || q.MaxSize > 0 || q.Ext != "" || len(q.Exts) > 0 || q.Keyword != "" ||
		q.Since > 0 || q.Until > 0 || q.SameDrive != "" || q.MaxDurDiff > 0 || q.IgnoreSameFolder ||
		q.FolderName != "" || q.FolderDepth > 0 || q.SimilarName || q.SizeTolKB > 0

	if !hasFilter {
		return groups
	}

	// 预计算卷前缀
	drivePrefix := ""
	if q.SameDrive != "" {
		drivePrefix = strings.TrimRight(q.SameDrive, "/") + "/"
	}

	out := make([]store.Group, 0, len(groups))
	for _, g := range groups {
		files := make([]store.FileRow, 0, len(g.Files))
		for _, f := range g.Files {
			// 扩展名过滤
			ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Name), "."))
			if q.Ext != "" && ext != strings.ToLower(strings.TrimPrefix(q.Ext, ".")) {
				continue
			}
			if len(q.Exts) > 0 {
				match := false
				for _, e := range q.Exts {
					if ext == strings.ToLower(e) {
						match = true
						break
					}
				}
				if !match {
					continue
				}
			}
			// 关键词（文件名）
			if q.Keyword != "" && !strings.Contains(strings.ToLower(f.Name), strings.ToLower(q.Keyword)) {
				continue
			}
			// 大小下限（v3.10.10：视频/片头页没有 SQL 层 min_size，必须在这里按文件卡）
			if q.MinSize > 0 && f.Size < q.MinSize {
				continue
			}
			// 大小上限（组内文件应该已经满足 min_size，这里额外卡 MaxSize）
			if q.MaxSize > 0 && f.Size > q.MaxSize {
				continue
			}
			// 同盘
			if drivePrefix != "" && !strings.HasPrefix(f.Path, drivePrefix) {
				continue
			}
			// 文件夹名 + 层级（v2.8.0）：只看指定层级上目录名相同的文件
			if q.FolderName != "" && !folderMatches(f.Path, q.FolderName, q.FolderDepth) {
				continue
			}
			// 时间（DateOnly 已在 SQL 层处理，这里只处理 Server 内存过滤的情况）
			if q.DateOnly {
				// v3.17.4（F-10）：SQL 侧用 date(...,'localtime') 截本地日，内存侧
				// 此前用 Truncate(24h) 截的是 UTC 日；UTC+8 下本地 0–8 点的文件
				// 会被算到前一天，两套口径对不上。
				if q.Since > 0 && localDayStart(f.Mtime).Before(localDayStart(q.Since)) {
					continue
				}
				if q.Until > 0 && localDayStart(f.Mtime).After(localDayStart(q.Until)) {
					continue
				}
			} else {
				if q.Since > 0 && f.Mtime < q.Since {
					continue
				}
				if q.Until > 0 && f.Mtime >= q.Until {
					continue
				}
			}
			files = append(files, f)
		}

		// IgnoreSameFolder：组内只保留目录互不相同的文件。
		// 语义：如果所有文件都在同一个目录下，整个组被丢弃；
		// 如果组里有跨目录的文件，剔除与保留文件同目录的冗余副本。
		if q.IgnoreSameFolder && len(files) >= 2 {
			// 按目录分桶
			buckets := map[string][]int{}
			for i, f := range files {
				buckets[filepath.Dir(f.Path)] = append(buckets[filepath.Dir(f.Path)], i)
			}
			// 每个目录桶只保留 ID 最小的文件
			var kept []store.FileRow
			for _, idxs := range buckets {
				minIdx := idxs[0]
				for _, idx := range idxs {
					if files[idx].ID < files[minIdx].ID {
						minIdx = idx
					}
				}
				kept = append(kept, files[minIdx])
			}
			sort.Slice(kept, func(i, j int) bool { return kept[i].Path < kept[j].Path })
			files = kept
		}

		// MaxDurDiff：组内所有文件时长互相之间的差必须 <= MaxDurDiff。
		// 如果有 Duration=0 的文件（没分析过），跳过此过滤——让用户看到原始分组。
		if q.MaxDurDiff > 0 && len(files) >= 2 {
			// 收集有效时长的文件
			var withDur []int
			var durations []float64
			for i, f := range files {
				if f.Duration > 0 {
					withDur = append(withDur, i)
					durations = append(durations, f.Duration)
				}
			}
			// 时长过滤只在所有组内文件都有 Duration 时生效
			if len(withDur) == len(files) && len(durations) >= 2 {
				minD, maxD := durations[0], durations[0]
				for _, d := range durations {
					if d < minD {
						minD = d
					}
					if d > maxD {
						maxD = d
					}
				}
				if maxD-minD > q.MaxDurDiff {
					// 整个组时长跨度超过容差，丢弃
					continue
				}
			}
		}

		// SizeTolKB：组内文件大小极差超过容差就整组丢弃（v2.8.0）。
		// 用途是排除"内容相同但码率差太多"的候选，与 MaxDurDiff 同理。
		if q.SizeTolKB > 0 && len(files) >= 2 {
			minS, maxS := files[0].Size, files[0].Size
			for _, f := range files[1:] {
				if f.Size < minS {
					minS = f.Size
				}
				if f.Size > maxS {
					maxS = f.Size
				}
			}
			if maxS-minS > int64(q.SizeTolKB)*1024 {
				continue
			}
		}

		// SimilarName：只保留文件名彼此相似的最大簇（v2.8.0）。
		// 复用 fname.NearGroups 的删字签名桶，避免逐个配对的 O(n²)。
		if q.SimilarName && len(files) >= 2 {
			stems := make([]string, len(files))
			for i, f := range files {
				// 先去掉末尾副本标记，避免「某剧集.mkv」与「某剧集 copy.mkv」被判为不相似
				stems[i] = tagCopySuffixRe.ReplaceAllString(fname.Normalize(fname.Stem(f.Name)), "")
			}
			var bestIdx []int
			for _, idxs := range fname.NearGroups(stems) {
				if len(idxs) > len(bestIdx) {
					bestIdx = idxs
				}
			}
			if len(bestIdx) < 2 {
				continue
			}
			kept := make([]store.FileRow, 0, len(bestIdx))
			for _, i := range bestIdx {
				kept = append(kept, files[i])
			}
			files = kept
		}

		if len(files) < 2 {
			continue
		}
		// 重算组统计（与实际占用、硬链接去重同口径，v3.10.10 P2-10）
		g.Files = files
		g.RecomputeStats()
		out = append(out, g)
	}
	return out
}

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	q := groupQuery(r, cfg)
	mode := r.URL.Query().Get("mode")
	kind := "exact"
	if mode == "name" {
		kind = "name"
	}
	if !matchModeAllows(cfg, kind) {
		writeModeDisabled(w, kind)
		return
	}
	var groups []store.Group
	var total int64
	var err error
	if mode == "name" {
		groups, total, err = s.st.NameGroups(r.Context(), q)
	} else {
		groups, total, err = s.st.ExactGroups(r.Context(), q)
	}
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	if groups == nil {
		groups = []store.Group{}
	}
	// 精确/同名组的目录名、大小条件已在 SQL 层生效；相似名和文件夹层级需要组内聚类/按段判断，
	// 只能在这里按当前页裁剪（v2.8.0 / v2.9.0），被剔除的组会让本页数量少于 page_size。
	if q.SimilarName || q.FolderDepth > 0 {
		before := len(groups)
		groups = filterGroupsByQuery(groups, q)
		// total 来自 SQL 粗筛；被内存过滤剔除的组要从总数里扣掉，
		// 否则会出现「total=1 但列表为空」的错觉（近似值，只修正当前页可见部分）。
		if removed := before - len(groups); removed > 0 {
			total -= int64(removed)
			if total < 0 {
				total = 0
			}
		}
	}
	beforeTag := len(groups)
	groups = s.attachTagsForPage(r, groups)
	// v3.4.0：cross=1 时补充「另一设备也有」标记（默认不加载，避免无谓开销）。
	if boolParam(r, "cross", false) {
		groups = s.attachPeers(r.Context(), groups)
	}
	// 标签值过滤同样会减少组数，total 也要跟着扣（v2.9.0）
	if q.TagName != "" {
		if removed := beforeTag - len(groups); removed > 0 {
			total -= int64(removed)
			if total < 0 {
				total = 0
			}
		}
	}
	writeJSON(w, 200, groupsResponse{Groups: groups, Total: int64(total), Page: q.Page, PageSize: q.PageSize})
}

func (s *Server) handleVideoGroups(w http.ResponseWriter, r *http.Request) {
	if !matchModeAllows(s.cfg.Get(), "video") {
		writeModeDisabled(w, "video")
		return
	}
	groups, err := s.e.VideoGroups(r.Context(), false)
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	cfg := s.cfg.Get()
	groups = filterGroupsByRoots(groups, cfg.Volumes)
	groups = filterGroupsByQuery(groups, groupQuery(r, cfg))
	if groups == nil {
		groups = []store.Group{}
	}
	groups = sortGroupsQ(groups, r)
	page := int(intParam(r, "page", 1, 1, 100000))
	// v3.26.0（F2）：与 /api/groups 同步放宽到 1000，默认值仍是 20。
	pageSize := int(intParam(r, "page_size", 20, 1, 1000))
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(groups) {
		start = len(groups)
	}
	if end > len(groups) {
		end = len(groups)
	}
	pageGroups := groups[start:end]
	pageGroups = s.attachTagsForPage(r, pageGroups)
	writeJSON(w, 200, groupsResponse{Groups: pageGroups, Total: int64(len(groups)), Page: page, PageSize: pageSize})
}

func (s *Server) handleIntroGroups(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	if !matchModeAllows(cfg, "intro") {
		writeModeDisabled(w, "intro")
		return
	}
	n := int(intParam(r, "n", int64(cfg.IntroSeconds), 1, 300))
	groups, err := s.e.IntroGroups(r.Context(), n, false)
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	groups = filterGroupsByRoots(groups, cfg.Volumes)
	groups = filterGroupsByQuery(groups, groupQuery(r, cfg))
	if groups == nil {
		groups = []store.Group{}
	}
	groups = sortGroupsQ(groups, r)
	analyzed, _ := s.st.IntroAnalyzedCount(r.Context(), n)
	pending, _ := s.st.CountIntroCandidates(r.Context(), n)
	page := int(intParam(r, "page", 1, 1, 100000))
	pageSize := int(intParam(r, "page_size", 20, 1, 200))
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(groups) {
		start = len(groups)
	}
	if end > len(groups) {
		end = len(groups)
	}
	pageGroups := groups[start:end]
	pageGroups = s.attachTagsForPage(r, pageGroups)
	writeJSON(w, 200, introGroupsResponse{Groups: pageGroups, Total: len(groups), Page: page, PageSize: pageSize, N: n, Analyzed: analyzed, Pending: pending})
}

func (s *Server) handleIntroReanalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Seconds int `json:"seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	cfg := s.cfg.Get()
	n := body.Seconds
	if n < 1 || n > 300 {
		n = cfg.IntroSeconds
	}
	if n < 1 || n > 300 {
		n = 10
	}
	if err := s.e.ReanalyzeIntro(r.Context(), n); err != nil {
		writeJSON(w, http.StatusConflict, countErrorResponse{Error: err.Error(), N: n})
		return
	}
	writeJSON(w, 200, nResponse{OK: true, N: n})
}

func (s *Server) handleVideoFailures(w http.ResponseWriter, r *http.Request) {
	items, err := s.e.VideoFailures(r.Context())
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	items = filterVideoFailuresByRoots(items, s.cfg.Get().Volumes)
	if items == nil {
		items = []store.VideoFailure{}
	}
	writeJSON(w, 200, itemsTotalResponse{Items: items, Total: len(items)})
}

func (s *Server) handleVideoReanalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		IDs []int64 `json:"ids"`
		All bool    `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	if len(body.IDs) == 0 && !body.All {
		writeJSON(w, 400, errorResponse{Error: "缺少 ids 或 all"})
		return
	}
	ids := body.IDs
	if body.All {
		ids = nil
	}
	if err := s.e.ReanalyzeVideos(r.Context(), ids); err != nil {
		writeJSON(w, 409, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, countOKResponse{OK: true, Count: len(ids)})
}

// scanBusy 在查重扫描进行中直接返回 409，并告知调用方已拒绝本次请求。
//
// v3.10.4：扫描先读到路径、用户随后移入隔离区或改名时，扫描收尾的 UpsertBatch
// 会把已移走的路径写回并置为 active，留下死行；这里与清理计划「扫描中不执行」
// 保持同一口径，写操作统一挡在入口。
func (s *Server) scanBusy(w http.ResponseWriter) bool {
	if !s.e.State().Running {
		return false
	}
	writeJSON(w, http.StatusConflict, errorResponse{Error: "扫描进行中，请稍后再操作"})
	return true
}

// handleTrash 是隔离区的入口：GET 返回清单（纯读），POST 把文件移入隔离区。
// v4.0.1（安全审查 问题3）：GET 不再顺手清理幽灵行（那是写库），改为纯读，
// 与 GET /api/trash/list 口径统一；幽灵行由引擎启动时的 HealTrashMaintenance 负责。
func (s *Server) handleTrash(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.writeTrashPage(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, 405, errorResponse{Error: "请求方法不支持"})
		return
	}
	if s.scanBusy(w) {
		return
	}
	var body struct {
		Paths     []string `json:"paths"`
		GroupKey  string   `json:"group_key"`
		GroupKeys []string `json:"group_keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	res, err := s.e.TrashFiles(r.Context(), body.Paths, s.trashGroups(body.GroupKey, body.GroupKeys))
	if err != nil {
		// v3.25.0（P1-1）：被闸门拒绝是「稍后再试」的冲突，不是参数错误。
		var ce *taskguard.ConflictError
		if errors.As(err, &ce) {
			writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, 400, errorResponse{Error: err.Error()})
		return
	}
	// v3.10.10（第五轮 P1-4）：已移走的条目立刻从结果快照剔除，
	// 否则结果页仍显示它们，下一次勾选还会用陈旧成员过 keep-one。
	s.snap.removePaths(res.Moved)
	writeJSON(w, 200, res)
}

// handleTrashList 是隔离区的纯只读清单入口（v3.26.0 F1）。
//
// 参数与 GET /api/trash 完全一致，但**不调用 HealTrashRows**，不写库也不动文件，
// 因此可以安全地用于自动化、监控与生产实例探测。旧接口行为保持不变。
func (s *Server) handleTrashList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, errorResponse{Error: "请求方法不支持"})
		return
	}
	s.writeTrashPage(w, r)
}

// writeTrashPage 是两条 GET 路由的共用实现，纯只读，不写库也不动文件（v4.0.1 安全审查 问题3）。
func (s *Server) writeTrashPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limitStr, cursor := q.Get("limit"), q.Get("cursor")
	if limitStr == "" && cursor == "" {
		// 老路径（v3.25.2）：不传分页参数时保持 v3.25.1 行为——一次返回全部。
		// 新增的 total / returned / has_more / next_cursor 只是追加字段，老调用方忽略即可。
		items, err := s.st.ListTrash(r.Context())
		if err != nil {
			writeJSON(w, 500, errorResponse{Error: err.Error()})
			return
		}
		if items == nil {
			items = []store.TrashRow{}
		}
		writeJSON(w, 200, trashListResponse{Items: items, Total: len(items), Returned: len(items), HasMore: false, NextCursor: ""})
		return
	}
	limit := store.TrashPageDefault
	if limitStr != "" {
		n, err := strconv.Atoi(limitStr)
		if err != nil || n < 1 || n > store.TrashPageMax {
			writeJSON(w, 400, errorResponse{Error: "分页参数 limit 必须是 1–500 的整数"})
			return
		}
		limit = n
	}
	page, err := s.st.ListTrashPage(r.Context(), cursor, limit)
	if err != nil {
		if errors.Is(err, store.ErrTrashCursor) {
			writeJSON(w, 400, errorResponse{Error: "分页游标无效，请刷新隔离区"})
			return
		}
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	kept, total := page.Rows, page.Total
	if kept == nil {
		kept = []store.TrashRow{}
	}
	writeJSON(w, 200, trashListResponse{Items: kept, Total: total, Returned: len(kept), HasMore: page.HasMore, NextCursor: page.NextCursor})
}

// trashGroups 组装引擎需要的分组入参：结果页跨组勾选传 group_keys，旧调用只传 group_key。
// 成员优先取结果页快照（规则类键只能这样解析），取不到则留空由引擎按分组键重算。
func (s *Server) trashGroups(single string, many []string) []engine.TrashGroup {
	keys := many
	if len(keys) == 0 && single != "" {
		keys = []string{single}
	}
	out := make([]engine.TrashGroup, 0, len(keys))
	seen := map[string]bool{}
	for _, k := range keys {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		members, _ := s.snap.groupMembers(k)
		out = append(out, engine.TrashGroup{Key: k, Members: members})
	}
	return out
}

func (s *Server) handleTrashPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	if s.scanBusy(w) {
		return
	}
	var body struct {
		Action    string   `json:"action"`
		Paths     []string `json:"paths"`
		IDs       []int64  `json:"ids"`
		GroupKey  string   `json:"group_key"`
		GroupKeys []string `json:"group_keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	switch body.Action {
	case "trash":
		items, err := s.e.PreviewTrash(r.Context(), body.Paths, s.trashGroups(body.GroupKey, body.GroupKeys))
		if err != nil {
			writeJSON(w, 400, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, 200, trashPreviewResponse{Action: body.Action, Items: items})
	case "restore":
		writeJSON(w, 200, trashPreviewResponse{Action: body.Action, Items: s.e.PreviewRestore(r.Context(), body.IDs)})
	case "delete":
		writeJSON(w, 200, trashPreviewResponse{Action: body.Action, Items: s.e.PreviewDelete(r.Context(), body.IDs)})
	default:
		writeJSON(w, 400, errorResponse{Error: "未知操作"})
	}
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	// v3.10.8（P1-1）：恢复也是写操作，扫描进行中执行会被扫描收尾把旧路径写回 active。
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "恢复仅支持 POST"})
		return
	}
	if s.scanBusy(w) {
		return
	}
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	res := s.e.Restore(r.Context(), body.IDs)
	// v3.25.2（P3-5）：闸门拒绝时一个文件都没恢复，按 409 回，别让界面显示成「0 个成功」。
	if res.Conflict != "" {
		writeJSON(w, http.StatusConflict, errorResponse{Error: res.Conflict})
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	// v3.17.3（F-17）：永久删除只接受 POST 且要求显式确认，与清空隔离区同口径。
	// 登录 Cookie 是 SameSite=Lax，顶层 GET 导航会带 Cookie，裸 GET 能被跨站链接直接触发。
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "永久删除仅支持 POST"})
		return
	}
	var body struct {
		IDs     []int64 `json:"ids"`
		Confirm bool    `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	if !body.Confirm {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "永久删除不可恢复，需显式确认（confirm=true）"})
		return
	}
	// 参数校验放在扫描状态检查之前：请求本身不合法时不必依赖引擎状态，响应也更稳定。
	if s.scanBusy(w) {
		return
	}
	res := s.e.DeletePermanent(r.Context(), body.IDs)
	if res.Conflict != "" {
		writeJSON(w, http.StatusConflict, errorResponse{Error: res.Conflict})
		return
	}
	writeJSON(w, 200, res)
}

// v3.10.6 安全修复：清空隔离区是永久删除，只接受 POST 且要求显式 confirm。
// 登录 Cookie 是 SameSite=Lax，顶层 GET 导航会带 Cookie，之前的裸 GET 能被跨站链接直接触发。
func (s *Server) handleEmptyTrash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "清空隔离区仅支持 POST"})
		return
	}
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.Confirm {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "清空隔离区不可恢复，需显式确认（confirm=true）"})
		return
	}
	n, errs := s.e.EmptyTrash(r.Context())
	writeJSON(w, 200, trashRemovedResponse{Removed: n, Errors: errs})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		MinSize            *int64                 `json:"min_size"`
		Watch              *bool                  `json:"watch"`
		VideoEnabled       *bool                  `json:"video_enabled"`
		IntroEnabled       *bool                  `json:"intro_enabled"`
		IntroSeconds       *int                   `json:"intro_seconds"`
		AudioThreshold     *float64               `json:"audio_threshold"`
		AudioWindowSeconds *int                   `json:"audio_window_seconds"`
		PHashHamming       *int                   `json:"phash_hamming"`
		AccessToken        *string                `json:"access_token"`
		FFmpeg             *string                `json:"ffmpeg"`
		FFprobe            *string                `json:"ffprobe"`
		FullRescanHours    *int                   `json:"full_rescan_hours"`
		PollMinutes        *int                   `json:"poll_minutes"`
		HashWorkers        *int                   `json:"hash_workers"`
		VideoWorkers       *int                   `json:"video_workers"`
		IgnoreIntro        *int                   `json:"ignore_intro_seconds"`
		IgnoreOutro        *int                   `json:"ignore_outro_seconds"`
		HardwareDecode     *bool                  `json:"hardware_decode"`
		ProtectPaths       []string               `json:"protect_paths"`
		PathMapsText       *string                `json:"path_maps_text"`
		PhotoEnabled       *bool                  `json:"photo_enabled"`
		ImgHamming         *int                   `json:"img_hamming"`
		AudioEnabled       *bool                  `json:"audio_enabled"`
		UnifyExt           *string                `json:"unify_ext"`
		SleepFriendly      *bool                  `json:"sleep_friendly"`
		QuietHours         *string                `json:"quiet_hours"`
		BrokenAuto         *bool                  `json:"broken_auto"`
		BrokenIdle         *int                   `json:"broken_auto_idle_minutes"`
		BrokenBatch        *int                   `json:"broken_auto_batch"`
		VideoDensity       *string                `json:"video_density"`
		WakeEnabled        *bool                  `json:"wake_enabled"`
		WakeWindow         *string                `json:"wake_window"`
		ContentEnabled     *bool                  `json:"content_enabled"`
		NameEnabled        *bool                  `json:"name_enabled"`
		ComputeDupFolders  *bool                  `json:"compute_dup_folders"`
		MatchMode          *string                `json:"match_mode"`
		MatchKinds         *[]string              `json:"match_kinds"`
		SearchRules        *config.SearchRules    `json:"search_rules"`
		SearchMemLimitMB   *int                   `json:"search_mem_limit_mb"`
		SearchPresets      *[]config.SearchPreset `json:"search_presets"`
		TraditionalEnabled *bool                  `json:"traditional_enabled"`
		TempPatterns       *[]string              `json:"temp_patterns"`
		NoiseWords         *[]string              `json:"noise_words"`
		ExcludePaths       *[]string              `json:"exclude_paths"`
		ExcludeDirs        *[]string              `json:"exclude_dirs"`
		Volumes            *[]string              `json:"volumes"`
		LogDir             *string                `json:"log_dir"`
		CacheDir           *string                `json:"cache_dir"`
		MemBudget          *int                   `json:"mem_budget_mb"`
		ThumbMaxMB         *int                   `json:"thumb_cache_max_mb"`
		AIEnabled          *bool                  `json:"ai_enabled"`
		AIEndpoint         *string                `json:"ai_endpoint"`
		AIModel            *string                `json:"ai_model"`
		OpLogEnabled       *bool                  `json:"op_log_enabled"`
		OpLogRetentionDays *int                   `json:"op_log_retention_days"`
		OpLogReads         *bool                  `json:"op_log_reads"`
		OpLogDetailLimit   *int                   `json:"op_log_detail_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	old := s.cfg.Get() // v3.7.0：保存前的完整快照，用于生成设置变更 diff
	cur := s.cfg.Get()
	oldIntroN := cur.IntroSeconds
	oldIgnoreIntro := cur.IgnoreIntroSeconds
	oldIgnoreOutro := cur.IgnoreOutroSeconds
	oldVideoDensity := cur.VideoDensity
	oldAudioWindow := cur.AudioWindowSeconds
	oldWatch := cur.Watch
	oldLogDir := cur.LogDir
	oldCacheDir := cur.CacheDir
	if body.MinSize != nil {
		cur.MinSize = *body.MinSize
	}
	if body.Watch != nil {
		cur.Watch = *body.Watch
	}
	if body.VideoEnabled != nil {
		cur.VideoEnabled = *body.VideoEnabled
	}
	if body.IntroEnabled != nil {
		cur.IntroEnabled = *body.IntroEnabled
	}
	if body.IntroSeconds != nil {
		cur.IntroSeconds = *body.IntroSeconds
	}
	if body.AudioThreshold != nil {
		cur.AudioThreshold = *body.AudioThreshold
	}
	if body.AudioWindowSeconds != nil {
		cur.AudioWindowSeconds = *body.AudioWindowSeconds
	}
	if body.PHashHamming != nil {
		cur.PHashHamming = *body.PHashHamming
	}
	if body.AccessToken != nil {
		v := strings.TrimSpace(*body.AccessToken)
		// v3.17.5（F-18）：令牌是唯一鉴权手段，非空时要求至少 16 位；
		// 留空表示关闭鉴权（前端已有二次确认）。
		if msg := validateTokenStrength(v); msg != "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: msg})
			return
		}
		cur.AccessToken = v
	}
	// v3.17.4（F-15）：ffmpeg / ffprobe 路径只接受可执行的普通文件，且不能在扫描卷内。
	// 此前可以指向任意可执行文件，等于给设置页开了一个"换程序执行"的入口。
	vols := cur.Volumes
	if len(vols) == 0 {
		vols = s.e.Volumes()
	}
	if body.FFmpeg != nil {
		v := strings.TrimSpace(*body.FFmpeg)
		if msg := checkToolPath("FFmpeg", v, vols); msg != "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: msg})
			return
		}
		cur.FFmpeg = v
	}
	if body.FFprobe != nil {
		v := strings.TrimSpace(*body.FFprobe)
		if msg := checkToolPath("FFprobe", v, vols); msg != "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: msg})
			return
		}
		cur.FFprobe = v
	}
	if body.FullRescanHours != nil {
		cur.FullRescanHours = *body.FullRescanHours
	}
	if body.PollMinutes != nil {
		cur.PollMinutes = *body.PollMinutes
	}
	if body.SleepFriendly != nil {
		cur.SleepFriendly = *body.SleepFriendly
	}
	if body.QuietHours != nil {
		// v3.7.1：格式非法直接报错，不再静默清空；否则界面显示保存成功、
		// 刷新后却弹回默认值，用户看不出哪里填错了。
		v := strings.TrimSpace(*body.QuietHours)
		if v != "" && !config.ParseQuietHours(v).Valid {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "静默时段格式应为 HH:MM-HH:MM"})
			return
		}
		cur.QuietHours = v
	}
	if body.ProtectPaths != nil {
		cur.ProtectPaths = config.NormalizePaths(body.ProtectPaths)
	}
	if body.PathMapsText != nil {
		// 路径映射文本逐行解析；格式非法返回 400 并带上行号，避免静默丢映射。
		pm, err := config.ParsePathMaps(*body.PathMapsText)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		cur.PathMaps = pm
	}
	if body.PhotoEnabled != nil {
		cur.PhotoEnabled = *body.PhotoEnabled
	}
	if body.AudioEnabled != nil {
		cur.AudioEnabled = *body.AudioEnabled
	}
	if body.UnifyExt != nil {
		switch *body.UnifyExt {
		case "off", "jpg", "jpeg":
			cur.UnifyExt = *body.UnifyExt
		}
	}
	if body.ImgHamming != nil {
		v := *body.ImgHamming
		if v < 0 {
			v = 0
		}
		if v > 64 {
			v = 64
		}
		cur.ImgHamming = v
	}
	if body.BrokenAuto != nil {
		cur.BrokenAuto = *body.BrokenAuto
	}
	if body.BrokenIdle != nil {
		v := *body.BrokenIdle
		if v < 1 {
			v = 1
		}
		if v > 720 {
			v = 720
		}
		cur.BrokenAutoIdleMinutes = v
	}
	if body.BrokenBatch != nil {
		v := *body.BrokenBatch
		if v < 10 {
			v = 10
		}
		if v > 5000 {
			v = 5000
		}
		cur.BrokenAutoBatch = v
	}
	if body.VideoDensity != nil {
		switch v := strings.TrimSpace(*body.VideoDensity); v {
		case "low", "medium", "high":
			cur.VideoDensity = v
		default:
			cur.VideoDensity = "medium"
		}
	}
	if body.WakeEnabled != nil {
		cur.WakeEnabled = *body.WakeEnabled
	}
	if body.WakeWindow != nil {
		// v3.7.1：与 quiet_hours 同口径，非法就报错而不是静默存坏值，
		// 否则用户填 23:00-7:00 会以为设置成功、实际唤醒窗口一直没生效。
		v := strings.TrimSpace(*body.WakeWindow)
		if v != "" && !config.ParseQuietHours(v).Valid {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "唤醒时段格式应为 HH:MM-HH:MM"})
			return
		}
		cur.WakeWindow = v
	}
	if body.HashWorkers != nil {
		cur.HashWorkers = *body.HashWorkers
	}
	if body.VideoWorkers != nil {
		cur.VideoWorkers = *body.VideoWorkers
	}
	if body.IgnoreIntro != nil {
		cur.IgnoreIntroSeconds = *body.IgnoreIntro
	}
	if body.IgnoreOutro != nil {
		cur.IgnoreOutroSeconds = *body.IgnoreOutro
	}
	if body.HardwareDecode != nil {
		cur.HardwareDecode = *body.HardwareDecode
	}
	if body.ContentEnabled != nil {
		cur.ContentEnabled = *body.ContentEnabled
	}
	if body.NameEnabled != nil {
		cur.NameEnabled = *body.NameEnabled
	}
	if body.ComputeDupFolders != nil {
		cur.ComputeDupFolders = *body.ComputeDupFolders
	}
	if body.MatchMode != nil {
		cur.MatchMode = config.NormalizeMatchMode(*body.MatchMode)
	}
	if body.MatchKinds != nil {
		cur.MatchKinds = config.NormalizeMatchKinds(*body.MatchKinds)
	}
	if body.SearchRules != nil {
		cur.SearchRules = body.SearchRules
	}
	if body.SearchMemLimitMB != nil {
		cur.SearchMemLimitMB = *body.SearchMemLimitMB
	}
	if body.SearchPresets != nil {
		cur.SearchPresets = *body.SearchPresets
	}
	if body.TraditionalEnabled != nil {
		cur.TraditionalEnabled = *body.TraditionalEnabled
	}
	if body.TempPatterns != nil {
		cur.TempPatterns = health.SanitizeTempPatterns(*body.TempPatterns)
	}
	if body.NoiseWords != nil {
		cur.NoiseWords = fname.SanitizeNoiseWords(*body.NoiseWords)
	}
	if body.ExcludePaths != nil {
		cur.ExcludePaths = cleanRootList(*body.ExcludePaths)
	}
	if body.ExcludeDirs != nil {
		cur.ExcludeDirs = *body.ExcludeDirs
	}
	if body.Volumes != nil {
		cur.Volumes = *body.Volumes
	}
	// 日志/缓存目录先验证可写，避免保存成功后才发现路径无效、日志或缩略图静默丢失。
	// v3.10.4：实际切换放到配置落盘之后——先切日志再写盘失败会留下「日志搬走了、配置没保存」的错位。
	if body.LogDir != nil {
		v := strings.TrimSpace(*body.LogDir)
		if v != "" {
			if err := ensureWritableDir(v); err != nil {
				writeJSON(w, 400, errorResponse{Error: "日志目录不可写：" + err.Error()})
				return
			}
		}
		cur.LogDir = v
	}
	if body.CacheDir != nil {
		v := strings.TrimSpace(*body.CacheDir)
		if v != "" {
			if err := ensureWritableDir(v); err != nil {
				writeJSON(w, 400, errorResponse{Error: "缓存目录不可写：" + err.Error()})
				return
			}
		}
		cur.CacheDir = v
	}
	if body.MemBudget != nil {
		cur.MemBudgetMB = *body.MemBudget
	}
	if body.ThumbMaxMB != nil {
		cur.ThumbCacheMaxMB = *body.ThumbMaxMB
	}
	// v3.3.0：可选 AI 连接器（仅生成理由，不参与判定）
	if body.AIEnabled != nil {
		cur.AIEnabled = *body.AIEnabled
	}
	if body.AIEndpoint != nil {
		// v3.7.1：AI 连接器按完整 URL 发请求，只填主机名会在每次调用时失败，
		// 因此保存前就拦下来，避免「保存成功但功能永远不工作」。
		v := strings.TrimSpace(*body.AIEndpoint)
		if v != "" && !isValidHTTPURL(v) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "AI 接口地址应为完整的 http:// 或 https:// 地址"})
			return
		}
		cur.AIEndpoint = v
	}
	if body.AIModel != nil {
		cur.AIModel = strings.TrimSpace(*body.AIModel)
	}
	if body.OpLogEnabled != nil {
		cur.OpLogEnabled = *body.OpLogEnabled
	}
	if body.OpLogRetentionDays != nil {
		cur.OpLogRetentionDays = *body.OpLogRetentionDays
	}
	if body.OpLogReads != nil {
		cur.OpLogReads = *body.OpLogReads
	}
	if body.OpLogDetailLimit != nil {
		cur.OpLogDetailLimit = *body.OpLogDetailLimit
	}
	if err := s.cfg.Replace(cur); err != nil {
		// v3.17.3（F-02）：配置值不合法属于请求参数问题，返回 400；
		// 其余（序列化 / 落盘失败）仍按 500 处理，便于区分故障来源。
		status := 500
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			status = 400
		}
		writeJSON(w, status, errorResponse{Error: err.Error()})
		return
	}
	// 配置已落盘，再切换日志输出；切换失败只告警，不把已保存的设置报成失败。
	if body.LogDir != nil {
		if err := applog.Switch(cur.LogDir); err != nil {
			applog.Warn("切换日志目录失败：%v", err)
		}
	}
	// v3.8.0：规则/扫描目录/最小文件大小变化后，旧快照立即作废，避免展示过期分组。
	if rulesChanged(old, cur) {
		s.snap.invalidate()
	}
	// v3.7.0：设置变更逐项留痕，令牌等敏感字段只记「已修改」不记值。
	if details := configDiffDetails(old, cur); len(details) > 0 {
		e := oplog.New("settings_change")
		e.Summary = fmt.Sprintf("修改设置 %d 项", len(details))
		e.TargetCount = len(details)
		e.Details = details
		s.recordOp(r, e)
	}
	// v1.31：内存预算/缩略图上限可能变化，立即生效
	s.applyCacheBudget()
	if body.CacheDir != nil {
		_ = os.MkdirAll(s.cacheDir(), 0o750)
		perm.Tighten(s.cacheDir(), 0o750)
	}
	if cur.LogDir != oldLogDir || cur.CacheDir != oldCacheDir {
		applog.Info("目录设置已更新：日志文件 %s，缩略图缓存 %s", applog.Path(), s.cacheDir())
	}
	s.e.RunnerReload()
	introReset := false
	if cur.IntroSeconds != oldIntroN {
		// N 变化后旧片头指纹不再有意义，清空等待下次扫描重算
		if _, err := s.st.ResetIntroAnalysis(r.Context()); err == nil {
			introReset = true
			s.e.MarkVideoDirty()
		}
	}
	// 忽略区间或帧密度改变后旧抽帧结果失效，清空视频指纹等待重新分析。
	// v3.10.8（P2-13）：以前漏了 VideoDensity，3/6/10 帧混存会让 groups.go
	// 按帧序号比较时错位，相似视频/整段重复判定跟着失真。
	videoReset := false
	if cur.IgnoreIntroSeconds != oldIgnoreIntro || cur.IgnoreOutroSeconds != oldIgnoreOutro || cur.VideoDensity != oldVideoDensity {
		if _, err := s.st.ResetVideoAnalysis(r.Context(), nil); err == nil {
			videoReset = true
			s.e.MarkVideoDirty()
		}
	}
	// 从关闭切换到启用监控时补一次扫描，避免关闭期间的文件变更永久漏索引。
	if body.Watch != nil && *body.Watch && !oldWatch {
		_ = s.e.StartScan()
	}
	// 音频比较时长变化后旧指纹不可比较，清空等待重算（v2.3.0）。
	audioReset := false
	if cur.AudioWindowSeconds != oldAudioWindow {
		if err := s.st.MigrateAudioWindow(r.Context(), cur.AudioWindowSeconds); err == nil {
			audioReset = true
		}
	}
	writeJSON(w, 200, watchRestartResponse{OK: true, RestartWatch: body.Volumes != nil || (body.Watch != nil && *body.Watch != oldWatch), IntroReset: introReset, VideoReset: videoReset, AudioReset: audioReset})
}

// handleAuthToken 把当前访问令牌返回给已登录用户（v3.7.2，设置页「查看」用）。
// 未启用令牌时返回空串；响应禁止缓存，避免令牌被浏览器或中间代理留存。
// v3.10.8（SEC-3）：要求 X-Requested-With 标记——跨站的图片、链接、表单导航都设不了自定义头，
// 令牌不会因为一次误点链接就落到第三方页面；同源页面仍可正常查看与复制。
func (s *Server) handleAuthToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	if r.Header.Get("X-Requested-With") != "verify" {
		writeJSON(w, http.StatusForbidden, errorResponse{Error: "缺少请求标记"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, tokenResponse{Token: s.cfg.Get().AccessToken})
}

// handleFSList 为设置页的目录选择弹窗列出子目录，只允许访问 /vol* 与数据目录。
func (s *Server) handleFSList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	// v1.41.3：展示入口与白名单同源（显式配置 ∪ trimacl 自动发现 ∪ 数据目录）
	path := r.URL.Query().Get("path")
	roots := s.browseRoots()
	// v3.5.3：飞牛授权了子目录（如 /vol3/1000/<共享目录>/<子目录>）时，共享根不可读、
	// 无法从 /fs 枚举子目录，自动发现看不到。允许用户手动输入已授权路径：
	// 服务端先做 /vol* + trimacl + 挂载类型探测，通过后临时并入本次白名单并记住，
	// 之后自动出现在两处目录选择器的根列表；未通过则明确拒绝，不放宽任何路径校验。
	if path != "" && !pathUnderRoots(filepath.Clean(path), roots) {
		if !fsbrowse.AuthorizedPath(path) {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "该路径未在飞牛系统设置中授权，或不是本地存储目录"})
			return
		}
		clean := filepath.Clean(path)
		roots = append(roots, clean)
		if err := s.cfg.AddBrowseRoot(clean); err != nil {
			applog.Warn("记住授权目录失败：%s", err.Error())
		}
	}
	list, err := fsbrowse.List(roots, s.authzRoots(), path)
	if err != nil {
		writeJSON(w, 400, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, list)
}

// handleFSMkdir 在允许范围内新建文件夹，供日志/缓存/扫描目录选择使用。
func (s *Server) handleFSMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Parent string `json:"parent"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	path, err := fsbrowse.Mkdir(s.browseRoots(), body.Parent, body.Name)
	if err != nil {
		writeJSON(w, 400, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, pathResponse{OK: true, Path: path})
}

// handleMetric 接收前端性能埋点，只写日志不落库，避免增加数据库负担。
func (s *Server) handleMetric(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Name   string `json:"name"`
		MS     int64  `json:"ms"`
		Detail string `json:"detail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 64 {
		writeJSON(w, 400, errorResponse{Error: "指标名称无效"})
		return
	}
	if body.MS < 0 {
		body.MS = 0
	}
	detail := strings.TrimSpace(body.Detail)
	if len(detail) > 200 {
		detail = detail[:200]
	}
	applog.Info("界面指标：%s=%dms %s", body.Name, body.MS, detail)
	writeJSON(w, 200, okResponse{OK: true})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode    string   `json:"mode"`
		Format  string   `json:"format"` // dc5（默认）/ readable
		Scope   string   `json:"scope"`  // all（默认）/ marked 仅标记行
		Marked  []string `json:"marked"`
		MinSize int64    `json:"min_size"`
		MaxSize int64    `json:"max_size"`
		Ext     string   `json:"ext"`
		Exts    []string `json:"exts"`
		Keyword string   `json:"q"`
		Since   int64    `json:"since"`
		Until   int64    `json:"until"`
		N       int      `json:"n"`
	}
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.Mode == "" {
		body.Mode = r.URL.Query().Get("mode")
	}
	if body.Mode == "" {
		body.Mode = "exact"
	}
	if body.Format == "" {
		body.Format = "dc5"
	}
	if body.Format != "dc5" && body.Format != "readable" {
		writeJSON(w, 400, errorResponse{Error: "无效的导出格式，可选 dc5 / readable"})
		return
	}
	onlyMarked := body.Scope == "marked"
	marked := map[string]bool{}
	for _, p := range body.Marked {
		marked[filepath.Clean(p)] = true
	}
	if onlyMarked && len(marked) == 0 {
		writeJSON(w, 400, errorResponse{Error: "未选择任何标记文件，无法按「仅标记行」导出"})
		return
	}
	groups, truncated, err := s.collectGroups(r.Context(), body.Mode, store.GroupQuery{
		MinSize: body.MinSize,
		MaxSize: body.MaxSize,
		Ext:     body.Ext,
		Exts:    body.Exts,
		Keyword: body.Keyword,
		Since:   body.Since,
		Until:   body.Until,
	}, body.N, reportMaxGroups)
	if err != nil {
		writeCollectError(w, err)
		return
	}
	name := fmt.Sprintf("verify-%s-%s-%s.csv", body.Format, body.Mode, time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if truncated {
		// v3.10.8（P2-9）：DC5 格式不能插入说明行（会破坏导入契约），
		// 因此把截断事实放在响应头并记日志，界面可读该头提示用户。
		w.Header().Set("X-Verify-Truncated", "true")
		w.Header().Set("X-Verify-Group-Limit", fmt.Sprintf("%d", reportMaxGroups))
		applog.Warn("导出结果超过 %d 组，已截断（模式 %s，格式 %s）", reportMaxGroups, body.Mode, body.Format)
	}
	opt := s.exportOptions(marked, onlyMarked)
	// v3.10.8（P2-7）：未勾选且导出全部行时传 nil，让 walkRows 走「每组保留第一个、
	// 其余标记」的默认规则；否则标记列整列为 0，用户无从判断哪份是副本。
	// 以前只有 body.Marked 字段缺省（nil）才生效，前端显式传空数组就绕过了。
	if !onlyMarked && len(marked) == 0 {
		opt.Marked = nil
	}
	s.applyPathMapping(&opt, body.Format)
	if body.Format == "dc5" {
		err = export.WriteDC5CSV(w, groups, opt)
	} else {
		err = export.WriteReadableCSV(w, groups, opt)
	}
	if err != nil {
		return
	}
}

// rawInlineTypes 是 /api/raw 允许浏览器内联打开的扩展名白名单（v3.10.6 SEC-2）。
// 之前用 mime.TypeByExtension 直接把扩展名映射成 Content-Type，共享目录里放一个 .html
// 或 .svg 就能以应用自身的源执行脚本，再调 /api/auth/token 拿到明文令牌（同源存储型 XSS）。
// 这里只放行媒体与字幕；白名单外的文件一律附件下载并加 nosniff。
var rawInlineTypes = map[string]string{
	// 图片（与前端 IMG_EXT 对齐，SVG 刻意排除：它可以内嵌脚本）
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".gif": "image/gif", ".bmp": "image/bmp", ".webp": "image/webp",
	".heic": "image/heic", ".avif": "image/avif", ".tif": "image/tiff", ".tiff": "image/tiff",
	// 视频（与前端 VID_EXT 对齐）
	".mp4": "video/mp4", ".m4v": "video/mp4", ".mkv": "video/x-matroska",
	".webm": "video/webm", ".avi": "video/x-msvideo", ".mov": "video/quicktime",
	".wmv": "video/x-ms-wmv", ".flv": "video/x-flv", ".ts": "video/mp2t",
	".m2ts": "video/mp2t", ".mts": "video/mp2t", ".mpg": "video/mpeg", ".mpeg": "video/mpeg",
	".3gp": "video/3gpp", ".ogv": "video/ogg", ".vob": "video/mpeg",
	".rmvb": "application/vnd.rn-realmedia-vbr", ".rm": "application/vnd.rn-realmedia",
	".asf": "video/x-ms-asf", ".divx": "video/divx", ".f4v": "video/mp4",
	".m2v": "video/mpeg", ".mxf": "application/mxf", ".dat": "video/mpeg",
	// 音频
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".aac": "audio/aac",
	".flac": "audio/flac", ".wav": "audio/wav", ".ogg": "audio/ogg",
	".opus": "audio/opus", ".ape": "audio/x-ape", ".wma": "audio/x-ms-wma",
	// 字幕按纯文本给，浏览器不会执行
	".srt": "text/plain; charset=utf-8", ".ass": "text/plain; charset=utf-8",
	".ssa": "text/plain; charset=utf-8", ".vtt": "text/vtt; charset=utf-8",
}

func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	// v3.10.10（P2-10）：用解析后的真实路径打开，消除「校验通过后被换成软链接」的窗口。
	p, ok := s.e.ResolveFilePath(r.URL.Query().Get("path"))
	if !ok {
		http.Error(w, "无效路径", http.StatusForbidden)
		return
	}
	f, err := os.Open(p) // #nosec G304 G703 -- 路径已通过 Engine.ResolveFilePath 的卷范围与软链接校验
	if err != nil {
		http.Error(w, "无法打开文件", 404)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		http.Error(w, "无法读取文件", 500)
		return
	}
	base := filepath.Base(p)
	ctype, inline := rawInlineTypes[strings.ToLower(filepath.Ext(base))]
	if inline {
		w.Header().Set("Content-Type", ctype)
	} else {
		// 白名单外的类型（html / svg / js / 未知扩展名）只允许下载，不允许在本站源内渲染。
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": base}))
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	// v3.10.10（P2-10）：缩略图缓存键也改用解析后的真实路径，与 raw 同口径。
	p, ok := s.e.ResolveFilePath(r.URL.Query().Get("path"))
	if !ok {
		http.Error(w, "无效路径", http.StatusForbidden)
		return
	}
	at := float64(intParam(r, "t", 0, 0, 1<<20))
	b, err := s.thumbBytes(r.Context(), p, at)
	if err != nil {
		// v3.10.8（SEC-8）：ffmpeg/os 原文只写日志，回显统一收敛
		applog.Warn("生成缩略图失败（%s）：%v", p, err)
		http.Error(w, "无法生成缩略图："+publicErrorText(err.Error()), 500)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=86400")
	// 缩略图内容由 ffmpeg 输出并固定声明为 image/jpeg，加 nosniff 防止浏览器按内容猜类型执行。
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(b) // #nosec G705 -- 响应体是 JPEG 字节流，Content-Type 与 nosniff 均已固定
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "不支持流式响应", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := s.e.Bus.Subscribe()
	defer s.e.Bus.Unsubscribe(ch)
	// 写事件失败说明客户端已断开，直接结束推送。
	if _, err := fmt.Fprintf(w, "event: hello\ndata: {}\n\n"); err != nil {
		return
	}
	fl.Flush()
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			payload, _ := json.Marshal(ev.Data)
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, payload); err != nil {
				return
			}
			fl.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleDiagAuthz 授权诊断（v1.41.7）：返回 /fs 视图枚举与逐路径探测结果，
// 用于核对"飞牛授权 → 应用可读"的对应关系。GET /api/diag/authz。
func (s *Server) handleDiagAuthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	writeJSON(w, 200, fsbrowse.Diagnose())
}
