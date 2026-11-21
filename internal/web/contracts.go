// API 响应契约（v4.0.0）
//
// 目的：把「后端 JSON 响应」从散落各 handler 的 map[string]any 收敛成具名 struct，
// 让字段名有编译期保障，前端也能据此声明类型。
//
// 为什么值得做（v4.0.0 重构的核心动机）：
//
//	原状：internal/web 下 490 处 writeJSON(..., map[string]any{...})，
//	      响应字段名只存在于字面量里。Go 编译器不检查（map 无静态类型），
//	      前端靠 `r.groups || []` 这类静默兜底消费（前端共 152 处），
//	      文档只覆盖 15/115 个接口。三道防线全漏——
//
// //	      AI 把后端 `waste` 改成 `reclaim`，Go 编译通过、前端不报错、页面显示 0。
//
//	改后：字段名写在 struct 的 json tag 上，
//	      改 tag → Go 编译期就发现；前端用 webtypes/types.d.ts 的 interface 消费，
//	      字段拼错 → tsc --noEmit 期就发现。
//
// 约束：
//   - 本文件只放响应结构，不放业务逻辑；
//   - struct 的 json tag 是前后端字段名的唯一真相源，改 tag 必须同步
//     webtypes/types.d.ts 里的同名 interface；
//   - 保持原有 JSON 键名与语义不变，避免破坏既有调用方（含自动化脚本）。
//
// 命名约定：
//   - 一个接口一个 struct，名字用「语义 + Response」；
//   - 同一语义被多个接口复用的（如分组列表）合并成一个；
//   - 可选字段用 omitempty，且必须与前端 types.d.ts 的可选标记一致。
package web

import "verify/internal/store"

// ── 通用响应 ────────────────────────────────────────────────────────────

// errorResponse 是统一错误响应。
//
// 所有 4xx/5xx 的失败响应都走这个形态，保证前端永远能从 body.error 拿到文案。
// 少数接口需要附带额外信息（如 search_ready 的 state/detail），用各自的 struct 组合。
type errorResponse struct {
	Error string `json:"error"`
}

// okResponse 是最简单的成功响应。
type okResponse struct {
	OK bool `json:"ok"`
}

// countResponse 是「只回一个计数」的成功响应。
type countResponse struct {
	OK    bool  `json:"ok"`
	ID    int64 `json:"id,omitempty"`
	Count int   `json:"count,omitempty"`
}

// ── 分组列表类 ──────────────────────────────────────────────────────────

// groupsResponse 是「分组列表」类接口的通用响应。
//
// 覆盖的接口：
//
//	GET /api/groups          server.go    handleGroups
//	GET /api/groups/video    server.go    handleVideoGroups
//	GET /api/groups/fuzzy    fuzzy.go     handleFuzzyGroups
//	GET /api/groups/semantic semantic.go  handleSemanticGroups
//	GET /api/groups/tag      tags.go      handleTagGroups
//
// 这些接口都带分页参数，故 page / page_size 总是显式输出（不做 omitempty）。
//
// 不带分页的媒体类分组接口（audio / folder / photo）见 groupsMediaResponse ——
// 它们的键集合与本 struct 不同，故分开定义。
type groupsResponse struct {
	Groups []store.Group `json:"groups"`
	// Total 是分组总数。
	//
	// 各接口来源不同（有的是 len(groups)，int；有的是 SQL 计数，int64），
	// 这里统一成 int64，赋值处做显式转换——
	// 不用 any 是因为 any 会把前端的 number 类型保障也一起丢掉。
	Total int64 `json:"total"`
	// Page / PageSize 是分页参数。
	//
	// 不加 omitempty：原 map 里这两个键总是显式写出（值为 0 时也在），
	// 加了 omitempty 会让 page=0 的响应少掉这个键，前端断言会拿到 nil。
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

// groupsMediaResponse 是「不分页的媒体类分组列表」响应。
//
// v4.0.0：媒体类分组接口有三个不同的键集合，故按键集合拆成三个 struct——
// 用一个 struct + omitempty 无法精确还原（值为 0/false 时键会消失，
// 而原 map 里这些键是无条件写出的）。
//
//	GET /api/groups/audio   media.go  handleAudioGroups
//	                          键集合：{groups, total, threshold}
//	GET /api/groups/folder  media.go  handleFolderGroups（禁用分支）
//	                          键集合：{groups, total, disabled}
//	GET /api/groups/folder  media.go  handleFolderGroups（正常分支）
//	                          键集合：{groups, total}
//
// photo 走 groupsPhotoResponse（带 threshold）。
type groupsMediaResponse struct {
	Groups []store.Group `json:"groups"`
	Total  int64         `json:"total"`
	// Threshold 是媒体分析的相似度阈值。
	//
	// 实际取值类型两处不同：photo.go 用 int64（汉明距离），media.go 用 int（音频阈值）。
	// 两者的 JSON 形态都是整数，故这里用 any 承载，避免为同一语义引入两个字段名。
	//
	// 不加 omitempty：原 map 里这个键是无条件写出的，
	// 加了 omitempty 会在阈值恰好为 0 时让键消失。
	Threshold any `json:"threshold"`
}

// groupsDisabledResponse 是「该类分组被配置禁用」的响应（媒体类）。
//
// 键集合：{groups, total, disabled}
type groupsDisabledResponse struct {
	Groups   []store.Group `json:"groups"`
	Total    int64         `json:"total"`
	Disabled bool          `json:"disabled"`
}

// groupsSimpleResponse 是「不分页、无附加字段」的分组列表。
//
// 键集合：{groups, total}
type groupsSimpleResponse struct {
	Groups []store.Group `json:"groups"`
	Total  int64         `json:"total"`
}
