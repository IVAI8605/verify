// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package applog 统一应用日志：中文级别前缀、按行加时间戳、单文件 10MB 轮转（保留 5 个）。
// 日志目录可运行时切换，切换失败时保留原文件，避免排查信息中断。
package applog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"verify/internal/perm"
)

const (
	fileName   = "verify.log"
	maxBytes   = 10 << 20
	maxBackups = 5
)

// Writer 同时写日志文件和标准输出；飞牛的服务脚本仍会把标准输出收进 info.log。
type Writer struct {
	mu       sync.Mutex
	dir      string
	fallback string
	file     *os.File
	size     int64
	stdout   *os.File

	// 待写缓冲：日志先驻内存，攒够条数或超时再一次性落盘。
	// 机械盘场景下每条日志一次小写入，会把刚休眠的盘反复唤醒。
	pending    []string
	flushTimer *time.Timer
}

const (
	// flushThresholdLines 攒够这么多行立即落盘。
	flushThresholdLines = 32
	// flushDelayLine 不足一批时最多等多久。
	flushDelayLine = 2 * time.Second
)

// DefaultDir 返回默认日志目录。
func DefaultDir(dataDir string) string {
	return filepath.Join(dataDir, "logs")
}

var defaultWriter = &Writer{stdout: os.Stdout}

// Init 打开日志文件；configured 为空时使用 fallback。
// 自定义目录不可用时回退到默认目录，保证启动阶段仍有日志可查。
func Init(fallback, configured string) error {
	err := defaultWriter.reopen(fallback, configured)
	if err != nil && configured != "" {
		if ferr := defaultWriter.reopen(fallback, fallback); ferr == nil {
			return fmt.Errorf("%w（已回退到默认目录 %s）", err, fallback)
		}
	}
	return err
}

// Switch 切换日志目录；configured 为空时回到 fallback。
func Switch(configured string) error {
	return defaultWriter.reopen("", configured)
}

// Flush 把缓冲中的日志立即落盘。
// 测试与优雅退出前调用；正常运行时由批阈值和定时器兜底。
func Flush() {
	defaultWriter.mu.Lock()
	defaultWriter.flushLocked()
	defaultWriter.mu.Unlock()
}

// Out 返回可直接交给 log.SetOutput 的写入器。
func Out() *Writer {
	return defaultWriter
}

// Path 返回当前日志文件路径；未成功打开时返回预期路径。
func Path() string {
	defaultWriter.mu.Lock()
	defer defaultWriter.mu.Unlock()
	dir := defaultWriter.dir
	if dir == "" {
		dir = defaultWriter.fallback
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, fileName)
}

// Close 关闭当前日志文件。
func Close() error {
	defaultWriter.mu.Lock()
	defer defaultWriter.mu.Unlock()
	defaultWriter.flushLocked()
	if defaultWriter.file == nil {
		return nil
	}
	err := defaultWriter.file.Close()
	defaultWriter.file = nil
	return err
}

func (w *Writer) reopen(fallback, configured string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked()
	if fallback != "" {
		w.fallback = fallback
	}
	dir := strings.TrimSpace(configured)
	if dir == "" {
		dir = w.fallback
	}
	if dir == "" {
		return fmt.Errorf("日志目录为空")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	// 旧版本建的日志目录是 0755，MkdirAll 不会改它，这里补一次收紧。
	perm.Tighten(dir, 0o750)
	// 日志含文件路径等隐私信息，仅授权用户可读；文件名固定为 fileName，不含用户输入。
	f, err := os.OpenFile(filepath.Join(dir, fileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- 文件名为常量 fileName，目录经 config 校验
	if err != nil {
		return err
	}
	// perm 参数只在创建时生效，已有日志文件必须显式 chmod 才能从 0644 收到 0600。
	perm.Tighten(filepath.Join(dir, fileName), 0o600)
	info, statErr := f.Stat()
	old := w.file
	w.dir = dir
	w.file = f
	w.size = 0
	if statErr == nil {
		w.size = info.Size()
	}
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// Write 给每一行补上时间戳后写入文件与标准输出，保持 24 小时制。
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	text := strings.TrimSuffix(string(p), "\n")
	if text == "" {
		return len(p), nil
	}
	for _, line := range strings.Split(text, "\n") {
		w.enqueueLocked(time.Now().Format("2006-01-02 15:04:05") + " " + line + "\n")
	}
	return len(p), nil
}

// enqueueLocked 把一行放进缓冲；攒满一批立即落盘，否则起一个短定时器。
// 调用方持有 w.mu。
func (w *Writer) enqueueLocked(line string) {
	w.pending = append(w.pending, line)
	if len(w.pending) >= flushThresholdLines {
		w.flushLocked()
		return
	}
	if w.flushTimer == nil {
		w.flushTimer = time.AfterFunc(flushDelayLine, func() {
			w.mu.Lock()
			w.flushLocked()
			w.mu.Unlock()
		})
	}
}

// flushLocked 把缓冲写入文件与标准输出。调用方持有 w.mu。
func (w *Writer) flushLocked() {
	if w.flushTimer != nil {
		w.flushTimer.Stop()
		w.flushTimer = nil
	}
	if len(w.pending) == 0 {
		return
	}
	for _, line := range w.pending {
		if w.file != nil {
			if w.size+int64(len(line)) > maxBytes {
				w.rotateLocked()
			}
			if n, err := w.file.WriteString(line); err == nil {
				w.size += int64(n)
			}
		}
		if w.stdout != nil {
			_, _ = w.stdout.WriteString(line)
		}
	}
	w.pending = w.pending[:0]
}

func (w *Writer) rotateLocked() {
	if w.file == nil {
		return
	}
	_ = w.file.Close()
	path := filepath.Join(w.dir, fileName)
	_ = os.Remove(fmt.Sprintf("%s.%d", path, maxBackups))
	for i := maxBackups - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	_ = os.Rename(path, path+".1")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- 文件名为常量 fileName，目录为已打开的日志目录
	if err != nil {
		w.file = nil
		w.size = 0
		return
	}
	// 轮转前若 rename 失败，这里会续写旧文件，权限仍需再收一次。
	perm.Tighten(path, 0o600)
	w.file = f
	w.size = 0
}

// Info 输出中文“信息”级日志。
func Info(format string, args ...any) {
	log.Printf("[信息] "+format, args...)
}

// Warn 输出中文“警告”级日志。
func Warn(format string, args ...any) {
	log.Printf("[警告] "+format, args...)
}

// Error 输出中文“错误”级日志。
func Error(format string, args ...any) {
	log.Printf("[错误] "+format, args...)
}
