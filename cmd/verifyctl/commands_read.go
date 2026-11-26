package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// maxPageSize 与服务端 groupQuery 的上限一致：分页一次最多取 1000 组。
const maxPageSize = 1000

// controlParams 是入口层自用参数，不能作为 query/body 发给服务端。
// 注意 dry_run / yes 故意不在此列：它们是 index repair、health rename/ignore 等
// 接口的真实业务参数，必须原样透传；本地开关改用 __cli_ 前缀的内部 key。
// token 也不在此列：events 与 search selection 确有同名路由参数。
var controlParams = map[string]bool{
	"pretty":        true,
	"confirm_token": true,
	"help-json":     true,
}

// isInternalParam 判断是否为 CLI 内部保留参数（本地 dry-run / yes 开关）。
// 这类 key 既不进 would_do 回包，也不进 HTTP 请求。
func isInternalParam(k string) bool {
	return strings.HasPrefix(k, "__cli_")
}

// isControlParam 汇总所有不应发给服务端的参数：入口层控制参数 + 内部保留 key。
func isControlParam(k string) bool {
	return controlParams[k] || isInternalParam(k)
}

// commandHTTPError 携带 HTTP 状态码与服务端错误原文，供入口层映射语义退出码。
// Status 为 0 表示连接层失败（Do 的网络错误），映射到退出码 4。
type commandHTTPError struct {
	Status int
	Detail *ErrDetail
	Cause  error
}

func (e *commandHTTPError) Error() string {
	if e.Detail != nil && e.Detail.Message != "" {
		return e.Detail.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "命令执行失败"
}

func (e *commandHTTPError) Unwrap() error { return e.Cause }

// httpStatusFromError 从命令错误里取出状态码与错误详情；非类型化错误按连接失败处理。
func httpStatusFromError(err error) (int, *ErrDetail) {
	if err == nil {
		return 0, nil
	}
	var he *commandHTTPError
	if errors.As(err, &he) {
		detail := he.Detail
		if detail == nil {
			detail = &ErrDetail{Message: he.Error()}
		}
		return he.Status, detail
	}
	return 0, &ErrDetail{Message: err.Error()}
}

func init() {
	// 所有 none 命令共用只读执行器：不落盘、不解码响应、只做可选缩进。
	// 其余副作用类保持 Run 为空，由后续任务接入。
	for i := range Commands {
		if Commands[i].SideEffect != "none" || Commands[i].Run != nil {
			continue
		}
		cmd := Commands[i]
		Commands[i].Run = func(ctx context.Context, c any, args map[string]string) (any, error) {
			cli, ok := c.(*Client)
			if !ok || cli == nil {
				return nil, &commandHTTPError{Detail: &ErrDetail{Message: "只读命令需要 HTTP 客户端"}}
			}
			return runReadCommand(ctx, cli, cmd, args)
		}
	}
}

// runReadCommand 执行一条无副作用命令：按注册表组请求，原样返回响应体。
// params 里的控制参数只影响本地输出，不进入 HTTP 请求；--pretty 仅做 json.Indent。
func runReadCommand(ctx context.Context, c *Client, cmd Command, params map[string]string) ([]byte, error) {
	if c == nil {
		return nil, &commandHTTPError{Detail: &ErrDetail{Message: "HTTP 客户端未初始化"}}
	}

	request := make(map[string]string, len(params))
	for k, v := range params {
		if !isControlParam(k) {
			request[k] = v
		}
	}
	clampPageSize(request)

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

// clampPageSize 把 page_size 钳到服务端允许的上限 1000。
// 非法值交给服务端按默认 20 处理，CLI 不擅自改写字符串。
func clampPageSize(params map[string]string) {
	v, ok := params["page_size"]
	if !ok || v == "" {
		return
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= maxPageSize {
		return
	}
	params["page_size"] = strconv.Itoa(maxPageSize)
}

// missingRequiredParams 列出注册表标记为必填但调用方没给的参数。
func missingRequiredParams(cmd Command, params map[string]string) []string {
	var missing []string
	for _, p := range cmd.Params {
		if p.Required && params[p.Name] == "" {
			missing = append(missing, p.Name)
		}
	}
	return missing
}

// runResultBytes 把 Run 的 any 结果收敛为字节流；只读命令返回 []byte，
// 其余分支只服务于后续任务的兼容，不做任何字段解码。
func runResultBytes(v any) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case []byte:
		return x, nil
	case string:
		return []byte(x), nil
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			return nil, fmt.Errorf("序列化命令结果失败（%v）", err)
		}
		return raw, nil
	}
}
