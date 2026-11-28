#!/usr/bin/env bash
# 构建 Linux amd64 静态二进制并打包 fpk。
# 产物统一输出到 dist/，避免在仓库根目录留下 verify.fpk。
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v fnpack >/dev/null 2>&1; then
	echo "未找到 fnpack，请先安装并加入 PATH：https://developer.fnnas.com/docs/cli/fnpack/" >&2
	exit 1
fi

# 版本号硬编码在 manifest 与 internal/web/server.go 两处。
# v3.10.9：构建前先核对，漏同步时会产出「文件名版本 ≠ /api/status 版本」的包，
# 只有装到真机上才会发现，浪费一整轮验证。
VERSION=$(sed -n 's/^version=//p' packaging/fpk/manifest | head -1 | tr -d '[:space:]')
GO_VERSION=$(sed -n 's/^const Version = "\(.*\)"$/\1/p' internal/web/server.go | head -1 | tr -d '[:space:]')
if [ -z "$VERSION" ] || [ -z "$GO_VERSION" ]; then
	echo "无法读取版本号（manifest=$VERSION, server.go=$GO_VERSION）" >&2
	exit 1
fi
if [ "$VERSION" != "$GO_VERSION" ]; then
	echo "版本号不一致：manifest=$VERSION，internal/web/server.go=$GO_VERSION，请先同步" >&2
	exit 1
fi

# 发版前门禁（默认执行）：检查器默认取仓库上一级的 _tool/spec-check/check-spec.ps1。
# 为什么默认开启：漏跑门禁是静默失败，代价比多一条 WARN 高得多。
# 为什么找不到时只告警：该检查器只存在于维护者的工作区，公开克隆下不保证存在，
# 硬阻断会让外部构建完全跑不起来。
# 为什么放在 go build 之前：构建一轮要几分钟，规范问题应在动手编译前就暴露。
ROOT="$(pwd)"
SPEC_CHECK="${SPEC_CHECK:-$(dirname "$ROOT")/_tool/spec-check/check-spec.ps1}"
if [ -f "$SPEC_CHECK" ]; then
	if command -v pwsh >/dev/null 2>&1; then
		if ! pwsh -NoProfile -File "$SPEC_CHECK" -Project "$ROOT" -Name "Verify查重助手" -Target "$VERSION" -IssuesOnly; then
			echo "FAIL: 规范检查未通过（见上方 FAIL 项），本次发版中止" >&2
			exit 1
		fi
	else
		echo "WARN: 找到规范检查器但缺少 pwsh，已跳过：$SPEC_CHECK（可用 SPEC_CHECK 覆盖）" >&2
	fi
else
	echo "WARN: 未找到规范检查器，已跳过：期望 $SPEC_CHECK（可用 SPEC_CHECK 环境变量覆盖）" >&2
fi

# -buildvcs=false：不把提交号写进二进制。大版本流程是「先装真机验证、后提交发布」，
# 这样发布前安装的包与 tag 附件能保持同一哈希。
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags "-s -w" \
	-o packaging/fpk/app/bin/verify .

# 同时构建 verifyctl CLI 进包：平台/静态参数与主二进制一致，版本号用 -X main.version 注入
# （CLI 的 version 是独立 var，不改动主程序 5 处版本号）。放进 app/bin 与 verify 并排。
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath \
	-ldflags "-s -w -X main.version=$VERSION" \
	-o packaging/fpk/app/bin/verifyctl ./cmd/verifyctl

mkdir -p dist
# fnpack 只能把 fpk 输出到当前工作目录，因此从 dist/ 调用。
cd dist
# fnpack 1.3.3 起默认要求开发者证书签名；无证书时打包会直接失败。
# 本地迭代没有证书，显式 --nosign 才能保证构建跑通；拿到飞牛官方证书并
# `fnpack cert import` 后，去掉 --nosign 即为正式上架包。
# 注意 --nosign 不能和 --key/--cert 同时用，所以这里只能二选一。
# v4.4.0：本机装的是 fnpack 1.2.3，还不认识 --nosign（传了会直接报未知参数）。
# 按版本探测一下，旧版不带该参数，新版才带，避免「换台机器就打不出包」。
if fnpack build --help 2>&1 | grep -q -- '--nosign'; then
	fnpack build --nosign -d ../packaging/fpk
else
	fnpack build -d ../packaging/fpk
fi
cd ..

# 安装包文件名必须带版本号：dist/ 里会并存多版产物，同名 verify.fpk 无法区分新旧。
mv -f dist/verify.fpk "dist/verify-$VERSION.fpk"

# 校验文件与包并排（规范 5.6），在 dist/ 内生成以保持内容是「哈希 + 纯文件名」，
# 这样把两件一起拷到别处后 sha256sum -c 仍然有效。
# 写不出来就判本次打包失败：缺校验值的包不能算发版完成。
cd dist
sha256sum "verify-$VERSION.fpk" > "verify-$VERSION.fpk.sha256"
cd ..
if [ ! -s "dist/verify-$VERSION.fpk.sha256" ]; then
	echo "校验文件未写出：dist/verify-$VERSION.fpk.sha256" >&2
	exit 1
fi

echo "已生成：dist/verify-$VERSION.fpk"
echo "校验值：dist/verify-$VERSION.fpk.sha256"

# 归档（默认执行，但只在归档根已存在时动手）：默认根取仓库上一级的
# _外置/产物/Verify查重助手，目录名沿用历史的 release-v<版本>。
# 为什么默认开启：归档漏做的话，已删 Release 的本地副本就断了，事后补不回来。
# 为什么缺目录只提示不 mkdir：该目录属于维护者的本机布局，公开克隆下一旦无条件
# mkdir，就会在别人的磁盘上凭空造目录树并塞进一个包。
# 为什么必须成对：.sha256 属于交付件的一部分，只搬包会留下「有包无校验」的形态，
# 回滚时无法确认文件完整性。
RELEASE_ARCHIVE_ROOT="${RELEASE_ARCHIVE_ROOT:-$(dirname "$ROOT")/_外置/产物/Verify查重助手}"
if [ -d "$RELEASE_ARCHIVE_ROOT" ]; then
	ARCH_DIR="$RELEASE_ARCHIVE_ROOT/release-v$VERSION"
	ARCH_FPK="$ARCH_DIR/verify-$VERSION.fpk"
	ARCH_SUM="$ARCH_FPK.sha256"
	mkdir -p "$ARCH_DIR"

	# 已归档的同名件不覆盖：归档目录是权威副本，覆盖会让它与已发布 Release 的附件对不上。
	# 需要重建同一版本时先确认版本号，或人工决定改名方式。
	if [ -e "$ARCH_FPK" ] || [ -e "$ARCH_SUM" ]; then
		echo "归档目标已存在，拒绝覆盖：$ARCH_DIR" >&2
		exit 1
	fi

	cp -f "dist/verify-$VERSION.fpk" "$ARCH_FPK"
	cp -f "dist/verify-$VERSION.fpk.sha256" "$ARCH_SUM"

	# 落地核对：两件缺一即失败；再在归档目录里就地跑一次 sha256sum -c，
	# 确认搬动后文件名没被改写、校验值仍与包匹配。
	if [ ! -s "$ARCH_FPK" ] || [ ! -s "$ARCH_SUM" ]; then
		echo "归档不完整，包与 .sha256 必须同时存在：$ARCH_DIR" >&2
		exit 1
	fi
	if ! (cd "$ARCH_DIR" && sha256sum -c "verify-$VERSION.fpk.sha256"); then
		echo "归档校验不通过：$ARCH_SUM" >&2
		exit 1
	fi

	echo "已归档：$ARCH_FPK"
	echo "已归档：$ARCH_SUM"
else
	echo "INFO: 未检测到归档根，已跳过归档：$RELEASE_ARCHIVE_ROOT（dist 内产物已生成；可用 RELEASE_ARCHIVE_ROOT 覆盖）"
fi
