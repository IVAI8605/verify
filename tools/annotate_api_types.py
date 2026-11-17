#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""给前端的 api() 调用点补上响应类型标注。

v4.0.0：让「后端改了响应字段名 → 前端还在用旧字段」在 tsc --noEmit 期被发现，
而不是等运行时被 `|| []` 静默兜底成空列表。

类型来源：tools/map_routes.py 从后端路由表推导的「路由 → struct」映射，
再经 tools/gen_types.py 转成 TS interface 名。

用法：
    python3 tools/annotate_api_types.py [--dry-run]
"""
import re
import sys
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent

# 路由 → TS 类型名（由 tools/map_routes.py 的输出整理而来）
ROUTE_TYPES = {
    "/api/oplog": "OplogListResp",
    "/api/op-snapshots": "OpSnapshotsResp",
    "/api/trash": "TrashListResp",
    "/api/rename/batches/unfinished": "RenameBatchesResidualResp",
    "/api/rename/batches/recent": "RenameBatchesResp",
    "/api/fs/list": "FilesResp",
    "/api/video_tags": "TagsResp",
    "/api/retain/pref/stats": "AnyResp",
    "/api/retain/pref/profile": "AnyResp",
    "/api/export": "AnyResp",
    "/api/report": "AnyResp",
    "/api/sync/export": "AnyResp",
    "/api/thumb": "AnyResp",
    "/api/auth/token": "TokenResp",
    "/api/big": "FilesLimitOrderResp",
    "/api/cache/clear": "CacheClearedResp",
    "/api/cache/stats": "CacheStatsResp",
    "/api/cleanup/plans": "CleanupPlanResp",
    "/api/cleanup/plans/delete": "OkResp",
    "/api/file/rename": "PathTaskIDResp",
    "/api/folder/stats": "FolderStatsResp",
    "/api/folder_rename/undo": "DoneFailedResp",
    "/api/fs/mkdir": "PathResp",
    "/api/groups": "GroupsResp",
    "/api/groups/audio": "GroupsMediaResp",
    "/api/groups/folder": "GroupsSimpleResp",
    "/api/groups/fuzzy": "GroupsResp",
    "/api/groups/intro": "IntroGroupsResp",
    "/api/groups/photo": "GroupsMediaResp",
    "/api/groups/rename/undo": "DoneFailedItemsResp",
    "/api/groups/semantic": "GroupsResp",
    "/api/groups/tag": "GroupsResp",
    "/api/groups/video": "GroupsResp",
    "/api/health/cancel": "OkResp",
    "/api/health/results": "HealthItemsResp",
    "/api/health/scan": "KindOKResp",
    "/api/health/status": "HealthStatusResp",
    "/api/import": "ImportResultResp",
    "/api/import/groups": "ImportGroupsResp",
    "/api/intro/reanalyze": "NResp",
    "/api/metric": "OkResp",
    "/api/rename/plan": "RenamePlanCreateResp",
    "/api/rename/plan/status": "RenamePlanStatusResp",
    "/api/rename/presets": "RenamePresetResp",
    "/api/rename/preview": "RenamePlanItemsResp",
    "/api/rename/undo-batch": "RenameBatchesResultResp",
    "/api/retain/explain": "FailedReasonsResp",
    "/api/retain/pref/apply": "OkResp",
    "/api/retain/pref/clear": "RetainClearedResp",
    "/api/retain/score": "RetainSuggestionsResp",
    "/api/rule-search/cancel": "OkResp",
    "/api/rule-search/facets": "SnapshotFacetsResp",
    "/api/rule-search/results": "SnapshotGroupsResp",
    "/api/rule-search/run": "SnapshotSavedResp",
    "/api/rule-search/status": "RuleSearchStatusResp",
    "/api/scan": "ModeResp",
    "/api/scan/stop": "OkResp",
    "/api/search": "SearchSQLResp",
    "/api/search/rebuild": "OkResp",
    "/api/search/selection": "SearchSelectionResp",
    "/api/search/selection/items": "SearchResultsResp",
    "/api/search/status": "SearchStatusResp",
    "/api/settings": "WatchRestartResp",
    "/api/status": "StatusResp",
    "/api/sync/import": "SyncImportResp",
    "/api/sync/peers": "SyncPeersResp",
    "/api/tasks": "TasksResp",
    "/api/tasks/pause": "PausedResp",
    "/api/tasks/resume": "PausedResp",
    "/api/transcode": "TranscodeStatusResp",
    "/api/transcode/cancel": "OkResp",
    "/api/transcode/residue": "TranscodeResidueResp",
    "/api/transcode/residue/clean": "MoveResultResp",
    "/api/transcode/status": "TranscodeTasksResp",
    "/api/trash/empty": "TrashRemovedResp",
    "/api/trash/preview": "TrashPreviewResp",
    "/api/trim/log": "TrimLogResp",
    "/api/trim/probe": "TrimProbeResp",
    "/api/video/cropdetect": "LabeledRemainingResp",
    "/api/video/failures": "ItemsTotalResp",
    "/api/video/reanalyze": "CountOKResp",
    "/api/video_tags/recompute": "TagSavedCountResp",
}

# await api('/api/xxx' ...) 完整调用（含括号）
# URL 里常有变量（?page=、+ n + 等），所以用括号配平扫描而不是简单正则。
CALL = re.compile(r"await api\((['\"])(/api/[^'\"]*)\1")


def find_call_end(src, i):
    """从 src[i]（api 的左括号）开始找到配平的右括号，跳过字符串字面量。"""
    depth = 0
    in_str = False
    quote = ''
    j = i
    while j < len(src):
        c = src[j]
        if in_str:
            if c == '\\':
                j += 2
                continue
            if c == quote:
                in_str = False
        elif c in ('"', "'"):
            in_str = True
            quote = c
        elif c == '(':
            depth += 1
        elif c == ')':
            depth -= 1
            if depth == 0:
                return j
        j += 1
    return -1


def find_type(path):
    """按接口路径找类型；取最长的匹配前缀。"""
    best = None
    best_len = -1
    for prefix, tname in ROUTE_TYPES.items():
        if path.startswith(prefix) and len(prefix) > best_len:
            best = tname
            best_len = len(prefix)
    return best


def main():
    dry = "--dry-run" in sys.argv
    types_file = ROOT / "webtypes" / "types.d.ts"
    if not types_file.exists():
        print("❌ webtypes/types.d.ts 不存在，先运行 python3 tools/gen_types.py")
        return 1
    declared = set(re.findall(r'^interface\s+(\w+)', types_file.read_text(encoding='utf-8'), re.M))

    total = 0
    for p in sorted((ROOT / "internal" / "web" / "ui").glob("*.js")):
        src = p.read_text(encoding='utf-8')
        out = []
        pos = 0
        changed = 0
        for m in CALL.finditer(src):
            if m.start() < pos:
                continue
            path = m.group(2)
            tname = find_type(path)
            if not tname or tname not in declared:
                continue
            # 已标注的跳过
            prefix = src[max(0, m.start() - 60):m.start()]
            if '/** @type {' in prefix and prefix.rstrip().endswith('*/ ('):
                continue
            # 找整个调用的结束位置（括号配平）
            open_paren = src.index('(', m.start())
            close_paren = find_call_end(src, open_paren)
            if close_paren < 0:
                continue
            whole = src[m.start():close_paren + 1]
            out.append(src[pos:m.start()])
            out.append(f"/** @type {{{tname}}} */ ({whole})")
            pos = close_paren + 1
            changed += 1
        out.append(src[pos:])
        new = "".join(out)
        if changed:
            total += changed
            print(f"{p.name}: {changed} 处")
            if not dry:
                p.write_text(new, encoding='utf-8')
    print(f"\n合计标注: {total} 处")
    if dry:
        print("(dry-run，未写入)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
