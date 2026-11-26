# verifyctl — Verify 完整命令行客户端设计（2026-11-26）

## 背景与目标

Verify 目前只有内嵌 Web UI，没有命令行入口。需求是让 AI（以及熟练用户）通过命令行**完整操控**本软件。

**范围（用户 2026-11-26 明确）**：不留残缺版。**GUI 能做的 CLI 都要做**——`internal/web/server.go` 注册的 130 条路由全部纳入最终能力，包括所有写操作、危险操作、导入导出、审计、同步。分波次只是实施顺序，不是能力裁剪；每一波都交付可用能力，最终全覆盖。

**核心判断**：给 AI 用和给人用是两回事。AI 需要自描述（自己发现命令/参数/副作用）、结构化输出（JSON 进 JSON 出）、可判定错误（语义退出码）、以及不会误触危险操作的默认行为（dry-run + 显式确认）。

**部署形态硬约束**（项目决策）：主程序单二进制 + `go:embed` 内嵌前端，不改。CLI 另起独立二进制 `verifyctl`，只走 HTTP API、不碰数据库，主程序零改动。

## 能力矩阵（全部路由，按功能域分组）

最终必须覆盖以下全部功能域。具体路由以 `internal/web/server.go` 当前注册为准（130 条），实施时逐条对照 `tools/map_routes.py` 的路由→响应类型映射。

| 功能域 | 代表路由 | 副作用 | 实施波次 |
|---|---|---|---|
| 状态与鉴权 | status / metrics / metric / auth/token / login / logout / events(SSE) | 无~会话 | 1 |
| 扫描与任务 | scan / scan/stop / tasks / tasks/pause / tasks/resume | trigger | 1 |
| 分组查询 | groups（各 mode）/ folder/stats / big / tag / retain/pref+score+explain | 无 | 1 |
| 搜索与快照 | search* / rule-search* / op-snapshots* | 无~trigger | 1、3 |
| 健康与索引 | health/status+results / health/scan+cancel / health/rename+fix-encoding+trash+undo+prune+ignore / index/repair / emptydir/clean | trigger~destructive | 2、4 |
| 隔离区 | trash/list+preview+export / trash/restore+delete+empty | 无~destructive | 2、4 |
| 文件整理（写） | relink* / move* / groups/rename* / file/rename / rename* / folder_rename* / trim* / transcode* / video_tags* / video/cropdetect / intro/video reanalyze | 写~destructive | 3、4 |
| 清理计划 | cleanup/plans+delete+run | 写 | 3 |
| 导入导出报告 | export / import* / report / oplog/export | 写文件 | 3 |
| 文件系统浏览 | fs/list+roots+mkdir / raw / thumb / diag/authz | 无~写 | 1、3 |
| 设置与缓存 | settings（读/写）/ cache/stats+clear | 无~写 | 1、3 |
| 审计 | oplog / oplog/clear | 无~destructive | 2、4 |
| 同步 | sync/export+import+peers | 写 | 3 |

**波次定义**（全部都要做，顺序只管先后）：
- **波次 1 · 基础与只读**：骨架、HTTP/鉴权/信封/退出码、全部无副作用查询命令、`--help-json` 自描述。
- **波次 2 · 低风险动作**：scan、任务暂停/恢复、health scan、隔离区只读、审计读。
- **波次 3 · 写操作**：改名/移动/转码/裁剪/清理计划/导入导出/设置/文件系统写/同步。dry-run + `--yes` 确认协议在此波成熟。
- **波次 4 · 危险操作**：永久删除、清空隔离区、批量改名执行、健康修复/删除类、审计清空。必须 `--yes` + `--confirm-token`（从 dry-run 回包取）。
- 每波结束跑集成冒烟；最终交付时对照 130 条路由做覆盖率核对，缺一条即不算完成。

## 设计要点

### 1. 形态与产物
- 源码 `cmd/verifyctl/`（Go 标准布局），不引入第三方 CLI 库（一期 `flag` 够用；多层级子命令若 `flag` 不够再举证引入）。
- 产物：`dist/verifyctl-<版本>`（本地）+ 打进 fpk 的 `packaging/fpk/app/bin/verifyctl`。
- 版本与主版本同源（构建期 ldflags 注入），不新增第六处版本号断言。

### 2. 连接与鉴权
- 默认 `http://127.0.0.1:8898`；`--url` 覆盖。
- 令牌优先级：`--token` > `VERIFY_TOKEN` > `--data-dir` 下 `config.json` 的 `access_token`。不读 `access-token.txt`（给人看的明文提示文件）。
- 统一 `Authorization: Bearer <token>`；不用 `?token=`（仅 SSE 特例接受）。
- `auth token` 子命令走 `/api/auth/token`，需 `X-Requested-With: verify` 头。令牌不写日志、不进输出。

### 3. 输出协议（AI 友好核心）
- 统一信封：`{"ok":true,"command":"...","data":<服务端原始响应>}`；`--raw` 跳过信封只出原始响应；`--format table` 显式开启人类表格。
- 信封理由：服务端 `okResponse` 自带 `ok` 键，数组响应也无处理顶层键——包 `data` 是唯一同时满足「可判定 + 不篡改服务端字段」的形状。
- 错误信封：`{"ok":false,"command":"...","error":{"code":"...","message":"中文","hint":"..."}}`，stderr 附人类摘要。
- 退出码：0 成功 / 2 用法 / 3 鉴权 / 4 连接 / 5 服务端 4xx（含所有 409）/ 6 服务端 5xx / 7 预留结构化 taskguard 冲突（一期不产生）。
- 409 重载（扫描中、search 未就绪等），占用者被压成中文串，一期统一退出码 5 + 原文透传；不解析中文串。

### 4. 命令自描述
- `verifyctl --help-json` / `<cmd> --help-json`：输出命令清单，含 `name`、`description`、`params`（名/类型/必填/默认）、`side_effect`（none|trigger|write|destructive）、`confirm_required`。
- 清单由代码内一张命令表生成，表即分发入口，避免与实现漂移。
- 覆盖率门禁：单测断言命令表覆盖 `server.go` 全部路由（允许少量显式豁免，如纯静态资源 `/`、`/ui/`），缺路由即失败——这是「不留残缺版」的机器保障。

### 5. dry-run 与确认（全量必做）
- 所有写操作默认 dry-run：返回 `{"dry_run":true,"would_do":...}`，不落盘。
- 真实执行需 `--yes`；destructive 额外要求 `--confirm-token`（dry-run 回包给出）。
- AI 可先 dry-run 观察、再带确认执行，避免盲操作。

### 6. 审查发现的坑（规避清单）
- `/api/trash` 写库（HealTrashRows），只读用 `/api/trash/list`。
- 同名分组是 `?mode=name`，无 `/api/groups/name`。
- `?token=` 仅 `/api/events` 接受，统一 Bearer。
- 部分 handler 响应未收口到具名 struct（见「响应契约层收口」倡议），解析对缺字段宽容。
- 长任务 taskguard 独占，409 语义重载（见 §3）。

## 测试策略
- 命令表 ↔ 路由覆盖率断言（防残缺）。
- `--help-json` 与命令表一致性。
- httptest 模拟服务端：成功/鉴权失败/4xx/409/5xx → 信封与退出码。
- dry-run 不落盘、确认令牌校验、令牌来源优先级不泄漏。
- 每波集成冒烟：起真实 Verify 临时实例跑对应命令。

## 落地顺序
按波次 1→4 实施，每波内按功能域推进；全部完成后对照 130 条路由核对覆盖率。writing-plans 将把每波拆成具体任务。

## 已收敛的决定
- 输出信封 `{ok,command,data}`；409 统一退出码 5 + 中文透传；清单以功能矩阵为准。
- 版本同源、fpk 带 verifyctl、全量覆盖（含危险操作）。
- 文档落点：按规范放 `docs/superpowers/specs/`（用户 2026-11-26 确认），不改项目「docs 只留速查」约定——设计/计划类流程文档是本规范下的例外。

## 完成状态（2026-11-26，Task 10 收口）
- 全量接线：命令表 132 条 `Command`，`Run` 全部非空（`TestAllCommandsImplemented` 断言，防截断）。
- 路由覆盖：`server.go` 130 条 `mux.HandleFunc`，减去静态豁免 `/`、`/ui/`、`/favicon.ico` 后 **127 条非静态路径全部覆盖**；命令表去重路径 127 个与之双向相等，132 条命令 = 127 路径 + 5 条同路径多方法（`TestCoverageCount`）。
- fpk 打包：`scripts/build-fpk.sh` 在主二进制之后以相同平台/ldflags 构建 `verifyctl` 到 `packaging/fpk/app/bin/verifyctl`；实测 `dist/verify-4.2.2.fpk` 内含 `bin/verify` 与 `bin/verifyctl` 两个可执行文件。
- 本地构建：`bash scripts/build-cli.sh` 产出 `dist/verifyctl-4.2.2` 及 `.sha256`。
- 测试：`go test ./cmd/verifyctl/... -count=1` 全绿；`scripts/quick-check.sh` 8 项全过（gofmt/vet/前端语法/前端类型/类型契约/单测/文档断言/公开把关）。
- 冒烟：`verifyctl version`、`--help-json`、`trash empty --dry-run`（只回 would_do + confirm_token，无网络）；临时实例只读冒烟 `status`/`trash list`/`tasks` 通过，无令牌 `trash list` 退出码 3。
