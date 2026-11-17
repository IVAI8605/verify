#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""从 Go 的响应契约 struct 生成前端 TypeScript 类型声明。

为什么要有这个工具：
  前后端字段名要有「单一真相源」。若前端 interface 与 Go 的 json tag 分头手写，
  迟早会对不上——而对不上的后果很隐蔽：后端改了字段名，Go 编译通过（struct 是有类型的，
  但前端不知道），前端字段拼错又被 `|| []` 兜底，页面显示空数据、不报错。

做法：
  1. 解析 internal/web/contracts*.go 里的 struct 定义，拿到 Go 字段名 + json tag；
  2. 按 Go 类型映射成 TS 类型（string/number/boolean/any/数组/结构体）；
  3. 生成 webtypes/types.d.ts；
  4. 附上「后端 struct ↔ 前端 interface」对照表，便于双向核对。

类型映射规则：
  Go string            → TS string
  Go int/int64/uint... → TS number
  Go bool              → TS boolean
  Go any               → TS any
  Go []T               → TS T[]
  Go struct（已知名）  → TS 展开的 interface
  其它（指针/复杂）     → TS any

用法：
    python3 tools/gen_types.py            # 生成到 webtypes/types.d.ts
    python3 tools/gen_types.py --check    # 只校验，不写入（CI 用）
"""
import re
import sys
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
WEB = ROOT / "internal" / "web"
OUT = ROOT / "webtypes" / "types.d.ts"

HEADER = """// 前端类型声明（v4.0.0 自动生成，请勿手改）
//
// 本文件由 tools/gen_types.py 从 internal/web/contracts*.go 与 internal/store/*.go
// 的响应契约生成，源头是 Go struct 的 json tag —— 那是前后端字段名的唯一真相源。
//
// 重新生成：
//     python3 tools/gen_types.py
//
// 约束：
//   - 本文件不产出任何运行时代码，只做类型标注；
//   - 产物仍是 ui/*.js 原生脚本，由 go:embed 内嵌，零构建形态不变；
//   - 改后端响应字段 → 改 Go struct 的 json tag → 重新生成本文件。
//
// 已知噪音：TS2339（属性不存在于 HTMLElement/Element）不阻断类型门禁，
// 因为 getElementById 的返回类型没有收窄到具体元素类型。

// ── 基础别名 ────────────────────────────────────────────────────────────

/** 后端下发的通用错误响应。 */
interface ApiError {
  error: string;
}

/** 后端下发的简单成功响应。 */
interface ApiOk {
  ok: boolean;
}
"""

# 除 contracts*.go 外，还要从这些文件里取类型（前端直接消费它们的 JSON 形态）
EXTRA_SOURCES = [
    ("internal/store/exact.go", ["Group", "GroupQuery"]),
    ("internal/store/store.go", ["FileRow", "MediaMeta", "TagValue", "TrashRow",
                                 "VideoTag", "ImportSession", "ImportGroup",
                                 "RenameBatchItem"]),
]

# 这些类型来自 store，生成时加 Store 前缀，避免与前端手写的类型重名
EXTRA_NAMES = set()
for _rel, _names in EXTRA_SOURCES:
    EXTRA_NAMES.update(_names)


def ts_name_of(go_name):
    """Go struct 名 → TS interface 名。

    Go 侧的响应 struct 都是小写（包内私有），直接搬到 TS 会让前端类型
    看起来像内部实现。这里统一转成 PascalCase，并加 Resp 后缀标明它是响应体。
    例：groupsResponse → GroupsResp，errorResponse → ErrorResp。
    """
    if go_name in EXTRA_NAMES:
        return "Store" + go_name
    if go_name.endswith("Response"):
        base = go_name[:-len("Response")]
        return base[0].upper() + base[1:] + "Resp"
    return go_name[0].upper() + go_name[1:]


def parse_go_types():
    """解析 contracts*.go 与 store 的相关 struct。

    返回 {struct名: [(go字段, json键, go类型, omitempty)]}。
    """
    defs = {}
    sources = list(WEB.glob("contracts*.go"))
    for rel, names in EXTRA_SOURCES:
        p = ROOT / rel
        if p.exists():
            sources.append(p)
    for p in sources:
        if p.name.endswith("_test.go"):
            continue
        src = p.read_text(encoding="utf-8")
        # 去掉行注释与块注释，避免把注释里的 struct 误读
        src = re.sub(r'/\*.*?\*/', '', src, flags=re.S)
        src = re.sub(r'//[^\n]*', '', src)
        for m in re.finditer(r'type\s+(\w+)\s+struct\s*\{(.*?)\n\}', src, re.S):
            name = m.group(1)
            body = m.group(2)
            fields = []
            for line in body.split('\n'):
                fm = re.match(
                    r'\s*(\w+)\s+([\w\[\]\*\.]+)\s+`json:"([^"]+)"`', line)
                if not fm:
                    continue
                go_field, go_type, tag = fm.group(1), fm.group(2), fm.group(3)
                parts = tag.split(',')
                json_key = parts[0]
                omitempty = 'omitempty' in parts
                fields.append((go_field, json_key, go_type, omitempty))
            if fields:
                defs[name] = fields
    return defs


def ts_type(go_type, defs, seen=None):
    """把 Go 类型映射成 TS 类型。"""
    if seen is None:
        seen = set()
    go_type = go_type.strip()
    if go_type == 'string':
        return 'string'
    if go_type in ('bool',):
        return 'boolean'
    if re.match(r'^(u?int(8|16|32|64)?|float(32|64)?)$', go_type):
        return 'number'
    if go_type == 'any':
        return 'any'
    if go_type.startswith('[]'):
        inner = go_type[2:]
        if inner in defs and inner not in seen:
            return expand_struct(inner, defs, seen | {inner})
        return ts_type(inner, defs, seen)
    if go_type.startswith('*'):
        return 'any'
    if go_type in defs and go_type not in seen:
        return expand_struct(go_type, defs, seen | {go_type})
    return 'any'


def expand_struct(name, defs, seen):
    """把 struct 展开成内联对象类型。"""
    parts = []
    for go_field, json_key, go_type, omitempty in defs.get(name, []):
        t = ts_type(go_type, defs, seen)
        opt = '?' if omitempty else ''
        parts.append(f"{json_key}{opt}: {t}")
    if not parts:
        return 'any'
    return "{ " + "; ".join(parts) + " }"


def main():
    check = "--check" in sys.argv
    defs = parse_go_types()
    if not defs:
        print("❌ 未解析到任何 struct")
        return 1

    lines = [HEADER]
    lines.append("\n// ── 后端响应契约（由 contracts*.go 生成）──────────────────────────────\n")
    for name in sorted(defs):
        if name in EXTRA_NAMES:
            continue
        fields = defs[name]
        ts_name = ts_name_of(name)
        lines.append(f"/** 后端 {name}（{len(fields)} 个字段）。 */")
        lines.append(f"interface {ts_name} {{")
        for go_field, json_key, go_type, omitempty in fields:
            t = ts_type(go_type, defs)
            opt = '?' if omitempty else ''
            lines.append(f"  {json_key}{opt}: {t};")
        lines.append("}")
        lines.append("")

    # store 里的类型用 Store 前缀，避免与前端手写的类型重名
    lines.append("\n// ── 数据行类型（由 internal/store 生成）──────────────────────────────\n")
    for name in sorted(defs):
        if name not in EXTRA_NAMES:
            continue
        fields = defs[name]
        lines.append(f"/** 后端 store.{name}（{len(fields)} 个字段）。 */")
        lines.append(f"interface Store{name} {{")
        for go_field, json_key, go_type, omitempty in fields:
            t = ts_type(go_type, defs)
            opt = '?' if omitempty else ''
            lines.append(f"  {json_key}{opt}: {t};")
        lines.append("}")
        lines.append("")

    content = "\n".join(lines)

    if check:
        if not OUT.exists():
            print(f"❌ {OUT} 不存在")
            return 1
        cur = OUT.read_text(encoding='utf-8')
        if cur.strip() == content.strip():
            print(f"✅ {OUT} 与后端契约一致（{len(defs)} 个 struct）")
            return 0
        print(f"❌ {OUT} 与后端契约不一致，请运行 python3 tools/gen_types.py")
        return 2

    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text(content, encoding='utf-8')
    print(f"✅ 已生成 {OUT.relative_to(ROOT)}（{len(defs)} 个 struct）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
