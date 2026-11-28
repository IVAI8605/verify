# verifyctl 完整 CLI 客户端 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建独立 Go 二进制 `verifyctl`，通过 HTTP API 完整操控 Verify，覆盖 `internal/web/server.go` 全部路由（GUI 能做的 CLI 都要做），对 AI 提供自描述、JSON 信封、语义退出码与 dry-run 确认协议。

**Architecture:** 独立命令 `cmd/verifyctl/`，不 import `internal/*` 业务包，只发 HTTP 请求到运行中的 Verify 实例。代码内一张命令表既是分发入口也是 `--help-json` 数据源；单测断言命令表覆盖 server.go 全部路由，作为「不留残缺版」的机器保障。写操作统一 dry-run + `--yes`（destructive 额外 `--confirm-token`）。

**Tech Stack:** Go 1.26、标准库 `flag`/`net/http`/`encoding/json`（一期不引第三方 CLI 库）、`CGO_ENABLED=0` 交叉编译 linux/amd64、httptest 做服务端模拟。

**Spec:** `docs/superpowers/specs/2026-11-26-verifyctl-design.md`

## Global Constraints
- 不改主程序 `main.go`/`internal/**` 的部署形态；CLI 只走 HTTP API，不碰数据库。
- 版本与主版本同源，构建期 ldflags 注入，不新增第六处版本号断言。
- 令牌不写日志、不进输出；统一 `Authorization: Bearer`，不用 `?token=`（仅 SSE 特例接受）。
- 输出信封 `{"ok":true,"command":"...","data":<原始响应>}`；`--raw` 跳过信封；`--format table` 显式开启。
- 退出码：0 成功 / 2 用法 / 3 鉴权 / 4 连接 / 5 服务端 4xx（含所有 409）/ 6 服务端 5xx / 7 预留。
- 409 语义重载，一期统一退出码 5 + 中文原文透传，不解析中文串判冲突。
- 所有写操作默认 dry-run，返回 `would_do`；`--yes` 才真实执行；destructive 额外 `--confirm-token`。
- 只读隔离区用 `/api/trash/list`（`/api/trash` 会 HealTrashRows 写库）；同名分组是 `?mode=name`。
- 部分 handler 响应未收口具名 struct，JSON 解析须对缺字段宽容。
- 产物：`dist/verifyctl-<版本>` + 打进 fpk 的 `packaging/fpk/app/bin/verifyctl`。
- CLI 源码与测试放 `cmd/verifyctl/`，注释/日志/错误一律中文。

## Review Focus
- **命令表漏路由**（GUI 能做 CLI 不能做）：Task 2 覆盖率断言，缺一条路由即 FAIL。
- **dry-run 误落盘**（AI 盲确认导致真删文件）：Task 6、Task 10 断言 dry-run 不发请求/不写库、无 `--yes` 不执行。
- **令牌泄漏到输出/日志**（安全红线）：Task 3 断言错误回包与 stderr 不含 token。
- **响应缺字段导致 CLI 崩溃**（响应契约未收口）：Task 4 断言 `data` 原样透传、不解码成强类型 struct。
- **409 被误判为任务冲突**（语义重载）：Task 3 断言 409 → 退出码 5 且中文错误原文透传、不产生退出码 7。

---

### Task 1: 骨架、Go 模块落点与构建入口

**Files:**
- Create: `cmd/verifyctl/main.go`
- Create: `scripts/build-cli.sh`
- Test: `cmd/verifyctl/main_test.go`

**Interfaces:**
- Produces: `main()` 入口；`verifyctl version` 输出构建期注入的版本（默认 `dev`）。

- [ ] **Step 1: 写失败测试 —— version 子命令有输出且非明文令牌**

```go
func TestVersionSubcommand(t *testing.T) {
    out := runCLI(t, "version")
    if strings.TrimSpace(out) == "" { t.Fatal("version 不应为空") }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/verifyctl -run TestVersionSubcommand -v`
Expected: FAIL（包不存在/函数未定义）

- [ ] **Step 3: 实现 main.go 骨架**

`main()` 用 `flag.NewFlagSet` 解析全局参数（`--url`、`--token`、`--data-dir`、`--format`、`--raw`、`--help-json`）；`version` 子命令打印 `version` 变量（`-X` ldflags 注入）。`runCLI(t, args...) string` 测试辅助函数捕获 stdout。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./cmd/verifyctl -run TestVersionSubcommand -v`
Expected: PASS

- [ ] **Step 5: 写 build-cli.sh**

`scripts/build-cli.sh`：`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=$VERSION" -o dist/verifyctl-$VERSION ./cmd/verifyctl`，VERSION 从 manifest 读；产物 sha256 落 `dist/`。（版本注入路径 2026-11-26 实测裁定：main 包链接符号是 `main.version`，`-X verify/cmd/verifyctl.version` 不生效。）

- [ ] **Step 6: 提交**

```bash
git add cmd/verifyctl scripts/build-cli.sh
git commit -m "feat(verifyctl): add CLI skeleton and build script"
```

---

### Task 2: 命令表 + 路由覆盖率断言（防残缺核心）

**Files:**
- Create: `cmd/verifyctl/registry.go`
- Create: `cmd/verifyctl/registry_test.go`
- Modify: `cmd/verifyctl/main.go`（接入命令表分发）

**Interfaces:**
- Produces: `type Command struct { Name string; Path string; Method string; Params []Param; SideEffect string; Confirm bool; Run func(ctx context.Context, c *Client, args map[string]string) (any, error) }`；`var Commands []Command`；`type Param struct { Name, Type string; Required bool; Default string }`；`func findCommand(name string) (Command, bool)`。

- [ ] **Step 1: 写失败测试 —— 命令表覆盖 server.go 全部路由**

```go
func TestCommandsCoverAllRoutes(t *testing.T) {
    routes := parseServerRoutes(t, "../internal/web/server.go") // 正则抽 mux.HandleFunc("...")
    covered := map[string]bool{}
    for _, cmd := range Commands { covered[cmd.Path] = true }
    for r := range routes {
        if strings.HasPrefix(r, "/ui/") || r == "/" || r == "/favicon.ico" { continue }
        if !covered[r] { t.Errorf("命令表未覆盖路由 %s", r) }
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/verifyctl -run TestCommandsCoverAllRoutes -v`
Expected: FAIL（Commands 为空/未定义）

- [ ] **Step 3: 实现 registry.go 命令表骨架**

按 Spec 能力矩阵，为 130 条路由逐条建 `Command{Name, Path, Method, Params, SideEffect, Confirm}`。`SideEffect` 取 `none|trigger|write|destructive`（值来自 Spec §能力矩阵与附录 A.3）。`Run` 统一走 Task 3 的 `Client.Do`。此步只建结构与只读/trigger 类条目；写/destructive 条目的 `Run` 实现留 Task 6/8/10，但条目本身必须在此步全部登记（覆盖率断言才过）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./cmd/verifyctl -run TestCommandsCoverAllRoutes -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add cmd/verifyctl/registry.go cmd/verifyctl/registry_test.go cmd/verifyctl/main.go
git commit -m "feat(verifyctl): command registry with full route coverage assertion"
```

---

### Task 3: HTTP 客户端、鉴权、信封、语义退出码

**Files:**
- Create: `cmd/verifyctl/client.go`
- Create: `cmd/verifyctl/client_test.go`
- Create: `cmd/verifyctl/output.go`

**Interfaces:**
- Produces: `type Client struct { BaseURL, Token string; HTTP *http.Client }`；`func NewClient(baseURL, token string) *Client`；`func (c *Client) Do(ctx context.Context, cmd Command, params map[string]string) ([]byte, int, error)`；`type Envelope struct { OK bool `json:"ok"`; Command string `json:"command"`; Data json.RawMessage `json:"data,omitempty"`; Error *ErrDetail `json:"error,omitempty"` }`；`type ErrDetail struct { Code, Message, Hint string }`；`func writeEnvelope(w io.Writer, command string, raw []byte, rawMode bool) error`；`func writeError(w io.Writer, command string, e *ErrDetail) error`；`func exitCodeForHTTP(status int) int`。

- [ ] **Step 1: 写失败测试 —— 鉴权头、信封、409、令牌不泄漏**

```go
func TestDoBearerAndEnvelope(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Authorization") != "Bearer test-token" { t.Error("缺 Bearer") }
        if r.URL.Query().Has("token") { t.Error("不应使用 ?token=") }
        w.Write([]byte(`{"ok":true,"items":[]}`))
    }))
    defer srv.Close()
    c := NewClient(srv.URL, "test-token")
    body, status, err := c.Do(context.Background(), Command{Path:"/api/status", Method:"GET"}, nil)
    // status==200, body 原样
}

func Test409MapsToExit5(t *testing.T) {
    if got := exitCodeForHTTP(409); got != 5 { t.Fatalf("409→%d, want 5", got) }
}

func TestErrorNoTokenLeak(t *testing.T) {
    var buf bytes.Buffer
    writeError(&buf, "status", &ErrDetail{Code:"auth", Message:"鉴权失败"})
    if strings.Contains(buf.String(), "test-token") { t.Fatal("错误输出含令牌") }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/verifyctl -run 'TestDoBearerAndEnvelope|Test409MapsToExit5|TestErrorNoTokenLeak' -v`
Expected: FAIL（类型/函数未定义）

- [ ] **Step 3: 实现 client.go**

`Do` 按 `cmd.Method` 组请求；GET 把 `params` 编码进 query（注意同名分组 `mode=name`、`page_size` 上限 1000），POST 编码成 JSON body；设 `Authorization: Bearer <token>`；返回原始 body 与 status。`exitCodeForHTTP`：200 段→0，401/403→3，400–499→5，500+→6；连接错误由调用方映射 4。

- [ ] **Step 4: 实现 output.go**

`writeEnvelope` 输出 `{"ok":true,"command":...,"data":<raw>}`，`rawMode` 时直接写 raw；`writeError` 输出错误信封，message 取服务端 `error` 字段中文原文（409 不解析、不映射退出码 7）。

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./cmd/verifyctl -run 'TestDoBearerAndEnvelope|Test409MapsToExit5|TestErrorNoTokenLeak' -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add cmd/verifyctl/client.go cmd/verifyctl/client_test.go cmd/verifyctl/output.go
git commit -m "feat(verifyctl): HTTP client, Bearer auth, JSON envelope and exit codes"
```

---

### Task 4: 令牌来源优先级与鉴权子命令

**Files:**
- Create: `cmd/verifyctl/auth.go`
- Create: `cmd/verifyctl/auth_test.go`

**Interfaces:**
- Produces: `func resolveToken(flagToken string, dataDir string) (string, error)`（优先 `--token` > `VERIFY_TOKEN` > `<dataDir>/config.json` 的 `access_token`）；`func (c *Client) AuthToken(ctx context.Context) (string, error)`（GET `/api/auth/token`，带 `X-Requested-With: verify`）。

- [ ] **Step 1: 写失败测试 —— 优先级与不读提示文件**

```go
func TestResolveTokenPriority(t *testing.T) {
    t.Setenv("VERIFY_TOKEN", "env-tok")
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir,"config.json"), []byte(`{"access_token":"file-tok"}`), 0600)
    if got,_ := resolveToken("", dir); got != "env-tok" { t.Fatalf("got %s", got) }
    if got,_ := resolveToken("flag-tok", dir); got != "flag-tok" { t.Fatalf("got %s", got) }
}
func TestDoesNotReadTokenHint(t *testing.T) {
    // 写 access-token.txt 但 config.json 无 access_token → resolveToken 返回空，不读提示文件
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/verifyctl -run 'TestResolveToken|TestDoesNotReadTokenHint' -v`
Expected: FAIL

- [ ] **Step 3: 实现 auth.go**

按优先级读取；`config.json` 用 `encoding/json` 解到 `struct{ AccessToken string `json:"access_token"` }`，缺文件/缺字段返回空串不报错。`AuthToken` 带 `X-Requested-With: verify` 头。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./cmd/verifyctl -run 'TestResolveToken|TestDoesNotReadTokenHint' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add cmd/verifyctl/auth.go cmd/verifyctl/auth_test.go
git commit -m "feat(verifyctl): token resolution and auth token subcommand"
```

---

### Task 5: 自描述 `--help-json` + 覆盖率一致性

**Files:**
- Modify: `cmd/verifyctl/output.go`
- Create: `cmd/verifyctl/help.go`
- Test: `cmd/verifyctl/help_test.go`

**Interfaces:**
- Consumes: `Commands`、`Command`、`Param`（Task 2）。
- Produces: `func writeCommandList(w io.Writer) error`（输出命令清单 JSON，每条含 name/description/params/side_effect/confirm_required）；`func writeCommandDetail(w io.Writer, cmd Command) error`。

- [ ] **Step 1: 写失败测试 —— help-json 与命令表一致**

```go
func TestHelpJSONMatchesRegistry(t *testing.T) {
    var buf bytes.Buffer
    writeCommandList(&buf)
    var list []Command
    json.Unmarshal(buf.Bytes(), &list)
    if len(list) != len(Commands) { t.Fatalf("help-json %d 条 != 命令表 %d 条", len(list), len(Commands)) }
    for _, c := range list {
        if c.SideEffect == "" || c.Path == "" { t.Errorf("%s 缺 side_effect/path", c.Name) }
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/verifyctl -run TestHelpJSONMatchesRegistry -v`
Expected: FAIL

- [ ] **Step 3: 实现 help.go**

`writeCommandList` 序列化 `Commands`（`Run` 字段用自定义 Marshal 或单独 DTO 排除，不输出函数指针）；`writeCommandDetail` 输出单命令。`main()` 在 `--help-json` 时调用并退出 0。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./cmd/verifyctl -run TestHelpJSONMatchesRegistry -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add cmd/verifyctl/help.go cmd/verifyctl/help_test.go cmd/verifyctl/output.go
git commit -m "feat(verifyctl): machine-readable self-describing help"
```

---

### Task 6: 只读命令接入（全量 none 类）

**Files:**
- Create: `cmd/verifyctl/commands_read.go`
- Test: `cmd/verifyctl/commands_read_test.go`

**Interfaces:**
- Consumes: `Commands`（Task 2，SideEffect=none 的条目）、`Client.Do`（Task 3）、`resolveToken`（Task 4）。
- Produces: `func runReadCommand(ctx context.Context, c *Client, cmd Command, params map[string]string) ([]byte, error)`；补完所有 `none` 类 `Run` 实现。

- [ ] **Step 1: 写失败测试 —— 只读命令不写库、原样透传**

```go
func TestTrashListNotTrash(t *testing.T) {
    // 断言 trash list 命令 Path == /api/trash/list，不是 /api/trash
}
func TestGroupsModeName(t *testing.T) {
    // 断言同名分组命令支持 mode=name 参数，path 为 /api/groups
}
func TestReadPassthrough(t *testing.T) {
    // httptest 返回带未知字段的 JSON，验证 runReadCommand 输出含该字段（不解码丢字段）
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/verifyctl -run 'TestTrashListNotTrash|TestGroupsModeName|TestReadPassthrough' -v`
Expected: FAIL

- [ ] **Step 3: 实现 commands_read.go**

为所有 `none` 类命令补 `Run`：调 `c.Do(ctx, cmd, params)` 返回原始 body。`--pretty` 仅做 `json.Indent`，不改字段。覆盖 Spec 能力矩阵中「状态与鉴权/分组查询/搜索与快照/健康只读/隔离区只读/审计读/缓存读/设置读/文件系统读」。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./cmd/verifyctl -run 'TestTrashListNotTrash|TestGroupsModeName|TestReadPassthrough' -v`
Expected: PASS

- [ ] **Step 5: 集成冒烟（波次 1）**

起 Verify 临时实例 → `./verifyctl --url http://127.0.0.1:<port> status` → 退出码 0、stdout 为含 `ok/command/data` 的 JSON。

- [ ] **Step 6: 提交**

```bash
git add cmd/verifyctl/commands_read.go cmd/verifyctl/commands_read_test.go
git commit -m "feat(verifyctl): implement all read-only commands"
```

---

### Task 7: 低风险动作（trigger 类）

**Files:**
- Create: `cmd/verifyctl/commands_trigger.go`
- Test: `cmd/verifyctl/commands_trigger_test.go`

**Interfaces:**
- Consumes: `Commands`（SideEffect=trigger）、`Client.Do`。
- Produces: `func runTriggerCommand(ctx context.Context, c *Client, cmd Command, params map[string]string) ([]byte, error)`（dry-run 返回 `would_do` 不发请求；`--yes` 才调用）。

- [ ] **Step 1: 写失败测试 —— dry-run 不发请求**

```go
func TestTriggerDryRunNoRequest(t *testing.T) {
    called := false
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request){ called = true }))
    defer srv.Close()
    c := NewClient(srv.URL, "t")
    out, _ := runTriggerCommand(context.Background(), c, Command{Path:"/api/scan",SideEffect:"trigger"}, map[string]string{"dry_run":"true"})
    if called { t.Fatal("dry-run 不应发请求") }
    if !strings.Contains(string(out), "would_do") { t.Fatal("dry-run 回包缺 would_do") }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/verifyctl -run TestTriggerDryRunNoRequest -v`
Expected: FAIL

- [ ] **Step 3: 实现 commands_trigger.go**

覆盖 scan / scan stop / tasks pause+resume / health scan+cancel / index repair / cache clear 等 trigger 类。dry-run 输出 `{"dry_run":true,"would_do":{"command":...,"method":...,"path":...,"params":...}}`；`--yes` 时调 `c.Do`。触发后返回任务号，不轮询。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./cmd/verifyctl -run TestTriggerDryRunNoRequest -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add cmd/verifyctl/commands_trigger.go cmd/verifyctl/commands_trigger_test.go
git commit -m "feat(verifyctl): trigger commands with dry-run default"
```

---

### Task 8: 写操作（write 类，全量）

**Files:**
- Create: `cmd/verifyctl/commands_write.go`
- Test: `cmd/verifyctl/commands_write_test.go`

**Interfaces:**
- Consumes: `Commands`（SideEffect=write）、`runTriggerCommand` 的 dry-run 机制（Task 7）。
- Produces: `func runWriteCommand(ctx context.Context, c *Client, cmd Command, params map[string]string) ([]byte, error)`。

- [ ] **Step 1: 写失败测试 —— 无 --yes 不执行写操作**

```go
func TestWriteRequiresYes(t *testing.T) {
    called := false
    srv := httptest.NewServer(... called = true ...)
    defer srv.Close()
    c := NewClient(srv.URL, "t")
    runWriteCommand(context.Background(), c, Command{Path:"/api/settings",SideEffect:"write"}, map[string]string{})
    if called { t.Fatal("无 --yes 不应执行写操作") }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/verifyctl -run TestWriteRequiresYes -v`
Expected: FAIL

- [ ] **Step 3: 实现 commands_write.go**

覆盖 Spec 能力矩阵全部 `write` 类：改名/移动/转码/裁剪/清理计划/导入导出/设置写/文件系统写/同步/健康忽略+undo 等。参数按 AGENTS.md「接口契约」表的精确类型构造（`prefix_a` bool、`max_res` string、`match_mode` 枚举等），避免 400。dry-run + `--yes` 复用 Task 7 机制。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./cmd/verifyctl -run TestWriteRequiresYes -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add cmd/verifyctl/commands_write.go cmd/verifyctl/commands_write_test.go
git commit -m "feat(verifyctl): all write commands with confirmation gate"
```

---

### Task 9: 危险操作确认协议（destructive 类）

**Files:**
- Create: `cmd/verifyctl/confirm.go`
- Create: `cmd/verifyctl/commands_danger.go`
- Test: `cmd/verifyctl/confirm_test.go`
- Test: `cmd/verifyctl/commands_danger_test.go`

**Interfaces:**
- Produces: `func makeConfirmToken(cmd Command, params map[string]string) string`（基于命令+参数+时间戳的短 token，dry-run 回包给出）；`func verifyConfirmToken(got, want string) bool`；`func runDangerCommand(ctx context.Context, c *Client, cmd Command, params map[string]string, confirmToken string) ([]byte, error)`。

- [ ] **Step 1: 写失败测试 —— 缺 confirm-token 不执行、错 token 不执行**

```go
func TestDangerRequiresConfirmToken(t *testing.T) {
    called := false
    srv := httptest.NewServer(... called = true ...)
    defer srv.Close()
    c := NewClient(srv.URL, "t")
    cmd := Command{Path:"/api/trash/empty",SideEffect:"destructive"}
    // dry-run → 取 confirm token
    dry, _ := runDangerCommand(ctx, c, cmd, nil, "")
    // 缺 token 执行 → called == false
    // 错 token 执行 → called == false
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/verifyctl -run 'TestDangerRequiresConfirmToken' -v`
Expected: FAIL

- [ ] **Step 3: 实现 confirm.go + commands_danger.go**

`makeConfirmToken` 生成短时效 token（含命令名+参数哈希+时间戳），dry-run 回包带 `confirm_token`。`runDangerCommand` 校验 `--yes` + `--confirm-token` 双门槛，缺一即返回用法错误（退出码 2），不发请求。覆盖：trash/delete+empty、rename execute/undo 执行、health 修复/删除类、oplog clear 等全部 destructive 路由。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./cmd/verifyctl -run 'TestDangerRequiresConfirmToken' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add cmd/verifyctl/confirm.go cmd/verifyctl/commands_danger.go cmd/verifyctl/*_test.go
git commit -m "feat(verifyctl): destructive commands with confirm-token gate"
```

---

### Task 10: 全量覆盖率收口 + fpk 打包 + 最终冒烟

**Files:**
- Modify: `scripts/build-fpk.sh`
- Create: `cmd/verifyctl/coverage_test.go`
- Modify: `docs/superpowers/specs/2026-11-26-verifyctl-design.md`（记录完成状态）

**Interfaces:**
- Consumes: 全部 Task 的 `Commands` 与 `Run` 实现。
- Produces: 全量 CLI + fpk 内含 `app/bin/verifyctl`。

- [ ] **Step 1: 写失败测试 —— 所有 Command.Run 非空**

```go
func TestAllCommandsImplemented(t *testing.T) {
    for _, c := range Commands {
        if c.Run == nil { t.Errorf("命令 %s (%s) 未实现", c.Name, c.Path) }
    }
}
func TestCoverageCount(t *testing.T) {
    // 命令表条数 == server.go 路由数 - 静态资源豁免数
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/verifyctl -run 'TestAllCommandsImplemented|TestCoverageCount' -v`
Expected: FAIL（若有遗漏条目）

- [ ] **Step 3: 补齐任何遗漏命令**

对照 `TestCommandsCoverAllRoutes` 与 `TestAllCommandsImplemented` 输出，补完所有缺 `Run` 的条目，直到两条测试全绿。

- [ ] **Step 4: 改 build-fpk.sh 带 verifyctl**

在 `go build` 主二进制后加一步 `go build` verifyctl 到 `packaging/fpk/app/bin/verifyctl`（同 ldflags/目标平台），确保 fpk 内两个 bin 都在。

- [ ] **Step 5: 全量单测 + 门禁**

Run: `go test ./cmd/verifyctl/... -v && bash scripts/quick-check.sh`
Expected: PASS（quick-check 的主程序断言不受 CLI 影响）

- [ ] **Step 6: 真机冒烟（只读 + 一次 dry-run 写）**

用临时实例跑 `verifyctl status`、`verifyctl trash list`、`verifyctl scan --dry-run`，核对信封/退出码/dry-run 不落盘。

- [ ] **Step 7: 提交**

```bash
git add scripts/build-fpk.sh cmd/verifyctl/coverage_test.go docs/superpowers/specs/2026-11-26-verifyctl-design.md
git commit -m "feat(verifyctl): full route coverage, fpk packaging and final smoke"
```
