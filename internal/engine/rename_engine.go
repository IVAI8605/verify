// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 本文件是 v3.5.0 的通用重命名规则引擎（设计文档 §6）。
//
// 与现有 RenameOne/RenameGroupTo 的关系：旧入口保持不动，服务于查重结果页的
// 快速改名；本引擎面向独立「重命名」页，支持链式方法、ApplyTo 三档、预览、
// 批次撤销与 18 万级批量执行。
//
// 性能设计（对齐 §6.7）：
//  1. 计划阶段纯字符串计算，零 IO（标签方法需要元数据时才查库）；
//  2. 冲突检查合并成一次内存 used 集合 + 必要的 Lstat；
//  3. 执行阶段 4–8 个 worker 并发 os.Rename，每 500 个文件 flush 一次
//     审计与 files 路径更新，SSE 每 500 个进度一次。
//
// 红线：保护目录在计划与执行两处拦截；撞名追加 _1，绝不覆盖。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"verify/internal/applog"
	"verify/internal/store"
	"verify/internal/taskguard"
)

// RenamePair 是 ListReplace 的一条替换对。
type RenamePair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RenameMethod 是一个重命名方法；字段按方法类型取用，未用到的留空即可。
type RenameMethod struct {
	Type          string       `json:"type"`
	ApplyTo       string       `json:"apply_to"` // name / extension / both（默认 name）
	Count         int          `json:"count"`
	FromEnd       bool         `json:"from_end"`
	Pattern       string       `json:"pattern"`
	Replacement   string       `json:"replacement"`
	Regex         bool         `json:"regex"`
	CaseSensitive bool         `json:"case_sensitive"`
	Occurrence    int          `json:"occurrence"` // 0=全部
	Pairs         []RenamePair `json:"pairs"`
	Text          string       `json:"text"`
	Position      string       `json:"position"` // head / tail / match
	Separator     string       `json:"separator"`
	IndexA        int          `json:"index_a"`
	IndexB        int          `json:"index_b"`
	Start         int          `json:"start"`
	Step          int          `json:"step"`
	Digits        int          `json:"digits"`
	Mode          string       `json:"mode"`
	Chars         string       `json:"chars"`
	Names         []string     `json:"names"`
}

// RenamePlanItem 是一个文件的改名计划。
type RenamePlanItem struct {
	OldPath      string `json:"old_path"`
	NewPath      string `json:"new_path"`
	OldName      string `json:"old_name"`
	NewName      string `json:"new_name"`
	HadCollision bool   `json:"had_collision,omitempty"`
	CollisionN   int    `json:"collision_n,omitempty"`
	Touch        bool   `json:"touch,omitempty"`
	Error        string `json:"error,omitempty"`
	// Size / Mtime 是计划生成时的文件身份快照（v3.25.0 P1-2）。
	// 落库后隔一段时间执行，用它确认 old_path 上还是当初那个文件。
	Size  int64 `json:"size,omitempty"`
	Mtime int64 `json:"mtime,omitempty"`
}

// RenameEngineResult 是批量执行结果。
type RenameEngineResult struct {
	BatchID int64                   `json:"batch_id"`
	TaskID  int64                   `json:"task_id"`
	Done    int                     `json:"done"`
	Failed  int                     `json:"failed"`
	Items   []store.RenameBatchItem `json:"items"`
	// Reconcile 是操作后对账结果（v3.10.5）：有差异时前端列路径并给出下一步建议。
	Reconcile *ReconcileReport `json:"reconcile,omitempty"`
}

// BuildRenamePlan 生成改名计划（dry-run 预览也走这里，不落盘）。
func (e *Engine) BuildRenamePlan(ctx context.Context, paths []string, methods []RenameMethod) []RenamePlanItem {
	hasTags := methodsNeedMeta(methods)
	plan := make([]RenamePlanItem, 0, len(paths))
	for _, p := range paths {
		p = filepath.Clean(p)
		item := RenamePlanItem{OldPath: p, OldName: filepath.Base(p)}
		// v3.17.3（F-09）：非 UTF-8 文件名经 JSON 往返会变成 U+FFFD，改名后索引与
		// 审计都对不上原路径，用户只看到「索引里没有这个文件」。与健康页改名
		// （planHealthRename）对齐：先拦下，让用户去健康页修编码。
		if !utf8.ValidString(p) {
			item.Error = "路径包含非 UTF-8 字节，请先在健康页修复文件名编码"
			plan = append(plan, item)
			continue
		}
		if reason := e.protectedReasonResolved(p); reason != "" {
			item.Error = reason
			plan = append(plan, item)
			continue
		}
		if strings.Contains(filepath.ToSlash(p), "/.verify-trash/") {
			item.Error = "隔离区内的文件不支持改名"
			plan = append(plan, item)
			continue
		}
		// v3.10.4：规则引擎此前只查保护目录与 Lstat，漏了三步防线；
		// 卷内软链接指向卷外或 `..` 逃逸的路径会被直接改名并污染索引。
		if !e.ValidPath(p) {
			item.Error = "文件不在受管卷内或路径无法解析（含软链接指向卷外）"
			plan = append(plan, item)
			continue
		}
		fi, statErr := os.Lstat(p)
		if statErr != nil || !fi.Mode().IsRegular() {
			item.Error = "文件不存在或不是普通文件"
			plan = append(plan, item)
			continue
		}
		// v3.25.0（P1-2）：记下此刻的大小与 mtime。计划落库后可能几小时才执行完，
		// 期间同路径可能被换成别的文件，执行前要拿这两个值复核身份。
		item.Size, item.Mtime = fi.Size(), fi.ModTime().UnixNano()
		var meta *store.FileRow
		if hasTags {
			if row, err := e.st.GetFileByPath(ctx, p); err == nil && row != nil {
				meta = row
			}
		}
		newName, touch := applyRenameMethods(item.OldName, len(plan), methods, meta)
		item.NewName = newName
		item.Touch = touch
		// v3.17.3（F-03）：改名规则可以把任意文本拼进文件名。含 "/"、"\"
		// 或恰好是 "." / ".." 时，filepath.Join 会把文件写到当前目录之外，
		// 甚至移出受管卷。整条标错而不是静默截断——截断会悄悄改掉用户预期的名字。
		if msg := invalidRenameName(newName); msg != "" {
			item.Error = msg
			plan = append(plan, item)
			continue
		}
		item.NewPath = filepath.Join(filepath.Dir(p), newName)
		if item.NewPath == p {
			item.Error = "目标与原名相同"
		}
		plan = append(plan, item)
	}
	resolveRenameCollisions(plan)
	return plan
}

// methodsNeedMeta 判断方法里是否用了需要文件元数据的 Tag。
func methodsNeedMeta(methods []RenameMethod) bool {
	for _, m := range methods {
		for _, s := range []string{m.Text, m.Replacement} {
			if strings.Contains(s, "<Width>") || strings.Contains(s, "<Height>") ||
				strings.Contains(s, "<Codec>") || strings.Contains(s, "<Year>") ||
				strings.Contains(s, "<Month>") || strings.Contains(s, "<Day>") ||
				strings.Contains(s, "<Inc ") {
				return true
			}
		}
		for _, n := range m.Names {
			if strings.Contains(n, "<") {
				return true
			}
		}
	}
	return false
}

// resolveRenameCollisions 在内存里解决撞名：目标重复或磁盘已存在时追加 _1/_2。
func resolveRenameCollisions(plan []RenamePlanItem) {
	used := map[string]bool{}
	for i := range plan {
		it := &plan[i]
		if it.Error != "" {
			continue
		}
		newPath := it.NewPath
		if used[newPath] || fileExists(newPath) {
			base := it.NewName
			ext := filepath.Ext(base)
			stem := strings.TrimSuffix(base, ext)
			for n := 1; n <= 999; n++ {
				cand := stem + "_" + strconv.Itoa(n) + ext
				cp := filepath.Join(filepath.Dir(it.OldPath), cand)
				if !used[cp] && !fileExists(cp) {
					it.NewName = cand
					it.NewPath = cp
					it.HadCollision = true
					it.CollisionN = n
					break
				}
			}
		}
		used[it.NewPath] = true
	}
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// invalidRenameName 校验规则引擎算出的新文件名，返回中文原因（空串表示可用）。
// v3.17.3（F-03）：改名方法（list / replace / add 等）能拼出任意文本，
// 只有文件名部分合法时后续 filepath.Join 才保证仍落在原目录内。
func invalidRenameName(name string) string {
	switch {
	case name == "" || name == "." || name == "..":
		return "新文件名为空或非法"
	case strings.ContainsAny(name, "/\\\x00"):
		return "新文件名包含路径分隔符或非法字符"
	}
	return ""
}

// renameNoOverwrite 只在目标不存在（或与源是同一个文件）时才执行改名。
//
// v3.10.10（P0-1）：计划阶段已由 resolveRenameCollisions 排过一次撞名，但「预览 → 点执行」
// 之间目标可能被另一个标签页的批次或外部工具占用；直接覆盖会让被覆盖文件既没进隔离区
// 也没有审计，撤销只把名字改回去、内容救不回来。
// v3.22.0：实现按平台拆分——Linux 用 renameat2(RENAME_NOREPLACE) 原子保证不覆盖，
// 其它平台回退 Lstat + rename（见 rename_fallback.go）。

// applyRenameMethods 依次应用方法，返回新文件名与是否需要更新时间戳。
func applyRenameMethods(name string, idx int, methods []RenameMethod, meta *store.FileRow) (string, bool) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	touch := false
	for _, m := range methods {
		if strings.EqualFold(m.Type, "timestamp") {
			touch = true
			continue
		}
		target, kind := renameTarget(stem, ext, m.ApplyTo)
		out := applyOneMethod(target, idx, m, meta)
		// List 方法给的是完整文件名（可含扩展名），按完整名重新拆分；
		// 其他方法仍按 ApplyTo 只替换对应部分，避免出现 a.mp4.mp4。
		if strings.EqualFold(m.Type, "list") && m.ApplyTo != "extension" && out != "" {
			e2 := filepath.Ext(out)
			stem = strings.TrimSuffix(out, e2)
			ext = e2
			continue
		}
		switch kind {
		case "extension":
			ext = "." + strings.TrimPrefix(out, ".")
			if out == "" {
				ext = ""
			}
		case "both":
			// both 直接产出完整名：重新拆分，保持后续方法仍可链式使用。
			if out == "" {
				continue
			}
			e2 := filepath.Ext(out)
			stem = strings.TrimSuffix(out, e2)
			ext = e2
			continue
		default:
			if out != "" {
				stem = out
			}
		}
	}
	return stem + ext, touch
}

// renameTarget 按 ApplyTo 取出要操作的文本。
func renameTarget(stem, ext string, applyTo string) (string, string) {
	switch applyTo {
	case "extension":
		return strings.TrimPrefix(ext, "."), "extension"
	case "both":
		return stem + ext, "both"
	default:
		return stem, "name"
	}
}

// applyOneMethod 是单个方法的纯字符串实现。
func applyOneMethod(target string, idx int, m RenameMethod, meta *store.FileRow) string {
	switch strings.ToLower(m.Type) {
	case "remove":
		return methodRemove(target, m)
	case "remove_pattern":
		return methodRemovePattern(target, m)
	case "replace":
		return methodReplace(target, m)
	case "list_replace":
		for _, p := range m.Pairs {
			if p.From != "" {
				target = strings.ReplaceAll(target, p.From, p.To)
			}
		}
		return target
	case "add":
		return methodAdd(target, idx, m, meta)
	case "renumber":
		return methodRenumber(target, idx, m)
	case "new_case":
		return methodNewCase(target, m.Mode)
	case "trim":
		return methodTrim(target, m.Chars)
	case "swap":
		return methodSwap(target, m)
	case "list":
		if idx < len(m.Names) && strings.TrimSpace(m.Names[idx]) != "" {
			return expandRenameTags(m.Names[idx], idx, meta)
		}
		return target
	default:
		return target
	}
}

func methodRemove(s string, m RenameMethod) string {
	n := m.Count
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if n >= len(r) {
		return ""
	}
	if m.FromEnd {
		return string(r[:len(r)-n])
	}
	return string(r[n:])
}

func methodRemovePattern(s string, m RenameMethod) string {
	if m.Pattern == "" {
		return s
	}
	re, err := renamePattern(m.Pattern, m.Regex, m.CaseSensitive)
	if err != nil {
		return s
	}
	return re.ReplaceAllString(s, "")
}

func methodReplace(s string, m RenameMethod) string {
	if m.Pattern == "" {
		return s
	}
	re, err := renamePattern(m.Pattern, m.Regex, m.CaseSensitive)
	if err != nil {
		return s
	}
	if m.Occurrence <= 0 {
		return re.ReplaceAllString(s, m.Replacement)
	}
	// 只替换第 N 次出现。
	matches := re.FindAllStringIndex(s, -1)
	if len(matches) < m.Occurrence {
		return s
	}
	at := matches[m.Occurrence-1]
	return s[:at[0]] + m.Replacement + s[at[1]:]
}

func renamePattern(pattern string, useRegex, caseSensitive bool) (*regexp.Regexp, error) {
	p := pattern
	if !useRegex {
		p = regexp.QuoteMeta(p)
	}
	if !caseSensitive {
		p = "(?i)" + p
	}
	return regexp.Compile(p)
}

func methodAdd(s string, idx int, m RenameMethod, meta *store.FileRow) string {
	text := expandRenameTags(m.Text, idx, meta)
	switch m.Position {
	case "match":
		if m.Pattern != "" {
			re, err := renamePattern(m.Pattern, m.Regex, m.CaseSensitive)
			if err == nil {
				if loc := re.FindStringIndex(s); loc != nil {
					return s[:loc[1]] + text + s[loc[1]:]
				}
			}
		}
		return s + text
	case "head":
		return text + s
	default:
		return s + text
	}
}

func methodRenumber(s string, idx int, m RenameMethod) string {
	start := m.Start
	if start == 0 {
		start = 1
	}
	step := m.Step
	if step == 0 {
		step = 1
	}
	digits := m.Digits
	if digits < 1 {
		digits = 2
	}
	// v3.17.3（F-07）：位数决定零填充宽度，上百万位会让 fmt.Sprintf 单次分配 GB 级
	// 内存。Web 入口已拦到 1–10，这里再兜一次，防止其它调用方绕过。
	if digits > 10 {
		digits = 10
	}
	num := fmt.Sprintf("%0*d", digits, start+idx*step)
	sep := m.Text
	return s + sep + num
}

func methodNewCase(s, mode string) string {
	switch strings.ToLower(mode) {
	case "upper":
		return strings.ToUpper(s)
	case "lower":
		return strings.ToLower(s)
	case "title":
		return renameTitleCase(s)
	case "sentence":
		r := []rune(strings.ToLower(s))
		if len(r) > 0 {
			r[0] = []rune(strings.ToUpper(string(r[0])))[0]
		}
		return string(r)
	default:
		return s
	}
}

func renameTitleCase(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		switch r {
		case ' ', '-', '_', '.', '·':
			b.WriteRune(r)
			up = true
			continue
		}
		if up {
			b.WriteString(strings.ToUpper(string(r)))
			up = false
		} else {
			b.WriteString(strings.ToLower(string(r)))
		}
	}
	return b.String()
}

func methodTrim(s, chars string) string {
	if chars == "" {
		return strings.TrimSpace(s)
	}
	return strings.Trim(s, " "+chars)
}

func methodSwap(s string, m RenameMethod) string {
	sep := m.Separator
	if sep == "" {
		sep = "_"
	}
	parts := strings.Split(s, sep)
	if m.IndexA < 0 || m.IndexB < 0 || m.IndexA >= len(parts) || m.IndexB >= len(parts) || m.IndexA == m.IndexB {
		return s
	}
	parts[m.IndexA], parts[m.IndexB] = parts[m.IndexB], parts[m.IndexA]
	return strings.Join(parts, sep)
}

var incTagRe = regexp.MustCompile(`<Inc Nr:(\d+)>`)

// expandRenameTags 实现最小 Tag 集：<Inc Nr:N>、<Year>/<Month>/<Day>、<Width>/<Height>/<Codec>。
// 不做 Exif/ID3：项目没有 exiftool，也不允许新增二进制。
func expandRenameTags(s string, idx int, meta *store.FileRow) string {
	if !strings.Contains(s, "<") {
		return s
	}
	s = incTagRe.ReplaceAllStringFunc(s, func(tag string) string {
		sub := incTagRe.FindStringSubmatch(tag)
		digits, _ := strconv.Atoi(sub[1])
		if digits < 1 || digits > 8 {
			digits = 3
		}
		return fmt.Sprintf("%0*d", digits, idx+1)
	})
	var t time.Time
	if meta != nil && meta.Mtime > 0 {
		t = time.Unix(meta.Mtime, 0)
	} else {
		t = time.Now()
	}
	repl := strings.NewReplacer(
		"<Year>", t.Format("2006"),
		"<Month>", t.Format("01"),
		"<Day>", t.Format("02"),
	)
	s = repl.Replace(s)
	if meta != nil {
		s = strings.NewReplacer(
			"<Width>", strconv.Itoa(meta.Width),
			"<Height>", strconv.Itoa(meta.Height),
			"<Codec>", meta.Codec,
		).Replace(s)
	} else {
		s = strings.NewReplacer("<Width>", "", "<Height>", "", "<Codec>", "").Replace(s)
	}
	return s
}

// ExecuteRenamePlan 执行改名计划：并发 rename + 每 500 条批量落库。
func (e *Engine) ExecuteRenamePlan(ctx context.Context, plan []RenamePlanItem, methods []RenameMethod, presetID int64) (*RenameEngineResult, error) {
	// v3.10.10（P0-1）：两个标签页同时执行各自的预览计划时，仅靠执行前的 Lstat 复核
	// 仍会互相插入；这里把整个批次串行化，保证「检查目标 → rename」不被同进程的另一个批次打断。
	// 与扫描/健康检测/索引体检互斥：这些任务会同时写 files 与 health_results。
	rel, gerr := e.acquireTask(taskguard.KindRename, "执行改名方案", taskguard.GroupIO, taskguard.ResDiskIO)
	if gerr != nil {
		return nil, gerr
	}
	defer rel("done")
	unlock := e.lockWrites()
	defer unlock()
	// v3.17.3（F-08）：改名同时写磁盘与 files/health 索引，与扫描、健康检测、
	// 索引体检并发会互相覆盖（扫描收尾 Upsert 会把旧路径写回来，体检会删掉刚更新的行）。
	// 与清理计划同款三向互斥；检查放在拿到写锁之后，避免判完就被另一路径插入。
	if e.IsRunning() {
		return nil, fmt.Errorf("扫描进行中，稍后再试")
	}
	if e.HealthRunning() {
		return nil, fmt.Errorf("文件健康检测进行中，稍后再试")
	}
	if e.IndexRepairRunning() {
		return nil, fmt.Errorf("索引体检进行中，稍后再试")
	}
	// 执行前再拦一次保护目录与冲突（计划可能生成后配置才变化）。
	exec := make([]RenamePlanItem, 0, len(plan))
	for _, it := range plan {
		if it.Error != "" {
			exec = append(exec, it)
			continue
		}
		if reason := e.protectedReasonResolved(it.OldPath); reason != "" {
			it.Error = reason
			exec = append(exec, it)
			continue
		}
		// v3.25.0（P1-2）：计划生成到执行之间可能隔几小时，old_path 上可能已被
		// 换成另一个文件。不复核就按计划改名，会把没参与计划的文件改掉并写进索引。
		// 旧库计划没有快照（0），跳过复核以保持升级前行为。
		if it.Size != 0 || it.Mtime != 0 {
			fi, statErr := os.Lstat(it.OldPath)
			if statErr != nil || !fi.Mode().IsRegular() {
				it.Error = "文件已不存在或不是普通文件，请重新生成改名计划"
				exec = append(exec, it)
				continue
			}
			if fi.Size() != it.Size || fi.ModTime().UnixNano() != it.Mtime {
				it.Error = "文件已变化，请重新生成改名计划"
				exec = append(exec, it)
				continue
			}
		}
		// v3.17.3（F-03）：计划阶段已拦新名里的分隔符，这里再对「预览→执行」之间
		// 可能变化的父目录做一次三步防线复核，防止软链接把目标目录指到卷外。
		if !e.ValidDirPath(filepath.Dir(it.NewPath)) {
			it.Error = "目标目录不在受管卷内或路径无法解析（含软链接指向卷外）"
			exec = append(exec, it)
			continue
		}
		exec = append(exec, it)
	}
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers < 2 {
		workers = 2
	}
	// v3.10.5：规则引擎可一次改十几万个文件，快照只记本次涉及的路径（不碰全库），
	// 采集仍是一次 Lstat + 一次批量哈希查询，不会额外读文件内容。
	snapPaths := make([]string, 0, len(exec))
	for i := range exec {
		if exec[i].Error == "" {
			snapPaths = append(snapPaths, exec[i].OldPath)
		}
	}
	snap := e.beginOpSnapshot(ctx, "rename_engine", snapPaths)
	// v3.10.10（第五轮 P1-2）：批次行与任务号必须在任何 rename 之前落库，
	// 否则崩溃后磁盘上已经改名、库里却没有 batch_id，前端「撤销本批」没有入口。
	// 批次写不进去就不动手（fail-closed），避免产生无法回放的改名。
	taskID, err := e.st.NewAuditTaskID(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建改名任务号失败：%w", err)
	}
	rawMethods, _ := json.Marshal(methods)
	batchCtx, cancelBatch := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	batchID, err := e.st.CreateRenameBatchRunning(batchCtx, presetID, len(exec), string(rawMethods), taskID)
	cancelBatch()
	if err != nil {
		return nil, fmt.Errorf("创建改名批次失败：%w", err)
	}
	// v3.10.12（P1-03）：执行前把整批 old→new 写成 pending 意图行。
	// 写不进去就不动手——没有意图行，崩溃后无法区分「没改」与「改了没记账」，
	// 磁盘上已经改名的文件会变成没有任何撤销入口的孤儿。
	intentPairs := make([][2]string, 0, len(exec))
	for i := range exec {
		if exec[i].Error == "" {
			intentPairs = append(intentPairs, [2]string{exec[i].OldPath, exec[i].NewPath})
		}
	}
	intentCtx, cancelIntent := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	ierr := e.st.AddRenameIntents(intentCtx, batchID, taskID, intentPairs)
	cancelIntent()
	if ierr != nil {
		finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = e.st.FinishRenameBatch(finishCtx, batchID, "[]", "failed")
		cancelFinish()
		return nil, fmt.Errorf("记录改名意图失败，已取消本次改名：%w", ierr)
	}
	type result struct {
		idx int
		err error
	}
	jobs := make(chan int)
	results := make(chan result, workers*2)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}
				it := exec[i]
				// v3.10.10（P0-1）：执行时复核目标，避免覆盖预览后被别人占用的同名文件。
				if err := renameNoOverwrite(it.OldPath, it.NewPath); err != nil {
					results <- result{idx: i, err: err}
					continue
				}
				if it.Touch {
					now := time.Now()
					_ = os.Chtimes(it.NewPath, now, now)
				}
				results <- result{idx: i}
			}
		}()
	}
	// 分发（跳过计划里已标记错误的项）。
	go func() {
		for i := range exec {
			if exec[i].Error != "" {
				continue
			}
			select {
			case jobs <- i:
			case <-ctx.Done():
				close(jobs)
				return
			}
		}
		close(jobs)
	}()

	items := make([]store.RenameBatchItem, len(exec))
	for i := range exec {
		items[i] = store.RenameBatchItem{Path: exec[i].OldPath, Orig: exec[i].OldName, Target: exec[i].NewName, Error: exec[i].Error, TaskID: taskID}
	}
	var audits []store.AuditEntry
	var pairs [][2]string
	// v3.10.10（第五轮 P1-2）：提交粒度从 500 降到 100。原来的崩溃窗口是「最多 499 个
	// 文件已在磁盘改名但无审计无索引」，压到两位数；18 万文件也只多出约一千多个短事务。
	const flushEvery = 100
	done, failed := 0, 0
	for i := range exec {
		if exec[i].Error != "" {
			failed++
		}
	}
	flush := func() {
		// v3.10.8：改名已经落到磁盘，索引与审计不能因为客户端断开（ctx 被取消）就不写，
		// 否则列表里还是旧名字、撤销也找不到记录。这里用脱离取消的上下文兜住。
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		auditErr := e.st.AddAuditBatch(writeCtx, audits)
		if auditErr != nil {
			applog.Warn("批量写入改名审计失败（%d 条）：%s", len(audits), auditErr.Error())
		}
		// v3.10.8：同时刷新 name/ext，只改 path 会让按扩展名筛选、同名分组继续拿旧值。
		missing, err := e.st.RenameFilePathsBatch(writeCtx, pairs)
		if err != nil {
			applog.Warn("批量更新改名索引失败（%d 条）：%s", len(pairs), err.Error())
		} else {
			if len(missing) > 0 {
				applog.Info("改名后有 %d 个文件不在索引中（零字节或尚未扫描）", len(missing))
			}
			// v3.17.3（F-01）：健康结论按路径存，改名后旧路径的行不会自己消失，
			// 健康页会继续列出已经不存在的文件。失败只告警不回滚——磁盘已改名。
			olds := make([]string, 0, len(pairs))
			for _, pr := range pairs {
				olds = append(olds, pr[0])
			}
			if herr := e.st.DropHealthByPaths(writeCtx, olds); herr != nil {
				applog.Warn("改名后清理旧路径健康结论失败（%d 条）：%s", len(olds), herr.Error())
			}
		}
		// v3.10.12（P1-03）：审计与索引都写成功才把意图标 done；
		// 中途崩溃留在 pending，启动恢复会按磁盘现状补齐，不会漏掉撤销记录。
		if auditErr == nil && err == nil {
			if merr := e.st.MarkRenameIntentsDone(writeCtx, batchID, pairs); merr != nil {
				applog.Warn("标记改名意图完成失败（%d 条，下次启动按磁盘校正）：%s", len(pairs), merr.Error())
			}
		}
		audits = audits[:0]
		pairs = pairs[:0]
	}
	collect := make(chan struct{})
	go func() {
		defer close(collect)
		for r := range results {
			it := &exec[r.idx]
			if r.err != nil {
				items[r.idx].Error = renameFailText(r.err)
				failed++
				// 改名没成功，文件仍在原路径，不属于「与预期不符」。
				snap.Skip(it.OldPath, renameFailText(r.err))
				continue
			}
			items[r.idx].NewPath = it.NewPath
			done++
			if it.Touch {
				// 该方法主动改 mtime，对账不能再拿旧 mtime 比对。
				snap.IgnoreMtime(it.OldPath)
			}
			snap.MarkMoved(it.OldPath, it.NewPath, opExpectRenamed)
			audits = append(audits, store.AuditEntry{TaskID: taskID, Operation: "rename", OriginalPath: it.OldPath, NewPath: it.NewPath})
			pairs = append(pairs, [2]string{it.OldPath, it.NewPath})
			if len(audits) >= flushEvery {
				flush()
				e.Bus.Publish(Event{Type: "rename_progress", Data: map[string]any{"done": done, "total": len(exec)}})
			}
		}
	}()
	wg.Wait()
	close(results)
	<-collect
	flush()
	for i := range exec {
		if exec[i].Error != "" {
			snap.Skip(exec[i].OldPath, exec[i].Error)
			continue
		}
		// 用户中途取消时，已分发但没跑到的项没有结果，按「未执行」处理。
		if items[i].NewPath == "" && items[i].Error == "" {
			snap.Skip(exec[i].OldPath, "任务已取消，未执行")
		}
	}
	// 批次保存在后面，对账结果先算好：即便批次落库失败，快照与差异也已写进库里。
	reconcile := e.finishOpSnapshot(snap)
	if done > 0 {
		e.bumpVersion()
		e.Bus.Publish(Event{Type: "update"})
	}

	status := "done"
	if done == 0 && failed > 0 {
		status = "failed"
	}
	// v3.22.0：用户取消或崩溃时，后面的项没有结果也没有错误，批次要标 partial，
	// 这样「未完成批次」提示与撤销入口都能找到它，不会像 done 一样被当成完整收尾。
	for i := range items {
		if items[i].NewPath == "" && items[i].Error == "" {
			status = "partial"
			break
		}
	}
	raw, _ := json.Marshal(items)
	// v3.10.8（P2-6）：磁盘已改名，批次记录必须落地，否则前端拿不到 batch_id、按批撤销入口缺失。
	// v3.10.10（第五轮 P1-2）：批次行开局已落，这里只补明细与最终状态；补齐失败不再让整个
	// 响应报错——改名已经完成，批次行仍带着任务号，撤销入口照旧可用。
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	if err := e.st.FinishRenameBatch(finishCtx, batchID, string(raw), status); err != nil {
		applog.Warn("补齐改名批次明细失败（批次 %d，仍可凭任务号撤销）：%s", batchID, err.Error())
	}
	cancelFinish()
	applog.Info("规则引擎改名完成：%d 个成功，%d 个失败（批次 %d）", done, failed, batchID)
	return &RenameEngineResult{BatchID: batchID, TaskID: taskID, Done: done, Failed: failed, Items: items, Reconcile: reconcile}, nil
}

// UndoRenameBatch 按批撤销：优先用批次任务号整批回退，旧批次按逐项任务号回退。
// failures 汇总各任务的失败原因（v3.10.4），供前端明确提示「原文件名已被占用」等。
func (e *Engine) UndoRenameBatch(ctx context.Context, batchID int64) (done, failed int, status string, failures []HealthOpItem, err error) {
	rel, gerr := e.acquireTask(taskguard.KindRename, "撤销批量改名", taskguard.GroupIO, taskguard.ResDiskIO)
	if gerr != nil {
		return 0, 0, "", nil, gerr
	}
	defer rel("done")
	unlock := e.lockWrites()
	defer unlock()
	// v3.10.10（第五轮 P1-3）：撤销已落盘，批次状态写入不能跟请求取消一起丢。
	// v3.10.11（P1-01）：这个 ctx 只在写状态时创建——放在函数开头的话，撤销上万文件
	// 超过 30 秒后它已过期，最后一次状态写入必然失败，批次会永远停在 running。
	currentStatus, raw, batchTaskID, err := e.st.GetRenameBatchMeta(ctx, batchID)
	if err != nil {
		return 0, 0, "", nil, err
	}
	// v3.10.13：已经完整撤过的批次再点一次撤销直接返回，不再回放、不覆盖状态。
	// 旧实现会把「文件早就改回去了」记成失败，把 undone 降级成 undone_failed。
	if currentStatus == "undone" {
		return 0, 0, "undone", nil, nil
	}
	// 总预算：单条 30 秒的写法能让一条慢盘条目卡住整批，整个撤销却没有任何总时限。
	// v3.22.0：默认改为 4 小时并可配置——40 万条分片改名后逐批撤销需要更长时间窗口，
	// 到点仍会停止回放，剩余条目计入 failed，批次落 partial 可重试。
	budget := time.Duration(e.cfg.Get().RenameUndoMinutes) * time.Minute
	if budget <= 0 {
		budget = undoTotalBudget
	}
	if budget > 24*time.Hour {
		budget = 24 * time.Hour
	}
	undoCtx, cancelUndo := context.WithTimeout(ctx, budget)
	defer cancelUndo()
	var items []store.RenameBatchItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return 0, 0, "", nil, fmt.Errorf("改名批数据损坏")
	}
	taskIDs := map[int64]bool{}
	for _, it := range items {
		if it.TaskID > 0 {
			taskIDs[it.TaskID] = true
		}
	}
	// v3.10.10（第五轮 P1-2）：批次行开局就落库，崩溃时明细可能还没补齐；
	// 批次任务号能定位 audit_log 里的整批改名，撤销不能因为明细缺失而失效。
	if batchTaskID > 0 {
		taskIDs[batchTaskID] = true
	}
	for tid := range taskIDs {
		// 已持有写锁，走不加锁的回放实现，避免同锁重入。
		d, f, fs, uerr := e.undoGroupRename(undoCtx, tid)
		done += d
		failed += f
		failures = append(failures, fs...)
		if uerr != nil {
			failed++
			failures = append(failures, HealthOpItem{Error: uerr.Error()})
		}
	}
	status = undoBatchStatus(done, failed)
	wctx, cancel := detachCtx(ctx)
	if err := e.st.FinishRenameBatchState(wctx, batchID, status, failed); err != nil {
		applog.Warn("撤销已完成，但批次状态写入失败（批次 %d）：%v", batchID, err)
	}
	// v3.10.12（P2-01）：崩溃遗留的 rename_engine 快照不会自己收尾，
	// 撤销后文件已回到原路径，快照里的「仍在原路径」属于正常结果，必须结清，
	// 否则启动告警与未完成快照提示会永久挂着。
	if n, ferr := e.st.FinishUnfinishedOpSnapshotsByKind(wctx, "rename_engine",
		fmt.Sprintf("改名批次 #%d 已撤销，快照随批次结清", batchID)); ferr != nil {
		applog.Warn("结清改名快照失败（批次 %d，下次启动仍会提示）：%v", batchID, ferr)
	} else if n > 0 {
		applog.Info("已结清 %d 个与撤销批次相关的未完成改名快照", n)
	}
	cancel()
	return done, failed, status, failures, nil
}

// undoTotalBudget 是撤销总预算的兜底值；实际使用配置项 rename_undo_minutes（默认 240）。
const undoTotalBudget = 4 * time.Hour

// undoBatchStatus 决定撤销结束后批次的状态（v3.10.11 P1-01）。
// 只有全部成功才算 undone（含空批次——没有任何可撤销项时没有失败，不该挂撤销失败告警）；
// 一个都没撤销成功且确有失败才算 undone_failed；
// 部分成功（含「文件已改回但索引没跟上」）必须单独标成 undone_partial，
// 否则用户会照着「全部成功」的结论停止排查，索引错位一直留着。
func undoBatchStatus(done, failed int) string {
	switch {
	case failed == 0:
		return "undone"
	case done == 0:
		return "undone_failed"
	default:
		return "undone_partial"
	}
}

// renameFailText 把改名失败整理成用户能照做的中文提示（v3.10.1）。
// 飞牛的文件权限走 Windows ACL，应用未被授权时 syscall 只回 permission denied；
// 直接展示英文错误用户无从下手，所以这里翻译成授权步骤，并保留原始错误便于排查。
func renameFailText(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	if errors.Is(err, fs.ErrPermission) || strings.Contains(lower, "permission denied") || strings.Contains(lower, "operation not permitted") {
		return "没有修改权限：请在飞牛「文件管理」打开该文件夹的「详细信息 → 权限」，为查重助手授予读写权限后重试（" + msg + "）"
	}
	// v3.25.1：改名统一走 renameNoOverwrite，目标被外部进程抢占时返回 EEXIST。
	// 这里给出可执行的中文提示，而不是抛出生硬的 "file exists"。
	if errors.Is(err, fs.ErrExist) || strings.Contains(lower, "file exists") {
		return "目标文件已存在，已跳过（请重新生成改名方案）"
	}
	return "改名失败：" + msg
}
