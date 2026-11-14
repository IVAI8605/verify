package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"verify/internal/applog"
	dictconv "verify/internal/engine/data"
	"verify/internal/store"
)

// 文件名繁简转换（v1.22 引入 §11，路线 A：纯 Go 词典 embed，无 cgo）。
// 复用 v1.15 改名基建：UniquePath 冲突追加、audit 可撤销、UpdatePath 索引同步。
// 词典实现见 internal/engine/data：改用内嵌的 OpenCC 词典数据自行匹配，
// 移除了 GPL-2.0 的 cedar-go 依赖链，输出与原实现逐条一致（见其中测试）。

// FanjianResult 是一次繁简转换的结果。
type FanjianResult struct {
	TaskID  int64             `json:"task_id,omitempty"`
	Done    []RenameDone      `json:"done"`
	Skipped []string          `json:"skipped"`
	Failed  []string          `json:"failed"`
	Errors  map[string]string `json:"errors"`
	// Reconcile 是操作后对账结果（v3.10.5）：有差异时前端列路径并给出下一步建议。
	Reconcile *ReconcileReport `json:"reconcile,omitempty"`
}

// ConvertNames 批量文件名繁简转换（设计文档 §11.3.1）。
// toTraditional=true 简→繁；false 繁→简。转换只作用于**文件主名与扩展名全名**中
// 的中文字符；转换后与原名相同（无中文字或已是目标体）→ 记 skipped。
// 前置校验同 v1.15：保护/隔离区拒绝；冲突追加 _1；audit operation='fanjian'。
func (e *Engine) ConvertNames(ctx context.Context, paths []string, toTraditional bool) *FanjianResult {
	res := &FanjianResult{Errors: map[string]string{}}
	conv := dictconv.T2S()
	if toTraditional {
		conv = dictconv.S2T()
	}
	taskID, _ := e.st.NewAuditTaskID(ctx)
	// v3.10.5：繁简改名是不可逆写操作，动手前先按本次涉及路径拍快照。
	snap := e.beginOpSnapshot(ctx, "fanjian", paths)

	// v3.10.8：一次转简体/繁体可能涉及上万个文件，逐条 AddAudit / DropHealthByPath /
	// UpdatePath 等于每个文件开三次事务并重新 stat 一遍。这里按 200 条攒批，
	// 索引只改路径字段（内容没变，不需要重算指纹），行为与逐条一致。
	const fanjianBatch = 200
	// 落库用独立上下文：客户端中途断开时，已经改完名的文件仍要写进审计与索引，
	// 否则磁盘已改名、库里还是旧路径（与批量改名的 flush 同口径）。
	var audits []store.AuditEntry
	var oldPaths []string
	var pairs [][2]string
	flush := func() {
		if len(audits) == 0 && len(oldPaths) == 0 && len(pairs) == 0 {
			return
		}
		// v3.10.9：超时上下文必须每批重建。原先在函数开头建一个 30 秒上下文全程复用，
		// 上万文件的转换一旦超过 30 秒，后续批次的审计、健康清理与索引更新会全部失败，
		// 磁盘已改名而库里仍是旧路径。
		flushCtx, flushCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer flushCancel()
		if len(audits) > 0 {
			if err := e.st.AddAuditBatch(flushCtx, audits); err != nil {
				applog.Warn("繁简转换审计批量写入失败（%d 条）：%v", len(audits), err)
			}
			audits = audits[:0]
		}
		if len(oldPaths) > 0 {
			// 旧路径的健康结论一并作废：繁体页转完还挂着「改后文件名」，点了也没用
			if err := e.st.DropHealthByPaths(flushCtx, oldPaths); err != nil {
				applog.Warn("繁简转换清理旧健康结论失败（%d 条）：%v", len(oldPaths), err)
			}
			oldPaths = oldPaths[:0]
		}
		if len(pairs) > 0 {
			missing, err := e.st.RenameFilePathsBatch(flushCtx, pairs)
			if err != nil {
				// 批量索引更新失败时退回逐条 Upsert，保证新路径至少能被看到；
				// UpdatePath 内部走 baseCtx，请求断开也不会丢。
				applog.Warn("繁简转换批量更新索引失败，回退逐条重建：%v", err)
				for _, pr := range pairs {
					e.UpdatePath(pr[1])
				}
			} else {
				// 零字节/未入索引的文件批量更新不命中，补一次完整 Upsert
				for _, np := range missing {
					e.UpdatePath(np)
				}
			}
			pairs = pairs[:0]
		}
	}
	defer flush()
	for _, p := range paths {
		p = filepath.Clean(p)
		// v1.30.1 安全修复：三步防线（Clean+软链接解析+卷前缀重判）。
		// 此前只查 Lstat，卷内软链接指向卷外文件时会被改名并污染索引。
		if !e.ValidPath(p) {
			snap.Skip(p, "文件不在受管卷内或路径无法解析（含软链接指向卷外）")
			res.Failed = append(res.Failed, p)
			res.Errors[p] = "文件不在受管卷内或路径无法解析（含软链接指向卷外）"
			continue
		}
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			snap.Skip(p, "文件不存在或不是普通文件")
			res.Failed = append(res.Failed, p)
			res.Errors[p] = "文件不存在或不是普通文件"
			continue
		}
		if strings.Contains(filepath.ToSlash(p), "/.verify-trash/") {
			snap.Skip(p, "隔离区内的文件不支持转换")
			res.Failed = append(res.Failed, p)
			res.Errors[p] = "隔离区内的文件不支持转换"
			continue
		}
		if reason := e.protectedReason(p); reason != "" {
			snap.Skip(p, reason)
			res.Failed = append(res.Failed, p)
			res.Errors[p] = reason
			continue
		}
		dir := filepath.Dir(p)
		name := filepath.Base(p)
		// v3.10.8：Linux 文件名是任意字节序列，非 UTF-8 名称交给繁简转换会变成
		// 替换字符组成的乱码，JSON 接口也无法把原路径完整传回前端。这里不猜测式改写，
		// 直接跳过并说明原因，避免把「转换」做成「毁名」。
		if !utf8.ValidString(name) {
			snap.Skip(p, "文件名不是合法 UTF-8，暂不支持自动转换")
			res.Failed = append(res.Failed, p)
			res.Errors[p] = "文件名不是合法 UTF-8，暂不支持自动转换"
			continue
		}
		converted := conv.Convert(name)
		if converted == name {
			snap.Skip(p, "文件名已是目标字体或不含中文字")
			res.Skipped = append(res.Skipped, p) // 无中文字或已是目标体
			continue
		}
		newPath := UniquePathEng(dir, converted, p)
		// v3.25.1：候选名检查与真正改名之间仍有窗口，外部进程可能抢占同名文件。
		// 用「不覆盖」改名替代裸 os.Rename，冲突时按失败上报而不是静默覆盖。
		if err := renameNoOverwrite(p, newPath); err != nil {
			snap.Skip(p, renameFailText(err))
			res.Failed = append(res.Failed, p)
			res.Errors[p] = renameFailText(err)
			continue
		}
		// v3.10.8（P2-2）：磁盘已改名，快照先标记，索引与审计交给攒批统一落库。
		snap.MarkMoved(p, newPath, opExpectRenamed)
		audits = append(audits, store.AuditEntry{TaskID: taskID, Operation: "fanjian", OriginalPath: p, NewPath: newPath})
		oldPaths = append(oldPaths, p)
		pairs = append(pairs, [2]string{p, newPath})
		res.Done = append(res.Done, RenameDone{From: p, To: newPath})
		if len(pairs) >= fanjianBatch {
			flush()
		}
	}
	// 先落库再对账：否则最后一批改名可能还停在内存里，对账看到的索引是旧的。
	flush()
	if len(res.Done) > 0 {
		res.TaskID = taskID
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
		// v3.10.0 UI-02：繁简转换同样改了健康页列表里的路径，
		// 只发 update 时其他窗口停在健康页不会重拉，这里补一次健康事件。
		e.pushHealth()
		applog.Info("繁简转换完成：%d 个（方向=%t）", len(res.Done), toTraditional)
	}
	// v3.10.1：批量改名失败过去不留任何痕迹，用户只能看到页面报错。
	// 这里补一条汇总日志（只记首个样例，避免上千条刷屏），便于事后排查权限等问题。
	if len(res.Failed) > 0 {
		first := res.Failed[0]
		applog.Warn("繁简转换失败 %d 个；首个路径 %s：%s", len(res.Failed), first, res.Errors[first])
	}
	res.Reconcile = e.finishOpSnapshot(snap)
	return res
}

// UniquePathEng 引用 health.UniquePath（避免包名混淆的本地别名）。
func UniquePathEng(dir, name, exclude string) string {
	return uniquePathSkip(dir, name, exclude)
}

// uniquePathSkip 冲突追加 _1（排除自身原路径——改名前原文件还在）。
func uniquePathSkip(dir, name, exclude string) string {
	candidate := filepath.Join(dir, name)
	if candidate == exclude {
		return candidate
	}
	if _, err := os.Lstat(candidate); err != nil {
		return candidate
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	ext := filepath.Ext(name)
	for i := 1; i <= 999; i++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s_%d%s", base, i, ext))
		if candidate == exclude {
			return candidate
		}
		if _, err := os.Lstat(candidate); err != nil {
			return candidate
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s_%d%s", base, 999, ext))
}

// UndoFanjian 撤销繁简转换任务（audit 逆序回放）。
// failures 回传失败原因（v3.10.4）：原路径被占用时明确告知，不再静默覆盖。
func (e *Engine) UndoFanjian(ctx context.Context, taskID int64) (done, failed int, failures []HealthOpItem, err error) {
	entries, err := e.st.ListAuditByTask(ctx, taskID)
	if err != nil {
		return 0, 0, nil, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		a := entries[i]
		if a.Operation != "fanjian" {
			continue
		}
		if reason := undoRenameTarget(a.NewPath, a.OriginalPath); reason != "" {
			failed++
			failures = append(failures, HealthOpItem{Path: a.OriginalPath, NewPath: a.NewPath, Error: reason})
			continue
		}
		e.dropIndexPath(ctx, nil, a.NewPath)
		e.UpdatePath(a.OriginalPath)
		done++
	}
	if len(failures) > 0 {
		applog.Warn("撤销繁简转换失败 %d 个；首个路径 %s：%s", len(failures), failures[0].Path, failures[0].Error)
	}
	if done > 0 {
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
		// v3.10.0 UI-02：撤销同样影响健康页路径，补健康事件让其他窗口同步刷新。
		e.pushHealth()
	}
	return done, failed, failures, nil
}

// NameHasTraditional v1.27.0：文件名是否含繁体字（t2s 转换后有差异即含）。
// 返回 (是否含繁体, 转简后的文件名)。
func NameHasTraditional(name string) (bool, string) {
	// 非 UTF-8 名称转换后只会得到替换字符，检测结果与展示都不可信，直接判为「不含」。
	if !utf8.ValidString(name) {
		return false, name
	}
	simp := dictconv.T2S().Convert(name)
	if simp == name {
		return false, name
	}
	return true, simp
}
