package main

import (
	"encoding/json"
	"io"
)

// Envelope 是 CLI 对外的统一 JSON 外壳。
// 服务端 okResponse 自带 ok 键、数组响应没有顶层位置，所以成功响应统一塞进 data。
type Envelope struct {
	OK      bool            `json:"ok"`
	Command string          `json:"command"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   *ErrDetail      `json:"error,omitempty"`
}

// ErrDetail 是错误信封里的错误详情；Message 保留服务端中文原文。
type ErrDetail struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// writeEnvelope 输出命令结果：rawMode 时原样写 raw，否则包一层带 data 的信封。
func writeEnvelope(w io.Writer, command string, raw []byte, rawMode bool) error {
	if rawMode {
		_, err := w.Write(raw)
		return err
	}
	data := json.RawMessage(raw)
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	env := Envelope{OK: true, Command: command, Data: data}
	return json.NewEncoder(w).Encode(env)
}

// writeError 输出失败信封；message 直接用服务端中文原文，不解析中文、不改写冲突文案。
func writeError(w io.Writer, command string, e *ErrDetail) error {
	if e == nil {
		e = &ErrDetail{Message: "未知错误"}
	}
	env := Envelope{OK: false, Command: command, Error: e}
	return json.NewEncoder(w).Encode(env)
}

// parseErrorDetail 从服务端失败响应体里取出 error 字段原文。
// 409 的中文文案（扫描进行中 / 搜索未就绪 / 全选冲突等）一律原样保留，
// 不靠匹配中文推断冲突类型，也不在此决定退出码。
func parseErrorDetail(raw []byte) *ErrDetail {
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return &ErrDetail{Message: "服务端返回了无法解析的错误响应"}
	}
	if resp.Error == "" {
		return &ErrDetail{Message: "服务端未给出错误说明"}
	}
	return &ErrDetail{Message: resp.Error}
}
