package main

import "context"

func init() {
	// 所有 write 命令接入统一的「默认 dry-run、--yes 才执行」执行器。
	// destructive 由 Task 9 的 commands_danger.go 单独接入「--yes + --confirm-token」双门槛。
	for i := range Commands {
		if Commands[i].SideEffect != "write" || Commands[i].Run != nil {
			continue
		}
		cmd := Commands[i]
		Commands[i].Run = func(ctx context.Context, c any, args map[string]string) (any, error) {
			cli, ok := c.(*Client)
			if !ok || cli == nil {
				return nil, &commandHTTPError{Detail: &ErrDetail{Message: "写命令需要 HTTP 客户端"}}
			}
			return runWriteCommand(ctx, cli, cmd, args)
		}
	}
}

// runWriteCommand 执行一条写命令：默认（未带 --yes）或显式 --dry-run 只回 would_do，
// 不发任何请求；带 --yes 才调用 c.Do。成功时原样透传响应体。
// 与 trigger 命令共用 runGuardedCommand（定义于 commands_trigger.go），保证两处确认协议不漂移。
func runWriteCommand(ctx context.Context, c *Client, cmd Command, params map[string]string) ([]byte, error) {
	return runGuardedCommand(ctx, c, cmd, params)
}
