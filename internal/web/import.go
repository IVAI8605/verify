// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"verify/internal/applog"
	"verify/internal/export"
	"verify/internal/store"
	"verify/internal/taskguard"
)

// importMaxBody 是上传请求体上限（CSV 128MB + multipart 编码余量）。
const importMaxBody = export.MaxImportBytes + (64 << 10)

// handleImport 接收 CSV 上传，解析后写入独立的导入会话。
// 路径匹配不上不是错误（从 Windows DC 导出的 C:\ 路径在本机全匹配不到是常态），
// 返回值里 matched/unmatched 让界面区分展示。
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, importMaxBody)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		// MaxBytesReader 超限的错误也走到这里，统一给中文提示。
		writeJSON(w, 400, errorResponse{Error: "上传失败：文件超过 128MB 上限或格式不正确"})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, 400, errorResponse{Error: "请求缺少 file 字段（multipart 上传）"})
		return
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, 400, errorResponse{Error: "读取上传内容失败（超过 128MB 上限）"})
		return
	}
	rows, source, warnings, err := export.ParseCSV(data)
	if err != nil {
		writeJSON(w, 400, errorResponse{Error: err.Error()})
		return
	}
	// 反向路径映射（v1.10.3）：DC5 导出的文件路径是外部形态（SMB/盘符），
	// 导入时转回 NAS 路径才能与当前库匹配上；未命中的行保持原样（仅列表）。
	if pm := s.cfg.Get().PathMaps; len(pm) > 0 {
		for i := range rows {
			if nas := pm.ToNas(rows[i].Path); nas != "" {
				rows[i].Path = nas
			}
		}
	}
	name := r.FormValue("name")
	if name == "" {
		name = header.Filename
	}
	// v3.10.10：按字符截断。原先按字节切，中文会话名会被切在 UTF-8 中间，
	// 非法字节入库后界面显示为乱码。
	if rs := []rune(name); len(rs) > 120 {
		name = string(rs[:120])
	}
	sess, err := s.e.ImportSession(r.Context(), name, source, rows)
	if err != nil {
		// v3.25.2（P3-3）：扫描 / 转码占用时属于冲突而不是故障，409 让前端提示稍后再试。
		var ce *taskguard.ConflictError
		if errors.As(err, &ce) {
			writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	applog.Info("导入会话 #%d：来源 %s，%d 行 / %d 组，匹配 %d", sess.ID, source, sess.TotalRows, sess.GroupCount, sess.MatchedRows)
	writeJSON(w, 200, importResultResponse{Session: sess, Total: sess.TotalRows, Groups: sess.GroupCount, Matched: sess.MatchedRows, Unmatched: sess.TotalRows - sess.MatchedRows, Warnings: importWarnings(warnings)})
}

// handleImportSessions 列出（GET）或删除（DELETE，?id=）导入会话。
func (s *Server) handleImportSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sessions, err := s.st.ListImportSessions(r.Context())
		if err != nil {
			writeJSON(w, 500, errorResponse{Error: err.Error()})
			return
		}
		if sessions == nil {
			sessions = []store.ImportSession{}
		}
		writeJSON(w, 200, importSessionsResponse{Sessions: sessions})
	case http.MethodDelete:
		id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if err != nil || id <= 0 {
			writeJSON(w, 400, errorResponse{Error: "无效的会话 ID"})
			return
		}
		if err := s.st.DeleteImportSession(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrImportSessionNotFound) {
				writeJSON(w, 404, errorResponse{Error: "导入会话不存在"})
				return
			}
			writeJSON(w, 500, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, 200, okResponse{OK: true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
	}
}

// handleImportGroups 返回会话详情与按 group_no 聚合的分组。
// 只有 matched=1 的行允许执行隔离/删除；未匹配行仅展示。
func (s *Server) handleImportGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("session_id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 400, errorResponse{Error: "无效的会话 ID"})
		return
	}
	sess, err := s.st.GetImportSession(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrImportSessionNotFound) {
			writeJSON(w, 404, errorResponse{Error: "导入会话不存在"})
			return
		}
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	// v3.10.8（P2-11）：可选按组分页；不传 page/page_size 时返回全部，保持旧行为。
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	groups, total, err := s.st.ImportGroups(r.Context(), id, page, pageSize)
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	if groups == nil {
		groups = []store.ImportGroup{}
	}
	writeJSON(w, 200, importGroupsResponse{Session: sess, Groups: groups, TotalGroups: total})
}

// exportOptions 把导出请求转成 export.Options；protect 复用 v1.9.0 保护文件夹判定。
func (s *Server) exportOptions(marked map[string]bool, onlyMarked bool) export.Options {
	return export.Options{
		Marked:     marked,
		OnlyMarked: onlyMarked,
		ProtectFn:  s.e.IsProtected,
	}
}

// applyPathMapping 为 DC5 格式导出注入 NAS→外部路径映射（v1.10.3，仅 DC5 格式生效）。
func (s *Server) applyPathMapping(opt *export.Options, format string) {
	if format != "dc5" {
		return
	}
	if pm := s.cfg.Get().PathMaps; len(pm) > 0 {
		opt.PathFn = pm.ToExt
	}
}

// importWarnings 汇总给界面的提示条目（上限内截断，防止超长响应）。
func importWarnings(ws []string) []string {
	const max = 50
	if len(ws) > max {
		rest := len(ws) - max
		return append(ws[:max:max], fmt.Sprintf("……另有 %d 条同类提示未展示", rest))
	}
	return ws
}
