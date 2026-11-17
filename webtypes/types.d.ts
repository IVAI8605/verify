// 前端类型声明（v4.0.0 自动生成，请勿手改）
//
// 本文件由 tools/gen_types.py 从 internal/web/contracts*.go 与 internal/store/*.go
// 的响应契约生成，源头是 Go struct 的 json tag —— 那是前后端字段名的唯一真相源。
//
// 重新生成：
//     python3 tools/gen_types.py
//
// 约束：
//   - 本文件不产出任何运行时代码，只做类型标注；
//   - 产物仍是 ui/*.js 原生脚本，由 go:embed 内嵌，零构建形态不变；
//   - 改后端响应字段 → 改 Go struct 的 json tag → 重新生成本文件。
//
// 已知噪音：TS2339（属性不存在于 HTMLElement/Element）不阻断类型门禁，
// 因为 getElementById 的返回类型没有收窄到具体元素类型。

// ── 基础别名 ────────────────────────────────────────────────────────────

/** 后端下发的通用错误响应。 */
interface ApiError {
  error: string;
}

/** 后端下发的简单成功响应。 */
interface ApiOk {
  ok: boolean;
}

/**
 * 尚未纳入契约的响应用它占位。
 *
 * 这些接口的返回体形态不稳定（导出文件、缩略图二进制、诊断信息等），
 * 或仍在改造中。用 AnyResp 标注等于「此处不做字段级检查」——
 * 等对应接口纳入契约后，把它换成具体类型。
 */
interface AnyResp {
  [key: string]: any;
}


// ── 后端响应契约（由 contracts*.go 生成）──────────────────────────────

/** 后端 Counts（10 个字段）。 */
interface Counts {
  files: number;
  bytes: number;
  exact_groups: number;
  exact_files: number;
  exact_waste: number;
  name_groups: number;
  name_waste: number;
  trash_files: number;
  trash_bytes: number;
  videos: number;
}

/** 后端 IntroFP（1 个字段）。 */
interface IntroFP extends StoreMediaMeta {
}

/** 后端 IntroResult（1 个字段）。 */
interface IntroResult extends StoreMediaMeta {
}

/** 后端 TrimLogEntry（14 个字段）。 */
interface TrimLogEntry {
  id: number;
  file_path: string;
  group_key: string;
  mode: string;
  start_offset: number;
  original_duration: number;
  output_duration: number;
  output_path: string;
  status: string;
  error_message: string;
  validation_passed: boolean;
  validation_detail: string;
  quarantine_path: string;
  created_at: number;
}

/** 后端 VideoFP（1 个字段）。 */
interface VideoFP extends StoreMediaMeta {
}

/** 后端 VideoFailure（8 个字段）。 */
interface VideoFailure {
  id: number;
  path: string;
  name: string;
  size: number;
  mtime: number;
  error: string;
  at: number;
  tries: number;
}

/** 后端 VideoResult（1 个字段）。 */
interface VideoResult extends StoreMediaMeta {
}

/** 后端 actionItemsResponse（2 个字段）。 */
interface ActionItemsResp {
  action: string;
  items: any;
}

/** 后端 authErrorResponse（2 个字段）。 */
interface AuthErrorResp {
  error: string;
  auth: boolean;
}

/** 后端 cacheClearedResponse（2 个字段）。 */
interface CacheClearedResp {
  ok: boolean;
  cleared: number;
}

/** 后端 cacheStatsResponse（7 个字段）。 */
interface CacheStatsResp {
  media: string;
  ssd_hint: boolean;
  cache_dir: string;
  classes: any;
  mem_budget_auto: boolean;
  mem_budget_mb: number;
  thumb_max_mb: number;
}

/** 后端 cleanupPlanResponse（1 个字段）。 */
interface CleanupPlanResp {
  plan: any;
}

/** 后端 cleanupPlansResponse（1 个字段）。 */
interface CleanupPlansResp {
  plans: any;
}

/** 后端 countErrorResponse（2 个字段）。 */
interface CountErrorResp {
  error: string;
  n: number;
}

/** 后端 countOKResponse（2 个字段）。 */
interface CountOKResp {
  ok: boolean;
  count: number;
}

/** 后端 countResponse（3 个字段）。 */
interface CountResp {
  ok: boolean;
  id?: number;
  count?: number;
}

/** 后端 doneFailedItemsResponse（3 个字段）。 */
interface DoneFailedItemsResp {
  done: number;
  failed: number;
  items: any;
}

/** 后端 doneFailedResponse（2 个字段）。 */
interface DoneFailedResp {
  done: number;
  failed: number;
}

/** 后端 errorResponse（1 个字段）。 */
interface ErrorResp {
  error: string;
}

/** 后端 failedReasonsResponse（2 个字段）。 */
interface FailedReasonsResp {
  failed: number;
  reasons: any;
}

/** 后端 filesLimitOrderResponse（3 个字段）。 */
interface FilesLimitOrderResp {
  files: StoreFileRow[];
  limit: number;
  order: string;
}

/** 后端 filesResponse（7 个字段）。 */
interface FilesResp {
  files: string;
  total?: number;
  limit?: number;
  order?: string;
  scanned_dirs?: number;
  path?: string;
  truncated?: boolean;
}

/** 后端 folderStatsResponse（3 个字段）。 */
interface FolderStatsResp {
  path: string;
  files: any;
  bytes: number;
}

/** 后端 groupsDisabledResponse（3 个字段）。 */
interface GroupsDisabledResp {
  groups: StoreGroup[];
  total: number;
  disabled: boolean;
}

/** 后端 groupsMediaResponse（3 个字段）。 */
interface GroupsMediaResp {
  groups: StoreGroup[];
  total: number;
  threshold: any;
}

/** 后端 groupsResponse（4 个字段）。 */
interface GroupsResp {
  groups: StoreGroup[];
  total: number;
  page: number;
  page_size: number;
}

/** 后端 groupsSimpleResponse（2 个字段）。 */
interface GroupsSimpleResp {
  groups: StoreGroup[];
  total: number;
}

/** 后端 healthItemsResponse（3 个字段）。 */
interface HealthItemsResp {
  kind: string;
  total: number;
  items: any;
}

/** 后端 healthResultsResponse（3 个字段）。 */
interface HealthResultsResp {
  kind: string;
  total: number;
  items: any;
}

/** 后端 healthStatusResponse（7 个字段）。 */
interface HealthStatusResp {
  progress: any;
  counts: any;
  uncertain: any;
  ffmpeg: any;
  last_audit: any;
  auto: any;
  probe_cache: any;
}

/** 后端 importGroupsResponse（3 个字段）。 */
interface ImportGroupsResp {
  session: any;
  groups: any;
  total_groups: number;
}

/** 后端 importResultResponse（6 个字段）。 */
interface ImportResultResp {
  session: any;
  total: number;
  groups: number;
  matched: number;
  unmatched: number;
  warnings?: string;
}

/** 后端 importSessionResponse（1 个字段）。 */
interface ImportSessionResp {
  session: any;
}

/** 后端 importSessionsResponse（1 个字段）。 */
interface ImportSessionsResp {
  sessions: any;
}

/** 后端 introGroupsResponse（7 个字段）。 */
interface IntroGroupsResp {
  groups: StoreGroup[];
  total: number;
  page: number;
  page_size: number;
  n: number;
  analyzed: number;
  pending: number;
}

/** 后端 itemsTotalResponse（2 个字段）。 */
interface ItemsTotalResp {
  items: any;
  total: number;
}

/** 后端 jobIDResponse（2 个字段）。 */
interface JobIDResp {
  job_id: number;
  ok: boolean;
}

/** 后端 kindOKResponse（2 个字段）。 */
interface KindOKResp {
  kind: string;
  ok: boolean;
}

/** 后端 labeledRemainingResponse（2 个字段）。 */
interface LabeledRemainingResp {
  labeled: number;
  remaining: number;
}

/** 后端 matchModeDisabledResponse（4 个字段）。 */
interface MatchModeDisabledResp {
  groups: StoreGroup[];
  total: number;
  disabled: boolean;
  reason?: string;
}

/** 后端 modeResponse（2 个字段）。 */
interface ModeResp {
  ok: boolean;
  mode: string;
}

/** 后端 moveResultResponse（2 个字段）。 */
interface MoveResultResp {
  moved: string;
  failed: string;
}

/** 后端 nResponse（2 个字段）。 */
interface NResp {
  ok: boolean;
  n: number;
}

/** 后端 okRemovedResponse（2 个字段）。 */
interface OkRemovedResp {
  ok: boolean;
  removed: number;
}

/** 后端 okResponse（1 个字段）。 */
interface OkResp {
  ok: boolean;
}

/** 后端 opSnapshotsResponse（4 个字段）。 */
interface OpSnapshotsResp {
  total: number;
  page: number;
  page_size: number;
  snapshots: any;
}

/** 后端 oplogListResponse（5 个字段）。 */
interface OplogListResp {
  items: any;
  total: number;
  page: number;
  page_size: number;
  next_before_id?: number;
}

/** 后端 pathResponse（2 个字段）。 */
interface PathResp {
  ok: boolean;
  path: string;
}

/** 后端 pathTaskIDResponse（2 个字段）。 */
interface PathTaskIDResp {
  path: string;
  task_id: string;
}

/** 后端 pausedResponse（2 个字段）。 */
interface PausedResp {
  ok: boolean;
  paused: boolean;
}

/** 后端 removedErrorsResponse（2 个字段）。 */
interface RemovedErrorsResp {
  removed: number;
  errors: any;
}

/** 后端 renameBatchesResidualResponse（2 个字段）。 */
interface RenameBatchesResidualResp {
  batches: any;
  residual: any;
}

/** 后端 renameBatchesResponse（1 个字段）。 */
interface RenameBatchesResp {
  batches: any;
}

/** 后端 renameBatchesResult（4 个字段）。 */
interface RenameBatchesResult {
  done: number;
  failed: number;
  items: any;
  batches: any;
}

/** 后端 renameCancelResponse（3 个字段）。 */
interface RenameCancelResp {
  ok: boolean;
  plan_id: number;
  cancel: boolean;
}

/** 后端 renameChunkCancelledResult（7 个字段）。 */
interface RenameChunkCancelledResult {
  cancelled: boolean;
  done: number;
  failed: number;
  remaining: number;
  total: number;
  total_done: number;
  total_failed: number;
}

/** 后端 renameChunkDoneResult（6 个字段）。 */
interface RenameChunkDoneResult {
  done: number;
  failed: number;
  remaining: number;
  total: number;
  total_done: number;
  total_failed: number;
}

/** 后端 renameChunkResult（9 个字段）。 */
interface RenameChunkResult {
  batch_id: number;
  done: number;
  failed: number;
  remaining: number;
  total: number;
  total_done: number;
  total_failed: number;
  items: any;
  reconcile: any;
}

/** 后端 renameChunkWarningResult（7 个字段）。 */
interface RenameChunkWarningResult {
  batch_id: number;
  done: number;
  failed: number;
  remaining: number;
  warning: string;
  items: any;
  reconcile: any;
}

/** 后端 renameExecResult（5 个字段）。 */
interface RenameExecResult {
  batch_id: number;
  done: number;
  failed: number;
  items: any;
  reconcile: any;
}

/** 后端 renameExecStatusResult（5 个字段）。 */
interface RenameExecStatusResult {
  ok: boolean;
  done: number;
  failed: number;
  status: string;
  items: any;
}

/** 后端 renamePlanCreateResponse（6 个字段）。 */
interface RenamePlanCreateResp {
  plan_id: number;
  total: number;
  conflicts: any;
  errors: number;
  source: string;
  samples: any;
}

/** 后端 renamePlanItemsResponse（2 个字段）。 */
interface RenamePlanItemsResp {
  items: any;
  count: number;
}

/** 后端 renamePlanItemsWithPlanResponse（3 个字段）。 */
interface RenamePlanItemsWithPlanResp {
  items: any;
  count: number;
  plan: any;
}

/** 后端 renamePlanStatusResponse（6 个字段）。 */
interface RenamePlanStatusResp {
  plan_id: number;
  total: number;
  done: number;
  failed: number;
  pending: number;
  status: string;
}

/** 后端 renamePresetResponse（1 个字段）。 */
interface RenamePresetResp {
  preset: any;
}

/** 后端 renamePresetsResponse（1 个字段）。 */
interface RenamePresetsResp {
  presets: any;
}

/** 后端 retainClearedResponse（1 个字段）。 */
interface RetainClearedResp {
  cleared: number;
}

/** 后端 retainSuggestionsResponse（1 个字段）。 */
interface RetainSuggestionsResp {
  suggestions: any;
}

/** 后端 ruleSearchStatusResponse（8 个字段）。 */
interface RuleSearchStatusResp {
  id: string;
  state: string;
  error: string;
  stale: boolean;
  loaded: number;
  total: number;
  totals: any;
  created_at: number;
}

/** 后端 runsResponse（1 个字段）。 */
interface RunsResp {
  runs: any;
}

/** 后端 searchFacetsResponse（2 个字段）。 */
interface SearchFacetsResp {
  state: string;
  totals: any;
}

/** 后端 searchItemsResponse（3 个字段）。 */
interface SearchItemsResp {
  total: number;
  items: any;
  ready: boolean;
}

/** 后端 searchNotReadyResponse（3 个字段）。 */
interface SearchNotReadyResp {
  error: string;
  state: string;
  detail: string;
}

/** 后端 searchReadyErrorResponse（3 个字段）。 */
interface SearchReadyErrorResp {
  error: string;
  state: string;
  detail: string;
}

/** 后端 searchResultsResponse（4 个字段）。 */
interface SearchResultsResp {
  total: number;
  items: any;
  next_offset: number;
  took_ms: number;
}

/** 后端 searchSQLResponse（9 个字段）。 */
interface SearchSQLResp {
  total: number;
  took_ms: number;
  truncated: boolean;
  degraded: boolean;
  ready: boolean;
  items: any;
  next_cursor: string;
  files: any;
  notice?: string;
}

/** 后端 searchSelectionResponse（4 个字段）。 */
interface SearchSelectionResp {
  token: string;
  count: number;
  index_built_at: number;
  data_version: number;
}

/** 后端 searchStatusResponse（9 个字段）。 */
interface SearchStatusResp {
  ready: boolean;
  total?: number;
  items?: any;
  building: boolean;
  built_at: number;
  degraded: boolean;
  entries: number;
  est_bytes: number;
  max_entries: number;
}

/** 后端 snapshotEmptyResponse（2 个字段）。 */
interface SnapshotEmptyResp {
  state: string;
  totals: any;
}

/** 后端 snapshotFacetsResponse（3 个字段）。 */
interface SnapshotFacetsResp {
  total: number;
  types: any;
  dirs: any;
}

/** 后端 snapshotGroupsResponse（5 个字段）。 */
interface SnapshotGroupsResp {
  groups: StoreGroup[];
  total: number;
  page: number;
  page_size: number;
  has_more: boolean;
}

/** 后端 snapshotSavedResponse（2 个字段）。 */
interface SnapshotSavedResp {
  ok: boolean;
  id: string;
}

/** 后端 statusResponse（14 个字段）。 */
interface StatusResp {
  state: any;
  paused: boolean;
  exclusive_task: any;
  health_running: boolean;
  ffmpeg_running: boolean;
  counts: any;
  volumes: any;
  version: string;
  cleanup: any;
  auth: any;
  config: any;
  hash: any;
  paths: any;
  ffmpeg: any;
}

/** 后端 statusUnauthResponse（3 个字段）。 */
interface StatusUnauthResp {
  version: string;
  auth: any;
  state?: any;
}

/** 后端 syncImportResponse（3 个字段）。 */
interface SyncImportResp {
  imported: number;
  invalid: number;
  machine: string;
}

/** 后端 syncPeersResponse（2 个字段）。 */
interface SyncPeersResp {
  peers: any;
  machine: string;
}

/** 后端 tagSavedCountResponse（2 个字段）。 */
interface TagSavedCountResp {
  ok: boolean;
  count: number;
}

/** 后端 tagSavedResponse（3 个字段）。 */
interface TagSavedResp {
  id: number;
  ok: boolean;
  count: number;
}

/** 后端 tagSavedWarningResponse（3 个字段）。 */
interface TagSavedWarningResp {
  id: number;
  ok: boolean;
  warning: string;
}

/** 后端 tagsResponse（1 个字段）。 */
interface TagsResp {
  tags: StoreVideoTag[];
}

/** 后端 tasksResponse（2 个字段）。 */
interface TasksResp {
  running: any;
  history: any;
}

/** 后端 tokenResponse（1 个字段）。 */
interface TokenResp {
  token: string;
}

/** 后端 transcodeResidueResponse（2 个字段）。 */
interface TranscodeResidueResp {
  files: any;
  scanned_dirs: number;
}

/** 后端 transcodeStatusResponse（2 个字段）。 */
interface TranscodeStatusResp {
  queued: number;
  encoder: string;
}

/** 后端 transcodeTasksResponse（2 个字段）。 */
interface TranscodeTasksResp {
  tasks: any;
  encoder: string;
}

/** 后端 trashListResponse（5 个字段）。 */
interface TrashListResp {
  items: StoreTrashRow[];
  total: number;
  returned: number;
  has_more: boolean;
  next_cursor: string;
}

/** 后端 trashPreviewResponse（2 个字段）。 */
interface TrashPreviewResp {
  action: string;
  items: any;
}

/** 后端 trashRemovedResponse（2 个字段）。 */
interface TrashRemovedResp {
  removed: number;
  errors: any;
}

/** 后端 trimLogResponse（1 个字段）。 */
interface TrimLogResp {
  log: TrimLogEntry[];
}

/** 后端 trimProbeResponse（3 个字段）。 */
interface TrimProbeResp {
  boundary: number;
  status: string;
  cached: boolean;
}

/** 后端 videoTagSavedResponse（4 个字段）。 */
interface VideoTagSavedResp {
  ok: boolean;
  id: number;
  count?: number;
  warning?: string;
}

/** 后端 videoTagsResponse（1 个字段）。 */
interface VideoTagsResp {
  tags: StoreVideoTag[];
}

/** 后端 watchRestartResponse（5 个字段）。 */
interface WatchRestartResp {
  ok: boolean;
  restart_watch: boolean;
  intro_reset: boolean;
  video_reset: boolean;
  audio_reset: boolean;
}


// ── 数据行类型（由 internal/store 生成）──────────────────────────────

/** 后端 store.FileRow（20 个字段）。 */
interface StoreFileRow extends StoreMediaMeta {
  id: number;
  path: string;
  volume: string;
  name: string;
  size: number;
  alloc?: number;
  mtime: number;
  ctime: number;
  nlink: number;
  inode: number;
  dev?: number;
  ext: string;
  quick?: string;
  hash?: string;
  duration?: number;
  thumb?: string;
  img_hash?: string;
  tags?: StoreTagValue[];
  peer?: string;
}

/** 后端 store.Group（9 个字段）。 */
interface StoreGroup {
  key: string;
  kind?: string;
  name: string;
  hash?: string;
  count: number;
  total_size: number;
  waste: number;
  similarity?: number;
  files: StoreFileRow[];
}

/** 后端 store.MediaMeta（5 个字段）。 */
interface StoreMediaMeta {
  width?: number;
  height?: number;
  codec?: string;
  bit_rate?: number;
  sample_rate?: number;
}

/** 后端 store.TagValue（2 个字段）。 */
interface StoreTagValue {
  name: string;
  value: string;
}

/** 后端 store.TrashRow（9 个字段）。 */
interface StoreTrashRow {
  id: number;
  orig_path: string;
  trash_path: string;
  volume: string;
  size: number;
  hash?: string;
  deleted_at: number;
  reason?: string;
  exported_at?: number;
}

/** 后端 store.VideoTag（8 个字段）。 */
interface StoreVideoTag {
  id: number;
  name: string;
  match_mode: string;
  join_group: boolean;
  sort_order: number;
  value_source: string;
  value_pattern: string;
  hits: number;
}
