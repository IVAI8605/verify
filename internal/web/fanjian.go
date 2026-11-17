package web

import (
	"encoding/json"
	"net/http"
)

// handleFanjian 文件名繁简转换（v1.22）：POST /api/fanjian {paths, to_traditional}。
func (s *Server) handleFanjian(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Paths         []string `json:"paths"`
		ToTraditional bool     `json:"to_traditional"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	if len(body.Paths) == 0 {
		writeJSON(w, 400, errorResponse{Error: "缺少文件列表"})
		return
	}
	// v3.10.0 UI-03：与健康页改名同理，检测中改名会让本轮结论挂在旧路径上。
	if s.e.HealthRunning() {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "健康检测进行中，请等检测结束再改名"})
		return
	}
	if s.scanBusy(w) {
		return
	}
	writeJSON(w, 200, s.e.ConvertNames(r.Context(), body.Paths, body.ToTraditional))
}

// handleFanjianUndo 撤销：POST /api/fanjian/undo {task_id}。
func (s *Server) handleFanjianUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	// v3.10.8（P1-1）：撤销繁简改名与扫描并发会让索引与磁盘错位
	if s.scanBusy(w) {
		return
	}
	var body struct {
		TaskID int64 `json:"task_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TaskID <= 0 {
		writeJSON(w, 400, errorResponse{Error: "缺少任务号"})
		return
	}
	done, failed, failures, err := s.e.UndoFanjian(r.Context(), body.TaskID)
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, doneFailedItemsResponse{Done: done, Failed: failed, Items: failures})
}
