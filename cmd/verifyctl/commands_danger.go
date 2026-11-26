package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

// dangerDryRunResult 是危险操作的 dry-run 回包：would_do 原样列出将发出的命令信息，
// confirm_token 是执行时必须原样回填的短时效确认令牌（仅本地确认协议，回包不含任何访问令牌）。
type dangerDryRunResult struct {
	DryRun       bool    `json:"dry_run"`
	WouldDo      wouldDo `json:"would_do"`
	ConfirmToken string  `json:"confirm_token"`
}

// usageError 表示本地用法错误（危险操作缺 --yes / --confirm-token，或令牌不匹配/过期），
// 入口层映射退出码 2，且绝不发 HTTP 请求。
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func init() {
	// 所有 destructive 命令接入「dry-run 签发确认令牌 + --yes 与 --confirm-token 双门槛」执行器。
	for i := range Commands {
		if Commands[i].SideEffect != "destructive" || Commands[i].Run != nil {
			continue
		}
		cmd := Commands[i]
		Commands[i].Run = func(ctx context.Context, c any, args map[string]string) (any, error) {
			cli, ok := c.(*Client)
			if !ok || cli == nil {
				return nil, &commandHTTPError{Detail: &ErrDetail{Message: "危险命令需要 HTTP 客户端"}}
			}
			return runDangerCommand(ctx, cli, cmd, args, args["confirm_token"])
		}
	}
}

// runDangerCommand 执行一条危险命令，在 runGuardedCommand 的 dry-run/--yes 逻辑之上叠加确认令牌门槛：
//   - 默认（未带 --yes）或显式 --dry-run：只回 would_do + confirm_token，不发任何请求；
//   - 已带 --yes 但缺 --confirm-token：本地用法错误（退出码 2），不发请求；
//   - --confirm-token 与 dry-run 签发的不一致或已过期：本地用法错误（退出码 2），不发请求；
//   - 双门槛都过：才调用 c.Do 真正执行，成功原样透传响应体。
func runDangerCommand(ctx context.Context, c *Client, cmd Command, params map[string]string, confirmToken string) ([]byte, error) {
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

	// 默认 dry-run（含显式 --dry-run）：回 would_do 并附带本次可用的确认令牌。
	if params["__cli_dry_run"] == "true" || params["__cli_yes"] != "true" {
		return marshalDangerDryRun(cmd.Name, method, effectivePath(cmd, request), request, params["pretty"] == "true")
	}

	// 到这里说明用户确实要执行（--yes），必须再拿确认令牌核对。
	if confirmToken == "" {
		return nil, &usageError{msg: "危险操作已带 --yes 但缺少 --confirm-token：先不带 --yes 跑一次取得 confirm_token，再原样回填"}
	}
	want := makeConfirmToken(cmd, params)
	if !verifyConfirmToken(confirmToken, want) {
		// 上一时间片内签发的令牌仍有效，避免恰好跨片时让用户重跑。
		prev := makeConfirmTokenAt(cmd, params, confirmNow().Unix()/confirmTokenBucketSeconds-1)
		if !verifyConfirmToken(confirmToken, prev) {
			return nil, &usageError{msg: "confirm_token 不匹配或已过期：请重新执行 dry-run 获取新令牌后原样回填"}
		}
	}

	return executeCommand(ctx, c, cmd, request, params["pretty"] == "true")
}

// marshalDangerDryRun 把将执行的命令信息与确认令牌编码成回包；控制/内部参数不进回包。
func marshalDangerDryRun(name, method, path string, request map[string]string, pretty bool) ([]byte, error) {
	clean := make(map[string]string, len(request))
	for k, v := range request {
		if isInternalParam(k) {
			continue
		}
		clean[k] = v
	}
	out, err := json.Marshal(dangerDryRunResult{
		DryRun: true,
		WouldDo: wouldDo{
			Command: name,
			Method:  method,
			Path:    path,
			Params:  clean,
		},
		// 令牌只基于命令身份 + 业务参数 + 时间片，与执行时校验口径一致。
		ConfirmToken: makeConfirmToken(Command{Name: name, Path: path, Method: method}, clean),
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
