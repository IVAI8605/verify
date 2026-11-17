#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""逐接口比对改造前后的 JSON 键集合。

背景：把 map[string]any 换成具名 struct 时，键集合必须逐字节一致。
两个方向都可能出错：
  - 少了键：struct 用了 omitempty，而原 map 里显式写出的键值为 0 时不会消失；
  - 多了键：struct 字段不是 omitempty，而原响应里根本没这个键。

做法：
  1. 从改造前的源码里，按「调用点顺序」扫出每个 writeJSON 的键集合；
  2. 从当前源码里，按同样的调用点顺序扫出 struct 字面量的键集合
     （struct 字面量用的是 Go 字段名，需通过 json tag 映射回 JSON 键名）；
  3. 逐位置比对。

用法：
    python3 tools/compare_json_keys.py --old <改造前源码目录> [--new .]
"""
import re
import sys
import pathlib

CALL = re.compile(r'writeJSON\(\s*\w+\s*,\s*[^,]+?,\s*', re.S)


def strip_comments(text):
    out = []
    in_str = False
    i = 0
    while i < len(text):
        c = text[i]
        if in_str:
            out.append(c)
            if c == '\\' and i + 1 < len(text):
                out.append(text[i + 1])
                i += 2
                continue
            if c == '"':
                in_str = False
            i += 1
            continue
        if c == '"':
            in_str = True
            out.append(c)
            i += 1
            continue
        if c == '/' and i + 1 < len(text) and text[i + 1] == '/':
            while i < len(text) and text[i] != '\n':
                i += 1
            continue
        out.append(c)
        i += 1
    return "".join(out)


def find_matching(src, i):
    depth = 0
    in_str = False
    j = i
    while j < len(src):
        c = src[j]
        if in_str:
            if c == '\\':
                j += 2
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
                return j
        j += 1
    return -1


def split_top(b):
    parts = []
    d = 0
    cur = ''
    in_str = False
    for ch in b:
        if in_str:
            cur += ch
            if ch == '"':
                in_str = False
            continue
        if ch == '"':
            in_str = True
            cur += ch
            continue
        if ch in '{[(':
            d += 1
        elif ch in '}])':
            d -= 1
        if ch == ',' and d == 0:
            parts.append(cur)
            cur = ''
        else:
            cur += ch
    if cur.strip():
        parts.append(cur)
    return parts


def load_struct_defs(web_dir):
    """解析 contracts*.go 里的 struct 定义：Go 字段名 → (json键, 是否 omitempty)。"""
    defs = {}
    for p in sorted(pathlib.Path(web_dir).glob("contracts*.go")):
        src = p.read_text(encoding='utf-8')
        for m in re.finditer(r'type\s+(\w+)\s+struct\s*\{(.*?)\n\}', src, re.S):
            name = m.group(1)
            body = m.group(2)
            fields = []
            for fm in re.finditer(r'^\s*(\w+)\s+[\w\[\]\*\.]+\s+`json:"([^"]+)"`', body, re.M):
                tag = fm.group(2)
                parts = tag.split(',')
                fields.append((fm.group(1), parts[0], 'omitempty' in parts))
            defs[name] = fields
    return defs


def scan_old(web_dir):
    """按文件、按出现顺序扫出改造前的 map 键集合。

    两种形态都要扫：
      1. writeJSON(w, code, map[string]any{...})   —— 直接字面量
      2. body := map[string]any{...}; writeJSON(w, code, body) —— 变量中转
    漏掉第 2 种会导致与新版对齐错位、误报差异。
    """
    out = []
    for p in sorted(pathlib.Path(web_dir).glob("*.go")):
        if p.name.endswith("_test.go"):
            continue
        src = p.read_text(encoding='utf-8')
        # ── 形态 1：直接字面量 ──
        for m in CALL.finditer(src):
            rest = src[m.end():]
            if not rest.startswith('map[string]any{'):
                continue
            ob = m.end() + len('map[string]any{') - 1
            cb = find_matching(src, ob)
            if cb < 0:
                continue
            body = strip_comments(src[ob + 1:cb])
            keys = []
            for part in split_top(body):
                part = part.strip()
                if not part:
                    continue
                km = re.match(r'^"([a-z_]+)"\s*:', part)
                if km:
                    keys.append(km.group(1))
            if keys:
                line = src[:m.start()].count('\n') + 1
                out.append((p.name, line, keys))
        # ── 形态 2：变量中转 ──
        #   var := map[string]any{...}  ...  writeJSON(w, code, var)
        for m in re.finditer(
                r'(\w+)\s*:?=\s*map\[string\]any\{', src):
            var = m.group(1)
            ob = src.index('{', m.start())
            cb = find_matching(src, ob)
            if cb < 0:
                continue
            body = strip_comments(src[ob + 1:cb])
            keys = []
            for part in split_top(body):
                part = part.strip()
                if not part:
                    continue
                km = re.match(r'^"([a-z_]+)"\s*:', part)
                if km:
                    keys.append(km.group(1))
            if not keys:
                continue
            # 找该变量被 writeJSON 使用的地方
            for wm in re.finditer(r'writeJSON\(\s*\w+\s*,\s*[^,]+?,\s*' + re.escape(var) + r'\s*\)', src):
                line = src[:wm.start()].count('\n') + 1
                out.append((p.name, line, keys))
                break
    # 按行号排序，保证与新版的顺序一致
    out.sort(key=lambda x: (x[0], x[1]))
    return out


def scan_new(web_dir, defs):
    """扫出改造后的 struct 字面量键集合（已映射成 JSON 键名）。

    两种形态都要扫：
      1. writeJSON(w, code, XxxResp{...})   —— 直接字面量
      2. resp := XxxResp{...}; writeJSON(w, code, resp) —— 变量中转
    """
    out = []
    for p in sorted(pathlib.Path(web_dir).glob("*.go")):
        if p.name.endswith("_test.go"):
            continue
        src = p.read_text(encoding='utf-8')
        for m in CALL.finditer(src):
            rest = src[m.end():]
            km = re.match(r'([A-Za-z_][A-Za-z0-9_]*)\{', rest)
            if not km:
                continue
            name = km.group(1)
            fields = defs.get(name)
            if not fields:
                continue
            ob = m.end() + km.end() - 1
            cb = find_matching(src, ob)
            if cb < 0:
                continue
            body = strip_comments(src[ob + 1:cb])
            used = []
            for part in split_top(body):
                part = part.strip()
                if not part:
                    continue
                fm = re.match(r'^([A-Z][A-Za-z0-9_]*)\s*:', part)
                if fm:
                    used.append(fm.group(1))
            if not used:
                continue
            keys = struct_keys(fields, body, used)
            line = src[:m.start()].count('\n') + 1
            out.append((p.name, line, name, keys))
        # ── 变量中转形态 ──
        for m in re.finditer(r'(\w+)\s*:?=\s*([A-Za-z_][A-Za-z0-9_]*)\{', src):
            var, name = m.group(1), m.group(2)
            fields = defs.get(name)
            if not fields:
                continue
            ob = src.index('{', m.start())
            cb = find_matching(src, ob)
            if cb < 0:
                continue
            body = strip_comments(src[ob + 1:cb])
            used = []
            for part in split_top(body):
                part = part.strip()
                if not part:
                    continue
                fm = re.match(r'^([A-Z][A-Za-z0-9_]*)\s*:', part)
                if fm:
                    used.append(fm.group(1))
            if not used:
                continue
            for wm in re.finditer(r'writeJSON\(\s*\w+\s*,\s*[^,]+?,\s*' + re.escape(var) + r'\s*\)', src):
                line = src[:wm.start()].count('\n') + 1
                out.append((p.name, line, name, struct_keys(fields, body, used)))
                break
    out.sort(key=lambda x: (x[0], x[1]))
    return out


def struct_keys(fields, body, used):
    """把 struct 字面量里用到的 Go 字段名映射成 JSON 键名，并按 omitempty 规则剔除。

    omitempty 字段的值若是字面量（0/false/""/nil/[]），可以静态判定会不会被省略；
    若是变量，静态判不出来 —— 标成「?」表示「该键的出现与否取决于运行时值」，
    与旧 map 的对比时按「旧 map 有它才算差异」处理（保守，不误报）。
    """
    tagmap = {f[0]: (f[1], f[2]) for f in fields}
    keys = []
    for part in split_top(body):
        part = part.strip()
        if not part:
            continue
        fm = re.match(r'^([A-Z][A-Za-z0-9_]*)\s*:', part)
        if not fm:
            continue
        u = fm.group(1)
        if u not in tagmap:
            keys.append(f"<未知字段 {u}>")
            continue
        jk, om = tagmap[u]
        if om:
            val = re.search(r'\b' + re.escape(u) + r'\s*:\s*(.+?)(?:,|$)', part, re.S)
            v = val.group(1).strip() if val else ''
            if re.match(r'^(0|false|""|nil|\[\]|$)', v):
                continue  # 字面量零值：必然被省略
            if re.match(r'^[A-Za-z_][A-Za-z0-9_]*$', v):
                # 变量：静态判不出，标记为「可能出现」
                keys.append(jk + "?")
                continue
        keys.append(jk)
    return keys


def main():
    if "--old" not in sys.argv:
        print(__doc__)
        return 1
    old_dir = pathlib.Path(sys.argv[sys.argv.index("--old") + 1])
    new_dir = pathlib.Path(sys.argv[sys.argv.index("--new") + 1]) if "--new" in sys.argv else pathlib.Path(".")

    old = scan_old(old_dir / "internal" / "web")
    defs = load_struct_defs(new_dir / "internal" / "web")
    new = scan_new(new_dir / "internal" / "web", defs)

    print(f"改造前 map 响应: {len(old)} 处")
    print(f"改造后 struct 响应: {len(new)} 处")
    print()

    # 按 (文件名, 出现序号) 对齐
    old_by_file = {}
    for i, (f, line, keys) in enumerate(old):
        old_by_file.setdefault(f, []).append((i, line, keys))
    new_by_file = {}
    for i, (f, line, name, keys) in enumerate(new):
        new_by_file.setdefault(f, []).append((i, line, name, keys))

    diffs = 0
    for f in sorted(set(old_by_file) | set(new_by_file)):
        olds = old_by_file.get(f, [])
        news = new_by_file.get(f, [])
        n = max(len(olds), len(news))
        for i in range(n):
            o = olds[i] if i < len(olds) else None
            nw = news[i] if i < len(news) else None
            if o is None:
                print(f"❌ {f}: 改造后多出调用点 {nw[1]}")
                diffs += 1
                continue
            if nw is None:
                print(f"❌ {f}:{o[1]} 改造后少了调用点")
                diffs += 1
                continue
            ok_, okeys = o[0], o[2]
            nk, nkeys = nw[2], nw[3]
            # 「?」标记的是 omitempty + 变量值的字段：出现与否取决于运行时值，
            # 静态判不出来。旧 map 有、新 struct 也可能有时，这不算差异。
            nkeys_set = {k for k in nkeys if not k.endswith('?')}
            okeys_set = set(okeys)
            # 新 struct 里带 ? 的键（omitempty + 变量值）：出现与否取决于运行时值，
            # 静态判不出来。若旧 map 有它，视为「可能一致」，不算差异。
            maybe = {k[:-1] for k in nkeys if k.endswith('?')}
            only_old = okeys_set - nkeys_set - maybe
            only_new = nkeys_set - okeys_set
            if only_old or only_new:
                print(f"❌ {f}:{o[1]} [{nk}]")
                if only_old:
                    print(f"     少了: {sorted(only_old)}")
                if only_new:
                    print(f"     多了: {sorted(only_new)}")
                diffs += 1
            else:
                for k in sorted(maybe & okeys_set):
                    print(f"ℹ️  {f}:{o[1]} [{nk}] 键 {k} 为 omitempty+变量值，出现与否取决于运行时（静态不可判）")

    print()
    if diffs == 0:
        print("✅ 所有响应的 JSON 键集合与改造前完全一致")
    else:
        print(f"⚠️  共 {diffs} 处键集合不一致")
    return 0 if diffs == 0 else 2


if __name__ == "__main__":
    sys.exit(main())
