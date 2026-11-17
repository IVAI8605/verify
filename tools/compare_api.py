#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Verify v4.0.0 重构的接口响应回归对比工具。

用途：改造前后各起一个实例，逐接口抓取响应并对比，
确保「响应结构改造」没有改变任何对外可见的行为。

做法：
  1. 用同一个二进制起两个实例（旧版备份 vs 当前工作区），分别用不同数据目录；
  2. 逐个抓 GET 接口的响应；
  3. 用 JSON 规范化（键排序）后比对哈希，键顺序差异不算失败。

用法：
    python3 tools/compare_api.py --old /path/to/old-binary --new /path/to/new-binary
"""
import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

OLD_PORT = 18990
NEW_PORT = 18991

GET_APIS = [
    "/api/status",
    "/api/tasks",
    "/api/groups?page=1&page_size=20",
    "/api/groups/video?page=1&page_size=20",
    "/api/groups/fuzzy?near=a",
    "/api/groups/semantic?page=1",
    "/api/groups/photo",
    "/api/groups/folder",
    "/api/groups/audio",
    "/api/groups/intro?n=5",
    "/api/health/status",
    "/api/health/results?kind=ext",
    "/api/video_tags",
    "/api/cache/stats",
    "/api/cleanup/plans",
    "/api/import/sessions",
    "/api/sync/peers",
    "/api/transcode/status",
    "/api/transcode/residue",
    "/api/tasks",
    "/api/rule-search/status",
    "/api/rule-search/facets",
    "/api/rule-search/results?q=x",
    "/api/search?q=x",
    "/api/rename/batches/recent?limit=10",
    "/api/rename/batches/unfinished",
    "/api/rename/presets",
    "/api/op-snapshots?page=1&page_size=20",
    "/api/fs/list?path=/tmp",
    "/api/big?limit=10",
    "/api/folder/stats?path=/tmp",
    "/api/metrics",
]


def start(binary, port, datadir):
    env = dict(os.environ)
    env["VERIFY_DATA"] = datadir
    proc = subprocess.Popen(
        [binary, "--addr", f"127.0.0.1:{port}", "--data", datadir],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        env=env, start_new_session=True,
    )
    for _ in range(60):
        time.sleep(0.4)
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{port}/api/status", timeout=2)
            return proc
        except urllib.error.HTTPError:
            return proc  # 401 也说明服务起来了
        except Exception:
            continue
    raise RuntimeError(f"实例未就绪: {binary} :{port}")


def login(port, datadir):
    """登录拿 Cookie。令牌在 access-token.txt 的第 2 行。"""
    token_file = os.path.join(datadir, "access-token.txt")
    with open(token_file, encoding="utf-8") as f:
        lines = f.read().split("\n")
    token = lines[1].strip()
    body = json.dumps({"token": token}).encode()
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}/api/login", data=body,
        headers={"Content-Type": "application/json", "X-Requested-With": "verify"},
    )
    try:
        urllib.request.urlopen(req, timeout=5)
    except urllib.error.HTTPError:
        pass
    # 从响应里取 Cookie
    req2 = urllib.request.Request(
        f"http://127.0.0.1:{port}/api/login", data=body,
        headers={"Content-Type": "application/json", "X-Requested-With": "verify"},
    )
    try:
        r = urllib.request.urlopen(req2, timeout=5)
        cookie = r.headers.get("Set-Cookie", "")
        return cookie.split(";")[0]
    except urllib.error.HTTPError as e:
        return e.headers.get("Set-Cookie", "").split(";")[0]


def fetch(port, cookie, path):
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}{path}",
        headers={"X-Requested-With": "verify", "Cookie": cookie},
    )
    try:
        with urllib.request.urlopen(req, timeout=8) as r:
            return r.status, r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")
    except Exception as e:
        return 0, str(e)


def normalize(text):
    try:
        obj = json.loads(text)
    except Exception:
        return text.strip()
    return json.dumps(_scrub(obj), sort_keys=True, ensure_ascii=False)


# 这些字段必然随实例环境变化，不是「响应结构改造」引入的差异：
#   - 路径类：两个实例用不同数据目录，路径必然不同
#   - 运行时指标：pid / 内存 / io 计数 / 运行时长
#   - 版本号：新旧版本本来就不同（本次对比的是 3.26.1 与 3.27.0）
#   - 耗时：took_ms 每次请求都不同
ENV_KEYS = {
    "pid", "rss_bytes", "io_read_bytes", "io_write_bytes",
    "cpu_seconds_total", "uptime_seconds", "version",
    "cache_dir", "data_dir", "log_file", "paths", "config",
    "took_ms",
}


def _scrub(obj):
    """把环境相关字段替换成占位符，消除假阳性。"""
    if isinstance(obj, dict):
        out = {}
        for k, v in obj.items():
            if k in ENV_KEYS:
                out[k] = "<env>"
            else:
                out[k] = _scrub(v)
        return out
    if isinstance(obj, list):
        return [_scrub(v) for v in obj]
    if isinstance(obj, str) and "/tmp/verify-cmp-" in obj:
        return "<path>"
    return obj


def main():
    if "--old" not in sys.argv or "--new" not in sys.argv:
        print(__doc__)
        return 1
    old_bin = sys.argv[sys.argv.index("--old") + 1]
    new_bin = sys.argv[sys.argv.index("--new") + 1]

    tmp = tempfile.mkdtemp(prefix="verify-cmp-")
    old_dir = os.path.join(tmp, "old")
    new_dir = os.path.join(tmp, "new")
    os.makedirs(old_dir)
    os.makedirs(new_dir)

    old_proc = new_proc = None
    try:
        old_proc = start(old_bin, OLD_PORT, old_dir)
        new_proc = start(new_bin, NEW_PORT, new_dir)
        old_ck = login(OLD_PORT, old_dir)
        new_ck = login(NEW_PORT, new_dir)

        same = diff = 0
        problems = []
        for path in GET_APIS:
            so, bo = fetch(OLD_PORT, old_ck, path)
            sn, bn = fetch(NEW_PORT, new_ck, path)
            if so != sn:
                diff += 1
                problems.append(f"{path}: 状态码 {so} → {sn}\n   旧: {bo[:200]}\n   新: {bn[:200]}")
                continue
            if normalize(bo) != normalize(bn):
                diff += 1
                problems.append(f"{path}: 响应体不同\n   旧: {normalize(bo)[:300]}\n   新: {normalize(bn)[:300]}")
                continue
            same += 1
            print(f"✅ {path}")

        print(f"\n一致 {same} 处，不同 {diff} 处")
        if problems:
            print("\n差异明细：")
            for p in problems:
                print("  " + p)
        return 0 if diff == 0 else 2
    finally:
        for p in (old_proc, new_proc):
            if p:
                try:
                    os.killpg(os.getpgid(p.pid), signal.SIGKILL)
                except Exception:
                    pass
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
