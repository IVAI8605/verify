// v3.9.8 修 F-03：Decode 失败时回报具体字段与期望类型，而不是笼统的"请求格式错误"
package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"verify/internal/engine"
)

// folderRenameBody 请求体。
type folderRenameBody struct {
	Dir     string `json:"dir"`
	PrefixA bool   `json:"prefix_a"`
}

// friendlyDecodeError 把 json.UnmarshalTypeError 转成可读的中文提示。
func friendlyDecodeError(err error) string {
	if terr, ok := err.(*json.UnmarshalTypeError); ok {
		field := terr.Field
		if field == "" {
			field = "body"
		}
		expected := terr.Type.String()
		switch expected {
		case "bool":
			expected = "布尔值（true/false）"
		case "string":
			expected = "字符串"
		case "int", "int64", "uint", "uint64", "float64":
			expected = "数字"
		case "[]string":
			expected = "字符串数组"
		}
		return "字段 '" + field + "' 类型不对，期望 " + expected + "，实际收到 " + terr.Value
	}
	// io.EOF / syntax error / 其它
	msg := err.Error()
	if strings.Contains(msg, "EOF") {
		return "请求体为空或不完整"
	}
	return "请求格式错误：" + msg
}

// handleFolderRenamePreview 预览：POST /api/folder_rename/preview。
func (s *Server) handleFolderRenamePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body folderRenameBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	preview, err := s.e.FolderRenamePreviewAPI(body.Dir, engine.FolderRenameOptions{PrefixA: body.PrefixA})
	if err != nil {
		writeJSON(w, 400, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, toFolderRenamePreviewResp(preview))
}

// handleFolderRename 执行：POST /api/folder_rename。
func (s *Server) handleFolderRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	// v3.10.8（P1-1）：扫描进行中改名，收尾会把旧路径写回 active
	if s.scanBusy(w) {
		return
	}
	var body folderRenameBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	preview, err := s.e.FolderRename(r.Context(), body.Dir, engine.FolderRenameOptions{PrefixA: body.PrefixA})
	if err != nil {
		writeJSON(w, 400, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, toFolderRenamePreviewResp(preview))
}

// handleFolderRenameUndo 撤销：POST /api/folder_rename/undo {task_id}。
func (s *Server) handleFolderRenameUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	// v3.10.8（P1-1）：撤销改名与扫描并发会让索引与磁盘错位
	if s.scanBusy(w) {
		return
	}
	var body struct {
		TaskID int64 `json:"task_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	done, failed, err := s.e.UndoFolderRename(r.Context(), body.TaskID)
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, doneFailedResponse{Done: done, Failed: failed})
}
