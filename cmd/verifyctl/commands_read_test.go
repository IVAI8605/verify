package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTrashListNotTrash 验证只读隔离区命令走 /api/trash/list，
// 绝不复用会因 HealTrashRows 写库的 /api/trash。
func TestTrashListNotTrash(t *testing.T) {
	cmd, ok := findCommand("trash list")
	if !ok {
		t.Fatal("命令表应有 trash list")
	}
	if cmd.SideEffect != "none" {
		t.Fatalf("trash list 应为 none，实际 %s", cmd.SideEffect)
	}
	if cmd.Path != "/api/trash/list" {
		t.Fatalf("trash list path = %s，期望 /api/trash/list", cmd.Path)
	}
	trash, ok := findCommand("trash")
	if !ok || trash.Path != "/api/trash" || trash.SideEffect == "none" {
		t.Fatalf("/api/trash 应保持写操作语义：%+v", trash)
	}

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path == "/api/trash" {
			t.Error("只读命令不得请求 /api/trash（该接口会清理幽灵行并写库）")
		}
		w.Write([]byte(`{"ok":true,"items":[]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	if _, err := runReadCommand(context.Background(), c, cmd, map[string]string{"limit": "2"}); err != nil {
		t.Fatalf("trash list 执行失败：%v", err)
	}
	if gotPath != "/api/trash/list" {
		t.Fatalf("实际请求 %s，期望 /api/trash/list", gotPath)
	}
}

// TestGroupsModeName 验证同名分组用 ?mode=name，且仍落在 /api/groups。
func TestGroupsModeName(t *testing.T) {
	cmd, ok := findCommand("groups")
	if !ok {
		t.Fatal("命令表应有 groups")
	}
	if cmd.Path != "/api/groups" {
		t.Fatalf("groups path = %s，期望 /api/groups（无 /api/groups/name 路由）", cmd.Path)
	}
	found := false
	for _, p := range cmd.Params {
		if p.Name == "mode" {
			found = true
		}
	}
	if !found {
		t.Fatal("groups 应登记 mode 参数")
	}

	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		if r.URL.Path != "/api/groups" {
			t.Errorf("路径错误：%s", r.URL.Path)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	if _, err := runReadCommand(context.Background(), c, cmd, map[string]string{"mode": "name"}); err != nil {
		t.Fatalf("groups mode=name 执行失败：%v", err)
	}
	if !strings.Contains(query, "mode=name") {
		t.Fatalf("查询串缺 mode=name：%q", query)
	}
}

// TestReadPassthrough 验证服务端未知字段原样透传，--pretty 只缩进不丢字段。
func TestReadPassthrough(t *testing.T) {
	const body = `{"ok":true,"items":[{"key":"a"}],"未来字段":{"深度":3,"list":[1,2]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	cmd := Command{Name: "groups", Path: "/api/groups", Method: "GET", SideEffect: "none"}

	got, err := runReadCommand(context.Background(), c, cmd, nil)
	if err != nil {
		t.Fatalf("只读执行失败：%v", err)
	}
	if string(got) != body {
		t.Fatalf("响应体被改写：得到 %s，期望 %s", got, body)
	}
	if !strings.Contains(string(got), "未来字段") {
		t.Fatal("未知字段被丢弃")
	}

	pretty, err := runReadCommand(context.Background(), c, cmd, map[string]string{"pretty": "true"})
	if err != nil {
		t.Fatalf("pretty 执行失败：%v", err)
	}
	if !json.Valid(pretty) || !strings.Contains(string(pretty), "\n") {
		t.Fatalf("pretty 未产生缩进合法 JSON：%s", pretty)
	}
	for _, key := range []string{"未来字段", "深度", "items", "ok"} {
		if !strings.Contains(string(pretty), key) {
			t.Errorf("pretty 丢了字段 %s：%s", key, pretty)
		}
	}

	// 控制参数不得泄漏成查询串。
	var query string
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Write([]byte(`{}`))
	})
	if _, err := runReadCommand(context.Background(), c, cmd, map[string]string{"pretty": "true", "__cli_dry_run": "true", "__cli_yes": "true"}); err != nil {
		t.Fatalf("控制参数场景执行失败：%v", err)
	}
	if strings.Contains(query, "pretty") || strings.Contains(query, "__cli_") {
		t.Fatalf("控制参数泄漏进查询串：%q", query)
	}
	// dry_run 是真实业务参数，不得被本地开关误过滤。
	if _, err := runReadCommand(context.Background(), c, cmd, map[string]string{"dry_run": "true"}); err != nil {
		t.Fatalf("业务 dry_run 场景执行失败：%v", err)
	}
	if !strings.Contains(query, "dry_run=true") {
		t.Fatalf("业务 dry_run 未透传到查询串：%q", query)
	}
}

// TestPageSizeClamped 验证 page_size 上限钳到 1000（服务端也钳，但 CLI 先行收敛）。
func TestPageSizeClamped(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cmd, _ := findCommand("groups")
	c := NewClient(srv.URL, "t")
	if _, err := runReadCommand(context.Background(), c, cmd, map[string]string{"page_size": "99999"}); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if !strings.Contains(query, "page_size=1000") {
		t.Fatalf("page_size 未钳到 1000：%q", query)
	}
}

// TestAllNoneCommandsWired 验证全部 none 命令都挂上 Run，
// write/destructive 仍保持未接入（trigger 已由 Task 7 接入，不在此断言范围内）。
func TestAllNoneCommandsWired(t *testing.T) {
	none, others := 0, 0
	for _, cmd := range Commands {
		if cmd.SideEffect == "none" {
			none++
			if cmd.Run == nil {
				t.Errorf("none 命令 %s 未挂 Run", cmd.Name)
			}
			continue
		}
		if cmd.SideEffect == "write" || cmd.SideEffect == "destructive" {
			others++
			if cmd.Run != nil {
				t.Errorf("副作用命令 %s 不应在本任务接入 Run", cmd.Name)
			}
		}
	}
	if none == 0 || others == 0 {
		t.Fatalf("命令表分类异常：none=%d 其他=%d", none, others)
	}
}

// TestReadCommandHTTPErrorMapping 验证 HTTP 错误经类型化错误透出状态码与中文原文。
func TestReadCommandHTTPErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"鉴权失败，令牌无效"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "cli-tok")
	cmd, _ := findCommand("status")
	_, err := runReadCommand(context.Background(), c, cmd, nil)
	if err == nil {
		t.Fatal("403 应返回错误")
	}
	status, detail := httpStatusFromError(err)
	if status != http.StatusForbidden {
		t.Fatalf("状态码 = %d，期望 403", status)
	}
	if detail == nil || detail.Message != "鉴权失败，令牌无效" {
		t.Fatalf("错误原文未透传：%+v", detail)
	}
	if exitCodeForHTTP(status) != 3 {
		t.Fatalf("403 退出码应为 3")
	}
	if strings.Contains(err.Error(), "cli-tok") {
		t.Fatal("错误文案泄漏令牌")
	}
}

// TestMultiWordCommandDispatch 验证多词命令路径解析、参数注入与信封输出。
func TestMultiWordCommandDispatch(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		if r.Header.Get("Authorization") != "Bearer cli-tok" {
			t.Error("未使用 Bearer 令牌")
		}
		w.Write([]byte(`{"ok":true,"items":[]}`))
	}))
	defer srv.Close()

	t.Setenv("VERIFY_TOKEN", "")
	var buf bytes.Buffer
	code := run([]string{"--url", srv.URL, "--token", "cli-tok", "trash", "list", "--limit", "2"}, &buf)
	if code != 0 {
		t.Fatalf("退出码=%d，期望 0；输出=%q", code, buf.String())
	}
	if gotPath != "/api/trash/list" {
		t.Fatalf("路径 = %s，期望 /api/trash/list", gotPath)
	}
	if !strings.Contains(gotQuery, "limit=2") {
		t.Fatalf("查询串 = %q，期望含 limit=2", gotQuery)
	}
	for _, key := range []string{`"ok":true`, `"command":"trash list"`, `"data":`} {
		if !strings.Contains(buf.String(), key) {
			t.Errorf("信封缺 %s：%s", key, buf.String())
		}
	}
	if strings.Contains(buf.String(), "cli-tok") {
		t.Fatal("信封泄漏令牌")
	}
}

// TestBinaryReadCommandRawByDefault 验证 raw/thumb 返回二进制时默认即原样输出，
// 不塞进 JSON 信封导致退出码 1（--pretty 也不得改写字节）。
func TestBinaryReadCommandRawByDefault(t *testing.T) {
	binary := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}
	for _, name := range []string{"raw", "thumb"} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Write(binary)
			}))
			defer srv.Close()

			for _, prefix := range [][]string{{"--url", srv.URL}, {"--url", srv.URL, "--pretty"}} {
				var buf bytes.Buffer
				code := run(append(prefix, name, "--path", "/vol1/照片.jpg"), &buf)
				if code != 0 {
					t.Fatalf("%s 退出码=%d，期望 0；输出=%q", name, code, buf.String())
				}
				if !bytes.Equal(buf.Bytes(), binary) {
					t.Fatalf("%s 输出被改写：得到 %v，期望 %v", name, buf.Bytes(), binary)
				}
			}
		})
	}
}

// TestPrettyEnvelopeIndented 验证 --pretty 在信封层仍保留缩进，不被 Encoder 压回单行。
func TestPrettyEnvelopeIndented(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"items":[{"key":"a"}]}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	code := run([]string{"--url", srv.URL, "status", "--pretty"}, &buf)
	if code != 0 {
		t.Fatalf("退出码=%d；输出=%q", code, buf.String())
	}
	if !strings.Contains(buf.String(), "\n  \"data\"") {
		t.Fatalf("--pretty 未缩进信封：%s", buf.String())
	}
	if !strings.Contains(buf.String(), `"key": "a"`) {
		t.Fatalf("--pretty 丢失字段：%s", buf.String())
	}
}

// TestAuthTokenSendsMarker 验证 auth token 走通用分发时带 X-Requested-With 头。
func TestAuthTokenSendsMarker(t *testing.T) {
	var marker string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marker = r.Header.Get("X-Requested-With")
		w.Write([]byte(`{"token":"srv-tok"}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	code := run([]string{"--url", srv.URL, "auth", "token"}, &buf)
	if code != 0 {
		t.Fatalf("auth token 退出码=%d；输出=%q", code, buf.String())
	}
	if marker != "verify" {
		t.Fatalf("X-Requested-With = %q，期望 verify", marker)
	}
}

// TestReadRequiredParams 验证必填参数缺失时按用法错误退出，不发请求。
func TestReadRequiredParams(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	var buf bytes.Buffer
	code := run([]string{"--url", srv.URL, "import", "groups"}, &buf)
	if code != 2 {
		t.Fatalf("缺必填参数退出码=%d，期望 2；输出=%q", code, buf.String())
	}
	if called {
		t.Fatal("缺必填参数不应发请求")
	}
	if !strings.Contains(buf.String(), "session_id") {
		t.Fatalf("错误提示应点名缺失参数：%s", buf.String())
	}
}

// TestReadCommandDetailHelpJSON 验证 <cmd> --help-json 输出单命令自描述。
func TestReadCommandDetailHelpJSON(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{"status", "--help-json"}, &buf)
	if code != 0 {
		t.Fatalf("退出码=%d；输出=%q", code, buf.String())
	}
	var info commandInfo
	if err := json.Unmarshal(buf.Bytes(), &info); err != nil {
		t.Fatalf("单命令帮助不是 JSON：%v；输出=%q", err, buf.String())
	}
	if info.Name != "status" || info.Path != "/api/status" {
		t.Fatalf("帮助内容错误：%+v", info)
	}
}
