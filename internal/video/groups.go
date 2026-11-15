// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package video

import (
	"math"
	"sort"
	"verify/internal/fname"
	"verify/internal/phash"
	"verify/internal/store"
)

type pairScore struct {
	sim   float64
	thumb int
}

// VerifyFunc 对阈值附近的疑似配对做二次确认；返回 false 表示正片内容不匹配。
type VerifyFunc func(a, b store.VideoFP) bool

const (
	verifyAudioMargin = 0.10
	verifyThumbMargin = 3
	maxVerifyPairs    = 64
)

// FindGroups 基于时长 + 音频指纹 + 缩略图 dHash 计算相似视频分组。
// 判定口径（v3.10.10，P1-1 起明确）：
//   - 两侧都有音频指纹：音频余弦 >= audioThr，且缩略图最小汉明距离 <= maxHamb；
//   - 至少一侧没有音频（无音轨 / 整段静音）：没有第二重证据，只按画面判定，
//     但要求「多数帧一致」（ThumbsMostlyClose），避免仅片头或台标相同就并组。
//
// verify 非空时，仅对阈值附近的配对调用二次校验，避免全量配对膨胀。
func FindGroups(items []store.VideoFP, audioThr float64, maxHamb int, verify VerifyFunc) []store.Group {
	return FindGroupsMode(items, audioThr, maxHamb, verify, false)
}

// FindGroupsMode 是 FindGroups 的带模式版本（v2.8.0）。
// thumbOnly=true 时只按开头缩略图 dHash 判定，完全不看音频指纹——
// 对应"仅缩略图"匹配模式，用于快速找出封面/开头画面相同的视频。
// v3.10.10（P1-1）：该分支同样要求多数帧一致，不再是「任意一帧命中即可」。
// 该模式下不调用二次校验：verify 依赖音频强匹配，没有音频就没有晋级依据。
func FindGroupsMode(items []store.VideoFP, audioThr float64, maxHamb int, verify VerifyFunc, thumbOnly bool) []store.Group {
	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}
	// 按时长排序，减少无效比较
	sort.Slice(idx, func(a, b int) bool {
		return items[idx[a]].Duration < items[idx[b]].Duration
	})
	sorted := make([]store.VideoFP, len(items))
	for i, k := range idx {
		sorted[i] = items[k]
	}

	parent := make([]int, len(sorted))
	rank := make([]int, len(sorted))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		if rank[ra] < rank[rb] {
			ra, rb = rb, ra
		}
		parent[rb] = ra
		if rank[ra] == rank[rb] {
			rank[ra]++
		}
	}

	best := map[string]*pairScore{}
	key := func(a, b int) string {
		if a > b {
			a, b = b, a
		}
		return itoa(a) + ":" + itoa(b)
	}

	// v3.10.6：桶宽必须 ≥ 2.5 秒的时长容差，否则跨两格的配对永远不会被比较。
	// 旧值 2.0 时，相差 2.4 秒的两条视频可能落在相隔两格的桶里（如 0.9s 与 3.3s），
	// 造成相似视频漏组；取 3.0 可保证任意 ≤2.5 秒的差值最多跨一个桶。
	bucketOf := func(d float64) int { return int(math.Round(d / 3.0)) }
	buckets := map[int][]int{}
	for i := range sorted {
		b := bucketOf(sorted[i].Duration)
		buckets[b] = append(buckets[b], i)
	}
	bkeys := make([]int, 0, len(buckets))
	for b := range buckets {
		bkeys = append(bkeys, b)
	}
	sort.Ints(bkeys)
	verifyCount := 0
	for _, b := range bkeys {
		// 与相邻桶合并比较（时长差 <= 约 4 秒）
		list := append([]int{}, buckets[b]...)
		if prev, ok := buckets[b-1]; ok {
			list = append(list, prev...)
		}
		sort.Ints(list)
		for ii := 0; ii < len(list); ii++ {
			for jj := ii + 1; jj < len(list); jj++ {
				i, j := list[ii], list[jj]
				a, bb := sorted[i], sorted[j]
				if math.Abs(a.Duration-bb.Duration) > 2.5 {
					continue
				}
				thumbD := MinHamming(a.Thumb, bb.Thumb)
				haveAudio := len(a.Audio) == Dim && len(bb.Audio) == Dim
				var audioSim float64
				if haveAudio {
					audioSim = phash.Cosine(a.Audio, bb.Audio)
				}
				if thumbOnly {
					// 仅缩略图模式：只认画面距离，忽略音频差异；仍要求多数帧一致。
					if thumbD >= 0 && ThumbsMostlyClose(a.Thumb, bb.Thumb, maxHamb) {
						union(i, j)
						k := key(i, j)
						if ps, ok := best[k]; !ok {
							best[k] = &pairScore{thumb: thumbD}
						} else if thumbD < ps.thumb {
							ps.thumb = thumbD
						}
					}
					continue
				}
				// 音频已强匹配但 dHash 差异很大时，可能是镜像/裁剪，交给二次校验而不是预筛丢弃。
				if thumbD >= 0 && thumbD > maxHamb*3 && !(verify != nil && haveAudio && audioSim >= audioThr) {
					continue
				}
				similar := false
				if haveAudio {
					if audioSim >= audioThr && (thumbD < 0 || thumbD <= maxHamb) {
						similar = true
					}
				} else if ThumbsMostlyClose(a.Thumb, bb.Thumb, maxHamb) {
					// 至少一侧没有音频：没有第二重证据，必须多数帧一致才成组。
					similar = true
				}
				if !similar && verify != nil && haveAudio && audioSim >= audioThr && thumbD >= 0 {
					// 二次校验只「晋级」不「降级」（审查 C-1）：
					// 音频强匹配但缩略图差异大的配对，verify 通过则归组，不通过才排除。
					if verifyCount < maxVerifyPairs {
						verifyCount++
						if !verify(a, bb) {
							continue
						}
						similar = true
					}
				}
				if !similar {
					continue
				}
				// 审查 C-1（2026-11-01）：此处原有一个对「thumbD 处于边缘区间」配对的
				// 二次校验，verify=false 会 continue 排除——但配对已被主规则判为相似，
				// 二次校验只应「晋级」不应「降级」，降级会导致相似视频漏组（方向为漏检，
				// 违背工具核心目的）。因此移除该降级路径；晋级语义保留在上方 !similar 分支。
				union(i, j)
				k := key(i, j)
				if ps, ok := best[k]; !ok || audioSim > ps.sim {
					best[k] = &pairScore{sim: audioSim, thumb: thumbD}
				}
			}
		}
	}

	members := map[int][]int{}
	for i := range sorted {
		r := find(i)
		members[r] = append(members[r], i)
	}

	var groups []store.Group
	roots := make([]int, 0, len(members))
	for r := range members {
		roots = append(roots, r)
	}
	sort.Ints(roots)
	for _, r := range roots {
		list := members[r]
		if len(list) < 2 {
			continue
		}
		// 组内最大相似度
		maxSim := 0.0
		for _, i := range list {
			for _, j := range list {
				if j <= i {
					continue
				}
				if ps, ok := best[key(i, j)]; ok && ps.sim > maxSim {
					maxSim = ps.sim
				}
			}
		}
		var rows []store.FileRow
		var total, maxSize int64
		lowest := int64(0)
		for _, i := range list {
			v := sorted[i]
			rows = append(rows, store.FileRow{
				ID:       v.ID,
				Path:     v.Path,
				Volume:   v.Volume,
				Name:     v.Name,
				Size:     v.Size,
				Mtime:    v.Mtime,
				Ext:      fname.Ext(v.Name),
				Duration: v.Duration,
				Thumb:    v.Thumb,
				MediaMeta: store.MediaMeta{
					Width: v.Width, Height: v.Height, Codec: v.Codec, BitRate: v.BitRate, SampleRate: v.SampleRate,
				},
			})
			total += v.Size
			if v.Size > maxSize {
				maxSize = v.Size
			}
			if lowest == 0 || v.ID < lowest {
				lowest = v.ID
			}
		}
		sort.Slice(rows, func(a, b int) bool { return rows[a].Path < rows[b].Path })
		groups = append(groups, store.Group{
			Key:        "v:" + itoa(int(lowest)),
			Name:       rows[0].Name,
			Count:      len(rows),
			TotalSize:  total,
			Waste:      total - maxSize,
			Similarity: math.Round(maxSim*1000) / 1000,
			Files:      rows,
		})
	}
	sort.Slice(groups, func(a, b int) bool { return groups[a].Waste > groups[b].Waste })
	return groups
}

// FindIntroGroups 基于开头 N 秒的音频指纹与帧 dHash 计算“相同片头”分组。
// 相似判定与整段视频一致（音频余弦 + 缩略图汉明距离），但只比较片头区间。
// 整段重复（相同内容、完整正片也一致）的文件会被剔除，只保留“片头相同、正片不同”的文件。
func FindIntroGroups(items []store.IntroFP, seconds int, audioThr float64, maxHamb int) []store.Group {
	n := len(items)
	if n < 2 {
		return nil
	}
	parent := make([]int, n)
	rank := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		if rank[ra] < rank[rb] {
			ra, rb = rb, ra
		}
		parent[rb] = ra
		if rank[ra] == rank[rb] {
			rank[ra]++
		}
	}

	dupPairs := map[string]bool{}
	best := map[string]*pairScore{}
	key := func(a, b int) string {
		if a > b {
			a, b = b, a
		}
		return itoa(a) + ":" + itoa(b)
	}

	for i := 0; i < n; i++ {
		a := items[i]
		aShort := a.Duration > 0 && a.Duration <= float64(seconds)+0.5
		for j := i + 1; j < n; j++ {
			b := items[j]
			bShort := b.Duration > 0 && b.Duration <= float64(seconds)+0.5
			// 整段都短于片头窗口的文件没有“正片”可供区分，不与长视频成组，
			// 避免短视频把一整组片头结果拉进来。
			if aShort != bShort {
				continue
			}
			thumbD := MinHamming(a.IntroThumb, b.IntroThumb)
			if thumbD >= 0 && thumbD > maxHamb*3 {
				continue
			}
			haveAudio := len(a.IntroAudio) == Dim && len(b.IntroAudio) == Dim
			var sim float64
			if haveAudio {
				sim = phash.Cosine(a.IntroAudio, b.IntroAudio)
			}
			similar := false
			if haveAudio {
				if sim >= audioThr && (thumbD < 0 || thumbD <= maxHamb) {
					similar = true
				}
			} else if thumbD >= 0 && thumbD <= maxHamb {
				similar = true
			}
			if !similar {
				continue
			}
			// 先按片头相似并入同一簇；整段重复的配对只记录标记，
			// 不能把两端都剔除，否则会连带删掉能与“片头相同、正片不同”文件成组的代表。
			union(i, j)
			k := key(i, j)
			if isFullDuplicate(a, b, seconds, audioThr, maxHamb) {
				dupPairs[k] = true
				continue
			}
			if ps, ok := best[k]; !ok || sim > ps.sim {
				best[k] = &pairScore{sim: sim, thumb: thumbD}
			}
		}
	}

	members := map[int][]int{}
	for i := range items {
		r := find(i)
		members[r] = append(members[r], i)
	}

	var groups []store.Group
	roots := make([]int, 0, len(members))
	for r := range members {
		roots = append(roots, r)
	}
	sort.Ints(roots)
	for _, r := range roots {
		list := members[r]
		if len(list) < 2 {
			continue
		}
		// 每个整段重复集合只保留最小 ID 的代表，其余副本从片头结果中剔除。
		removed := make([]bool, len(items))
		for ai := 0; ai < len(list); ai++ {
			i := list[ai]
			for bj := ai + 1; bj < len(list); bj++ {
				j := list[bj]
				if !dupPairs[key(i, j)] {
					continue
				}
				if items[i].ID <= items[j].ID {
					removed[j] = true
				} else {
					removed[i] = true
				}
			}
		}
		var kept []int
		for _, i := range list {
			if !removed[i] {
				kept = append(kept, i)
			}
		}
		if len(kept) < 2 {
			continue
		}
		maxSim := 0.0
		for _, i := range kept {
			for _, j := range kept {
				if j <= i {
					continue
				}
				if ps, ok := best[key(i, j)]; ok && ps.sim > maxSim {
					maxSim = ps.sim
				}
			}
		}
		var rows []store.FileRow
		var total, maxSize int64
		lowest := int64(0)
		for _, i := range kept {
			v := items[i]
			rows = append(rows, store.FileRow{
				ID:       v.ID,
				Path:     v.Path,
				Volume:   v.Volume,
				Name:     v.Name,
				Size:     v.Size,
				Mtime:    v.Mtime,
				Ext:      fname.Ext(v.Name),
				Duration: v.Duration,
				Thumb:    v.Thumb,
				MediaMeta: store.MediaMeta{
					Width: v.Width, Height: v.Height, Codec: v.Codec, BitRate: v.BitRate, SampleRate: v.SampleRate,
				},
			})
			total += v.Size
			if v.Size > maxSize {
				maxSize = v.Size
			}
			if lowest == 0 || v.ID < lowest {
				lowest = v.ID
			}
		}
		sort.Slice(rows, func(a, b int) bool { return rows[a].Path < rows[b].Path })
		groups = append(groups, store.Group{
			Key:        "intro:" + itoa(int(lowest)),
			Name:       rows[0].Name,
			Count:      len(rows),
			TotalSize:  total,
			Waste:      total - maxSize,
			Similarity: math.Round(maxSim*1000) / 1000,
			Files:      rows,
		})
	}
	sort.Slice(groups, func(a, b int) bool { return groups[a].Waste > groups[b].Waste })
	return groups
}

// isFullDuplicate 判断两个文件是否属于“整段重复”，用于从片头分组中剔除。
func isFullDuplicate(a, b store.IntroFP, seconds int, audioThr float64, maxHamb int) bool {
	// 两个文件都被片头区间完整覆盖时，片头相同即整段相同
	if a.Duration > 0 && b.Duration > 0 && a.Duration <= float64(seconds)+0.5 && b.Duration <= float64(seconds)+0.5 {
		return true
	}
	if a.Hash != "" && b.Hash != "" && a.Hash == b.Hash {
		return true
	}
	if len(a.Audio) == Dim && len(b.Audio) == Dim {
		return math.Abs(a.Duration-b.Duration) <= 2.5 &&
			phash.Cosine(a.Audio, b.Audio) >= audioThr &&
			ThumbsAllClose(a.Thumb, b.Thumb, maxHamb)
	}
	// 缺少整段指纹时的保守判断：大小与时长一致且片头已判定相似。
	// v3.10.6：只在「至少一侧没有整段指纹」时兜底，否则会推翻上面基于指纹的
	// 不相似结论——大小相同 + 时长差 ≤2.5s 不足以证明整段重复。
	if (len(a.Audio) != Dim || len(b.Audio) != Dim) &&
		a.Duration > 0 && b.Duration > 0 && math.Abs(a.Duration-b.Duration) <= 2.5 && a.Size == b.Size {
		return true
	}
	return false
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
