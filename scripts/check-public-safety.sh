#!/usr/bin/env bash
# 公开前把关：拦「关键信息泄漏」，不拦「路径写法」。
#
# 拦截（零容忍，宁可误报）：内网/私有 IP、带个人目录的本机绝对路径、Unix 用户目录、
# UNC 主机名、邮箱、账号、令牌/私钥样式、私有仓库地址、内部文档、构建产物。
# 只提示：盘符路径（示例值多）、/mnt/、/volN/、_外置、_tool、_规范。
#
# 用法：
#   bash scripts/check-public-safety.sh              # 仓库树 + 待提交差异（门禁用这个）
#   bash scripts/check-public-safety.sh --tree       # 只看将要公开的仓库树（HEAD）
#   bash scripts/check-public-safety.sh --diff       # 只看暂存区 / 工作区 / 未跟踪
#   bash scripts/check-public-safety.sh --history    # 追加扫提交历史（默认关，慢）
#   bash scripts/check-public-safety.sh --selftest   # 构造样本，验证每类都能拦住
#   bash scripts/check-public-safety.sh --show       # 打印命中原文（默认脱敏，防止日志二次泄露）
#
# 放行：仓库根 `.public-safety-allow`，一行一条 `<path-glob><TAB><rule><TAB><原因>`。
# 每条放行都会打印成 ALLOW，不静默跳过。
#
# 本机真实值（账号 / 机器名 / 真实姓名 / 私有仓名）不进脚本，放 `.public-safety.local`
# （已被 .gitignore 忽略），避免「防泄漏脚本自己泄露」。
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT" || exit 2

LOCAL_CONF=${PUBLIC_SAFETY_LOCAL:-$ROOT/.public-safety.local}
ALLOW_FILE=${PUBLIC_SAFETY_ALLOW:-$ROOT/.public-safety-allow}
SHOW=0
DO_TREE=0
DO_DIFF=0
DO_HIST=0
DO_SELFTEST=0

for a in "$@"; do
	case "$a" in
	--tree) DO_TREE=1 ;;
	--diff) DO_DIFF=1 ;;
	--history) DO_HIST=1 ;;
	--show) SHOW=1 ;;
	--selftest) DO_SELFTEST=1 ;;
	-h | --help) sed -n '2,20p' "$0"; exit 0 ;;
	*)
		echo "未知参数：$a（-h 看用法）" >&2
		exit 2
		;;
	esac
done
if [ "$DO_TREE$DO_DIFF$DO_HIST$DO_SELFTEST" = "0000" ]; then
	DO_TREE=1
	DO_DIFF=1
fi

hits=0
allowed=0
tips=0

# ---------------------------------------------------------------- 规则表
RID=()
RRE=()
RSEV=() # block | tip
RWHY=()
add_rule() { # id regex severity why
	RID+=("$1")
	RRE+=("$2")
	RSEV+=("$3")
	RWHY+=("$4")
}

# 内网地址：10/8、172.16/12、192.168/16。前后加边界，避免误伤版本号与更长数字。
add_rule ip-private \
	'(^|[^0-9A-Za-z.])(10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]{1,3}\.[0-9]{1,3}|192\.168\.[0-9]{1,3}\.[0-9]{1,3})([^0-9]|$)' \
	block '内网地址一出现就等于公开拓扑'
# 带个人目录的盘符路径：这种不是「示例盘符」，基本就是某台机器上的真实位置。
add_rule path-abs-win \
	'[A-Za-z]:\\+(Users|Documents|Desktop|AppData|Program|工作|项目|桌面|文档)' \
	block '本机绝对路径：会暴露用户目录与目录结构'
# 任意盘符路径：示例值太多，只提示。
add_rule path-drive \
	'(^|[^A-Za-z0-9])[A-Za-z]:\\' \
	tip '盘符路径，多为示例，人工判断'
# Unix 用户目录（去掉 /mnt/、单字母占位）。
add_rule path-home \
	'(/home/[a-z][a-z0-9_-]{2,}/|/Users/[A-Za-z][A-Za-z0-9_-]{2,}/|(^|[[:space:]"'"'"'(])/root/)' \
	block '本机绝对路径：会暴露用户名'
add_rule path-mount \
	'(^|[^A-Za-z0-9])/mnt/[a-z]' \
	tip '挂载点写法，未必是隐私'
# UNC：\\主机\共享。纯占位名（NAS/HOST/SERVER…）降级为提示，见 placeholder_host。
add_rule unc-host \
	'(^|[^\\])\\\\[A-Za-z0-9._-]{2,}\\[A-Za-z]' \
	block 'UNC 主机名等于机器名'
# 邮箱：noreply 与示例域名走放行清单。
add_rule email \
	'[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' \
	block '邮箱属个人信息'
add_rule secret \
	'(ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY|xox[baprs]-[A-Za-z0-9-]{10,})' \
	block '凭据进公开历史几乎无法彻底撤销'
add_rule jwt \
	'eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.' \
	block 'JWT 含明文声明，等同凭据'
add_rule token-assign \
	'(password|passwd|secret|token|api[_-]?key)[[:space:]]*[:=][[:space:]]*["'"'"'][^"'"'"']{8,}' \
	block '硬编码口令 / 令牌'
# 账号：只在带上下文词时判，避免把时间戳、字节数当账号。
add_rule acct-context \
	'(账号|帐号|account|user_?id|uid)[^0-9]{0,8}[0-9]{6,}' \
	block '账号属身份信息'
# 拆分拼接：把 IP / 路径拆成多段以绕过单行正则。
add_rule ip-split \
	'["'"'"'][0-9]{1,3}\.["'"'"'][[:space:]]*\+[[:space:]]*["'"'"']' \
	block '疑似用拼接绕过关键词扫描'
# 真正的凭据文件（产品自带的 access-token.txt 属公开知识，不在此列）。
add_rule cred-file \
	'(id_rsa|\.netrc\b|credentials\.json|\.pem\b)' \
	block '凭据文件名会把读者引向真实凭据位置'

# ---------------------------------------------------------------- 放行与输出
allow_reason() { # $1 path $2 rule → 打印原因
	[ -f "$ALLOW_FILE" ] || return 1
	local path=$1 rule=$2 g r why
	while IFS=$'\t' read -r g r why; do
		case "$g" in '' | '#'*) continue ;; esac
		[ "$r" = "$rule" ] || continue
		# shellcheck disable=SC2254
		case "$path" in $g) printf '%s' "$why"; return 0 ;; esac
	done <"$ALLOW_FILE"
	return 1
}

redact() {
	local s=$1
	if [ "$SHOW" -eq 1 ]; then
		printf '%s' "$s"
		return
	fi
	printf '%s' "$s" |
		sed -E 's/[0-9]{1,3}(\.[0-9]{1,3}){3}/<IP>/g; s/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/<EMAIL>/g; s/[0-9]{6,}/<NUM>/g; s/\\\\[A-Za-z0-9._-]+\\/<HOST>\\/g' |
		cut -c1-100
}

placeholder_host() { # UNC 占位名：NAS/FNOS/HOST/SERVER 这类是产品或示例名，不是机器名
	printf '%s' "$1" | grep -qiE '\\\\(nas|fnos|host|server|localhost|example|myhost|computer)[0-9]*\\'
}

report_hit() { # path line rule text
	local p=$1 n=$2 r=$3 t=$4 why i sev=''
	for i in "${!RID[@]}"; do
		[ "${RID[$i]}" = "$r" ] && sev=${RSEV[$i]}
	done
	if [ "$sev" = tip ]; then
		tips=$((tips + 1))
		return
	fi
	if [ "$r" = unc-host ] && placeholder_host "$t"; then
		tips=$((tips + 1))
		return
	fi
	if why=$(allow_reason "$p" "$r"); then
		allowed=$((allowed + 1))
		printf '  ALLOW  [%s] %s:%s  %s\n' "$r" "$p" "$n" "$why"
		return
	fi
	hits=$((hits + 1))
	printf '  FAIL   [%s] %s:%s  「%s」\n' "$r" "$p" "$n" "$(redact "$t")"
}

# ---------------------------------------------------------------- 本机真实值
load_local_conf() {
	[ -f "$LOCAL_CONF" ] || return 0
	local key val
	while IFS='=' read -r key val; do
		case "$key" in '' | '#'*) continue ;; esac
		val=${val%%#*}
		val=$(printf '%s' "$val" | sed -E 's/^[[:space:]]+|[[:space:]]+$//g')
		[ -n "$val" ] || continue
		case "$key" in
		name) add_rule local-name "$val" block '真实姓名属个人信息' ;;
		account) add_rule local-account "$val" block '账号属身份信息' ;;
		host) add_rule local-host "$val" block '机器名会暴露内网命名' ;;
		private_repo) add_rule local-private-repo "$val" block '私有仓库地址不得出现在公开仓' ;;
		esac
	done <"$LOCAL_CONF"
}

# ---------------------------------------------------------------- 路径级规则
internal_doc_paths() {
	[ -f .gitignore ] || return 0
	# 内部文档清单从 .gitignore 读，不在脚本里再抄一份标题
	sed -nE 's#^/(docs/.+\.md)$#\1#p' .gitignore
}

path_rules() { # $1 path
	local p=$1 d
	case "$p" in
	dist/* | */app/bin/* | *.fpk | *.exe | *.dll | *.so | *.dylib | *.db | *.db-wal | *.db-shm | *.sqlite | *.sqlite3 | *.log | *.out | *.test | *.zip | *.7z | *.tar | *.tgz | *.gz)
		report_hit "$p" '-' build-artifact "$p"
		return
		;;
	esac
	for d in $INTERNAL_DOCS; do
		if [ "$p" = "$d" ]; then
			report_hit "$p" '-' internal-doc "$p"
			return
		fi
	done
}

# ---------------------------------------------------------------- 内容扫描
# 内容规则合并成「每条规则一次 grep」：341 文件 × 13 规则逐文件跑会慢到没人愿意执行。
scan_files() {
	local utf8=() f out p rest n body i
	local candidates=()
	for f in "$@"; do
		[ -f "$f" ] || continue
		path_rules "$f"
		case "$f" in
		scripts/check-public-safety.sh | .public-safety-allow) continue ;; # 自查脚本自身含合成样本
		*.go | *.js | *.md | *.html | *.css | *.sh | *.ps1 | *.yml | *.yaml | *.mod | *.sum | *.txt | *.json | *.svg | *.gitignore | *.gitattributes | .gitignore) ;;
		*) continue ;;
		esac
		candidates+=("$f")
	done
	[ ${#candidates[@]} -gt 0 ] || return 0

	# 先一次性批量验证 UTF-8：逐个 iconv 要起几百个进程，光进程开销就十秒级。
	# 只有批量校验失败时才逐文件判断，把慢路径留给真正需要转码的仓库。
	local all_utf8=0
	if iconv -f UTF-8 -t UTF-8 "${candidates[@]}" >/dev/null 2>&1; then
		all_utf8=1
	fi

	for f in "${candidates[@]}"; do
		if [ "$all_utf8" -eq 1 ]; then
			utf8+=("$f")
			continue
		fi
		# 非 UTF-8（GB18030 / UTF-16）先转码：只搜 UTF-8 会整段漏掉中文泄露。
		# 按 BOM 判编码：GB18030 当 UTF-16 硬解会得到合法乱码，从而整段漏扫。
		if ! iconv -f UTF-8 -t UTF-8 "$f" >/dev/null 2>&1; then
			local tmp
			tmp=$(mktemp)
			case "$(head -c 2 "$f" | od -An -tx1 | tr -d ' \n')" in
			fffe | feff) iconv -f UTF-16 -t UTF-8 "$f" >"$tmp" 2>/dev/null ;;
			*) iconv -f GB18030 -t UTF-8 "$f" >"$tmp" 2>/dev/null ;;
			esac
			[ -s "$tmp" ] && scan_one_text "$tmp" "$f"
			rm -f "$tmp"
			continue
		fi
		utf8+=("$f")
	done

	[ ${#utf8[@]} -gt 0 ] || return 0
	for i in "${!RID[@]}"; do
		while IFS= read -r out; do
			[ -n "$out" ] || continue
			p=${out%%:*}
			rest=${out#*:}
			n=${rest%%:*}
			body=${rest#*:}
			report_hit "$p" "$n" "${RID[$i]}" "$body"
		done < <(grep -nHE --binary-files=without-match -e "${RRE[$i]}" "${utf8[@]}" 2>/dev/null)
	done

	# base64 藏内容：只扫 16KB 以下的前 25 个文件，解出来再套规则。
	# 取 blob 用有界长度正则，无界 {12,} 在长 base64 串上回溯很慢。
	local cap=0
	for f in "${utf8[@]}"; do
		[ "$cap" -lt 25 ] || break
		[ -n "$(find "$f" -size -16k -print -quit 2>/dev/null)" ] || continue
		cap=$((cap + 1))
		scan_base64 "$f" "$f"
	done
}

scan_one_text() { # $1 实际文件 $2 报告用路径
	local real=$1 label=$2 i
	for i in "${!RID[@]}"; do
		while IFS= read -r line; do
			[ -n "$line" ] || continue
			local n=${line%%:*}
			local body=${line#*:}
			report_hit "$label" "$n" "${RID[$i]}" "$body"
		done < <(grep -nE -e "${RRE[$i]}" "$real" 2>/dev/null)
	done
	scan_base64 "$real" "$label"
}

# 解码后用一次组合正则判断（命中才逐条细分），避免每个 blob 起十几个进程。
B64_RE='([0-9]{1,3}\.){3}[0-9]{1,3}|[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}|ghp_[A-Za-z0-9]{20,}|:\\\\+(Users|Documents|Desktop)'
scan_base64() {
	local real=$1 label=$2 blob dec i
	while IFS= read -r blob; do
		[ ${#blob} -ge 12 ] || continue
		dec=$(printf '%s' "$blob" | base64 -d 2>/dev/null | tr -d '\000') || continue
		[ -n "$dec" ] || continue
		case "$dec" in
		*[![:print:][:space:]]*) continue ;;
		esac
		[[ $dec =~ $B64_RE ]] || continue
		for i in "${!RID[@]}"; do
			if printf '%s\n' "$dec" | grep -qE -e "${RRE[$i]}" 2>/dev/null; then
				report_hit "$label" 'b64' "${RID[$i]}" "base64 解码后命中"
			fi
		done
	done < <(grep -oE '[A-Za-z0-9+/]{16,64}={0,2}' "$real" 2>/dev/null | head -20)
}

# ---------------------------------------------------------------- 三种范围
tree_files() { git -c core.quotepath=false ls-tree -r --name-only HEAD 2>/dev/null; }
diff_files() {
	{
		git -c core.quotepath=false diff --cached --name-only
		git -c core.quotepath=false diff --name-only
		git -c core.quotepath=false ls-files --others --exclude-standard
	} 2>/dev/null | sort -u
}

run_tree() {
	echo "[公开把关] 仓库树（HEAD，将被公开的文件）"
	local list
	list=$(tree_files)
	if [ -z "$list" ]; then
		echo "  SKIP  无 HEAD"
		return
	fi
	# shellcheck disable=SC2086
	scan_files $list
}

run_diff() {
	echo "[公开把关] 待提交差异（暂存 + 工作区 + 未跟踪）"
	local list
	list=$(diff_files)
	if [ -z "$list" ]; then
		echo "  SKIP  无待提交改动"
		return
	fi
	# shellcheck disable=SC2086
	scan_files $list
}

run_history() {
	echo "[公开把关] 提交历史（--history 显式开启，最多回溯 50 个提交）"
	local c i line p rest n
	while IFS= read -r c; do
		[ -n "$c" ] || continue
		for i in "${!RID[@]}"; do
			local hit
			hit=$(git grep -nE -e "${RRE[$i]}" "$c" 2>/dev/null | head -3)
			[ -n "$hit" ] || continue
			while IFS= read -r line; do
				p=${line%%:*}
				rest=${line#*:}
				n=${rest%%:*}
				report_hit "$p" "$n" "${RID[$i]}:历史" "${rest#*:}"
			done <<<"$hit"
		done
	done < <(git rev-list --all 2>/dev/null | head -50)
}

# ---------------------------------------------------------------- 自测
selftest() {
	echo "[自测] 构造样本，逐类确认能拦住"
	local d
	d=$(mktemp -d) || exit 2

	local A=192 B=168 C=10 D=0
	printf 'server = %s.%s.1.20\nlan = %s.%s.0.7\n' "$A" "$B" "$C" "$D" >"$d/ip.txt"
	printf 'path = %s:\\Users\\someone\\demo\n' "D" >"$d/winpath.txt"
	printf 'home = /%s/alice/work\n' "home" >"$d/homepath.txt"
	printf 'share = \\\\%s\\media\n' "HOSTX" >"$d/unc.txt"
	printf 'mail = %s@%s.com\n' "alice" "example" >"$d/email.txt"
	printf 'tok = %s%s\n' "ghp_" "abcdefghijklmnopqrstuvwxyz0123" >"$d/token.txt"
	printf '账号：%s\n' "123456789" >"$d/acct.txt"
	printf 'x = "%s." + "%s.1.1"\n' "$A" "$B" >"$d/split.txt"
	# 用通用占位内网地址，避免把维护者真实 IP 写进仓库（自测仍需命中 ip-private 规则）
	printf '// 旧部署在 %s.%s.1.100 上\n' "$A" "$B" >"$d/comment.txt"
	printf '{"k":"%s"}\n' "$(printf '%s.%s.1.90' "$A" "$B" | base64 -w0)" >"$d/b64.txt"
	printf 'key 放在 %s 里\n' "id_rsa" >"$d/credfile.txt"
	printf '账号：%s\n' "123456789" | iconv -f UTF-8 -t GB18030 >"$d/gb18030.txt" 2>/dev/null || : >"$d/gb18030.txt"
	printf 'see /vol1/1000/movies and _外置/产物/nothing\n' >"$d/clean.txt"

	local cases="ip.txt:ip-private winpath.txt:path-abs-win homepath.txt:path-home unc.txt:unc-host email.txt:email token.txt:secret acct.txt:acct-context split.txt:ip-split comment.txt:ip-private b64.txt:ip-private credfile.txt:cred-file gb18030.txt:acct-context"
	local c path want got bad=0
	for c in $cases; do
		path=${c%%:*}
		want=${c##*:}
		got=$(scan_files "$d/$path" 2>&1 | grep -oE "\[(ip-private|path-abs-win|path-home|unc-host|email|secret|acct-context|ip-split|cred-file)\]" | tr -d '[]' | sort -u | tr '\n' ' ')
		if printf '%s' "$got" | grep -q "$want"; then
			printf '  PASS   %-14s 命中 %s\n' "$path" "$want"
		else
			printf '  FAIL   %-14s 期望 %s，实际 %s\n' "$path" "$want" "${got:-无}"
			bad=$((bad + 1))
		fi
	done
	if scan_files "$d/clean.txt" 2>&1 | grep -q 'FAIL'; then
		echo "  FAIL   clean.txt 被误拦（布局字样不应拦截）"
		bad=$((bad + 1))
	else
		echo "  PASS   clean.txt 未误拦（布局字样只提示）"
	fi

	if [ "$bad" -eq 0 ]; then
		echo "自测：全部通过"
	else
		echo "自测：$bad 项失败"
	fi
	hits=$bad
}

# ---------------------------------------------------------------- 主流程
INTERNAL_DOCS=$(internal_doc_paths | tr '\n' ' ')
load_local_conf

if [ "$DO_SELFTEST" -eq 1 ]; then
	selftest
	printf '\n公开把关：自测 %d 项失败\n' "$hits"
	[ "$hits" -eq 0 ] || exit 1
	exit 0
fi

[ "$DO_TREE" -eq 1 ] && run_tree
[ "$DO_DIFF" -eq 1 ] && run_diff
[ "$DO_HIST" -eq 1 ] && run_history

if [ "$hits" -gt 0 ]; then
	printf '\n公开把关：%d 处必须处理，%d 处已放行，%d 条提示\n' "$hits" "$allowed" "$tips"
	printf '放行：.public-safety-allow 增加一行（glob<TAB>规则<TAB>原因）；历史用 --history，看原文用 --show\n'
	exit 1
fi
printf '\n公开把关：0 处命中，%d 处已放行，%d 条提示\n' "$allowed" "$tips"
