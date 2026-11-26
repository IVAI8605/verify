package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

// wouldDo 描述一次 dry-run 将会执行的动作，只含本地元信息，绝不回显令牌。
type wouldDo struct {
	Command string            `json:"command"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Params  map[string]string `json:"params"`
}

// triggerDryRunResult 是 dry-run 回包：would_do 原样列出将发出的命令信息。
type triggerDryRunResult struct {
	DryRun  bool    `json:"dry_run"`
	WouldDo wouldDo `json:"would_do"`
}

func init() {
	// 所有 trigger 命令共用低风险执行器：默认 dry-run，只有 --yes 才真正发请求。
	// write/destructive 保持 Run 为空，由后续任务接入。
	for i := range Commands {
		if Commands[i].SideEffect != "trigger" || Commands[i].Run != nil {
			continue
		}
		cmd := Commands[i]
		Commands[i].Run = func(ctx context.Context, c any, args map[string]string) (any, error) {
			cli, ok := c.(*Client)
			if !ok || cli == nil {
				return nil, &commandHTTPError{Detail: &ErrDetail{Message: "触发命令需要 HTTP 客户端"}}
			}
			return runTriggerCommand(ctx, cli, cmd, args)
		}
	}
}

// runTriggerCommand 执行一条低风险触发命令。
// 默认（未带 --yes）或显式 --dry-run 只回 would_do，不发任何请求；
// 带 --yes 才调用 c.Do；成功时原样透传响应体（触发类只返回任务号，CLI 不轮询）。
func runTriggerCommand(ctx context.Context, c *Client, cmd Command, params map[string]string) ([]byte, error) {
	if c == nil {
		return nil, &commandHTTPError{Detail: &ErrDetail{Message: "HTTP 客户端未初始化"}}
	}

	// 控制参数只影响本地行为，既不进 would_do.params，也不发给服务端。
	request := make(map[string]string, len(params))
	for k, v := range params {
		if !controlParams[k] {
			request[k] = v
		}
	}

	method := cmd.Method
	if method == "" {
		method = http.MethodGet
	}
	if params["dry_run"] == "true" || params["yes"] != "true" {
		return marshalDryRun(cmd.Name, method, cmd.Path, request, params["pretty"] == "true")
	}

	raw, status, err := c.Do(ctx, cmd, request)
	if err != nil {
		return nil, &commandHTTPError{Status: status, Cause: err, Detail: &ErrDetail{Message: err.Error()}}
	}
	if status < 200 || status >= 300 {
		return nil, &commandHTTPError{Status: status, Detail: parseErrorDetail(raw)}
	}
	if params["pretty"] == "true" && json.Valid(raw) {
		var buf bytes.Buffer
		if err := json.Indent(&buf, raw, "", "  "); err != nil {
			// 已通过 json.Valid，理论上不会走到；仍原样透传，绝不改写字段。
			return raw, nil
		}
		return buf.Bytes(), nil
	}
	return raw, nil
}

// marshalDryRun 把将执行的命令信息编码成 would_do 回包。
func marshalDryRun(name, method, path string, request map[string]string, pretty bool) ([]byte, error) {
	if request == nil {
		request = map[string]string{}
	}
	out, err := json.Marshal(triggerDryRunResult{
		DryRun: true,
		WouldDo: wouldDo{
			Command: name,
			Method:  method,
			Path:    path,
			Params:  request,
		},
	})
	if err != nil {
		return nil, &commandHTTPError{Detail: &ErrDetail{Message: "序列化 dry-run 结果失败"}}
	}
	if pretty {
		var buf bytes.Buffer
		if err := json.Indent(&buf, out, "", "  "); err == nil {
			return buf.Bytes(), nil
		}
	}
	return out, nil
}
