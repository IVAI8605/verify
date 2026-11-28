package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// multipartFileParam 是 multipart 上传命令里代表「本地文件路径」的参数名。
// 约定：Command.Multipart 为真时，该参数的值按本地路径读取为文件部件，其余参数作为表单字段。
const multipartFileParam = "file"

// Client 是 verifyctl 的轻量 HTTP 客户端。
// Token 只进 Authorization: Bearer，Password 只进 Authorization: Basic，
// 两者都不写进 URL、日志或错误文案。
type Client struct {
	BaseURL  string
	Token    string
	Password string
	HTTP     *http.Client
}

// NewClient 构造客户端；baseURL 末尾斜杠会被去掉，避免拼出双斜杠。
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// Do 按 cmd.Method 与命令的传输声明组装请求：
//   - 路径参数（cmd.PathParams）按声明顺序拼到 Path 末尾并从其余两路剔除；
//   - GET/HEAD/DELETE 默认把参数编码进 query；POST/PUT/PATCH 默认进 JSON body；
//   - cmd.QueryParams 里的参数即使非 GET 方法也强制走 query（如 video_tags recompute?id=）；
//   - cmd.Multipart 为真时以 multipart/form-data 上传：multipartFileParam 按本地路径读文件，其余为表单字段。
//
// 内部保留 key（__cli_*）与入口层控制参数在 query 与 body 两路都剔除，业务 dry_run 等真实参数不受影响。
// 返回原始响应体与 HTTP 状态码；网络错误时状态码为 0，由调用方映射到退出码 4。
func (c *Client) Do(ctx context.Context, cmd Command, params map[string]string) ([]byte, int, error) {
	request := make(map[string]string, len(params))
	for k, v := range params {
		if isControlParam(k) {
			continue
		}
		request[k] = v
	}

	method := cmd.Method
	if method == "" {
		method = http.MethodGet
	}

	// 路径参数：拼到 Path 末尾并移出 query/body 两路。
	target := c.BaseURL + cmd.Path
	for _, name := range cmd.PathParams {
		if v, ok := request[name]; ok && v != "" {
			target += v
			delete(request, name)
		}
	}

	// 非 GET 方法也需走查询串的参数集合。
	queryKeys := make(map[string]bool, len(cmd.QueryParams))
	for _, name := range cmd.QueryParams {
		queryKeys[name] = true
	}

	// 拆分到 query / body / file 三路。
	q := url.Values{}
	bodyParams := make(map[string]string, len(request))
	var filePath string
	for k, v := range request {
		switch {
		case usesQuery(method) || queryKeys[k]:
			if v != "" {
				q.Set(k, v)
			}
		case cmd.Multipart && k == multipartFileParam:
			filePath = v
		default:
			bodyParams[k] = v
		}
	}
	if len(q) > 0 {
		if strings.Contains(target, "?") {
			target += "&" + q.Encode()
		} else {
			target += "?" + q.Encode()
		}
	}

	var body io.Reader
	contentType := ""
	if cmd.Multipart {
		buf := &bytes.Buffer{}
		mw := multipart.NewWriter(buf)
		for k, v := range bodyParams {
			if err := mw.WriteField(k, v); err != nil {
				return nil, 0, fmt.Errorf("编码表单字段失败（%v）", err)
			}
		}
		if filePath != "" {
			f, err := os.Open(filePath)
			if err != nil {
				// 本地上传文件打不开是用法/输入错误（用户给的路径不对），
				// 不是连接层失败：返回 usageError 让入口映射退出码 2 而非 4（#6）。
				return nil, 0, &usageError{msg: "打开上传文件失败：" + err.Error()}
			}
			part, perr := mw.CreateFormFile(multipartFileParam, filepath.Base(filePath))
			if perr != nil {
				f.Close()
				return nil, 0, fmt.Errorf("构造上传字段失败（%v）", perr)
			}
			if _, cerr := io.Copy(part, f); cerr != nil {
				f.Close()
				return nil, 0, fmt.Errorf("读取上传文件失败（%v）", cerr)
			}
			f.Close()
		}
		if err := mw.Close(); err != nil {
			return nil, 0, fmt.Errorf("收尾上传请求失败（%v）", err)
		}
		body = buf
		contentType = mw.FormDataContentType()
	} else if !usesQuery(method) {
		payload, err := typedPayload(cmd, bodyParams)
		if err != nil {
			return nil, 0, fmt.Errorf("编码请求体失败（%v）", err)
		}
		body = bytes.NewReader(payload)
		contentType = "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, 0, fmt.Errorf("构造请求失败（%v）", err)
	}
	if c.Password != "" {
		req.SetBasicAuth("", c.Password)
	} else if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	// /api/auth/token 强制要求同源页面标记（SEC-3），CLI 与前端一致地带上；
	// 只针对该路由设置，避免给其它接口引入额外语义。
	if cmd.Path == "/api/auth/token" {
		req.Header.Set("X-Requested-With", "verify")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
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

// effectivePath 返回代入了路径参数后的实际请求路径，供 dry-run 的 would_do 展示。
func effectivePath(cmd Command, params map[string]string) string {
	p := cmd.Path
	for _, name := range cmd.PathParams {
		if v := params[name]; v != "" {
			p += v
		}
	}
	return p
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
		if isInternalParam(k) {
			continue
		}
		switch types[k] {
		case "bool":
			if v == "" { // 显式空串按「未提供」处理，避免 bool 解成空串
				continue
			}
			if b, err := strconv.ParseBool(v); err == nil {
				payload[k] = b
			} else {
				payload[k] = v
			}
		case "int":
			if v == "" {
				continue
			}
			if n, err := strconv.Atoi(v); err == nil {
				payload[k] = n
			} else {
				payload[k] = v
			}
		case "float":
			if v == "" {
				continue
			}
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				payload[k] = f
			} else {
				payload[k] = v
			}
		case "json":
			if v == "" {
				continue
			}
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
