// API 响应契约 —— 改名 / 保留 / 清理计划（v4.0.0）
//
// 改名相关的响应字段多、组合多，但语义高度集中：
// 「一次改名执行的结果」几乎总是 batch_id + done + failed + items + reconcile，
// 「一次改名计划的状态」总是 total + done + failed + pending + status。
// 这里按这两个语义抽出公共 struct，其余接口按需组合。
package web

// ── 改名：执行结果 ──────────────────────────────────────────────────────

// renameExecResult 是「一次改名执行」的结果。
//
// 覆盖：
//
//	POST /api/rename/execute      rename_batch.go  handleRenameExecute
//	POST /api/rename/chunk        rename_plan.go   handleRenameChunk
//
// 注意：不同接口带的附加字段略有差别，这里用可选字段覆盖，
// omitempty 保证未设置的字段不出现在 JSON 里（与原 map 行为一致）。
// renameExecResult 是「一次改名执行」的结果。
//
// 覆盖：POST /api/rename/execute、POST /api/rename/chunk
//
// v4.0.0 重要约束：这个结构原先被 6 个分支共用，但各分支的键集合不同
// （有的带 cancelled、有的带 warning、有的带 total）。
// 用一个 struct + omitempty 无法精确还原——
//   - 带 omitempty：值为 0 的键会消失（原 map 里显式写出的键永远在）；
//   - 不带 omitempty：会多出原响应没有的键。
//
// 因此按「键集合」拆分：本 struct 只服务键集合固定的那两个分支，
// 其余分支各自有专用 struct（见下方）。
//
// 本 struct 服务的分支（键集合完全一致）：
//
//	{batch_id, done, failed, items, reconcile}
type renameExecResult struct {
	// BatchID 是本批次号。
	BatchID int64 `json:"batch_id"`
	// Done / Failed 是本批次的成功与失败数。
	Done   int `json:"done"`
	Failed int `json:"failed"`
	// Items 是逐项结果。
	Items any `json:"items"`
	// Reconcile 是执行后对账结果。
	Reconcile any `json:"reconcile"`
}

// renameExecStatusResult 是「改名执行结果 + 状态」的响应。
//
// 键集合：{ok, done, failed, status, items}
type renameExecStatusResult struct {
	OK     bool   `json:"ok"`
	Done   int    `json:"done"`
	Failed int    `json:"failed"`
	Status string `json:"status"`
	Items  any    `json:"items"`
}

// renameChunkCancelledResult 是「分片被取消」的响应。
//
// 键集合：{cancelled, done, failed, remaining, total, total_done, total_failed}
type renameChunkCancelledResult struct {
	Cancelled   bool `json:"cancelled"`
	Done        int  `json:"done"`
	Failed      int  `json:"failed"`
	Remaining   int  `json:"remaining"`
	Total       int  `json:"total"`
	TotalDone   int  `json:"total_done"`
	TotalFailed int  `json:"total_failed"`
}

// renameChunkDoneResult 是「分片已无剩余条目」的响应。
//
// 键集合：{done, failed, remaining, total, total_done, total_failed}
type renameChunkDoneResult struct {
	Done        int `json:"done"`
	Failed      int `json:"failed"`
	Remaining   int `json:"remaining"`
	Total       int `json:"total"`
	TotalDone   int `json:"total_done"`
	TotalFailed int `json:"total_failed"`
}

// renameChunkWarningResult 是「改名成功但结果回填失败」的响应。
//
// 键集合：{batch_id, done, failed, remaining, warning, items, reconcile}
type renameChunkWarningResult struct {
	BatchID   int64  `json:"batch_id"`
	Done      int    `json:"done"`
	Failed    int    `json:"failed"`
	Remaining int    `json:"remaining"`
	Warning   string `json:"warning"`
	Items     any    `json:"items"`
	Reconcile any    `json:"reconcile"`
}

// renameChunkResult 是「分片执行完成」的响应。
//
// 键集合：{batch_id, done, failed, remaining, total, total_done, total_failed, items, reconcile}
type renameChunkResult struct {
	BatchID     int64 `json:"batch_id"`
	Done        int   `json:"done"`
	Failed      int   `json:"failed"`
	Remaining   int   `json:"remaining"`
	Total       int   `json:"total"`
	TotalDone   int   `json:"total_done"`
	TotalFailed int   `json:"total_failed"`
	Items       any   `json:"items"`
	Reconcile   any   `json:"reconcile"`
}

// ── 改名：计划状态 ──────────────────────────────────────────────────────

// renamePlanStatusResponse 是改名计划的状态响应。
//
// 覆盖：GET /api/rename/plan/status
type renamePlanStatusResponse struct {
	PlanID  int64  `json:"plan_id"`
	Total   int    `json:"total"`
	Done    int    `json:"done"`
	Failed  int    `json:"failed"`
	Pending int    `json:"pending"`
	Status  string `json:"status"`
}

// renamePlanCreateResponse 是「创建改名计划」的响应。
//
// 覆盖：POST /api/rename/plan
type renamePlanCreateResponse struct {
	PlanID    int64  `json:"plan_id"`
	Total     int    `json:"total"`
	Conflicts any    `json:"conflicts"`
	Errors    int    `json:"errors"`
	Source    string `json:"source"`
	Samples   any    `json:"samples"`
}

// renameCancelResponse 是「取消 / 清除计划」的响应。
//
// 覆盖：POST /api/rename/cancel
type renameCancelResponse struct {
	OK     bool  `json:"ok"`
	PlanID int64 `json:"plan_id"`
	// Cancel 标记是否真的取消（Clear 模式只清标记不取消）。
	Cancel bool `json:"cancel"`
}

// renameBatchesResponse 是「最近批次」列表的响应。
type renameBatchesResponse struct {
	Batches any `json:"batches"`
}

// renameBatchesResult 是「按计划汇总各批次执行结果」的响应。
//
// 覆盖：GET /api/rename/plan/batches
type renameBatchesResult struct {
	Done    int `json:"done"`
	Failed  int `json:"failed"`
	Items   any `json:"items"`
	Batches any `json:"batches"`
}

// renameBatchesResidualResponse 是「批次 + 未完成残留」的响应。
type renameBatchesResidualResponse struct {
	Batches  any `json:"batches"`
	Residual any `json:"residual"`
}

// renamePlanItemsResponse 是「计划项预览」的响应。
//
// v4.0.0：两处调用的键集合不同 —— 一处带 plan、一处不带，
// 故拆成两个 struct（共用一个 + omitempty 无法精确还原）。
//
// 带 plan 的那处（键集合 {items, count, plan}）：
// renamePlanItemsWithPlanResponse
// 不带 plan 的那处（键集合 {items, count}）：
// renamePlanItemsResponse
type renamePlanItemsResponse struct {
	Items any `json:"items"`
	Count int `json:"count"`
}

// renamePlanItemsWithPlanResponse 是「计划项预览 + 计划内容」的响应。
type renamePlanItemsWithPlanResponse struct {
	Items any `json:"items"`
	Count int `json:"count"`
	Plan  any `json:"plan"`
}

// ── 改名预设 ────────────────────────────────────────────────────────────

// renamePresetsResponse 是改名预设列表。
type renamePresetsResponse struct {
	Presets any `json:"presets"`
}

// renamePresetResponse 是单个改名预设。
type renamePresetResponse struct {
	Preset any `json:"preset"`
}

// ── 保留策略 ────────────────────────────────────────────────────────────

// retainSuggestionsResponse 是「保留策略候选」的响应。
type retainSuggestionsResponse struct {
	Suggestions any `json:"suggestions"`
}

// retainClearedResponse 是「清除保留策略」的响应。
type retainClearedResponse struct {
	Cleared int64 `json:"cleared"`
}

// ── 清理计划 ────────────────────────────────────────────────────────────

// cleanupPlansResponse 是清理计划列表。
type cleanupPlansResponse struct {
	Plans any `json:"plans"`
}

// cleanupPlanResponse 是单个清理计划。
type cleanupPlanResponse struct {
	Plan any `json:"plan"`
}

// ── 通用「只回 ok」 ─────────────────────────────────────────────────────

// okResponse 已定义在 contracts.go；这里补一个带附加布尔位的变体。
//
// 覆盖：POST /api/tasks/pause、POST /api/tasks/resume
type pausedResponse struct {
	OK     bool `json:"ok"`
	Paused bool `json:"paused"`
}

// modeResponse 是「切换模式」的响应。
type modeResponse struct {
	OK   bool   `json:"ok"`
	Mode string `json:"mode"`
}

// nResponse 是「只回一个数字」的响应。
type nResponse struct {
	OK bool `json:"ok"`
	N  int  `json:"n"`
}

// itemsTotalResponse 是「列表 + 总数」的响应。
type itemsTotalResponse struct {
	Items any `json:"items"`
	Total int `json:"total"`
}
