// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package lrucache

import (
	"bytes"
	"fmt"
	"testing"
)

// TestLRUEvictsOldest 验证容量淘汰顺序是"最久未使用"。
func TestLRUEvictsOldest(t *testing.T) {
	c := New(2, 1<<20)
	c.Put("a", []byte("1"))
	c.Put("b", []byte("2"))
	c.Get("a") // a 变成最近使用
	c.Put("c", []byte("3"))

	if _, ok := c.Get("b"); ok {
		t.Fatal("b 应被淘汰")
	}
	if v, ok := c.Get("a"); !ok || string(v) != "1" {
		t.Fatal("a 应保留")
	}
	if v, ok := c.Get("c"); !ok || string(v) != "3" {
		t.Fatal("c 应保留")
	}
}

// TestLRUByteLimit 验证按字节总量淘汰。
func TestLRUByteLimit(t *testing.T) {
	c := New(100, 12) // 总共 12 字节，单值 6 字节不触发超限拒绝
	c.Put("big", bytes.Repeat([]byte("x"), 6))
	c.Put("big2", bytes.Repeat([]byte("y"), 6))
	c.Put("big3", bytes.Repeat([]byte("w"), 6)) // 总量超限，最早的 big 应被淘汰

	if _, ok := c.Get("big"); ok {
		t.Fatal("超过总容量时最早的大值应被淘汰")
	}
	if v, ok := c.Get("big2"); !ok || len(v) != 6 {
		t.Fatal("big2 应保留")
	}
	if _, ok := c.Get("big3"); !ok {
		t.Fatal("big3 应保留")
	}
}

// TestLRURejectOversize 验证单个超过总容量一半的值被拒绝，不挤占缓存。
func TestLRURejectOversize(t *testing.T) {
	c := New(10, 100)
	c.Put("ok", []byte("v"))
	c.Put("huge", bytes.Repeat([]byte("z"), 80))

	if _, ok := c.Get("huge"); ok {
		t.Fatal("超大值应被拒绝")
	}
	if _, ok := c.Get("ok"); !ok {
		t.Fatal("原值不应被挤出")
	}
}

// TestLRUOverwrite 验证同 key 覆盖不产生重复计数。
func TestLRUOverwrite(t *testing.T) {
	c := New(10, 1<<20)
	for i := 0; i < 100; i++ {
		c.Put("k", []byte(fmt.Sprint(i)))
	}
	if got := c.Len(); got != 1 {
		t.Fatalf("覆盖后应仍为 1 条，实际 %d", got)
	}
}
