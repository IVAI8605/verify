package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"time"
)

// confirmTokenBucketSeconds 是确认令牌的有效时间片（秒）。
// 令牌只在「当前时间片 + 上一时间片」内可复验，跨度约 10–20 分钟；
// 恰好跨过时间片边界时 dry-run → 执行不会被误拒，超过两片即视为过期需重新 dry-run。
const confirmTokenBucketSeconds int64 = 600

// confirmNow 是可注入时钟，测试里前移时间验证令牌过期；生产恒为 time.Now。
var confirmNow = time.Now

// confirmDomain 是令牌哈希的域前缀，非秘密，只做域隔离避免与其它哈希撞车。
const confirmDomain = "verifyctl-confirm/v1"

// makeConfirmToken 基于命令身份 + 用户参数 + 当前时间片生成短确认令牌（16 位十六进制）。
// 控制参数（pretty / confirm_token / __cli_* 等）不进哈希，保证 dry-run 签发与执行时校验
// 基于同一份「将要发送的参数」。令牌只在当前与上一时间片内可复验，过期必须重新 dry-run。
func makeConfirmToken(cmd Command, params map[string]string) string {
	return makeConfirmTokenAt(cmd, params, confirmNow().Unix()/confirmTokenBucketSeconds)
}

// makeConfirmTokenAt 是 makeConfirmToken 的指定时间片版本，供过期回退校验使用。
func makeConfirmTokenAt(cmd Command, params map[string]string, bucket int64) string {
	h := sha256.New()
	h.Write([]byte(confirmDomain))
	h.Write([]byte{0})
	h.Write([]byte(cmd.Name))
	h.Write([]byte{0})
	h.Write([]byte(cmd.Path))
	h.Write([]byte{0})
	h.Write([]byte(cmd.Method))
	h.Write([]byte{0})

	keys := make([]string, 0, len(params))
	for k := range params {
		if isControlParam(k) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(params[k]))
		h.Write([]byte{0})
	}

	// 时间片也进哈希：同命令同参数在不同时间片签出的令牌不同，天然带短时效。
	var bucketBytes [8]byte
	binary.BigEndian.PutUint64(bucketBytes[:], uint64(bucket)) // #nosec G115 -- 时间片由 Unix()/600 计算，恒为非负
	h.Write(bucketBytes[:])

	return hex.EncodeToString(h.Sum(nil)[:8])
}

// verifyConfirmToken 常量时间比较用户回填的令牌与期望令牌。
// 空串或长度不一致直接拒绝；用 subtle.ConstantTimeCompare 避免时序旁路。
func verifyConfirmToken(got, want string) bool {
	if got == "" || want == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
