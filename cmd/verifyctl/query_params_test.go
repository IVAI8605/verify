package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// wantGroupQueryParams 是 internal/web/common.go 里 groupQuery 实际读取的全部查询参数，
// 以及各自的服务端读取方式对应的 CLI 类型。凡走 groupQuery 的接口都必须声明齐全。
var wantGroupQueryParams = map[string]string{
	"page_size":          "int",
	"page":               "int",
	"min_size":           "int",
	"max_kb":             "int",
	"since_ms":           "int",
	"until_ms":           "int",
	"ext":                "string",
	"exts":               "string",
	"q":                  "string",
	"sort":               "string",
	"order":              "string",
	"date_only":          "bool",
	"same_drive":         "string",
	"max_dur_diff":       "int",
	"ignore_same_folder": "bool",
	"folder_name":        "string",
	"folder_depth":       "int",
	"similar_name":       "bool",
	"size_tol_kb":        "int",
	"tag_name":           "string",
	"tag_value":          "string",
	"skip_total":         "bool",
}

// assertParams 断言命令声明了 name→type 的全部参数（类型必须一致，必填性不在此断言）。
func assertParams(t *testing.T, cmdName string, want map[string]string) {
	t.Helper()
	cmd, ok := findCommand(cmdName)
	if !ok {
		t.Fatalf("注册表缺少命令 %s", cmdName)
	}
	for name, typ := range want {
		got := paramType(cmd, name)
		if got == "" {
			t.Errorf("%s 缺可选参数 %s（期望类型 %s）", cmdName, name, typ)
			continue
		}
		if got != typ {
			t.Errorf("%s 的参数 %s 类型应为 %s，实际 %s", cmdName, name, typ, got)
		}
	}
}

// TestGroupsDeclareGroupQueryParams 守 /api/groups（handleGroups 调 groupQuery）声明齐全。
func TestGroupsDeclareGroupQueryParams(t *testing.T) {
	assertParams(t, "groups", wantGroupQueryParams)
	// handleGroups 自身读取的 mode 与 cross。
	assertParams(t, "groups", map[string]string{"mode": "string", "cross": "bool"})
}

// TestGroupFamilyDeclareGroupQueryParams 守其余走 groupQuery 的分组接口继承同一套过滤参数。
func TestGroupFamilyDeclareGroupQueryParams(t *testing.T) {
	for _, name := range []string{
		"groups video", "groups intro", "groups audio", "groups fuzzy", "groups semantic", "groups tag",
	} {
		assertParams(t, name, wantGroupQueryParams)
	}
	// 各命令的专属前置参数不得因共享参数而丢失。
	assertParams(t, "groups fuzzy", map[string]string{"near": "string"})
	assertParams(t, "groups tag", map[string]string{"tag_id": "int"})
	assertParams(t, "groups intro", map[string]string{"n": "int"})
}

// TestGroupSpecialParamsComeFirst 守 I5：专属参数必须排声明首位，
// 否则位置参数按序填充会错位（`groups intro 10` 会静默落成 page_size=10）。
func TestGroupSpecialParamsComeFirst(t *testing.T) {
	cases := map[string]string{
		"groups":       "mode",
		"groups intro": "n",
		"groups fuzzy": "near",
		"groups tag":   "tag_id",
		"groups photo": "threshold",
	}
	for cmdName, wantFirst := range cases {
		cmd, ok := findCommand(cmdName)
		if !ok {
			t.Fatalf("注册表缺少命令 %s", cmdName)
		}
		if len(cmd.Params) == 0 {
			t.Fatalf("%s 不应无参数", cmdName)
		}
		if got := cmd.Params[0].Name; got != wantFirst {
			t.Errorf("%s 的首个参数应为 %s（专属参数在前），实际 %s", cmdName, wantFirst, got)
		}
	}
}

// TestGroupsIntroPositionalFillsN 守 I5 的运行时后果：位置参数 10 必须落到 n，不是 page_size。
func TestGroupsIntroPositionalFillsN(t *testing.T) {
	cmd, ok := findCommand("groups intro")
	if !ok {
		t.Fatal("注册表缺少 groups intro")
	}
	params, err := parseCommandParams(cmd, []string{"10"})
	if err != nil {
		t.Fatalf("解析位置参数失败：%v", err)
	}
	if params["n"] != "10" {
		t.Errorf("位置参数应填 n=10，实际 n=%q", params["n"])
	}
	if params["page_size"] != "" {
		t.Errorf("page_size 不应被位置参数占用，实际 %q", params["page_size"])
	}
}

// TestPhotoFolderDeclareRealParams 守 I8：photo / folder 只声明 handler 真实读取的参数。
// photo（photo.go:26）读 threshold + sortGroupsQ 的 sort/order + 标签过滤；
// folder（media.go:40）读 sort/order + 标签过滤，两者都不调 groupQuery、不读分页。
func TestPhotoFolderDeclareRealParams(t *testing.T) {
	assertParams(t, "groups photo", map[string]string{
		"threshold": "int", "sort": "string", "order": "string",
		"tag_name": "string", "tag_value": "string",
	})
	assertParams(t, "groups folder", map[string]string{
		"sort": "string", "order": "string",
		"tag_name": "string", "tag_value": "string",
	})

	// 不得超声明：这两个 handler 不调 groupQuery，故不该出现 min_size/q 等共享过滤参数。
	sharedOnly := []string{"min_size", "max_kb", "since_ms", "until_ms", "q", "ext", "exts", "page", "page_size"}
	for _, name := range []string{"groups photo", "groups folder"} {
		cmd, _ := findCommand(name)
		for _, p := range sharedOnly {
			if paramType(cmd, p) != "" {
				t.Errorf("%s 不该声明未读取的参数 %s", name, p)
			}
		}
	}
}

// TestPhotoThresholdReachesQuery 守 I8：photo 的 threshold 真的进查询串。
func TestPhotoThresholdReachesQuery(t *testing.T) {
	cmd, ok := findCommand("groups photo")
	if !ok {
		t.Fatal("注册表缺少 groups photo")
	}
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	if _, err := runReadCommand(context.Background(), c, cmd, map[string]string{"threshold": "12", "sort": "waste", "order": "asc"}); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	for k, want := range map[string]string{"threshold": "12", "sort": "waste", "order": "asc"} {
		if query.Get(k) != want {
			t.Errorf("查询串缺 %s=%s（实际 %q）：%v", k, want, query.Get(k), query)
		}
	}
}

// TestOpLogDeclareQueryParams 守 /api/oplog（opQueryFromRequest）声明齐全。
func TestOpLogDeclareQueryParams(t *testing.T) {
	assertParams(t, "oplog", map[string]string{
		"action":    "string",
		"result":    "string",
		"keyword":   "string",
		"since":     "int",
		"until":     "int",
		"page":      "int",
		"page_size": "int",
		"before_id": "int",
	})
	// oplog export 早就声明了同一套，这里顺带防回退。
	assertParams(t, "oplog export", map[string]string{"action": "string", "before_id": "int"})
}

// TestImportGroupsDeclarePageParams 守 /api/import/groups 的分页参数，且 session_id 仍必填。
func TestImportGroupsDeclarePageParams(t *testing.T) {
	assertParams(t, "import groups", map[string]string{"page": "int", "page_size": "int"})
	cmd, _ := findCommand("import groups")
	for _, p := range cmd.Params {
		if p.Name == "session_id" && (!p.Required || p.Type != "int") {
			t.Fatalf("import groups 的 session_id 必须仍是必填 int：%+v", p)
		}
	}
}

// TestNewOptionalParamsReachQuery 断言新加的可选参数真的进查询串（而不是被本地吞掉）。
// 只提供显式参数，未提供的不得出现在查询串里（服务端把空串当非法值会 400）。
func TestNewOptionalParamsReachQuery(t *testing.T) {
	cmd, ok := findCommand("groups")
	if !ok {
		t.Fatal("注册表缺少 groups")
	}
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	params := map[string]string{
		"min_size": "1024", "max_kb": "2048", "ext": "mp4", "exts": "mp4,mkv",
		"q": "keyword", "sort": "waste", "order": "asc",
		"date_only": "true", "same_drive": "/vol1", "skip_total": "true",
		"tag_name": "lang", "tag_value": "zh",
	}
	if _, err := runReadCommand(context.Background(), c, cmd, params); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	for k, v := range params {
		if query.Get(k) != v {
			t.Errorf("查询串缺 %s=%s（实际 %q）：%v", k, v, query.Get(k), query)
		}
	}
	// 未显式提供的可选参数不得出现（避免给服务端塞空串）。
	for _, absent := range []string{"max_dur_diff", "folder_depth", "size_tol_kb", "until_ms"} {
		if query.Has(absent) {
			t.Errorf("未提供的参数 %s 不应出现在查询串：%v", absent, query)
		}
	}
}

// TestOpLogParamsReachQuery 守 oplog 的新增筛选参数真的发给服务端。
func TestOpLogParamsReachQuery(t *testing.T) {
	cmd, _ := findCommand("oplog")
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	params := map[string]string{"action": "delete", "result": "ok", "keyword": "电影", "since": "1", "until": "2", "page": "3", "page_size": "50", "before_id": "99"}
	if _, err := runReadCommand(context.Background(), c, cmd, params); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	for k, v := range params {
		if query.Get(k) != v {
			t.Errorf("oplog 查询串缺 %s=%s（实际 %q）：%v", k, v, query.Get(k), query)
		}
	}
}
