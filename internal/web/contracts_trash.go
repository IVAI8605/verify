// API 响应契约 —— 隔离区 / 快照 / 通用动作（v4.0.0)
//
// 覆盖三类语义：
//  1. 隔离区（trash）相关：列表带游标分页、恢复/删除预览、批量操作结果；
//  2. 快照（snapshot）相关：规则搜索的结果页、目录分面；
//  3. 通用动作响应：只回 ok、带计数、带路径等。
package web

import (
	"verify/internal/rules"
	"verify/internal/store"
)

// ── 隔离区 ──────────────────────────────────────────────────────────────

// trashListResponse 是隔离区列表的响应。
//
// 覆盖：GET /api/trash、GET /api/trash/list
//
// 语义说明（v3.26.0 起）：
//   - 两个都不传分页参数 = 一次返回全部（老行为），此时 returned == total、has_more 为 false；
//   - GET /api/trash 会顺手清理幽灵行（属写操作），
//     GET /api/trash/list 只读，不写库、不动文件，自动化与监控应优先用它。
type trashListResponse struct {
	Items      []store.TrashRow `json:"items"`
	Total      int              `json:"total"`
	Returned   int              `json:"returned"`
	HasMore    bool             `json:"has_more"`
	NextCursor string           `json:"next_cursor"`
}

// trashPreviewResponse 是隔离区恢复/删除的预览响应。
//
// 覆盖：POST /api/trash/preview
type trashPreviewResponse struct {
	Action string `json:"action"`
	Items  any    `json:"items"`
}

// trashRemovedResponse 是隔离区批量操作的响应。
type trashRemovedResponse struct {
	Removed int64 `json:"removed"`
	Errors  any   `json:"errors"`
}

// ── 快照（规则搜索结果）────────────────────────────────────────────────

// snapshotGroupsResponse 是快照分组的响应。
//
// 覆盖：GET /api/snapshot/groups
type snapshotGroupsResponse struct {
	Groups   []store.Group `json:"groups"`
	Total    int           `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	HasMore  bool          `json:"has_more"`
}

// snapshotFacetsResponse 是目录分面的响应。
//
// 覆盖：GET /api/snapshot/facets
type snapshotFacetsResponse struct {
	Total int        `json:"total"`
	Types any        `json:"types"`
	Dirs  []dirFacet `json:"dirs"`
}

// snapshotSavedResponse 是「快照已保存」的响应。
type snapshotSavedResponse struct {
	OK bool   `json:"ok"`
	ID string `json:"id"`
}

// snapshotEmptyResponse 是「还没有搜索结果」的响应。
//
// state 固定为 "none"，totals 为空，前端据此显示「请先搜索」。
type snapshotEmptyResponse struct {
	State  string       `json:"state"`
	Totals rules.Totals `json:"totals"`
}

// ── 通用动作 ────────────────────────────────────────────────────────────

// tokenResponse 是返回访问令牌的响应。
//
// 覆盖：GET /api/auth/token
type tokenResponse struct {
	Token string `json:"token"`
}

// pathResponse 是「返回处理后的路径」的响应。
type pathResponse struct {
	OK   bool   `json:"ok"`
	Path string `json:"path"`
}

// watchRestartResponse 是「设置变更后是否需重启监控」的响应。
//
// 覆盖：POST /api/settings
type watchRestartResponse struct {
	OK           bool `json:"ok"`
	RestartWatch bool `json:"restart_watch"`
	IntroReset   bool `json:"intro_reset"`
	VideoReset   bool `json:"video_reset"`
	AudioReset   bool `json:"audio_reset"`
}

// actionItemsResponse 是「动作 + 条目列表」的响应。
//
// 覆盖：POST /api/import/groups 之类的「先预览再执行」接口。
type actionItemsResponse struct {
	Action string `json:"action"`
	Items  any    `json:"items"`
}

// countOKResponse 是「成功 + 计数」的响应。
type countOKResponse struct {
	OK    bool `json:"ok"`
	Count int  `json:"count"`
}

// removedErrorsResponse 是「删除结果」的响应。
type removedErrorsResponse struct {
	Removed int64 `json:"removed"`
	Errors  any   `json:"errors"`
}

// introGroupsResponse 是片头分组接口的响应。
//
// 覆盖：GET /api/groups/intro
// 除分组字段外还带片头分析的进度计数（n / analyzed / pending）。
type introGroupsResponse struct {
	Groups   []store.Group `json:"groups"`
	Total    int           `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	// N 是本次请求的片头时长参数（秒）。
	N int `json:"n"`
	// Analyzed 是已分析的片头数。
	Analyzed int64 `json:"analyzed"`
	// Pending 是仍在队列里的片头数。
	Pending int64 `json:"pending"`
}
