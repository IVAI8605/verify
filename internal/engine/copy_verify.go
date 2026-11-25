// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package engine

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"lukechampine.com/blake3"

	"verify/internal/applog"
	"verify/internal/hashx"
)

// 跨卷移动（v4.2.0，2026-11-25 建议 6）。
//
// 背景：应用内的 MoveFiles / MoveDir 原先只做同卷 os.Rename，跨卷直接拒绝，
// 本次手工把 68GB 文件搬到另一个卷只能绕到系统层。这里补上跨卷降级：
//   复制（写入目标卷）→ 校验（大小 + BLAKE3 全量哈希）→ 删源
// 顺序刻意是「先完全复制并校验成功，才删源」——trim-cli 那次事故正是
// 跨卷 mv 失败时把源删了只留半截副本，这个顺序保证任何一步失败源文件都还在。

// copyBufSize 是跨卷复制的读写缓冲。
const copyBufSize = 1 << 20

// moveFileAcross 把 src 移到 dst：同卷走原子 rename，跨卷自动降级为复制+校验+删源。
// 目标已存在时一律失败（绝不覆盖），与 renameNoOverwrite 语义一致。
func moveFileAcross(src, dst string) error {
	err := renameNoOverwrite(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	return copyFileVerified(src, dst, true)
}

// moveDirAcross 把目录整体移到 dst：同卷 rename，跨卷递归复制+校验+删源。
func moveDirAcross(src, dst string) error {
	err := renameNoOverwrite(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	return copyTreeVerified(src, dst)
}

// copyFileVerified 复制单个文件并校验；removeSrc 为真时校验通过后删除源文件。
//
// 校验用「复制时对源字节流做 BLAKE3」+「重读目标文件做 BLAKE3」两份一致，
// 只多读一遍目标文件，不重复读源，68GB 文件也只需一次源读 + 一次目的读。
func copyFileVerified(src, dst string, removeSrc bool) error {
	sfi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !sfi.Mode().IsRegular() {
		return errors.New("源不是普通文件，无法跨卷复制")
	}
	if err := ensureFreeSpace(filepath.Dir(dst), sfi.Size()); err != nil {
		return err
	}
	in, err := os.Open(src) // #nosec G304 -- 路径来自索引/请求校验后的受管卷路径
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, sfi.Mode().Perm())
	if err != nil {
		return err
	}
	hasher := blake3.New(32, nil)
	buf := make([]byte, copyBufSize)
	n, copyErr := io.CopyBuffer(io.MultiWriter(out, hasher), in, buf)
	closeErr := out.Close()
	cleanup := func() { _ = os.Remove(dst) }
	if copyErr != nil {
		cleanup()
		return fmt.Errorf("复制到目标卷失败：%w", copyErr)
	}
	if closeErr != nil {
		cleanup()
		return fmt.Errorf("写入目标文件失败：%w", closeErr)
	}
	if n != sfi.Size() {
		cleanup()
		return fmt.Errorf("复制后大小不一致（%d != %d）", n, sfi.Size())
	}
	got, err := hashx.FullHash(dst)
	if err != nil {
		cleanup()
		return fmt.Errorf("校验目标文件失败：%w", err)
	}
	if got != hex.EncodeToString(hasher.Sum(nil)) {
		cleanup()
		return errors.New("复制校验失败：目标文件与源文件内容不一致，已保留源文件")
	}
	// 权限位按源文件还原：OpenFile 的 mode 受进程 umask 影响，显式 chmod 才准。
	if err := os.Chmod(dst, sfi.Mode().Perm()); err != nil {
		applog.Warn("跨卷复制后设置权限失败（%s）：%v", dst, err)
	}
	// 保真修改时间：跨卷后探测缓存、文件查重都按 mtime 判断「文件是否变过」，
	// 不改回来会让刚搬过去的文件全部被当成新文件重算一遍。
	if err := os.Chtimes(dst, time.Now(), sfi.ModTime()); err != nil {
		applog.Warn("跨卷复制后设置修改时间失败（%s）：%v", dst, err)
	}
	if !removeSrc {
		return nil
	}
	if err := os.Remove(src); err != nil {
		// 目标已经是完整副本，删源失败不算数据丢失，但必须报出来让用户知道有两个副本。
		return fmt.Errorf("复制校验已完成，但删除源文件失败（源仍在 %s）：%w", src, err)
	}
	return nil
}

// copyTreeVerified 递归复制目录树并校验，全部成功后才删除源目录。
// 任何一步失败都会清掉半成品目标目录并保留源目录。
//
// 只支持普通文件与目录：遇到符号链接/设备文件等直接报错，
// 因为跨卷复制无法保证这些特殊文件的语义，宁可让用户改走同卷或手工处理。
func copyTreeVerified(src, dst string) error {
	sfi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !sfi.IsDir() {
		return errors.New("源不是目录")
	}
	walkErr := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			fi, serr := os.Stat(p)
			if serr != nil {
				return serr
			}
			return os.MkdirAll(target, fi.Mode().Perm()|0o700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("包含非普通文件（%s），跨卷移动暂不支持", p)
		}
		return copyFileVerified(p, target, false)
	})
	if walkErr != nil {
		_ = os.RemoveAll(dst)
		return walkErr
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("复制校验已完成，但删除源目录失败（源仍在 %s）：%w", src, err)
	}
	return nil
}

// ensureFreeSpace 在复制前粗查目标卷剩余空间，避免复制到一半把盘写满。
// 拿不到 statfs 时放行（不做猜测式拒绝），真正的失败仍由复制步骤报出。
func ensureFreeSpace(dir string, need int64) error {
	var sf syscall.Statfs_t
	if err := syscall.Statfs(dir, &sf); err != nil {
		return nil
	}
	free := int64(sf.Bavail) * int64(sf.Bsize)
	// 留 64MB 余量，避免刚好写满导致后续操作全部失败。
	if free < need+64<<20 {
		return fmt.Errorf("目标卷剩余空间不足：需要约 %d MB，可用 %d MB", (need+64<<20)>>20, free>>20)
	}
	return nil
}
