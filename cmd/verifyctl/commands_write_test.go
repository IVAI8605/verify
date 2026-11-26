package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteRequiresYes 验证无 --yes 时写操作只做本地 dry-run，绝不是一个真请求。
// 这是 Task 8 的核心红线：写操作默认不落盘。
func TestWriteRequiresYes(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")

	cmd := Command{Name: "settings", Path: "/api/settings", Method: "POST", SideEffect: "write"}
	out, err := runWriteCommand(context.Background(), c, cmd, map[string]string{})
	if err != nil {
		t.Fatalf("无 --yes 不应报错：%v", err)
	}
	if called {
		t.Fatal("无 --yes 不应执行写操作")
	}
	if !strings.Contains(string(out), "would_do") {
		t.Fatalf("dry-run 回包缺 would_do：%s", out)
	}
}

// TestWriteDryRunNoRequest 显式 --dry-run 同样不发请求。
func TestWriteDryRunNoRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")

	cmd := Command{Name: "move", Path: "/api/move", Method: "POST", SideEffect: "write"}
	if _, err := runWriteCommand(context.Background(), c, cmd, map[string]string{"__cli_dry_run": "true"}); err != nil {
		t.Fatalf("dry-run 不应报错：%v", err)
	}
	if called {
		t.Fatal("--dry-run 不应发请求")
	}
}

// TestWriteYesExecutes 带 --yes 才真正发请求，并按注册表类型编码请求体、原样透传响应。
func TestWriteYesExecutes(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")

	cmd := Command{
		Name: "folder_rename", Path: "/api/folder_rename", Method: "POST", SideEffect: "write",
		Params: []Param{{"dir", "string", true, ""}, {"prefix_a", "bool", false, "false"}},
	}
	out, err := runWriteCommand(context.Background(), c, cmd, map[string]string{
		"__cli_yes": "true", "dir": "/vol1/a", "prefix_a": "true",
	})
	if err != nil {
		t.Fatalf("--yes 执行失败：%v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/folder_rename" {
		t.Fatalf("请求错误：%s %s", gotMethod, gotPath)
	}
	if gotBody["dir"] != "/vol1/a" || gotBody["prefix_a"] != true {
		t.Fatalf("请求体字段/类型错误：%#v", gotBody)
	}
	if !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("应原样透传响应体：%s", out)
	}
}

// TestWriteYesPropagatesHTTPError 服务端 409 要带回状态码与中文原文，供入口映射退出码。
func TestWriteYesPropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"扫描进行中，请稍后再试"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")

	cmd := Command{Name: "trash restore", Path: "/api/trash/restore", Method: "POST", SideEffect: "write"}
	_, err := runWriteCommand(context.Background(), c, cmd, map[string]string{"__cli_yes": "true", "ids": "[1,2]"})
	if err == nil {
		t.Fatal("409 应返回错误")
	}
	status, detail := httpStatusFromError(err)
	if status != http.StatusConflict {
		t.Fatalf("状态码 = %d，期望 409", status)
	}
	if detail == nil || !strings.Contains(detail.Message, "扫描进行中") {
		t.Fatalf("应保留服务端中文原文：%+v", detail)
	}
}

// TestWriteWouldDoShape dry-run 回包结构：command/method/path/params，内部 key 与控制参数不外泄。
func TestWriteWouldDoShape(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "t")
	cmd := Command{Name: "settings", Path: "/api/settings", Method: "POST", SideEffect: "write"}
	out, err := runWriteCommand(context.Background(), c, cmd, map[string]string{
		"__cli_yes": "true", "__cli_dry_run": "true", "pretty": "true", "confirm_token": "x", "watch": "true",
	})
	if err != nil {
		t.Fatalf("dry-run 失败：%v", err)
	}
	var got struct {
		DryRun  bool `json:"dry_run"`
		WouldDo struct {
			Command string            `json:"command"`
			Method  string            `json:"method"`
			Path    string            `json:"path"`
			Params  map[string]string `json:"params"`
		} `json:"would_do"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("回包不是合法 JSON：%v，%s", err, out)
	}
	if !got.DryRun || got.WouldDo.Command != "settings" || got.WouldDo.Method != "POST" || got.WouldDo.Path != "/api/settings" {
		t.Fatalf("would_do 结构错误：%+v", got)
	}
	if got.WouldDo.Params["watch"] != "true" {
		t.Fatalf("业务参数 watch 应出现在 would_do.params：%+v", got.WouldDo.Params)
	}
	for _, k := range []string{"__cli_yes", "__cli_dry_run", "pretty", "confirm_token"} {
		if _, ok := got.WouldDo.Params[k]; ok {
			t.Fatalf("控制参数 %s 不应外泄：%+v", k, got.WouldDo.Params)
		}
	}
}

// TestAllWriteCommandsWired 断言命令表里每一条 write 命令都接上了 Run（Job A 全量接线）。
func TestAllWriteCommandsWired(t *testing.T) {
	var missing []string
	for _, cmd := range Commands {
		if cmd.SideEffect == "write" && cmd.Run == nil {
			missing = append(missing, cmd.Name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("以下 write 命令尚未接入 Run：%v", missing)
	}
}

// ---- Job B：传输通道 / 参数完整性 ----

// TestRenamePresetUpdateIDInPath 服务端从 URL PATH 读 id（TrimPrefix /api/rename/presets/），
// 必须把 id 拼进路径，而不是塞进 body。
func TestRenamePresetUpdateIDInPath(t *testing.T) {
	cmd, ok := findCommand("rename preset update")
	if !ok {
		t.Fatal("缺少 rename preset update")
	}
	var gotPath string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	if _, err := runWriteCommand(context.Background(), c, cmd, map[string]string{
		"__cli_yes": "true", "id": "7", "name": "我的预设", "methods": `[{"type":"replace"}]`,
	}); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if gotPath != "/api/rename/presets/7" {
		t.Fatalf("路径 = %s，期望 /api/rename/presets/7", gotPath)
	}
	if _, ok := body["id"]; ok {
		t.Fatalf("id 不应进 body：%#v", body)
	}
	if body["name"] != "我的预设" {
		t.Fatalf("body.name 丢失：%#v", body)
	}
}

// TestVideoTagsRecomputeIDInQuery 服务端 POST 但从 query 读 id，CLI 必须走查询串。
func TestVideoTagsRecomputeIDInQuery(t *testing.T) {
	cmd, ok := findCommand("video_tags recompute")
	if !ok {
		t.Fatal("缺少 video_tags recompute")
	}
	var gotQuery, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	if _, _, err := c.Do(context.Background(), cmd, map[string]string{"id": "9"}); err != nil {
		t.Fatalf("Do 失败：%v", err)
	}
	if gotPath != "/api/video_tags/recompute" {
		t.Fatalf("路径 = %s", gotPath)
	}
	if !strings.Contains(gotQuery, "id=9") {
		t.Fatalf("id 应走查询串，实际 query=%q", gotQuery)
	}
}

// TestTranscodeSendsPathsAndQuality 转码服务端需要 paths []string + quality + max_res。
func TestTranscodeSendsPathsAndQuality(t *testing.T) {
	cmd, ok := findCommand("transcode")
	if !ok {
		t.Fatal("缺少 transcode")
	}
	if paramType(cmd, "paths") != "json" {
		t.Fatalf("transcode.paths 应为 json，实际 %q", paramType(cmd, "paths"))
	}
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	if _, err := runWriteCommand(context.Background(), c, cmd, map[string]string{
		"__cli_yes": "true", "paths": `["/a.mp4","/b.mp4"]`, "quality": "balanced", "max_res": "1080",
	}); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if arr, ok := body["paths"].([]any); !ok || len(arr) != 2 {
		t.Fatalf("paths 未以 JSON 数组发送：%T %#v", body["paths"], body["paths"])
	}
	if body["quality"] != "balanced" || body["max_res"] != "1080" {
		t.Fatalf("quality/max_res 丢失：%#v", body)
	}
}

// TestFolderRenameSendsDir 目录改名服务端需要 dir + prefix_a。
func TestFolderRenameSendsDir(t *testing.T) {
	cmd, ok := findCommand("folder_rename")
	if !ok {
		t.Fatal("缺少 folder_rename")
	}
	if paramType(cmd, "dir") != "string" {
		t.Fatal("folder_rename 应声明 dir 参数")
	}
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	if _, err := runWriteCommand(context.Background(), c, cmd, map[string]string{
		"__cli_yes": "true", "dir": "/vol1/x", "prefix_a": "false",
	}); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if body["dir"] != "/vol1/x" {
		t.Fatalf("dir 丢失：%#v", body)
	}
}

// TestRenamePresetsCreateBodyFields 新建预设服务端 decode store.RenamePreset，需要 name+methods。
func TestRenamePresetsCreateBodyFields(t *testing.T) {
	cmd, ok := findCommand("rename presets")
	if !ok {
		t.Fatal("缺少 rename presets")
	}
	if paramType(cmd, "methods") != "string" {
		t.Fatalf("rename presets.methods 应为 string（服务端字段是 JSON 文本），实际 %q", paramType(cmd, "methods"))
	}
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	if _, err := runWriteCommand(context.Background(), c, cmd, map[string]string{
		"__cli_yes": "true", "name": "预设A", "methods": `[{"type":"replace"}]`, "description": "备注",
	}); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if body["name"] != "预设A" || body["description"] != "备注" {
		t.Fatalf("name/description 丢失：%#v", body)
	}
	if s, ok := body["methods"].(string); !ok || !strings.Contains(s, "replace") {
		t.Fatalf("methods 应作为 JSON 文本字段发送：%#v", body["methods"])
	}
}

// TestCleanupPlansSendsName 清理计划服务端校验 name 非空，CLI 必须声明并发送。
func TestCleanupPlansSendsName(t *testing.T) {
	cmd, ok := findCommand("cleanup plans")
	if !ok {
		t.Fatal("缺少 cleanup plans")
	}
	if paramType(cmd, "name") != "string" {
		t.Fatal("cleanup plans 应声明 name 参数")
	}
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	if _, err := runWriteCommand(context.Background(), c, cmd, map[string]string{
		"__cli_yes": "true", "name": "每周清理", "schedule": "daily 03:00", "match_mode": "exact",
	}); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if body["name"] != "每周清理" || body["schedule"] != "daily 03:00" || body["match_mode"] != "exact" {
		t.Fatalf("字段丢失：%#v", body)
	}
}

// TestFSRootsSendsPath fs roots 服务端 decode addBrowseRootReq{Path}。
func TestFSRootsSendsPath(t *testing.T) {
	cmd, ok := findCommand("fs roots")
	if !ok {
		t.Fatal("缺少 fs roots")
	}
	if paramType(cmd, "path") != "string" {
		t.Fatal("fs roots 应声明 path 参数")
	}
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	if _, err := runWriteCommand(context.Background(), c, cmd, map[string]string{"__cli_yes": "true", "path": "/vol1/movies"}); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if body["path"] != "/vol1/movies" {
		t.Fatalf("path 丢失：%#v", body)
	}
}

// TestTrashPostsBody 真正移入隔离区是 POST /api/trash，带 paths/group_key/group_keys。
func TestTrashPostsBody(t *testing.T) {
	cmd, ok := findCommand("trash")
	if !ok {
		t.Fatal("缺少 trash")
	}
	if cmd.Method != http.MethodPost {
		t.Fatalf("trash 应为 POST，实际 %s（GET /api/trash 只列清单且忽略这些参数）", cmd.Method)
	}
	if cmd.SideEffect != "write" {
		t.Fatalf("trash 应为 write，实际 %s", cmd.SideEffect)
	}
	if paramType(cmd, "paths") != "json" || paramType(cmd, "group_keys") != "json" {
		t.Fatalf("trash.paths/group_keys 应为 json：%q %q", paramType(cmd, "paths"), paramType(cmd, "group_keys"))
	}
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("方法 = %s，期望 POST", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	if _, err := runWriteCommand(context.Background(), c, cmd, map[string]string{
		"__cli_yes": "true", "paths": `["/a","/b"]`, "group_key": "k1", "group_keys": `["k1","k2"]`,
	}); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if arr, ok := body["paths"].([]any); !ok || len(arr) != 2 {
		t.Fatalf("paths 未以数组发送：%T %#v", body["paths"], body["paths"])
	}
	if body["group_key"] != "k1" {
		t.Fatalf("group_key 丢失：%#v", body)
	}
}

// TestOplogExportQueryParams oplog export 是 POST 但服务端只从 query 读过滤参数。
func TestOplogExportQueryParams(t *testing.T) {
	cmd, ok := findCommand("oplog export")
	if !ok {
		t.Fatal("缺少 oplog export")
	}
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte("time,action\n"))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	if _, err := runWriteCommand(context.Background(), c, cmd, map[string]string{
		"__cli_yes": "true", "action": "delete", "since": "100", "keyword": "x",
	}); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if !strings.Contains(gotQuery, "action=delete") || !strings.Contains(gotQuery, "since=100") {
		t.Fatalf("过滤参数应走查询串，实际 query=%q", gotQuery)
	}
}

// TestTrimProbeSendsDurationsAndThreshold trim probe 服务端需要 durations + threshold。
func TestTrimProbeSendsDurationsAndThreshold(t *testing.T) {
	cmd, ok := findCommand("trim probe")
	if !ok {
		t.Fatal("缺少 trim probe")
	}
	if paramType(cmd, "durations") != "json" || paramType(cmd, "threshold") != "float" {
		t.Fatalf("trim probe durations/threshold 类型错误：%q %q", paramType(cmd, "durations"), paramType(cmd, "threshold"))
	}
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	if _, err := runWriteCommand(context.Background(), c, cmd, map[string]string{
		"__cli_yes": "true", "group_key": "g", "paths": `["/a","/b"]`, "durations": `[1.5,2.5]`, "threshold": "0.5",
	}); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if arr, ok := body["durations"].([]any); !ok || len(arr) != 2 {
		t.Fatalf("durations 未以数组发送：%T %#v", body["durations"], body["durations"])
	}
	if f, ok := body["threshold"].(float64); !ok || f != 0.5 {
		t.Fatalf("threshold 未以浮点发送：%#v", body["threshold"])
	}
}

// TestImportMultipartUpload import 是 multipart 上传：file 按本地路径读取，name 作为表单字段。
func TestImportMultipartUpload(t *testing.T) {
	cmd, ok := findCommand("import")
	if !ok {
		t.Fatal("缺少 import")
	}
	dir := t.TempDir()
	csv := filepath.Join(dir, "rows.csv")
	if err := os.WriteFile(csv, []byte("path,name\n/a,1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotName, gotFile string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Errorf("不是合法 multipart：%v", err)
			w.WriteHeader(400)
			return
		}
		gotName = r.FormValue("name")
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("缺少 file 字段：%v", err)
			w.WriteHeader(400)
			return
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		gotFile = string(b)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t")
	if _, err := runWriteCommand(context.Background(), c, cmd, map[string]string{
		"__cli_yes": "true", "file": csv, "name": "导入会话",
	}); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if gotName != "导入会话" {
		t.Fatalf("name 表单字段 = %q", gotName)
	}
	if !strings.Contains(gotFile, "/a,1") {
		t.Fatalf("文件内容未上传：%q", gotFile)
	}
}
