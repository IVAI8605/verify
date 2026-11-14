// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package lrucache 提供一个按字节数与条目数双重限流的内存 LRU。
//
// 用途：缩略图这类"生成过一次、反复读取"的小对象，
// 驻内存后滚动浏览不再反复读 cache 目录，机械盘负载明显下降。
package lrucache

import (
	"container/list"
	"sync"
)

// Cache 是线程安全的 LRU。
type Cache struct {
	mu       sync.Mutex
	maxEntry int
	maxBytes int64
	bytes    int64
	ll       *list.List
	items    map[string]*list.Element
}

type entry struct {
	key string
	val []byte
}

// New 构造缓存；maxEntry 与 maxBytes 任一超限都会淘汰最旧项。
func New(maxEntry int, maxBytes int64) *Cache {
	if maxEntry <= 0 {
		maxEntry = 1
	}
	if maxBytes <= 0 {
		maxBytes = 1
	}
	return &Cache{
		maxEntry: maxEntry,
		maxBytes: maxBytes,
		ll:       list.New(),
		items:    make(map[string]*list.Element, maxEntry),
	}
}

// Get 返回缓存值并把它标记为最近使用。
func (c *Cache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	it := el.Value.(*entry)
	return it.val, true
}

// Put 写入缓存；单个值超过总容量一半时拒绝，防止个别大图把缓存挤空。
func (c *Cache) Put(key string, val []byte) {
	if int64(len(val)) > c.maxBytes/2 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		it := el.Value.(*entry)
		c.bytes -= int64(len(it.val))
		it.val = val
		c.bytes += int64(len(val))
		c.ll.MoveToFront(el)
		c.evictLocked()
		return
	}
	el := c.ll.PushFront(&entry{key: key, val: val})
	c.items[key] = el
	c.bytes += int64(len(val))
	c.evictLocked()
}

// Len 返回当前条数，测试用。
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Bytes 返回当前缓存字节数（缓存管理统计用）。
func (c *Cache) Bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// Purge 清空全部条目（缓存管理"清理内存缓存"用）。
func (c *Cache) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.items = map[string]*list.Element{}
	c.bytes = 0
}

// Resize 调整上限并立即淘汰超额条目（设置页修改内存预算后生效）。
func (c *Cache) Resize(maxEntry int, maxBytes int64) {
	if maxEntry <= 0 {
		maxEntry = 1
	}
	if maxBytes <= 0 {
		maxBytes = 1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maxEntry = maxEntry
	c.maxBytes = maxBytes
	c.evictLocked()
}

// evictLocked 淘汰最旧项直到两个上限都满足。调用方持有 c.mu。
func (c *Cache) evictLocked() {
	for c.ll.Len() > c.maxEntry || (c.bytes > c.maxBytes && c.ll.Len() > 0) {
		back := c.ll.Back()
		if back == nil {
			return
		}
		it := back.Value.(*entry)
		c.ll.Remove(back)
		delete(c.items, it.key)
		c.bytes -= int64(len(it.val))
	}
}
