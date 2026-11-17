// API 响应契约 —— 领域分组（v4.0.0）
//
// 本文件按业务域组织响应结构。每个 struct 对应一个或一组接口的响应形态。
// 命名约定：<语义>Response。
//
// 通用约定：
//   - 字段名（json tag）是前后端唯一真相源，改 tag 必须同步 webtypes/types.d.ts；
//   - 可选字段用 omitempty，且与前端 interface 的可选标记一致；
//   - 不改变任何既有键名与取值语义，保证自动化调用方不受影响。
package web

import (
	"verify/internal/engine"
	"verify/internal/rules"
	"verify/internal/search"
	"verify/internal/store"
)

// ── 状态与任务 ──────────────────────────────────────────────────────────

// statusResponse 是 GET /api/status 的响应。
//
// 这是前端启动时拉取的最大一个接口：一次拿回版本、配置摘要、任务状态、
// 引擎状态与卷列表。
//
// v4.0.0 策略说明：只给响应的「最外层容器」做类型化，
// 内部嵌套（hash / paths / ffmpeg / config / state）保留现有的 map 与 view 函数。
//
// 为什么不全扁平化：
//   - 内部嵌套是同构的 map（如 paths 下的 data_dir/cache_dir/log_file），
//     它们由 statusConfigView / RunnerSnapshot 等既有 view 函数产出，
//     压成 struct 会牵动这些函数与 status_test.go，收益远小于风险；
//   - 本次重构要解决的是「最外层字段名没有编译期保障」这一核心问题，
//     嵌套部分留待后续按需推进。
//
// 未登录时（s.authed(r) 为假）走另一个更小的响应体 statusUnauthResponse。
type statusResponse struct {
	// 扫描/任务状态
	State  any  `json:"state"`
	Paused bool `json:"paused"`
	// ExclusiveTask 描述当前占用独占槽的长任务（扫描/转码/健康检测等），无则为空。
	//
	// 不加 omitempty：原 map 里这个键总是存在，无任务时值为 null。
	// 加了 omitempty 会让 null 整个消失，前端读 state.exclusive_task 会拿到 undefined。
	ExclusiveTask any `json:"exclusive_task"`
	// HealthRunning 标记健康检测是否在跑（v3.10.8 P2-3）。
	HealthRunning bool `json:"health_running"`
	// FFMpegRunning 标记 ffmpeg 是否正被视频/片头/裁剪/转码占用（v3.12.0）。
	FFmpegRunning bool `json:"ffmpeg_running"`

	Counts  any `json:"counts"`
	Volumes any `json:"volumes"`

	Version string `json:"version"`

	// Cleanup 是清理任务状态摘要。
	Cleanup any `json:"cleanup"`

	// Auth 是鉴权状态。
	Auth any `json:"auth"`

	// Config 是设置页回显用的配置视图。
	Config any `json:"config"`

	// Hash 是哈希算法信息（algo / level / quick_chunk）。
	Hash any `json:"hash"`

	// Paths 是运行期路径（data_dir / cache_dir / log_file）。
	Paths any `json:"paths"`

	// FFmpeg 是外部依赖信息（available / ffmpeg / ffprobe / hw_mode / render_node / guc）。
	FFmpeg any `json:"ffmpeg"`
}

// searchSQLResponse 是 GET /api/search 的响应（数据库模糊匹配路径）。
//
// 覆盖：POST/GET /api/search
//
// 注意：files 字段是 res.Items 的别名，保留旧 /api/search 的字段名，
// 兼容已有脚本调用。
type searchSQLResponse struct {
	Total     int           `json:"total"`
	TookMs    int64         `json:"took_ms"`
	Truncated bool          `json:"truncated"`
	Degraded  bool          `json:"degraded"`
	Ready     bool          `json:"ready"`
	Items     []search.Item `json:"items"`
	// NextCursor 是下一页游标；空串表示已经到底。
	NextCursor string `json:"next_cursor"`
	// Files 保留旧 /api/search 的字段名，兼容已有脚本调用。
	Files []search.Item `json:"files"`
	// Notice 非空表示回退到数据库模糊匹配，前端据此提示用户。
	Notice string `json:"notice,omitempty"`
}

// ruleSearchStatusResponse 是 GET /api/rule-search/status 的响应。
//
// 与 searchStatusResponse（索引状态）不是一回事：
// 这里描述的是「规则搜索快照」的状态机，由 internal/web/snapshot.go 产出。
type ruleSearchStatusResponse struct {
	ID        string       `json:"id"`
	State     string       `json:"state"`
	Error     string       `json:"error"`
	Stale     bool         `json:"stale"`
	Loaded    int64        `json:"loaded"`
	Total     int64        `json:"total"`
	Totals    rules.Totals `json:"totals"`
	CreatedAt int64        `json:"created_at"`
}

// oplogListResponse 是 GET /api/oplog 的响应（操作记录分页）。
type oplogListResponse struct {
	Items    any `json:"items"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	// NextBeforeID 是下一页游标：满页时给出最后一条的 ID，
	// 前端拿它再请求即可跳过 OFFSET。为 0 时不返回（omitempty）。
	NextBeforeID int64 `json:"next_before_id,omitempty"`
}

// statusUnauthResponse 是未登录时 GET /api/status 的响应。
//
// v3.10.8（SEC-8 延伸）：未登录时只下发登录页需要的字段，
// 不返回数据目录、日志路径、ffmpeg 路径与完整配置。
//
// State 只在引擎已就绪时下发（omitempty + any：nil 会被省略，
// 与原 map 的 out["state"] = ... 条件添加等价）。
type statusUnauthResponse struct {
	Version string `json:"version"`
	Auth    any    `json:"auth"`
	State   any    `json:"state,omitempty"`
}

// ── 搜索 ────────────────────────────────────────────────────────────────

// tasksResponse 是 GET /api/tasks 的响应。
//
// running / history 两项由 engine.TaskSnapshot() 直接给出，形状由 engine 决定，
// 这里只给最外层容器做类型化。
type tasksResponse struct {
	Running any `json:"running"`
	History any `json:"history"`
}

// ── 搜索 ────────────────────────────────────────────────────────────────

// searchStatusResponse 是搜索索引状态与搜索结果状态的统一响应。
//
// 覆盖：
//
//	GET /api/rule-search/status  规则搜索结果（带 total/items）
//	GET /api/search/status        索引状态（只有索引字段，没有 total/items）
//
// Total 与 Items 是 omitempty：索引状态接口不返回它们。
type searchStatusResponse struct {
	Ready      bool          `json:"ready"`
	Total      int64         `json:"total,omitempty"`
	Items      []search.Item `json:"items,omitempty"`
	Building   bool          `json:"building"`
	BuiltAt    int64         `json:"built_at"`
	Degraded   bool          `json:"degraded"`
	Entries    int           `json:"entries"`
	EstBytes   int64         `json:"est_bytes"`
	MaxEntries int           `json:"max_entries"`
}

// searchResultsResponse 是 GET /api/rule-search/results 的响应。
type searchResultsResponse struct {
	Total      int           `json:"total"`
	Items      []search.Item `json:"items"`
	NextOffset int           `json:"next_offset"`
	TookMs     int64         `json:"took_ms"`
}

// searchSelectionResponse 是 GET /api/search 的响应（选择助手用）。
type searchSelectionResponse struct {
	Token        string `json:"token"`
	Count        int    `json:"count"`
	IndexBuiltAt int64  `json:"index_built_at"`
	DataVersion  int64  `json:"data_version"`
}

// searchFacetsResponse 是 GET /api/rule-search/facets 的响应。
type searchFacetsResponse struct {
	State  string       `json:"state"`
	Totals rules.Totals `json:"totals"`
}

// searchNotReadyResponse 是「搜索结果未就绪」时的响应。
//
// v4.0.0：这是带 error 的响应，需要参与错误收敛（实现 errorPayload）。
type searchNotReadyResponse struct {
	Error  string `json:"error"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

func (e searchNotReadyResponse) errorText() string { return e.Error }

func (e searchNotReadyResponse) withError(clean string) any {
	e.Error = clean
	return e
}

// ── 健康检测 ────────────────────────────────────────────────────────────

// healthStatusResponse 是 GET /api/health/status 的响应。
//
// progress / counts / uncertain / probe_cache 的内部形状由 engine 与 store 决定，
// 这里只给最外层容器做类型化。
type healthStatusResponse struct {
	Progress   any `json:"progress"`
	Counts     any `json:"counts"`
	Uncertain  any `json:"uncertain"`
	FFmpeg     any `json:"ffmpeg"`
	LastAudit  any `json:"last_audit"`
	Auto       any `json:"auto"`
	ProbeCache any `json:"probe_cache"`
}

// healthResultsResponse 是 GET /api/health/results 的响应。
type healthResultsResponse struct {
	Kind  string `json:"kind"`
	Total int64  `json:"total"`
	Items any    `json:"items"`
}

// ── 标签 ────────────────────────────────────────────────────────────────

// videoTagsResponse 是 GET /api/video_tags 的响应。
type videoTagsResponse struct {
	Tags []store.VideoTag `json:"tags"`
}

// videoTagSavedResponse 是保存视频标签后的响应。
type videoTagSavedResponse struct {
	OK    bool  `json:"ok"`
	ID    int64 `json:"id"`
	Count int64 `json:"count,omitempty"`
	// Warning 非空表示标签已保存但重算失败（部分成功）。
	Warning string `json:"warning,omitempty"`
}

// ── 导入 / 同步 ─────────────────────────────────────────────────────────

// importSessionsResponse 是 GET /api/import/sessions 的响应。
type importSessionsResponse struct {
	Sessions []store.ImportSession `json:"sessions"`
}

// importSessionResponse 是 GET /api/import/sessions?id= 的响应。
type importSessionResponse struct {
	Session any `json:"session"`
}

// importGroupsResponse 是 GET /api/import/groups?session_id= 的响应。
type importGroupsResponse struct {
	Session     any                 `json:"session"`
	Groups      []store.ImportGroup `json:"groups"`
	TotalGroups int                 `json:"total_groups"`
}

// importResultResponse 是 POST /api/import 的响应。
type importResultResponse struct {
	Session   any      `json:"session"`
	Total     int64    `json:"total"`
	Groups    int64    `json:"groups"`
	Matched   int64    `json:"matched"`
	Unmatched int64    `json:"unmatched"`
	Warnings  []string `json:"warnings,omitempty"`
}

// syncImportResponse 是 POST /api/sync/import 的响应。
type syncImportResponse struct {
	Imported int    `json:"imported"`
	Invalid  int    `json:"invalid"`
	Machine  string `json:"machine"`
}

// syncPeersResponse 是 GET /api/sync/peers 的响应。
type syncPeersResponse struct {
	Peers   []store.SyncPeer `json:"peers"`
	Machine string           `json:"machine"`
}

// ── 缓存 ────────────────────────────────────────────────────────────────

// cacheStatsResponse 是 GET /api/cache/stats 的响应。
type cacheStatsResponse struct {
	Media         string `json:"media"`
	SSDHint       bool   `json:"ssd_hint"`
	CacheDir      string `json:"cache_dir"`
	Classes       any    `json:"classes"`
	MemBudgetAuto bool   `json:"mem_budget_auto"`
	MemBudgetMB   int    `json:"mem_budget_mb"`
	ThumbMaxMB    int    `json:"thumb_max_mb"`
}

// cacheClearedResponse 是清理缓存后的响应。
type cacheClearedResponse struct {
	OK      bool  `json:"ok"`
	Cleared int64 `json:"cleared"`
}

// ── 任务执行结果 ────────────────────────────────────────────────────────

// moveResultResponse 是移动类操作的响应。
type moveResultResponse struct {
	Moved  []string `json:"moved"`
	Failed []string `json:"failed"`
}

// transcodeStatusResponse 是 GET /api/transcode/status 的响应。
type transcodeStatusResponse struct {
	Queued  int    `json:"queued"`
	Encoder string `json:"encoder"`
}

// transcodeTasksResponse 是 GET /api/transcode/tasks 的响应。
type transcodeTasksResponse struct {
	Tasks   []engine.TranscodeTask `json:"tasks"`
	Encoder string                 `json:"encoder"`
}

// transcodeResidueResponse 是 GET /api/transcode/residue 的响应。
type transcodeResidueResponse struct {
	Files       []engine.TranscodeResidue `json:"files"`
	ScannedDirs int                       `json:"scanned_dirs"`
}

// trimProbeResponse 是 POST /api/trim/probe 的响应。
type trimProbeResponse struct {
	Boundary float64 `json:"boundary"`
	Status   string  `json:"status"`
	Cached   bool    `json:"cached"`
}

// trimLogResponse 是 POST /api/trim 的响应。
type trimLogResponse struct {
	Log []store.TrimLogEntry `json:"log"`
}
