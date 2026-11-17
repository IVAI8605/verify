// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"verify/internal/engine"
	"verify/internal/taskguard"
)

// handleTranscode 入队并启动（v1.17 引入 17.5.3）：POST /api/transcode {paths, quality, max_res}。
func (s *Server) handleTranscode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Paths   []string `json:"paths"`
		Quality string   `json:"quality"`
		MaxRes  string   `json:"max_res"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	if body.Quality == "" {
		body.Quality = "balanced"
	}
	// v3.10.9：转码带「替换」时会把原文件移入隔离区，与隔离类入口同口径，
	// 扫描进行中执行会让收尾的 UpsertBatch 把旧路径写回 active。
	if s.scanBusy(w) {
		return
	}
	n, err := s.e.TranscodeStart(body.Paths, body.Quality, body.MaxRes)
	if err != nil {
		writeJSON(w, 400, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, transcodeStatusResponse{Queued: n, Encoder: s.e.TranscodeEncoder()})
}

// handleTranscodeStatus 队列快照：GET /api/transcode/status。
func (s *Server) handleTranscodeStatus(w http.ResponseWriter, r *http.Request) {
	tasks, encoder := s.e.TranscodeStatus()
	if tasks == nil {
		tasks = []engine.TranscodeTask{}
	}
	writeJSON(w, 200, transcodeTasksResponse{Tasks: tasks, Encoder: encoder})
}

// handleTranscodeCancel 取消转码：POST /api/transcode/cancel。
// 带 job_id 时只取消该任务（v3.10.8 新增），不带时取消整批。
func (s *Server) handleTranscodeCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		JobID int64 `json:"job_id"`
	}
	// 请求体可省略（整批取消），因此 EOF 视为正常；格式错误仍按字段约定拒绝。
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	if body.JobID > 0 {
		if !s.e.TranscodeCancelJob(body.JobID) {
			writeJSON(w, 400, errorResponse{Error: "任务不存在或已结束"})
			return
		}
		writeJSON(w, 200, jobIDResponse{OK: true, JobID: body.JobID})
		return
	}
	s.e.TranscodeCancel()
	writeJSON(w, 200, okResponse{OK: true})
}

// handleTranscodeResidue GET /api/transcode/residue — 预览超过 24h 的转码/剪除残留（v3.23.0）。
// 只读：不移动、不删除，界面确认后才走 clean。
func (s *Server) handleTranscodeResidue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	files, dirs, err := s.e.FindTranscodeResidue(r.Context(), 24*time.Hour, 5000)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, transcodeResidueResponse{Files: files, ScannedDirs: dirs})
}

// handleTranscodeResidueClean POST /api/transcode/residue/clean — 把残留移入隔离区（v3.23.0）。
// 不传 paths 时服务端重新扫描一遍，避免界面拿到的清单过期。
func (s *Server) handleTranscodeResidueClean(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	if len(body.Paths) == 0 {
		files, _, err := s.e.FindTranscodeResidue(r.Context(), 24*time.Hour, 5000)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		for _, f := range files {
			body.Paths = append(body.Paths, f.Path)
		}
	}
	if len(body.Paths) == 0 {
		writeJSON(w, 200, moveResultResponse{Moved: []string{}, Failed: []string{}})
		return
	}
	if s.scanBusy(w) {
		return
	}
	res, err := s.e.CleanTranscodeResidue(r.Context(), body.Paths)
	if err != nil {
		// v3.25.0（P1-1）：转码/扫描占用时是冲突而不是故障，返回 409 让前端提示稍后再试。
		var ce *taskguard.ConflictError
		if errors.As(err, &ce) {
			writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, res)
}
