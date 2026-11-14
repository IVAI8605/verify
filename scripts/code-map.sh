#!/usr/bin/env bash
# 大文件符号索引：先看行号，再用 Read(offset/limit) 精读目标片段，避免整文件读入。
#
# 用法：
#   bash scripts/code-map.sh                          # 列出建议按需阅读的大文件
#   bash scripts/code-map.sh internal/web/ui/view.js    # 输出该文件的顶层声明行号
set -uo pipefail

cd "$(dirname "$0")/.."

if [ $# -eq 0 ]; then
	echo "建议按需阅读的大文件（别整读）："
	for f in internal/web/ui/helper.js internal/web/ui/api.js internal/web/ui/settings.js \
		internal/web/ui/view.js internal/web/ui/modal.js internal/web/ui/app.js \
		internal/web/ui/styles.css internal/store/store.go \
		internal/engine/engine.go internal/web/server.go CHANGELOG.md; do
		[ -f "$f" ] || continue
		printf '  %-32s %5s 行  %6s KB\n' "$f" \
			"$(wc -l <"$f" | tr -d ' ')" "$(($(wc -c <"$f") / 1024))"
	done
	echo
	echo "示例：bash scripts/code-map.sh internal/web/ui/view.js"
	exit 0
fi

for f in "$@"; do
	if [ ! -f "$f" ]; then
		echo "跳过（文件不存在）：$f"
		continue
	fi
	printf '\n=== %s（%s 行）===\n' "$f" "$(wc -l <"$f" | tr -d ' ')"
	case "$f" in
	*.go)
		grep -nE '^(func|type) ' "$f" | head -140 || true
		;;
	*.js)
		grep -nE '^(async )?function |^const [A-Za-z_$][A-Za-z0-9_$]* ?= |^// =====' "$f" | head -240 || true
		;;
	*.css)
		grep -nE '^\/\*|^:root|^\.?[A-Za-z0-9_-]+ ?\{' "$f" | head -150 || true
		;;
	*.md)
		grep -nE '^#{1,3} ' "$f" | head -100 || true
		;;
	*)
		grep -nE '^(func|type|const|var) ' "$f" | head -100 || true
		;;
	esac
done
