// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件是 v3.0.0 从 server.go 抽出的登录节流逻辑（设计文档 §1.3）。
// 独立成文件是为了让「登录安全」这一块和路由/业务 handler 分开看，
// 逻辑本身未改动。

import (
	"time"

	"verify/internal/applog"
)

// loginFail 单个来源 IP 的失败窗口。
type loginFail struct {
	count     int
	windowAt  time.Time // 首次失败时间，超过 loginWindow 视为一轮
	lockedTil time.Time // 非零表示锁定截止时间
}

const (
	loginMaxFails = 5
	loginWindow   = time.Minute
	loginLockout  = 5 * time.Minute
)

// loginThrottleCheck 返回当前是否处于锁定状态（true=拒绝登录尝试）。
func (s *Server) loginThrottleCheck(ip string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	f, ok := s.loginFails[ip]
	if !ok {
		return false
	}
	now := time.Now()
	if !f.lockedTil.IsZero() {
		if now.Before(f.lockedTil) {
			return true
		}
		delete(s.loginFails, ip) // 锁定期结束，重新计数
		return false
	}
	if now.Sub(f.windowAt) > loginWindow {
		delete(s.loginFails, ip)
	}
	return false
}

// loginThrottleFail 记录一次失败；达到阈值则锁定。
func (s *Server) loginThrottleFail(ip string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if s.loginFails == nil {
		s.loginFails = map[string]*loginFail{}
	}
	// 防御性清理：条目过多时整体重置（正常局域网场景远达不到）
	if len(s.loginFails) > 1000 {
		s.loginFails = map[string]*loginFail{}
	}
	now := time.Now()
	f, ok := s.loginFails[ip]
	if !ok || now.Sub(f.windowAt) > loginWindow {
		s.loginFails[ip] = &loginFail{count: 1, windowAt: now}
		return
	}
	f.count++
	if f.count >= loginMaxFails {
		f.lockedTil = now.Add(loginLockout)
		applog.Warn("登录失败过多（%s），锁定 %d 分钟", ip, int(loginLockout.Minutes()))
	}
}

// loginThrottleReset 登录成功后清零该 IP 的失败记录。
func (s *Server) loginThrottleReset(ip string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	delete(s.loginFails, ip)
}
