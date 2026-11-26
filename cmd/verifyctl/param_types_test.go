package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPostPathsParamSendsJSONArray 断言注册表里 POST 命令的 paths 参数真正以 JSON 数组进请求体，
// 而不是把 '["/a","/b"]' 当普通字符串发出去。直接取真实命令表，注册表漏改即失败。
func TestPostPathsParamSendsJSONArray(t *testing.T) {
	cmd, ok := findCommand("health trash")
	if !ok {
		t.Fatal("找不到命令 health trash")
	}
	if cmd.Method != http.MethodPost {
		t.Fatalf("health trash 应为 POST，实际 %s", cmd.Method)
	}
	if typ := paramType(cmd, "paths"); typ != "json" {
		t.Fatalf("health trash 的 paths 类型应为 json，实际 %q", typ)
	}

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("解析请求体失败：%v", err)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	if _, _, err := c.Do(context.Background(), cmd, map[string]string{
		"paths": `["/a","/b"]`,
	}); err != nil {
		t.Fatalf("Do 失败：%v", err)
	}

	arr, ok := got["paths"].([]any)
	if !ok {
		t.Fatalf("paths 未以 JSON 数组发送，实际类型 %T 值 %#v", got["paths"], got["paths"])
	}
	if len(arr) != 2 || arr[0] != "/a" || arr[1] != "/b" {
		t.Fatalf("paths 数组内容错误：%#v", arr)
	}
}

// TestPostWorkerParamSendsInt 断言 health scan 的 workers 参数以真整数进请求体。
func TestPostWorkerParamSendsInt(t *testing.T) {
	cmd, ok := findCommand("health scan")
	if !ok {
		t.Fatal("找不到命令 health scan")
	}
	if typ := paramType(cmd, "workers"); typ != "int" {
		t.Fatalf("health scan 的 workers 类型应为 int，实际 %q", typ)
	}

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("解析请求体失败：%v", err)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	if _, _, err := c.Do(context.Background(), cmd, map[string]string{
		"workers": "4",
	}); err != nil {
		t.Fatalf("Do 失败：%v", err)
	}

	num, ok := got["workers"].(float64)
	if !ok {
		t.Fatalf("workers 未以整数发送，实际类型 %T 值 %#v", got["workers"], got["workers"])
	}
	if num != 4 {
		t.Fatalf("workers 数值错误：%v", num)
	}
}

// TestGetArraysStayString 守住「GET 只能走查询串、数组不能 JSON」这条线：
// trash export 是 GET，ids 必须仍是 string（逗号分隔），不得被改成 json。
func TestGetArraysStayString(t *testing.T) {
	cmd, ok := findCommand("trash export")
	if !ok {
		t.Fatal("找不到命令 trash export")
	}
	if cmd.Method != http.MethodGet {
		t.Fatalf("trash export 应为 GET，实际 %s", cmd.Method)
	}
	if typ := paramType(cmd, "ids"); typ != "string" {
		t.Fatalf("GET 的 trash export ids 应保持 string，实际 %q", typ)
	}
}

// TestTrashPreviewGroupKeysSendsJSONArray 守 trash preview.group_keys：POST body 里必须是真数组。
func TestTrashPreviewGroupKeysSendsJSONArray(t *testing.T) {
	cmd, ok := findCommand("trash preview")
	if !ok {
		t.Fatal("找不到命令 trash preview")
	}
	if typ := paramType(cmd, "group_keys"); typ != "json" {
		t.Fatalf("trash preview 的 group_keys 类型应为 json，实际 %q", typ)
	}
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("解析请求体失败：%v", err)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")
	if _, _, err := c.Do(context.Background(), cmd, map[string]string{
		"group_keys": `["g1","g2"]`,
	}); err != nil {
		t.Fatalf("Do 失败：%v", err)
	}
	arr, ok := got["group_keys"].([]any)
	if !ok || len(arr) != 2 || arr[0] != "g1" || arr[1] != "g2" {
		t.Fatalf("group_keys 未以 JSON 数组发送：%T %#v", got["group_keys"], got["group_keys"])
	}
}

// TestSettingsArrayAndObjectFields 守 settings 的数组/对象字段：数组字段以真数组、对象字段以真对象进 body。
func TestSettingsArrayAndObjectFields(t *testing.T) {
	cmd, ok := findCommand("settings")
	if !ok {
		t.Fatal("找不到命令 settings")
	}
	for _, name := range []string{"protect_paths", "match_kinds", "exclude_dirs", "volumes", "search_rules", "search_presets"} {
		if typ := paramType(cmd, name); typ != "json" {
			t.Errorf("settings.%s 类型应为 json，实际 %q", name, typ)
		}
	}
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("解析请求体失败：%v", err)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")
	if _, _, err := c.Do(context.Background(), cmd, map[string]string{
		"protect_paths": `["/p1","/p2"]`,
		"exclude_dirs":  `["/d1"]`,
		"search_rules":  `{"ignore_case":true}`,
	}); err != nil {
		t.Fatalf("Do 失败：%v", err)
	}
	if arr, ok := got["protect_paths"].([]any); !ok || len(arr) != 2 {
		t.Fatalf("protect_paths 未以数组发送：%T %#v", got["protect_paths"], got["protect_paths"])
	}
	if arr, ok := got["exclude_dirs"].([]any); !ok || len(arr) != 1 {
		t.Fatalf("exclude_dirs 未以数组发送：%T %#v", got["exclude_dirs"], got["exclude_dirs"])
	}
	if obj, ok := got["search_rules"].(map[string]any); !ok || obj["ignore_case"] != true {
		t.Fatalf("search_rules 未以对象发送：%T %#v", got["search_rules"], got["search_rules"])
	}
}

// TestSettingsAudioThresholdSendsFloat 守 settings.audio_threshold（服务端 *float64）以真浮点进 body。
func TestSettingsAudioThresholdSendsFloat(t *testing.T) {
	cmd, ok := findCommand("settings")
	if !ok {
		t.Fatal("找不到命令 settings")
	}
	if typ := paramType(cmd, "audio_threshold"); typ != "float" {
		t.Fatalf("settings.audio_threshold 类型应为 float，实际 %q", typ)
	}
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("解析请求体失败：%v", err)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")
	if _, _, err := c.Do(context.Background(), cmd, map[string]string{"audio_threshold": "2.5"}); err != nil {
		t.Fatalf("Do 失败：%v", err)
	}
	f, ok := got["audio_threshold"].(float64)
	if !ok || f != 2.5 {
		t.Fatalf("audio_threshold 未以浮点发送：%T %#v", got["audio_threshold"], got["audio_threshold"])
	}
}

// TestSearchSelectionLimitSendsInt 守 search selection.limit（服务端 int）以真整数进 body。
func TestSearchSelectionLimitSendsInt(t *testing.T) {
	cmd, ok := findCommand("search selection")
	if !ok {
		t.Fatal("找不到命令 search selection")
	}
	for _, name := range []string{"limit", "offset", "min_size"} {
		if typ := paramType(cmd, name); typ != "int" {
			t.Errorf("search selection.%s 类型应为 int，实际 %q", name, typ)
		}
	}
	if typ := paramType(cmd, "case"); typ != "bool" {
		t.Errorf("search selection.case 类型应为 bool，实际 %q", typ)
	}
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("解析请求体失败：%v", err)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")
	if _, _, err := c.Do(context.Background(), cmd, map[string]string{
		"limit": "25", "offset": "10", "case": "true",
	}); err != nil {
		t.Fatalf("Do 失败：%v", err)
	}
	if n, ok := got["limit"].(float64); !ok || n != 25 {
		t.Fatalf("limit 未以整数发送：%T %#v", got["limit"], got["limit"])
	}
	if b, ok := got["case"].(bool); !ok || !b {
		t.Fatalf("case 未以布尔发送：%T %#v", got["case"], got["case"])
	}
}

// TestHealthUndoSendsIntTaskID 守 health undo.task_id（服务端 int64）以真整数进 body。
func TestHealthUndoSendsIntTaskID(t *testing.T) {
	cmd, ok := findCommand("health undo")
	if !ok {
		t.Fatal("找不到命令 health undo")
	}
	if typ := paramType(cmd, "task_id"); typ != "int" {
		t.Fatalf("health undo.task_id 类型应为 int，实际 %q", typ)
	}
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")
	if _, _, err := c.Do(context.Background(), cmd, map[string]string{"task_id": "42"}); err != nil {
		t.Fatalf("Do 失败：%v", err)
	}
	if n, ok := got["task_id"].(float64); !ok || n != 42 {
		t.Fatalf("task_id 未以整数发送：%T %#v", got["task_id"], got["task_id"])
	}
}

// paramType 返回命令里某参数声明的类型，找不到返回空串。
func paramType(cmd Command, name string) string {
	for _, p := range cmd.Params {
		if p.Name == name {
			return p.Type
		}
	}
	return ""
}
