#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""从后端路由表推导「路由 → 响应类型」映射。

做法：
  1. 扫 internal/web/server.go 拿到每个路由注册的 handler 名；
  2. 在对应文件里找到该 handler 函数的 writeJSON 调用，取出它用的 struct 名；
  3. 输出「路由 → struct」清单，供 annotate_api_types.py 使用。

注意：一个 handler 可能有多个 writeJSON（不同分支）。
这类情况取「成功响应」的 struct —— 通常是最后一个非 errorResponse 的调用。
errorResponse / authErrorResponse 等错误分支统一映射到 ErrorResp。

用法：
    python3 tools/map_routes.py
"""
import re
import sys
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
WEB = ROOT / "internal" / "web"

# 错误响应类型：这些不作为「成功响应」
ERROR_TYPES = {
    "errorResponse", "authErrorResponse", "countErrorResponse",
    "searchNotReadyResponse",
}

ROUTE_RE = re.compile(
    r'mux\.HandleFunc\("([^"]+)",\s*(?:s\.auth\(s\.(\w+)\)|s\.(\w+))\)')
CALL_RE = re.compile(r'writeJSON\(\s*\w+\s*,\s*[^,]+?,\s*([A-Za-z_][A-Za-z0-9_]*)\{')
MAP_RE = re.compile(r'(\w+)\s*:?=\s*([A-Za-z_][A-Za-z0-9_]*)\{')


def handler_bodies():
    """收集每个 handler 函数的源码片段。"""
    bodies = {}
    for p in sorted(WEB.glob("*.go")):
        if p.name.endswith("_test.go"):
            continue
        src = p.read_text(encoding='utf-8')
        for m in re.finditer(r'func\s+\(s\s+\*Server\)\s+(\w+)\s*\(', src):
            name = m.group(1)
            start = src.index('{', m.end())
            depth = 0
            i = start
            in_str = False
            while i < len(src):
                c = src[i]
                if in_str:
                    if c == '\\':
                        i += 2
                        continue
                    if c == '"':
                        in_str = False
                elif c == '"':
                    in_str = True
                elif c == '{':
                    depth += 1
                elif c == '}':
                    depth -= 1
                    if depth == 0:
                        break
                i += 1
            bodies[name] = src[start:i + 1]
    return bodies


def main():
    bodies = handler_bodies()
    server = (WEB / "server.go").read_text(encoding='utf-8')

    mapping = {}
    for m in ROUTE_RE.finditer(server):
        route = m.group(1)
        handler = m.group(2) or m.group(3)
        body = bodies.get(handler)
        if not body:
            continue
        # 收集该 handler 里用到的 struct
        types = []
        for cm in CALL_RE.finditer(body):
            t = cm.group(1)
            if t not in types:
                types.append(t)
        # 变量中转形式
        for vm in MAP_RE.finditer(body):
            t = vm.group(2)
            if t not in types:
                types.append(t)
        # 取「成功响应」：最后一个不在 ERROR_TYPES 里的
        success = None
        for t in types:
            if t not in ERROR_TYPES:
                success = t
        if success is None:
            success = "ErrorResp" if types else None
        if success:
            mapping[route] = success

    print(f"# 路由 → 响应类型（共 {len(mapping)} 条）")
    for route in sorted(mapping):
        print(f"    (\"{route}\", \"{mapping[route]}\"),")
    return 0


if __name__ == "__main__":
    sys.exit(main())
