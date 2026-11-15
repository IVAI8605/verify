#!/usr/bin/env bash
# 本地快速门禁：格式、跨平台 vet、前端语法、按改动选包测试、文档断言，一次跑完。
# 输出压缩到 20 行内，失败才展开末尾日志。全量 -race / lint 由 CI 负责。
#
# 用法：
#   bash scripts/quick-check.sh                # 只测工作区改动涉及的包
#   bash scripts/quick-check.sh --all          # 测本机可编译的全部包（发版前用）
#   bash scripts/quick-check.sh ./internal/store ./internal/config
#
# 纯前端改动（ui/*.js、styles.css、index.html）不会有 Go 包被选中，
# 此时真正起作用的检查是 [前端语法] 的 node --check 与 [文档断言]。
set -uo pipefail

cd "$(dirname "$0")/.."

pass=0
fail=0
ok() { pass=$((pass + 1)); printf '  PASS  %s\n' "$*"; }
bad() { fail=$((fail + 1)); printf '  FAIL  %s\n' "$*"; }

# 由改动过的 .go 文件推导包路径；根目录文件映射为 ./
changed_pkgs() {
	{ git diff --name-only; git diff --cached --name-only; git ls-files --others --exclude-standard; } \
		| grep -E '\.go$' | sort -u \
		| awk '{
			if (index($0, "/") == 0) { print "./"; next }
			p = $0
			sub(/\/[^/]+$/, "", p)
			print "./" p
		}' | sort -u
}

# 是否存在非 Go 的工作区改动（前端 / 文档 / 配置）。
# 纯前端改动时 changed_pkgs 为空，不能就这样打印「未检测到改动」了事。
has_non_go_changes() {
	{ git diff --name-only; git diff --cached --name-only; git ls-files --others --exclude-standard; } \
		| grep -vE '\.go$' | grep -q .
}

echo "[格式]"
# 已跟踪 + 新增未跟踪的 Go 文件都要检查，否则新文件能绕过 gofmt。
unformatted=$({ git ls-files '*.go'; git ls-files --others --exclude-standard '*.go'; } | sort -u | xargs -r gofmt -l 2>/dev/null)
if [ -z "$unformatted" ]; then
	ok "gofmt 无未格式化文件"
else
	bad "gofmt 发现未格式化文件（前 10 个）"
	printf '%s\n' "$unformatted" | head -10
fi

echo "[静态检查]"
if vet_out=$(GOOS=linux GOARCH=amd64 go vet ./... 2>&1); then
	ok "GOOS=linux go vet ./..."
else
	bad "go vet 失败（末尾 15 行）"
	printf '%s\n' "$vet_out" | tail -15
fi

echo "[前端语法]"
# 前端零构建，没有打包阶段的语法检查；只有 node --check 能在本地拦住拼写、括号、非法语法。
# 纯前端改动时这是唯一真正执行到的代码检查，所以不看有没有改动，一律全量检查 ui/*.js。
if ! command -v node >/dev/null 2>&1; then
	echo "  SKIP  未找到 node，前端语法未检查（CI 也不覆盖这项）"
else
	js_bad=""
	for f in internal/web/ui/*.js; do
		[ -f "$f" ] || continue
		node --check "$f" >/dev/null 2>&1 || js_bad="$js_bad $f"
	done
	if [ -z "$js_bad" ]; then
		ok "node --check 通过（internal/web/ui/*.js）"
	else
		bad "node --check 失败：$js_bad"
	fi
fi

echo "[单元测试]"
pkgs=()
if [ "${1:-}" = "--all" ]; then
	mapfile -t pkgs < <(go list ./...)
elif [ $# -gt 0 ]; then
	pkgs=("$@")
else
	mapfile -t pkgs < <(changed_pkgs)
fi

if [ ${#pkgs[@]} -eq 0 ]; then
	if [ "${1:-}" = "--all" ]; then
		echo "  SKIP  未能列出可测试的包（检查 go list 输出）"
	elif has_non_go_changes; then
		echo "  SKIP  无 Go 改动（本次为前端/文档改动，已由[前端语法]与[文档断言]覆盖）"
	else
		echo "  SKIP  未检测到改动（可传 --all 或指定包）"
	fi
else
	for p in "${pkgs[@]}"; do
		# Windows 下依赖 syscall.Stat_t 的包无法编译测试，静默跳过并交给 CI
		if ! go test -run '^$' "$p" >/dev/null 2>&1; then
			printf '  SKIP  %s（本机无法编译，交给 CI linux）\n' "$p"
			continue
		fi
		if out=$(go test -count=1 "$p" 2>&1); then
			ok "$p"
		else
			bad "$p（末尾 20 行）"
			printf '%s\n' "$out" | tail -20
		fi
	done
fi

echo "[文档断言]"
if doc_out=$(bash scripts/check-agents-md.sh 2>&1); then
	ok "$(printf '%s\n' "$doc_out" | tail -1)"
else
	bad "check-agents-md.sh 失败"
	printf '%s\n' "$doc_out" | grep -E 'FAIL|结果' | head -20
fi

echo "[公开把关]"
# 防「关键信息泄漏」（内网地址、本机路径、令牌、账号…），不防路径写法。
# 放行清单在 .public-safety-allow，每条都带原因并会被打印出来。
if ps_out=$(bash scripts/check-public-safety.sh 2>&1); then
	ok "$(printf '%s\n' "$ps_out" | tail -1)"
else
	bad "check-public-safety.sh 有命中（处理或补放行）"
	printf '%s\n' "$ps_out" | grep -E 'FAIL' | head -10
fi

printf '\nquick-check：%d 项通过，%d 项失败\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
