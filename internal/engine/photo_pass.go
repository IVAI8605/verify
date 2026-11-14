// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"verify/internal/applog"
	"verify/internal/config"
	"verify/internal/photo"
	"verify/internal/store"
)

// AlgoVersion 标记图片指纹算法版本；采样尺寸或方向集变化后旧指纹整体失效。
const AlgoVersion = 1

// photoWorkers 是图片指纹 worker 数：解码 4000×3000 约占 36MB 内存，
// 2 并发峰值 ~72MB，对 32G NAS 无压力；调大收益低（IO 才是瓶颈）。
const photoWorkers = 2

// imgBatchWait 让路等待间隔：磁盘占用高时每轮等这么久再重试。
const imgBatchWait = 5 * time.Second

// RunPhotoPass 对全部图片文件计算 4 方向 dHash 指纹（外部供扫描 pass 与手动触发调用）。
// 复用 DiskMonitor：每批开始前检测占用，忙则让路等待，不与前台 IO 抢机械盘。
func (e *Engine) RunPhotoPass(ctx context.Context, cfg *config.Config) {
	if !cfg.PhotoEnabled {
		return
	}
	if reset, err := e.st.EnsureImgAlgo(ctx, AlgoVersion); err != nil {
		applog.Error("检查图片指纹算法版本失败：%v", err)
	} else if reset {
		applog.Warn("图片指纹算法已更新，全部旧指纹已失效，将重新计算")
	}
	total := 0
	e.updateState(func(s *State) { s.Phase = "photo"; s.Message = "图片指纹计算中…" }) // v1.28.1：补阶段上报
	for {
		if ctx.Err() != nil {
			return
		}
		cands, err := e.st.ImgCandidates(ctx, 40)
		if err != nil {
			e.fail(err)
			return
		}
		if len(cands) == 0 {
			break
		}
		if total == 0 {
			// 首批进入时把阶段进度清零（Total 未知——按处理计数，前端显示累计）
			e.updateState(func(s *State) { s.Done = 0; s.Total = 0 })
		}
		if e.diskBusy.Load() {
			e.updateState(func(s *State) { s.Message = "磁盘占用高，图片分析让路…" })
			e.publishProgress(true)
			select {
			case <-ctx.Done():
				return
			case <-time.After(imgBatchWait):
			}
			continue
		}

		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, photoWorkers)
		results := make([]store.ImgResult, 0, len(cands))
	outerPhoto:
		for _, f := range cands {
			if ctx.Err() != nil {
				break
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				break outerPhoto
			}
			wg.Add(1)
			go func(f store.FileRow) {
				defer wg.Done()
				defer func() { <-sem }()
				fp, ferr := photo.Fingerprint(f.Path)
				mu.Lock()
				defer mu.Unlock()
				e.updateState(func(s *State) {
					s.Done++
					s.Current = f.Name
				})
				res := store.ImgResult{ID: f.ID, Size: f.Size, Mtime: f.Mtime}
				if ferr != nil {
					res.Err = ferr.Error()
				} else {
					hs := make([]string, 4)
					for i, h := range fp.Hashes {
						hs[i] = strconv.FormatUint(h, 16)
					}
					res.Hash = strings.Join(hs, "|")
					res.W, res.H = fp.W, fp.H
				}
				results = append(results, res)
			}(f)
		}
		wg.Wait()
		total += len(results)
		if len(results) > 0 {
			if err := e.st.SetImgBatch(ctx, results); err != nil {
				e.fail(err)
				return
			}
		}
	}
	if total > 0 {
		applog.Info("图片指纹分析完成，共处理 %d 个文件", total)
	}
}
