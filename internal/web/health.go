// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"net/http"
	"verify/internal/engine"
	"verify/internal/health"
)

// healthScanRequest 是启动检测的请求体。
type healthScanRequest struct {
	Kind string `json:"kind"`
	// ContentMode 仅对空文件检测生效：none / zeroed / unprintable
	ContentMode string `json:"content_mode"`
	// RestrictCharset 与 NonCJK 仅对文件名检测生效，默认关闭
	RestrictCharset bool `json:"restrict_charset"`
	NonCJK          bool `json:"non_cjk"`
	Workers         int  `json:"workers"`
	// Force 为真时重新探测已有结论（损坏视频检测）
	Force bool `json:"force"`
}

// parseContentMode 把前端字符串映射成内容检查模式。
func parseContentMode(v string) health.ContentMode {
	switch v {
	case "zeroed":
		return health.ContentZeroed
	case "unprintable":
		return health.ContentUnprintable
	}
	return health.ContentNone
}

// handleHealthScan 启动一次文件健康检测。
func (s *Server) handleHealthScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body healthScanRequest
	_ = json.NewDecoder(r.Body).Decode(&body)

	kind := health.Kind(body.Kind)
	if !kind.Valid() && !health.IsAll(kind) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "未知的检测类型"})
		return
	}
	// 全量扫描里 ffmpeg 只影响损坏文件一类，缺了它就跳过该类型，而不是整轮失败。
	if kind == health.KindBroken && !s.e.RunnerAvailable() {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "未找到 ffmpeg，无法检测损坏视频"})
		return
	}

	opt := healthOptions(kind, body)
	if err := s.e.StartHealth(kind, opt); err != nil {
		writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, kindOKResponse{OK: true, Kind: string(kind)})
}

// healthOptions 把请求体翻译成引擎参数，只保留该检测类型真正用得上的字段。
func healthOptions(kind health.Kind, body healthScanRequest) engine.HealthOptions {
	opt := engine.HealthOptions{Workers: body.Workers, Force: body.Force}
	if health.IsAll(kind) {
		// 全量扫描要同时跑空文件与文件名两类，它们各自的选项都得带上。
		opt.ContentMode = parseContentMode(body.ContentMode)
		opt.NameOpts = health.NameOptions{
			RestrictCharset: body.RestrictCharset,
			NonCJKNonASCII:  body.NonCJK,
		}
		return opt
	}
	switch kind {
	case health.KindEmpty:
		opt.ContentMode = parseContentMode(body.ContentMode)
	case health.KindName:
		opt.NameOpts = health.NameOptions{
			RestrictCharset: body.RestrictCharset,
			NonCJKNonASCII:  body.NonCJK,
		}
	}
	return opt
}

// handleHealthCancel 取消正在进行的检测。
func (s *Server) handleHealthCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	s.e.CancelHealth()
	writeJSON(w, 200, okResponse{OK: true})
}

// handleHealthStatus 返回进度、四类计数与 ffmpeg 可用性。
func (s *Server) handleHealthStatus(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	counts, err := s.st.HealthCounts(r.Context())
	if err != nil {
		counts = map[string]int64{}
	}
	lastAudit, _ := s.e.HealthLastAuditTask(r.Context())
	probedTotal, probedBroken, _ := s.st.ProbeStats(r.Context())
	// v3.15.0：把「无法确定」的条目单独统计，界面用 counts - uncertain 显示可处理数量。
	// 只有扩展名（内容认不出）与损坏文件（超时/过大未检）会产生这类结论。
	uncertain := map[string]int64{}
	if n, err := s.st.CountHealthByReason(r.Context(), string(health.KindExt), health.ReasonUnknown); err == nil && n > 0 {
		uncertain[string(health.KindExt)] = n
	}
	for _, reason := range []string{health.ReasonTimeout, health.ReasonOversized} {
		if n, err := s.st.CountHealthByReason(r.Context(), string(health.KindBroken), reason); err == nil {
			uncertain[string(health.KindBroken)] += n
		}
	}
	writeJSON(w, 200, healthStatusResponse{Progress: s.e.HealthState(), Counts: counts, Uncertain: uncertain, FFmpeg: s.e.RunnerAvailable(), LastAudit: lastAudit, Auto: s.e.AutoProbeState(), ProbeCache: probeCacheResponse{Total: probedTotal, Broken: probedBroken}})
}

// handleHealthResults 按类别分页返回结果。
func (s *Server) handleHealthResults(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	kind := health.Kind(r.URL.Query().Get("kind"))
	if !kind.Valid() {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "未知的检测类型"})
		return
	}
	// v3.10.8：前端按 2000 一页拉取（settings.js 注释即此口径），HTTP 入口统一收敛。
	// store 层仍保留更大的内部上限，避免把「接口契约」和「内部查询能力」绑死。
	limit := int(intParam(r, "limit", 500, 1, 2000))
	offset := int(intParam(r, "offset", 0, 0, 1<<30))

	items, err := s.st.ListHealthResults(r.Context(), string(kind), limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	total, _ := s.st.CountHealthResults(r.Context(), string(kind))
	writeJSON(w, 200, healthItemsResponse{Kind: string(kind), Total: total, Items: items})
}

// healthPathsRequest 是需要操作的文件列表。
type healthPathsRequest struct {
	Kind            string   `json:"kind"`
	Paths           []string `json:"paths"`
	RestrictCharset bool     `json:"restrict_charset"`
	NonCJK          bool     `json:"non_cjk"`
	// OnlyUppercase 为真时只处理扩展名含大写的文件
	OnlyUppercase bool `json:"only_uppercase"`
}

// handleHealthRename 批量修正扩展名或文件名。
func (s *Server) handleHealthRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body healthPathsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体解析失败"})
		return
	}
	kind := health.Kind(body.Kind)
	if kind != health.KindExt && kind != health.KindName {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "该类型不支持重命名"})
		return
	}
	if len(body.Paths) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "未选择文件"})
		return
	}
	// v3.10.0 UI-03：检测进行中改名，当轮结果仍按旧路径落库，界面会立刻出现死行；
	// 直接挡住，等这轮检测结束再改，比事后补刷更稳。
	if s.e.HealthRunning() {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "健康检测进行中，请等检测结束再改名"})
		return
	}
	if s.scanBusy(w) {
		return
	}
	opt := health.NameOptions{RestrictCharset: body.RestrictCharset, NonCJKNonASCII: body.NonCJK}
	writeJSON(w, 200, toHealthOpResultResp(s.e.RenameHealthFiles(r.Context(), kind, body.Paths, opt, body.OnlyUppercase)))
}

// handleHealthFixEncoding 修复库里所有非 UTF-8 文件名（v3.12.0）。
//
// 请求体只带两个检测选项，空 body 也接受（界面上就是一个按钮）：
// 路径由服务端从索引里自己找，前端传不了——这类路径经 JSON 编码后原始字节
// 已被替换成 U+FFFD，回传的字符串对不上磁盘文件，写入口护栏也会直接拒绝。
func (s *Server) handleHealthFixEncoding(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		RestrictCharset bool `json:"restrict_charset"`
		NonCJK          bool `json:"non_cjk"`
	}
	if r.Body != nil {
		// 解析失败按默认选项继续：这里没有必填字段，空 body 是正常用法。
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if s.e.HealthRunning() {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "健康检测进行中，请等检测结束再修复"})
		return
	}
	if s.scanBusy(w) {
		return
	}
	opt := health.NameOptions{RestrictCharset: body.RestrictCharset, NonCJKNonASCII: body.NonCJK}
	writeJSON(w, 200, toHealthOpResultResp(s.e.FixInvalidEncodingNames(r.Context(), opt)))
}

// handleHealthTrash 把选中文件移入隔离区。
func (s *Server) handleHealthTrash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	if s.scanBusy(w) {
		return
	}
	// 检测进行中移走文件，本轮结论会挂在已不存在的路径上（撤销改名同理）。
	if s.e.HealthRunning() {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "健康检测进行中，请等检测结束再操作"})
		return
	}
	var body healthPathsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体解析失败"})
		return
	}
	if len(body.Paths) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "未选择文件"})
		return
	}
	writeJSON(w, 200, toHealthOpResultResp(s.e.TrashHealthFiles(r.Context(), body.Paths)))
}

// handleHealthUndo 按任务号撤销批量重命名。
func (s *Server) handleHealthUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	// v3.10.8（P1-1）：健康撤销同样会改磁盘与索引，扫描进行中不许执行
	if s.scanBusy(w) {
		return
	}
	if s.e.HealthRunning() {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "健康检测进行中，请等检测结束再操作"})
		return
	}
	var body struct {
		TaskID int64 `json:"task_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TaskID == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "缺少 task_id"})
		return
	}
	res, err := s.e.UndoHealthTask(r.Context(), body.TaskID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, toHealthOpResultResp(res))
}
