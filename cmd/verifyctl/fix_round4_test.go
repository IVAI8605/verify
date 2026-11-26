package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestBoolFlagSwitchNoValue 守 I1：bool 参数是开关，`--quick` 不带值不应报
// 「flag needs an argument」，且应解析为 true。
func TestBoolFlagSwitchNoValue(t *testing.T) {
	// scan 是 trigger 命令，默认 dry-run 不联网；直接看 would_do.params.quick。
	out, code := runOut(t, "scan", "--quick")
	if code != 0 {
		t.Fatalf("scan --quick 退出码=%d，期望 0；输出=%q", code, out)
	}
	if strings.Contains(out, "flag needs an argument") {
		t.Fatalf("bool 开关被当成取值 flag：%q", out)
	}
	if !strings.Contains(out, `"quick":"true"`) {
		t.Fatalf("--quick 未解析为 true：%q", out)
	}
}

// TestBoolFlagExplicitFalse 守 I1：`--quick=false` 显式关闭要与「未设置」区分。
func TestBoolFlagExplicitFalse(t *testing.T) {
	cmd, ok := findCommand("scan")
	if !ok {
		t.Fatal("缺少 scan 命令")
	}
	params, err := parseCommandParams(cmd, []string{"--quick=false"})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if params["quick"] != "false" {
		t.Fatalf("quick 应为 false，实际 %q", params["quick"])
	}

	// 未设置时不应出现在 params（保持「只发用户显式提供的参数」）。
	params2, err := parseCommandParams(cmd, nil)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if _, exists := params2["quick"]; exists {
		t.Fatalf("未设置 quick 不应进 params：%#v", params2)
	}
}

// TestBoolFlagDoesNotSwallowNextFlag 守 I1 的静默损坏：`--prefix_a --yes /vol1/a`
// 不得把 --yes 当成 prefix_a 的取值吞掉。
func TestBoolFlagDoesNotSwallowNextFlag(t *testing.T) {
	cmd, ok := findCommand("folder_rename")
	if !ok {
		t.Fatal("缺少 folder_rename 命令")
	}
	params, err := parseCommandParams(cmd, []string{"--prefix_a", "--yes", "/vol1/a"})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if params["prefix_a"] != "true" {
		t.Fatalf("prefix_a 应=true，实际 %q（疑似吞掉 --yes）", params["prefix_a"])
	}
	if params["__cli_yes"] != "true" {
		t.Fatalf("--yes 应被识别为确认开关，实际 %#v", params)
	}
	if params["dir"] != "/vol1/a" {
		t.Fatalf("位置参数应落 dir=/vol1/a，实际 %q", params["dir"])
	}
}

// TestBoolFlagReadPathSwitch 守 I1 在读路径：groups --date_only 无值应进查询串 =true。
func TestBoolFlagReadPathSwitch(t *testing.T) {
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	out, code := runOut(t, "--url", srv.URL, "groups", "--date_only", "--min_size", "5")
	if code != 0 {
		t.Fatalf("groups --date_only 退出码=%d；输出=%q", code, out)
	}
	if query.Get("date_only") != "true" {
		t.Fatalf("date_only 应为 true，实际 %q（%v）", query.Get("date_only"), query)
	}
	if query.Get("min_size") != "5" {
		t.Fatalf("min_size 应为 5，实际 %q", query.Get("min_size"))
	}
}

// TestSearchCommandQuery 守 I2：search 主命令能传 q 与各过滤参数。
func TestSearchCommandQuery(t *testing.T) {
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Write([]byte(`{"total":0,"items":[]}`))
	}))
	defer srv.Close()

	out, code := runOut(t, "--url", srv.URL, "search", "--q", "movie", "--case", "--count_only", "--min_size", "10", "--ext", "mp4,mkv", "--limit", "50")
	if code != 0 {
		t.Fatalf("search 退出码=%d；输出=%q", code, out)
	}
	for k, want := range map[string]string{
		"q": "movie", "case": "true", "count_only": "true",
		"min_size": "10", "ext": "mp4,mkv", "limit": "50",
	} {
		if query.Get(k) != want {
			t.Errorf("search 查询串缺 %s=%s（实际 %q）：%v", k, want, query.Get(k), query)
		}
	}
}

// TestSearchDeclaresParams 守 I2 白盒：search 声明齐全。
func TestSearchDeclaresParams(t *testing.T) {
	assertParams(t, "search", map[string]string{
		"q": "string", "mode": "string", "case": "bool", "ext": "string",
		"min_size": "int", "max_size": "int", "dir": "string", "sort": "string",
		"order": "string", "offset": "int", "limit": "int", "cursor": "string",
		"count_only": "bool", "since_ms": "int", "until_ms": "int",
	})
}

// TestEventsCommandTokenUsed 守 I3：events 命令自身的 --token 必须生效，
// 覆盖全局 --token，且以 Bearer 发送（不落到查询串）。
func TestEventsCommandTokenUsed(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		// events 路由虽允许 ?token=，但我们用 Bearer；这里确认没走 query。
		if r.URL.Query().Get("token") != "" {
			t.Errorf("不应把 token 放进查询串：%v", r.URL.Query())
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: hello\n\n"))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	if code := run([]string{"--url", srv.URL, "events", "--token", "cmdtok"}, &buf); code != 0 {
		t.Fatalf("events --token 退出码=%d；输出=%q", code, buf.String())
	}
	if gotAuth != "Bearer cmdtok" {
		t.Fatalf("应使用命令级 token（Bearer cmdtok），实际 %q", gotAuth)
	}

	// 仅全局 --token 时回退全局。
	gotAuth = ""
	buf.Reset()
	if code := run([]string{"--url", srv.URL, "--token", "globaltok", "events"}, &buf); code != 0 {
		t.Fatalf("events 全局 token 退出码=%d；输出=%q", code, buf.String())
	}
	if gotAuth != "Bearer globaltok" {
		t.Fatalf("应回退全局 token，实际 %q", gotAuth)
	}

	// 两者都给时命令级优先。
	gotAuth = ""
	buf.Reset()
	if code := run([]string{"--url", srv.URL, "--token", "globaltok", "events", "--token", "cmdtok"}, &buf); code != 0 {
		t.Fatalf("events 双 token 退出码=%d；输出=%q", code, buf.String())
	}
	if gotAuth != "Bearer cmdtok" {
		t.Fatalf("命令级 token 应优先，实际 %q", gotAuth)
	}
}

// TestDryRunRedactsAccessToken 守 I4：write 默认 dry-run 的 would_do.params 不得回显 access_token 明文。
func TestDryRunRedactsAccessToken(t *testing.T) {
	const secret = "cli-明文令牌-禁止回显-4f2a"
	out, code := runOut(t, "settings", "--access_token", secret)
	if code != 0 {
		t.Fatalf("settings dry-run 退出码=%d；输出=%q", code, out)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("dry-run 回显了 access_token 明文：%q", out)
	}
	// JSON 会把 <> 转义成 \u003c..\u003e，故解析后比对 params 里的真实值。
	var env struct {
		Data struct {
			WouldDo struct {
				Params map[string]string `json:"params"`
			} `json:"would_do"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("解析 dry-run 输出失败：%v（%q）", err, out)
	}
	if got := env.Data.WouldDo.Params["access_token"]; got != "<redacted>" {
		t.Fatalf("access_token 应脱敏为 <redacted>，实际 %q", got)
	}
	// 非敏感参数保持原样。
	out2, _ := runOut(t, "settings", "--min_size", "1024", "--access_token", secret)
	if !strings.Contains(out2, `"min_size":"1024"`) {
		t.Fatalf("非敏感参数不应被脱敏：%q", out2)
	}
}

// TestRedactSensitiveParamsUnit 单测脱敏谓词与副本语义。
func TestRedactSensitiveParamsUnit(t *testing.T) {
	in := map[string]string{
		"access_token": "S1", "password": "S2", "foo_secret": "S3",
		"selection_token": "S4", "min_size": "1024", "pretty": "x",
	}
	out := redactSensitiveParams(in)
	for _, k := range []string{"access_token", "password", "foo_secret", "selection_token"} {
		if out[k] != "<redacted>" {
			t.Errorf("%s 应被脱敏，实际 %q", k, out[k])
		}
	}
	if out["min_size"] != "1024" {
		t.Errorf("min_size 不应被脱敏，实际 %q", out["min_size"])
	}
	// 原 map 不被修改（展示层副本）。
	if in["access_token"] != "S1" {
		t.Errorf("redactSensitiveParams 不应修改入参原 map")
	}
}

// TestDangerDryRunTokenUsesUnredacted 守 I4：danger dry-run 的展示脱敏不影响确认令牌，
// 令牌必须用未脱敏参数计算，否则用户回填时校验失败。
func TestDangerDryRunTokenUsesUnredacted(t *testing.T) {
	cmd := Command{Name: "synth", Path: "/api/synth", Method: http.MethodPost}
	req := map[string]string{"access_token": "SEC", "foo": "bar"}
	out, err := marshalDangerDryRun(cmd, http.MethodPost, "/api/synth", req, false)
	if err != nil {
		t.Fatalf("marshalDangerDryRun 失败：%v", err)
	}
	var res dangerDryRunResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if res.WouldDo.Params["access_token"] != "<redacted>" {
		t.Fatalf("would_do 未脱敏：%#v", res.WouldDo.Params)
	}
	// 令牌按未脱敏参数计算 → 与执行侧 makeConfirmToken(cmd, req) 一致。
	if want := makeConfirmToken(cmd, req); res.ConfirmToken != want {
		t.Fatalf("确认令牌用了脱敏参数（%s），应与未脱敏一致（%s）", res.ConfirmToken, want)
	}
}

// TestPositionalFlagRejected 守 #5：位置参数之后残留的 -flag 不得静默当值。
func TestPositionalFlagRejected(t *testing.T) {
	out, code := runOut(t, "groups", "mode", "-q", "x")
	if code != 2 {
		t.Fatalf("残留 -flag 退出码=%d，期望 2；输出=%q", code, out)
	}
	if !strings.Contains(out, "位置参数之前") {
		t.Fatalf("应提示「参数须置于位置参数之前」：%q", out)
	}
}

// TestUploadOpenFailureUsageError 守 #6：本地上传文件打不开是用法错误（exit 2），不是连接失败（exit 4）。
func TestUploadOpenFailureUsageError(t *testing.T) {
	out, code := runOut(t, "--url", "http://127.0.0.1:1", "import", "--file", "/definitely/not/here.bin", "--yes")
	if code != 2 {
		t.Fatalf("上传文件打不开退出码=%d，期望 2；输出=%q", code, out)
	}
	if !strings.Contains(out, "打开上传文件失败") {
		t.Fatalf("应提示打开上传文件失败：%q", out)
	}
}

// TestSnapshotCommandsDeclarePaging 守 #7/#8：op-snapshots 与 rule-search results 声明 page/page_size。
func TestSnapshotCommandsDeclarePaging(t *testing.T) {
	assertParams(t, "op-snapshots", map[string]string{"page": "int", "page_size": "int"})
	assertParams(t, "rule-search results", map[string]string{"page": "int", "page_size": "int"})
}

// TestSnapshotPagingReachesQuery 守 #7/#8 运行期：分页参数真的进查询串。
func TestSnapshotPagingReachesQuery(t *testing.T) {
	cases := []struct{ name string }{{"op-snapshots"}, {"rule-search results"}}
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	for _, c := range cases {
		cmd, ok := findCommand(c.name)
		if !ok {
			t.Fatalf("缺少命令 %s", c.name)
		}
		query = nil
		cl := NewClient(srv.URL, "t")
		if _, err := runReadCommand(context.Background(), cl, cmd, map[string]string{"page": "2", "page_size": "50"}); err != nil {
			t.Fatalf("%s 执行失败：%v", c.name, err)
		}
		if query.Get("page") != "2" || query.Get("page_size") != "50" {
			t.Errorf("%s 分页未进查询串：%v", c.name, query)
		}
	}
}
