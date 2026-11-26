package main

import "context"

// Param 描述命令的一个参数：Name 参数名，Type 参数类型（string/int/float/bool/json），
// Required 是否必填，Default 缺省值。供 --help-json 自描述使用。
type Param struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Default  string `json:"default"`
}

// Command 是一条 CLI 命令的注册信息。Name 为手工分发用的命令名（可含空格表示二级子命令），
// Path/Method 对应 HTTP API；SideEffect 取 none|trigger|write|destructive；
// Confirm 为真表示执行前必须经 Task 9 的 --yes + --confirm-token 双门槛。
//
// Run 的客户端参数采用 any 而非 *Client：Task 2 阶段 *Client 尚未定义（Task 3 才引入），
// 用 any 才能让本包独立编译。后续任务实现 Run 时签名必须严格使用下面的 any 版本，
// 并在函数体内做 c.(*Client) 断言；这是本任务产出的权威接口。
// 本任务只登记条目，不实现 Run；Run 的填充由 Task 6/8/9/10 完成。
type Command struct {
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Method     string  `json:"method"`
	Params     []Param `json:"params"`
	SideEffect string  `json:"side_effect"`
	Confirm    bool    `json:"confirm_required"`
	// PathParams 列出按声明顺序拼接到 Path 末尾的参数名（服务端从 URL 路径段读，如
	// /api/rename/presets/{id}）。这些参数既不进 query 也不进 body。
	PathParams []string `json:"path_params,omitempty"`
	// QueryParams 列出即使非 GET 方法也必须走查询串的参数名（如 POST /api/video_tags/recompute?id=）。
	QueryParams []string `json:"query_params,omitempty"`
	// Multipart 为真表示以 multipart/form-data 上传：名为 file 的参数按本地路径读为文件部件，
	// 其余参数作为表单字段（如 POST /api/import）。
	Multipart bool                                                                  `json:"multipart,omitempty"`
	Run       func(ctx context.Context, c any, args map[string]string) (any, error) `json:"-"`
}

// Commands 是全量命令表：逐条对应 internal/web/server.go 注册的非静态路由，
// 是分发入口与 --help-json 的唯一真相源。覆盖断言见 registry_test.go。
// 特例：/api/trash 走 GET 但会因 HealTrashRows 写库，故 SideEffect 标 write；
// 只读场景请用 trash list（/api/trash/list）。
var Commands = []Command{
	{Name: "login", Path: "/api/login", Method: "POST", Params: []Param{{"token", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "logout", Path: "/api/logout", Method: "POST", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "status", Path: "/api/status", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "tasks", Path: "/api/tasks", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "scan", Path: "/api/scan", Method: "POST", Params: []Param{{"mode", "string", false, ""}, {"quick", "bool", false, ""}}, SideEffect: "trigger", Confirm: false},
	{Name: "scan stop", Path: "/api/scan/stop", Method: "POST", Params: nil, SideEffect: "trigger", Confirm: false},
	{Name: "groups", Path: "/api/groups", Method: "GET", Params: []Param{{"mode", "string", false, ""}, {"page_size", "int", false, "20"}, {"page", "int", false, "1"}}, SideEffect: "none", Confirm: false},
	{Name: "folder stats", Path: "/api/folder/stats", Method: "GET", Params: []Param{{"path", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "groups video", Path: "/api/groups/video", Method: "GET", Params: []Param{{"page_size", "int", false, "20"}}, SideEffect: "none", Confirm: false},
	{Name: "groups intro", Path: "/api/groups/intro", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "groups photo", Path: "/api/groups/photo", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "relink preview", Path: "/api/relink/preview", Method: "POST", Params: []Param{{"keep", "string", false, ""}, {"paths", "json", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "relink", Path: "/api/relink", Method: "POST", Params: []Param{{"keep", "string", false, ""}, {"paths", "json", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "big", Path: "/api/big", Method: "GET", Params: []Param{{"limit", "int", false, ""}, {"order", "string", false, ""}, {"crop", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "emptydir clean", Path: "/api/emptydir/clean", Method: "POST", Params: []Param{{"paths", "json", false, ""}}, SideEffect: "destructive", Confirm: true},
	{Name: "move preview", Path: "/api/move/preview", Method: "POST", Params: []Param{{"target", "string", false, ""}, {"paths", "json", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "move", Path: "/api/move", Method: "POST", Params: []Param{{"target", "string", false, ""}, {"paths", "json", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "move undo", Path: "/api/move/undo", Method: "POST", Params: []Param{{"task_id", "int", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "move dir preview", Path: "/api/move/dir/preview", Method: "POST", Params: []Param{{"src", "string", false, ""}, {"dst", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "move dir", Path: "/api/move/dir", Method: "POST", Params: []Param{{"src", "string", false, ""}, {"dst", "string", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "move dir undo", Path: "/api/move/dir/undo", Method: "POST", Params: []Param{{"task_id", "int", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "groups rename", Path: "/api/groups/rename", Method: "POST", Params: []Param{{"base", "string", false, ""}, {"paths", "json", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "groups rename undo", Path: "/api/groups/rename/undo", Method: "POST", Params: []Param{{"task_id", "int", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "file rename", Path: "/api/file/rename", Method: "POST", Params: []Param{{"path", "string", false, ""}, {"name", "string", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "trim", Path: "/api/trim", Method: "POST", Params: []Param{{"jobs", "json", true, ""}}, SideEffect: "write", Confirm: false},
	{Name: "trim log", Path: "/api/trim/log", Method: "GET", Params: []Param{{"limit", "int", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "trim probe", Path: "/api/trim/probe", Method: "POST", Params: []Param{{"group_key", "string", true, ""}, {"paths", "json", true, ""}, {"durations", "json", false, ""}, {"threshold", "float", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "transcode", Path: "/api/transcode", Method: "POST", Params: []Param{{"paths", "json", true, ""}, {"quality", "string", false, "balanced"}, {"max_res", "string", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "transcode status", Path: "/api/transcode/status", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "tasks pause", Path: "/api/tasks/pause", Method: "POST", Params: nil, SideEffect: "trigger", Confirm: false},
	{Name: "tasks resume", Path: "/api/tasks/resume", Method: "POST", Params: nil, SideEffect: "trigger", Confirm: false},
	{Name: "fanjian", Path: "/api/fanjian", Method: "POST", Params: []Param{{"paths", "json", false, ""}, {"to_traditional", "bool", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "fanjian undo", Path: "/api/fanjian/undo", Method: "POST", Params: []Param{{"task_id", "int", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "transcode cancel", Path: "/api/transcode/cancel", Method: "POST", Params: []Param{{"job_id", "int", false, ""}}, SideEffect: "trigger", Confirm: false},
	{Name: "transcode residue", Path: "/api/transcode/residue", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "transcode residue clean", Path: "/api/transcode/residue/clean", Method: "POST", Params: []Param{{"paths", "json", false, ""}}, SideEffect: "destructive", Confirm: true},
	{Name: "groups audio", Path: "/api/groups/audio", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "groups folder", Path: "/api/groups/folder", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "groups fuzzy", Path: "/api/groups/fuzzy", Method: "GET", Params: []Param{{"near", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "folder_rename preview", Path: "/api/folder_rename/preview", Method: "POST", Params: []Param{{"dir", "string", false, ""}, {"prefix_a", "bool", false, "false"}}, SideEffect: "none", Confirm: false},
	{Name: "folder_rename", Path: "/api/folder_rename", Method: "POST", Params: []Param{{"dir", "string", true, ""}, {"prefix_a", "bool", false, "false"}}, SideEffect: "write", Confirm: false},
	{Name: "folder_rename undo", Path: "/api/folder_rename/undo", Method: "POST", Params: []Param{{"task_id", "int", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "groups semantic", Path: "/api/groups/semantic", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "video_tags", Path: "/api/video_tags", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "video_tags add", Path: "/api/video_tags", Method: "POST", Params: []Param{{"name", "string", true, ""}, {"match_mode", "string", false, ""}, {"value_source", "string", false, ""}, {"value_pattern", "string", false, ""}, {"join_group", "bool", false, "true"}, {"sort_order", "int", false, "0"}}, SideEffect: "write", Confirm: false},
	{Name: "video_tags delete", Path: "/api/video_tags", Method: "DELETE", Params: []Param{{"id", "int", true, ""}}, SideEffect: "destructive", Confirm: true},
	{Name: "video_tags recompute", Path: "/api/video_tags/recompute", Method: "POST", Params: []Param{{"id", "string", false, ""}}, QueryParams: []string{"id"}, SideEffect: "trigger", Confirm: false},
	{Name: "groups tag", Path: "/api/groups/tag", Method: "GET", Params: []Param{{"tag_id", "int", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "search", Path: "/api/search", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "search selection", Path: "/api/search/selection", Method: "POST", Params: []Param{{"q", "string", false, ""}, {"mode", "string", false, ""}, {"case", "bool", false, ""}, {"ext", "string", false, ""}, {"min_size", "int", false, ""}, {"max_size", "int", false, ""}, {"since_ms", "int", false, ""}, {"until_ms", "int", false, ""}, {"dir", "string", false, ""}, {"sort", "string", false, ""}, {"order", "string", false, ""}, {"token", "string", false, ""}, {"offset", "int", false, ""}, {"limit", "int", false, ""}, {"excludes", "json", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "search selection items", Path: "/api/search/selection/items", Method: "POST", Params: []Param{{"q", "string", false, ""}, {"mode", "string", false, ""}, {"case", "bool", false, ""}, {"ext", "string", false, ""}, {"min_size", "int", false, ""}, {"max_size", "int", false, ""}, {"since_ms", "int", false, ""}, {"until_ms", "int", false, ""}, {"dir", "string", false, ""}, {"sort", "string", false, ""}, {"order", "string", false, ""}, {"token", "string", false, ""}, {"offset", "int", false, ""}, {"limit", "int", false, ""}, {"excludes", "json", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "search status", Path: "/api/search/status", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "search rebuild", Path: "/api/search/rebuild", Method: "POST", Params: nil, SideEffect: "trigger", Confirm: false},
	{Name: "rename preview", Path: "/api/rename/preview", Method: "POST", Params: []Param{{"files", "json", false, ""}, {"rules", "json", false, ""}, {"methods", "json", false, ""}, {"preset_id", "int", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "rename execute", Path: "/api/rename/execute", Method: "POST", Params: []Param{{"files", "json", false, ""}, {"rules", "json", false, ""}, {"methods", "json", false, ""}, {"preset_id", "int", false, ""}}, SideEffect: "destructive", Confirm: true},
	{Name: "rename plan", Path: "/api/rename/plan", Method: "POST", Params: []Param{{"files", "json", false, ""}, {"methods", "json", false, ""}, {"selection_token", "string", false, ""}, {"excludes", "json", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "rename plan status", Path: "/api/rename/plan/status", Method: "GET", Params: []Param{{"plan_id", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "rename execute-chunk", Path: "/api/rename/execute-chunk", Method: "POST", Params: []Param{{"plan_id", "int", false, ""}, {"limit", "int", false, ""}}, SideEffect: "destructive", Confirm: true},
	{Name: "rename cancel", Path: "/api/rename/cancel", Method: "POST", Params: []Param{{"plan_id", "int", false, ""}, {"clear", "bool", false, ""}}, SideEffect: "trigger", Confirm: false},
	{Name: "rename undo-batch", Path: "/api/rename/undo-batch", Method: "POST", Params: []Param{{"batch_ids", "json", false, ""}}, SideEffect: "destructive", Confirm: true},
	{Name: "rename batches recent", Path: "/api/rename/batches/recent", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "rename undo", Path: "/api/rename/undo/", Method: "POST", Params: nil, SideEffect: "destructive", Confirm: true},
	{Name: "rename batches unfinished", Path: "/api/rename/batches/unfinished", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "rename presets", Path: "/api/rename/presets", Method: "POST", Params: []Param{{"name", "string", true, ""}, {"description", "string", false, ""}, {"methods", "string", true, ""}}, SideEffect: "write", Confirm: false},
	{Name: "rename preset delete", Path: "/api/rename/presets/", Method: "DELETE", Params: []Param{{"id", "int", true, ""}}, PathParams: []string{"id"}, SideEffect: "destructive", Confirm: true},
	{Name: "rename preset update", Path: "/api/rename/presets/", Method: "PUT", Params: []Param{{"id", "int", true, ""}, {"name", "string", true, ""}, {"description", "string", false, ""}, {"methods", "string", true, ""}}, PathParams: []string{"id"}, SideEffect: "write", Confirm: false},
	{Name: "video cropdetect", Path: "/api/video/cropdetect", Method: "POST", Params: []Param{{"limit", "int", false, ""}}, SideEffect: "trigger", Confirm: false},
	{Name: "retain score", Path: "/api/retain/score", Method: "POST", Params: []Param{{"groups", "json", true, ""}}, SideEffect: "none", Confirm: false},
	{Name: "retain pref", Path: "/api/retain/pref", Method: "POST", Params: []Param{{"entries", "json", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "retain pref stats", Path: "/api/retain/pref/stats", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "retain pref clear", Path: "/api/retain/pref/clear", Method: "POST", Params: nil, SideEffect: "write", Confirm: false},
	{Name: "retain pref profile", Path: "/api/retain/pref/profile", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "retain pref apply", Path: "/api/retain/pref/apply", Method: "POST", Params: []Param{{"enable", "bool", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "retain explain", Path: "/api/retain/explain", Method: "POST", Params: []Param{{"groups", "json", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "sync export", Path: "/api/sync/export", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "sync import", Path: "/api/sync/import", Method: "POST", Params: []Param{{"version", "int", false, ""}, {"machine", "string", false, ""}, {"rows", "json", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "sync peers", Path: "/api/sync/peers", Method: "GET", Params: []Param{{"machine", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "sync_peer delete", Path: "/api/sync/peers", Method: "DELETE", Params: []Param{{"machine", "string", true, ""}}, SideEffect: "destructive", Confirm: true},
	{Name: "intro reanalyze", Path: "/api/intro/reanalyze", Method: "POST", Params: []Param{{"seconds", "int", false, ""}}, SideEffect: "trigger", Confirm: false},
	{Name: "video failures", Path: "/api/video/failures", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "video reanalyze", Path: "/api/video/reanalyze", Method: "POST", Params: []Param{{"ids", "json", false, ""}, {"all", "bool", false, ""}}, SideEffect: "trigger", Confirm: false},
	{Name: "trash", Path: "/api/trash", Method: "POST", Params: []Param{{"paths", "json", false, ""}, {"group_key", "string", false, ""}, {"group_keys", "json", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "trash list", Path: "/api/trash/list", Method: "GET", Params: []Param{{"limit", "int", false, ""}, {"cursor", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "trash export", Path: "/api/trash/export", Method: "GET", Params: []Param{{"ids", "string", false, ""}, {"all", "bool", false, "false"}}, SideEffect: "none", Confirm: false},
	{Name: "trash preview", Path: "/api/trash/preview", Method: "POST", Params: []Param{{"action", "string", false, ""}, {"paths", "json", false, ""}, {"ids", "json", false, ""}, {"group_key", "string", false, ""}, {"group_keys", "json", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "trash restore", Path: "/api/trash/restore", Method: "POST", Params: []Param{{"ids", "json", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "trash delete", Path: "/api/trash/delete", Method: "POST", Params: []Param{{"ids", "json", false, ""}, {"confirm", "bool", false, ""}}, SideEffect: "destructive", Confirm: true},
	{Name: "trash empty", Path: "/api/trash/empty", Method: "POST", Params: []Param{{"confirm", "bool", false, ""}}, SideEffect: "destructive", Confirm: true},
	{Name: "cleanup plans", Path: "/api/cleanup/plans", Method: "POST", Params: []Param{{"id", "int", false, ""}, {"name", "string", true, ""}, {"enabled", "bool", false, ""}, {"schedule", "string", true, ""}, {"match_mode", "string", false, "all"}, {"min_size", "int", false, ""}, {"min_count", "int", false, ""}, {"extensions", "string", false, ""}, {"retain_strategy", "string", false, "modify_newest"}, {"max_keep_gb", "int", false, ""}, {"rules_json", "string", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "cleanup plans delete", Path: "/api/cleanup/plans/delete", Method: "POST", Params: []Param{{"id", "int", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "cleanup run", Path: "/api/cleanup/run", Method: "POST", Params: []Param{{"id", "int", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "export", Path: "/api/export", Method: "POST", Params: []Param{{"mode", "string", false, ""}, {"format", "string", false, ""}, {"scope", "string", false, ""}, {"marked", "json", false, ""}, {"min_size", "int", false, ""}, {"max_size", "int", false, ""}, {"ext", "string", false, ""}, {"exts", "json", false, ""}, {"q", "string", false, ""}, {"since", "int", false, ""}, {"until", "int", false, ""}, {"n", "int", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "import", Path: "/api/import", Method: "POST", Params: []Param{{"file", "string", true, ""}, {"name", "string", false, ""}}, Multipart: true, SideEffect: "write", Confirm: false},
	{Name: "import sessions", Path: "/api/import/sessions", Method: "GET", Params: []Param{{"id", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "import_session delete", Path: "/api/import/sessions", Method: "DELETE", Params: []Param{{"id", "int", true, ""}}, SideEffect: "destructive", Confirm: true},
	{Name: "import groups", Path: "/api/import/groups", Method: "GET", Params: []Param{{"session_id", "int", true, ""}}, SideEffect: "none", Confirm: false},
	{Name: "report", Path: "/api/report", Method: "POST", Params: []Param{{"mode", "string", false, ""}, {"marked", "json", false, ""}, {"min_size", "int", false, ""}, {"max_size", "int", false, ""}, {"ext", "string", false, ""}, {"exts", "json", false, ""}, {"q", "string", false, ""}, {"since", "int", false, ""}, {"until", "int", false, ""}, {"n", "int", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "op-snapshots", Path: "/api/op-snapshots", Method: "GET", Params: []Param{{"kind", "string", false, ""}, {"result", "string", false, ""}, {"keyword", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "fs list", Path: "/api/fs/list", Method: "GET", Params: []Param{{"path", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "fs roots", Path: "/api/fs/roots", Method: "POST", Params: []Param{{"path", "string", true, ""}}, SideEffect: "write", Confirm: false},
	{Name: "diag authz", Path: "/api/diag/authz", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "fs mkdir", Path: "/api/fs/mkdir", Method: "POST", Params: []Param{{"parent", "string", false, ""}, {"name", "string", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "metric", Path: "/api/metric", Method: "POST", Params: []Param{{"name", "string", false, ""}, {"ms", "int", false, ""}, {"detail", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "metrics", Path: "/api/metrics", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "raw", Path: "/api/raw", Method: "GET", Params: []Param{{"path", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "thumb", Path: "/api/thumb", Method: "GET", Params: []Param{{"path", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "cache stats", Path: "/api/cache/stats", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "cache clear", Path: "/api/cache/clear", Method: "POST", Params: []Param{{"kind", "string", false, ""}}, SideEffect: "trigger", Confirm: false},
	{Name: "settings", Path: "/api/settings", Method: "POST", Params: []Param{{"min_size", "int", false, ""}, {"watch", "bool", false, ""}, {"video_enabled", "bool", false, ""}, {"intro_enabled", "bool", false, ""}, {"intro_seconds", "int", false, ""}, {"audio_threshold", "float", false, ""}, {"audio_window_seconds", "int", false, ""}, {"phash_hamming", "int", false, ""}, {"access_token", "string", false, ""}, {"ffmpeg", "string", false, ""}, {"ffprobe", "string", false, ""}, {"full_rescan_hours", "int", false, ""}, {"poll_minutes", "int", false, ""}, {"hash_workers", "int", false, ""}, {"video_workers", "int", false, ""}, {"ignore_intro_seconds", "int", false, ""}, {"ignore_outro_seconds", "int", false, ""}, {"hardware_decode", "bool", false, ""}, {"protect_paths", "json", false, ""}, {"path_maps_text", "string", false, ""}, {"photo_enabled", "bool", false, ""}, {"img_hamming", "int", false, ""}, {"audio_enabled", "bool", false, ""}, {"unify_ext", "string", false, ""}, {"sleep_friendly", "bool", false, ""}, {"quiet_hours", "string", false, ""}, {"broken_auto", "bool", false, ""}, {"broken_auto_idle_minutes", "int", false, ""}, {"broken_auto_batch", "int", false, ""}, {"video_density", "string", false, ""}, {"wake_enabled", "bool", false, ""}, {"wake_window", "string", false, ""}, {"content_enabled", "bool", false, ""}, {"name_enabled", "bool", false, ""}, {"compute_dup_folders", "bool", false, ""}, {"match_mode", "string", false, ""}, {"match_kinds", "json", false, ""}, {"search_rules", "json", false, ""}, {"search_mem_limit_mb", "int", false, ""}, {"search_presets", "json", false, ""}, {"traditional_enabled", "bool", false, ""}, {"temp_patterns", "json", false, ""}, {"noise_words", "json", false, ""}, {"exclude_paths", "json", false, ""}, {"exclude_dirs", "json", false, ""}, {"volumes", "json", false, ""}, {"log_dir", "string", false, ""}, {"cache_dir", "string", false, ""}, {"mem_budget_mb", "int", false, ""}, {"thumb_cache_max_mb", "int", false, ""}, {"ai_enabled", "bool", false, ""}, {"ai_endpoint", "string", false, ""}, {"ai_model", "string", false, ""}, {"op_log_enabled", "bool", false, ""}, {"op_log_retention_days", "int", false, ""}, {"op_log_reads", "bool", false, ""}, {"op_log_detail_limit", "int", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "auth token", Path: "/api/auth/token", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "rule-search run", Path: "/api/rule-search/run", Method: "POST", Params: nil, SideEffect: "trigger", Confirm: false},
	{Name: "rule-search status", Path: "/api/rule-search/status", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "rule-search cancel", Path: "/api/rule-search/cancel", Method: "POST", Params: nil, SideEffect: "trigger", Confirm: false},
	{Name: "rule-search results", Path: "/api/rule-search/results", Method: "GET", Params: []Param{{"type", "string", false, ""}, {"q", "string", false, ""}, {"dir", "string", false, ""}, {"sort", "string", false, ""}, {"order", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "rule-search facets", Path: "/api/rule-search/facets", Method: "GET", Params: []Param{{"type", "string", false, ""}, {"dir", "string", false, ""}, {"q", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "rule-search export", Path: "/api/rule-search/export", Method: "GET", Params: []Param{{"type", "string", false, ""}, {"q", "string", false, ""}, {"sort", "string", false, ""}, {"order", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "events", Path: "/api/events", Method: "GET", Params: []Param{{"token", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "health scan", Path: "/api/health/scan", Method: "POST", Params: []Param{{"kind", "string", false, ""}, {"content_mode", "string", false, ""}, {"restrict_charset", "bool", false, ""}, {"non_cjk", "bool", false, ""}, {"workers", "int", false, ""}, {"force", "bool", false, ""}}, SideEffect: "trigger", Confirm: false},
	{Name: "health cancel", Path: "/api/health/cancel", Method: "POST", Params: nil, SideEffect: "trigger", Confirm: false},
	{Name: "health status", Path: "/api/health/status", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "health results", Path: "/api/health/results", Method: "GET", Params: []Param{{"kind", "string", false, ""}}, SideEffect: "none", Confirm: false},
	{Name: "health rename", Path: "/api/health/rename", Method: "POST", Params: []Param{{"kind", "string", false, ""}, {"paths", "json", false, ""}, {"restrict_charset", "bool", false, ""}, {"non_cjk", "bool", false, ""}, {"only_uppercase", "bool", false, ""}, {"force", "bool", false, ""}, {"force_ext", "string", false, ""}, {"dry_run", "bool", false, ""}, {"all", "bool", false, ""}, {"reason", "string", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "health fix-encoding", Path: "/api/health/fix-encoding", Method: "POST", Params: []Param{{"restrict_charset", "bool", false, ""}, {"non_cjk", "bool", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "health trash", Path: "/api/health/trash", Method: "POST", Params: []Param{{"paths", "json", false, ""}}, SideEffect: "destructive", Confirm: true},
	{Name: "health undo", Path: "/api/health/undo", Method: "POST", Params: []Param{{"task_id", "int", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "health prune", Path: "/api/health/prune", Method: "POST", Params: []Param{{"kind", "string", false, ""}}, SideEffect: "destructive", Confirm: true},
	{Name: "health ignore", Path: "/api/health/ignore", Method: "POST", Params: []Param{{"kind", "string", false, ""}, {"paths", "json", false, ""}, {"restrict_charset", "bool", false, ""}, {"non_cjk", "bool", false, ""}, {"only_uppercase", "bool", false, ""}, {"force", "bool", false, ""}, {"force_ext", "string", false, ""}, {"dry_run", "bool", false, ""}, {"all", "bool", false, ""}, {"reason", "string", false, ""}}, SideEffect: "write", Confirm: false},
	{Name: "index repair", Path: "/api/index/repair", Method: "POST", Params: []Param{{"dry_run", "bool", false, ""}}, SideEffect: "trigger", Confirm: false},
	{Name: "oplog", Path: "/api/oplog", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
	{Name: "oplog export", Path: "/api/oplog/export", Method: "POST", Params: []Param{{"action", "string", false, ""}, {"result", "string", false, ""}, {"keyword", "string", false, ""}, {"since", "int", false, ""}, {"until", "int", false, ""}, {"page", "int", false, ""}, {"page_size", "int", false, ""}, {"before_id", "int", false, ""}}, QueryParams: []string{"action", "result", "keyword", "since", "until", "page", "page_size", "before_id"}, SideEffect: "write", Confirm: false},
	{Name: "oplog clear", Path: "/api/oplog/clear", Method: "POST", Params: []Param{{"before_ts", "int", false, ""}, {"confirm", "bool", false, ""}}, SideEffect: "destructive", Confirm: true},
	{Name: "op-snapshots unfinished", Path: "/api/op-snapshots/unfinished", Method: "GET", Params: nil, SideEffect: "none", Confirm: false},
}

// findCommand 按命令名查注册表，找到返回命令本体与 true，否则返回零值与 false。
func findCommand(name string) (Command, bool) {
	for _, cmd := range Commands {
		if cmd.Name == name {
			return cmd, true
		}
	}
	return Command{}, false
}
