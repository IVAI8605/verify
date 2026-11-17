// 契约一致性测试（v4.0.0）
//
// 目的：锁死「后端 JSON 响应结构」与「错误收敛机制」，
// 防止后续改动悄悄改掉字段名或让安全机制失效。
//
// 这是把接口契约从「文档描述」变成「可执行断言」的关键一步：
//   - 之前改字段名，AGENTS.md 的契约表不会被自动检查，AI 改完自测说好了，页面却显示空；
//   - 现在改字段名，本测试直接红。
//
// 本轮覆盖：
//   - 通用响应形态（groups / files / error / ok / count）
//   - 错误收敛对具名 struct 仍然生效（SEC-8 安全机制不能因重构而失效）
package web

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"verify/internal/store"
)

// jsonOf 把任意响应体序列化成 map，便于断言键名与取值。
func jsonOf(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	return got
}

// ── 分组列表 ────────────────────────────────────────────────────────────

// TestGroupsResponseJSONShape 锁死分组列表响应的键名与取值。
// 字段名与前端 webtypes/types.d.ts 的 GroupsResponse 必须一致。
func TestGroupsResponseJSONShape(t *testing.T) {
	got := jsonOf(t, groupsResponse{
		Groups: []store.Group{{
			Key:       "k1",
			Kind:      "exact",
			Name:      "n1",
			Count:     2,
			TotalSize: 100,
			Waste:     40,
			Files: []store.FileRow{{
				ID: 1, Path: "/a/b", Volume: "v", Name: "b", Size: 50,
			}},
		}},
		Total:    7,
		Page:     2,
		PageSize: 20,
	})

	for _, key := range []string{"groups", "total", "page", "page_size"} {
		if _, ok := got[key]; !ok {
			t.Errorf("响应缺少键 %q，实际: %v", key, got)
		}
	}
	// 可选字段在未设置时不应出现（omitempty）
	for _, key := range []string{"threshold", "disabled"} {
		if _, ok := got[key]; ok {
			t.Errorf("响应多出键 %q（应被 omitempty 省略），实际: %v", key, got)
		}
	}
	if got["total"] != float64(7) {
		t.Errorf("total = %v，期望 7", got["total"])
	}
	if got["page"] != float64(2) {
		t.Errorf("page = %v，期望 2", got["page"])
	}
	if got["page_size"] != float64(20) {
		t.Errorf("page_size = %v，期望 20", got["page_size"])
	}

	groups, ok := got["groups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("groups 结构不对: %v", got["groups"])
	}
	g, ok := groups[0].(map[string]any)
	if !ok {
		t.Fatalf("group 不是对象: %v", groups[0])
	}
	// Group 的 json tag 是前端字段名的真相源
	for _, key := range []string{"key", "name", "count", "total_size", "waste", "files"} {
		if _, ok := g[key]; !ok {
			t.Errorf("group 缺少键 %q，实际: %v", key, g)
		}
	}
	if g["total_size"] != float64(100) {
		t.Errorf("group.total_size = %v，期望 100", g["total_size"])
	}
}

// TestGroupsMediaResponseShape 覆盖媒体类分组（audio）的响应形态。
//
// v4.0.0：媒体类分组按键集合拆成了三个 struct（audio 带 threshold、
// folder 禁用分支带 disabled、folder 正常分支两个键都没有），
// 这里分别断言各自不会多出别人的键。
func TestGroupsMediaResponseShape(t *testing.T) {
	got := jsonOf(t, groupsMediaResponse{
		Groups:    []store.Group{},
		Total:     0,
		Threshold: 10,
	})
	if got["threshold"] != float64(10) {
		t.Errorf("threshold = %v，期望 10", got["threshold"])
	}
	// audio 的响应不带 disabled，也不带分页参数
	for _, key := range []string{"disabled", "page", "page_size"} {
		if _, ok := got[key]; ok {
			t.Errorf("响应多出键 %q，实际: %v", key, got)
		}
	}
	// threshold 必须始终存在（阈值恰好为 0 时也不能消失）
	got0 := jsonOf(t, groupsMediaResponse{Groups: []store.Group{}, Total: 0, Threshold: 0})
	if _, ok := got0["threshold"]; !ok {
		t.Errorf("threshold 为 0 时也不该消失，实际: %v", got0)
	}
}

// TestGroupsDisabledResponseShape 断言「分组被禁用」的响应形态。
func TestGroupsDisabledResponseShape(t *testing.T) {
	got := jsonOf(t, groupsDisabledResponse{
		Groups:   []store.Group{},
		Total:    0,
		Disabled: true,
	})
	if got["disabled"] != true {
		t.Errorf("disabled = %v，期望 true", got["disabled"])
	}
	for _, key := range []string{"threshold", "page", "page_size"} {
		if _, ok := got[key]; ok {
			t.Errorf("响应多出键 %q，实际: %v", key, got)
		}
	}
}

// TestGroupsSimpleResponseShape 断言「不分页、无附加字段」的分组响应形态。
func TestGroupsSimpleResponseShape(t *testing.T) {
	got := jsonOf(t, groupsSimpleResponse{Groups: []store.Group{}, Total: 0})
	for _, key := range []string{"groups", "total"} {
		if _, ok := got[key]; !ok {
			t.Errorf("响应缺少键 %q，实际: %v", key, got)
		}
	}
	for _, key := range []string{"threshold", "disabled", "page", "page_size"} {
		if _, ok := got[key]; ok {
			t.Errorf("响应多出键 %q，实际: %v", key, got)
		}
	}
}

// TestGroupsResponseAlwaysHasPage 锁死分页类分组响应总是带 page / page_size。
//
// v4.0.0 的关键回归项：这两个字段一旦加上 omitempty，
// page=0 的响应就会少掉这两个键，前端 `out["page"]` 会拿到 nil。
func TestGroupsResponseAlwaysHasPage(t *testing.T) {
	got := jsonOf(t, groupsResponse{Groups: []store.Group{}, Total: 0})
	for _, key := range []string{"groups", "total", "page", "page_size"} {
		if _, ok := got[key]; !ok {
			t.Errorf("响应缺少键 %q（值�� 0 时也必须存在），实际: %v", key, got)
		}
	}
	if got["page"] != float64(0) {
		t.Errorf("page = %v，期望 0", got["page"])
	}
}

// TestGroupsResponseGroupsNeverNull 保证 groups 不会是 null。
// 前端大量使用 `r.groups || []` 兜底，但 null 与 [] 的 JSON 形态不同，
// 自动化调用方若直接读 groups 字段会拿到 null。
func TestGroupsResponseGroupsNeverNull(t *testing.T) {
	raw, err := json.Marshal(groupsResponse{Groups: []store.Group{}, Total: 0})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if string(raw) == `{"groups":null,"total":0}` {
		t.Errorf("groups 为 nil，应初始化为空切片: %s", raw)
	}
	got := jsonOf(t, groupsResponse{Groups: []store.Group{}, Total: 0})
	groups, ok := got["groups"].([]any)
	if !ok {
		t.Fatalf("groups 不是数组: %v", got)
	}
	if len(groups) != 0 {
		t.Errorf("groups 长度 = %d，期望 0", len(groups))
	}
}

// ── 通用响应 ────────────────────────────────────────────────────────────

// TestErrorResponseJSONShape 锁死错误响应形态。
func TestErrorResponseJSONShape(t *testing.T) {
	got := jsonOf(t, errorResponse{Error: "出错了"})
	if got["error"] != "出错了" {
		t.Errorf("error = %v，期望 \"出错了\"", got["error"])
	}
	if len(got) != 1 {
		t.Errorf("错误响应应只有 error 一个键，实际: %v", got)
	}
}

// TestOKResponseJSONShape 锁死简单成功响应形态。
func TestOKResponseJSONShape(t *testing.T) {
	got := jsonOf(t, okResponse{OK: true})
	if got["ok"] != true {
		t.Errorf("ok = %v，期望 true", got["ok"])
	}
	if len(got) != 1 {
		t.Errorf("成功响应应只有 ok 一个键，实际: %v", got)
	}
}

// TestFilesResponseJSONShape 锁死文件列表响应形态。
func TestFilesResponseJSONShape(t *testing.T) {
	got := jsonOf(t, filesResponse{
		Files: []string{"/a", "/b"},
		Total: 2,
		Limit: 100,
		Order: "small",
	})
	for _, key := range []string{"files", "total", "limit", "order"} {
		if _, ok := got[key]; !ok {
			t.Errorf("响应缺少键 %q，实际: %v", key, got)
		}
	}
	files, ok := got["files"].([]any)
	if !ok || len(files) != 2 {
		t.Fatalf("files 结构不对: %v", got["files"])
	}
}

// ── 错误收敛（SEC-8 安全机制，v4.0.0 重点回归项）────────────────────

// TestSanitizeErrorPayloadStruct 确认错误收敛对具名 struct 仍然生效。
//
// 这是 v4.0.0 重构最关键的安全回归项：响应体从 map 换成 struct 后，
// 若收敛逻辑没跟着改，底层英文错误原文会直接回显给浏览器。
func TestSanitizeErrorPayloadStruct(t *testing.T) {
	cases := []struct {
		name string
		in   any
	}{
		{"errorResponse", errorResponse{Error: "delete failed: permission denied"}},
		{"authErrorResponse", authErrorResponse{Error: "delete failed: permission denied", Auth: true}},
		{"countErrorResponse", countErrorResponse{Error: "delete failed: permission denied", N: 3}},
		{"searchReadyErrorResponse", searchReadyErrorResponse{Error: "delete failed: permission denied", State: "none", Detail: "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := sanitizeErrorPayload(500, c.in)
			got := jsonOf(t, out)
			text, _ := got["error"].(string)
			if text == "delete failed: permission denied" {
				t.Errorf("错误原文未收敛，会直接回显给浏览器: %v", got)
			}
			if text == "" {
				t.Errorf("收敛后 error 为空: %v", got)
			}
		})
	}
}

// TestSanitizeErrorPayloadKeepsOtherFields 确认收敛只改 error，其它字段保留。
func TestSanitizeErrorPayloadKeepsOtherFields(t *testing.T) {
	out := sanitizeErrorPayload(500, countErrorResponse{Error: "permission denied", N: 3})
	got := jsonOf(t, out)
	if got["n"] != float64(3) {
		t.Errorf("n 字段丢失或被改: %v", got)
	}
	if got["auth"] != nil {
		// authErrorResponse 场景另测
		t.Logf("意外出现 auth 字段: %v", got)
	}
	out2 := sanitizeErrorPayload(500, authErrorResponse{Error: "permission denied", Auth: true})
	got2 := jsonOf(t, out2)
	if got2["auth"] != true {
		t.Errorf("auth 字段丢失或被改: %v", got2)
	}
}

// TestSanitizeErrorPayloadMapStillWorks 确认 map 形态（旧接口）没被破坏。
func TestSanitizeErrorPayloadMapStillWorks(t *testing.T) {
	out := sanitizeErrorPayload(500, map[string]any{"error": "permission denied", "n": 3})
	got := jsonOf(t, out)
	if got["n"] != float64(3) {
		t.Errorf("n 字段丢失或被改: %v", got)
	}
	if text, _ := got["error"].(string); text == "permission denied" {
		t.Errorf("map 形态的收敛失效了: %v", got)
	}
}

// TestSanitizeErrorPayloadDoesNotMutate 确认收敛不改原对象。
// 否则调用方持有的结构会被意外改写，影响后续日志或重试逻辑。
func TestSanitizeErrorPayloadDoesNotMutate(t *testing.T) {
	orig := errorResponse{Error: "permission denied"}
	_ = sanitizeErrorPayload(500, orig)
	if orig.Error != "permission denied" {
		t.Errorf("原对象被修改了: %q", orig.Error)
	}
}

// TestSanitizeErrorPayloadChineseUnchanged 确认已是中文的错误不被二次改写。
func TestSanitizeErrorPayloadChineseUnchanged(t *testing.T) {
	orig := errorResponse{Error: "没有权限访问该文件或目录"}
	out := sanitizeErrorPayload(500, orig)
	got := jsonOf(t, out)
	if got["error"] != "没有权限访问该文件或目录" {
		t.Errorf("中文错误被改写了: %v", got)
	}
}

// ── writeJSON 出口 ──────────────────────────────────────────────────────

// TestWriteJSONWithStruct 确保 writeJSON 能正确输出 struct。
func TestWriteJSONWithStruct(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, 200, groupsResponse{
		Groups: []store.Group{},
		Total:  0,
		Page:   1,
		// PageSize 故意设为 0：原 map 里 page_size 总是显式写出，
		// 所以这里也必须出现（v4.0.0 回归项，见 TestGroupsResponseAlwaysHasPage）。
	})
	if rec.Code != 200 {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	for _, key := range []string{"groups", "total", "page", "page_size"} {
		if _, ok := got[key]; !ok {
			t.Errorf("响应缺少键 %q，实际: %s", key, rec.Body.String())
		}
	}
}

// TestWriteJSONSanitizesStructError 端到端验证：经 writeJSON 出口的错误响应已收敛。
func TestWriteJSONSanitizesStructError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, 500, errorResponse{Error: "delete failed: permission denied"})
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if text, _ := got["error"].(string); text == "delete failed: permission denied" {
		t.Errorf("经 writeJSON 出口的错误未收敛: %s", rec.Body.String())
	}
}
