// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件是 v3.3.0 的可选 AI 连接器（设计文档 §4.2 方案 B / V3.3-2）。
//
// 定位非常窄：只把文件名与媒体元数据发给用户配置的 OpenAI 兼容端点
// （本地 Ollama 一般填 http://127.0.0.1:11434），生成一句「为什么保留这个」。
// 它不参与 RetainSuggest 的保留判定，不会自动调用，也不会发送文件内容。
// 端点由管理员在设置页显式填写；请求失败只返回错误，页面自动退回规则理由。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"verify/internal/config"
	"verify/internal/engine"
)

type aiExplainGroup struct {
	Key   string              `json:"key"`
	Files []engine.RetainFile `json:"files"`
}

type aiExplainReason struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// handleRetainExplain 为当前页分组生成 AI 解释：POST /api/retain/explain。
func (s *Server) handleRetainExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	cfg := s.cfg.Get()
	if !cfg.AIEnabled || strings.TrimSpace(cfg.AIEndpoint) == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "未启用 AI 连接器，请到设置页填写本地模型地址"})
		return
	}
	var body struct {
		Groups []aiExplainGroup `json:"groups"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	if len(body.Groups) == 0 || len(body.Groups) > 50 {
		writeJSON(w, 400, errorResponse{Error: "分组数量应为 1–50"})
		return
	}
	reasons := make([]aiExplainReason, 0, len(body.Groups))
	failed := 0
	for _, g := range body.Groups {
		if len(g.Files) < 2 {
			continue
		}
		sug := engine.RetainSuggest(g.Files)
		reason, err := s.aiChat(r.Context(), cfg, aiPrompt(g.Files, sug.Retain))
		if err != nil {
			failed++
			continue
		}
		reasons = append(reasons, aiExplainReason{Key: g.Key, Reason: reason})
	}
	status := 200
	if len(reasons) == 0 {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, failedReasonsResponse{Reasons: reasons, Failed: failed})
}

// aiPrompt 构造紧凑提示词：只含文件名与已提取的媒体元数据。
func aiPrompt(files []engine.RetainFile, keep string) string {
	var b strings.Builder
	b.WriteString("你是文件清理助手。下面是一组重复文件，规则建议保留：")
	b.WriteString(keep)
	b.WriteString("。请用一句不超过 40 字的中文说明为什么保留它，只输出理由，不要解释。\n文件：\n")
	for _, f := range files {
		fmt.Fprintf(&b, "- %s（%d 字节", f.Name, f.Size)
		if f.Width > 0 && f.Height > 0 {
			fmt.Fprintf(&b, "，%dx%d", f.Width, f.Height)
		}
		if f.Duration > 0 {
			fmt.Fprintf(&b, "，%.0f 秒", f.Duration)
		}
		if f.Broken {
			b.WriteString("，已损坏")
		}
		b.WriteString("）\n")
	}
	return b.String()
}

// aiChat 调用 OpenAI 兼容的 /v1/chat/completions。
// 端点来自管理员配置，属于可信输入；仍限制 20 秒超时并只读取 1MB 响应。
func (s *Server) aiChat(ctx context.Context, cfg *config.Config, prompt string) (string, error) {
	payload := map[string]any{
		"model": cfg.AIModel,
		"messages": []map[string]string{
			{"role": "system", "content": "你只输出一句简短中文理由，不输出其他内容。"},
			{"role": "user", "content": prompt},
		},
		"temperature": 0.2,
		"stream":      false,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	url := strings.TrimRight(cfg.AIEndpoint, "/") + "/v1/chat/completions"
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req) // #nosec G107 -- 端点由管理员在设置页显式配置
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("AI 接口返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("AI 响应解析失败：%w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("AI 未返回内容")
	}
	reason := strings.TrimSpace(out.Choices[0].Message.Content)
	if reason == "" {
		return "", fmt.Errorf("AI 返回空理由")
	}
	if len([]rune(reason)) > 120 {
		reason = string([]rune(reason)[:120])
	}
	return reason, nil
}
