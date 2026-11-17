// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件是 v3.4.0 跨设备副本识别（设计文档 §5 V3.4-1）。
//
// 设计要点：
//   - 只交换指纹（path/size/hash/quick），不传文件、不传凭据、不自动同步；
//   - 导出采用流式 JSON，18 万行不整体装进内存；
//   - 导入按机器名整体替换，格式错误不破坏已有数据；
//   - 结果页只有带 cross=1 时才加载索引并打「另一设备也有」标记。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"verify/internal/store"
)

// localMachineName 返回本机机器名（导出的指纹包标识）。
func localMachineName() string {
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		return "verify"
	}
	return strings.TrimSpace(h)
}

// handleSyncExport 导出本机指纹包：GET /api/sync/export。
func (s *Server) handleSyncExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	machine := localMachineName()
	name := "verify-fp-" + sanitizeFileName(machine) + "-" + time.Now().Format("20060102-150405") + ".json"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	// 流式写：{"version":1,"machine":"...","exported_at":N,"rows":[...]}
	fmt.Fprintf(w, `{"version":%d,"machine":%s,"exported_at":%d,"rows":[`, store.PeerPackageVersion, mustJSON(machine), time.Now().Unix())
	first := true
	_, err := s.st.ExportPeerRows(r.Context(), func(row store.PeerExportRow) error {
		if !first {
			_, _ = w.Write([]byte(","))
		}
		first = false
		b, mErr := json.Marshal(row)
		if mErr != nil {
			return mErr
		}
		_, werr := w.Write(b)
		return werr
	})
	if err != nil {
		// 已经开始输出，无法再改状态码；用一条错误说明结尾，避免残缺 JSON 卡死解析。
		_, _ = fmt.Fprintf(w, `],"error":%s}`, mustJSON(err.Error()))
		return
	}
	_, _ = w.Write([]byte("]}"))
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func sanitizeFileName(v string) string {
	repl := strings.NewReplacer("/", "_", "\\", "_", " ", "_", ":", "_")
	return repl.Replace(v)
}

// handleSyncImport 导入另一台机器的指纹包：POST /api/sync/import。
func (s *Server) handleSyncImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var pkg struct {
		Version int              `json:"version"`
		Machine string           `json:"machine"`
		Rows    []store.PeerFile `json:"rows"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 96<<20)).Decode(&pkg); err != nil {
		writeJSON(w, 400, errorResponse{Error: "指纹包解析失败：" + err.Error()})
		return
	}
	if pkg.Version != 0 && pkg.Version != store.PeerPackageVersion {
		writeJSON(w, 400, errorResponse{Error: fmt.Sprintf("不支持的指纹包版本：%d", pkg.Version)})
		return
	}
	pkg.Machine = strings.TrimSpace(pkg.Machine)
	if err := store.ValidatePeerMachine(pkg.Machine); err != nil {
		writeJSON(w, 400, errorResponse{Error: err.Error()})
		return
	}
	if len(pkg.Rows) > 1_000_000 {
		writeJSON(w, 400, errorResponse{Error: "指纹包行数超过 100 万"})
		return
	}
	valid := make([]store.PeerFile, 0, len(pkg.Rows))
	invalid := 0
	for _, row := range pkg.Rows {
		row.Path = strings.TrimSpace(row.Path)
		if row.Path == "" || len(row.Path) > 4096 || row.Size < 0 || len(row.Hash) > 128 || len(row.Quick) > 128 {
			invalid++
			continue
		}
		valid = append(valid, row)
	}
	if err := s.st.SavePeerPackage(r.Context(), pkg.Machine, valid); err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	s.invalidatePeerIndex()
	writeJSON(w, 200, syncImportResponse{Imported: len(valid), Invalid: invalid, Machine: pkg.Machine})
}

// handleSyncPeers 查看/删除已导入的机器：GET / DELETE /api/sync/peers?machine=。
func (s *Server) handleSyncPeers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.st.ListSyncPeers(r.Context())
		if err != nil {
			writeJSON(w, 500, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, 200, syncPeersResponse{Peers: items, Machine: localMachineName()})
	case http.MethodDelete:
		machine := strings.TrimSpace(r.URL.Query().Get("machine"))
		if machine == "" {
			writeJSON(w, 400, errorResponse{Error: "machine 参数不能为空"})
			return
		}
		if err := s.st.DeleteSyncPeer(r.Context(), machine); err != nil {
			writeJSON(w, 500, errorResponse{Error: err.Error()})
			return
		}
		s.invalidatePeerIndex()
		writeJSON(w, 200, okResponse{OK: true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
	}
}

// attachPeers 给分组内文件打「另一设备也有」标记；没有导入过任何指纹包时直接返回。
func (s *Server) attachPeers(ctx context.Context, groups []store.Group) []store.Group {
	idx := s.peerIndex(ctx)
	if idx == nil {
		return groups
	}
	for gi := range groups {
		for fi := range groups[gi].Files {
			f := &groups[gi].Files[fi]
			if f.Peer == "" {
				f.Peer = idx.MatchPeer(f.Path, f.Size, f.Hash)
			}
		}
	}
	return groups
}

// peerIndex 返回进程内缓存的指纹索引（v3.10.10，P2-15）。
// 以前每个分组类请求都全量重读 peer_files，导入多台机器后翻页会反复重建索引。
// 索引只读，导入/删除对端时由 invalidatePeerIndex 清空。
func (s *Server) peerIndex(ctx context.Context) *store.PeerIndex {
	s.peerIdxMu.Lock()
	defer s.peerIdxMu.Unlock()
	if s.peerIdx != nil {
		return s.peerIdx
	}
	idx, err := s.st.LoadPeerIndex(ctx)
	if err != nil {
		return nil
	}
	s.peerIdx = idx
	return idx
}

// invalidatePeerIndex 在导入或删除对端指纹包后让缓存失效。
func (s *Server) invalidatePeerIndex() {
	s.peerIdxMu.Lock()
	s.peerIdx = nil
	s.peerIdxMu.Unlock()
}
