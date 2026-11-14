// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

// Package health 提供"文件健康"四类检测的判定逻辑：
// 空文件、扩展名与内容不符、损坏视频、不规范文件名。
//
// 这里只做纯判定，不碰数据库、不碰 HTTP，因此可以在任何平台上直接单测。
// 扫描调度、进度与结果落库由 engine 负责。
package health

// Kind 标识一类检测任务。
type Kind string

const (
	// KindEmpty 零字节文件。
	KindEmpty Kind = "empty"
	// KindEmptyDir 空目录树（v1.14.0 引入 10.2）。
	KindEmptyDir Kind = "empty_dir"
	// KindExt 扩展名与内容不符（含多重扩展名、大写扩展名）。
	KindExt Kind = "ext"
	// KindBroken 无法正常解码的文件（v1.14.0 扩展：视频/音频/图片/zip 系/JSON/XML）。
	KindBroken Kind = "broken"
	// KindName 不规范文件名。
	KindName Kind = "name"
	// KindTraditional 文件名含繁体字（v1.27.0）。
	KindTraditional Kind = "traditional"
	// KindTemp 临时文件（v1.34.0 引入 F1）。
	KindTemp Kind = "temp"
	// KindAll v3.9.3：一次扫描全部类型的入口标记。
	// 它只用于启动扫描，不产生该类型的落库记录，因此 Valid 不收录。
	KindAll Kind = "all"
)

// IsAll 判断是否为“一次扫描全部类型”。
func IsAll(k Kind) bool { return k == KindAll }

// Valid 判断是否为已知检测类型。
func (k Kind) Valid() bool {
	switch k {
	case KindEmpty, KindEmptyDir, KindExt, KindBroken, KindName, KindTraditional, KindTemp:
		return true
	}
	return false
}

// AllKinds 返回全部检测类型，顺序与界面标签一致。
func AllKinds() []Kind {
	return []Kind{KindEmpty, KindEmptyDir, KindExt, KindBroken, KindName, KindTraditional, KindTemp}
}

// AdvisoryReason 判断某个原因是否属于「无法确定，仅供参考」（v3.15.0）。
//
// 三类都属于「没能得出结论」而不是「确认有问题」：
//   - unknown：扩展名像媒体类型，但文件头认不出内容；
//   - timeout：探测超时，可能只是同机其它任务抢了 CPU；
//   - oversized：超过检测上限，压根没检测。
//
// 这类结论只用于提示扫描覆盖面：不计入可处理数量、默认不列出、不允许批量改名。
func AdvisoryReason(reason string) bool {
	switch reason {
	case ReasonUnknown, ReasonTimeout, ReasonOversized:
		return true
	}
	return false
}

// Label 返回中文名称，用于日志与结果表。
func (k Kind) Label() string {
	switch k {
	case KindEmpty:
		return "空文件"
	case KindEmptyDir:
		return "空目录"
	case KindExt:
		return "扩展名不符"
	case KindBroken:
		return "损坏文件"
	case KindName:
		return "文件名不规范"
	case KindTraditional:
		return "繁体文件名"
	case KindTemp:
		return "临时文件"
	case KindAll:
		return "全部类型"
	}
	return string(k)
}
