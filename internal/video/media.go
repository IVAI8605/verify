// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package video

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// MediaInfo 是 ffprobe 采集的视频基础元数据。
type MediaInfo struct {
	Duration   float64
	Width      int
	Height     int
	Codec      string
	BitRate    int64
	SampleRate int
}

type probeStream struct {
	CodecType  string `json:"codec_type"`
	CodecName  string `json:"codec_name"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	SampleRate string `json:"sample_rate"`
	BitRate    string `json:"bit_rate"`
	Duration   string `json:"duration"`
}

type probeResult struct {
	Streams []probeStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
		BitRate  string `json:"bit_rate"`
	} `json:"format"`
}

// ProbeInfo 一次调用 ffprobe 获取时长、分辨率、编码、码率与音频采样率。
func (r *Runner) ProbeInfo(ctx context.Context, path string) (MediaInfo, error) {
	if r.FFprobe == "" {
		return MediaInfo{}, fmt.Errorf("未找到 ffprobe")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, r.FFprobe, "-v", "error", // #nosec G204 -- 可执行文件为探测到的 ffprobe 绝对路径，其余参数为常量
		"-show_entries", "format=duration,bit_rate:stream=codec_type,codec_name,width,height,sample_rate,bit_rate,duration",
		"-of", "json", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		logFFmpegStderr("媒体信息解析", path, stderr.String())
		return MediaInfo{}, fmt.Errorf("媒体信息读取失败：%s", friendlyFFmpegError(stderr.String()))
	}
	var pr probeResult
	if err := json.Unmarshal(out, &pr); err != nil {
		return MediaInfo{}, fmt.Errorf("解析 ffprobe 输出失败: %w", err)
	}
	info := MediaInfo{}
	if v, e := strconv.ParseFloat(strings.TrimSpace(pr.Format.Duration), 64); e == nil && v > 0 {
		info.Duration = v
	}
	if v, e := strconv.ParseInt(strings.TrimSpace(pr.Format.BitRate), 10, 64); e == nil && v > 0 {
		info.BitRate = v
	}
	for _, s := range pr.Streams {
		switch s.CodecType {
		case "video":
			if info.Width == 0 && s.Width > 0 {
				info.Width, info.Height = s.Width, s.Height
			}
			if info.Codec == "" {
				info.Codec = s.CodecName
			}
			if info.BitRate == 0 {
				if v, e := strconv.ParseInt(strings.TrimSpace(s.BitRate), 10, 64); e == nil && v > 0 {
					info.BitRate = v
				}
			}
			if info.Duration == 0 {
				if v, e := strconv.ParseFloat(strings.TrimSpace(s.Duration), 64); e == nil && v > 0 {
					info.Duration = v
				}
			}
		case "audio":
			if info.SampleRate == 0 {
				if v, e := strconv.Atoi(strings.TrimSpace(s.SampleRate)); e == nil && v > 0 {
					info.SampleRate = v
				}
			}
			if info.Codec == "" {
				info.Codec = s.CodecName
			}
		}
	}
	if info.Duration <= 0 {
		return MediaInfo{}, fmt.Errorf("无法读取时长")
	}
	return info, nil
}

// FileFingerprint 是视频/片头分析的完整结果。
type FileFingerprint struct {
	Audio  []byte
	Thumbs string
	Media  MediaInfo
}
