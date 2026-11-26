package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client 是 verifyctl 的轻量 HTTP 客户端。
// Token 只进 Authorization: Bearer，绝不写进 URL、日志或错误文案。
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient 构造客户端；baseURL 末尾斜杠会被去掉，避免拼出双斜杠。
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// Do 按 cmd.Method 组装请求：GET/HEAD/DELETE 把参数编码进 query，
// POST/PUT/PATCH 按注册表里的 Param.Type 转成 JSON 字段。
// 返回原始响应体与 HTTP 状态码；网络错误时状态码为 0，由调用方映射到退出码 4。
func (c *Client) Do(ctx context.Context, cmd Command, params map[string]string) ([]byte, int, error) {
	method := cmd.Method
	if method == "" {
		method = http.MethodGet
	}
	target := c.BaseURL + cmd.Path

	var body io.Reader
	if usesQuery(method) {
		q := url.Values{}
		for k, v := range params {
			if v == "" {
				continue
			}
			q.Set(k, v)
		}
		if len(q) > 0 {
			target += "?" + q.Encode()
		}
	} else {
		payload, err := typedPayload(cmd, params)
		if err != nil {
			return nil, 0, fmt.Errorf("编码请求体失败（%v）", err)
		}
		body = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, 0, fmt.Errorf("构造请求失败（%v）", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求 %s 失败（%v）", cmd.Path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("读取 %s 响应失败（%v）", cmd.Path, err)
	}
	return raw, resp.StatusCode, nil
}

// usesQuery 判断参数走 URL 查询串还是 JSON 请求体。
// 服务端 DELETE 处理器统一从 query 读 id，故 DELETE 与 GET 同组。
func usesQuery(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodDelete:
		return true
	default:
		return false
	}
}

// typedPayload 按注册表的 Param.Type 把字符串参数转成对应的 JSON 类型；
// json 类型内联原始 JSON，避免把数组/对象当成字符串发给服务端。
func typedPayload(cmd Command, params map[string]string) ([]byte, error) {
	types := make(map[string]string, len(cmd.Params))
	for _, p := range cmd.Params {
		types[p.Name] = p.Type
	}
	payload := make(map[string]any, len(params))
	for k, v := range params {
		switch types[k] {
		case "bool":
			if b, err := strconv.ParseBool(v); err == nil {
				payload[k] = b
			} else {
				payload[k] = v
			}
		case "int":
			if n, err := strconv.Atoi(v); err == nil {
				payload[k] = n
			} else {
				payload[k] = v
			}
		case "json":
			if json.Valid([]byte(v)) {
				payload[k] = json.RawMessage(v)
			} else {
				payload[k] = v
			}
		default:
			payload[k] = v
		}
	}
	return json.Marshal(payload)
}

// exitCodeForHTTP 把 HTTP 状态码映射到语义退出码：
// 2xx→0，401/403→3（鉴权），4xx→5（请求/冲突），5xx→6（服务端），0→4（连接失败）。
// 409 走通用的 4xx→5，控制器明令不得单独映射为 7。
func exitCodeForHTTP(status int) int {
	switch {
	case status >= 200 && status < 300:
		return 0
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return 3
	case status >= 400 && status < 500:
		return 5
	case status >= 500:
		return 6
	case status <= 0:
		return 4
	default:
		return 5
	}
}
