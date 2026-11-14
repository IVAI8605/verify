// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

//go:build linux

package engine

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"unsafe"

	"verify/internal/applog"
)

// atFDCWD 是 renameat2 的「相对当前工作目录」，UAPI 里是 -100。
// Go 的 syscall 包没有导出它，用补码形式避开「负数常量转 uintptr 溢出」。
var atFDCWD = ^uintptr(99)

// renameNoReplace 对应 RENAME_NOREPLACE：目标存在时返回 EEXIST，绝不覆盖。
const renameNoReplace = 1

var renameat2FallbackOnce sync.Once

// renameNoOverwrite 用 renameat2(RENAME_NOREPLACE) 原子改名（v3.22.0）。
//
// 相比 Lstat + os.Rename，这里把「目标不存在」的检查交给内核，在同一系统调用里
// 完成判断与改名，不存在「检查完到 rename 之间被人占了名字」的覆盖窗口。
// 部分 FUSE 卷 / 老内核不认这个标志，退回旧实现并记一次日志。
func renameNoOverwrite(oldPath, newPath string) error {
	// v3.22.0：Go 的 syscall 包未导出 SYS_RENAMEAT2，按架构在单独文件里给常量；
	// 未覆盖的架构常量为 0，直接退回旧实现。
	if sysRenameat2 == 0 {
		return renameNoOverwriteFallback(oldPath, newPath)
	}
	oldPtr, err := syscall.BytePtrFromString(oldPath)
	if err != nil {
		return err
	}
	newPtr, err := syscall.BytePtrFromString(newPath)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(sysRenameat2,
		atFDCWD, uintptr(unsafe.Pointer(oldPtr)),
		atFDCWD, uintptr(unsafe.Pointer(newPtr)),
		uintptr(renameNoReplace), 0)
	if errno == 0 {
		return nil
	}
	if errors.Is(errno, syscall.ENOSYS) || errors.Is(errno, syscall.EINVAL) || errors.Is(errno, syscall.EOPNOTSUPP) {
		renameat2FallbackOnce.Do(func() {
			applog.Warn("当前文件系统不支持 renameat2(RENAME_NOREPLACE)，改名回退到 Lstat + rename（仍有极小覆盖窗口）")
		})
		return renameNoOverwriteFallback(oldPath, newPath)
	}
	if errors.Is(errno, syscall.EEXIST) {
		// v3.25.0（P2-7 兜底）：大小写不敏感卷（CIFS/NTFS 挂载）上，
		// 「只改大小写」的改名 old/new 指向同一个 inode，内核仍会报 EEXIST。
		// SameFile 成立时用普通 rename 改回大小写，不会覆盖别的文件。
		if fiOld, e1 := os.Lstat(oldPath); e1 == nil {
			if fiNew, e2 := os.Lstat(newPath); e2 == nil && os.SameFile(fiOld, fiNew) {
				return os.Rename(oldPath, newPath)
			}
		}
		return errors.New("目标文件已存在，已跳过以免覆盖")
	}
	return &os.PathError{Op: "renameat2", Path: oldPath, Err: errno}
}
