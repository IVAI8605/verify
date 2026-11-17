// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件是 v3.1.0 的隔离区导出备份（设计文档 §2）。
// 把隔离区里登记的文件打包成 zip 下载，方便用户在执行「永久删除」前先留一份备份。
//
// 安全约束：
//   - 只读 quarantine 表登记的 trash_path，请求里的 ids 仅用于查表，不拼路径；
//   - 每个文件读取前走 Engine.ResolveTrashPath（卷内 + .verify-trash + 软链接三步校验）；
//   - zip 条目名由已解析的真实路径裁剪而来，不含用户可控的目录片段。
//
// 实现取舍：先写临时 zip 再提供下载，而不是边压边发。原因是浏览器下载需要
// Content-Length 才能显示进度；标准库 zip 的压缩后大小在写完前无法准确预知。
// 临时文件落在缓存目录，下载结束即删除，内存占用保持常数级。

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"verify/internal/applog"
	"verify/internal/oplog"
	"verify/internal/store"
)

// handleTrashExport 导出隔离区为 zip。
// GET /api/trash/export?all=true 或 ?ids=1,2,3。
func (s *Server) handleTrashExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	wantAll := strings.EqualFold(r.URL.Query().Get("all"), "true")
	ids, err := parseIDList(r.URL.Query().Get("ids"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "ids 参数应为逗号分隔的整数"})
		return
	}
	if !wantAll && len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "未指定导出条目"})
		return
	}

	all, err := s.st.ListTrash(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	// v3.10.10（第五轮 P2-1）：导出前同样清掉幽灵行，导出清单里不再出现空条目。
	all = s.e.HealTrashRows(r.Context(), all)
	rows := selectTrashRows(all, ids, wantAll)
	if len(rows) == 0 {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "没有可导出的隔离区条目"})
		return
	}
	// v3.10.10（P2-5）：导出先写暂存文件再写 zip，峰值约源文件总量 2 倍且都落在
	// 缓存目录（与 verify.db 同卷）。空间不足时不提前拦住，写满后 SQLite 写失败
	// 会留下「磁盘已动、索引未写」的中间态。
	if err := s.trashExportSpaceCheck(rows); err != nil {
		writeJSON(w, http.StatusInsufficientStorage, errorResponse{Error: err.Error()})
		return
	}

	tmp, err := os.CreateTemp(s.cacheDir(), "trash-export-*.zip")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "创建导出临时文件失败：" + err.Error()})
		return
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	exportedIDs, skipped, werr := s.writeTrashZip(r, tmp, rows)
	if werr != nil {
		// v3.10.8：zip 没写完整就绝不能标记「已备份」——否则用户会据此永久删除，
		// 而手里那个下载包其实打不开，隔离区副本成了唯一且即将被删的证据。
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "导出包写入失败：" + publicErrorText(werr.Error())})
		return
	}
	if len(exportedIDs) == 0 && len(skipped) == 0 {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "隔离区条目均已失效"})
		return
	}
	sz, err := tmp.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	name := "verify-trash-" + time.Now().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Skipped-Count", strconv.Itoa(len(skipped)))
	w.Header().Set("Cache-Control", "no-store")
	// ServeContent 负责 Content-Length 与 Range；文件是刚写好的临时文件。
	// v3.10.8：必须确认整包真正写给客户端后才标「已备份」。客户端中途断开时
	// ServeContent 不报错，这里用计数字节判断；Range 请求不完整也按未完成处理（保守）。
	cw := &countingWriter{ResponseWriter: w}
	http.ServeContent(cw, r, name, sz.ModTime(), tmp)
	if cw.n < sz.Size() {
		applog.Warn("隔离区导出未完整写出（%d/%d 字节），不标记已备份", cw.n, sz.Size())
		return
	}
	// 下载已结束，请求 ctx 可能已取消；标记用独立 ctx 落库。
	markCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := s.st.MarkTrashExported(markCtx, exportedIDs, time.Now().Unix()); err != nil {
		// 标记失败不影响下载，只影响界面徽标；记录后继续。
		applog.Warn("隔离区导出标记失败：%s", err.Error())
	}
	if len(skipped) > 0 {
		applog.Warn("隔离区导出跳过 %d 个条目（文件不存在或校验失败）", len(skipped))
	}
	// v3.10.10：导出会把这些条目标成「已备份」，是用户后续永久删除的依据，
	// 必须有业务级记录（中间件汇总行不含条目明细，且本路径已在 opSkipPaths 中跳过）。
	e := oplog.New("trash_export")
	e.TargetCount = len(exportedIDs)
	e.Summary = fmt.Sprintf("导出隔离区条目 %d 个（全部=%v）", len(exportedIDs), wantAll)
	e.Details = idPreview(exportedIDs, 20)
	s.recordOp(r, e)
}

// idPreview 生成操作记录里的条目明细；只取前 n 个，避免几千个 id 撑爆明细字段。
func idPreview(ids []int64, n int) []oplog.Detail {
	out := make([]oplog.Detail, 0, n+1)
	for i, id := range ids {
		if i == n {
			out = append(out, oplog.Detail{Reason: fmt.Sprintf("另有 %d 个条目未展开", len(ids)-n)})
			break
		}
		out = append(out, oplog.Detail{Field: "trash_id", New: strconv.FormatInt(id, 10)})
	}
	return out
}

// trashExportSpaceCheck 估算导出所需空间并在不足时拒绝（v3.10.10，P2-5）。
// 按库里的 size 求和再乘 2：每个文件先完整复制到暂存文件，再写入 zip。
// 估算偏保守——文件可能已被改动；目的只是拦住「几乎肯定写满」的导出。
// 无法探测的空间（非 Linux、Statfs 失败）保持放行，不阻断原有能力。
func (s *Server) trashExportSpaceCheck(rows []store.TrashRow) error {
	var total int64
	for _, row := range rows {
		if row.Size > 0 {
			total += row.Size
		}
	}
	if total <= 0 || total > 1<<61 {
		return nil
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(s.cacheDir(), &fs); err != nil {
		return nil
	}
	bs := blockSizeUint64(fs.Bsize)
	if bs == 0 {
		return nil
	}
	need := total * 2
	// fs.Bavail 是 uint64，乘上块大小后可能超过 int64 上限（极端容量卷）。
	// 这里只用于「够不够」判断和提示文案，超出时按 MaxInt64 截断即可，不做精确换算。
	free := int64(math.MaxInt64)
	if fs.Bavail <= uint64(math.MaxInt64)/bs {
		free = int64(fs.Bavail * bs) // #nosec G115 -- 上一行已用 MaxInt64/bs 做上界，乘积不会溢出
	}
	if free < need {
		return fmt.Errorf("缓存目录可用空间不足：导出约需 %s，当前可用 %s", humanBytes(need), humanBytes(free))
	}
	return nil
}

// countingWriter 统计真正写给客户端的字节数，用于判断下载是否完整。
type countingWriter struct {
	http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.n += int64(n)
	return n, err
}

// parseIDList 解析逗号分隔的 id；空串返回空切片。
func parseIDList(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid id: %s", p)
		}
		out = append(out, n)
	}
	return out, nil
}

// selectTrashRows 按 all/ids 取子集；保持列表原有顺序。
func selectTrashRows(all []store.TrashRow, ids []int64, wantAll bool) []store.TrashRow {
	if wantAll {
		return all
	}
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make([]store.TrashRow, 0, len(ids))
	for _, row := range all {
		if want[row.ID] {
			out = append(out, row)
		}
	}
	return out
}

// writeTrashZip 把可读的隔离区条目写入 zip，返回成功导出的 id 与被跳过的原路径。
// 先通过 ResolveTrashPath 逐个校验；失效条目写入 _skipped.txt 后继续，
// 不因为一个文件被手动删掉就中断整次导出。
//
// 单个文件先完整复制到暂存文件，确认读成功后再创建 zip 条目：
// 否则 io.Copy 中途失败会在包里留下一个截断条目，用户解压时才发现坏包。
// 返回 error 表示 zip 本身没写完整（磁盘满、临时文件不可写），调用方必须放弃此次导出。
func (s *Server) writeTrashZip(r *http.Request, dst *os.File, rows []store.TrashRow) ([]int64, []string, error) {
	zw := zip.NewWriter(dst)
	exported := make([]int64, 0, len(rows))
	skipped := make([]string, 0)
	used := make(map[string]bool, len(rows))
	for _, row := range rows {
		resolved, ok := s.e.ResolveTrashPath(row.TrashPath)
		if !ok {
			skipped = append(skipped, row.OrigPath)
			continue
		}
		name := zipEntryName(resolved)
		if name == "" || used[name] {
			skipped = append(skipped, row.OrigPath)
			continue
		}
		src, err := os.Open(resolved) // #nosec G304 -- resolved 已由 ResolveTrashPath 完成卷内与 .verify-trash 校验
		if err != nil {
			skipped = append(skipped, row.OrigPath)
			continue
		}
		stage, err := os.CreateTemp(s.cacheDir(), "trash-item-*")
		if err != nil {
			_ = src.Close()
			return exported, skipped, err
		}
		_, cpErr := io.Copy(stage, src)
		_ = src.Close()
		if cpErr == nil {
			_, cpErr = stage.Seek(0, io.SeekStart)
		}
		if cpErr != nil {
			_ = stage.Close()
			_ = os.Remove(stage.Name())
			skipped = append(skipped, row.OrigPath)
			continue
		}
		used[name] = true
		hdr := &zip.FileHeader{Name: name, Method: zip.Store}
		// Go 1.10 起 SetModTime 已废弃，直接写 Modified 字段（staticcheck SA1019）。
		hdr.Modified = time.Unix(row.DeletedAt, 0)
		dw, err := zw.CreateHeader(hdr)
		if err == nil {
			_, err = io.Copy(dw, stage)
		}
		_ = stage.Close()
		_ = os.Remove(stage.Name())
		if err != nil {
			return exported, skipped, err
		}
		exported = append(exported, row.ID)
	}
	if len(skipped) > 0 {
		// 清单用 ASCII 文件名，避免部分解压工具对中文条目名的兼容问题。
		if fw, err := zw.Create("_skipped.txt"); err == nil {
			_, _ = fmt.Fprintf(fw, "以下隔离区文件不存在或校验失败，未包含在备份中：\n")
			for _, p := range skipped {
				_, _ = fmt.Fprintf(fw, "%s\n", p)
			}
		}
	}
	if err := zw.Close(); err != nil {
		return exported, skipped, err
	}
	// 临时文件需回绕到开头再交给 ServeContent（zip.Writer 已顺序写到底部）。
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return exported, skipped, err
	}
	return exported, skipped, nil
}

// zipEntryName 把隔离区真实路径转成 zip 内相对路径：
// /vol6/.verify-trash/2026-11-05/x.mp4 → vol6/.verify-trash/2026-11-05/x.mp4。
func zipEntryName(resolved string) string {
	p := strings.TrimPrefix(filepath.ToSlash(resolved), "/")
	if p == "" {
		return ""
	}
	// 防御性拒绝异常片段；resolved 来自 EvalSymlinks，正常情况下不会出现。
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." {
			return ""
		}
	}
	return p
}
