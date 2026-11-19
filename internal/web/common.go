// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 本文件是 v3.0.0 从 server.go 抽出的跨 handler 通用 helper（设计文档 §1.3）。
// 只搬位置不改签名：请求参数解析、目录范围过滤、分组查询构造、缩略图与缓存、
// JSON 响应等被多个 handler 复用的逻辑集中在这里；具体 handler 仍留在 server.go，
// 方便在一个文件里通读整张路由表。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"verify/internal/applog"
	"verify/internal/config"
	"verify/internal/store"
	"verify/internal/volpath"
)

// blockSizeUint64 把 Statfs 的块大小收敛为非负值。
// Bsize 是 int64，直接转 uint64 遇到异常负值会回绕成极大数，导致容量显示错乱。
func blockSizeUint64(v int64) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v)
}

func intParam(r *http.Request, name string, def int64, min, max int64) int64 {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	return n
}

// filterGroupsByRoots 把视频/片头分组裁剪到当前扫描目录内，只剩至少 2 个文件的组。
func filterGroupsByRoots(groups []store.Group, roots []string) []store.Group {
	roots = cleanRootList(roots)
	if len(roots) == 0 {
		return groups
	}
	out := make([]store.Group, 0, len(groups))
	for _, g := range groups {
		files := make([]store.FileRow, 0, len(g.Files))
		for _, f := range g.Files {
			if pathUnderRoots(f.Path, roots) {
				files = append(files, f)
			}
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

// cleanRootList 规范化扫描根 / 排除目录列表。
// v3.10.9：与保护目录合并为同一份实现（config.NormalizePaths），
// 两套规则不一致时，`/vol1/a/../b` 这类手输路径会在其中一侧静默失效。
func cleanRootList(roots []string) []string {
	return config.NormalizePaths(roots)
}

func pathUnderRoots(path string, roots []string) bool {
	for _, r := range roots {
		if path == r || strings.HasPrefix(path, r+"/") {
			return true
		}
	}
	return false
}

// filterVideoFailuresByRoots 让失败列表也遵守扫描目录范围，避免展示未选中目录的旧记录。
func filterVideoFailuresByRoots(items []store.VideoFailure, roots []string) []store.VideoFailure {
	roots = cleanRootList(roots)
	if len(roots) == 0 {
		return items
	}
	out := make([]store.VideoFailure, 0, len(items))
	for _, it := range items {
		if pathUnderRoots(it.Path, roots) {
			out = append(out, it)
		}
	}
	return out
}

func groupQuery(r *http.Request, cfg *config.Config) store.GroupQuery {
	// v3.26.0（F2）：上限从 200 放宽到 1000，万组级库十次请求即可拉完；
	// 默认值仍是 20，旧调用方无感。
	pageSize := int(intParam(r, "page_size", 20, 1, 1000))
	page := int(intParam(r, "page", 1, 1, 100000))
	minSize := intParam(r, "min_size", cfg.MinSize, 0, 1<<50)
	maxKB := int64(intParam(r, "max_kb", 0, 0, 1<<40))
	// 时间参数用毫秒（JS Date.getTime 原生单位），入库转纳秒与 files.mtime 对齐
	sinceMs := int64(intParam(r, "since_ms", 0, 0, 1<<40))
	untilMs := int64(intParam(r, "until_ms", 0, 0, 1<<40))
	var exts []string
	for _, e := range strings.Split(r.URL.Query().Get("exts"), ",") {
		if e = strings.TrimSpace(e); e != "" {
			exts = append(exts, e)
		}
	}
	sortKey, order := sortParam(r)
	// v2.4.0 高级过滤参数：
	dateOnly := boolParam(r, "date_only", false)
	sameDrive := strings.TrimSpace(r.URL.Query().Get("same_drive"))
	maxDurDiff := float64(intParam(r, "max_dur_diff", 0, 0, 36000)) // 单位秒，上限 10 小时
	ignoreSameFolder := boolParam(r, "ignore_same_folder", false)
	// v2.8.0 高级过滤补充参数
	folderName := strings.TrimSpace(r.URL.Query().Get("folder_name"))
	folderDepth := intParam(r, "folder_depth", 0, 0, 50)
	similarName := boolParam(r, "similar_name", false)
	sizeTolKB := intParam(r, "size_tol_kb", 0, 0, 1048576) // 上限 1GB 容差
	// v2.9.0 标签过滤参数
	tagName := strings.TrimSpace(r.URL.Query().Get("tag_name"))
	tagValue := strings.TrimSpace(r.URL.Query().Get("tag_value"))
	return store.GroupQuery{
		MinSize:  minSize,
		MaxSize:  maxKB * 1024,
		Ext:      strings.TrimSpace(r.URL.Query().Get("ext")),
		Exts:     exts,
		Keyword:  strings.TrimSpace(r.URL.Query().Get("q")),
		Since:    sinceMs * int64(time.Millisecond),
		Until:    untilMs * int64(time.Millisecond),
		Sort:     sortKey,
		Order:    order,
		Roots:    cfg.Volumes,
		Page:     page,
		PageSize: pageSize,
		// v4.1.1：交互页上限已放宽到 1000，这里同步告诉 store 放宽单页 cap，
		// 否则 q.limit() 仍按默认 200 截断，page_size=1000 只是回显、实际只给 200 条。
		// 导出/报告路径会在后面把 MaxGroups 覆盖成更大值（reportMaxGroups+1）。
		MaxGroups:        1000,
		DateOnly:         dateOnly,
		SameDrive:        sameDrive,
		MaxDurDiff:       maxDurDiff,
		IgnoreSameFolder: ignoreSameFolder,
		FolderName:       folderName,
		FolderDepth:      int(folderDepth),
		SimilarName:      similarName,
		SizeTolKB:        int(sizeTolKB),
		TagName:          tagName,
		TagValue:         tagValue,
		// v3.10.10（第七轮 P1-2）：翻页时前端带 skip_total=1，第二页起不再跑 COUNT。
		SkipTotal: boolParam(r, "skip_total", false),
	}
}

// volumePrefix 提取路径所在卷前缀（/vol5/1000/x → /vol5；非 /vol* 返回空）。
// v3.17.3（F-02）：卷根判定统一走 volpath，避免这里与配置校验的口径分叉。
func volumePrefix(p string) string {
	clean := filepath.Clean(p)
	if !volpath.IsVolRoot(clean) {
		return ""
	}
	i := strings.IndexByte(clean[len("/vol"):], '/') // volN 后第一个 /
	if i < 0 {
		return clean
	}
	return clean[:len("/vol")+i]
}

// localDayStart 把纳秒时间戳截到「本地时区的当天 0 点」。
// v3.17.4（F-10）：与 SQL 侧 date(mtime/1e9,'unixepoch','localtime') 保持同一口径；
// 用 Truncate(24h) 截的是 UTC 日，在 UTC+8 会把本地 0–8 点的文件算到前一天。
func localDayStart(ns int64) time.Time {
	t := time.Unix(ns/1e9, 0).In(time.Local)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

// checkToolPath 校验用户填写的 ffmpeg / ffprobe 路径（v3.17.4 F-15）。
// 只接受「可执行的普通文件」，且不能落在扫描卷内：卷内文件随时可能被改名、
// 移入隔离区或被替换成任意内容，把外部程序路径指向数据盘等于把执行权交出去。
// 留空表示自动探测，合法。
func checkToolPath(field, p string, volumes []string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return field + " 路径不存在或不是普通文件：" + p
	}
	if st.Mode().Perm()&0o111 == 0 {
		return field + " 没有执行权限：" + p
	}
	clean := filepath.Clean(p)
	for _, vol := range volumes {
		vol = strings.TrimRight(filepath.Clean(strings.TrimSpace(vol)), "/")
		if vol == "" {
			continue
		}
		if clean == vol || strings.HasPrefix(clean, vol+"/") {
			return field + " 不能位于扫描卷内：" + p
		}
	}
	return ""
}

// validateTokenStrength 校验访问令牌强度（v3.17.5 F-18）。
// 非空时至少 16 位；留空表示关闭鉴权，由前端二次确认。
func validateTokenStrength(v string) string {
	if v != "" && len(v) < 16 {
		return "访问令牌至少 16 位"
	}
	return ""
}

// thumbPruneInterval 是清理节流间隔：避免每次落盘都全目录扫描。
const thumbPruneInterval = time.Minute

// thumbCachePrune 按文件 mtime 从旧到新删除缓存文件，直到总大小 ≤ maxBytes。
// 只删 cacheDir 下的 .jpg（缩略图文件名均为 SHA-256 十六进制 + .jpg，无子目录）。
func thumbCachePrune(dir string, maxBytes int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type cacheFile struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []cacheFile
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jpg") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, cacheFile{filepath.Join(dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= maxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if os.Remove(f.path) == nil { // #nosec G703 -- 路径由受管 cacheDir 拼接
			total -= f.size
		}
	}
}

// thumbLuma 估算缩略图的平均亮度（0–255）并判断是否接近全黑。
// 抽样 4 像素一次，320x180 的图只要几毫秒；解不开就当不黑，避免误触发重截。
func thumbLuma(b []byte) (int, bool) {
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return 255, false
	}
	bounds := img.Bounds()
	// 用 float64 累加：像素亮度有上限（255），不会溢出，也不用做窄化转换
	var sum, n float64
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 4 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			l := (299*float64(r>>8) + 587*float64(g>>8) + 114*float64(bl>>8)) / 1000
			sum += l
			n++
		}
	}
	if n == 0 {
		return 255, false
	}
	avg := sum / n
	return int(avg), avg < 24
}

// thumbBytes 复用缩略图缓存；报告与网页预览共用。调用方负责路径合法性。
// ctx 用于等待 ffmpeg 名额与解码超时（v3.25.1：缩略图纳入统一资源闸门）。
func (s *Server) thumbBytes(ctx context.Context, p string, at float64) ([]byte, error) {
	fi, err := os.Stat(p) // #nosec G703 -- 调用方已用 Engine.ValidPath 校验（Clean+EvalSymlinks+卷前缀重判，v1.13.1）
	if err != nil {
		return nil, err
	}
	// v2：缩略图生成参数变了（排除封面、10bit 转 yuv420p、黑场换时间点），
	// 加版本前缀让旧缓存失效，否则已缓存的黑图会一直显示下去。
	sum := sha256.Sum256([]byte(fmt.Sprintf("v2|%s|%d|%d|%.0f", p, fi.Size(), fi.ModTime().UnixNano(), at)))
	dir := s.ensureCacheDir()
	cachePath := filepath.Join(dir, hex.EncodeToString(sum[:])+".jpg")
	// cachePath 由哈希值拼成，不含外部输入的路径片段。
	if b, ok := s.thumbs.Get(cachePath); ok {
		return b, nil
	}
	b, err := os.ReadFile(cachePath) // #nosec G304 G703 -- 文件名是 SHA-256 十六进制串，目录来自配置
	if err != nil {
		// 损坏自检（v1.31）：文件存在但读不出来 → 删除失效条目，走重新生成
		if !os.IsNotExist(err) {
			_ = os.Remove(cachePath) // #nosec G703 -- 同上，cachePath 由受管缓存目录与哈希文件名拼成
		}
	} else {
		// 磁盘命中：touch mtime 保持活跃条目不被 LRU 淘汰（审查 D-2）
		now := time.Now()
		_ = os.Chtimes(cachePath, now, now)
		s.thumbs.Put(cachePath, b)
		return b, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// v3.25.1：与转码/视频分析/健康探测共用 heavySem 与 ffmpegGate。
	// 名额等待单独限时 5 秒：缩略图是次要资源，转码占满槽位时不能把浏览器的
	// 同源连接池一起拖住（6 个连接都被挂住，整个页面看起来就像没响应）。
	gateCtx, gateCancel := context.WithTimeout(ctx, 5*time.Second)
	release, ok := s.e.ThumbGate(gateCtx)
	gateCancel()
	if !ok {
		return nil, fmt.Errorf("服务器正忙，缩略图稍后重试")
	}
	defer release()
	b, err = s.e.RunnerSnapshot().Thumbnail(ctx, p, at, 320)
	if err != nil {
		return nil, err
	}
	// 片头是黑场或长淡入时，第 1 秒截出来几乎全黑，缩略图等于没有。
	// 只对默认时间点补救：先试第 10 秒，短片整段黑再从结尾倒着取，取更亮的那张。
	if at <= 1 {
		if l, dark := thumbLuma(b); dark {
			best, bestL := b, l
			for _, cand := range []func() ([]byte, error){
				func() ([]byte, error) { return s.e.RunnerSnapshot().Thumbnail(ctx, p, 10, 320) },
				func() ([]byte, error) { return s.e.RunnerSnapshot().ThumbnailFromEnd(ctx, p, 10, 320) },
			} {
				b2, e2 := cand()
				if e2 != nil {
					continue
				}
				if l2, dark2 := thumbLuma(b2); !dark2 && l2 > bestL {
					best, bestL = b2, l2
				}
			}
			b = best
		}
	}
	// 缩略图属中间产物，只需应用自身可读写。
	_ = os.WriteFile(cachePath, b, 0o600) // #nosec G703 -- cachePath 由 SHA-256 十六进制串与配置目录拼成
	s.thumbs.Put(cachePath, b)
	// 落盘后按容量上限淘汰最旧条目（审查 D-2）；节流避免频繁全目录扫描
	s.thumbPruneMu.Lock()
	if time.Since(s.thumbLastPrune) >= thumbPruneInterval {
		s.thumbLastPrune = time.Now()
		thumbCachePrune(dir, s.thumbDiskMaxBytes())
	}
	s.thumbPruneMu.Unlock()
	return b, nil
}

func gucParam() string {
	b, err := os.ReadFile("/sys/module/i915/parameters/enable_guc")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ensureWritableDir 创建目录并实际写入一个临时文件，确认路径可用于日志/缓存。
func ensureWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".verify-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

// requireGET 给只读接口统一做方法校验（v3.10.10）。
// 此前 /api/status、/api/health/status、/api/health/results 不判方法，
// POST/DELETE 也会执行读逻辑，与全站「显式 405」的口径不一致。
func requireGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "请求方法不支持"})
	return false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	// v3.10.8（SEC-8）：失败响应统一收敛错误文案，避免把 errno / SQLite 原文
	// 直接回显给浏览器。收敛只发生在 HTTP 出口，日志里仍是完整原文。
	if code >= 400 {
		v = sanitizeErrorPayload(code, v)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// errTextHints 把常见底层英文错误映射成用户能看懂的中文摘要。
// 顺序有意义：更具体的键必须排在更宽泛的键前面（table is locked 先于 is locked）。
var errTextHints = []struct{ key, text string }{
	{"database table is locked", "数据库表正忙，请稍后重试"},
	{"database is locked", "数据库正忙，请稍后重试"},
	{"unique constraint failed", "数据已存在，不能重复写入"},
	{"constraint failed", "数据不满足约束，请检查输入"},
	{"no such table", "数据表缺失，请升级应用或重新初始化数据"},
	{"sql logic error", "数据库操作失败"},
	{"no such column", "数据表结构过期，请升级应用"},
	{"permission denied", "没有权限访问该文件或目录"},
	{"operation not permitted", "操作被系统拒绝"},
	{"no such file or directory", "文件或目录已不存在"},
	{"not a directory", "路径不是目录"},
	{"is a directory", "目标是一个目录"},
	{"directory not empty", "目录不为空，无法删除"},
	{"file exists", "目标已存在"},
	{"invalid cross-device link", "跨文件系统移动需要先复制再删除"},
	{"no space left on device", "磁盘空间不足"},
	{"disk quota exceeded", "磁盘配额不足"},
	{"read-only file system", "文件系统为只读，无法写入"},
	{"input/output error", "磁盘读写出错，请检查存储设备"},
	{"too many open files", "打开的文件过多，请稍后重试"},
	{"filename too long", "文件名或路径过长"},
	{"argument list too long", "参数或名称过长"},
	{"cannot allocate memory", "内存不足，请稍后重试"},
	{"broken pipe", "连接已断开"},
	{"connection reset by peer", "连接被对端重置"},
	{"context deadline exceeded", "操作超时，请稍后重试"},
	{"context canceled", "操作已取消"},
	{"invalid character", "请求内容不是合法的 JSON"},
	{"unexpected end of json input", "请求内容不完整"},
	{"unexpected eof", "请求内容不完整"},
	{"method not allowed", "不支持的请求方法"},
}

// publicErrorText 保留中文描述，把底层的英文 errno / SQLite 原文换成中文摘要。
// 整段都是英文且无法归类时返回通用提示：这类内容对用户没有可操作价值，
// 同时又可能暴露目录布局或数据库结构，只应留在 verify.log。
func publicErrorText(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "操作失败，请查看服务端日志"
	}
	lower := strings.ToLower(msg)
	for _, h := range errTextHints {
		idx := strings.Index(lower, h.key)
		if idx < 0 {
			continue
		}
		if prefix := errPrefix(msg[:idx]); prefix != "" {
			return prefix + h.text
		}
		return h.text
	}
	if hasRawErrDetail(msg) {
		return "操作失败，请查看服务端日志"
	}
	return msg
}

// errPrefix 取错误原文中英文出现之前的中文前缀（如「移入隔离区失败：」），
// 去掉尾部分隔符；前缀里没有中文时返回空串，避免拼出半截英文。
func errPrefix(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, " \t（(:：,，-—")
	if s == "" || !strings.ContainsFunc(s, func(r rune) bool { return r >= 0x4E00 && r <= 0x9FFF }) {
		return ""
	}
	return s + "："
}

// hasRawErrDetail 判断一条错误是否属于「不应回显的内部细节」：
// 纯英文内容、Go 运行时/SQLite 标识、源码位置等。中文提示一律放行。
func hasRawErrDetail(msg string) bool {
	lower := strings.ToLower(msg)
	for _, mark := range []string{".go:", "goroutine ", "sqlite", "modernc.org", "runtime error", "panic:", "errno "} {
		if strings.Contains(lower, mark) {
			return true
		}
	}
	return !strings.ContainsFunc(msg, func(r rune) bool { return r >= 0x4E00 && r <= 0x9FFF })
}

// sanitizeErrorPayload 只改写 {"error": "..."} 形态的失败响应，成功响应原样返回；
// 其它字段（如扫描冲突时的 n）保持不变，前端逻辑不受影响。
//
// v4.0.0：响应体逐步从 map[string]any 换成具名 struct 后，这里要同时认两种形态。
// 做法是让带 error 字段的响应体实现 errorPayload 接口，
// 收敛时复制一份并替换 Error 字段——原对象不被修改（避免误改调用方持有的结构）。
func sanitizeErrorPayload(code int, v any) any {
	// 形态一：具名 struct（v4.0.0 起的首选）
	if p, ok := v.(errorPayload); ok {
		raw := p.errorText()
		if raw == "" {
			return v
		}
		clean := publicErrorText(raw)
		if clean == raw {
			return v
		}
		if code >= 500 {
			// 原文只在服务端留痕，方便按时间对照前端看到的摘要
			applog.Warn("接口错误已收敛（%d）：%s", code, raw)
		}
		cp := p.withError(clean)
		return cp
	}
	// 形态二：map[string]any（尚未改造的接口）
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	raw, ok := m["error"].(string)
	if !ok || raw == "" {
		return v
	}
	clean := publicErrorText(raw)
	if clean == raw {
		return v
	}
	if code >= 500 {
		// 原文只在服务端留痕，方便按时间对照前端看到的摘要
		applog.Warn("接口错误已收敛（%d）：%s", code, raw)
	}
	cp := make(map[string]any, len(m))
	for k, val := range m {
		cp[k] = val
	}
	cp["error"] = clean
	return cp
}
