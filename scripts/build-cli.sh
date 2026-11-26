#!/usr/bin/env bash
# 构建 verifyctl CLI 的 Linux amd64 静态二进制，并把校验值落在 dist/。
# 版本号从 manifest 读取，与主程序同源；产物固定为 dist/verifyctl-<版本>。
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION=$(sed -n 's/^version=//p' packaging/fpk/manifest | head -1 | tr -d '[:space:]')
if [ -z "$VERSION" ]; then
	echo "无法从 packaging/fpk/manifest 读取版本号" >&2
	exit 1
fi

mkdir -p dist

# main 包的链接符号名是 main.version，构建期注入与 manifest 同源的版本号。
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath \
	-ldflags "-s -w -X main.version=$VERSION" \
	-o "dist/verifyctl-$VERSION" ./cmd/verifyctl

# 在 dist/ 内生成校验值，内容保持「哈希 + 纯文件名」，便于 sha256sum -c 就地复核。
(cd dist && sha256sum "verifyctl-$VERSION" > "verifyctl-$VERSION.sha256")
if [ ! -s "dist/verifyctl-$VERSION.sha256" ]; then
	echo "校验文件未写出：dist/verifyctl-$VERSION.sha256" >&2
	exit 1
fi

echo "已生成：dist/verifyctl-$VERSION"
echo "校验值：dist/verifyctl-$VERSION.sha256"
