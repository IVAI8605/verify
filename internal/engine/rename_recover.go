// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 本文件实现改名批次的中断恢复（v3.10.12 P1-03）。
//
// 规则引擎在改名前后要写三层记录：意图行（执行前）、审计行与索引路径（每 100 条）。
// 进程被强杀时，最后一段「磁盘已改名、审计/索引没写」的文件没有任何撤销入口。
// 恢复流程不重做改名，只拿 pending 意图行去对磁盘：
//   - 新路径在、旧路径不在 → 改名确实成功了，补写审计与索引后标 done；
//   - 旧路径在、新路径不在 → 改名没发生，标 failed，不动文件；
//   - 新旧都在或都不在   → 状态不明，标 ambiguous，留给用户判断。

import (
	"context"
	"fmt"
	"time"

	"verify/internal/applog"
	"verify/internal/oplog"
	"verify/internal/store"
)

// RenameRecoveryResult 是一次启动恢复的汇总，只进日志与操作记录。
type RenameRecoveryResult struct {
	Batches   int `json:"batches"`
	Repaired  int `json:"repaired"`
	Failed    int `json:"failed"`
	Ambiguous int `json:"ambiguous"`
}

// RecoverRenameIntents 在启动时校正崩溃残留的改名意图（v3.10.12 P1-03）。
// 只遍查 pending 行，不动任何磁盘文件；已正常收尾的批次没有 pending 行，代价接近零。
func (e *Engine) RecoverRenameIntents(ctx context.Context) (RenameRecoveryResult, error) {
	var res RenameRecoveryResult
	unlock := e.lockWrites()
	defer unlock()
	batches, err := e.st.ListRunningRenameBatches(ctx, 200)
	if err != nil {
		return res, fmt.Errorf("读取未完成改名批次失败（%v）", err)
	}
	for _, b := range batches {
		res.Batches++
		repaired, failed, ambiguous, rerr := e.reconcileRenameBatchIntents(ctx, b.ID)
		if rerr != nil {
			return res, rerr
		}
		res.Repaired += repaired
		res.Failed += failed
		res.Ambiguous += ambiguous
		// v3.10.13（P1）：pending 行到这里已全部处理完（done / failed / ambiguous），
		// 批次必须当场落终态。旧实现只在「一条没改成功」时收尾，修复过的批次会永远停在
		// running，界面提示条和启动告警常驻，而唯一出口「撤销」会把刚补好的改名再改回去。
		// 还有 ambiguous 的行说明需要人工看一眼，落 repaired_partial 继续提醒；
		// 其余情况修复完成即 repaired / failed，不再看起来像还需要撤销。
		var finishStatus string
		var finishFailed int
		switch {
		case ambiguous > 0:
			finishStatus, finishFailed = "repaired_partial", ambiguous
		case repaired > 0:
			finishStatus = "repaired"
		default:
			finishStatus = "failed"
		}
		if merr := e.st.FinishRenameBatchState(ctx, b.ID, finishStatus, finishFailed); merr != nil {
			applog.Warn("收尾改名批次失败（#%d，下次启动会重试）：%v", b.ID, merr)
		}
	}
	if res.Repaired > 0 {
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
		entry := oplog.New("rename_recover")
		entry.Source = oplog.SourceSystem
		entry.Summary = fmt.Sprintf("崩溃恢复：校正 %d 条已改名未记账的文件（%d 条未改动，%d 条需人工确认）",
			res.Repaired, res.Failed, res.Ambiguous)
		entry.TargetCount = res.Repaired
		if err := e.recordOp(ctx, entry); err != nil {
			applog.Warn("崩溃恢复结果写入操作记录失败：%v", err)
		}
		applog.Info("改名中断恢复：校正 %d 条，未改动 %d 条，待确认 %d 条（共 %d 个批次）",
			res.Repaired, res.Failed, res.Ambiguous, res.Batches)
	}
	return res, nil
}

// reconcileRenameBatchIntents 处理单批 pending 意图，按磁盘现状补记录。
func (e *Engine) reconcileRenameBatchIntents(ctx context.Context, batchID int64) (repaired, failed, ambiguous int, err error) {
	var after int64
	for {
		if cerr := ctx.Err(); cerr != nil {
			return repaired, failed, ambiguous, cerr
		}
		batch, lerr := e.st.ListPendingRenameIntentsAfter(ctx, batchID, after, 2000)
		if lerr != nil {
			return repaired, failed, ambiguous, fmt.Errorf("读取改名意图失败（%v）", lerr)
		}
		if len(batch) == 0 {
			return repaired, failed, ambiguous, nil
		}
		after = batch[len(batch)-1].ID
		var doneIDs, failedIDs, ambiguousIDs []int64
		for _, it := range batch {
			oldExists := fileExists(it.OriginalPath)
			newExists := it.NewPath != "" && fileExists(it.NewPath)
			switch {
			case newExists && !oldExists:
				if rerr := e.repairRenamedIntent(ctx, it); rerr != nil {
					// 落库失败不猜结论：保持 pending，下次启动再试。
					return repaired, failed, ambiguous, rerr
				}
				doneIDs = append(doneIDs, it.ID)
				repaired++
			case oldExists && !newExists:
				failedIDs = append(failedIDs, it.ID)
				failed++
			default:
				ambiguousIDs = append(ambiguousIDs, it.ID)
				ambiguous++
			}
		}
		if err := e.st.MarkRenameIntentStatus(ctx, doneIDs, store.RenameIntentDone); err != nil {
			return repaired, failed, ambiguous, fmt.Errorf("标记改名意图完成失败（%v）", err)
		}
		if err := e.st.MarkRenameIntentStatus(ctx, failedIDs, store.RenameIntentFailed); err != nil {
			return repaired, failed, ambiguous, fmt.Errorf("标记改名意图失败失败（%v）", err)
		}
		if err := e.st.MarkRenameIntentStatus(ctx, ambiguousIDs, store.RenameIntentAmbiguous); err != nil {
			return repaired, failed, ambiguous, fmt.Errorf("标记改名意图待确认失败（%v）", err)
		}
		if len(batch) < 2000 {
			return repaired, failed, ambiguous, nil
		}
	}
}

// repairRenamedIntent 给「磁盘已改名、库里没记录」的意图补写索引与审计。
// 两步都必须幂等：flush 可能已经写过其中一项，只是还没把意图标 done。
func (e *Engine) repairRenamedIntent(ctx context.Context, it store.RenameIntent) error {
	ectx, cancel := detachCtx(ctx)
	defer cancel()
	// 索引：新路径已有行说明 flush 已刷新过，不能再调 RenameFilePathsBatch——
	// 它会先 DELETE 新路径再 UPDATE 旧路径，旧行不在时会把新行一起删掉。
	// 旧路径没有行则说明是零字节/未扫描文件，按正常改名的「不在索引中」处理。
	if row, err := e.st.GetFileByPath(ectx, it.NewPath); err != nil {
		return fmt.Errorf("查询新路径索引失败（%v）", err)
	} else if row == nil {
		if oldRow, oerr := e.st.GetFileByPath(ectx, it.OriginalPath); oerr != nil {
			return fmt.Errorf("查询旧路径索引失败（%v）", oerr)
		} else if oldRow != nil {
			if _, rerr := e.st.RenameFilePathsBatch(ectx, [][2]string{{it.OriginalPath, it.NewPath}}); rerr != nil {
				return fmt.Errorf("恢复改名索引失败（%v）", rerr)
			}
		}
	}
	if aerr := e.st.AddAuditIfMissing(ectx, store.AuditEntry{
		TaskID:       it.TaskID,
		Operation:    "rename",
		OriginalPath: it.OriginalPath,
		NewPath:      it.NewPath,
		Timestamp:    time.Now().Unix(),
	}); aerr != nil {
		return fmt.Errorf("恢复改名审计失败（%v）", aerr)
	}
	// v3.17.3（F-01）：恢复路径与正常 flush 一样要清旧路径的健康结论，
	// 否则崩溃恢复后健康页仍在列出已经不存在的旧路径。幂等，可随重试重复执行。
	if herr := e.st.DropHealthByPaths(ectx, []string{it.OriginalPath}); herr != nil {
		return fmt.Errorf("恢复后清理旧路径健康结论失败（%v）", herr)
	}
	return nil
}

// startupRecover 启动时先做改名恢复，再报告其他残留（v3.10.12 P1-03）。
// 恢复可能遍历较慢的机械盘，给 5 分钟兜底；失败只告警，不阻断启动。
func (e *Engine) startupRecover() {
	ctx, cancel := context.WithTimeout(e.baseCtx, 5*time.Minute)
	res, err := e.RecoverRenameIntents(ctx)
	cancel()
	if err != nil {
		applog.Warn("改名中断恢复未完成（下次启动会继续）：%v", err)
	} else if res.Batches > 0 {
		applog.Info("改名中断恢复检查了 %d 个批次：校正 %d 条，未改动 %d 条，待确认 %d 条",
			res.Batches, res.Repaired, res.Failed, res.Ambiguous)
	}
	e.logUnfinishedSnapshots()
}
