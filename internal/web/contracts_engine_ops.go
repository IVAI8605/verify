// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import "verify/internal/engine"

// 本文件收口「操作类」engine 结果的 HTTP 响应契约：繁简转换、组内改名、移动、剪除。
//
// 背景（v4.0.0 契约规则，与 contracts_engine.go 同源）：handler 直接把 engine 的
// *FanjianResult 等结构体回给前端时，字段名真相源在 engine 包，而 tools/gen_types.py
// 只扫 contracts*.go。engine 改一个 json tag：Go 编译通过、门禁全绿、前端静默拿到 undefined。
// 这里为每个 engine 结果定义具名契约 struct + 显式转换函数：
// 前者是前端字段名的唯一真相源，后者保证新增字段不会无意泄露到接口上。
//
// 两处生成器限制（见 tools/gen_types.py 的 ts_type 映射表），在此说明而非偷偷退化：
//   - errors 的 Go 类型是 map[string]string，JSON 为「路径 → 原因」动态键对象，
//     无法用固定 struct 表达，生成器输出 TS any；engine 侧原类型也相同，形状未变。
//   - reconcile 必须保留 *T + omitempty：改成值类型会让 encoding/json 不再省略
//     nil 分支，凭空多出一个键、改变 JSON 键集合；生成器对指针输出 TS any。
// 除此之外的嵌套 struct（renameDone / reconcileItem / moveDone / trimDone）
// 都定义了契约类型，生成器会生成具体 interface，不会退化成 any。

// renameDoneResp 是一个成功改名条目（对应 engine.RenameDone）。
type renameDoneResp struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// reconcileItemResp 是对账单条差异（对应 engine.ReconcileItem）。
type reconcileItemResp struct {
	Path     string `json:"path"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Problem  string `json:"problem"`
	State    string `json:"state"`
}

// reconcileReportResp 是一次批量写操作的后对账结果（对应 engine.ReconcileReport）。
// Items 保留 engine 的 omitempty；转换时仍给非 nil 空切片，避免 null——
// 键会被 omitempty 省掉，一旦出现就一定是数组，前端可安全读 .length。
type reconcileReportResp struct {
	SnapshotID int64               `json:"snapshot_id"`
	Checked    int                 `json:"checked"`
	Mismatched int                 `json:"mismatched"`
	Truncated  bool                `json:"truncated,omitempty"`
	Items      []reconcileItemResp `json:"items,omitempty"`
	Advice     string              `json:"advice,omitempty"`
}

// toReconcileReportResp 把对账结果转成契约 struct，nil 时返回带空切片的零值。
func toReconcileReportResp(src *engine.ReconcileReport) reconcileReportResp {
	out := reconcileReportResp{Items: []reconcileItemResp{}}
	if src == nil {
		return out
	}
	out.SnapshotID, out.Checked, out.Mismatched = src.SnapshotID, src.Checked, src.Mismatched
	out.Truncated, out.Advice = src.Truncated, src.Advice
	out.Items = make([]reconcileItemResp, 0, len(src.Items))
	for _, it := range src.Items {
		out.Items = append(out.Items, reconcileItemResp{
			Path: it.Path, Expected: it.Expected, Actual: it.Actual,
			Problem: it.Problem, State: it.State,
		})
	}
	return out
}

// fanjianResultResp 是 /api/fanjian 的响应（对应 engine.FanjianResult）。
type fanjianResultResp struct {
	TaskID    int64                `json:"task_id,omitempty"`
	Done      []renameDoneResp     `json:"done"`
	Skipped   []string             `json:"skipped"`
	Failed    []string             `json:"failed"`
	Errors    map[string]string    `json:"errors"`
	Reconcile *reconcileReportResp `json:"reconcile,omitempty"`
}

// toFanjianResultResp 把 engine 结果转成契约 struct。
// Done / Skipped / Failed 一律给非 nil 空切片：前端直接读 .length，null 会炸。
func toFanjianResultResp(src *engine.FanjianResult) fanjianResultResp {
	out := fanjianResultResp{
		Done: []renameDoneResp{}, Skipped: []string{}, Failed: []string{},
		Errors: map[string]string{},
	}
	if src == nil {
		return out
	}
	out.TaskID = src.TaskID
	out.Done = make([]renameDoneResp, 0, len(src.Done))
	for _, it := range src.Done {
		out.Done = append(out.Done, renameDoneResp{From: it.From, To: it.To})
	}
	out.Skipped = append(out.Skipped, src.Skipped...)
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

// renameResultResp 是 /api/groups/rename 的响应（对应 engine.RenameResult）。
type renameResultResp struct {
	TaskID    int64                `json:"task_id,omitempty"`
	Done      []renameDoneResp     `json:"done"`
	Failed    []string             `json:"failed"`
	Errors    map[string]string    `json:"errors"`
	Reconcile *reconcileReportResp `json:"reconcile,omitempty"`
}

// toRenameResultResp 把 engine 结果转成契约 struct，nil 时返回带空切片的零值。
func toRenameResultResp(src *engine.RenameResult) renameResultResp {
	out := renameResultResp{
		Done: []renameDoneResp{}, Failed: []string{}, Errors: map[string]string{},
	}
	if src == nil {
		return out
	}
	out.TaskID = src.TaskID
	out.Done = make([]renameDoneResp, 0, len(src.Done))
	for _, it := range src.Done {
		out.Done = append(out.Done, renameDoneResp{From: it.From, To: it.To})
	}
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

// moveDoneResp 是一个成功移动条目（对应 engine.MoveDone）。
type moveDoneResp struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// moveFilesResultResp 是 /api/move/preview 与 /api/move 的响应（对应 engine.MoveResult）。
// 名字故意避开 contracts_domain.go 的 moveResultResponse：后者经 ts_name_of 生成的
// TS 名正是 MoveResultResp，若本 struct 也叫 moveResultResp，types.d.ts 会出现
// 两个同名 interface、前端类型门禁报重复声明。
type moveFilesResultResp struct {
	TaskID int64             `json:"task_id,omitempty"`
	Done   []moveDoneResp    `json:"done"`
	Failed []string          `json:"failed"`
	Errors map[string]string `json:"errors"`
	// CrossVol 是走跨卷复制+校验的条数（v4.2.0，omitempty 与 engine 侧一致）。
	CrossVol int `json:"cross_vol,omitempty"`
}

// toMoveFilesResultResp 把 engine 结果转成契约 struct，nil 时返回带空切片的零值。
func toMoveFilesResultResp(src *engine.MoveResult) moveFilesResultResp {
	out := moveFilesResultResp{
		Done: []moveDoneResp{}, Failed: []string{}, Errors: map[string]string{},
	}
	if src == nil {
		return out
	}
	out.TaskID = src.TaskID
	out.CrossVol = src.CrossVol
	out.Done = make([]moveDoneResp, 0, len(src.Done))
	for _, it := range src.Done {
		out.Done = append(out.Done, moveDoneResp{From: it.From, To: it.To})
	}
	out.Failed = append(out.Failed, src.Failed...)
	for k, v := range src.Errors {
		out.Errors[k] = v
	}
	return out
}

// moveDirResultResp 是 /api/move/dir/preview 与 /api/move/dir 的响应
// （对应 engine.MoveDirResult）。无切片字段，nil 时返回零值即可。
type moveDirResultResp struct {
	TaskID  int64  `json:"task_id,omitempty"`
	From    string `json:"from"`
	To      string `json:"to"`
	Files   int    `json:"files"`
	Health  int64  `json:"health"`
	Missing int    `json:"missing"`
	Error   string `json:"error,omitempty"`
}

// toMoveDirResultResp 把 engine 结果转成契约 struct。
func toMoveDirResultResp(src *engine.MoveDirResult) moveDirResultResp {
	out := moveDirResultResp{}
	if src == nil {
		return out
	}
	out.TaskID, out.From, out.To = src.TaskID, src.From, src.To
	out.Files, out.Health, out.Missing, out.Error = src.Files, src.Health, src.Missing, src.Error
	return out
}

// trimDoneResp 是一个完成的剪除条目（对应 engine.TrimDone）。
type trimDoneResp struct {
	Path     string  `json:"path"`
	Output   string  `json:"output"`
	Replaced bool    `json:"replaced"`
	Duration float64 `json:"output_duration"`
}

// trimResultResp 是 /api/trim 的响应（对应 engine.TrimResult）。
type trimResultResp struct {
	Done   []trimDoneResp    `json:"done"`
	Failed []string          `json:"failed"`
	Errors map[string]string `json:"errors"`
}

// toTrimResultResp 把 engine 结果转成契约 struct，nil 时返回带空切片的零值。
func toTrimResultResp(src *engine.TrimResult) trimResultResp {
	out := trimResultResp{
		Done: []trimDoneResp{}, Failed: []string{}, Errors: map[string]string{},
	}
	if src == nil {
		return out
	}
	out.Done = make([]trimDoneResp, 0, len(src.Done))
	for _, it := range src.Done {
		out.Done = append(out.Done, trimDoneResp{
			Path: it.Path, Output: it.Output, Replaced: it.Replaced, Duration: it.Duration,
		})
	}
	out.Failed = append(out.Failed, src.Failed...)
	for k, v := range src.Errors {
		out.Errors[k] = v
	}
	return out
}
