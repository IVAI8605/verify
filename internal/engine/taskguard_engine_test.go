// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

// 本文件是 v3.18.0 的任务闸门回归：扫描占用磁盘时，ffmpeg 类任务必须被拒；
// 登记与历史快照能对上。

import (
	"context"
	"testing"

	"verify/internal/taskguard"
)

// TestCropRejectedWhileScanHeld 回归验收项「扫描中点识别竖横屏被拒」。
func TestCropRejectedWhileScanHeld(t *testing.T) {
	e, _, _ := newMoveDirEngine(t)
	done, err := e.acquireTask(taskguard.KindScan, "", taskguard.GroupIO)
	if err != nil {
		t.Fatal(err)
	}
	defer done("done")
	if _, _, err := e.CropLabelBatch(context.Background(), 1); err == nil {
		t.Fatal("扫描占用磁盘时 cropdetect 应被拒")
	}
}

// TestTaskSnapshot 验证运行中快照与结束后历史。
func TestTaskSnapshot(t *testing.T) {
	e, _, _ := newMoveDirEngine(t)
	done, err := e.acquireTask(taskguard.KindTranscode, "", taskguard.GroupFFmpeg, taskguard.ResFFmpeg)
	if err != nil {
		t.Fatal(err)
	}
	running, _ := e.TaskSnapshot()
	if len(running) != 1 || running[0].Kind != taskguard.KindTranscode {
		t.Fatalf("运行中任务应为 1 条转码，实际 %+v", running)
	}
	done("done")
	running, history := e.TaskSnapshot()
	if len(running) != 0 || len(history) != 1 {
		t.Fatalf("释放后应无运行中、1 条历史，实际 running=%v history=%v", running, history)
	}
}

// TestScanRejectedWhileTranscodeHeld 回归验收项「转码中点开始扫描被拒」。
// 转码占用 ffmpeg 独占槽时，StartScanMode 必须在建状态前返回冲突。
func TestScanRejectedWhileTranscodeHeld(t *testing.T) {
	e, _, _ := newMoveDirEngine(t)
	done, err := e.acquireTask(taskguard.KindTranscode, "", taskguard.GroupFFmpeg)
	if err != nil {
		t.Fatal(err)
	}
	defer done("done")
	if err := e.StartScanMode(ScanModeQuick); err == nil {
		t.Fatal("转码占用编码资源时启动扫描应被拒")
	}
	if e.IsRunning() {
		t.Fatal("被拒后不应留下扫描运行状态")
	}
}
