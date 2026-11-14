#!/bin/bash
# 一次性脚本：给全仓 .go 源文件加版权声明（v1.19 改名收尾，规划 §3 F/§9.4）。
# 已有版权/SPDX 注释的文件跳过；新注释插在文件最顶部。
set -e
YEAR_END=$(date +%Y)
HEADER="// Copyright (c) 2024-${YEAR_END} IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT"
count=0
while IFS= read -r f; do
  if head -5 "$f" | grep -q "Copyright\|SPDX"; then
    continue
  fi
  printf '%s\n\n%s' "$HEADER" "$(cat "$f")" > "$f.tmp"
  mv "$f.tmp" "$f"
  count=$((count+1))
done < <(find . -name "*.go" -not -path "./.git/*" -not -path "./cache/*")
echo "已添加版权头：$count 个文件"
