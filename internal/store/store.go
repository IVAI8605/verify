// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"verify/internal/applog"
	"verify/internal/fname"
	"verify/internal/perm"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS files (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  path      TEXT NOT NULL UNIQUE,
  volume    TEXT NOT NULL DEFAULT '',
  name      TEXT NOT NULL DEFAULT '',
  size      INTEGER NOT NULL DEFAULT 0,
  -- alloc 是实际占用磁盘字节数（st_blocks*512），稀疏文件远小于 size（v3.10.8）。
  -- 0 表示旧库升级后尚未重新扫描，展示时退回 size，不会把文件显示成 0 占用。
  alloc     INTEGER NOT NULL DEFAULT 0,
  mtime     INTEGER NOT NULL DEFAULT 0,
  ctime     INTEGER NOT NULL DEFAULT 0,
  nlink     INTEGER NOT NULL DEFAULT 1,
  inode     INTEGER NOT NULL DEFAULT 0,
  -- dev 是文件所在设备号（st_dev），硬链接去重必须按 (dev,inode) 而不是扫描根（v3.10.10）。
  dev       INTEGER NOT NULL DEFAULT 0,
  ext       TEXT NOT NULL DEFAULT '',
  quick     TEXT,
  hash      TEXT,
  audio     BLOB,
  thumb     TEXT,
  duration  REAL,
  width     INTEGER NOT NULL DEFAULT 0,
  height    INTEGER NOT NULL DEFAULT 0,
  codec     TEXT NOT NULL DEFAULT '',
  bit_rate  INTEGER NOT NULL DEFAULT 0,
  sample_rate INTEGER NOT NULL DEFAULT 0,
  video_error TEXT,
  video_at  INTEGER NOT NULL DEFAULT 0,
  video_tries INTEGER NOT NULL DEFAULT 0,
  intro_audio BLOB,
  intro_thumbs TEXT,
  intro_seconds INTEGER NOT NULL DEFAULT 0,
  intro_at INTEGER NOT NULL DEFAULT 0,
  intro_error TEXT,
  last_seen INTEGER NOT NULL DEFAULT 0,
  status    TEXT NOT NULL DEFAULT 'active'
);
CREATE INDEX IF NOT EXISTS idx_files_size ON files(size);
CREATE INDEX IF NOT EXISTS idx_files_quick ON files(quick);
CREATE INDEX IF NOT EXISTS idx_files_hash ON files(hash);
CREATE INDEX IF NOT EXISTS idx_files_name ON files(name);
CREATE INDEX IF NOT EXISTS idx_files_status ON files(status);
-- v3.10.10（第七轮 P1-1）：精确组/同名组翻页都带 status='active'，
-- 整列索引会让 SQLite 先按 status 扫再建临时 B-tree 分组；部分索引可直接按
-- hash / lower(name) 定位并省掉临时 B-tree。旧库同样在 Open 时补建。
CREATE INDEX IF NOT EXISTS idx_files_hash_active ON files(hash) WHERE status='active';
CREATE INDEX IF NOT EXISTS idx_files_lower_name_active ON files(lower(name)) WHERE status='active';

-- v3.10.3：全量哈希候选的预哈希重复集合。旧实现每取 300 条就重跑一次
-- GROUP BY HAVING COUNT(*)>1，34 万文件要重复聚合上千次；改为每轮扫描
-- 刷新一次这张小表，续扫时只做一次主键/等值查找。
CREATE TABLE IF NOT EXISTS hash_dup_quick (
  quick TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS quarantine (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  orig_path  TEXT NOT NULL,
  trash_path TEXT NOT NULL UNIQUE,
  volume     TEXT NOT NULL DEFAULT '',
  size       INTEGER NOT NULL DEFAULT 0,
  hash       TEXT,
  group_key  TEXT,
  reason     TEXT,
  deleted_at INTEGER NOT NULL,
  exported_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_quarantine_time ON quarantine(deleted_at);

-- v3.2.0：预设清理计划（设计文档 §3）。只描述策略，执行只移入隔离区。
CREATE TABLE IF NOT EXISTS cleanup_plans (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  name            TEXT NOT NULL,
  enabled         INTEGER NOT NULL DEFAULT 1,
  schedule        TEXT NOT NULL,
  match_mode      TEXT NOT NULL DEFAULT 'all',
  min_size        INTEGER NOT NULL DEFAULT 0,
  min_count       INTEGER NOT NULL DEFAULT 0,
  extensions      TEXT NOT NULL DEFAULT '',
  retain_strategy TEXT NOT NULL DEFAULT 'modify_newest',
  max_keep_gb     INTEGER NOT NULL DEFAULT 0,
  last_run        INTEGER NOT NULL DEFAULT 0,
  last_result     TEXT NOT NULL DEFAULT '',
  last_error      TEXT NOT NULL DEFAULT '',
  rules_json      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT
);

-- v3.4.0：跨设备指纹包（设计文档 §5）。只存机器名与指纹，不存任何凭据。
CREATE TABLE IF NOT EXISTS sync_peers (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  machine     TEXT NOT NULL UNIQUE,
  imported_at INTEGER NOT NULL DEFAULT 0,
  row_count   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS peer_files (
  machine TEXT NOT NULL,
  path    TEXT NOT NULL,
  size    INTEGER NOT NULL DEFAULT 0,
  hash    TEXT,
  quick   TEXT,
  UNIQUE(machine, path)
);

-- 文件健康检测结果：四类检测共用一张表，用 kind 区分。
-- 每次全量检测按 kind 清空重写，因此 (kind, path) 唯一即可。
CREATE TABLE IF NOT EXISTS health_results (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  kind        TEXT NOT NULL,
  path        TEXT NOT NULL,
  reason      TEXT NOT NULL DEFAULT '',
  detail      TEXT NOT NULL DEFAULT '',
  size        INTEGER NOT NULL DEFAULT 0,
  suggest     TEXT NOT NULL DEFAULT '',
  -- v3.15.0：一次命中的全部原因（JSON 数组）。reason 仍存首个原因，保持旧查询与索引兼容。
  reasons     TEXT NOT NULL DEFAULT '',
  detected_at INTEGER NOT NULL DEFAULT 0,
  UNIQUE(kind, path)
);
CREATE INDEX IF NOT EXISTS idx_health_kind ON health_results(kind);
-- v3.10.11（第七轮 P2-2）：健康页固定按 size DESC, path 分页，
-- 单列索引会让 SQLite 建临时 B-tree 排序；复合索引直接给出顺序。旧库在 Open 时补建。
CREATE INDEX IF NOT EXISTS idx_health_kind_size ON health_results(kind, size DESC, path);

-- 一次检测任务的最终摘要；进度只走内存与 SSE，不逐帧写库。
CREATE TABLE IF NOT EXISTS health_tasks (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  kind        TEXT NOT NULL,
  status      TEXT NOT NULL,
  total       INTEGER NOT NULL DEFAULT 0,
  processed   INTEGER NOT NULL DEFAULT 0,
  errors      INTEGER NOT NULL DEFAULT 0,
  started_at  INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_health_tasks_kind ON health_tasks(kind, started_at);

-- 视频探测缓存：连"健康"结论也记下来。
-- 只记有问题的文件是不够的——17486 个视频里坏的只有几个，
-- 不记健康结论就等于每次重跑都要重新调一遍 ffmpeg。
CREATE TABLE IF NOT EXISTS probe_cache (
  path      TEXT PRIMARY KEY,
  size      INTEGER NOT NULL DEFAULT 0,
  mtime     INTEGER NOT NULL DEFAULT 0,
  status    TEXT NOT NULL,
  reason    TEXT NOT NULL DEFAULT '',
  detail    TEXT NOT NULL DEFAULT '',
  probed_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_probe_status ON probe_cache(status);

-- 重命名/删除的审计记录，按 task_id 支持撤销。
CREATE TABLE IF NOT EXISTS trim_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  file_path TEXT NOT NULL,
  group_key TEXT NOT NULL DEFAULT '',
  mode TEXT NOT NULL DEFAULT 'fast',
  start_offset REAL NOT NULL DEFAULT 0,
  original_duration REAL NOT NULL DEFAULT 0,
  output_duration REAL NOT NULL DEFAULT 0,
  output_path TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  validation_passed INTEGER NOT NULL DEFAULT 0,
  validation_detail TEXT NOT NULL DEFAULT '',
  quarantine_path TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS trim_probe (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  group_key TEXT NOT NULL UNIQUE,
  boundary_seconds REAL NOT NULL DEFAULT 0,
  probed_at INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS audit_log (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id       INTEGER NOT NULL DEFAULT 0,
  operation     TEXT NOT NULL,
  original_path TEXT NOT NULL,
  new_path      TEXT NOT NULL DEFAULT '',
  timestamp     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_task ON audit_log(task_id);

-- 导入会话：一次 CSV 导入 = 一个 session，与当前扫描库完全独立。
-- 设计要点：导入的路径可能与本机无关（比如从 Windows 的 DC 导出的 C:\ 路径），
-- 一个都匹配不上也不是错误——用户要的是恢复当初的分组结构和标记。
CREATE TABLE IF NOT EXISTS import_session (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT    NOT NULL DEFAULT '',
  source       TEXT    NOT NULL DEFAULT '',   -- dc5 / readable
  created_at   INTEGER NOT NULL DEFAULT 0,
  total_rows   INTEGER NOT NULL DEFAULT 0,
  group_count  INTEGER NOT NULL DEFAULT 0,
  matched_rows INTEGER NOT NULL DEFAULT 0
);

-- 导入会话的行。matched=1 表示路径在当前 files 表中找到，只有这些行允许执行隔离/删除。
CREATE TABLE IF NOT EXISTS import_row (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id INTEGER NOT NULL,
  group_no   INTEGER NOT NULL DEFAULT 0,
  path       TEXT    NOT NULL DEFAULT '',
  name       TEXT    NOT NULL DEFAULT '',
  size       INTEGER NOT NULL DEFAULT 0,
  hash       TEXT    NOT NULL DEFAULT '',
  chk        INTEGER NOT NULL DEFAULT 0,
  protect    INTEGER NOT NULL DEFAULT 0,
  hardlink   INTEGER NOT NULL DEFAULT 1,
  datec      INTEGER NOT NULL DEFAULT 0,
  datem      INTEGER NOT NULL DEFAULT 0,
  duration   REAL    NOT NULL DEFAULT 0,
  width      INTEGER NOT NULL DEFAULT 0,
  height     INTEGER NOT NULL DEFAULT 0,
  matched    INTEGER NOT NULL DEFAULT 0
);
-- 智能保留偏好学习（F4 二期）：建议 vs 用户实际处理；Chosen 空即直接采纳。
CREATE TABLE IF NOT EXISTS user_pref (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  group_key  TEXT NOT NULL DEFAULT '',
  suggested  TEXT NOT NULL DEFAULT '',
  chosen     TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_import_row_session ON import_row(session_id, group_no);

-- v2.6.0 元数据标签规则引擎：用户自定义标签项，每项有匹配模式（仅显示/精确/相似/含空白变体）。
-- v2.6.0 只暴露 CRUD API；v2.7.0 起增加取值来源（目录名/文件名正则）并接入辅助分组。
CREATE TABLE IF NOT EXISTS video_tags (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT NOT NULL UNIQUE,                     -- 标签名（如"剧集名""年份"）
  match_mode    TEXT NOT NULL DEFAULT 'exact',           -- exact/similar/exact_ws/display_only
  join_group    INTEGER NOT NULL DEFAULT 1,              -- 1=参与分组，0=仅显示
  sort_order    INTEGER NOT NULL DEFAULT 0,              -- 设置页排序
  value_source  TEXT NOT NULL DEFAULT 'dirname',         -- v2.7.0 取值来源：dirname/filename_regex
  value_pattern TEXT NOT NULL DEFAULT ''                 -- v2.7.0 文件名正则，须含 1 个捕获组
);
CREATE INDEX IF NOT EXISTS idx_video_tags_sort ON video_tags(sort_order);

-- 文件标签值：把用户定义的标签值挂到 files 上。
CREATE TABLE IF NOT EXISTS file_tags (
  file_id  INTEGER NOT NULL,
  tag_id   INTEGER NOT NULL,
  value    TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(file_id, tag_id)
);
CREATE INDEX IF NOT EXISTS idx_file_tags_tag ON file_tags(tag_id);

-- v2.10.0 重命名批：一次清单式改名的快照，用于按批撤销（V3.5 在此基础上扩展预设）。
CREATE TABLE IF NOT EXISTS rename_batches (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at INTEGER NOT NULL DEFAULT 0,
  status     TEXT NOT NULL DEFAULT 'done',   -- done / failed / running / undone / undone_failed / undone_partial / repaired / repaired_partial
  items_json TEXT NOT NULL DEFAULT '',       -- [{path,orig,target,new_path,task_id,error}]
  preset_id  INTEGER NOT NULL DEFAULT 0,
  files_count INTEGER NOT NULL DEFAULT 0,
  methods    TEXT NOT NULL DEFAULT '',
  task_id    INTEGER NOT NULL DEFAULT 0,
  -- v3.10.13：撤销 / 崩溃修复后仍没处理干净的行数，供未完成批次提示直接显示，不必再猜。
  failed_count INTEGER NOT NULL DEFAULT 0
);

-- v3.10.12（P1-03）：改名意图行。执行前先记下「打算把 A 改成 B」，成功后标 done；
-- 进程中途被杀时，启动恢复凭 pending 行 + 磁盘现状补写审计与索引，
-- 避免「磁盘已改名、库里没有任何记录可撤销」的孤儿。
CREATE TABLE IF NOT EXISTS rename_intents (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  batch_id      INTEGER NOT NULL DEFAULT 0,
  task_id       INTEGER NOT NULL DEFAULT 0,
  original_path TEXT NOT NULL,
  new_path      TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'pending',  -- pending / done / failed / ambiguous
  created_at    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_rename_intents_batch ON rename_intents(batch_id, status);

-- v3.21.0 两阶段改名计划：40 万条清单只落这里，执行时按 2000 一片取走。
-- 明细逐行存，避免 rename_batches.items_json 变成几十 MB 的单行。
CREATE TABLE IF NOT EXISTS rename_plans (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at INTEGER NOT NULL DEFAULT 0,
  total      INTEGER NOT NULL DEFAULT 0,
  status     TEXT NOT NULL DEFAULT 'planned'   -- planned / running / done / failed / discarded
);
CREATE TABLE IF NOT EXISTS rename_plan_items (
  plan_id       INTEGER NOT NULL,
  idx           INTEGER NOT NULL,
  old_path      TEXT NOT NULL,
  old_name      TEXT NOT NULL DEFAULT '',
  new_name      TEXT NOT NULL DEFAULT '',
  size          INTEGER NOT NULL DEFAULT 0, -- v3.25.0：计划生成时的文件大小，执行前复核身份
  mtime         INTEGER NOT NULL DEFAULT 0, -- v3.25.0：计划生成时的 mtime（纳秒）
  state         TEXT NOT NULL DEFAULT 'pending', -- pending / done / failed
  error         TEXT NOT NULL DEFAULT '',
  real_batch_id INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (plan_id, idx)
);
CREATE INDEX IF NOT EXISTS idx_rename_plan_items_pending ON rename_plan_items(plan_id, state, idx);

-- v3.5.0：重命名方法预设（用户可保存/加载/共享）
CREATE TABLE IF NOT EXISTS rename_presets (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  methods     TEXT NOT NULL,
  created_at  INTEGER NOT NULL DEFAULT 0,
  updated_at  INTEGER NOT NULL DEFAULT 0
);

-- v3.7.0：操作审计记录。只增不改，不随业务表删除而消失；权威记录在此，
-- 文本日志（verify.log）只作排错辅助。字段含义见 internal/oplog/oplog.go。
CREATE TABLE IF NOT EXISTS op_log (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  ts            INTEGER NOT NULL,
  req_id        TEXT NOT NULL DEFAULT '',
  actor         TEXT NOT NULL DEFAULT '',
  source        TEXT NOT NULL DEFAULT 'ui',
  action        TEXT NOT NULL,
  result        TEXT NOT NULL DEFAULT 'ok',
  summary       TEXT NOT NULL DEFAULT '',
  target_count  INTEGER NOT NULL DEFAULT 0,
  detail_json   TEXT NOT NULL DEFAULT '',
  error         TEXT NOT NULL DEFAULT '',
  duration_ms   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_op_log_ts ON op_log(ts);
CREATE INDEX IF NOT EXISTS idx_op_log_action ON op_log(action, ts);
CREATE INDEX IF NOT EXISTS idx_op_log_req ON op_log(req_id);

-- v3.10.5：批量写操作前的文件快照 + 操作后对账。
-- 只记录本次操作触及的条目（Lstat 采集大小/时间，哈希复用 files 已有值），
-- 不会为了快照去读全库，也不会重算哈希。
CREATE TABLE IF NOT EXISTS op_snapshot_run (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  kind        TEXT NOT NULL,
  started_at  INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0,
  checked     INTEGER NOT NULL DEFAULT 0,
  mismatched  INTEGER NOT NULL DEFAULT 0,
  note        TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS op_snapshot_item (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id     INTEGER NOT NULL,
  path       TEXT NOT NULL,
  size       INTEGER NOT NULL DEFAULT 0,
  mtime      INTEGER NOT NULL DEFAULT 0,
  hash       TEXT,
  expect     TEXT NOT NULL DEFAULT 'absent',   -- absent / relocated / renamed
  final_path TEXT NOT NULL DEFAULT '',
  state      TEXT NOT NULL DEFAULT 'pending',  -- pending / ok / still_exists / missing / changed / path_mismatch / skipped
  detail     TEXT NOT NULL DEFAULT '',
  checked_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_op_snapshot_item_run ON op_snapshot_item(run_id);
-- v3.10.8：对账按 (run_id, path) 更新单条状态，只有 run_id 单列索引时每次要扫该批次全部行
CREATE INDEX IF NOT EXISTS idx_op_snapshot_item_run_path ON op_snapshot_item(run_id, path);
CREATE INDEX IF NOT EXISTS idx_op_snapshot_run_at ON op_snapshot_run(started_at);
`

// FileInfo 是扫描时写入的最小文件信息。
type FileInfo struct {
	Path   string
	Volume string
	Name   string
	Size   int64
	// Alloc 是磁盘实际占用字节数（st_blocks*512），用于「可回收空间」估算。
	Alloc int64
	Mtime int64 // UnixNano
	Ctime int64 // UnixNano
	Nlink int64
	Inode int64
	// Dev 是文件系统设备号；0 表示旧库尚未重新扫描，硬链接去重退回 volume:inode。
	Dev int64
	Ext string
}

// MediaMeta 是 ffprobe 采集的媒体属性，随文件行返回给 UI 与报告。
type MediaMeta struct {
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	Codec      string `json:"codec,omitempty"`
	BitRate    int64  `json:"bit_rate,omitempty"`
	SampleRate int    `json:"sample_rate,omitempty"`
}

// FileRow 是查询返回的文件行。
type FileRow struct {
	ID     int64  `json:"id"`
	Path   string `json:"path"`
	Volume string `json:"volume"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	// Alloc 是实际占用磁盘字节数；0 表示该行还没被新版本扫描过。
	Alloc    int64   `json:"alloc,omitempty"`
	Mtime    int64   `json:"mtime"`
	Ctime    int64   `json:"ctime"`
	Nlink    int64   `json:"nlink"`
	Inode    int64   `json:"inode"`
	Dev      int64   `json:"dev,omitempty"`
	Ext      string  `json:"ext"`
	Quick    string  `json:"quick,omitempty"`
	Hash     string  `json:"hash,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Thumb    string  `json:"thumb,omitempty"`
	ImgHash  string  `json:"img_hash,omitempty"`
	MediaMeta
	// Tags 是文件命中的元数据标签（v2.8.0），按需由 AttachFileTags 填充。
	Tags []TagValue `json:"tags,omitempty"`
	// Peer 是跨设备标记（v3.4.0）：命中另一台机器指纹包时写入机器名。
	// 不落库，只在请求带 cross=1 时由 Web 层临时填充。
	Peer string `json:"peer,omitempty"`
}

// MarshalJSON 在序列化时标注「路径/文件名的原始字节不是合法 UTF-8」的行（v3.11.0）。
//
// 为什么不能靠字符串本身判断：encoding/json 在编码时会把非法 UTF-8 字节统一替换成 U+FFFD，
// 前端拿到的是看起来正常的替换字符，无法区分「真的叫这个名字」和「编码坏了」。
// 因此这里在编码前检查原始字节，异常行额外带 name_encoding:"invalid"，正常行不出现该字段。
func (f FileRow) MarshalJSON() ([]byte, error) {
	type plain FileRow
	out := struct {
		plain
		NameEncoding string `json:"name_encoding,omitempty"`
	}{plain: plain(f)}
	if !utf8.ValidString(f.Path) || !utf8.ValidString(f.Name) {
		out.NameEncoding = "invalid"
	}
	return json.Marshal(out)
}

// AllocSize 返回参与「可回收空间」计算的实际占用字节数。
//
// 稀疏文件（虚拟机镜像常见）逻辑大小可能是几十 GB，实际只占几百 MB，
// 直接拿 Size 累加会把可回收空间算大一个数量级。旧库升级后 Alloc=0
// 表示尚未重新扫描，此时退回 Size，保证既不显示 0 占用、也不凭空变化。
func (f FileRow) AllocSize() int64 {
	if f.Alloc > 0 {
		return f.Alloc
	}
	return f.Size
}

// TagValue 是文件上的一条标签值（标签名 + 取值），用于结果列表展示。
type TagValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// TrashRow 是回收站条目。
type TrashRow struct {
	ID        int64  `json:"id"`
	OrigPath  string `json:"orig_path"`
	TrashPath string `json:"trash_path"`
	Volume    string `json:"volume"`
	Size      int64  `json:"size"`
	Hash      string `json:"hash,omitempty"`
	DeletedAt int64  `json:"deleted_at"`
	// Reason 记录来源：duplicate 表示查重清理，health 表示文件健康清理
	Reason string `json:"reason,omitempty"`
	// ExportedAt 是最近一次成功导出备份的时间戳（秒）；0 表示未导出（v3.1.0）。
	ExportedAt int64 `json:"exported_at,omitempty"`
}

type Store struct {
	db   *sql.DB
	mu   sync.Mutex
	path string

	// dataVersion v3.8.0：文件数据（路径/大小/名称/hash 等）的变更计数。
	// 搜索快照记录创建时的版本，用它判断结果是否过期；只改指纹/缩略图列不 bump。
	dataVersion atomic.Int64

	// Counts 聚合缓存：空闲时界面每 15 秒轮询一次，
	// 不缓存的话机械盘每 15 秒就被读一次，永远休眠不了。
	cmu  sync.Mutex
	cVal Counts
	cAt  time.Time
	cMin int64
	cOK  bool
	// cFrozen 为真时忽略 TTL 且不再被写路径失效（v3.10.11，第七轮 P2-1）。
	cFrozen bool
}

func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "verify.db")
	// v3.10.11（第七轮 P2-4）：journal_size_limit 让 WAL 回放/截断后不再保留
	// 旧的高速缓存页文件。全量扫描时 16 万次 upsert 会把 WAL 撑到数百 MB，
	// 默认 auto-checkpoint 只复用文件、不缩小，长期占盘。
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=journal_size_limit(67108864)")
	if err != nil {
		return nil, err
	}
	// 连接池放宽到 4（审查 #3）：WAL 支持多读一写，busy_timeout(5000) 兜底写排队。
	// 单连接会让 UI 读请求在扫描/分析写库时全部排队；多连接后偶发的单连接
	// ctx 取消（客户端断开，审查 D-4）也不再阻塞其它请求。
	db.SetMaxOpenConns(4)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("初始化数据库: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("升级数据库: %w", err)
	}
	// SQLite 用 0777 & ~umask 建文件，落到 0700 会带上多余的可执行位；
	// 顺手把主库与 WAL/SHM 收到 0600，数据库是数据文件，不该可执行。
	perm.Tighten(path, 0o600)
	perm.Tighten(path+"-wal", 0o600)
	perm.Tighten(path+"-shm", 0o600)
	return &Store{db: db, path: path}, nil
}

// migrate 为旧数据库补充视频分析相关字段，并把旧版 "ERR:" 标记迁移为 video_error。
//
// 列存在性用 PRAGMA table_info 判定（审查 D-3）：不依赖 ALTER 的英文错误串——
// 不同 SQLite/modernc 版本的报错措辞可能变化，误判会让启动直接失败。
// "duplicate column name" 兜底保留，防 PRAGMA 本身异常。
func migrate(ctx context.Context, db *sql.DB) error {
	existing := map[string]bool{}
	if rows, err := db.QueryContext(ctx, `PRAGMA table_info(files)`); err == nil {
		for rows.Next() {
			var cid, notNull, pk int
			var name, ctype string
			var dflt any
			if rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk) == nil {
				existing[name] = true
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for _, c := range []struct{ name, ddl string }{
		{"video_error", `ALTER TABLE files ADD COLUMN video_error TEXT`},
		{"video_at", `ALTER TABLE files ADD COLUMN video_at INTEGER NOT NULL DEFAULT 0`},
		{"video_tries", `ALTER TABLE files ADD COLUMN video_tries INTEGER NOT NULL DEFAULT 0`},
		{"crop_w", `ALTER TABLE files ADD COLUMN crop_w INTEGER NOT NULL DEFAULT 0`},
		{"crop_h", `ALTER TABLE files ADD COLUMN crop_h INTEGER NOT NULL DEFAULT 0`},
		{"crop_at", `ALTER TABLE files ADD COLUMN crop_at INTEGER NOT NULL DEFAULT 0`},
		{"intro_audio", `ALTER TABLE files ADD COLUMN intro_audio BLOB`},
		{"intro_thumbs", `ALTER TABLE files ADD COLUMN intro_thumbs TEXT`},
		{"intro_seconds", `ALTER TABLE files ADD COLUMN intro_seconds INTEGER NOT NULL DEFAULT 0`},
		{"intro_at", `ALTER TABLE files ADD COLUMN intro_at INTEGER NOT NULL DEFAULT 0`},
		{"intro_error", `ALTER TABLE files ADD COLUMN intro_error TEXT`},
		{"width", `ALTER TABLE files ADD COLUMN width INTEGER NOT NULL DEFAULT 0`},
		{"height", `ALTER TABLE files ADD COLUMN height INTEGER NOT NULL DEFAULT 0`},
		{"codec", `ALTER TABLE files ADD COLUMN codec TEXT NOT NULL DEFAULT ''`},
		{"bit_rate", `ALTER TABLE files ADD COLUMN bit_rate INTEGER NOT NULL DEFAULT 0`},
		{"sample_rate", `ALTER TABLE files ADD COLUMN sample_rate INTEGER NOT NULL DEFAULT 0`},
		{"img_hash", `ALTER TABLE files ADD COLUMN img_hash TEXT`},
		{"img_w", `ALTER TABLE files ADD COLUMN img_w INTEGER NOT NULL DEFAULT 0`},
		{"img_h", `ALTER TABLE files ADD COLUMN img_h INTEGER NOT NULL DEFAULT 0`},
		{"img_at", `ALTER TABLE files ADD COLUMN img_at INTEGER NOT NULL DEFAULT 0`},
		{"img_error", `ALTER TABLE files ADD COLUMN img_error TEXT`},
		{"img_size", `ALTER TABLE files ADD COLUMN img_size INTEGER NOT NULL DEFAULT 0`},
		{"img_mtime", `ALTER TABLE files ADD COLUMN img_mtime INTEGER NOT NULL DEFAULT 0`},
		{"trimmed", `ALTER TABLE files ADD COLUMN trimmed INTEGER NOT NULL DEFAULT 0`},
		{"trimmed_offset", `ALTER TABLE files ADD COLUMN trimmed_offset REAL NOT NULL DEFAULT 0`},
		// v3.10.8：实际占用（st_blocks*512）。旧库补列后值为 0，
		// 展示层按「0 视为未采集」回退到逻辑大小，下次扫描自动补齐。
		{"alloc", `ALTER TABLE files ADD COLUMN alloc INTEGER NOT NULL DEFAULT 0`},
		// v3.10.10：设备号（st_dev），硬链接去重按 (dev,inode)。旧库补列后为 0，
		// 去重逻辑回退到 volume:inode，下次扫描自动补齐。
		{"dev", `ALTER TABLE files ADD COLUMN dev INTEGER NOT NULL DEFAULT 0`},
	} {
		if existing[c.name] {
			continue
		}
		if _, err := db.ExecContext(ctx, c.ddl); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return err
		}
	}
	// v3.25.0（P1-2）：改名计划补 size/mtime。计划生成到执行之间可能隔几小时，
	// 期间 old_path 上可能已被换成别的文件；执行前用这两列复核身份，避免改错文件。
	// 旧库补列后为 0，执行逻辑跳过复核（与升级前行为一致），新计划才会写入。
	planCols := map[string]bool{}
	if rows, err := db.QueryContext(ctx, `PRAGMA table_info(rename_plan_items)`); err == nil {
		for rows.Next() {
			var cid, notNull, pk int
			var name, ctype string
			var dflt any
			if rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk) == nil {
				planCols[name] = true
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for _, c := range []struct{ name, ddl string }{
		{"size", `ALTER TABLE rename_plan_items ADD COLUMN size INTEGER NOT NULL DEFAULT 0`},
		{"mtime", `ALTER TABLE rename_plan_items ADD COLUMN mtime INTEGER NOT NULL DEFAULT 0`},
	} {
		if planCols[c.name] {
			continue
		}
		if _, err := db.ExecContext(ctx, c.ddl); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return err
		}
	}
	// v2.7.0：video_tags 增加取值来源与正则。新库由 schema 直接建好，旧库在这里补列；
	// 表结构同样用 PRAGMA 判定，理由与 files 的迁移一致。
	tagCols := map[string]bool{}
	if rows, err := db.QueryContext(ctx, `PRAGMA table_info(video_tags)`); err == nil {
		for rows.Next() {
			var cid, notNull, pk int
			var name, ctype string
			var dflt any
			if rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk) == nil {
				tagCols[name] = true
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for _, c := range []struct{ name, ddl string }{
		{"value_source", `ALTER TABLE video_tags ADD COLUMN value_source TEXT NOT NULL DEFAULT 'dirname'`},
		{"value_pattern", `ALTER TABLE video_tags ADD COLUMN value_pattern TEXT NOT NULL DEFAULT ''`},
	} {
		if tagCols[c.name] {
			continue
		}
		if _, err := db.ExecContext(ctx, c.ddl); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return err
		}
	}
	// v3.1.0：quarantine 增加 exported_at（隔离区导出备份标记）。新库由 schema 建好，
	// 旧库在这里补列，判定方式与其他表迁移一致（PRAGMA table_info）。
	trashCols := map[string]bool{}
	if rows, err := db.QueryContext(ctx, `PRAGMA table_info(quarantine)`); err == nil {
		for rows.Next() {
			var cid, notNull, pk int
			var name, ctype string
			var dflt any
			if rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk) == nil {
				trashCols[name] = true
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if !trashCols["exported_at"] {
		if _, err := db.ExecContext(ctx, `ALTER TABLE quarantine ADD COLUMN exported_at INTEGER NOT NULL DEFAULT 0`); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return err
			}
		}
	}
	// v3.5.0：rename_batches 补 preset_id/files_count/methods/task_id（旧库兼容）。
	batchCols := map[string]bool{}
	if rows, err := db.QueryContext(ctx, `PRAGMA table_info(rename_batches)`); err == nil {
		for rows.Next() {
			var cid, notNull, pk int
			var name, ctype string
			var dflt any
			if rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk) == nil {
				batchCols[name] = true
			}
		}
		_ = rows.Close()
	}
	for _, c := range []struct{ name, ddl string }{
		{"preset_id", `ALTER TABLE rename_batches ADD COLUMN preset_id INTEGER NOT NULL DEFAULT 0`},
		{"files_count", `ALTER TABLE rename_batches ADD COLUMN files_count INTEGER NOT NULL DEFAULT 0`},
		{"methods", `ALTER TABLE rename_batches ADD COLUMN methods TEXT NOT NULL DEFAULT ''`},
		{"task_id", `ALTER TABLE rename_batches ADD COLUMN task_id INTEGER NOT NULL DEFAULT 0`},
		// v3.10.13：旧库补列，未完成批次提示要直接读出「还有几行没撤干净」。
		{"failed_count", `ALTER TABLE rename_batches ADD COLUMN failed_count INTEGER NOT NULL DEFAULT 0`},
	} {
		if batchCols[c.name] {
			continue
		}
		if _, err := db.ExecContext(ctx, c.ddl); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_files_img ON files(img_hash)`); err != nil {
		return err
	}
	// v3.2.0（设计文档 §10.2/§10.7 实测结论）：只加这一条表达式索引，
	// 必须与 NameGroups 的 GROUP BY lower(name) 一字不差才能命中；
	// status/hash、status/quick 两条实测无收益，不加，避免写放大。
	if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_files_lower_name ON files(lower(name))`); err != nil {
		return err
	}
	// v3.9.0：清理计划可保存搜索规则；旧库补列（SQLite 没有 IF NOT EXISTS ADD COLUMN）。
	var hasRules int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cleanup_plans') WHERE name='rules_json'`).Scan(&hasRules); err == nil && hasRules == 0 {
		if _, err := db.ExecContext(ctx, `ALTER TABLE cleanup_plans ADD COLUMN rules_json TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	// v3.15.0：健康结果补 reasons 列（一次命中的全部原因，JSON 数组）。
	// 旧行留空，读取层按 reason 单值回退，不需要回填历史数据。
	var hasHealthReasons int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('health_results') WHERE name='reasons'`).Scan(&hasHealthReasons); err == nil && hasHealthReasons == 0 {
		if _, err := db.ExecContext(ctx, `ALTER TABLE health_results ADD COLUMN reasons TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	// v3.10.3：旧版本把哈希失败写成 'ERR'，而候选查询只挑 NULL，失败文件
	// 除非大小或 mtime 变化就不会再重算。这里一次性还原成 NULL 让下轮扫描
	// 重试；用 meta 标记只做一次，避免每次启动都全表扫描。
	var errCleaned int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM meta WHERE key='hash_err_cleanup'`).Scan(&errCleaned); err == nil && errCleaned == 0 {
		if _, err := db.ExecContext(ctx, `UPDATE files SET quick=NULL WHERE quick='ERR'`); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `UPDATE files SET hash=NULL WHERE hash='ERR'`); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('hash_err_cleanup','1')`); err != nil {
			return err
		}
	}
	// v3.10.4：video_error 迁移同样加 meta 守卫。旧写法每次启动都全表扫一遍 files，
	// 几万行时启动会明显变慢，而迁移本身只需要成功执行一次。
	var audioErrMigrated int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM meta WHERE key='audio_err_migrate'`).Scan(&audioErrMigrated); err == nil && audioErrMigrated == 0 {
		if _, err := db.ExecContext(ctx, `UPDATE files SET video_error=substr(CAST(audio AS TEXT),5), audio=NULL, thumb='', duration=NULL
			WHERE audio IS NOT NULL AND substr(CAST(audio AS TEXT),1,4)='ERR:' AND (video_error IS NULL OR video_error='')`); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('audio_err_migrate','1')`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

// CheckpointWAL 把 WAL 里已提交的页写回主库并截断 WAL 文件（v3.10.11，第七轮 P2-4）。
//
// 只在扫描这类批量写结束后调用：频繁 checkpoint 会和写事务抢锁。
// TRUNCATE 在第一行返回 busy 标志，被读事务挡住时不算错误，留给下次自动 checkpoint。
func (s *Store) CheckpointWAL(ctx context.Context) (busy bool, err error) {
	var busyFlag, logFrames, ckptFrames int
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).
		Scan(&busyFlag, &logFrames, &ckptFrames); err != nil {
		return false, fmt.Errorf("收尾 WAL（%v）", err)
	}
	return busyFlag != 0, nil
}

// TagMode 标签匹配模式常量（v2.6.0）。
const (
	TagModeExact       = "exact"        // 精确匹配
	TagModeSimilar     = "similar"      // 相似文本（编辑距离 ≤ 1 或包含）
	TagModeExactWS     = "exact_ws"     // 精确含空白变体（去空格后比较）
	TagModeDisplayOnly = "display_only" // 仅显示在结果列表，不参与分组
)

// VideoTag 是用户自定义的元数据标签定义（v2.6.0，v2.7.0 增加取值来源）。
type VideoTag struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	MatchMode    string `json:"match_mode"`
	JoinGroup    bool   `json:"join_group"`
	SortOrder    int    `json:"sort_order"`
	ValueSource  string `json:"value_source"`
	ValuePattern string `json:"value_pattern"`
	// Hits 命中该标签的文件数（列表展示用，不计入分行）。
	Hits int64 `json:"hits"`
}

// GetVideoTags 返回全部标签，按 sort_order 排序（v2.6.0；v2.7.0 附带命中数）。
func (s *Store) GetVideoTags(ctx context.Context) ([]VideoTag, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.name, t.match_mode, t.join_group, t.sort_order,
		t.value_source, t.value_pattern,
		(SELECT COUNT(*) FROM file_tags ft WHERE ft.tag_id=t.id AND ft.value<>'') AS hits
		FROM video_tags t ORDER BY t.sort_order, t.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var tags []VideoTag
	for rows.Next() {
		var t VideoTag
		var jg int
		if err := rows.Scan(&t.ID, &t.Name, &t.MatchMode, &jg, &t.SortOrder,
			&t.ValueSource, &t.ValuePattern, &t.Hits); err != nil {
			return nil, err
		}
		t.JoinGroup = jg != 0
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// UpsertVideoTag 创建或更新一个标签（v2.6.0）。
// 按 name 唯一键 upsert；返回新/已存在的 ID。取值规则在这里统一校验，
// 因为 Web 层、计算层都依赖同一套白名单，避免各写一份。
func (s *Store) UpsertVideoTag(ctx context.Context, name, matchMode, valueSource, valuePattern string, joinGroup bool, sortOrder int) (int64, error) {
	// 校验 match_mode
	switch matchMode {
	case TagModeExact, TagModeSimilar, TagModeExactWS, TagModeDisplayOnly:
	default:
		matchMode = TagModeExact
	}
	// 校验取值来源：正则必须在保存时就编译通过，否则等到扫描重算才失败，用户很难定位
	switch valueSource {
	case TagSourceDirname:
		valuePattern = ""
	case TagSourceFilenameRegex:
		if _, err := compileTagPattern(valueSource, valuePattern); err != nil {
			return 0, err
		}
	default:
		valueSource = TagSourceDirname
		valuePattern = ""
	}
	jg := 0
	if joinGroup {
		jg = 1
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO video_tags(name, match_mode, join_group, sort_order, value_source, value_pattern) VALUES(?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET match_mode=excluded.match_mode, join_group=excluded.join_group, sort_order=excluded.sort_order,
		value_source=excluded.value_source, value_pattern=excluded.value_pattern`,
		name, matchMode, jg, sortOrder, valueSource, valuePattern)
	if err != nil {
		return 0, err
	}
	// 冲突走 UPDATE 分支时 LastInsertId 不可靠（可能返回 0），统一回查 name 拿准 ID
	if id, lerr := res.LastInsertId(); lerr == nil && id > 0 {
		return id, nil
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM video_tags WHERE name=?`, name).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// DeleteVideoTag 删除指定标签及其关联的所有文件标签值（v2.6.0）。
// 两张表必须一起删，否则 file_tags 会留下无主行；用事务保证不留半删状态。
func (s *Store) DeleteVideoTag(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM file_tags WHERE tag_id=?`, id); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM video_tags WHERE id=?`, id); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// NextGeneration 返回递增的扫描代次。
func (s *Store) NextGeneration(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='generation'`).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var cur int64
	_, _ = fmt.Sscan(v, &cur)
	cur++
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('generation',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fmt.Sprint(cur))
	if err != nil {
		return 0, err
	}
	return cur, nil
}

// sameFileCond 判断「同一文件、未被改动」，用于决定是否保留已算出的指纹。
// v3.10.10（P2-5）：加入 ctime——内容替换后 mtime 可被回拨，ctime 由内核维护不能伪造。
// 两侧任一 ctime 为 0（旧库/平台未采集）时退回 size+mtime，避免升级首扫把全库指纹清空。
const sameFileCond = `files.size=excluded.size AND files.mtime=excluded.mtime AND files.status='active'
	  AND (files.ctime=0 OR excluded.ctime=0 OR files.ctime=excluded.ctime)`

const upsertSQL = `
INSERT INTO files(path,volume,name,size,alloc,mtime,ctime,nlink,inode,dev,ext,quick,hash,last_seen,status)
VALUES(?,?,?,?,?,?,?,?,?,?,?,NULL,NULL,?, 'active')
ON CONFLICT(path) DO UPDATE SET
  last_seen=excluded.last_seen,
  status='active',
  volume=excluded.volume,
  name=excluded.name,
  ext=excluded.ext,
  size=excluded.size,
  alloc=excluded.alloc,
  mtime=excluded.mtime,
  ctime=excluded.ctime,
  nlink=excluded.nlink,
  inode=excluded.inode,
  dev=excluded.dev,
  quick=CASE WHEN ` + sameFileCond + ` THEN files.quick ELSE NULL END,
  hash=CASE WHEN ` + sameFileCond + ` THEN files.hash ELSE NULL END,
  audio=CASE WHEN ` + sameFileCond + ` THEN files.audio ELSE NULL END,
  thumb=CASE WHEN ` + sameFileCond + ` THEN files.thumb ELSE NULL END,
  duration=CASE WHEN ` + sameFileCond + ` THEN files.duration ELSE NULL END,
  width=CASE WHEN ` + sameFileCond + ` THEN files.width ELSE 0 END,
  height=CASE WHEN ` + sameFileCond + ` THEN files.height ELSE 0 END,
  codec=CASE WHEN ` + sameFileCond + ` THEN files.codec ELSE '' END,
  bit_rate=CASE WHEN ` + sameFileCond + ` THEN files.bit_rate ELSE 0 END,
  sample_rate=CASE WHEN ` + sameFileCond + ` THEN files.sample_rate ELSE 0 END,
  video_error=CASE WHEN ` + sameFileCond + ` THEN files.video_error ELSE NULL END,
  video_at=CASE WHEN ` + sameFileCond + ` THEN files.video_at ELSE 0 END,
  img_hash=CASE WHEN ` + sameFileCond + ` THEN files.img_hash ELSE NULL END,
  img_error=CASE WHEN ` + sameFileCond + ` THEN files.img_error ELSE NULL END,
  img_w=CASE WHEN ` + sameFileCond + ` THEN files.img_w ELSE 0 END,
  img_h=CASE WHEN ` + sameFileCond + ` THEN files.img_h ELSE 0 END,
  img_at=CASE WHEN ` + sameFileCond + ` THEN files.img_at ELSE 0 END,
  img_size=CASE WHEN ` + sameFileCond + ` THEN files.img_size ELSE 0 END,
  img_mtime=CASE WHEN ` + sameFileCond + ` THEN files.img_mtime ELSE 0 END,
  video_tries=CASE WHEN ` + sameFileCond + ` THEN files.video_tries ELSE 0 END,
  intro_audio=CASE WHEN ` + sameFileCond + ` THEN files.intro_audio ELSE NULL END,
  intro_thumbs=CASE WHEN ` + sameFileCond + ` THEN files.intro_thumbs ELSE '' END,
  intro_seconds=CASE WHEN ` + sameFileCond + ` THEN files.intro_seconds ELSE 0 END,
  intro_at=CASE WHEN ` + sameFileCond + ` THEN files.intro_at ELSE 0 END,
  intro_error=CASE WHEN ` + sameFileCond + ` THEN files.intro_error ELSE NULL END
`

// UpsertBatch 批量写入文件信息（每个批次单独事务）。
func (s *Store) UpsertBatch(ctx context.Context, rows []FileInfo, gen int64) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, upsertSQL)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Path, r.Volume, r.Name, r.Size, r.Alloc, r.Mtime, r.Ctime, r.Nlink, r.Inode, r.Dev, r.Ext, gen); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("写入 %s: %w", r.Path, err)
		}
	}
	// 语句关闭失败不应掩盖数据写入结果，事务提交才是判断成功的依据。
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		return err
	}
	// v3.8.0：文件数据变了，搜索快照据此判断过期。
	s.dataVersion.Add(1)
	return nil
}

// UpsertOne 写入单个文件信息并返回其 id。
func (s *Store) UpsertOne(ctx context.Context, f FileInfo, gen int64) (int64, error) {
	if _, err := s.db.ExecContext(ctx, upsertSQL, f.Path, f.Volume, f.Name, f.Size, f.Alloc, f.Mtime, f.Ctime, f.Nlink, f.Inode, f.Dev, f.Ext, gen); err != nil {
		return 0, err
	}
	s.dataVersion.Add(1)
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM files WHERE path=?`, f.Path).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// DeleteMissing 清理扫描中未刷新的行。
//
// 宽限一代（删 last_seen < gen-1，审查 D-1）：watcher 可能在扫描代次推进后、
// 遍历触及该文件前回写「当前代次」——若按 last_seen < gen 立即删，
// 扫描间隙的回写会被误删导致指纹重算。多留一轮的代价只是极少量脏行延后清理。
func (s *Store) DeleteMissing(ctx context.Context, gen int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	// v3.17.6（F-24 / F-23）：索引行删掉后，挂在 file_id 上的标签与按路径存的探测缓存
	// 会变成孤儿：标签页出现点不开的条目，probe_cache 随扫描轮次无限增长。
	// 同事务清理，失败即整批回滚，不留半套数据。
	if _, err := tx.ExecContext(ctx, `DELETE FROM file_tags WHERE file_id IN (SELECT id FROM files WHERE last_seen < ?)`, gen-1); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM probe_cache WHERE path IN (SELECT path FROM files WHERE last_seen < ?)`, gen-1); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM files WHERE last_seen < ?`, gen-1)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if n > 0 {
		s.dataVersion.Add(1)
	}
	return n, nil
}

// ListActiveFilePathsAfter 与 ListFilePathsAfter 相同，但只取 status='active' 的行（v3.12.0）。
// 修编码异常文件名时用：已删除/已移走的行不该再进改名计划。
func (s *Store) ListActiveFilePathsAfter(ctx context.Context, afterID int64, limit int) ([]FilePathRow, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, path FROM files WHERE id > ? AND status='active' ORDER BY id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]FilePathRow, 0, limit)
	for rows.Next() {
		var r FilePathRow
		if err := rows.Scan(&r.ID, &r.Path); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FilePathRow 只带 id 与 path：索引体检这类遍历只要路径，不拉 57 列整行。
type FilePathRow struct {
	ID   int64
	Path string
}

// ListFilePathsAfter 按 id 升序取一批路径（keyset 分页，v3.10.12 P1-02）。
//
// 不用 OFFSET 是因为 files 在真机上是十几万行量级，深翻页会越来越慢；
// id 是自增主键，keyset 每批代价恒定。
func (s *Store) ListFilePathsAfter(ctx context.Context, afterID int64, limit int) ([]FilePathRow, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, path FROM files WHERE id > ? ORDER BY id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]FilePathRow, 0, limit)
	for rows.Next() {
		var r FilePathRow
		if err := rows.Scan(&r.ID, &r.Path); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteGhostPaths 批量删除磁盘上已不存在的索引行及其健康结论（v3.10.12 P1-02）。
//
// 只删索引行，不动磁盘上的任何文件；分块提交是为了让十几万行的体检
// 不至于攒成一个巨型事务把 WAL 顶满。返回实际删掉的 files 行数。
func (s *Store) DeleteGhostPaths(ctx context.Context, paths []string) (int64, error) {
	const chunk = 400
	var total int64
	for start := 0; start < len(paths); start += chunk {
		end := start + chunk
		if end > len(paths) {
			end = len(paths)
		}
		part := paths[start:end]
		args := make([]any, 0, len(part))
		for _, p := range part {
			args = append(args, p)
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return total, err
		}
		// 健康结论与索引行同删：只删 files 会留下体检页的幽灵路径（v3.10.8 同一教训）。
		// 两处拼接内容均为 len(part) 个问号，路径值走 args 绑定；G202 属误报。
		// v3.10.13（P2）：健康结论属于派生数据，删不掉不该拖累索引清理——
		// 旧实现一失败就整批回滚，用户看到「清理失败」但索引行一条没少。
		// #nosec G202 -- 拼接内容仅为 len(part) 个问号，无外部输入
		if _, err := tx.ExecContext(ctx, `DELETE FROM health_results WHERE path IN (`+ph+`)`, args...); err != nil {
			applog.Warn("清理幽灵索引时删除健康结论失败，索引行照常清理：%v", err)
		}
		// v3.17.6（F-24 / F-23）：标签按 file_id 关联、探测缓存按路径关联，同样会留下孤儿。
		// #nosec G202 -- 拼接内容仅为 len(part) 个问号，无外部输入
		if _, err := tx.ExecContext(ctx, `DELETE FROM file_tags WHERE file_id IN (SELECT id FROM files WHERE path IN (`+ph+`))`, args...); err != nil {
			applog.Warn("清理幽灵索引时删除文件标签失败，索引行照常清理：%v", err)
		}
		// #nosec G202 -- 同上，占位符由切片长度生成
		if _, err := tx.ExecContext(ctx, `DELETE FROM probe_cache WHERE path IN (`+ph+`)`, args...); err != nil {
			applog.Warn("清理幽灵索引时删除探测缓存失败，索引行照常清理：%v", err)
		}
		// #nosec G202 -- 同上，占位符由切片长度生成
		res, err := tx.ExecContext(ctx, `DELETE FROM files WHERE path IN (`+ph+`)`, args...)
		if err != nil {
			_ = tx.Rollback()
			return total, err
		}
		n, _ := res.RowsAffected()
		if err := tx.Commit(); err != nil {
			return total, err
		}
		total += n
	}
	if total > 0 {
		s.dataVersion.Add(1)
	}
	return total, nil
}

func (s *Store) DeleteByPath(ctx context.Context, path string) error {
	// v3.10.8：健康结论与索引行同删。此前只删 files，健康页会永久留着
	// 已删除/已改名路径的旧结论，点「隔离/改名」必然失败（幽灵行）。
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM health_results WHERE path=?`, path); err != nil {
		return err
	}
	// v3.17.6（F-24 / F-23）：同事务清掉标签与探测缓存，避免删文件后留下孤儿行。
	if _, err := tx.ExecContext(ctx, `DELETE FROM file_tags WHERE file_id IN (SELECT id FROM files WHERE path=?)`, path); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM probe_cache WHERE path=?`, path); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE path=?`, path); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.dataVersion.Add(1)
	return nil
}

// RenameFilePathsBatch 改名后批量同步 files 索引（v3.10.0 SYNC-01/02）。
//
// 与 UpdatePathsBatch（rename_preset.go）的区别：那个只改 path，用于「名字不变、位置变」；
// 改名场景必须连 name 与 ext 一起刷新，否则按扩展名筛选、同名分组会继续拿旧值。
// 改名不动文件内容，这里刻意不碰 quick/hash/audio/thumb 等指纹列：
// 健康页一次能改几千个文件名，照 UpdatePath 逐条重算会把磁盘读满。
// 返回索引里没命中的新路径，调用方决定是否补进索引（零字节、尚未扫描的文件不在库里）。
func (s *Store) RenameFilePathsBatch(ctx context.Context, pairs [][2]string) ([]string, error) {
	// v3.21.0：按 1000 条一片提交。40 万条改名如果整批一个事务，WAL 会被顶满，
	// 中途失败也要整批回滚；分片后每片独立落库，进度与内存都可控。
	const chunk = 1000
	var missing []string
	for start := 0; start < len(pairs); start += chunk {
		end := start + chunk
		if end > len(pairs) {
			end = len(pairs)
		}
		m, err := s.renameFilePathsChunk(ctx, pairs[start:end])
		if err != nil {
			return missing, err
		}
		missing = append(missing, m...)
	}
	return missing, nil
}

// renameFilePathsChunk 是 RenameFilePathsBatch 的单片实现（一个事务）。
func (s *Store) renameFilePathsChunk(ctx context.Context, pairs [][2]string) ([]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// 新路径可能残留旧行的历史脏数据，先清掉，否则 UPDATE 会撞 path 唯一约束。
	delStmt, err := tx.PrepareContext(ctx, `DELETE FROM files WHERE path=?`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = delStmt.Close() }()
	upStmt, err := tx.PrepareContext(ctx, `UPDATE files SET path=?, name=?, ext=? WHERE path=?`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = upStmt.Close() }()

	var missing []string
	touched := false
	for _, pr := range pairs {
		oldPath, newPath := pr[0], pr[1]
		name := filepath.Base(newPath)
		ext := fname.Ext(name)
		if _, err := delStmt.ExecContext(ctx, newPath); err != nil {
			return nil, err
		}
		res, err := upStmt.ExecContext(ctx, newPath, name, ext, oldPath)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			missing = append(missing, newPath)
			continue
		}
		touched = true
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if touched {
		s.dataVersion.Add(1)
	}
	return missing, nil
}

func (s *Store) GetFileByPath(ctx context.Context, path string) (*FileRow, error) {
	return s.scanFile(s.db.QueryRowContext(ctx, fileCols+` FROM files WHERE path=?`, path))
}

// FilterActivePaths 返回 paths 中仍以 active 存在于索引里的子集，顺序与入参一致。
// v3.10.10（第五轮 P1-4）：结果页快照可能比索引旧，已隔离 / 已改名 / 被外部删除的成员
// 不能再被当作「组里还在的那一份」，否则「同组至少保留一份」会被陈旧成员放行。
// 分批拼 IN：分组可能上千个成员，一次绑定过多参数会触到 SQLite 变量上限。
func (s *Store) FilterActivePaths(ctx context.Context, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	uniq := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		// 不在这里 Clean：库里的 path 是原始绝对路径，Windows 下 Clean 会改成分隔符，
		// 反而对不上；Linux 目标上引擎传进来的路径本就已经 Clean 过。
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		uniq = append(uniq, p)
	}
	const chunk = 400
	out := make([]string, 0, len(uniq))
	for start := 0; start < len(uniq); start += chunk {
		end := start + chunk
		if end > len(uniq) {
			end = len(uniq)
		}
		marks := make([]string, 0, end-start)
		args := make([]any, 0, end-start)
		for _, p := range uniq[start:end] {
			marks = append(marks, "?")
			args = append(args, p)
		}
		// 占位符由固定字符串 "?" 拼成，路径值全部走 args 绑定参数，不拼接用户输入。
		rows, err := s.db.QueryContext(ctx, `SELECT path FROM files WHERE status='active' AND path IN (`+strings.Join(marks, ",")+`)`, args...) // #nosec G202 -- 仅拼接 "?" 占位符，值走参数绑定
		if err != nil {
			return nil, err
		}
		live := make(map[string]bool, end-start)
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				_ = rows.Close()
				return nil, err
			}
			live[p] = true
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		for _, p := range uniq[start:end] {
			if live[p] {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// DataVersion 返回文件数据版本（v3.8.0）。搜索快照用它判断结果是否过期。
func (s *Store) DataVersion() int64 { return s.dataVersion.Load() }

// WalkActiveFiles 逐个读取活动文件行，供搜索阶段全量物化使用（v3.8.0）。
// 用回调而不是一次性返回切片：调用方可以边读边建组、边更新进度、边检查取消。
func (s *Store) WalkActiveFiles(ctx context.Context, minSize int64, fn func(FileRow) error) error {
	// v3.10.10（第七轮 P1-3）：去掉 ORDER BY path。搜索/清理计划拿到行后都会重新分组
	// 并自行排序，全表排序只是在 SQLite 里多建一个临时 B-tree 和一次排序 IO。
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files WHERE status='active' AND size>=?`, minSize)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := s.scanFile(rows)
		if err != nil {
			return err
		}
		if err := fn(*f); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) GetFileByID(ctx context.Context, id int64) (*FileRow, error) {
	return s.scanFile(s.db.QueryRowContext(ctx, fileCols+` FROM files WHERE id=?`, id))
}

// fileColsNoSelect 是 files 表的固定读取列（scanFile 的顺序必须与之一致）。
// 拆成「列清单」和「带 SELECT 的完整语句」两种常量，是为了让需要额外前缀列
// （如分组键）的查询不必用字符串切片拼掉 SELECT 关键字。
const fileColsNoSelect = `id,path,volume,name,size,alloc,mtime,ctime,nlink,inode,dev,ext,quick,hash,duration,thumb,width,height,codec,bit_rate,sample_rate,img_hash`

const fileCols = `SELECT ` + fileColsNoSelect

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanFile(r rowScanner) (*FileRow, error) {
	return s.scanFileExtra(r)
}

// scanFileExtra 在文件列之前多读 extra 个列（如 SQL 计算出的分组键）。
// 列顺序必须与调用方的 SELECT 保持一致。
func (s *Store) scanFileExtra(r rowScanner, extra ...any) (*FileRow, error) {
	var f FileRow
	var quick, hash sql.NullString
	var dur sql.NullFloat64
	var thumb sql.NullString
	var imgHash sql.NullString
	dest := append(extra, &f.ID, &f.Path, &f.Volume, &f.Name, &f.Size, &f.Alloc, &f.Mtime, &f.Ctime, &f.Nlink, &f.Inode, &f.Dev, &f.Ext, &quick, &hash, &dur, &thumb,
		&f.Width, &f.Height, &f.Codec, &f.BitRate, &f.SampleRate, &imgHash)
	err := r.Scan(dest...)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	f.Quick = quick.String
	f.Hash = hash.String
	f.Duration = dur.Float64
	f.Thumb = thumb.String
	f.ImgHash = imgHash.String
	return &f, nil
}

func (s *Store) SetQuick(ctx context.Context, id int64, quick string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE files SET quick=? WHERE id=?`, quick, id)
	if err == nil {
		// v3.17.4（F-05）：预哈希参与候选分组，写入后搜索快照必须判定过期。
		s.dataVersion.Add(1)
	}
	return err
}

func (s *Store) SetHash(ctx context.Context, id int64, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE files SET hash=? WHERE id=?`, hash, id)
	if err == nil {
		// v3.17.4（F-05）：全量哈希是查重结论的依据，同样要让快照失效。
		s.dataVersion.Add(1)
	}
	return err
}

// ClearHash 让单个文件的哈希失效（配合算法变更或手工强制重算）。
// v3.9.10 备注：当前零调用，保留给「按文件强制重算」入口使用。
func (s *Store) ClearHash(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE files SET hash=NULL,quick=NULL WHERE id=?`, id)
	if err == nil {
		// v3.17.4（F-05）：清空哈希会改变后续分组结果，快照必须过期。
		s.dataVersion.Add(1)
	}
	return err
}

// SetVideo 写入视频指纹。size/mtime 是分析开始时读到的大小与修改时间，
// 作为乐观条件参与 WHERE（v3.10.8）：分析期间文件被覆盖时受影响行数为 0，
// 调用方需要重新入队，避免旧指纹永久挂在新内容上。返回 false 表示未命中。
func (s *Store) SetVideo(ctx context.Context, id, size, mtime, ctime int64, audio []byte, thumb string, duration float64, meta MediaMeta) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE files SET audio=?, thumb=?, duration=?, width=?, height=?, codec=?, bit_rate=?, sample_rate=?,
		video_error=NULL, video_at=?, video_tries=video_tries+1 WHERE id=? AND size=? AND mtime=? AND (ctime=0 OR ?=0 OR ctime=?)`,
		audio, thumb, duration, meta.Width, meta.Height, meta.Codec, meta.BitRate, meta.SampleRate, time.Now().Unix(), id, size, mtime, ctime, ctime)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// VideoResult 是视频分析的落库结果；Err 非空表示分析失败。
// Size/Mtime 是分析时读到的大小与修改时间，落库时作为乐观条件（v3.10.4）。
type VideoResult struct {
	ID    int64
	Size  int64
	Mtime int64
	// Ctime 参与乐观条件（v3.10.10，P2-5）；0 表示未采集，只看 size+mtime。
	Ctime    int64
	Audio    []byte
	Thumb    string
	Duration float64
	MediaMeta
	Err string
}

// SetVideoBatch 在一个事务内批量写入视频分析结果。
func (s *Store) SetVideoBatch(ctx context.Context, items []VideoResult) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for _, it := range items {
		if it.Err != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE files SET audio=NULL, thumb='', duration=NULL, video_error=?, video_at=?, video_tries=video_tries+1
				WHERE id=? AND size=? AND mtime=? AND (ctime=0 OR ?=0 OR ctime=?)`,
				it.Err, now, it.ID, it.Size, it.Mtime, it.Ctime, it.Ctime); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET audio=?, thumb=?, duration=?, width=?, height=?, codec=?, bit_rate=?, sample_rate=?,
			video_error=NULL, video_at=?, video_tries=video_tries+1
			WHERE id=? AND size=? AND mtime=? AND (ctime=0 OR ?=0 OR ctime=?)`,
			it.Audio, it.Thumb, it.Duration, it.Width, it.Height, it.Codec, it.BitRate, it.SampleRate, now, it.ID, it.Size, it.Mtime, it.Ctime, it.Ctime); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MigrateFingerprintDim 使旧版本/不同维度的音频指纹失效，等待重新分析。
func (s *Store) MigrateFingerprintDim(ctx context.Context, dim int) error {
	// v3.10.11（第六轮 P2-4）：先用 meta 记下已迁移的维度，维度没变直接返回。
	// 否则每次启动都会对 files 做两次全表 UPDATE（34 万行、无可用索引），
	// 在飞牛这类低功耗机器上表现为「每次开机都要空转一会儿」。
	var cur string
	loadErr := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='fingerprint_dim'`).Scan(&cur)
	if loadErr == nil && cur == fmt.Sprint(dim) {
		return nil
	}
	if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
		return loadErr
	}
	// v3.10.8（P2-12）：清空音频指纹时必须同时把视频状态归零。
	// 否则这些行仍带 video_at>0 且无 video_error，VideoCandidates 认为「已分析过」永不重选，
	// 缩略图/时长停留在旧维度结果上。
	_, err := s.db.ExecContext(ctx, `UPDATE files SET audio=NULL, thumb='', duration=NULL,
		video_at=0, video_error=NULL, video_tries=0
		WHERE audio IS NOT NULL AND length(audio)<>? AND CAST(audio AS TEXT) NOT LIKE 'ERR:%'`, dim)
	if err != nil {
		return err
	}
	// 片头指纹同属音频维度：失败标记一并清掉，否则上一轮的 intro_error 会挡住重新分析。
	_, err = s.db.ExecContext(ctx, `UPDATE files SET intro_audio=NULL, intro_thumbs='', intro_seconds=0,
		intro_at=0, intro_error=NULL
		WHERE intro_audio IS NOT NULL AND length(intro_audio)<>?`, dim)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('fingerprint_dim',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fmt.Sprint(dim))
	return err
}

// MigrateVideoAlgo 在视频指纹算法版本变化时清空视频与片头缓存，等待重新分析。
func (s *Store) MigrateVideoAlgo(ctx context.Context, version int) error {
	var cur string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='video_algo'`).Scan(&cur)
	if err == nil && cur == fmt.Sprint(version) {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE files SET audio=NULL, thumb='', duration=NULL, video_error=NULL, video_at=0, video_tries=0,
		intro_audio=NULL, intro_thumbs='', intro_seconds=0, intro_at=0, intro_error=NULL
		WHERE status='active' AND ext IN (`+videoExtSQL()+`)`) // #nosec G202 -- 片段仅由常量 videoExts 生成，不含任何外部输入
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('video_algo',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fmt.Sprint(version))
	return err
}

// EnsureHashAlgo 在哈希算法或预哈希抽样规则变化时清空旧结果，返回是否真的清掉了数据。
// 旧 SHA-256 值与 BLAKE3 值不可比较，混用会让重复文件无法成组。
func (s *Store) EnsureHashAlgo(ctx context.Context, algo string) (bool, error) {
	var cur string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='hash_algo'`).Scan(&cur)
	if err == nil && cur == algo {
		return false, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE quick IS NOT NULL OR hash IS NOT NULL`).Scan(&n); err != nil {
		return false, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE files SET quick=NULL, hash=NULL`); err != nil {
		return false, err
	}
	// v3.17.4（F-05）：算法变更让全部哈希失效，搜索结果与分组都随之改变。
	s.dataVersion.Add(1)
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('hash_algo',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, algo)
	return n > 0, err
}

// HasVideoState 表示该文件已分析过（成功或失败），避免重复入队。
// 无音轨视频成功分析后 audio 仍为 NULL，因此以 video_at 为准（v1.42.0，BUG-01）。
func (s *Store) HasVideoState(ctx context.Context, id int64) (bool, error) {
	var done int
	err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN video_at > 0 OR video_error IS NOT NULL THEN 1 ELSE 0 END
		FROM files WHERE id=?`, id).Scan(&done)
	if err != nil {
		return false, err
	}
	return done == 1, nil
}

// SetVideoError 记录视频分析失败，同样带 size/mtime/ctime 乐观条件：
// 新文件不应被旧内容的错误压住重试窗口（v3.10.8）。ctime 与 SetVideo/SetIntro 对齐
// （v3.10.10 P2-5）：rsync -t、备份还原这类「size 与 mtime 都不变、只换内容」的覆盖
// 会让错误结论落到新内容上，新文件被误标分析失败且 7 天内不重试。
func (s *Store) SetVideoError(ctx context.Context, id, size, mtime, ctime int64, errMsg string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE files SET audio=NULL, thumb='', duration=NULL, video_error=?, video_at=?, video_tries=video_tries+1
		WHERE id=? AND size=? AND mtime=? AND (ctime=0 OR ?=0 OR ctime=?)`, errMsg, time.Now().Unix(), id, size, mtime, ctime, ctime)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ---- 相同片头分析 ----

// IntroResult 是片头分析的落库结果；Err 非空表示分析失败。
// Size/Mtime 是分析时读到的大小与修改时间，落库时作为乐观条件（v3.10.4）。
type IntroResult struct {
	ID    int64
	Size  int64
	Mtime int64
	// Ctime 参与乐观条件（v3.10.10，P2-5）；0 表示未采集，只看 size+mtime。
	Ctime    int64
	Audio    []byte
	Thumbs   string
	Duration float64
	Seconds  int
	MediaMeta
	Err string
}

// SetIntro 写入单个文件的片头指纹；已有的有效媒体属性不会被空值覆盖。
// v3.10.10（P2-11）：与批量版本一致，加 size+mtime 乐观条件——分析期间文件被
// 替换时，结果不能落到已经变了的那一行上（否则下一轮会拿旧指纹当有效结果跳过）。
func (s *Store) SetIntro(ctx context.Context, id, size, mtime, ctime int64, audio []byte, thumbs string, duration float64, seconds int, meta MediaMeta) error {
	_, err := s.db.ExecContext(ctx, `UPDATE files SET intro_audio=?, intro_thumbs=?, intro_seconds=?, intro_at=?, intro_error=NULL,
		duration=COALESCE(duration, ?),
		width=CASE WHEN ?>0 THEN ? ELSE width END,
		height=CASE WHEN ?>0 THEN ? ELSE height END,
		codec=CASE WHEN ?<>'' THEN ? ELSE codec END,
		bit_rate=CASE WHEN ?>0 THEN ? ELSE bit_rate END,
		sample_rate=CASE WHEN ?>0 THEN ? ELSE sample_rate END
		WHERE id=? AND size=? AND mtime=? AND (ctime=0 OR ?=0 OR ctime=?)`,
		audio, thumbs, seconds, time.Now().Unix(), duration,
		meta.Width, meta.Width, meta.Height, meta.Height, meta.Codec, meta.Codec,
		meta.BitRate, meta.BitRate, meta.SampleRate, meta.SampleRate, id, size, mtime, ctime, ctime)
	return err
}

// SetIntroBatch 在一个事务内批量写入片头分析结果。
func (s *Store) SetIntroBatch(ctx context.Context, items []IntroResult) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for _, it := range items {
		if it.Err != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE files SET intro_audio=NULL, intro_thumbs='', intro_seconds=0, intro_at=?, intro_error=?
				WHERE id=? AND size=? AND mtime=? AND (ctime=0 OR ?=0 OR ctime=?)`,
				now, it.Err, it.ID, it.Size, it.Mtime, it.Ctime, it.Ctime); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET intro_audio=?, intro_thumbs=?, intro_seconds=?, intro_at=?, intro_error=NULL,
			duration=COALESCE(duration, ?),
			width=CASE WHEN ?>0 THEN ? ELSE width END,
			height=CASE WHEN ?>0 THEN ? ELSE height END,
			codec=CASE WHEN ?<>'' THEN ? ELSE codec END,
			bit_rate=CASE WHEN ?>0 THEN ? ELSE bit_rate END,
			sample_rate=CASE WHEN ?>0 THEN ? ELSE sample_rate END
			WHERE id=? AND size=? AND mtime=? AND (ctime=0 OR ?=0 OR ctime=?)`,
			it.Audio, it.Thumbs, it.Seconds, now, it.Duration,
			it.Width, it.Width, it.Height, it.Height, it.Codec, it.Codec,
			it.BitRate, it.BitRate, it.SampleRate, it.SampleRate, it.ID, it.Size, it.Mtime, it.Ctime, it.Ctime); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// IntroCandidates 返回需要按当前 N 计算片头指纹的文件。
// v3.10.3：按 path 续扫，避免每批都从头扫描已处理的前缀。
func (s *Store) IntroCandidates(ctx context.Context, seconds int, after string, limit int) ([]FileRow, error) {
	retryBefore := time.Now().Add(-videoRetryAfter).Unix()
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files WHERE status='active' AND ext IN (`+videoExtSQL()+`)
		AND ((intro_error IS NULL OR intro_error='') OR intro_at<?)
		AND (intro_seconds IS NULL OR intro_seconds<>?)
		AND path>? ORDER BY path LIMIT ?`, retryBefore, seconds, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []FileRow
	for rows.Next() {
		f, err := s.scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// CountIntroCandidates 返回待按当前 N 分析片头的文件数。
func (s *Store) CountIntroCandidates(ctx context.Context, seconds int) (int64, error) {
	var n int64
	retryBefore := time.Now().Add(-videoRetryAfter).Unix()
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE status='active' AND ext IN (`+videoExtSQL()+`)
		AND ((intro_error IS NULL OR intro_error='') OR intro_at<?)
		AND (intro_seconds IS NULL OR intro_seconds<>?)`, retryBefore, seconds).Scan(&n)
	return n, err
}

// IntroAnalyzedCount 返回已按给定 N 完成片头分析的文件数。
func (s *Store) IntroAnalyzedCount(ctx context.Context, seconds int) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE status='active' AND intro_seconds=?`, seconds).Scan(&n)
	return n, err
}

// ResetIntroAnalysis 清空片头分析结果；N 变化或手动重算时使用。
func (s *Store) ResetIntroAnalysis(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE files SET intro_audio=NULL, intro_thumbs='', intro_seconds=0, intro_at=0, intro_error=NULL
		WHERE status='active' AND ext IN (`+videoExtSQL()+`)`) // #nosec G202 -- 片段仅由常量 videoExts 生成，不含任何外部输入
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// IntroFP 是参与片头分组的文件指纹。
type IntroFP struct {
	ID          int64
	Path        string
	Volume      string
	Name        string
	Size        int64
	Mtime       int64
	Hash        string
	IntroAudio  []byte
	IntroThumb  string
	IntroSecond int
	Audio       []byte
	Thumb       string
	Duration    float64
	MediaMeta
}

// IntroFingerprints 返回已按给定 N 完成片头分析的文件（含整段指纹，用于排除整段重复）。
func (s *Store) IntroFingerprints(ctx context.Context, seconds int) ([]IntroFP, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,path,volume,name,size,mtime,COALESCE(hash,''),intro_audio,COALESCE(intro_thumbs,''),
		intro_seconds,audio,COALESCE(thumb,''),COALESCE(duration,0),width,height,codec,bit_rate,sample_rate FROM files
		WHERE status='active' AND intro_seconds=? ORDER BY id`, seconds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []IntroFP
	for rows.Next() {
		var v IntroFP
		var ia, au sql.NullString
		var thumb, ithumb sql.NullString
		var dur sql.NullFloat64
		if err := rows.Scan(&v.ID, &v.Path, &v.Volume, &v.Name, &v.Size, &v.Mtime, &v.Hash, &ia, &ithumb,
			&v.IntroSecond, &au, &thumb, &dur,
			&v.Width, &v.Height, &v.Codec, &v.BitRate, &v.SampleRate); err != nil {
			return nil, err
		}
		v.IntroAudio = []byte(ia.String)
		v.IntroThumb = ithumb.String
		v.Audio = []byte(au.String)
		v.Thumb = thumb.String
		v.Duration = dur.Float64
		out = append(out, v)
	}
	return out, rows.Err()
}

// CurrentGeneration 返回当前扫描代次。
func (s *Store) CurrentGeneration(ctx context.Context) int64 {
	var v string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='generation'`).Scan(&v); err != nil {
		return 0
	}
	var cur int64
	_, _ = fmt.Sscan(v, &cur)
	return cur
}

// FilesNeedingQuick 返回尚未计算预哈希的文件。
// v3.10.3：where 增加 path>after 续扫条件。调用方每批把最后一条 path 传回来，
// 已处理过的前缀不再重复扫描；path 是唯一键，语义与原来等价。
func (s *Store) FilesNeedingQuick(ctx context.Context, minSize int64, after string, limit int) ([]FileRow, error) {
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files WHERE status='active' AND quick IS NULL AND size>=? AND path>? ORDER BY path LIMIT ?`, minSize, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []FileRow
	for rows.Next() {
		f, err := s.scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (s *Store) CountNeedingQuick(ctx context.Context, minSize int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE status='active' AND quick IS NULL AND size>=?`, minSize).Scan(&n)
	return n, err
}

// RefreshDupQuick 重建 hash_dup_quick 表（v3.10.3）。
//
// 预哈希重复集合在一轮扫描内不会变化，没必要每取一批候选都重新聚合全表。
// 在进入全量哈希阶段前刷新一次，后续查询直接做主键等值匹配。
func (s *Store) RefreshDupQuick(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hash_dup_quick`); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO hash_dup_quick(quick)
		SELECT quick FROM files WHERE status='active' AND quick IS NOT NULL AND quick NOT LIKE 'ERR%'
		GROUP BY quick HAVING COUNT(*)>1`)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// FilesNeedingFullHash 返回预哈希已重复但缺少 BLAKE3 完整哈希的文件。
// v3.10.3：重复预哈希集合改读 hash_dup_quick，并按 path 续扫。
func (s *Store) FilesNeedingFullHash(ctx context.Context, minSize int64, after string, limit int) ([]FileRow, error) {
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files WHERE status='active' AND size>=? AND hash IS NULL
		AND quick IN (SELECT quick FROM hash_dup_quick) AND path>? ORDER BY path LIMIT ?`, minSize, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []FileRow
	for rows.Next() {
		f, err := s.scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (s *Store) CountNeedingFull(ctx context.Context, minSize int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE status='active' AND size>=? AND hash IS NULL
		AND quick IN (SELECT quick FROM hash_dup_quick)`, minSize).Scan(&n)
	return n, err
}

// CountQuickGroup 返回与给定预哈希相同的活动文件数。
func (s *Store) CountQuickGroup(ctx context.Context, quick string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE status='active' AND quick=?`, quick).Scan(&n)
	return n, err
}

// FullHashForQuick 将同一预哈希组内缺失完整哈希的文件返回。
func (s *Store) FullHashForQuick(ctx context.Context, quick string, limit int) ([]FileRow, error) {
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files WHERE status='active' AND quick=? AND hash IS NULL AND quick NOT LIKE 'ERR%' LIMIT ?`, quick, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []FileRow
	for rows.Next() {
		f, err := s.scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// videoRetryAfter 是视频/片头分析失败后的重试间隔。
//
// v3.10.4：失败行会被候选查询排除，一次 ffmpeg 超时就会让该文件永久缺指纹；
// 给一个冷却期后允许重试，既不会每轮扫描反复烧 CPU，也不会永久放弃。
const videoRetryAfter = 7 * 24 * time.Hour

// VideoCandidates 返回需要分析视频指纹的文件。
// v1.42.0：用 video_at 判定"是否分析过"，不能再用 audio IS NULL。
// 无音轨/静音视频的音频指纹本来就是空（audio 存 NULL），但画面指纹已成功，
// 旧条件会把它们当成未分析反复选中，导致扫描永不结束（BUG-01）。
// v3.10.3：按 path 续扫，避免每批都从头扫描已处理的前缀。
func (s *Store) VideoCandidates(ctx context.Context, after string, limit int) ([]FileRow, error) {
	retryBefore := time.Now().Add(-videoRetryAfter).Unix()
	rows, err := s.db.QueryContext(ctx, fileCols+` FROM files WHERE status='active'
		AND (video_at=0 OR (video_error IS NOT NULL AND video_error<>'' AND video_at<?))
		AND ext IN (`+videoExtSQL()+`)
		AND path>? ORDER BY path LIMIT ?`, retryBefore, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []FileRow
	for rows.Next() {
		f, err := s.scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// videoExts 是编译期写死的视频扩展名白名单，是 videoExtSQL 的唯一来源。
// 这里只允许出现字面量扩展名，任何来自配置或请求的值都不得加入，否则 SQL 拼接将失效。
var videoExts = []string{"mp4", "mkv", "avi", "mov", "wmv", "flv", "m4v", "ts", "mts", "m2ts", "mpg", "mpeg", "rmvb", "rm", "3gp", "webm", "vob", "ogv", "asf", "f4v", "divx", "m2v", "mxf", "dat"}

func videoExtSQL() string {
	q := make([]string, len(videoExts))
	for i, e := range videoExts {
		q[i] = "'" + e + "'"
	}
	return strings.Join(q, ",")
}

// videoFpCondition 是「视频指纹可用于分组」的统一条件（v3.10.10，P1-1）。
// 无音轨或整段静音的视频分析成功后 audio 为 NULL，但 thumb/duration 有值——
// 它们同样能按画面分组，不能因为缺音频被永久排除。分析失败的行由 video_error 排除。
// VideoFingerprints 与 Counts.Videos 必须共用这份条件，否则概览数会与实际分组漂移。
const videoFpCondition = `status='active' AND duration IS NOT NULL AND (video_error IS NULL OR video_error='')
	AND (audio IS NOT NULL OR (thumb IS NOT NULL AND thumb<>''))
	AND (audio IS NULL OR substr(CAST(audio AS TEXT),1,4) NOT LIKE 'ERR%')`

// VideoFingerprints 返回可参与相似分组的视频行（含音频或缩略图指纹）。
func (s *Store) VideoFingerprints(ctx context.Context) ([]VideoFP, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,path,volume,name,size,mtime,audio,thumb,duration,width,height,codec,bit_rate,sample_rate FROM files
		WHERE `+videoFpCondition+`
		AND ext IN (`+videoExtSQL()+`)`) // #nosec G202 -- 片段仅由常量 videoExts 生成，不含任何外部输入
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []VideoFP
	for rows.Next() {
		var v VideoFP
		var thumb sql.NullString
		var dur sql.NullFloat64
		if err := rows.Scan(&v.ID, &v.Path, &v.Volume, &v.Name, &v.Size, &v.Mtime, &v.Audio, &thumb, &dur,
			&v.Width, &v.Height, &v.Codec, &v.BitRate, &v.SampleRate); err != nil {
			return nil, err
		}
		v.Thumb = thumb.String
		v.Duration = dur.Float64
		out = append(out, v)
	}
	return out, rows.Err()
}

type VideoFP struct {
	ID       int64
	Path     string
	Volume   string
	Name     string
	Size     int64
	Mtime    int64
	Audio    []byte
	Thumb    string
	Duration float64
	MediaMeta
}

// Counts 汇总首页统计。
type Counts struct {
	Files       int64 `json:"files"`
	Bytes       int64 `json:"bytes"`
	ExactGroups int64 `json:"exact_groups"`
	ExactFiles  int64 `json:"exact_files"`
	ExactWaste  int64 `json:"exact_waste"`
	NameGroups  int64 `json:"name_groups"`
	NameWaste   int64 `json:"name_waste"`
	TrashFiles  int64 `json:"trash_files"`
	TrashBytes  int64 `json:"trash_bytes"`
	Videos      int64 `json:"videos"`
}

// countsTTL 是状态聚合结果的缓存时长。
//
// 界面每 15 秒轮询一次 /api/status，若每次都对 files 表做聚合查询，
// 机械盘就会被周期性唤醒、永远睡不成。缓存让空闲时的轮询变成纯内存读；
// 写路径通过 InvalidateCounts 主动失效，扫描期间的数字最多滞后一分钟。
const countsTTL = 60 * time.Second

func (s *Store) Counts(ctx context.Context, minSize int64) (Counts, error) {
	s.cmu.Lock()
	// 冻结期间不看 TTL：扫描时写路径每批都调 InvalidateCounts，
	// 若照常按时效重算，/api/status 每 15 秒就会重跑一次全表聚合（第七轮 P2-1）。
	if s.cOK && s.cMin == minSize && (s.cFrozen || time.Since(s.cAt) < countsTTL) {
		c := s.cVal
		s.cmu.Unlock()
		return c, nil
	}
	s.cmu.Unlock()

	c, err := s.countsDB(ctx, minSize)
	if err != nil {
		return c, err
	}
	s.cmu.Lock()
	s.cVal, s.cAt, s.cMin, s.cOK = c, time.Now(), minSize, true
	s.cmu.Unlock()
	return c, nil
}

// InvalidateCounts 使聚合缓存失效。引擎在每次写操作后调用。
func (s *Store) InvalidateCounts() {
	s.cmu.Lock()
	// 扫描期间写路径会连续调用本方法，冻结时保留上一份结果，
	// 避免缓存反复失效导致界面轮询频率等于全表聚合频率（第七轮 P2-1）。
	if !s.cFrozen {
		s.cOK = false
	}
	s.cmu.Unlock()
}

// SetCountsFrozen 冻结/解冻概览聚合缓存（v3.10.11，第七轮 P2-1）。
//
// 扫描开始时冻结：此后的写操作不再清缓存，Counts 直接返回上一份结果；
// 扫描收尾解冻并强制失效一次，让界面拿到最终数字。
func (s *Store) SetCountsFrozen(frozen bool) {
	s.cmu.Lock()
	s.cFrozen = frozen
	if !frozen {
		s.cOK = false
	}
	s.cmu.Unlock()
}

func (s *Store) countsDB(ctx context.Context, minSize int64) (Counts, error) {
	// v3.10.8：与分组查询同一口径，0 字节文件不计入重复分组（内容必然相同、无可回收空间）。
	if minSize < 1 {
		minSize = 1
	}
	var c Counts
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size),0) FROM files WHERE status='active'`).Scan(&c.Files, &c.Bytes)
	if err != nil {
		return c, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(rows),0), COALESCE(SUM(total-MAXONE),0) FROM (
		SELECT hash, SUM(dsz) AS total, MAX(dsz) AS MAXONE, SUM(rn) AS rows FROM (
			SELECT hash, volume, inode, MAX(CASE WHEN alloc>0 THEN alloc ELSE size END) AS dsz, COUNT(*) AS rn FROM files
			WHERE status='active' AND hash IS NOT NULL AND hash<>'' AND hash NOT LIKE 'ERR%' AND size>=? GROUP BY hash, volume, inode
		) GROUP BY hash HAVING SUM(rn)>1)`, minSize).
		Scan(&c.ExactGroups, &c.ExactFiles, &c.ExactWaste)
	if err != nil {
		return c, err
	}
	var nameFiles int64
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(c),0), COALESCE(SUM(total-MAXONE),0) FROM (
		SELECT COUNT(*) c, SUM(CASE WHEN alloc>0 THEN alloc ELSE size END) total,
		       MAX(CASE WHEN alloc>0 THEN alloc ELSE size END) MAXONE FROM files
		WHERE status='active' AND size>=? GROUP BY lower(name) HAVING COUNT(*)>1)`, minSize).
		Scan(&c.NameGroups, &nameFiles, &c.NameWaste)
	if err != nil {
		return c, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size),0) FROM quarantine`).Scan(&c.TrashFiles, &c.TrashBytes)
	if err != nil {
		return c, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE `+videoFpCondition).Scan(&c.Videos)
	return c, err
}

// CountVideoCandidates 与 VideoCandidates 必须用同一套条件，
// 否则进度条总数和实际候选漂移（无音轨文件会让 done 超过 total）。
func (s *Store) CountVideoCandidates(ctx context.Context) (int64, error) {
	var n int64
	retryBefore := time.Now().Add(-videoRetryAfter).Unix()
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE status='active'
		AND (video_at=0 OR (video_error IS NOT NULL AND video_error<>'' AND video_at<?))
		AND ext IN (`+videoExtSQL()+`)`, retryBefore).Scan(&n)
	return n, err
}

// VideoFailure 描述一个视频分析失败的文件。
type VideoFailure struct {
	ID    int64  `json:"id"`
	Path  string `json:"path"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
	Error string `json:"error"`
	At    int64  `json:"at"`
	Tries int    `json:"tries"`
}

// VideoFailures 返回视频分析失败的文件列表（按最近失败时间倒序）。
func (s *Store) VideoFailures(ctx context.Context) ([]VideoFailure, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,path,name,size,mtime,COALESCE(video_error,''),video_at,video_tries FROM files
		WHERE status='active' AND video_error IS NOT NULL AND video_error<>'' ORDER BY video_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []VideoFailure
	for rows.Next() {
		var v VideoFailure
		if err := rows.Scan(&v.ID, &v.Path, &v.Name, &v.Size, &v.Mtime, &v.Error, &v.At, &v.Tries); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ResetVideoAnalysis 清空视频分析结果；ids 为空时作用于全部视频文件。
// 保留 duration 供仍有效的片头指纹复用，重新分析视频时会覆盖。
func (s *Store) ResetVideoAnalysis(ctx context.Context, ids []int64) (int64, error) {
	base := `UPDATE files SET audio=NULL, thumb='', video_error=NULL, video_at=0, video_tries=0
		WHERE status='active'`
	if len(ids) == 0 {
		res, err := s.db.ExecContext(ctx, base+` AND ext IN (`+videoExtSQL()+`)`)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}
	args := make([]any, len(ids))
	ph := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		ph[i] = "?"
	}
	res, err := s.db.ExecContext(ctx, base+` AND id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- 回收站 ----

func (s *Store) AddTrash(ctx context.Context, origPath, trashPath, volume, hash, groupKey, reason string, size int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO quarantine(orig_path,trash_path,volume,size,hash,group_key,reason,deleted_at)
		VALUES(?,?,?,?,?,?,?,?)`, origPath, trashPath, volume, size, hash, groupKey, reason, time.Now().Unix())
	return err
}

func (s *Store) ListTrash(ctx context.Context) ([]TrashRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,orig_path,trash_path,volume,size,COALESCE(hash,''),deleted_at,COALESCE(reason,''),exported_at FROM quarantine ORDER BY deleted_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []TrashRow{}
	for rows.Next() {
		var t TrashRow
		if err := rows.Scan(&t.ID, &t.OrigPath, &t.TrashPath, &t.Volume, &t.Size, &t.Hash, &t.DeletedAt, &t.Reason, &t.ExportedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// 隔离区分页参数（v3.25.2）：界面一次最多拉 200 条，硬上限 500 条。
// 上限同时是 heal 的单批上限——分页路径只对返回的这一页做幽灵判定。
const (
	TrashPageDefault = 200
	TrashPageMax     = 500
)

// ErrTrashCursor 表示分页游标无法解析；Web 层据此回 400 而不是 500。
var ErrTrashCursor = errors.New("分页游标无效")

// TrashPage 是一页隔离记录与分页信息（v3.25.2）。
type TrashPage struct {
	Rows       []TrashRow
	Total      int
	HasMore    bool
	NextCursor string
}

// CountTrash 返回隔离区总行数。
func (s *Store) CountTrash(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quarantine`).Scan(&n)
	return n, err
}

// ListTrashPage 按 deleted_at DESC, id DESC 做 keyset 分页（v3.25.2）。
// cursor 为空表示第一页；limit 截断到 1..TrashPageMax。
// 多取一条判断是否还有下一页；游标取本页最后一行，即使它随后被判为幽灵行也仍然有效——
// keyset 只看排序键，不依赖该行还在表里。
func (s *Store) ListTrashPage(ctx context.Context, cursor string, limit int) (TrashPage, error) {
	if limit <= 0 {
		limit = TrashPageDefault
	}
	if limit > TrashPageMax {
		limit = TrashPageMax
	}
	page := TrashPage{Rows: []TrashRow{}}
	var afterAt, afterID int64
	if cursor != "" {
		at, id, ok := parseTrashCursor(cursor)
		if !ok {
			return page, ErrTrashCursor
		}
		afterAt, afterID = at, id
	}
	// afterAt=0 是「第一页」哨兵：deleted_at 是秒级时间戳，不存在 0 值行。
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,orig_path,trash_path,volume,size,COALESCE(hash,''),deleted_at,COALESCE(reason,''),exported_at
		 FROM quarantine
		 WHERE ? = 0 OR deleted_at < ? OR (deleted_at = ? AND id < ?)
		 ORDER BY deleted_at DESC, id DESC LIMIT ?`,
		afterAt, afterAt, afterAt, afterID, limit+1)
	if err != nil {
		return page, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var t TrashRow
		if err := rows.Scan(&t.ID, &t.OrigPath, &t.TrashPath, &t.Volume, &t.Size, &t.Hash, &t.DeletedAt, &t.Reason, &t.ExportedAt); err != nil {
			return page, err
		}
		page.Rows = append(page.Rows, t)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Rows) > limit {
		page.Rows = page.Rows[:limit]
		page.HasMore = true
	}
	// 总数读失败不影响本页数据，前端只在有 total 时显示「共 N 个」。
	if n, cerr := s.CountTrash(ctx); cerr == nil {
		page.Total = n
	}
	if page.HasMore && len(page.Rows) > 0 {
		last := page.Rows[len(page.Rows)-1]
		page.NextCursor = strconv.FormatInt(last.DeletedAt, 10) + ":" + strconv.FormatInt(last.ID, 10)
	}
	return page, nil
}

// parseTrashCursor 解析 "deleted_at:id"；只接受本包生成的格式。
func parseTrashCursor(s string) (at, id int64, ok bool) {
	sep := strings.IndexByte(s, ':')
	if sep <= 0 || sep == len(s)-1 {
		return 0, 0, false
	}
	at, err := strconv.ParseInt(s[:sep], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	id, err = strconv.ParseInt(s[sep+1:], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	// v4.1.1：本服务生成的游标里 deleted_at 是秒级时间戳、id 是自增主键，
	// 两者都恒为正数。负数或 0 是伪造/损坏的游标（afterAt=0 又是「第一页」哨兵，
	// 会让 0:5 静默返回第一页），必须拒绝，否则 Web 层拿不到 ErrTrashCursor、返不出 400。
	if at <= 0 || id <= 0 {
		return 0, 0, false
	}
	return at, id, true
}

// MarkTrashExported 把成功导出备份的条目打上时间戳；只影响传入的 id。
// 导出结束后调用，便于隔离区列表显示「已备份」徽标。
func (s *Store) MarkTrashExported(ctx context.Context, ids []int64, ts int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE quarantine SET exported_at=? WHERE id=?`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, id := range ids {
		if _, err := stmt.ExecContext(ctx, ts, id); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return err
		}
	}
	_ = stmt.Close()
	return tx.Commit()
}

func (s *Store) GetTrash(ctx context.Context, id int64) (*TrashRow, error) {
	var t TrashRow
	var hash sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,orig_path,trash_path,volume,size,hash,deleted_at FROM quarantine WHERE id=?`, id).
		Scan(&t.ID, &t.OrigPath, &t.TrashPath, &t.Volume, &t.Size, &hash, &t.DeletedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	t.Hash = hash.String
	return &t, nil
}

func (s *Store) DeleteTrashRows(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `DELETE FROM quarantine WHERE id=?`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, id := range ids {
		if _, err := stmt.ExecContext(ctx, id); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return err
		}
	}
	_ = stmt.Close()
	return tx.Commit()
}

func (s *Store) EmptyTrash(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM quarantine`)
	return err
}

// TrimLogEntry 是一条剪除记录（v1.16 引入 16.5.1，表 trim_log）。
type TrimLogEntry struct {
	ID               int64   `json:"id"`
	FilePath         string  `json:"file_path"`
	GroupKey         string  `json:"group_key"`
	Mode             string  `json:"mode"`
	StartOffset      float64 `json:"start_offset"`
	OriginalDuration float64 `json:"original_duration"`
	OutputDuration   float64 `json:"output_duration"`
	OutputPath       string  `json:"output_path"`
	Status           string  `json:"status"`
	ErrorMessage     string  `json:"error_message"`
	ValidationPassed bool    `json:"validation_passed"`
	ValidationDetail string  `json:"validation_detail"`
	QuarantinePath   string  `json:"quarantine_path"`
	CreatedAt        int64   `json:"created_at"`
}

// AddTrimLog 写入一条剪除记录。
func (s *Store) AddTrimLog(ctx context.Context, e TrimLogEntry) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO trim_log(file_path,group_key,mode,start_offset,original_duration,
		output_duration,output_path,status,error_message,validation_passed,validation_detail,quarantine_path,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, e.FilePath, e.GroupKey, e.Mode, e.StartOffset, e.OriginalDuration,
		e.OutputDuration, e.OutputPath, e.Status, e.ErrorMessage, boolInt(e.ValidationPassed), e.ValidationDetail, e.QuarantinePath, e.CreatedAt)
	return err
}

// ListTrimLog 倒序返回剪除记录。
func (s *Store) ListTrimLog(ctx context.Context, limit int) ([]TrimLogEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,file_path,group_key,mode,start_offset,original_duration,
		output_duration,output_path,status,error_message,validation_passed,validation_detail,quarantine_path,created_at
		FROM trim_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TrimLogEntry
	for rows.Next() {
		var e TrimLogEntry
		var vp int
		if err := rows.Scan(&e.ID, &e.FilePath, &e.GroupKey, &e.Mode, &e.StartOffset, &e.OriginalDuration,
			&e.OutputDuration, &e.OutputPath, &e.Status, &e.ErrorMessage, &vp, &e.ValidationDetail, &e.QuarantinePath, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.ValidationPassed = vp == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetTrimProbe 返回某组的边界探测缓存；无记录返回 nil。
func (s *Store) GetTrimProbe(ctx context.Context, groupKey string) (seconds float64, status string, ok bool) {
	var st string
	var sec float64
	err := s.db.QueryRowContext(ctx, `SELECT boundary_seconds,status FROM trim_probe WHERE group_key=?`, groupKey).Scan(&sec, &st)
	if err != nil {
		return 0, "", false
	}
	return sec, st, true
}

// SaveTrimProbe 缓存边界探测结果（按组 UPSERT）。
func (s *Store) SaveTrimProbe(ctx context.Context, groupKey string, seconds float64, status string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO trim_probe(group_key,boundary_seconds,probed_at,status)
		VALUES(?,?,?,?) ON CONFLICT(group_key) DO UPDATE SET boundary_seconds=excluded.boundary_seconds,
		probed_at=excluded.probed_at,status=excluded.status`, groupKey, seconds, time.Now().Unix(), status)
	return err
}

// MarkTrimmed 标记文件已剪除（替换模式时）。
func (s *Store) MarkTrimmed(ctx context.Context, path string, offset float64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE files SET trimmed=1, trimmed_offset=? WHERE path=?`, offset, path)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		// v3.17.4（F-05）：修剪标记会改变列表展示与后续清理候选，快照应过期。
		s.dataVersion.Add(1)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// LatestTrashByOrig 返回某原始路径最近一条隔离记录（trim 替换回滚用）。
func (s *Store) LatestTrashByOrig(ctx context.Context, orig string) (*TrashRow, bool) {
	var r TrashRow
	err := s.db.QueryRowContext(ctx, `SELECT id,orig_path,trash_path,volume,size,COALESCE(hash,''),deleted_at,COALESCE(reason,'')
		FROM quarantine WHERE orig_path=? ORDER BY id DESC LIMIT 1`, orig).
		Scan(&r.ID, &r.OrigPath, &r.TrashPath, &r.Volume, &r.Size, &r.Hash, &r.DeletedAt, &r.Reason)
	if err != nil {
		return nil, false
	}
	return &r, true
}
