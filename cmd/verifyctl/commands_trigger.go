package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
	// write 命令由 commands_write.go 接入同类执行器；destructive 由 Task 9 接入双门槛。
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
	return runGuardedCommand(ctx, c, cmd, params)
}

// runGuardedCommand 是 trigger/write 共用的低风险执行器：dry-run 默认开启，--yes 才真正发请求。
// 控制参数（入口层参数与 __cli_ 内部 key）只影响本地行为，既不进 would_do.params，也不发给服务端；
// 业务 dry_run 等真实参数原样透传。放在本文件是为了让 transport 层可独立编译（write 接线在 Task 8）。
func runGuardedCommand(ctx context.Context, c *Client, cmd Command, params map[string]string) ([]byte, error) {
	if c == nil {
		return nil, &commandHTTPError{Detail: &ErrDetail{Message: "HTTP 客户端未初始化"}}
	}

	request := make(map[string]string, len(params))
	for k, v := range params {
		if !isControlParam(k) {
			request[k] = v
		}
	}

	method := cmd.Method
	if method == "" {
		method = http.MethodGet
	}
	// 本地干跑开关走 __cli_ 内部 key；业务 dry_run 原样透传给服务端。
	if params["__cli_dry_run"] == "true" || params["__cli_yes"] != "true" {
		// would_do 展示实际会请求的路径（已代入路径参数），便于核对。
		return marshalDryRun(cmd.Name, method, effectivePath(cmd, request), request, params["pretty"] == "true")
	}

	return executeCommand(ctx, c, cmd, request, params["pretty"] == "true")
}

// executeCommand 发送真实请求并统一处理网络错误、非 2xx 状态与 --pretty 缩进。
// trigger / write / destructive 三类执行器共用，保证确认协议之外的发送路径不漂移；
// 内部保留 key 与入口层控制参数已在 request 组装阶段剔除，这里不再重复过滤。
func executeCommand(ctx context.Context, c *Client, cmd Command, request map[string]string, pretty bool) ([]byte, error) {
	raw, status, err := c.Do(ctx, cmd, request)
	if err != nil {
		return nil, &commandHTTPError{Status: status, Cause: err, Detail: &ErrDetail{Message: err.Error()}}
	}
	if status < 200 || status >= 300 {
		return nil, &commandHTTPError{Status: status, Detail: parseErrorDetail(raw)}
	}
	if pretty && json.Valid(raw) {
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
// 内部保留 key（__cli_*）绝不进回包；业务 dry_run 照常显示，便于核对预演动作。
func marshalDryRun(name, method, path string, request map[string]string, pretty bool) ([]byte, error) {
	clean := make(map[string]string, len(request))
	for k, v := range request {
		if isInternalParam(k) {
			continue
		}
		clean[k] = v
	}
	out, err := json.Marshal(triggerDryRunResult{
		DryRun: true,
		WouldDo: wouldDo{
			Command: name,
			Method:  method,
			Path:    path,
			// 展示层对敏感参数脱敏；不影响真实发送（发送走 request，非 clean）。
			Params: redactSensitiveParams(clean),
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

// sensitiveParamName 判断参数名是否属于密钥类字段（令牌/口令等）：
// 展示 dry-run 时必须脱敏，防止 `settings --access_token xxx` 把明文令牌打进输出（I4）。
func sensitiveParamName(name string) bool {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
		strings.Contains(lower, "password") || lower == "authorization" {
		return true
	}
	return false
}

// redactSensitiveParams 返回脱敏后的副本：敏感参数值替换为 <redacted>。
// 只用于 dry-run 的 would_do 展示层，绝不参与真实请求组装或确认令牌计算。
func redactSensitiveParams(params map[string]string) map[string]string {
	if len(params) == 0 {
		return params
	}
	out := make(map[string]string, len(params))
	for k, v := range params {
		if sensitiveParamName(k) {
			out[k] = "<redacted>"
			continue
		}
		out[k] = v
	}
	return out
}
