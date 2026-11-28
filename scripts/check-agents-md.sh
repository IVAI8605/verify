#!/usr/bin/env bash
# 逐条核验 AGENTS.md 里可以机械判断的断言。
#
# 只覆盖"读文件/查依赖就能判定真假"的那部分；需要联网、真机或产品判断的条目
# 不在本脚本范围内，改动 AGENTS.md 时请人工过一遍那几类。
#
# 任何一条 FAIL 都以非 0 退出，CI 里直接卡住。
set -uo pipefail

fail=0
pass=0

ok() { printf '  PASS  %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  FAIL  %s\n' "$1"; fail=$((fail + 1)); }
section() { printf '\n[%s]\n' "$1"; }

cd "$(dirname "$0")/.." || exit 1

section "技术栈与依赖"
gover=$(awk '$1 == "go" { print $2; exit }' go.mod)
case "$gover" in
1.26*) ok "go.mod 的 Go 版本为 $gover（AGENTS.md 声明 Go 1.26）" ;;
*) bad "go.mod 的 Go 版本为 $gover，与 AGENTS.md 声明的 Go 1.26 不符" ;;
esac

# 直接依赖：取 go.mod 第一个 require 块里不带 // indirect 的条目。
deps=$(awk '/^require \(/{b=1;next} b&&/^\)/{b=0} b&&NF&&$0!~/\/\/ indirect/{print $1}' go.mod | sort)
expected=$(printf '%s\n' \
	"github.com/fsnotify/fsnotify" \
	"github.com/klauspost/cpuid/v2" \
	"lukechampine.com/blake3" \
	"github.com/mozillazg/go-pinyin" \
	"modernc.org/sqlite" \
	"golang.org/x/image" \
	"golang.org/x/text" | sort)
if [ "$deps" = "$expected" ]; then
	ok "直接依赖仍是 7 个且与约定一致"
else
	bad "直接依赖与 AGENTS.md 约定的 7 个不一致：$(echo "$deps" | tr '\n' ' ')"
fi

# 路径守卫断言（2026-11-01 审查发现 #1 修复后固化，防回潮）：
# ValidPath 必须同时含 Clean（消解 ..）与 EvalSymlinks（解析软链接），
# 并对解析结果重判卷前缀——否则 raw/thumb 等入口可被路径穿越读取任意文件。
if grep -q "filepath.Clean" internal/engine/engine.go && grep -q "filepath.EvalSymlinks" internal/engine/engine.go && grep -q "resolveManagedPath" internal/engine/engine.go; then
	ok "ValidPath 路径守卫含 Clean + EvalSymlinks + 卷前缀重判"
else
	bad "ValidPath 路径守卫缺失 Clean/EvalSymlinks/resolveManagedPath（路径穿越风险回潮）"
fi

if grep -q '"modernc.org/sqlite"' internal/store/store.go && ! grep -rq 'mattn/go-sqlite3' internal/; then
	ok "SQLite 驱动为 modernc.org/sqlite（纯 Go，无 cgo 驱动）"
else
	bad "SQLite 驱动不是 modernc.org/sqlite，或引入了 cgo 驱动"
fi

section "长任务资源声明（v3.18.0 起硬约束）"
acquire_calls=$(grep -rn 'acquireTask(taskguard.Kind' internal/engine --include='*.go' 2>/dev/null | grep -v '_test.go' | wc -l | tr -d ' ')
if grep -q 'e.guard = taskguard.New' internal/engine/engine.go \
	&& grep -q 'task_guard_enabled' internal/config/config.go \
	&& [ "$acquire_calls" -ge 7 ]; then
	ok "长任务通过 taskguard 声明资源（接入点 $acquire_calls 处，≥7）"
else
	bad "长任务资源声明缺失：acquireTask 接入点 $acquire_calls 处（应 ≥7），或 taskguard/配置开关未接线"
fi

section "前端模块文件与零外部依赖（v3.0.0 拆分）"
ui_count=$(find internal/web/ui -type f | wc -l | tr -d ' ')
ui_modules="helper.js api.js api-files.js settings.js view.js modal.js onboarding.js app.js"
ui_missing=""
for f in index.html styles.css $ui_modules; do
	[ -f "internal/web/ui/$f" ] || ui_missing="$ui_missing $f"
done
# v3.27 类型层：类型声明放在 webtypes/ 而不是 internal/web/ui/，
# 这样 go:embed ui 不会把编译期文件打进二进制（v4.5.0 起 ui/ 共 10 个文件）。
if [ "$ui_count" = "10" ] && [ -z "$ui_missing" ]; then
	ok "internal/web/ui 含 index.html + 8 个 JS 模块 + styles.css（go:embed 单二进制）"
else
	bad "internal/web/ui 应恰含 10 个文件，实际 $ui_count 个，缺失：${ui_missing:-无}"
fi

# v3.27 类型层：类型声明必须在 ui/ 之外，否则会被 go:embed 打进二进制。
if [ -f "webtypes/types.d.ts" ] && [ ! -f "internal/web/ui/types.d.ts" ]; then
	ok "类型声明在 webtypes/（不进 go:embed 产物）"
else
	bad "类型声明位置不对：应在 webtypes/types.d.ts，且不得放在 internal/web/ui/ 内"
fi

if grep -q '//go:embed ui' internal/web/server.go; then
	ok "server.go 通过 go:embed 内嵌 index.html"
else
	bad "server.go 未发现 //go:embed ui"
fi

if grep -nE '(src|href)="(https?:)?//' internal/web/ui/index.html >/dev/null; then
	bad "index.html 存在外部资源引用：$(grep -nE '(src|href)="(https?:)?//' internal/web/ui/index.html | head -3 | tr '\n' ' ')"
else
	ok "index.html 无外部（CDN/协议相对）资源引用"
fi

if grep -qiE '\b(react|vue|jquery|angular|bootstrap|tailwindcss|alpinejs)\b' \
	internal/web/ui/index.html internal/web/ui/*.js internal/web/ui/styles.css; then
	bad "前端模块出现前端框架关键字"
else
	ok "前端模块未引入框架"
fi

if grep -nE '<script[^>]*src="(https?:)?//' internal/web/ui/index.html >/dev/null; then
	bad "index.html 存在外链脚本，前端不再零构建"
else
	ui_script_missing=""
	for f in $ui_modules; do
		grep -q "<script src=\"ui/$f\"" internal/web/ui/index.html || ui_script_missing="$ui_script_missing $f"
	done
	if [ -z "$ui_script_missing" ]; then
		ok "index.html 按顺序引用 6 个本地 ui 模块（前端零构建）"
	else
		bad "index.html 缺少本地模块引用：${ui_script_missing}"
	fi
fi

section "鉴权实现"
if grep -q 'webauth.TokenEqual' internal/web/server.go; then
	ok "server.go 的令牌比较走 webauth.TokenEqual"
else
	bad "server.go 未使用 webauth.TokenEqual，令牌比较可能退化为 =="
fi

if grep -q 'subtle.ConstantTimeCompare' internal/webauth/webauth.go; then
	ok "webauth 使用 subtle.ConstantTimeCompare 做常量时间比较"
else
	bad "webauth 未使用常量时间比较"
fi

if grep -qE 'Value: *token[[:space:]]*[,}]' internal/web/server.go; then
	bad "登录 Cookie 仍在保存明文令牌（AGENTS.md 声明只存派生值）"
else
	ok "登录 Cookie 未保存明文令牌"
fi

section "版本号一致性"
v_go=$(sed -nE 's/^const Version = "([^"]+)"$/\1/p' internal/web/server.go | head -1)
v_manifest=$(sed -nE 's/^version=([0-9]+\.[0-9]+\.[0-9]+)$/\1/p' packaging/fpk/manifest | head -1)
v_readme=$(sed -nE 's/.*当前版本 \*\*v([0-9]+\.[0-9]+\.[0-9]+)\*\*.*/\1/p' README.md | head -1)
v_changelog=$(sed -nE 's/^## v([0-9]+\.[0-9]+\.[0-9]+).*/\1/p' CHANGELOG.md | head -1)
v_index=$(sed -nE 's/.*id="ver">v([0-9]+\.[0-9]+\.[0-9]+)<.*/\1/p' internal/web/ui/index.html | head -1)
if [ -n "$v_go" ] && [ "$v_go" = "$v_manifest" ] && [ "$v_go" = "$v_readme" ] && [ "$v_go" = "$v_changelog" ] && [ "$v_go" = "$v_index" ]; then
	ok "版本五处一致：server.go / manifest / index.html / README / CHANGELOG 均为 $v_go"
else
	bad "版本不一致：server.go=$v_go manifest=$v_manifest index.html=$v_index README=$v_readme CHANGELOG=$v_changelog"
fi

section "数据层约定"
if grep -q 'journal_mode(WAL)' internal/store/store.go; then
	ok "SQLite 使用 WAL 模式"
else
	bad "store.go 未发现 journal_mode(WAL)"
fi

if grep -q 'CREATE TABLE IF NOT EXISTS' internal/store/store.go; then
	ok "schema 使用 CREATE TABLE IF NOT EXISTS"
else
	bad "schema 未使用 CREATE TABLE IF NOT EXISTS"
fi

if grep -q 'ALTER TABLE' internal/store/store.go; then
	ok "存在 ALTER TABLE 迁移路径（新增列兼容旧库）"
else
	bad "未发现 ALTER TABLE，新增列可能缺少旧库迁移路径"
fi

section "打包与产物"
# 内嵌字典/规则数据 50M 护栏（v1.31 §0）：internal/**/data/ 目录总大小不得超过 50MiB
dict_bytes=$(find internal -type d -name data -exec du -sb {} + 2>/dev/null | awk '{s+=$1} END {print s+0}')
if [ "$dict_bytes" -le $((50 * 1024 * 1024)) ]; then
	ok "内嵌字典/规则数据 ${dict_bytes} 字节，未超 50M 护栏"
else
	bad "内嵌字典/规则数据共 ${dict_bytes} 字节，超过 50M 护栏（规划 §0 红线）"
fi
if grep -qE '^dist/$' .gitignore && grep -qE '^packaging/fpk/app/bin/$' .gitignore; then
	ok "构建产物（dist/、app/bin/）已被 .gitignore 忽略"
else
	bad ".gitignore 未同时忽略 dist/ 与 packaging/fpk/app/bin/"
fi

if [ -z "$(find packaging -type f \( -iname '*ffmpeg*' -o -iname '*ffprobe*' \) 2>/dev/null)" ]; then
	ok "packaging 下没有打包静态 FFmpeg/ffprobe"
else
	bad 'packaging 下发现 FFmpeg/ffprobe 二进制，与复用系统 FFmpeg 的约定不符'
fi

printf '\n结果：%d 项通过，%d 项失败\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
