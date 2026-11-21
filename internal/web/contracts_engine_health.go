// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"verify/internal/engine"
	"verify/internal/store"
)

// 本文件收口「健康 / 改名 / 偏好 / 空目录 / 清理 / 索引体检」engine 结果的 HTTP 响应契约。
//
// 背景（v4.0.0 契约规则，与 contracts_engine.go / contracts_engine_ops.go 同源）：
// handler 直接把 engine 的 *HealthOpResult 等结构体回给前端时，字段名真相源在
// engine / store 包，而 tools/gen_types.py 只扫 contracts*.go。engine 改一个 json tag：
// Go 编译通过、门禁全绿、前端静默拿到 undefined。这里为每个结果定义具名契约
// struct + 显式转换函数：前者是前端字段名的唯一真相源，后者保证新增字段不会
// 无意泄露到接口上。
//
// 复用说明：reconcileReportResp / reconcileItemResp / toReconcileReportResp 定义在
// contracts_engine_ops.go（同包可见），本文件不重复定义，只引用。
//
// 生成器限制（见 tools/gen_types.py 的 ts_type 映射表），在此说明而非偷偷退化：
//   - errors 的 Go 类型是 map[string]string，JSON 为「路径 → 原因」动态键对象，
//     无法用固定 struct 表达，生成器输出 TS any；engine 侧原类型也相同，形状未变。
//   - reconcile 必须保留 *T + omitempty：改成值类型会让 encoding/json 不再省略
//     nil 分支，凭空多出一个键、改变 JSON 键集合；生成器对指针输出 TS any。
// 除此之外的嵌套 struct（healthOpItem / folderRenameItem / renameBatchItem /
// retainWeights / retainPrefixStat / volumeSkip）都定义了契约类型，生成器会生成
// 具体 interface，不会退化成 any。

// healthOpItemResp 是单条健康文件操作的结果（对应 engine.HealthOpItem）。
type healthOpItemResp struct {
	Path    string `json:"path"`
	NewPath string `json:"new_path,omitempty"`
	Error   string `json:"error,omitempty"`
}

// healthOpResultResp 是改名 / 修编码 / 移隔离区 / 撤销的响应（对应 engine.HealthOpResult）。
type healthOpResultResp struct {
	TaskID    int64                `json:"task_id"`
	Done      int                  `json:"done"`
	Failed    int                  `json:"failed"`
	Items     []healthOpItemResp   `json:"items"`
	Reconcile *reconcileReportResp `json:"reconcile,omitempty"`
}

// toHealthOpResultResp 把 engine 结果转成契约 struct。
// Items 一律给非 nil 空切片：前端直接读 r.items.length，null 会炸。
func toHealthOpResultResp(src *engine.HealthOpResult) healthOpResultResp {
	out := healthOpResultResp{Items: []healthOpItemResp{}}
	if src == nil {
		return out
	}
	out.TaskID, out.Done, out.Failed = src.TaskID, src.Done, src.Failed
	out.Items = make([]healthOpItemResp, 0, len(src.Items))
	for _, it := range src.Items {
		out.Items = append(out.Items, healthOpItemResp{
			Path: it.Path, NewPath: it.NewPath, Error: it.Error,
		})
	}
	if src.Reconcile != nil {
		rc := toReconcileReportResp(src.Reconcile)
		out.Reconcile = &rc
	}
	return out
}

// folderRenameItemResp 是文件夹改名预览条目（对应 engine.FolderRenameItem）。
type folderRenameItemResp struct {
	Path  string `json:"path"`
	Old   string `json:"old"`
	New   string `json:"new"`
	Class string `json:"class"`
	Skip  bool   `json:"skip"`
	Err   string `json:"error,omitempty"`
}

// folderRenamePreviewResp 是 /api/folder_rename 与 /api/folder_rename/preview 的响应
// （对应 engine.FolderRenamePreview）。
type folderRenamePreviewResp struct {
	Dir         string                 `json:"dir"`
	TaskID      int64                  `json:"task_id,omitempty"`
	Items       []folderRenameItemResp `json:"items"`
	RenameCount int                    `json:"rename_count"`
}

// toFolderRenamePreviewResp 把 engine 结果转成契约 struct，nil 时返回带空切片的零值。
func toFolderRenamePreviewResp(src *engine.FolderRenamePreview) folderRenamePreviewResp {
	out := folderRenamePreviewResp{Items: []folderRenameItemResp{}}
	if src == nil {
		return out
	}
	out.Dir, out.TaskID, out.RenameCount = src.Dir, src.TaskID, src.RenameCount
	out.Items = make([]folderRenameItemResp, 0, len(src.Items))
	for _, it := range src.Items {
		out.Items = append(out.Items, folderRenameItemResp{
			Path: it.Path, Old: it.Old, New: it.New, Class: it.Class, Skip: it.Skip, Err: it.Err,
		})
	}
	return out
}

// renameBatchItemResp 是规则引擎批量改名的单条快照（对应 store.RenameBatchItem）。
type renameBatchItemResp struct {
	Path    string `json:"path"`
	Orig    string `json:"orig"`
	Target  string `json:"target"`
	NewPath string `json:"new_path,omitempty"`
	TaskID  int64  `json:"task_id,omitempty"`
	Error   string `json:"error,omitempty"`
}

// renameEngineResultResp 是 /api/rename/execute（带 methods）的响应
// （对应 engine.RenameEngineResult）。名字避开 contracts_engine_ops.go 的 renameResultResp。
type renameEngineResultResp struct {
	BatchID   int64                 `json:"batch_id"`
	TaskID    int64                 `json:"task_id"`
	Done      int                   `json:"done"`
	Failed    int                   `json:"failed"`
	Items     []renameBatchItemResp `json:"items"`
	Reconcile *reconcileReportResp  `json:"reconcile,omitempty"`
}

// toRenameEngineResultResp 把 engine 结果转成契约 struct，nil 时返回带空切片的零值。
func toRenameEngineResultResp(src *engine.RenameEngineResult) renameEngineResultResp {
	out := renameEngineResultResp{Items: []renameBatchItemResp{}}
	if src == nil {
		return out
	}
	out.BatchID, out.TaskID, out.Done, out.Failed = src.BatchID, src.TaskID, src.Done, src.Failed
	out.Items = make([]renameBatchItemResp, 0, len(src.Items))
	for _, it := range src.Items {
		out.Items = append(out.Items, renameBatchItemResp{
			Path: it.Path, Orig: it.Orig, Target: it.Target,
			NewPath: it.NewPath, TaskID: it.TaskID, Error: it.Error,
		})
	}
	if src.Reconcile != nil {
		rc := toReconcileReportResp(src.Reconcile)
		out.Reconcile = &rc
	}
	return out
}

// userPrefStatsResp 是 /api/retain/pref/stats 的响应（对应 store.UserPrefStats，值类型）。
type userPrefStatsResp struct {
	Total   int64 `json:"total"`
	Adopt   int64 `json:"adopt"`
	Changed int64 `json:"changed"`
}

// toUserPrefStatsResp 把 store 结果转成契约 struct。
func toUserPrefStatsResp(src store.UserPrefStats) userPrefStatsResp {
	return userPrefStatsResp{Total: src.Total, Adopt: src.Adopt, Changed: src.Changed}
}

// retainWeightsResp 是五项偏好信号的权重（对应 engine.RetainWeights）。
type retainWeightsResp struct {
	Quality   float64 `json:"quality"`
	Integrity float64 `json:"integrity"`
	PathTidy  float64 `json:"path_tidy"`
	NameQ     float64 `json:"name_q"`
	Fresh     float64 `json:"fresh"`
}

// retainPrefixStatResp 是按目录前缀聚合的偏好统计（对应 engine.RetainPrefixStat）。
type retainPrefixStatResp struct {
	Prefix  string `json:"prefix"`
	Adopt   int    `json:"adopt"`
	Changed int    `json:"changed"`
}

// retainProfileResp 是 /api/retain/pref/profile 与 /api/retain/pref/apply 的响应
// （对应 engine.RetainProfile，值类型）。
type retainProfileResp struct {
	Learned     int                    `json:"learned"`
	Adopt       int                    `json:"adopt"`
	Changed     int                    `json:"changed"`
	Enabled     bool                   `json:"enabled"`
	Weights     retainWeightsResp      `json:"weights"`
	TopPrefixes []retainPrefixStatResp `json:"top_prefixes"`
}

// toRetainProfileResp 把 engine 结果转成契约 struct。
// TopPrefixes 一律给非 nil 空切片：前端直接读 .length，null 会炸。
func toRetainProfileResp(src engine.RetainProfile) retainProfileResp {
	out := retainProfileResp{
		Learned: src.Learned, Adopt: src.Adopt, Changed: src.Changed, Enabled: src.Enabled,
		Weights: retainWeightsResp{
			Quality: src.Weights.Quality, Integrity: src.Weights.Integrity,
			PathTidy: src.Weights.PathTidy, NameQ: src.Weights.NameQ, Fresh: src.Weights.Fresh,
		},
		TopPrefixes: []retainPrefixStatResp{},
	}
	out.TopPrefixes = make([]retainPrefixStatResp, 0, len(src.TopPrefixes))
	for _, it := range src.TopPrefixes {
		out.TopPrefixes = append(out.TopPrefixes, retainPrefixStatResp{
			Prefix: it.Prefix, Adopt: it.Adopt, Changed: it.Changed,
		})
	}
	return out
}

// emptyDirResultResp 是 /api/emptydir/clean 的响应（对应 engine.EmptyDirResult）。
type emptyDirResultResp struct {
	Removed   []string             `json:"removed"`
	Failed    []string             `json:"failed"`
	Errors    map[string]string    `json:"errors"`
	Conflict  string               `json:"conflict,omitempty"`
	Reconcile *reconcileReportResp `json:"reconcile,omitempty"`
}

// toEmptyDirResultResp 把 engine 结果转成契约 struct，nil 时返回带空集合的零值。
func toEmptyDirResultResp(src *engine.EmptyDirResult) emptyDirResultResp {
	out := emptyDirResultResp{
		Removed: []string{}, Failed: []string{}, Errors: map[string]string{},
	}
	if src == nil {
		return out
	}
	out.Conflict = src.Conflict
	out.Removed = append(out.Removed, src.Removed...)
	out.Failed = append(out.Failed, src.Failed...)
	for k, v := range src.Errors {
		out.Errors[k] = v
	}
	if src.Reconcile != nil {
		rc := toReconcileReportResp(src.Reconcile)
		out.Reconcile = &rc
	}
	return out
}

// cleanupRunResultResp 是 /api/cleanup/run 的响应（对应 engine.CleanupRunResult）。
type cleanupRunResultResp struct {
	PlanID  int64    `json:"plan_id"`
	Groups  int      `json:"groups"`
	Trashed int      `json:"trashed"`
	Freed   int64    `json:"freed"`
	Skipped int      `json:"skipped"`
	Errors  []string `json:"errors,omitempty"`
}

// toCleanupRunResultResp 把 engine 结果转成契约 struct，nil 时返回带空切片的零值。
func toCleanupRunResultResp(src *engine.CleanupRunResult) cleanupRunResultResp {
	out := cleanupRunResultResp{Errors: []string{}}
	if src == nil {
		return out
	}
	out.PlanID, out.Groups, out.Trashed = src.PlanID, src.Groups, src.Trashed
	out.Freed, out.Skipped = src.Freed, src.Skipped
	out.Errors = append(out.Errors, src.Errors...)
	return out
}

// volumeSkipResp 是一个被整体跳过的卷（对应 engine.VolumeSkip）。
type volumeSkipResp struct {
	Volume string `json:"volume"`
	Rows   int    `json:"rows"`
	Reason string `json:"reason"`
}

// indexRepairResultResp 是 /api/index/repair 的响应（对应 engine.IndexRepairResult，值类型）。
type indexRepairResultResp struct {
	Checked        int              `json:"checked"`
	Removed        int              `json:"removed"`
	WouldRemove    int              `json:"would_remove"`
	Skipped        int              `json:"skipped"`
	SkippedVolumes []volumeSkipResp `json:"skipped_volumes,omitempty"`
	DryRun         bool             `json:"dry_run"`
}

// toIndexRepairResultResp 把 engine 结果转成契约 struct。
// SkippedVolumes 保留 omitempty，转换时给非 nil 空切片避免 null。
func toIndexRepairResultResp(src engine.IndexRepairResult) indexRepairResultResp {
	out := indexRepairResultResp{SkippedVolumes: []volumeSkipResp{}}
	out.Checked, out.Removed, out.WouldRemove = src.Checked, src.Removed, src.WouldRemove
	out.Skipped, out.DryRun = src.Skipped, src.DryRun
	out.SkippedVolumes = make([]volumeSkipResp, 0, len(src.SkippedVolumes))
	for _, it := range src.SkippedVolumes {
		out.SkippedVolumes = append(out.SkippedVolumes, volumeSkipResp{
			Volume: it.Volume, Rows: it.Rows, Reason: it.Reason,
		})
	}
	return out
}
