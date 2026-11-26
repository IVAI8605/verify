# AGENTS.md — Verify查重助手

本文件是仓库内唯一的协作约定来源，自包含、不外链仓库外文档。硬约束（日志与调试前置、永不永久删除、备份先行、发版顺序、产物不入库）分散在下方「构建与测试」「版本与发布」「安全与权限」三节。

**下面的约束是上位约束**：不得违背。确需偏离的，必须先开 issue 或 PR 说明理由并取得维护者同意，不要自行豁免。

---

## 该读哪节

遇到下列动作，先读对应章节再动手：

- 新建或移动任何文件、目录之前 → 按 README「目录结构」定落点；构建产物与本地数据一律不进仓库
- 给文件或目录起名之前 → 沿用现有风格：目录小写、Go 文件用下划线分词、前端文件按职责命名
- 生成产物、要备份、要归档时 → 产物留在 `dist/`（已被 `.gitignore` 忽略），历史版本不随仓库分发
- 碰到密钥、口令、夹带凭据的普通文件时 → 不得写入仓库、日志、测试用例或文档示例
- 准备改动任何文件之前 → 先读「完成前自检」
- 要判断谁在什么时点审核、要不要留痕时 → 见「版本与发布」与「发版门禁」

---

## 项目补充

> 本节只补本项目特有的事实与约束；与通用规范冲突的不在此处自行豁免，确需偏离按文件头口径先问用户并登记。

本文件给在此仓库工作的 AI Agent 使用，也适合人类贡献者快速对齐约定。

### 项目是什么

飞牛 NAS（fnOS）上的重复文件清理工具：单个 Go 二进制 + 内嵌中文 Web UI + 本地 SQLite，通过 `.fpk` 分发给飞牛应用中心。

- 精确查重：大小 + 头尾各 4KB 预哈希 → BLAKE3-256 全量哈希
- 同名分组：按文件名归组
- 相似视频：5 秒音频频谱指纹 + 3 帧 dHash，阈值附近二次校验
- 相同片头：前 N 秒（1–300s）音频指纹 + 3 帧 dHash
- 隔离区、实时监控（fsnotify）、CSV/HTML 报告、选择助手、虚拟滚动
- 操作记录（审计）：`op_log` 表 + 内嵌「操作记录」页，删除类操作先记账再动手，可按条件查询并导出 CSV

### 技术约束（不要轻易打破）

- Go 1.26，`CGO_ENABLED=0` 交叉编译到 `linux/amd64`；SQLite 使用纯 Go 的 `modernc.org/sqlite`
- Web UI 通过 `go:embed ui` 内嵌，**不允许引入 CDN 或运行时前端依赖**；v3.0.0 起 `ui/` 含 `index.html` + 6 个经典脚本（helper/api/settings/view/modal/app）+ `styles.css`，由 `index.html` 按顺序 `<script defer>` 引入
- 前端零构建是刻意的部署优势；如需拆模块，必须保证产物仍是单文件并由 `go:embed` 引用
- **v4.0.0 起：响应契约是具名 struct（不改变零构建形态）**。规则：
  - **所有 `writeJSON` 的响应体都必须是 `internal/web/contracts*.go` 里的具名 struct**，不再用 `map[string]any`。
    字段名写在 struct 的 json tag 上，编译器能查出来；写在 map 字面量里则只有字面量知道。
  - **json tag 是前后端字段名的唯一真相源**：改 tag → 跑 `python3 tools/gen_types.py` 重新生成前端类型。
    `webtypes/types.d.ts` 是**自动生成**的，不要手改（手改会被下次生成覆盖）。
  - **`omitempty` 是陷阱**：原 map 里显式写出的键，即使值为 0/false/"" 也一定会出现在 JSON 里；
    struct 的 `omitempty` 会在值为零时把键整个省掉，前端 `out["done"].(float64)` 会 panic。
    所以：原 map 里无条件写出的键 → struct 里**不要**加 omitempty；
    原 map 里条件添加的键（值可能为空）→ 才用 omitempty。
  - **同一个 struct 服务多个键集合不同的接口是不行的**：要么按键集合拆成多个 struct，
    要么用 omitempty 精确表达「这处不写这个键」。改造时用 `tools/compare_json_keys.py` 核对。
  - 类型声明放 `webtypes/`，**不得放 `internal/web/ui/`**——`ui/` 整个目录被 `go:embed` 内嵌，放进去会把编译期文件打进二进制（`check-agents-md.sh` 断言位置）
  - 仍是**零 npm 运行时依赖**：`node_modules/` 只装 `typescript` 一个 devDependency，不进版本库，不进二进制
  - 前端类型门禁只阻断**真实类型错误**（字段拼错、对象字面量重复键、参数类型不符）；`TS2339`（属性不存在于 HTMLElement/Element）当前是已知噪音，不阻断——因为 `getElementById` 返回类型没有收窄到具体元素类型
  - **改响应结构后必须跑这两条验证**（它们是「零行为变更」的保证）：
    ```bash
    # 1) 逐接口比对 JSON 键集合（与改造前的源码比，需有基准目录）
    python3 tools/compare_json_keys.py --old <改造前源码目录> --new .
    # 2) 起新旧两个实例实抓响应对比
    python3 tools/compare_api.py --old <旧二进制> --new <新二进制>
    ```
- 直接依赖 7 个（fsnotify、cpuid、blake3、modernc sqlite、golang.org/x/image、golang.org/x/text——GB18030 解码（v3.12.0 修非 UTF-8 文件名）、go-pinyin——拼音音序键，均纯 Go 无 cgo）。新增依赖前先证明必要
- 繁简转换**不引入第三方库**：沿用 OpenCC 上游 Apache-2.0 的词典数据（内嵌在 `internal/engine/data/opencc/`，归属见该目录 `NOTICE.md`），前向最大匹配在 `internal/engine/data/` 自实现。原因是原依赖链里的 `github.com/liuzl/cedar-go` 为 GPL-2.0，静态链接会污染本项目的 MIT 分发；替换时用旧实现输出固化了对齐样本（`internal/engine/data/dict_test.go`）
- 视频分析复用系统 `ffmpeg`/`ffprobe`，不打包静态 FFmpeg
- 内嵌字典/规则数据（internal/**/data/）总量 ≤50M（`check-agents-md.sh` 断言，v1.31 护栏）
- `packaging/fpk/cmd/` 的 `install_init`、`uninstall_init`、`upgrade_init`、`upgrade_callback` 是 fnpack 1.2.3 实测强制要求的（缺文件直接打包失败），不能删；占位脚本至少要写一行日志，避免钩子静默空跑
- **飞牛上架结构约定（2026-11-26，按 developer.fnnas.com 官方文档）**：
  - 包图标 `ICON.PNG` 必须是 **64×64**、`ICON_256.PNG` 是 256×256（旧包曾把 `ICON.PNG` 误做成 256×256，已修）；入口图标 `app/ui/images/icon_64.png`/`icon_256.png` 是独立的一套，别混
  - `manifest` 上架审核会看 `maintainer_url`、`os_min_version`、`changelog`、`platform`（只有 amd64 二进制就写 `x86`，不要写 `all`）；字段名是 `changelog` 不是 `change_log`
  - 应用入口 `type=url` 是官方文档认可的打开方式（浏览器标签页），不是只能用 `iframe`；管理员专属用 `allUsers=false` + `control.accessPerm=readonly`
  - fnpack **1.3.3** 起默认要求开发者证书签名：无证书时 `fnpack build` 会直接失败，`scripts/build-fpk.sh` 显式加 `--nosign` 打本地调试包；拿到飞牛官方证书并 `fnpack cert import` 后去掉 `--nosign` 即为上架包（`--nosign` 不能与 `--key`/`--cert` 同用）。自签证书导入会被拒（`Invalid certificate`），证书只能从飞牛官方申请
  - 上架提交走飞牛粉丝群 → 社区主理人 → 「应用中心开发者先锋交流群」，另需真实界面截图；手动安装仅用于本地测试，不作为公开分发方式

### 长任务并发（v3.18.0 起硬约束）

- 任何会吃 CPU / 磁盘 / ffmpeg / 数据库写入的长任务（扫描、健康检测、视频/片头分析、剪除、转码、cropdetect、索引体检、清理计划、空目录清理、批量隔离与恢复/永久删除/清空、改名、导入）**必须**在动手前调用 `internal/engine` 的 `acquireTask(...)`，向 `internal/taskguard` 声明资源与独占组；冲突时拒绝启动并说明占用者，不允许静默并行。
- 新增长任务必须同步 `taskguard.Kind*`、`/api/tasks` 的展示名与本节清单；`scripts/check-agents-md.sh` 会断言 `acquireTask(taskguard.Kind` 的接入点不少于 7 处。
- 优先级固定：`userPaused`（用户暂停）> 闸门拒绝 > `diskBusyNow` 等待 > 正常执行；配置开关 `task_guard_enabled` 关闭时回到 v3.15 行为。

### 构建与测试

```bash
# 本地调试
go build -o verify .

# 目标平台静态构建
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags "-s -w" \
  -o packaging/fpk/app/bin/verify .

# 单元测试
go test ./...
go test -race ./...        # Windows 需要 gcc；CI 在 ubuntu-latest 执行

# 本地门禁（推荐：格式 + vet + 改动包测试 + 文档断言，输出 ≤20 行）
bash scripts/quick-check.sh

# 打包（产物输出到 dist/verify-<版本>.fpk，避免污染根目录）
bash scripts/build-fpk.sh
```

注意：

- 测试中存在 Linux 风格的绝对路径断言（以 `/` 开头的卷路径），**不要把 Windows 的测试失败直接当成代码 bug**；以 CI（ubuntu）为准
- `packaging/fpk/app/bin/verify` 与 `dist/verify-<版本>.fpk` 是构建产物，已被 `.gitignore` 忽略，**不要提交**
- 涉及 `syscall.Stat_t` / `Statfs_t` 的包（engine、watch、web）在 Windows 目标下无法编译，`go vet` / `go test` / `gosec` / `errcheck` 都必须带 `GOOS=linux`；否则 SSA 分析会被跳过，taint 类规则不会执行
- **不要凭印象断言工具是否存在**。`fnpack` 官方提供 Windows x86 版本（当前 `fnpack-1.3.3-windows-amd64`，下载后去掉后缀即可运行），需自行安装并加入 PATH；`scripts/build-fpk.sh` 启动时会检查。同类判断一律先查官方文档
- **公开前把关**：`bash scripts/check-public-safety.sh` 已挂进 `quick-check` 与 CI，扫三处（HEAD 树、待提交差异、可选 `--history`）。零容忍内网地址、本机绝对路径、邮箱、账号、令牌、私有仓地址、内部文档、构建产物；`/volN/`、`_外置`、`_tool` 只提示。确属误报的写进 `.public-safety-allow`（`glob<TAB>规则<TAB>原因`），门禁会逐条打印放行记录；本机真实值（账号、机器名、姓名）放 `.public-safety.local`，该文件不进仓库

### 开发工作流（省 Token 优先）

会话成本主要来自「整文件读入」和「全量命令输出」。按下面做，质量不降：

1. **定位再读**：改大文件前先拿行号索引，再用 `Read(offset/limit)` 只读目标段。`bash scripts/code-map.sh` 列出建议按需阅读的文件；`bash scripts/code-map.sh internal/web/ui/view.js` 输出该文件的顶层声明行号。不要整读单个 JS 模块（`view.js`/`api.js` 仍各约 1500 行）。
2. **一条命令验证**：`bash scripts/quick-check.sh` 依次跑 gofmt、`GOOS=linux go vet ./...`、工作区改动包的测试、`check-agents-md.sh`，失败才展开末尾日志。Windows 无法编译的包会标 `SKIP`，以 CI 为准；发版前用 `--all` 跑本机全部可编译包，`-race` 与 lint 交给 CI。
3. **读历史要有边界**：`CHANGELOG.md` 只读前 60 行（新版本段插在开头说明行之后）；`docs\` 下的方案文档先读文件头部「阅读顺序」，不整读几十 KB 的旧报告；`.workbuddy/memory` 只读 `MEMORY.md` 与当日文件。
4. **不重复求证**：技术约束、版本号位置、真机路径、发布流程等文档已写明的事实直接引用；只有怀疑文档过期时才核对代码。
5. **CI 日志按需取**：只看失败步骤 `gh run view <run-id> --log-failed | tail -40`；成功只记结论，不拉全量日志。
6. **审查先定范围**：审查 / 盘点类会话开场先按下面「审查范围白名单」确定读取范围，不在归档目录里做递归 glob / grep。

常见任务的起点：

| 任务 | 先读 |
|---|---|
| 前端交互 / 页面 | 按职责找 `helper.js`/`api.js`/`settings.js`/`view.js`/`modal.js`/`app.js` + `styles.css` 的 `:root` |
| 扫描 / 健康 / 剪除 / 转码 | `engine.go` 相关段 + 对应子文件 |
| 接口 / 鉴权 / 路径校验 | `server.go` 相关段 + 本文「安全与权限」 |
| 数据 / schema / 候选查询 | `store.go` 相关段 + 本文「代码风格」数据库条 |
| 发版 | 本文「版本与发布」 |
| 接手 / 交接 / 续接会话 | `README.md` + 本文件 + `CHANGELOG.md` 顶部版本段，再按需看 `docs\` |

#### 审查范围白名单（审查类会话开场先定范围）

全面审查项目前先按这张表划线，不要「先把项目全读一遍」。**审查 / 盘点类会话开场必须先按此表确定读取范围，不得在归档目录里做递归 glob / grep。**

- **必读范围**：`internal/`（按大文件分段读）、`scripts/`、`packaging/fpk/`（不含 `app/bin/`）、`AGENTS.md`、`CHANGELOG.md` 前 60 行。
- **禁读范围**：`cache/_patchcheck/`（整仓旧副本，400+ 项）、`dist/`、`app/bin/`、任何 `*.fpk`、`*.test`、`*.db`、`*.png`、`*.jpg`。
- **理由**：列举噪音按文件数算，历史归档目录曾有数千个文件（2026-11-09 已整体移出仓库，由维护者保存在仓库外）；字节大的是 `dist` 与二进制产物，误读一次就吃满上下文。
- 需要确认「某文件是否存在 / 多大」时，用只统计元数据的命令（`Get-ChildItem ... | Measure-Object`），不要读内容。

### 真机验证（飞牛）

- 使用飞牛官方 CLI（`trim-cli`）通过 HTTP API 访问 NAS（OAuth PKCE 登录，禁止索取/代填账号密码）
- 应用数据目录：fnOS 默认 `@appdata/verify/data`（`verify.db`、`logs/verify.log`、`cache/`）
- Web UI：`http://<NAS>:8898`，`/api/status` 可用于只读探测
- 写操作前先做只读探测（`system info`、`file ls`、`storage pools`），写后立即回归；一次只做一个可解释的写操作

### 版本与发布

版本号当前硬编码在 **5 处**，改动时必须同步：

1. `internal/web/server.go` 的 `const Version = "x.y.z"`
2. `packaging/fpk/manifest` 的 `version=`
3. `internal/web/ui/index.html` 的 `#ver` 占位（`vX.Y.Z`）
4. `README.md`（当前版本）
5. `CHANGELOG.md`（新增版本段落，插在开头说明行之后）

约定：

- 提交信息使用 Conventional Commits：`feat:` / `fix:` / `docs:` / `refactor:` / `chore:`
- 每个版本在本地打 `vX.Y.Z` tag；只有对外发布版才创建 GitHub Release（附件只挂该版 `dist/verify-<版本>.fpk`）
- **安装包文件名必须带版本号**（2026-11-07 起）：`scripts/build-fpk.sh` 从 manifest 读 `version=`，产物固定为 `dist/verify-<版本>.fpk`，Release 附件沿用同名；不要再用无版本号的 `dist/verify.fpk`
- **大版本发布顺序（2026-11-02 调整）**：本地门禁 → 构建 `dist/verify-<版本>.fpk` → 飞牛正式「卸载 → 重装」并真机验证功能 → 提交 → tag → push → GitHub Release。Git 四项仍打包执行、不逐项确认；**真机验证不通过就先修，不发布**，数据目录必须保留。这条只适用于「对外发布的那一版」，日常迭代按下方两级节奏走
- **推送需明确授权（2026-11-15 起，硬约束）**：未经用户明确授权，不得把本项目的代码、tag、Release 或安装包推送到 GitHub；建仓、改可见性、上传附件与 `gh release create` 同属推送类动作，一并适用。本地构建、`_外置` 归档、本地备份不受限制。**默认状态是「不发」**，只有用户明确说发，才执行 push / tag / Release / 上传附件——「门禁全绿」「验证通过」都不构成授权
- 构建统一走 `scripts/build-fpk.sh`（带 `-buildvcs=false`），保证发布前安装的包与最终 tag 附件是同一份、哈希一致。脚本**默认**附带两个本机动作，公开克隆下会自动降级、不影响构建：
  - 跨项目规范检查：默认取仓库上一级的 `_tool/spec-check/check-spec.ps1`，文件存在且 `pwsh` 可用就执行，FAIL 即中止发版；找不到只打一条 WARN（含期望路径）后继续
  - 归档：默认根为仓库上一级的 `_外置/产物/Verify查重助手`，**仅当该目录已存在**时把包与 `.sha256` 成对放进 `release-v<版本>/`；目录不存在或同名归档已存在时打 INFO / 拒绝覆盖，不会 `mkdir` 出目录树
  - 两者都可用环境变量 `SPEC_CHECK`、`RELEASE_ARCHIVE_ROOT` 覆盖
- **两级发布节奏（2026-11-15 起，与上一条授权要求配套）**：
  - **日常迭代只走本地**：改代码 → 同步五处版本号 → `CHANGELOG.md` 新增版本段 → 写 `cache/验证单/v<版本>.md` → `bash scripts/build-fpk.sh` 产出 `dist/verify-X.Y.Z.fpk` → 归档到 `_外置/产物/Verify查重助手/release-vX.Y.Z/` → 本地 commit + 本地打 tag。**不 push、不建 Release**
  - **重大更新才对外发布一次**：`git push origin main`（不带 `--tags`）→ 只推当前这一版的 tag → 建一个 Release，只挂这一版的 fpk 附件；中间版本的安装包一律不上传
  - **禁止 `git push --tags`**：本地 tag 会累积多版，一条命令就把每个中间版本都变成公开发布页。要发哪版就显式写全：`git push origin main vX.Y.Z`
  - **Release 说明写累积差异**：描述「自上次对外发布基线（如 `v3.26.1`）以来的变更」，按主题合并成几组；不逐条罗列中间版本的 commit 或 CHANGELOG 条目
  - **本地 tag 与远端 tag 不一致是预期状态**：远端只保留对外发布版的 tag；需要完整版本对照时以 `_外置/产物/Verify查重助手/` 下的 `release-v*` 归档目录为准
  - **飞牛「卸载 → 重装」真机验证只在对外发布的那一版做一次**；日常本地迭代按验证单执行即可
- 非大版本补丁在本地同样先构建、按验证单做真机冒烟再提交；纯文档或仅文案版本可免重装
- CHANGELOG 按「新增 / 改进 / 修复 / 升级说明」分组，使用中文
- 后续计划：用 `-X verify/internal/web.Version=...` 注入版本（需先把 `const` 改为 `var`），并用 `git-cliff` 从提交生成 CHANGELOG
- **产物归位（2026-11-13 起）**：项目内 `dist\` 只留最近 2–3 个 fpk 副本；维护者另外在仓库外保存全部历史版本发布包（一版一目录，目录名与 tag 一致）

#### 发版门禁：验证单与关门三问（2026-11-08 起）

发版前必须有 `cache/验证单/v<版本>.md`。它是「本次改了什么」和「本次要验什么」之间唯一的对应物；
没有它，真机冒烟靠记忆挑几项，改了三件事只验两件也没人会察觉。

- 起草：`pwsh -NoProfile -File scripts/release-sheet.ps1`
  （改动文件取自 `git diff HEAD` + `git ls-files --others`，改动条目取自 CHANGELOG 顶部版本段，套 `scripts/templates/验证单.md`）
- 已存在的验证单默认拒绝覆盖，防止冲掉已填的证据；确认旧单还没填证据时才加 `-Force`
- `/cache/` 已被 `.gitignore` 忽略：验证单留在本机，不进仓库、不随 tag 发布
- **关门三问，三条全过才能提交 / tag / Release**（与 `scripts/templates/验证单.md` 顶部勾选栏同文）：
  1. 验证表行数 ≥ CHANGELOG 条目数（合并同类项要在「来源」列写明合并了哪几条）
  2. 证据列没有空行
  3. 「未验证项」为空，或每一条都标了 `N-A` 并写明原因
- 结论只允许 `PASS` / `FAIL` / `N-A` / `BLOCKED`；证据必须是「命令 + 一行关键输出」或截图文件名，
  **不接受「正常」「已验证」这类叙述**；`FAIL` 与 `BLOCKED` 一律不发版
- 门禁没跑成（本机 bash 不可用、Linux-only 包无法编译等）必须在验证单的「等价替换」栏写清：
  为什么跑不了、替代命令、覆盖了原门的哪几项、结果；**整栏空白视为没跑门禁**

### 代码风格

- 注释、日志、用户可见错误一律中文；错误信息采用「中文描述（英文原错误）」。`internal/apperr` 提供统一封装，但**目前只有 `internal/engine` 接入**，`store` / `web` / `video` 仍各自拼中文串；改到相关代码时优先收敛，不要把这个当成已完成的现状
- 日志走 `internal/applog`，不要直接 `fmt.Println`
- 文件系统错误、ffmpeg 错误要保留原始 stderr 到日志，界面只展示摘要
- 配置校验集中在 `internal/config`；当前 `Load` 与 `Replace` 有重复校验，改动时优先抽公共函数
- 数据库：SQLite WAL；schema 用 `CREATE TABLE IF NOT EXISTS`，新增列必须给出兼容旧库的迁移路径；哈希算法/指纹算法变更必须让旧缓存失效
- 改名入口必须同步索引（v3.10.0 GUARD-01）：任何改名 / 移动函数都要同时更新 `files`（`path` / `name` / `ext`）与 `health_results`（删旧路径的全部行），参考 `internal/engine/group_rename.go` 的四步模板；批量改名用 `store.RenameFilePathsBatch` 只改路径字段、不重算指纹，避免几千个文件一起改名时读满磁盘
- 大函数（`store.go`、`engine.go`、`server.go`）改动时优先拆小，新增函数保持单一职责
- 前端：原生 JS + 原生 CSS，禁止引入框架；交互改动需覆盖空结果、超大结果集、扫描中三种状态
- 设计流程：新建 UI 或整页重设计走 `hallmark` 技能（全局技能目录 `~/.trae-cn/skills/hallmark`）；既有界面的局部改动沿用 `styles.css` 的 `:root` 变量，不套完整流程（技能自身限定不用于既有设计）。Hallmark 的 `tokens.css`、`design.md`、`.hallmark/` 目录、外部字体与 Node 检查脚本都不进本仓库，落地必须服从上面的零构建与无 CDN 约束；`sloplint` 只作本地自检，不进 CI

### 接口契约（v3.9.8 新增）

前端/外部调用方必须按以下契约构造请求。后端对类型/参数缺失做严格校验，错误文案已改成「字段 `xxx` 类型不对，期望 `bool`」风格（见 `internal/web/folder_rename.go` 的 `friendlyDecodeError`）。

| 接口 | 字段 / 参数 | 实际要求 | 错误传参的响应 | 代码位置 |
|---|---|---|---|---|
| `POST /api/folder_rename` | `prefix_a` | **bool** | 传 `""` → 400「字段 'prefix_a' 类型不对，期望 布尔值（true/false）」 | `internal/web/folder_rename.go:16` |
| `POST /api/transcode` | `max_res` | **string**（`""` 或 `"1080"`） | 传 `0` → 400「字段 'max_res' 类型不对，期望 字符串」 | `internal/web/transcode.go:22` |
| `GET /api/trash` | `limit` + `cursor`（均可选） | `limit` 取 1–500；**两个都不传 = 一次返回全部（老行为）**，响应 `items` 语义不变，只追加 `total`/`returned`/`has_more`/`next_cursor` | `limit=abc` → 400「分页参数 limit 必须是 1–500 的整数」；游标非法 → 400「分页游标无效，请刷新隔离区」 | `internal/web/server.go` handleTrash |
| `GET /api/trash/list` | `limit` + `cursor`（均可选） | **只读**：参数与 `GET /api/trash` 相同，但不调用 `HealTrashRows`、不写库，可安全用于自动化与生产探测 | 同 `GET /api/trash` | `internal/web/server.go` handleTrashList（v3.26.0 F1） |
| `GET /api/groups`、`GET /api/groups/video` | `page_size` | 1–**1000**（v3.26.0 F2 从 200 放宽），默认 20；超上界自动钳到 1000 | 非法值按默认 20 处理 | `internal/web/common.go:127`、`internal/web/server.go` handleVideoGroups |
| `GET /api/metrics` | 无参数 | 只读返回 `pid`/`uptime_seconds`/`cpu_seconds_total`/`rss_bytes`/`io_read_bytes`/`io_write_bytes`/`goroutines`/`version`；与 `POST /api/metric`（前端埋点）不是同一接口 | 非 GET → 405 | `internal/web/metrics.go` handleMetrics |
| `GET /api/events` | `?token=` | **仅该路由接受查询参数令牌**（SEC-6 有意收紧）；`/api/raw`、`/api/thumb` 带 `?token=` → 401，需 Cookie 或 Bearer | — | `internal/web/server.go:490` |
| `GET /api/trash/export` | `ids` 或 `all=true` | 必须显式指定 | 不传 → 400「未指定导出条目」 | `internal/web/trash_export.go:47` |
| `GET /api/import/groups` | `session_id`（不是 `session`） | 正整数 | 传 `session=2` → 400「无效的会话 ID」 | `internal/web/import.go:123` |
| `GET /api/groups` | 同名分组是 `?mode=name` | 无 `/api/groups/name` 路由 | 调 `/api/groups/name` → 404 | `internal/web/server.go:857-863` |
| `POST /api/cleanup/plans` | `schedule` | `daily HH:MM` / `weekly fri HH:MM` / `interval 秒数` | 缺格式 → 「时间格式应为…」 | `internal/engine/cleanup.go:65` |
| 同上 | `retain_strategy` | 仅 `modify_newest` / `largest_size` / `best_codec` | 传 `newest` → 「保留策略只能是…」 | `internal/web/cleanup.go:157` |
| `POST /api/trim` | `jobs[].offset` + `jobs[].group_key` + `jobs[].replace` | offset 是裁剪点秒数（**不是** intro_seconds），group_key 从分组接口取，replace 布尔（true=隔离原文件） | 传 `intro_seconds` 字段 → 后端读不到 offset → 「裁剪点必须大于 0」 | `internal/engine/trim.go:30` |
| `POST /api/trim/probe` | `group_key` + `paths`（不是 jobs） | 边界探测入参与 trim 不同，传 jobs 结构 → 「缺少分组或成员不足」 | - | `internal/web/trim.go:54` |
| `POST /api/cleanup/plans` | `match_mode` 枚举 `all/exact/name/video/audio` | **不是** `same_hash`；same_hash 概念对应 `exact` | 传 `same_hash` → 400「匹配模式只能是 all / exact / name / video / audio」 | `internal/web/cleanup.go:45` |
| `POST /api/retain/score` | `groups` | 非空分组数组 | 传 `[]` → 400「未选择分组」；超限 → 400「分组数量超限（最多 200 组）」 | `internal/web/retain.go:36` |

**撤销能力说明**（v3.9.8 F-02 修正）：`/api/health/undo` 只回放改名类任务（修正扩展名、繁简改名）；移入隔离区的清理（`/api/health/trash`、`/api/emptydir/clean`）请到隔离区页恢复——恢复入口 `POST /api/trash/restore` 由 `checkTrashRowPath` 做路径复核，不得绕过。

### 安全与权限

本节混着两类条目，必须区分：`[现状]` 描述代码当前行为，可被 `scripts/check-agents-md.sh` 核验；`[要求]` 是对后续改动的约束，没有对应实现，不要当成已做到。

**上架依据**：飞牛应用开放平台《应用入口 › 鉴权和安全》（developer.fnnas.com/docs/core-concepts/app-entry/）要求「网关校验登录态、应用执行业务权限」：用户只能访问自己的数据、管理接口需管理员身份、高风险操作需明确权限检查、文件路径与记录 ID 结合当前用户校验，且文件访问要标准化路径、拒绝 `..`、只从预期目录提供、不暴露密钥/数据库/配置/私有日志。本项目是单用户 NAS 工具（无多用户数据模型），令牌即管理凭据；2026-11-26 上架前逐条核对，下方 `[现状]` 条目即对应这些要求的实现，改动时不得放宽。

- [现状] 应用可能无鉴权运行在局域网内；`AccessToken` 为空表示不登录
- [现状] 令牌比较统一走 `internal/webauth.TokenEqual`：两侧先 SHA-256 再 `subtle.ConstantTimeCompare`，同时避免时序旁路和长度泄露
- [现状] 登录 Cookie 保存的是 `internal/webauth.CookieValue(token)` 派生值，不含明文令牌；令牌变更后旧 Cookie 自动失效
- [现状] `?token=` 查询参数**仅** `/api/events`（SSE）接受（SEC-6 有意收紧）；`/api/raw`、`/api/thumb` 需 Cookie 或 `Authorization: Bearer`，带 `?token=` 一律 401
- [要求] 新增任何令牌校验入口都必须用 `webauth.TokenEqual`，不要写 `==`
- [现状] 所有面向请求的文件路径入口（含 `/api/raw`、`/api/thumb` 与 `internal/fsbrowse` 目录浏览）均做「Clean + 软链接解析 + 卷前缀重判」三步防线校验（v1.13.1 起，`ValidPath`/`ValidDirPath` → `resolveManagedPath`；CI 断言第 18 项防回潮）
- [要求] 任何放宽上述校验的改动都必须补测试
- [现状] `GET /api/trash` 会顺手清理幽灵隔离行（`HealTrashRows`），**属写操作**；兼容性验证与生产探测一律用临时实例，不得直接拿生产实例发这个 GET（2026-11-10 起为约定）。只读场景改用 `GET /api/trash/list`（v3.26.0 新增）：参数相同、不清理、不写库
- [现状] 操作审计记录存 `op_log` 表（`internal/oplog` 纯逻辑 + `internal/store/oplog.go` SQL），权威记录不依赖 `verify.log` 轮转；界面「操作记录」页可查、可导出 CSV
- [现状] 永久删除 / 清空隔离区在动文件前独立提交审计行，写入失败即拒绝操作（fail-closed）；其它操作审计失败只告警
- [要求] 新增不可逆操作必须沿用「先记账、再动手」的顺序，且不得把审计行放进业务事务（业务回滚不能带走审计）
- [要求] 任何操作记录都不得写入访问令牌、Cookie、Authorization 头或完整请求体；URL 只记 path 不记 query
- [要求] 发布动作按「提交 → tag → push → Release」一次打包执行（**执行前须按「版本与发布」取得用户明确授权**）；任何版本都不要删除用户数据，也不要改动仓库外的历史归档

### 完成前自检

1. `bash scripts/quick-check.sh` 全绿：gofmt、`GOOS=linux go vet ./...`、`internal/web/ui/*.js` 的 `node --check`、改动包测试、文档断言一次跑完；标 `SKIP` 的 Linux-only 包以 CI 为准。本机 bash 起不来时，按验证单「等价替换」栏逐条顶替并记结果
2. 版本号五处一致（如本次涉及发版）
3. 新增/修改的路径有对应测试；UI 改动附截图或明确说明验证方式
4. 真机相关改动在 NAS 上做过只读探测 + 写后回归
5. 不把构建产物、数据库、日志、缓存提交进仓库
6. 涉及发版：`cache/验证单/v<版本>.md` 存在，且过了关门三问（见「版本与发布」）
