// API 响应契约 —— 其余业务接口（v4.0.0)
//
// 补齐 v4.0.0 改造中尚未类型化的接口响应。
// 命名沿用「语义 + Response」，字段名与后端 json tag 一一对应。
package web

import (
	"verify/internal/store"
)

// ── 分组（带 threshold / disabled 的变体已在 contracts.go）───────────

// matchModeDisabledResponse 是「该类分组被配置禁用」的响应。
//
// 覆盖：GET /api/groups（命中 matchMode 白名单失败时）
type matchModeDisabledResponse struct {
	Groups   []store.Group `json:"groups"`
	Total    int64         `json:"total"`
	Disabled bool          `json:"disabled"`
	// Reason 说明为什么被禁用（前端提示用）。
	Reason string `json:"reason,omitempty"`
}

// ── 批量操作结果 ────────────────────────────────────────────────────────

// doneFailedResponse 是「成功数 + 失败数」的响应。
//
// 覆盖：POST /api/move、POST /api/folder_rename
type doneFailedResponse struct {
	Done   int `json:"done"`
	Failed int `json:"failed"`
}

// doneFailedItemsResponse 是「成功数 + 失败数 + 明细」的响应。
//
// 覆盖：POST /api/group_rename、POST /api/fanjian
type doneFailedItemsResponse struct {
	Done   int `json:"done"`
	Failed int `json:"failed"`
	Items  any `json:"items"`
}

// pathTaskIDResponse 是「改名后的路径 + 任务号」的响应。
//
// 覆盖：POST /api/group_rename（异步任务模式）
type pathTaskIDResponse struct {
	Path   string `json:"path"`
	TaskID string `json:"task_id"`
}

// ── 大文件 / 目录 ───────────────────────────────────────────────────────

// filesLimitOrderResponse 是「文件列表 + 分页参数 + 排序」的响应。
//
// 覆盖：GET /api/big
type filesLimitOrderResponse struct {
	Files []store.FileRow `json:"files"`
	Limit int             `json:"limit"`
	Order string          `json:"order"`
}

// labeledRemainingResponse 是「已标注数 + 剩余数」的响应。
type labeledRemainingResponse struct {
	Labeled   int `json:"labeled"`
	Remaining int `json:"remaining"`
}

// folderStatsResponse 是「目录统计」的响应。
//
// 覆盖：GET /api/folder/stats
type folderStatsResponse struct {
	Path  string `json:"path"`
	Files any    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// ── 健康检测 ────────────────────────────────────────────────────────────

// kindOKResponse 是「按类型执行成功」的响应。
type kindOKResponse struct {
	Kind string `json:"kind"`
	OK   bool   `json:"ok"`
}

// healthItemsResponse 是「健康检测条目列表」的响应。
type healthItemsResponse struct {
	Kind  string `json:"kind"`
	Total int64  `json:"total"`
	Items any    `json:"items"`
}

// ── AI ──────────────────────────────────────────────────────────────────

// failedReasonsResponse 是「AI 批量操作失败明细」的响应。
type failedReasonsResponse struct {
	Failed  int `json:"failed"`
	Reasons any `json:"reasons"`
}

// ── 标签 ────────────────────────────────────────────────────────────────

// tagsResponse 是标签列表。
type tagsResponse struct {
	Tags []store.VideoTag `json:"tags"`
}

// tagSavedResponse 是「保存标签并重算完成」的响应。
//
// v4.0.0：保存标签有三处调用，键集合各不相同，故拆成三个 struct——
// 共用一个 + omitempty 无法精确还原（原 map 里这些键都是无条件写出的）。
//
// 带 id + count 的那处（键集合 {id, ok, count}）：
//
//	tagSavedResponse
//
// 带 id + warning 的那处（键集合 {id, ok, warning}）：
//
//	tagSavedWarningResponse
//
// 只有 ok + count 的那处（键集合 {ok, count}）：
//
//	tagSavedCountResponse
type tagSavedResponse struct {
	ID    int64 `json:"id"`
	OK    bool  `json:"ok"`
	Count int   `json:"count"`
}

// tagSavedWarningResponse 是「标签已保存但重算失败」的响应。
type tagSavedWarningResponse struct {
	ID      int64  `json:"id"`
	OK      bool   `json:"ok"`
	Warning string `json:"warning"`
}

// tagSavedCountResponse 是「重算完成、只回计数」的响应。
type tagSavedCountResponse struct {
	OK    bool `json:"ok"`
	Count int  `json:"count"`
}

// ── 操作记录 ────────────────────────────────────────────────────────────

// okRemovedResponse 是「清理操作记录」的响应。
type okRemovedResponse struct {
	OK      bool  `json:"ok"`
	Removed int64 `json:"removed"`
}

// runsResponse 是「写操作快照的运行记录」列表。
type runsResponse struct {
	Runs any `json:"runs"`
}

// opSnapshotsResponse 是「写操作快照」的分页列表。
type opSnapshotsResponse struct {
	Total     int `json:"total"`
	Page      int `json:"page"`
	PageSize  int `json:"page_size"`
	Snapshots any `json:"snapshots"`
}

// ── 转码 ────────────────────────────────────────────────────────────────

// jobIDResponse 是「提交转码任务」的响应。
type jobIDResponse struct {
	JobID int64 `json:"job_id"`
	OK    bool  `json:"ok"`
}

// ── 导入 ────────────────────────────────────────────────────────────────

// ── 目录浏览 ────────────────────────────────────────────────────────────

// fsListEntryResponse 是 /api/fs/list 里的一个目录条目（对应 fsbrowse.Entry）。
type fsListEntryResponse struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Locked 表示该目录不可读（trimacl 未授权）——照样列出并显示锁标，
	// 让用户看得到结构并可直接手输已授权路径。
	Locked bool   `json:"locked,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// fsListResponse 是 GET /api/fs/list 的响应（对应 fsbrowse.Listing）。
//
// 字段与 fsbrowse.Listing 一一对应，包括 omitempty 的位置：
// 目录选择器依赖 entries / roots / parent，锁标依赖 locked / reason，
// 截断与「另有 N 个未授权卷根」依赖 truncated / hidden_locked。
type fsListResponse struct {
	Path    string                `json:"path"`
	Parent  string                `json:"parent"`
	Roots   []string              `json:"roots"`
	Entries []fsListEntryResponse `json:"entries"`
	// Locked 表示当前目录本身不可读，返回体仍然完整（含 roots）。
	Locked bool   `json:"locked,omitempty"`
	Reason string `json:"reason,omitempty"`
	// HiddenLocked 是入口视图里被收起的未授权卷根数量。
	HiddenLocked int  `json:"hidden_locked,omitempty"`
	Truncated    bool `json:"truncated,omitempty"`
}

// addBrowseRootReq 是 POST /api/fs/roots 的请求体。
// 路径由服务端重新做三步防线校验，不信任前端「刚才探测通过」的结论。
type addBrowseRootReq struct {
	Path string `json:"path"`
}
