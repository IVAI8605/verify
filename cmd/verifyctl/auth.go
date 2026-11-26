package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// resolveToken 按优先级读取访问令牌：命令行 --token > 环境变量 VERIFY_TOKEN >
// 数据目录 config.json 里的 access_token。
// 刻意不读取 access-token.txt 明文提示文件；配置不存在或没有该字段时返回空串，不当作错误。
func resolveToken(flagToken string, dataDir string) (string, error) {
	if flagToken != "" {
		return flagToken, nil
	}
	if envToken := os.Getenv("VERIFY_TOKEN"); envToken != "" {
		return envToken, nil
	}
	if dataDir == "" {
		return "", nil
	}

	path := filepath.Join(dataDir, "config.json")
	raw, err := os.ReadFile(path) // #nosec G304 -- 文件名固定，目录来自命令行参数而非请求
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("读取配置失败（%v）", err)
	}
	var cfg struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("解析配置失败（%v）", err)
	}
	return cfg.AccessToken, nil
}

// AuthToken 通过 HTTP 取服务端的当前访问令牌。
// 服务端要求 X-Requested-With: verify 标记，否则拒绝；令牌只作为返回值，
// 不进查询串、日志或错误文案。
func (c *Client) AuthToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/auth/token", nil)
	if err != nil {
		return "", fmt.Errorf("构造令牌请求失败（%v）", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("X-Requested-With", "verify")

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取令牌失败（%v）", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取令牌响应失败（%v）", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("获取令牌失败：HTTP %d，%s", resp.StatusCode, parseErrorDetail(raw).Message)
	}

	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("解析令牌响应失败（%v）", err)
	}
	return payload.Token, nil
}
