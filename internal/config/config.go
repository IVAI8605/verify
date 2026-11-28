// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"verify/internal/fname"
	"verify/internal/health"
	"verify/internal/perm"
)

// MatchMode 取值（v2.1.0）。默认 all 表示沿用 v1.x 的多依据并存行为。
const (
	MatchModeAll           = "all"
	MatchModeExact         = "exact"
	MatchModeSimilar       = "similar"
	MatchModeAudio         = "audio"
	MatchModeIgnoreContent = "ignore_content"
	// MatchModeThumbs 仅缩略图（v2.8.0）：只按开头画面 dHash 分组，不看音频。
	MatchModeThumbs = "thumbs"
	// MatchModeCustom 自定义组合（v3.6.0）：结果页开哪些类型由 MatchKinds 决定，
	// 用于 DC5 那种“勾选多种条件”的用法，扫描期仍照常计算全部特征。
	MatchModeCustom = "custom"
)

// MatchKind 取值：与结果页类型一一对应，供自定义组合使用。
const (
	MatchKindExact    = "exact"
	MatchKindName     = "name"
	MatchKindVideo    = "video"
	MatchKindIntro    = "intro"
	MatchKindAudio    = "audio"
	MatchKindPhoto    = "photo"
	MatchKindFolder   = "folder"
	MatchKindFuzzy    = "fuzzy"
	MatchKindSemantic = "semantic"
)

// MatchKindAll 是自定义组合可选的类型顺序（前端按此顺序渲染勾选项）。
var MatchKindAll = []string{
	MatchKindExact, MatchKindName, MatchKindVideo, MatchKindIntro, MatchKindAudio,
	MatchKindPhoto, MatchKindFolder, MatchKindFuzzy, MatchKindSemantic,
}

// NormalizeMatchKinds 只保留受支持的类型并去重，未知值直接丢弃；
// 空表示什么都不显示，由调用方决定是否回退到预设。
func NormalizeMatchKinds(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	valid := map[string]bool{}
	for _, k := range MatchKindAll {
		valid[k] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, k := range in {
		k = strings.TrimSpace(k)
		if !valid[k] || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// NormalizeMatchMode 把空值/未知值归一为 all，旧 config.json 与手写请求都不会打开不存在的模式。
func NormalizeMatchMode(mode string) string {
	switch v := strings.TrimSpace(mode); v {
	case MatchModeExact, MatchModeSimilar, MatchModeAudio, MatchModeIgnoreContent, MatchModeThumbs, MatchModeCustom:
		return v
	default:
		return MatchModeAll
	}
}

// MatchMode 是本次查重的匹配模式。它只决定分组/结果页展示用哪套算法，
// 不改变扫描期已算好的特征，因此切换模式不需要重扫。
type Config struct {
	// 监听地址，默认 :18898（v4.4.0 起避开 8898 等常用端口）
	Addr string `json:"addr"`
	// 数据目录（数据库、日志、配置）
	DataDir string `json:"data_dir"`
	// 需要扫描的卷，留空则自动探测 /vol*
	Volumes []string `json:"volumes"`
	// 扫描时跳过的目录名
	ExcludeDirs []string `json:"exclude_dirs"`
	// 只对大于等于该字节数的文件建立查重索引
	MinSize int64 `json:"min_size"`
	// 是否开启实时监控
	Watch bool `json:"watch"`
	// 是否启用内容精确查重
	ContentEnabled bool `json:"content_enabled"`
	// 是否启用同名文件分组
	NameEnabled bool `json:"name_enabled"`
	// ComputeDupFolders 是否计算「重复文件夹」页（v2.0.0，默认开）。
	// 只影响该页的查询开销，不影响扫描；旧 config.json 缺字段时保持开启。
	ComputeDupFolders bool `json:"compute_dup_folders"`
	// MatchMode 匹配模式（v2.1.0）：all（默认）/ exact / similar / audio / ignore_content；
	// v2.8.0 增加 thumbs（仅缩略图）；v3.6.0 增加 custom（自定义组合，配合 match_kinds）。
	MatchMode string `json:"match_mode"`
	// MatchKinds 仅 match_mode=custom 时生效：勾选的结果类型（exact/name/video/intro/
	// audio/photo/folder/fuzzy/semantic）。扫描期仍算全部特征，只决定结果页开哪些。
	MatchKinds []string `json:"match_kinds"`
	// SearchRules v3.8.0：DC5 式搜索规则组合。为 nil 表示旧配置，继续沿用
	// match_mode / match_kinds 的旧行为；由新建查重或设置页保存后写入。
	SearchRules *SearchRules `json:"search_rules,omitempty"`
	// SearchMemLimitMB 搜索结果快照的内存上限（MB）。0=自动（系统内存 25%，256–4096）。
	// 超限时搜索阶段直接报错，不做静默截断（设计文档 D1）。
	SearchMemLimitMB int `json:"search_mem_limit_mb"`
	// SearchPresets v3.9.0：可复用的搜索规则预设，存 config.json，不建新表。
	SearchPresets []SearchPreset `json:"search_presets,omitempty"`
	// SearchTokenEnabled 全选凭据模式（v3.20.0）：开启后「全选全部结果」只记查询条件与
	// 排除项，不把几十万条路径搬到前端；关闭后退回「只选已加载页」。
	SearchTokenEnabled bool `json:"search_token_enabled"`
	// RenameUndoMinutes 改名撤销总预算（分钟，v3.22.0）。默认 240；
	// 40 万条分片改名后逐批撤销需要比 30 分钟更长的窗口，到点仍会停止并保留可重试状态。
	RenameUndoMinutes int `json:"rename_undo_minutes"`
	// TraditionalEnabled 繁体文件名检测（v1.27.0，默认开）。
	TraditionalEnabled bool `json:"traditional_enabled"`
	// 是否启用视频相似检测
	VideoEnabled bool `json:"video_enabled"`
	// 是否启用“相同片头”检测（独立于整段视频查重）
	IntroEnabled bool `json:"intro_enabled"`
	// 需要排除的绝对目录；目录本身及其子目录都不会被扫描
	ExcludePaths []string `json:"exclude_paths"`
	// ProtectPaths 是"扫但禁删"的保护目录：文件照常进查重与展示，
	// 但移入隔离区、永久删除一律拦截。与排除（直接不扫）语义不同。
	ProtectPaths []string `json:"protect_paths"`
	// BrowseRoots v3.5.3：用户手动粘贴并验证通过的授权目录。
	// 飞牛只授权共享下的某个子目录时，共享根不可读、无法从 /fs 枚举出子目录，
	// 这里记住一次粘贴结果，之后自动作为目录选择器的根显示（只存已验证路径）。
	BrowseRoots []string `json:"browse_roots"`
	// PathMaps 是「NAS 内部路径 → 外部设备路径」映射，仅 DC5 格式导出时应用，
	// 让导出的 CSV 在 Windows 的 Duplicate Cleaner 里能定位到 SMB 共享上的真实文件。
	PathMaps PathMaps `json:"path_maps"`
	// PhotoEnabled 开启相似图片查重（图片指纹为重 IO，图片库很大时可关）。
	PhotoEnabled bool `json:"photo_enabled"`
	// ImgHamming 是相似图片判定的汉明距离阈值（0–64，越小越严格）。
	ImgHamming int `json:"img_hamming"`
	// AudioEnabled 开启纯音频查重（每文件一次 ffmpeg 进程，曲库很大时可关）。
	AudioEnabled bool `json:"audio_enabled"`
	// AudioWindowSeconds 纯音频指纹比较时长（v2.3.0）：15 / 120（秒），0=全片。
	// 旧 config.json 缺字段时默认 15 秒；改档会让旧指纹失效并重算。
	AudioWindowSeconds int `json:"audio_window_seconds"`
	// UnifyExt 同族扩展名归一开关（10.7）：off 不归一（默认）；jpg 统一为 .jpg；
	// jpeg 统一为 .jpeg。仅影响「改名安全」的族，是偏好不是错误。
	UnifyExt string `json:"unify_ext"`
	// 片头比较长度（秒），允许 1–300
	IntroSeconds int `json:"intro_seconds"`
	// TaskGuardEnabled 全局任务闸门（v3.18.0）：长任务启动前声明资源，
	// 独占组冲突时拒绝启动并说明占用者；关闭后回到 v3.15 的并发行为。
	TaskGuardEnabled bool `json:"task_guard_enabled"`
	// 视频分析并发进程数（1–4），兼顾核显解码能力
	VideoWorkers int `json:"video_workers"`
	// 相似视频抽帧时忽略的片头秒数，用于避开共享片头
	IgnoreIntroSeconds int `json:"ignore_intro_seconds"`
	// 相似视频抽帧时忽略的片尾秒数
	IgnoreOutroSeconds int `json:"ignore_outro_seconds"`
	// 是否优先使用核显（QSV）解码抽帧，失败会自动回退软解
	HardwareDecode bool `json:"hardware_decode"`
	// 音频指纹相似度阈值
	AudioThreshold float64 `json:"audio_threshold"`
	// 缩略图 dHash 最大汉明距离
	PHashHamming int `json:"phash_hamming"`
	// 访问令牌，为空则不需要登录；仅用于本地 config.json 落盘，不会写入日志或响应体
	AccessToken string `json:"access_token"`
	// ffmpeg / ffprobe 路径，留空自动探测
	FFmpeg  string `json:"ffmpeg"`
	FFprobe string `json:"ffprobe"`
	// 定时全量重扫的小时数，0 表示关闭
	FullRescanHours int `json:"full_rescan_hours"`
	// 监控不可用时的兜底轮询间隔（分钟），0 关闭
	PollMinutes int `json:"poll_minutes"`
	// 扫描并发哈希协程数
	HashWorkers int `json:"hash_workers"`
	// 日志目录，留空使用数据目录下的 logs
	LogDir string `json:"log_dir"`
	// v3.7.0 操作记录（审计）配置。删除类审计不受 OpLogEnabled 关闭影响，见 internal/engine/trash.go。
	OpLogEnabled       bool `json:"op_log_enabled"`
	OpLogRetentionDays int  `json:"op_log_retention_days"` // 0 表示永久保留
	OpLogReads         bool `json:"op_log_reads"`          // 是否记录只读 GET 请求（排除名单仍生效）
	OpLogDetailLimit   int  `json:"op_log_detail_limit"`   // 单次操作明细条数上限
	// 缩略图缓存目录，留空使用数据目录下的 cache
	CacheDir string `json:"cache_dir"`
	// 休眠友好：开启后不做任何"主动扫描"类的空转 I/O
	// （轮询触发的全量扫描、定时全量重扫都停掉），文件监控仍靠内核事件驱动。
	SleepFriendly bool `json:"sleep_friendly"`
	// 静默时段，格式 "23:00-07:00"，留空表示不启用。
	// 区间内后台自动任务一律不启动，用户手动触发的任务不受影响。
	QuietHours string `json:"quiet_hours"`
	// 闲时自动巡检损坏视频。默认关闭：首次全量要跑数小时，
	// 升级后突然启动长时间任务不是好体验。
	BrokenAuto bool `json:"broken_auto"`
	// 判定"闲着"所需的静默分钟数：这段时间内没有收到任何 API 请求。
	BrokenAutoIdleMinutes int `json:"broken_auto_idle_minutes"`
	// 每轮巡检最多探测的文件数，跑完即退出，避免长时间占用磁盘。
	BrokenAutoBatch int `json:"broken_auto_batch"`
	// WakeEnabled 每日定时唤醒（v1.32，默认关）：授权后在时段内唤醒磁盘跑
	// "数据保鲜"任务（增量对账/指纹·缩略图预计算/健康巡检）。
	// 红线修正：休眠友好 = 默认不唤醒、授权可定时唤醒。
	WakeEnabled bool `json:"wake_enabled"`
	// WakeWindow 授权唤醒时段，格式 "HH:MM-HH:MM"，默认 "04:00-08:00"。
	// 时段结束强制停止保鲜任务，放手让睡。
	WakeWindow string `json:"wake_window"`
	// TempPatterns 临时文件清单（v1.34 F1，健康页第五类检测）。
	// 以 '.' 开头按扩展名后缀匹配，"~" 匹配编辑器备份，其余按文件名精确匹配。
	// 清空 = 关闭该检测。默认清单见 health.DefaultTempExts。
	TempPatterns []string `json:"temp_patterns"`
	// VideoDensity 视频帧密度档位（v1.38 F2）："low"=3 帧、"medium"=6 帧（默认）、
	// "high"=10 帧；全片均匀分散。密度越高检出越强、首扫越慢。
	VideoDensity string `json:"video_density"`
	// NoiseWords 语义噪声词典（v1.36 F7，设置页可编辑）：
	// 分辨率/编码/片源/语言等噪声词，剥离后提取语义核心键辅助分组。
	NoiseWords []string `json:"noise_words"`
	// MemBudgetMB 内存缓存预算（MB）。0=按系统总内存自动检测（v1.31 §1.3 公式），
	// 用户可手动覆盖；预算只决定"内存缓存尽量用多大"，超限走 LRU 驱逐，不会 OOM。
	MemBudgetMB int `json:"mem_budget_mb"`
	// ThumbCacheMaxMB 缩略图磁盘缓存上限（MB）。0=不限制；默认 256。
	ThumbCacheMaxMB int `json:"thumb_cache_max_mb"`
	// AIEnabled/AIEndpoint/AIModel 是可选 AI 连接器（v3.3.0，默认关闭）。
	// 只支持 OpenAI 兼容接口（本地 Ollama 填 http://127.0.0.1:11434）。
	// 它只用于给「智能保留建议」生成一句理由，绝不参与保留判定，也不会自动调用。
	AIEnabled  bool   `json:"ai_enabled"`
	AIEndpoint string `json:"ai_endpoint"`
	AIModel    string `json:"ai_model"`
}

// Default 返回默认配置。
func Default(dataDir string) *Config {
	return &Config{
		Addr:                  ":18898",
		DataDir:               dataDir,
		Volumes:               nil,
		ExcludeDirs:           []string{"@eaDir", "#recycle", ".git", "node_modules", ".verify-trash", ".Trash", "System Volume Information"},
		MinSize:               1,
		Watch:                 true,
		ContentEnabled:        true,
		NameEnabled:           true,
		ComputeDupFolders:     true,
		MatchMode:             MatchModeAll,
		TraditionalEnabled:    true,
		SearchTokenEnabled:    true,
		RenameUndoMinutes:     240,
		VideoEnabled:          true,
		IntroEnabled:          false,
		IntroSeconds:          10,
		TaskGuardEnabled:      true,
		VideoWorkers:          2,
		IgnoreIntroSeconds:    0,
		IgnoreOutroSeconds:    0,
		HardwareDecode:        true,
		AudioThreshold:        0.82,
		PHashHamming:          10,
		AccessToken:           "",
		FullRescanHours:       24,
		PollMinutes:           10,
		HashWorkers:           4,
		SleepFriendly:         true,
		QuietHours:            "",
		BrokenAuto:            false,
		BrokenAutoIdleMinutes: 10,
		BrokenAutoBatch:       200,
		WakeEnabled:           false,
		WakeWindow:            "04:00-08:00",
		// v1.42.0：默认清单直接取 health 包的唯一来源。
		// 此前这里另抄了一份字面量，health 补了 .DS_Store 配置侧也不会跟着变（BUG-06）。
		TempPatterns:       append([]string(nil), health.DefaultTempExts...),
		VideoDensity:       "medium",
		NoiseWords:         fname.DefaultNoiseWords,
		MemBudgetMB:        0, // 0=自动检测
		ThumbCacheMaxMB:    256,
		AIModel:            "llama3.2",
		OpLogEnabled:       true,
		OpLogRetentionDays: 180,
		OpLogReads:         false,
		OpLogDetailLimit:   200,
		PhotoEnabled:       true,
		ImgHamming:         10,
		AudioEnabled:       true,
		AudioWindowSeconds: 15,
		UnifyExt:           "off",
	}
}

type Manager struct {
	mu   sync.RWMutex
	path string
	cfg  *Config
}

func Load(dataDir string) (*Manager, error) {
	cfg := Default(dataDir)
	path := filepath.Join(dataDir, "config.json")
	if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- 固定文件名 config.json，目录来自启动参数而非请求
		// 旧版本落盘的 config.json 是 0644 且含 access_token，读取时顺手收紧到 0600。
		perm.Tighten(path, 0o600)
		// 忽略解析错误：使用默认值
		_ = json.Unmarshal(b, cfg)
	}
	// v1.42.0：旧版本落盘的默认清单缺 .DS_Store。只有清单仍与旧默认完全一致
	// 才补这一项；用户手动增删过的清单保持原样，避免每次启动把删掉的项加回来（BUG-06）。
	if isLegacyDefaultTempPatterns(cfg.TempPatterns) {
		cfg.TempPatterns = append(cfg.TempPatterns, ".DS_Store")
	}
	// v3.0.0：字段范围校验与规范化统一走 validateConfig（Load 用宽松模式，
	// 存量配置里的坏映射条目直接丢弃，不因此拒载）。
	_ = validateConfig(cfg, false)
	// data_dir 永远以启动参数为准（v1.23 实测：改名迁移后持久化的旧路径会让
	// 探测类临时文件写进不存在的目录）。该字段不属于用户可配置项。
	cfg.DataDir = dataDir
	return &Manager{path: path, cfg: cfg}, nil
}

// normalizeAudioWindow 只接受 15 / 120 / 0（全片），其余回退 15 秒。
func normalizeAudioWindow(sec int) int {
	switch sec {
	case 0, 15, 120:
		return sec
	default:
		return 15
	}
}

// EffectiveAudioThreshold 长指纹档收紧阈值，降低长音轨误报（v2.3.0）。
func (c *Config) EffectiveAudioThreshold() float64 {
	thr := c.AudioThreshold
	switch c.AudioWindowSeconds {
	case 120:
		thr += 0.04
	case 0:
		thr += 0.06
	}
	if thr > 1 {
		thr = 1
	}
	return math.Round(thr*100) / 100
}

// legacyDefaultTempPatterns 是 v1.42.0 之前的默认临时文件清单。
// 只用于升级时的一次性迁移，不再作为新默认值使用。
var legacyDefaultTempPatterns = []string{
	".tmp", ".bak", ".crdownload", ".part", ".download", ".temp",
	".swp", ".swo", "~", "Thumbs.db", "desktop.ini",
}

// isLegacyDefaultTempPatterns 判断存量清单是否仍是旧默认原样（顺序无关）。
func isLegacyDefaultTempPatterns(in []string) bool {
	if len(in) != len(legacyDefaultTempPatterns) {
		return false
	}
	seen := make(map[string]bool, len(in))
	for _, p := range in {
		seen[strings.TrimSpace(p)] = true
	}
	for _, p := range legacyDefaultTempPatterns {
		if !seen[p] {
			return false
		}
	}
	return true
}

// NormalizePaths 规范化路径列表：去空白、折叠 ./.. 与重复分隔符、去尾部斜杠、去重。
// 排除目录与保护目录共用同一套规则，避免两处行为漂移。
// v3.10.9：补上 path.Clean。此前只做反斜杠替换与去尾斜杠，手输的 `/vol1/a/../b`
// 与扫描产物里 Clean 过的 `/vol1/b/...` 前缀永远匹配不上，保护目录会静默失效。
func NormalizePaths(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		// 统一用字符串替换反斜杠：filepath.ToSlash 在 Linux 上不处理 "\"，
		// 而 fnOS 目标是 Linux；NAS 路径不该出现反斜杠，出现也按分隔符归一。
		// 用 path.Clean 而不是 filepath.Clean：后者在 Windows 上会把 "/" 转成 "\"，
		// 让本地跑测试时的结果与目标平台不一致。
		p := strings.TrimRight(path.Clean(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")), "/")
		if p == "" || p == "." || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// AddBrowseRoot v3.5.3：记住用户手动粘贴且验证通过的授权目录。
// 只追加、去重，不删除已有项；路径需已由调用方完成 /vol* 与 trimacl 校验。
func (m *Manager) AddBrowseRoot(p string) error {
	p = filepath.Clean(p)
	if p == "" || p == "." {
		return nil
	}
	cur := m.Get()
	for _, x := range cur.BrowseRoots {
		if x == p {
			return nil
		}
	}
	cp := *cur
	cp.BrowseRoots = append(append([]string{}, cur.BrowseRoots...), p)
	return m.Replace(&cp)
}

// TokenHintPath 返回「当前访问令牌」提示文件的路径（v3.7.2）。
// 首次启动自动生成令牌时用户还没法登录，需要一条登录前可读的渠道得知令牌。
func TokenHintPath(dataDir string) string {
	return filepath.Join(dataDir, "access-token.txt")
}

// WriteTokenHint 写入或更新令牌提示文件；token 为空时删除该文件。
// 与 config.json 同为 0600，只用于登录前查看；登录后可在设置页更换或清除令牌。
func WriteTokenHint(dataDir, token string) error {
	path := TokenHintPath(dataDir)
	if strings.TrimSpace(token) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	body := "查重助手访问令牌（自动写入，仅供登录前查看）\n" + token +
		"\n\n登录后可在「设置 → 访问令牌」更换或清除；本文件会随之更新，不再需要时可以删除。\n"
	// 提示文件含明文令牌，权限必须仅属主可读写。
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return err
	}
	perm.Tighten(path, 0o600)
	return nil
}

// Get 返回配置快照（浅拷贝，切片需只读使用）。
func (m *Manager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := *m.cfg
	return &cp
}

// Replace 用新配置替换并落盘。
func (m *Manager) Replace(c *Config) error {
	// v3.0.0：与 Load 共用同一套校验；strict 模式下坏的路径映射会拦下整次保存，
	// 避免设置页静默丢映射。
	if err := validateConfig(c, true); err != nil {
		// v3.17.3（F-02）：标记为校验错误，Web 入口据此返回 400 而不是 500。
		return asValidationError(err)
	}
	// v3.10.4：先取路径，落盘成功后再替换内存配置。旧顺序是「先赋值后写盘」，
	// 写盘失败时界面已显示新值、重启又回退，用户会以为保存成功。
	m.mu.RLock()
	path := m.path
	m.mu.RUnlock()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ") // #nosec G117 -- 这是配置对象自身的序列化，access_token 必须随配置落盘才能生效（文件权限已收窄到 0600）
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	// 配置含 access_token，落盘权限必须收窄到仅属主可读写。
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// rename 指向的是刚创建的 0600 临时文件，这里再兜一次，避免极端情况下沿用旧文件权限。
	perm.Tighten(path, 0o600)

	m.mu.Lock()
	m.cfg = c
	m.mu.Unlock()
	// v3.7.2：令牌变化时同步登录前提示文件，避免它一直停留在旧令牌上误导用户。
	// 配置此刻已经落盘，提示文件只是辅助渠道，写失败不能让上层误判为「保存失败」。
	_ = WriteTokenHint(filepath.Dir(path), c.AccessToken)
	return nil
}
