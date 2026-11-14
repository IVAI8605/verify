// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package oplog 定义操作审计记录的通用结构与纯逻辑，不含任何 SQL。
// 数据库读写在 internal/store/oplog.go；本包只负责结构、上下文传递、明细截断与脱敏辅助。
package oplog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	ResultOK      = "ok"
	ResultPartial = "partial"
	ResultFail    = "fail"

	SourceUI     = "ui"
	SourceTimer  = "timer"
	SourceSystem = "system"

	// DefaultDetailLimit 是单次操作最多保留的明细条数，超出只记总数与前 N 条。
	DefaultDetailLimit = 200
	// MaxDetailBytes 是单条 detail_json 的硬上限，防止一次删上万文件塞爆数据库。
	MaxDetailBytes = 64 << 10
)

// Detail 描述一条操作明细：一个文件、一次改名或一个字段变更。
// 调用方只填用得上的字段；敏感值（令牌、Cookie、请求体）一律不填。
type Detail struct {
	Path    string `json:"path,omitempty"`
	NewPath string `json:"new_path,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Hash    string `json:"hash,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Field   string `json:"field,omitempty"`
	Old     string `json:"old,omitempty"`
	New     string `json:"new,omitempty"`
}

// Entry 是一次操作的审计记录。权威记录存 SQLite，文本日志只作辅助。
type Entry struct {
	TS          int64    `json:"ts"`
	ReqID       string   `json:"req_id,omitempty"`
	Actor       string   `json:"actor,omitempty"`
	Source      string   `json:"source,omitempty"`
	Action      string   `json:"action"`
	Result      string   `json:"result"`
	Summary     string   `json:"summary,omitempty"`
	TargetCount int      `json:"target_count"`
	Details     []Detail `json:"-"` // 由 EncodeDetails 转成 detail_json，不直接入库
	Error       string   `json:"error,omitempty"`
	DurationMS  int64    `json:"duration_ms,omitempty"`
}

// New 创建一条默认成功、来源为界面的操作记录。
func New(action string) Entry {
	return Entry{
		TS:     time.Now().UnixMilli(),
		Source: SourceUI,
		Action: action,
		Result: ResultOK,
	}
}

// FromContext 补齐请求 ID 与来源 IP；上下文里没有时保持调用方已填的值。
func (e *Entry) FromContext(ctx context.Context) {
	if ctx == nil {
		return
	}
	if e.ReqID == "" {
		e.ReqID = ReqID(ctx)
	}
	if e.Actor == "" {
		e.Actor = Actor(ctx)
	}
}

// SetFailure 把失败原因收敛进记录；全量失败由调用方显式改成 ResultFail。
func (e *Entry) SetFailure(errs []string) {
	if len(errs) == 0 {
		return
	}
	if e.Result == ResultOK {
		e.Result = ResultPartial
	}
	e.Error = JoinErrors(errs)
}

// JoinErrors 去重后最多保留 3 条失败原因，避免把上万条错误塞进一行。
func JoinErrors(errs []string) string {
	seen := map[string]bool{}
	out := make([]string, 0, 3)
	for _, s := range errs {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) == 3 {
			break
		}
	}
	return strings.Join(out, "；")
}

type detailPayload struct {
	Items     []Detail `json:"items"`
	Total     int      `json:"total"`
	Truncated bool     `json:"truncated"`
}

// EncodeDetails 把明细序列化成 detail_json，按条数与字节数双重截断。
// 返回空串表示没有明细。
func EncodeDetails(items []Detail, limit int) string {
	if len(items) == 0 {
		return ""
	}
	if limit <= 0 {
		limit = DefaultDetailLimit
	}
	total := len(items)
	kept := items
	truncated := false
	if len(kept) > limit {
		kept = kept[:limit]
		truncated = true
	}
	payload := detailPayload{Items: kept, Total: total, Truncated: truncated}
	b, err := json.Marshal(payload)
	if err != nil { // Detail 全是可序列化标量，理论上不会走到
		return ""
	}
	for len(b) > MaxDetailBytes && len(kept) > 0 {
		kept = kept[:len(kept)/2]
		payload.Items = kept
		payload.Truncated = true
		if b, err = json.Marshal(payload); err != nil {
			return ""
		}
	}
	return string(b)
}

type ctxKey int

const (
	keyReqID ctxKey = iota
	keyActor
)

// WithReqID 把请求 ID 放进上下文，供业务层把明细并到同一条记录。
func WithReqID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyReqID, id)
}

// ReqID 取出上下文里的请求 ID。
func ReqID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(keyReqID).(string)
	return v
}

// WithActor 记录来源 IP。应用可能无鉴权运行，IP 只能当「来源」不能当「人」。
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, keyActor, actor)
}

// Actor 取出来源 IP。
func Actor(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(keyActor).(string)
	return v
}

// NewReqID 生成 16 位十六进制请求 ID。
func NewReqID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// ClientIP 从 RemoteAddr 取来源 IP，取不到时原样返回。
func ClientIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil && host != "" {
		return host
	}
	return remoteAddr
}

// SafePath 去掉 URL 查询串：SSE 等接口会在 query 里带访问令牌，绝不能进操作记录。
func SafePath(rawURL string) string {
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		return rawURL[:i]
	}
	return rawURL
}
