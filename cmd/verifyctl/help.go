package main

import (
	"encoding/json"
	"io"
)

// commandInfo 是 --help-json 对外输出用的数据传输对象。
// 它排除 Run 函数指针（函数无法序列化），并补上人可读的 description 字段。
type commandInfo struct {
	Name            string  `json:"name"`
	Description     string  `json:"description"`
	Path            string  `json:"path"`
	Method          string  `json:"method"`
	Params          []Param `json:"params"`
	SideEffect      string  `json:"side_effect"`
	ConfirmRequired bool    `json:"confirm_required"`
	// 以下为传输声明，供调用方/AI 自描述：哪些参数走 URL 路径段、哪些非 GET 也走查询串、是否 multipart 上传。
	PathParams  []string `json:"path_params,omitempty"`
	QueryParams []string `json:"query_params,omitempty"`
	Multipart   bool     `json:"multipart,omitempty"`
}

// newCommandInfo 由注册项生成自描述条目；description 暂用方法+接口兜底，
// 待后续任务补独立文案时只需改这里。
func newCommandInfo(cmd Command) commandInfo {
	return commandInfo{
		Name:            cmd.Name,
		Description:     cmd.Method + " " + cmd.Path,
		Path:            cmd.Path,
		Method:          cmd.Method,
		Params:          cmd.Params,
		SideEffect:      cmd.SideEffect,
		ConfirmRequired: cmd.Confirm,
		PathParams:      cmd.PathParams,
		QueryParams:     cmd.QueryParams,
		Multipart:       cmd.Multipart,
	}
}

// writeCommandList 输出全量命令清单 JSON，供脚本在 --help-json 下自描述。
// 只输出注册项的元数据，不序列化 Run 函数指针，也不回显令牌。
func writeCommandList(w io.Writer) error {
	list := make([]commandInfo, 0, len(Commands))
	for _, cmd := range Commands {
		list = append(list, newCommandInfo(cmd))
	}
	return json.NewEncoder(w).Encode(list)
}

// writeCommandDetail 输出单条命令的 JSON 自描述，字段与命令清单一致。
func writeCommandDetail(w io.Writer, cmd Command) error {
	return json.NewEncoder(w).Encode(newCommandInfo(cmd))
}
