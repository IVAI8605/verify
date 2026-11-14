// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"verify/internal/applog"
	"verify/internal/store"
)

// 本文件实现「写操作前快照 + 操作后对账」（v3.10.5）。
//
// 目的不是阻止失败，而是让失败可解释：这次批量操作前文件是什么样，做完后
// 磁盘上实际变了哪些、跟预期差在哪。设计取舍：
//   - 快照只覆盖本次操作触及的路径，用 Lstat 采集大小/时间（不做全库遍历）；
//   - 哈希只复用 files 表已有值，绝不为了快照重算；
//   - 对账同样只做 Lstat 级判断，不读文件内容。

// 对账状态。ok 表示与预期一致；skipped 表示这一条没有真正执行（预检失败等）。
const (
	opStatePending      = "pending"
	opStateOK           = "ok"
	opStateSkipped      = "skipped"
	opStateStillExists  = "still_exists"
	opStateMissing      = "missing"
	opStateChanged      = "changed"
	opStatePathMismatch = "path_mismatch"
	// opStateRecordLost：文件确实动了，但配套记录没写进去（回滚也失败），只能人工善后。
	opStateRecordLost = "record_lost"
)

// 预期状态。
const (
	opExpectAbsent    = "absent"    // 文件/目录应该不存在（隔离、删除、空目录清理）
	opExpectRelocated = "relocated" // 应该搬到 final_path（隔离区），只校验存在与大小
	opExpectRenamed   = "renamed"   // 应该改名到 final_path，校验存在、大小与 mtime
)

// ReconcileItem 是对账单条差异，直接返回给前端展示。
type ReconcileItem struct {
	Path     string `json:"path"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Problem  string `json:"problem"`
	State    string `json:"state"`
}

// ReconcileReport 是一次批量写操作的后对账结果。
type ReconcileReport struct {
	SnapshotID int64           `json:"snapshot_id"`
	Checked    int             `json:"checked"`
	Mismatched int             `json:"mismatched"`
	Truncated  bool            `json:"truncated,omitempty"`
	Items      []ReconcileItem `json:"items,omitempty"`
	Advice     string          `json:"advice,omitempty"`
}

// reconcileItemLimit 控制返回条数：差异通常很少，真出问题时也不该把几千条塞进响应。
const reconcileItemLimit = 200

type opSnapshotItem struct {
	path      string
	size      int64
	mtime     int64
	hash      string
	finalPath string
	expect    string
	state     string
	detail    string
	// ignoreMtime 用于「主动更新时间戳」的改名：这类操作本来就会改 mtime，
	// 再拿旧 mtime 对账会把正常结果误报成不一致。
	ignoreMtime bool
	// forced 表示调用方已判定这条不一致，不再用 Lstat 重新推断。
	forced bool
}

// OpSnapshot 是一次批量操作的内存快照。
type OpSnapshot struct {
	runID  int64
	kind   string
	items  []*opSnapshotItem
	byPath map[string]*opSnapshotItem
}

// beginOpSnapshot 在任何文件动作之前采集状态并落库。
//
// 采集失败（数据库写不进）不阻断业务：快照是安全带，不是刹车；
// 此时仍保留内存快照，本次响应里的对账结果照常可用。
func (e *Engine) beginOpSnapshot(ctx context.Context, kind string, paths []string) *OpSnapshot {
	snap := &OpSnapshot{
		kind:   kind,
		byPath: make(map[string]*opSnapshotItem, len(paths)),
	}
	uniq := make([]string, 0, len(paths))
	for _, p := range paths {
		p = filepath.Clean(p)
		if _, ok := snap.byPath[p]; ok {
			continue
		}
		it := &opSnapshotItem{path: p, expect: opExpectAbsent, state: opStatePending}
		// Lstat 级采集：只看元数据，不读内容、不算哈希。
		if fi, err := os.Lstat(p); err == nil {
			it.size = fi.Size()
			it.mtime = fi.ModTime().UnixNano()
		}
		snap.items = append(snap.items, it)
		snap.byPath[p] = it
		uniq = append(uniq, p)
	}
	if len(snap.items) == 0 {
		return snap
	}
	// 哈希只在库里已经有的时候顺手记下，供事后追查；取不到就留空。
	if hashes, err := e.st.FileHashesByPaths(ctx, uniq); err == nil {
		for p, h := range hashes {
			if it := snap.byPath[p]; it != nil {
				it.hash = h
			}
		}
	} else {
		applog.Warn("读取操作快照哈希失败（不影响本次操作）：%v", err)
	}
	if runID, err := e.st.CreateOpSnapshot(ctx, kind); err != nil {
		applog.Warn("创建操作快照失败（本次仍会在响应里对账，但不留档）：%v", err)
		return snap
	} else {
		snap.runID = runID
	}
	items := make([]store.OpSnapshotItem, 0, len(snap.items))
	for _, it := range snap.items {
		items = append(items, store.OpSnapshotItem{Path: it.path, Size: it.size, Mtime: it.mtime, Hash: it.hash})
	}
	if err := e.st.AddOpSnapshotItems(ctx, snap.runID, items); err != nil {
		applog.Warn("写入操作快照条目失败（不影响本次操作）：%v", err)
	}
	return snap
}

// MarkMoved 记录一条「应该搬到/改名到 to」的预期。
func (s *OpSnapshot) MarkMoved(from, to, expect string) {
	if s == nil {
		return
	}
	if it := s.byPath[filepath.Clean(from)]; it != nil {
		it.expect = expect
		it.finalPath = to
	}
}

// MarkRenamed 是 MarkMoved 的改名快捷方式，供 web 层「逐条改名」循环复用。
func (s *OpSnapshot) MarkRenamed(from, to string) {
	s.MarkMoved(from, to, opExpectRenamed)
}

// dropIndexPath 删除索引里的旧路径，失败留痕（v3.10.8，P2-2）。
// 文件已改名/移动/隔离后，若索引行删不掉会继续留在 active；对账只比磁盘，发现不了这类脏行，
// 所以失败必须 Warn；snap 非空时一并标记为对账问题，让用户在差异清单里看得到（MarkProblem 对 nil 安全）。
func (e *Engine) dropIndexPath(ctx context.Context, snap *OpSnapshot, path string) {
	if err := e.st.DeleteByPath(ctx, path); err != nil {
		applog.Warn("索引删除旧路径失败（%s）：%s", path, err.Error())
		snap.MarkProblem(path, opExpectAbsent, path, "索引删除旧路径失败，数据库可能仍认为文件在原处："+err.Error())
	}
}

// MarkProblem 记录一条调用方已经确认的不一致（典型是「记录写入失败且回滚也失败」）。
// 这种结果不能再靠 Lstat 推断——文件在磁盘上可能「看起来正常」，但没有对应的记录，
// 必须直接进差异清单，让用户知道该去哪里善后。
func (s *OpSnapshot) MarkProblem(path, expect, finalPath, detail string) {
	if s == nil {
		return
	}
	if it := s.byPath[filepath.Clean(path)]; it != nil {
		it.expect = expect
		it.finalPath = finalPath
		it.state = opStateRecordLost
		it.detail = detail
		it.forced = true
	}
}

// BeginOpSnapshot / FinishOpSnapshot 是对内的快照入口的导出包装。
// 仅用于 web 层「逐条改名」这类不经过 engine 批量函数的写循环，其余场景直接用内部方法。
func (e *Engine) BeginOpSnapshot(ctx context.Context, kind string, paths []string) *OpSnapshot {
	return e.beginOpSnapshot(ctx, kind, paths)
}

func (e *Engine) FinishOpSnapshot(snap *OpSnapshot) *ReconcileReport {
	return e.finishOpSnapshot(snap)
}

// Skip 标记这一条没有真正执行（预检失败、无需处理等），不计入差异。
// 失败原因已经在业务结果里返回，这里只避免对账把「没动手」误报成「不一致」。
func (s *OpSnapshot) Skip(path, detail string) {
	if s == nil {
		return
	}
	if it := s.byPath[filepath.Clean(path)]; it != nil {
		it.state = opStateSkipped
		it.detail = detail
	}
}

// SkipPending 把「还没动手」的条目标记为跳过。
// 任务中途取消时，未处理到的路径仍是 pending + absent，若不标跳过，
// 对账会把「根本没执行」误报成「文件仍在原路径/不一致」（P1）。
func (s *OpSnapshot) SkipPending(detail string) {
	if s == nil {
		return
	}
	for _, it := range s.items {
		if it.state == opStatePending && it.expect == opExpectAbsent {
			it.state = opStateSkipped
			it.detail = detail
		}
	}
}

// IgnoreMtime 让对账不校验修改时间，用于「更新时间戳」这类本就会改 mtime 的方法。
func (s *OpSnapshot) IgnoreMtime(path string) {
	if s == nil {
		return
	}
	if it := s.byPath[filepath.Clean(path)]; it != nil {
		it.ignoreMtime = true
	}
}

// finishOpSnapshot 做操作后对账，落库并返回可展示的报告。
// 对账使用独立 context：业务请求被取消也要把结果写下来。
func (e *Engine) finishOpSnapshot(snap *OpSnapshot) *ReconcileReport {
	if snap == nil || len(snap.items) == 0 {
		return nil
	}
	report := &ReconcileReport{SnapshotID: snap.runID}
	results := make([]store.OpSnapshotResult, 0, len(snap.items))
	adviceSet := make(map[string]bool)

	for _, it := range snap.items {
		if it.state != opStateSkipped {
			report.Checked++
			if !it.forced {
				checkSnapshotItem(it)
			}
			if it.state != opStateOK {
				report.Mismatched++
				if len(report.Items) < reconcileItemLimit {
					report.Items = append(report.Items, ReconcileItem{
						Path:     it.path,
						Expected: expectText(it),
						Actual:   actualText(it),
						Problem:  it.detail,
						State:    it.state,
					})
				} else {
					report.Truncated = true
				}
				if a := adviceFor(it.state); a != "" {
					adviceSet[a] = true
				}
			}
		}
		results = append(results, store.OpSnapshotResult{
			Path: it.path, Size: it.size, Mtime: it.mtime, Hash: it.hash,
			Expect: it.expect, FinalPath: it.finalPath, State: it.state, Detail: it.detail,
		})
	}

	report.Advice = joinAdvice(adviceSet)
	if snap.runID > 0 {
		if err := e.st.FinishOpSnapshot(context.Background(), snap.runID, results, report.Advice); err != nil {
			applog.Warn("保存操作对账结果失败（响应里的结果仍然有效）：%v", err)
		}
	}
	if report.Mismatched > 0 {
		applog.Warn("操作后对账发现 %d 项与预期不符（快照 %d，类型 %s）：%s",
			report.Mismatched, snap.runID, snap.kind, report.Advice)
	} else {
		applog.Info("操作后对账完成：%d 项一致（快照 %d，类型 %s）", report.Checked, snap.runID, snap.kind)
	}
	return report
}

// checkSnapshotItem 用 Lstat 判断实际状态，不读文件内容、不重算哈希。
func checkSnapshotItem(it *opSnapshotItem) {
	switch it.expect {
	case opExpectRelocated:
		// 跨盘移动可能是「复制 + 删除」，mtime 不一定保留，所以只校验存在与大小。
		fi, err := os.Lstat(it.finalPath)
		if err != nil {
			it.state = opStateMissing
			it.detail = "隔离区里找不到该文件"
			return
		}
		if fi.Size() != it.size {
			it.state = opStateChanged
			it.detail = fmt.Sprintf("隔离区文件大小与快照不一致（快照 %d 字节，实际 %d 字节）", it.size, fi.Size())
			return
		}
		if _, err := os.Lstat(it.path); err == nil {
			it.state = opStatePathMismatch
			it.detail = "隔离区已有副本，但原路径仍然存在"
			return
		}
		it.state = opStateOK
	case opExpectRenamed:
		fi, err := os.Lstat(it.finalPath)
		if err != nil {
			it.state = opStateMissing
			it.detail = "改名后的文件不存在"
			return
		}
		if fi.Size() != it.size || (!it.ignoreMtime && fi.ModTime().UnixNano() != it.mtime) {
			it.state = opStateChanged
			it.detail = fmt.Sprintf("改名后大小或修改时间与快照不一致（快照 %d 字节，实际 %d 字节）", it.size, fi.Size())
			return
		}
		if _, err := os.Lstat(it.path); err == nil && filepath.Clean(it.path) != filepath.Clean(it.finalPath) {
			it.state = opStatePathMismatch
			it.detail = "改名后的文件已生成，但旧路径仍然存在"
			return
		}
		it.state = opStateOK
	default: // absent
		if _, err := os.Lstat(it.path); err == nil {
			it.state = opStateStillExists
			it.detail = "预期不存在，但文件/目录仍在（可能隔离、删除或回滚失败）"
			return
		}
		it.state = opStateOK
	}
}

func expectText(it *opSnapshotItem) string {
	switch it.expect {
	case opExpectRelocated:
		return "应已移入隔离区：" + it.finalPath
	case opExpectRenamed:
		return "应已改名为：" + it.finalPath
	default:
		return "应已不存在"
	}
}

func actualText(it *opSnapshotItem) string {
	switch it.state {
	case opStateStillExists:
		return "仍然存在"
	case opStateMissing:
		return "已不存在"
	case opStateChanged:
		return "存在但大小/时间变了"
	case opStatePathMismatch:
		return "新旧路径同时存在"
	case opStateRecordLost:
		return "文件已移动，但没有配套记录"
	default:
		return ""
	}
}

// adviceFor 把状态翻译成用户能照做的下一步，而不是只说「有差异」。
func adviceFor(state string) string {
	switch state {
	case opStateStillExists:
		return "有文件仍在原路径：请重新扫描确认实际状态，并到「操作记录」查看对应批次；若是撤销操作，请检查原路径是否被其他文件占用"
	case opStateMissing:
		return "预期存在的文件没了：请重新扫描，并到「隔离区」确认是否可恢复"
	case opStateChanged:
		return "文件大小或修改时间与快照不一致：可能被其他程序改动，请重新扫描后再操作"
	case opStatePathMismatch:
		return "新旧路径同时存在：改名或撤销没有完全生效，请重新扫描并到「操作记录」核对"
	case opStateRecordLost:
		return "文件已移动但没有配套记录：请到「操作记录」核对，并在隔离区确认文件位置，必要时重新扫描"
	default:
		return ""
	}
}

func joinAdvice(set map[string]bool) string {
	if len(set) == 0 {
		return ""
	}
	parts := make([]string, 0, len(set))
	for _, a := range []string{
		adviceFor(opStateStillExists),
		adviceFor(opStateMissing),
		adviceFor(opStateChanged),
		adviceFor(opStatePathMismatch),
		adviceFor(opStateRecordLost),
	} {
		if a != "" && set[a] {
			parts = append(parts, a)
		}
	}
	return strings.Join(parts, "；")
}

// UnfinishedSnapshotAudit 是一次未完成快照的只读对账结果（v3.10.10，第五轮 P1-1）。
type UnfinishedSnapshotAudit struct {
	ID           int64           `json:"id"`
	Kind         string          `json:"kind"`
	StartedAt    int64           `json:"started_at"`
	Items        int             `json:"items"`
	Checked      int             `json:"checked"`
	StillExists  int             `json:"still_exists"`
	Missing      int             `json:"missing"`
	PathMismatch int             `json:"path_mismatch"`
	Truncated    bool            `json:"truncated,omitempty"`
	Problems     []ReconcileItem `json:"problems,omitempty"`
}

// ReconcileUnfinishedSnapshots 对未完成的快照做只读对账，绝不改动文件。
// 未完成 run 的 final_path 通常为空（finish 才落库），所以这里只能回答三件事：
// 原路径仍在（操作可能没生效）、原路径没了（去了哪无从得知）、新旧路径同时存在。
func (e *Engine) ReconcileUnfinishedSnapshots(ctx context.Context, maxRuns, maxItems int) ([]UnfinishedSnapshotAudit, error) {
	if maxRuns <= 0 || maxRuns > 20 {
		maxRuns = 5
	}
	if maxItems <= 0 || maxItems > 2000 {
		maxItems = 2000
	}
	runs, err := e.st.ListUnfinishedOpSnapshots(ctx, maxRuns)
	if err != nil {
		return nil, err
	}
	out := make([]UnfinishedSnapshotAudit, 0, len(runs))
	for _, r := range runs {
		items, err := e.st.ListOpSnapshotItems(ctx, r.ID, maxItems)
		if err != nil {
			return nil, err
		}
		a := UnfinishedSnapshotAudit{ID: r.ID, Kind: r.Kind, StartedAt: r.StartedAt, Items: r.Items}
		for _, it := range items {
			a.Checked++
			_, origErr := os.Lstat(it.Path)
			origExists := origErr == nil
			finalExists := false
			if it.FinalPath != "" {
				if _, ferr := os.Lstat(it.FinalPath); ferr == nil {
					finalExists = true
				}
			}
			var state, problem string
			switch {
			case origExists && finalExists:
				state, problem = opStatePathMismatch, "原路径与目标路径同时存在"
			case origExists:
				state, problem = opStateStillExists, "预期已移走或删除，但文件仍在原路径"
			case finalExists:
				state = opStateOK
			default:
				state, problem = opStateMissing, "原路径已不存在，且快照未记录目标路径，无法确认文件去向"
			}
			switch state {
			case opStateStillExists:
				a.StillExists++
			case opStateMissing:
				a.Missing++
			case opStatePathMismatch:
				a.PathMismatch++
			}
			if state != opStateOK && len(a.Problems) < reconcileItemLimit {
				a.Problems = append(a.Problems, ReconcileItem{
					Path:     it.Path,
					Expected: expectLabelText(it.Expect),
					Actual:   actualLabelText(state),
					Problem:  problem,
					State:    state,
				})
			}
		}
		a.Truncated = r.Items > len(items)
		out = append(out, a)
	}
	return out, nil
}

func expectLabelText(expect string) string {
	switch expect {
	case opExpectRelocated:
		return "应已移入隔离区"
	case opExpectRenamed:
		return "应已改名"
	default:
		return "应已不存在"
	}
}

func actualLabelText(state string) string {
	switch state {
	case opStateStillExists:
		return "仍然存在"
	case opStateMissing:
		return "已不存在"
	case opStatePathMismatch:
		return "新旧路径同时存在"
	default:
		return ""
	}
}

// logUnfinishedSnapshots 启动时识别未完成的写操作快照与改名批次并告警（v3.10.10，第五轮 P1-1/P1-2）。
// 只查库、不碰文件；对账结果由 GET /api/op-snapshots/unfinished 按需返回，撤销由用户显式发起。
func (e *Engine) logUnfinishedSnapshots() {
	ctx, cancel := context.WithTimeout(e.baseCtx, 10*time.Second)
	defer cancel()
	if runs, err := e.st.ListUnfinishedOpSnapshots(ctx, 5); err == nil && len(runs) > 0 {
		ids := make([]string, 0, len(runs))
		for _, r := range runs {
			ids = append(ids, fmt.Sprintf("#%d(%s)", r.ID, r.Kind))
		}
		applog.Warn("发现 %d 次未完成的写操作快照：%s；可调用 /api/op-snapshots/unfinished 查看只读对账，系统不会自动改动文件",
			len(runs), strings.Join(ids, "、"))
	}
	// v3.10.10（第五轮 P1-2）：批次行开局即落库，崩溃会留下 running 行；
	// 启动时把批次号报出来，否则「可撤销」但用户不知道撤哪一个。
	if batches, err := e.st.ListRunningRenameBatches(ctx, 5); err == nil && len(batches) > 0 {
		parts := make([]string, 0, len(batches))
		for _, b := range batches {
			parts = append(parts, fmt.Sprintf("#%d(已记录 %d 个)", b.ID, b.Done))
		}
		applog.Warn("发现 %d 个未完成的改名批次：%s；可在「操作记录」页顶部提示条一键撤销（POST /api/rename/undo/<批次号>），系统不会自动改动文件",
			len(batches), strings.Join(parts, "、"))
	}
}
