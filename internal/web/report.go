// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"verify/internal/store"
)

const reportMaxGroups = 3000

var reportImageExts = map[string]bool{
	"jpg": true, "jpeg": true, "png": true, "gif": true, "bmp": true, "webp": true, "heic": true, "avif": true, "tiff": true,
}

var reportVideoExts = map[string]bool{
	"mp4": true, "mkv": true, "avi": true, "mov": true, "wmv": true, "flv": true, "m4v": true, "ts": true, "mts": true,
	"m2ts": true, "mpg": true, "mpeg": true, "rmvb": true, "rm": true, "3gp": true, "webm": true, "vob": true, "ogv": true,
	"asf": true, "f4v": true, "divx": true, "m2v": true, "mxf": true, "dat": true,
}

// reportVideoExtList 返回视频扩展名清单（升序），供「相同视频」页导出复用精确查重通道。
func reportVideoExtList() []string {
	out := make([]string, 0, len(reportVideoExts))
	for ext := range reportVideoExts {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}

// reportModes 是报告/导出支持的页面模式白名单（v3.10.6）。
// 旧实现只特判 video / intro，其余任何值都会落进 ExactGroups——在「模糊文件名」
// 「语义分组」「相同视频」页点导出会拿到精确重复的数据，属于张冠李戴。
var reportModes = map[string]bool{
	"exact": true, "name": true, "video": true, "intro": true, "samevideo": true,
}

// errUnsupportedReportMode 表示调用方传了不在白名单里的模式，应回 400 而不是静默降级。
var errUnsupportedReportMode = errors.New("该页面暂不支持导出报告，可选：精确重复 / 同名文件 / 相似视频 / 相同片头 / 相同视频")

// writeCollectError 把分组收集错误映射成响应：模式非法是调用方问题（400），其余按服务端错误（500）。
func writeCollectError(w http.ResponseWriter, err error) {
	if errors.Is(err, errUnsupportedReportMode) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
}

// filterGroupsBySize 按大小上下限裁剪视频类分组的成员并重算组统计。
// 视频/片头分组不走 SQL 层筛选，必须在这里补上，否则导出结果比页面宽（v3.10.6 P0-3）。
func filterGroupsBySize(groups []store.Group, minSize, maxSize int64) []store.Group {
	if minSize <= 0 && maxSize <= 0 {
		return groups
	}
	out := make([]store.Group, 0, len(groups))
	for _, g := range groups {
		files := make([]store.FileRow, 0, len(g.Files))
		for _, f := range g.Files {
			if minSize > 0 && f.Size < minSize {
				continue
			}
			if maxSize > 0 && f.Size > maxSize {
				continue
			}
			files = append(files, f)
		}
		if len(files) < 2 {
			continue
		}
		var total, max int64
		for _, f := range files {
			total += f.Size
			if f.Size > max {
				max = f.Size
			}
		}
		g.Files = files
		g.Count = len(files)
		g.TotalSize = total
		g.Waste = total - max
		out = append(out, g)
	}
	return out
}

// collectGroups 复用导出与 HTML 报告的分组收集逻辑。
// 第二个返回值表示结果是否被 maxGroups 截断（v3.10.8 P2-9）：
// 截断必须让调用方知道并显式提示，否则用户以为拿到了全量分组。
func (s *Server) collectGroups(ctx context.Context, mode string, q store.GroupQuery, n, maxGroups int) ([]store.Group, bool, error) {
	if mode == "" {
		mode = "exact"
	}
	// v3.10.6：未知模式直接报错，不再静默降级成精确重复
	if !reportModes[mode] {
		return nil, false, errUnsupportedReportMode
	}
	roots := s.cfg.Get().Volumes
	q.Roots = roots
	// 「相同视频」页在结果页等价于精确重复 + 视频扩展名（见 helper.js 的 fetchChunk）
	if mode == "samevideo" {
		mode = "exact"
		q.Exts = reportVideoExtList()
	}
	switch mode {
	case "video", "intro":
		var groups []store.Group
		var err error
		if mode == "video" {
			groups, err = s.e.VideoGroups(ctx, false)
		} else {
			groups, err = s.e.IntroGroups(ctx, n, false)
		}
		if err != nil {
			return nil, false, err
		}
		groups = filterGroupsByRoots(groups, roots)
		// v3.10.6：视频类分组此前完全绕过大小筛选与数量上限，与精确页口径不一致
		groups = filterGroupsBySize(groups, q.MinSize, q.MaxSize)
		groups = filterGroupsByQuery(groups, q)
		truncated := false
		if maxGroups > 0 && len(groups) > maxGroups {
			groups = groups[:maxGroups]
			truncated = true
		}
		return groups, truncated, nil
	}
	cfg := s.cfg.Get()
	if q.MinSize <= 0 {
		q.MinSize = cfg.MinSize
	}
	// v3.10.10（第七轮 P1-4）：原实现每 200 组跑一次分组查询（每次还带 COUNT），
	// 3000 组要跑 30 次全表聚合。改为一次取 maxGroups+1 组：
	// 多取的一组只用于判断是否被截断，同时跳过 COUNT（导出不需要总数）。
	q.Page = 1
	q.MaxGroups = maxGroups + 1
	q.PageSize = maxGroups + 1
	q.SkipTotal = true
	var groups []store.Group
	var err error
	if mode == "name" {
		groups, _, err = s.st.NameGroups(ctx, q)
	} else {
		groups, _, err = s.st.ExactGroups(ctx, q)
	}
	if err != nil {
		return nil, false, err
	}
	truncated := false
	if maxGroups > 0 && len(groups) > maxGroups {
		groups = groups[:maxGroups]
		truncated = true
	}
	return groups, truncated, nil
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
		return
	}
	var body struct {
		Mode    string   `json:"mode"`
		Marked  []string `json:"marked"`
		MinSize int64    `json:"min_size"`
		MaxSize int64    `json:"max_size"`
		Ext     string   `json:"ext"`
		Exts    []string `json:"exts"`
		Keyword string   `json:"q"`
		Since   int64    `json:"since"`
		Until   int64    `json:"until"`
		N       int      `json:"n"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, errorResponse{Error: friendlyDecodeError(err)})
		return
	}
	if body.Mode == "" {
		body.Mode = "exact"
	}
	q := store.GroupQuery{
		MinSize: body.MinSize,
		MaxSize: body.MaxSize,
		Ext:     body.Ext,
		Exts:    body.Exts,
		Keyword: body.Keyword,
		Since:   body.Since,
		Until:   body.Until,
	}
	groups, truncated, err := s.collectGroups(r.Context(), body.Mode, q, body.N, reportMaxGroups)
	if err != nil {
		writeCollectError(w, err)
		return
	}
	marked := map[string]bool{}
	for _, p := range body.Marked {
		marked[filepath.Clean(p)] = true
	}
	// v3.10.11（第七轮 P2-6）：报告改为边生成边写响应，不再用 strings.Builder
	// 攒完整页面。以前缩略图 base64 + Builder 扩容叠加，单次导出峰值可达 80–120MB。
	name := fmt.Sprintf("verify-report-%s-%s.html", body.Mode, time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	s.buildReportHTML(w, groups, marked, body.Mode, body.N, truncated)
}

func (s *Server) buildReportHTML(out io.Writer, groups []store.Group, marked map[string]bool, mode string, n int, truncated bool) {
	modeLabel := map[string]string{"exact": "精确重复", "name": "同名文件", "video": "相似视频", "intro": "相同片头", "samevideo": "相同视频"}[mode]
	if modeLabel == "" {
		modeLabel = mode
	}
	var fileCount, markedCount, waste int64
	var allMarked []int
	for gi, g := range groups {
		fileCount += int64(len(g.Files))
		waste += g.Waste
		all := len(g.Files) > 0
		for _, f := range g.Files {
			if marked[filepath.Clean(f.Path)] {
				markedCount++
			} else {
				all = false
			}
		}
		if all {
			allMarked = append(allMarked, gi+1)
		}
	}
	_, _ = io.WriteString(out, "<!DOCTYPE html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	_, _ = io.WriteString(out, "<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	_, _ = io.WriteString(out, "<title>Verify 查重报告</title><style>")
	_, _ = io.WriteString(out, `body{font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;background:#f4f6f9;color:#1f2937;margin:0;padding:24px}
.wrap{max-width:1280px;margin:0 auto}h1{font-size:24px;margin:0 0 6px}h2{font-size:16px;margin:0 0 10px}
.sub{color:#6b7280;font-size:12px;margin-bottom:18px}.stats{display:flex;gap:12px;flex-wrap:wrap;margin-bottom:18px}
.stat{background:#fff;border:1px solid #e3e8ef;border-radius:10px;padding:10px 14px;min-width:130px}
.stat b{display:block;font-size:20px}.warn{background:#fff5f5;border:1px solid #f3c2c2;color:#a61b1b;padding:10px 14px;border-radius:10px;margin-bottom:16px}
.group{background:#fff;border:1px solid #e3e8ef;border-radius:12px;margin-bottom:16px;overflow:hidden;box-shadow:0 1px 2px rgba(16,24,40,.06)}
.gh{padding:10px 14px;border-bottom:1px solid #e3e8ef;display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.gh b{font-size:15px}.badge{font-size:11px;background:#eef2f6;border:1px solid #e3e8ef;border-radius:99px;padding:1px 7px;color:#6b7280}
table{width:100%;border-collapse:collapse}th,td{padding:8px 10px;border-bottom:1px solid #f0f3f7;font-size:12px;text-align:left;vertical-align:middle}
th{background:#fbfcfe;color:#6b7280;font-weight:500}.thumb{width:96px;height:54px;object-fit:cover;border-radius:5px;background:#dfe5ec}
.ph{width:96px;height:54px;display:grid;place-items:center;background:#eef2f6;border-radius:5px;color:#9aa4b2;font-size:11px}
.path{color:#6b7280;max-width:360px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.marked{color:#a61b1b;font-weight:600}
tr.markedrow{background:#fff8f8}.num{text-align:right;font-variant-numeric:tabular-nums}td.mk{font-weight:600}
.footer{color:#6b7280;font-size:11px;margin-top:18px}@media print{body{background:#fff;padding:0}.group{box-shadow:none;break-inside:avoid}}`)
	_, _ = io.WriteString(out, "</style></head><body><div class=\"wrap\">")
	fmt.Fprintf(out, "<h1>Verify 查重报告 · %s</h1>", html.EscapeString(modeLabel))
	fmt.Fprintf(out, "<div class=\"sub\">生成时间 %s · 来源：飞牛 NAS 查重助手</div>", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(out, "<div class=\"stats\"><div class=\"stat\"><b>%d</b>重复组</div><div class=\"stat\"><b>%d</b>文件</div><div class=\"stat\"><b>%d</b>已标记</div><div class=\"stat\"><b>%s</b>可释放</div></div>",
		len(groups), fileCount, markedCount, humanBytes(waste))
	if truncated {
		// v3.10.8（P2-9）：以前超限静默截断，用户以为报告覆盖全部分组。
		fmt.Fprintf(out, "<div class=\"warn\"><b>注意：报告仅包含前 %d 组</b>，其余分组未导出。请缩小范围（大小/关键字/扩展名）分批导出。</div>", reportMaxGroups)
	}
	if len(allMarked) > 0 {
		var parts []string
		for _, n := range allMarked {
			parts = append(parts, fmt.Sprintf("#%d", n))
		}
		fmt.Fprintf(out, "<div class=\"warn\"><b>危险：以下分组全部文件均被标记：</b>%s。请至少保留一个副本后再执行删除。</div>",
			html.EscapeString(strings.Join(parts, "、")))
	}

	thumbBudget := 240
	// 张数上限挡不住单张大图：240 × 2MB ≈ 480MB，导出大图库会直接把内存打满。
	// 再给一个总原始字节上限，超出后统一退回「无预览」占位。
	thumbBytes := int64(reportThumbByteBudget)
	thumbDeadline := time.Now().Add(45 * time.Second)
	for gi, g := range groups {
		fmt.Fprintf(out, "<section class=\"group\"><div class=\"gh\"><b>#%d %s</b><span class=\"badge\">%d 个文件</span><span class=\"badge\">可释放 %s</span>",
			gi+1, html.EscapeString(g.Name), g.Count, humanBytes(g.Waste))
		if g.Similarity > 0 {
			fmt.Fprintf(out, "<span class=\"badge\">相似度 %.1f%%</span>", g.Similarity*100)
		}
		if mode == "intro" && n > 0 {
			fmt.Fprintf(out, "<span class=\"badge\">前 %d 秒</span>", n)
		}
		_, _ = io.WriteString(out, "</div><table><thead><tr><th>预览</th><th>文件</th><th>路径</th><th class=\"num\">大小</th><th>分辨率</th><th>编码</th><th class=\"num\">时长</th><th>修改时间</th><th>标记</th></tr></thead><tbody>")
		for _, f := range g.Files {
			thumb := s.reportThumb(f, &thumbBudget, &thumbBytes, thumbDeadline)
			thumbCell := "<div class=\"ph\">无预览</div>"
			if thumb != "" {
				thumbCell = "<img class=\"thumb\" src=\"" + thumb + "\" alt=\"\">"
			}
			res := "—"
			if f.Width > 0 && f.Height > 0 {
				res = fmt.Sprintf("%d×%d", f.Width, f.Height)
			}
			codec := f.Codec
			if codec == "" {
				codec = "—"
			}
			dur := "—"
			if f.Duration > 0 {
				dur = fmtDuration(f.Duration)
			}
			isMarked := marked[filepath.Clean(f.Path)]
			rowClass, mkText := "", "未标记"
			if isMarked {
				rowClass, mkText = " class=\"markedrow\"", "已标记"
			}
			fmt.Fprintf(out, "<tr%s><td>%s</td><td>%s</td><td class=\"path\" title=\"%s\">%s</td><td class=\"num\">%s</td><td>%s</td><td>%s</td><td class=\"num\">%s</td><td>%s</td><td class=\"mk\">%s</td></tr>",
				rowClass, thumbCell, html.EscapeString(f.Name), html.EscapeString(f.Path), html.EscapeString(f.Path),
				humanBytes(f.Size), res, html.EscapeString(codec), dur, fmtTime(f.Mtime), mkText)
		}
		_, _ = io.WriteString(out, "</tbody></table></section>")
	}
	_, _ = io.WriteString(out, "<div class=\"footer\">报告由 Verify 生成。标记状态来自结果页选择助手；删除前请人工复核。</div></div></body></html>")
}

// reportThumbByteBudget 是整份报告内嵌缩略图允许占用的原始字节总量（base64 后约 4/3）。
// v3.10.11（第七轮 P2-6）：从 24MB 收到 12MB——报告改为流式输出后，这里只用于控制
// 单个 HTML 文件的体积和前端渲染压力，不再承担「避免内存打满」的职责。
const reportThumbByteBudget = 12 << 20

func (s *Server) reportThumb(f store.FileRow, budget *int, bytesLeft *int64, deadline time.Time) string {
	if *budget <= 0 || *bytesLeft <= 0 || time.Now().After(deadline) {
		return ""
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Name), "."))
	*budget--
	// v3.10.8（P2-8）：报告缩略图同样要过路径校验。thumbBytes 的注释写明
	// 「调用方负责路径合法性」，而这里原先直接 Stat/ReadFile，绕过了 /api/raw、/api/thumb
	// 共用的三步防线；分组数据虽来自本库，但路径可被改名/手工写库污染。
	// 单测里 Server 常无引擎（只验渲染逻辑），此时跳过校验。
	if s.e != nil && !s.e.ValidPath(f.Path) {
		return ""
	}
	if reportImageExts[ext] {
		fi, err := os.Stat(f.Path)
		if err != nil || fi.Size() > 2*1024*1024 || fi.Size() > *bytesLeft {
			return ""
		}
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return ""
		}
		*bytesLeft -= int64(len(data))
		ctype := mime.TypeByExtension("." + ext)
		if ctype == "" {
			ctype = http.DetectContentType(data)
		}
		return "data:" + ctype + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	if !reportVideoExts[ext] {
		return ""
	}
	b, err := s.thumbBytes(context.Background(), f.Path, 1)
	if err != nil || int64(len(b)) > *bytesLeft {
		return ""
	}
	*bytesLeft -= int64(len(b))
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
}

func humanBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func fmtDuration(sec float64) string {
	d := int(sec)
	return fmt.Sprintf("%d:%02d:%02d", d/3600, (d/60)%60, d%60)
}

func fmtTime(ns int64) string {
	if ns <= 0 {
		return "—"
	}
	return time.Unix(0, ns).Format("2006-01-02 15:04")
}
