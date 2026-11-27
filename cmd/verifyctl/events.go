package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// runEventStream 处理 /api/events 的 SSE 流：逐行读取并实时写到 out（stdout），
// Ctrl+C / SIGTERM 停止。这是唯一不走「读全响应再包信封」的命令——SSE 是无限心跳流，
// io.ReadAll 会一直阻塞到客户端超时再失败，既拿不到事件又白等。
// 返回语义退出码：0 正常或被用户中断，1 本地输出失败，3 鉴权，4 连接/读流失败，5/6 服务端错误。
func runEventStream(out io.Writer, baseURL, token string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	url := strings.TrimRight(baseURL, "/") + "/api/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		_ = writeError(out, "events", &ErrDetail{Message: "构造事件流请求失败（" + err.Error() + "）"})
		return 1
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// 用户主动 Ctrl+C 导致的取消按正常结束处理，不当作错误。
		if errors.Is(err, context.Canceled) {
			return 0
		}
		_ = writeError(out, "events", &ErrDetail{Message: "连接事件流失败（" + err.Error() + "）"})
		return 4
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		status, detail := httpStatusFromError(&commandHTTPError{Status: resp.StatusCode, Detail: parseErrorDetail(raw)})
		_ = writeError(out, "events", detail)
		return exitCodeForHTTP(status)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if _, err := io.WriteString(out, sc.Text()+"\n"); err != nil {
			return 1
		}
		// 若 out 带缓冲（如 bufio.Writer），要求其逐行刷新，保证事件实时可见。
		if f, ok := out.(interface{ Flush() error }); ok {
			_ = f.Flush()
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		_ = writeError(out, "events", &ErrDetail{Message: "读取事件流中断（" + err.Error() + "）"})
		return 4
	}
	return 0
}
