// 查重助手 verify Web UI · 设置与工具页（v3.0.0 从 app.js 拆分）
// 加载顺序第 3：设置页（扫描根/标签/路径映射）、健康检查、导入会话、视频转码。
// 这些页面配置项多、表单密集，单独成文件避免把结果页渲染撑大。

// ---------- 视频转码（v1.17 引入 17.4-17.5）----------
// 独立功能页：队列管理（等待/转码中/成功/失败 + 进度 + 取消）。
// 结果组「转码」按钮只入队不执行（决策 C5），执行统一在本页发起。
async function loadTranscode(){
  try {
    const r = /** @type {TranscodeTasksResp} */ (await api('/api/transcode/status'));
    S.tcTasks = r.tasks || [];
    S.tcEncoder = r.encoder || '';
  } catch(e) { S.tcTasks = []; }
  renderTranscode();
}

function renderTranscode(){
  const v = $('view');
  const tasks = S.tcTasks || [];
  const pending = tasks.filter(t => t.status === 'waiting' || t.status === 'running');
  // v1.26.0：进度聚合（N/M 完成 + 当前文件进度）
  const doneN = tasks.filter(t => t.status === 'done').length;
  const failN = tasks.filter(t => t.status === 'failed').length;
  const running = tasks.find(t => t.status === 'running');
  const paused = S.status && S.status.paused;
  const agg = tasks.length ? `<span class="badge ${doneN + failN === tasks.length ? 'good' : 'warn'}">完成 ${doneN}/${tasks.length}${failN ? ` · 失败 ${failN}` : ''}${running ? ` · 当前 ${Math.round(running.progress)}%` : ''}</span>
    <div class="pbar" style="flex:1;min-width:160px"><i style="--p:${tasks.length ? (doneN + failN) / tasks.length : 0}"></i></div>` : '';
  // 2026-11-08 unslop：编码器/队列是参考信息不是状态，从彩色胶囊降为安静文字
  const head = `<div class="toolbar">
    <span class="hint">编码器 ${S.tcEncoder === 'hevc_qsv' ? '核显硬编 hevc_qsv' : S.tcEncoder === 'hevc_vaapi' ? '核显硬编 hevc_vaapi（VAAPI）' : S.tcEncoder === 'libx265' ? '软编 libx265' : '未探测'}</span>
    <span class="hint">队列 ${tasks.length}</span>
    <select id="tcQuality"><option value="high"${S.tcQuality==='high'?' selected':''}>高保真 CRF20</option><option value="balanced"${S.tcQuality==='balanced'?' selected':''}>均衡 CRF24（默认）</option><option value="compact"${S.tcQuality==='compact'?' selected':''}>省空间 CRF28</option></select>
    <select id="tcMaxRes"><option value=""${!S.tcMaxRes?' selected':''}>保持原分辨率</option><option value="1080"${S.tcMaxRes==='1080'?' selected':''}>上限 1080p</option></select>
    ${agg}
    <div class="spacer"></div>
    <!-- v3.9.6 修 S-33：PauseTasks() 实际暂停全部引擎任务，文案不再暗示"只暂停转码" -->
    <button class="btn" data-act="tc-pause"${pending.length ? '' : ' disabled'}>${paused ? '继续全部任务' : '暂停全部任务'}</button>
    <button class="btn primary" data-act="tc-start"${S.tcSel && S.tcSel.size ? '' : ' disabled'}>开始转码（${S.tcSel ? S.tcSel.size : 0}）</button>
    <span class="tb-sep" aria-hidden="true"></span>
    <button class="btn danger" data-act="tc-cancel"${pending.length ? '' : ' disabled'}>取消整批</button>
    <button class="btn ghost" data-act="tc-clear">清空入队清单</button>
    <button class="btn ghost" data-act="tc-residue" title="扫描索引里的视频目录，把超过 24 小时的 *_h265_tmp* / *_trimmed_tmp* 移入隔离区">清理转码残留</button>
    <button class="btn ghost" data-act="tc-export-failures"${tasks.some(t => t.status === 'failed') ? '' : ' disabled'}>导出失败明细</button>
  </div>
  <div class="hint tc-note">
    转码会改变文件内容：旧的重复关系会打散（预期行为），转码完成后原文件进隔离区可恢复。<br>
    H.265 约省 30–50% 空间；音轨统一转 AAC 128k，文本字幕内封保留。<br>
    图形字幕（PGS/VobSub）MP4 不支持，会被丢弃。
  </div>`;
  const pendingSel = Array.from(S.tcSel || []);
  const selHtml = pendingSel.length ? `<div class="card tc-card">
    <b>待入队（${pendingSel.length}）：</b>${pendingSel.map(p => `<span class="chip">${esc(p.split('/').pop())}</span>`).join('')}</div>` : '';
  if (!tasks.length && !pendingSel.length) {
    v.innerHTML = head + selHtml + `<div class="card hint tc-empty">队列为空。在「精确重复」「相似视频」结果行点「转码」按钮加入队列，然后在这里统一发起。</div>`;
    return;
  }
  const rows = tasks.map(t => {
    const badge = t.status === 'done' ? '<span class="badge good">完成</span>'
      : t.status === 'running' ? '<span class="badge warn">转码中</span>'
      : t.status === 'failed' ? '<span class="badge">失败</span>'
      : t.status === 'cancelled' ? '<span class="badge">已取消</span>' : '<span class="badge">等待</span>';
    const prog = t.status === 'running'
      ? `<div class="pbar tc-pbar"><i style="--p:${Math.min(1, Math.max(0, (Number(t.progress) || 0) / 100))}"></i></div>
         <div class="hint">当前文件 ${Math.round(Number(t.progress) || 0)}% · 队列 ${pending.length} 个待处理</div>`
      : '';
    const err = t.error ? `<div class="hint">${esc(t.error)}</div>` : '';
    // v3.10.8：后端支持按 job_id 取消单个任务，等待/转码中才给入口，
    // 已结束的任务再显示「取消」只会让人以为还能撤销。
    const canCancel = t.status === 'waiting' || t.status === 'running';
    const cancelBtn = canCancel
      ? `<button class="btn ghost" data-act="tc-cancel-one" data-id="${t.id}" title="只取消这一个任务，其他任务继续">取消</button>`
      : '';
    // v3.23.0：源不是 .mp4 时输出落在 <主名>.mp4，文案必须说清扩展名变了。
    const replacedHint = t.replaced
      ? (t.renamed_ext ? '<span class="hint">已转为 .mp4（原件在隔离区）</span>' : '<span class="hint">已替换原位（原件在隔离区）</span>')
      : '';
    const showPath = t.final_path || t.path;
    return `<div class="card tc-card">
      <div class="tc-row">
        <b class="tc-name" title="${esc(showPath)}">${esc(t.name)}</b>
        ${badge}${replacedHint}${cancelBtn}
      </div>${prog}${err}
      <div class="hint tc-path" title="${esc(showPath)}">${esc(showPath)}</div>
    </div>`;
  }).join('');
  v.innerHTML = head + selHtml + rows;
  scheduleTranscodePoll();
}

// scheduleTranscodePoll 是 SSE 之外的兜底轮询（v3.23.0）。
// 单文件转码时后端每行 stderr 都会推 transcode_progress，但网络抖动或
// 浏览器不支持 SSE 时界面会停在旧进度；有 running/waiting 时每 3 秒补拉一次。
let tcPollTimer = 0;
function scheduleTranscodePoll(){
  clearTimeout(tcPollTimer);
  if (S.tab !== 'transcode') return;
  const active = (S.tcTasks || []).some(t => t.status === 'running' || t.status === 'waiting');
  if (!active) return;
  tcPollTimer = setTimeout(() => { loadTranscode(); }, 3000);
}

async function transcodeStart(){
  const paths = Array.from(S.tcSel || []);
  if (!paths.length) { toast('先在结果页加入队列', 'err'); return; }
  S.tcQuality = $('tcQuality') ? $('tcQuality').value : 'balanced';
  S.tcMaxRes = $('tcMaxRes') ? $('tcMaxRes').value : '';
  try {
    const r = await postJSON('/api/transcode', { paths, quality: S.tcQuality, max_res: S.tcMaxRes });
    toast('已开始转码 ' + (r.queued || 0) + ' 个（编码器 ' + (r.encoder || '?') + '）', 'ok');
    S.tcSel = new Set();
    await loadTranscode();
  } catch(e) { toast(e.message, 'err'); }
}

// transcodeCleanResidue 预览并清理断电/崩溃留下的临时文件（v3.23.0）。
// 只移入隔离区，不删除；扫描范围限定在索引里的视频目录，不做全盘遍历。
async function transcodeCleanResidue(){
  try {
    const r = /** @type {TranscodeResidueResp} */ (await api('/api/transcode/residue'));
    const files = r.files || [];
    if (!files.length) {
      toast('没有超过 24 小时的转码残留（扫描 ' + (r.scanned_dirs || 0) + ' 个目录）', 'ok');
      return;
    }
    const preview = files.slice(0, 5).map(f => f.path).join('\n');
    if (!confirm('发现 ' + files.length + ' 个转码残留（扫描 ' + (r.scanned_dirs || 0) + ' 个目录），全部移入隔离区？\n\n' + preview + (files.length > 5 ? '\n…' : ''))) return;
    const res = await postJSON('/api/transcode/residue/clean', { paths: files.map(f => f.path) });
    toast('已移入隔离区 ' + (res.moved || 0) + ' 个，失败 ' + (res.failed || 0) + ' 个', res.failed && res.failed.length ? 'warn' : 'ok');
  } catch(e) { toast(e.message, 'err'); }
}

// transcodeExportFailures 导出转码失败明细 CSV（v3.24.0）。
// 与健康/大文件/改名的失败导出同口径：路径、原因、时间，BOM 头保证 Excel 中文不乱码。
function transcodeExportFailures(){
  const failed = (S.tcTasks || []).filter(t => t.status === 'failed');
  if (!failed.length) { toast('没有失败的转码任务', 'warn'); return; }
  const lines = ['路径,原因,时间'];
  failed.forEach(t => lines.push(csvCell(t.path || '') + ',' + csvCell(t.error || '') + ',' + csvCell(new Date().toLocaleString())));
  const blob = new Blob(['\ufeff' + lines.join('\r\n')], {type:'text/csv;charset=utf-8'});
  const a = document.createElement('a');
  a.download = '转码失败明细-' + new Date().toISOString().slice(0, 16).replace(/[:T]/g, '') + '.csv';
  a.href = URL.createObjectURL(blob);
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  toast('已导出 ' + failed.length + ' 条失败明细', 'ok');
}

// ---------- 导入会话（v1.10.0）----------
// 设计：导入与当前扫描库完全独立。路径匹配不上不是错误（比如从 Windows DC
// 导出的 C:\ 路径在本机一个都匹配不到），这类行按「仅列表」展示、禁止清理操作。
const IMPORT_PAGE = 200; // 详情页每次渲染的组数，防止大导入把 DOM 撑爆

async function loadImport(){
  S.loading = true; renderImport();
  try {
    const r = /** @type {ImportSessionsResp} */ (await api('/api/import/sessions'));
    S.importSessions = r.sessions || [];
  } catch(e) { toast(e.message, 'err'); S.importSessions = []; }
  finally { S.loading = false; renderImport(); }
}

async function loadImportGroups(id){
  S.loading = true; renderImport();
  try {
    const r = /** @type {ImportGroupsResp} */ (await api('/api/import/groups?session_id=' + id));
    S.importDetail = { session: r.session, groups: r.groups || [], shown: IMPORT_PAGE };
  } catch(e) { toast(e.message, 'err'); S.importDetail = null; }
  finally { S.loading = false; renderImport(); }
}

function importSourceBadge(src){
  return src === 'dc5' ? '<span class="badge mono">DC5</span>' : '<span class="badge">通用</span>';
}

function importTime(ts){
  return ts ? new Date(ts * 1000).toLocaleString() : '—';
}

function renderImport(){
  const v = $('view');
  if (S.importDetail) { v.innerHTML = importDetailHtml(); applyTableSort('import-detail'); return; }
  const list = S.importSessions || [];
  const rows = list.map(ss => `<tr>
      <td class="fname">${esc(ss.name || ('会话 #' + ss.id))}</td>
      <td>${importSourceBadge(ss.source)}</td>
      <td class="hint">${importTime(ss.created_at)}</td>
      <td class="num">${(ss.total_rows||0).toLocaleString()}</td>
      <td class="num">${(ss.group_count||0).toLocaleString()}</td>
      <td class="num">${(ss.total_rows||0) ? Math.round((ss.matched_rows||0) * 100 / ss.total_rows) + '%' : '—'}</td>
      <td class="act">
        <button class="btn ghost" data-act="import-open" data-id="${ss.id}">查看</button>
        <button class="btn ghost" data-act="import-del" data-id="${ss.id}" data-name="${esc(ss.name)}">删除</button>
      </td>
    </tr>`).join('');
  const cols = importColumns();
  const colHtml = '<colgroup>' + cols.map(c => { const w = colWidthFor(c.id, cols); return `<col data-col="${c.id}"${w ? ` style="width:${w}px"` : ''}>`; }).join('') + '</colgroup>';
  const headHtml = '<thead><tr>' + thCells(cols) + '</tr></thead>';
  const table = list.length
    ? `<div class="group"><div class="tscroll"><table class="rtable" data-sortkey="import">${colHtml}${headHtml}<tbody>${rows}</tbody></table></div></div>`
    : `<div class="card hint" style="padding:18px">还没有导入记录。把 DC5 或本工具导出的 CSV 传上来，可以随时回看当初的分组和标记。</div>`;
  v.innerHTML = `
    <div class="section"><h2 class="section-title">导入 CSV</h2><div class="card">
      <div class="toolbar" style="flex-wrap:wrap">
        <input type="file" id="importFile" accept=".csv,text/csv">
        <input id="importName" placeholder="会话名称（留空用文件名）" style="width:220px">
        <button class="btn primary" data-act="import-upload">开始导入</button>
      </div>
      <div class="hint" style="margin-top:8px">支持 DC5 兼容 CSV 与本工具导出的通用 CSV（按表头自动识别）。导入结果独立保存，不影响当前扫描库；
      路径在本机匹配不上的行会以「仅列表」展示，不能执行清理操作。上限 128MB / 50 万行。</div>
    </div></div>
    <div class="section"><h2 class="section-title">导入记录</h2>${table}</div>`;
  applyTableSort('import');
}

function importDetailHtml(){
  const d = S.importDetail, ss = d.session || {};
  const back = `<div class="toolbar"><button class="btn ghost" data-act="import-back">← 返回列表</button>
    <span class="hint">${esc(ss.name || ('会话 #' + ss.id))} · ${importTime(ss.created_at)}</span>
    <span class="badge">${(ss.total_rows||0).toLocaleString()} 行</span>
    <span class="badge">${(ss.group_count||0).toLocaleString()} 组</span>
    <span class="badge warn">未匹配 ${((ss.total_rows||0)-(ss.matched_rows||0)).toLocaleString()}</span>
    <div class="spacer"></div>
    <button class="btn" data-act="import-trash-marked">隔离全部已标记且已匹配</button></div>`;
  const detailCols = importDetailColumns();
  const detailColHtml = '<colgroup>' + detailCols.map(c => { const w = colWidthFor(c.id, detailCols); return `<col data-col="${c.id}"${w ? ` style="width:${w}px"` : ''}>`; }).join('') + '</colgroup>';
  const detailHeadHtml = '<thead><tr>' + thCells(detailCols) + '</tr></thead>';
  const groups = d.groups || [];
  const shown = groups.slice(0, d.shown || IMPORT_PAGE);
  const body = shown.map(g => {
    const label = g.group_no === 0 ? '唯一 / 孤组' : '组 ' + g.group_no;
    const rows = (g.rows || []).map(r => {
      const matched = !!r.matched;
      const nameCell = `<td class="fname"${matched ? '' : ' style="color:var(--muted)"'}>${esc(r.name)}${r.chk ? ' <span class="badge">已标记</span>' : ''}${r.protect ? ' <span class="badge">保护</span>' : ''}</td>`;
      const pathCell = `<td class="fpath" title="${esc(r.path)}">${esc(r.path)}</td>`;
      const sizeCell = `<td class="num">${fmtB(r.size)}</td>`;
      const timeCell = `<td class="num">${r.datem ? new Date(r.datem/1e6).toLocaleDateString() : '—'}</td>`;
      const stateCell = matched
        ? '<td><span class="badge" style="color:var(--accent-2)">已匹配</span></td>'
        : '<td><span class="badge">仅列表</span></td>';
      const actCell = (matched && !r.protect)
        ? `<td class="act"><button class="btn ghost" data-act="import-trash" data-path="${esc(r.path)}">隔离</button></td>`
        : `<td class="act">${r.protect ? '<span class="hint">受保护</span>' : '<span class="hint">—</span>'}</td>`;
      return `<tr${matched ? '' : ' style="opacity:.55"'}>${nameCell}${pathCell}${sizeCell}${timeCell}${stateCell}${actCell}</tr>`;
    }).join('');
    return `<div class="group"><div class="ghead" style="display:flex;align-items:center;gap:8px;padding:10px 12px">
        <span class="badge mono">${label}</span><span class="hint">${(g.rows||[]).length} 个文件</span></div>
      <table class="rtable" data-sortkey="import-detail">${detailColHtml}${detailHeadHtml}<tbody>${rows}</tbody></table></div>`;
  }).join('');
  const more = groups.length > (d.shown || IMPORT_PAGE)
    ? `<div class="toolbar"><span class="hint">已显示 ${Math.min(d.shown||IMPORT_PAGE, groups.length)} / ${groups.length} 组</span>
       <button class="btn" data-act="import-more">显示更多</button></div>` : '';
  return back + body + more;
}

async function importUpload(){
  const fi = $('importFile');
  const file = fi && fi.files && fi.files[0];
  if (!file) { toast('先选择要导入的 CSV 文件', 'err'); return; }
  // v3.10.10（第七轮 P0）：后端上限提到 128MB，前端同步放宽，否则大文件在本地就被挡掉
  const MAX_BYTES = 128 * 1024 * 1024; // 128MB
  if (file.size > MAX_BYTES) { toast(`文件 ${(file.size/1024/1024).toFixed(1)}MB 超过上限 128MB，可能行数过多`, 'err'); return; }
  const fd = new FormData();
  fd.append('file', file);
  const nameEl = $('importName');
  if (nameEl && nameEl.value.trim()) fd.append('name', nameEl.value.trim());
  try {
    // v3.10.7：改走 api()，让 multipart 写请求带上 CSRF 校验所需的 X-Requested-With
    const r = /** @type {AnyResp} */ (await api('/api/import', { method:'POST', body: fd }));
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || '导入失败');
    S.importDetail = null;
    await loadImport();
    toast(`导入完成：${(j.total||0).toLocaleString()} 行 / ${(j.groups||0).toLocaleString()} 组，本机匹配 ${(j.matched||0).toLocaleString()}`, 'ok');
    const warns = j.warnings || [];
    if (warns.length) showPreviewModal('导入提示（部分行已跳过或修正）',
      warns.map(w => ({ path: w })), '知道了', () => {});
  } catch(e) { toast(e.message, 'err'); }
}

async function importDelete(id, name){
  if (!confirm(`删除导入会话「${name || ('#' + id)}」？只删导入记录，不影响文件。`)) return;
  try {
    /** @type {ImportSessionsResp} */ (await api('/api/import/sessions?id=' + id, { method: 'DELETE' }));
    toast('已删除导入会话', 'ok');
    await loadImport();
  } catch(e) { toast(e.message, 'err'); }
}

// v3.10.10（第六轮 P1-3）：导入会话的隔离入口也要带分组键，否则后端取不到成员集合，
// 「同组至少保留一份」直接放行。只对「本机已匹配行 ≥2」的重复组生成键：
// 组内本地只剩一个副本时没有可保留的对象；组号 0 是唯一/孤组，本身不含重复副本。
function importGroupKeysFor(paths){
  const d = S.importDetail;
  const sid = (d && d.session && d.session.id) || 0;
  const want = new Set(paths || []);
  const out = [];
  if (!sid) return out;
  ((d && d.groups) || []).forEach(g => {
    if (!g.group_no) return;
    const matched = (g.rows || []).filter(r => r.matched);
    if (matched.length < 2 || !matched.some(r => want.has(r.path))) return;
    out.push('import:' + sid + ':' + g.group_no);
  });
  return out;
}

// importRemainCount 统计勾选路径所在组里未被选中的已匹配成员数（确认框展示用）。
function importRemainCount(paths, keys){
  const d = S.importDetail;
  const sid = (d && d.session && d.session.id) || 0;
  if (!d || !sid || !keys.length) return 0;
  const want = new Set(paths || []);
  let n = 0;
  (d.groups || []).forEach(g => {
    if (!keys.includes('import:' + sid + ':' + g.group_no)) return;
    (g.rows || []).forEach(r => { if (r.matched && !want.has(r.path)) n++; });
  });
  return n;
}

async function importTrashPaths(paths){
  if (!paths.length) { toast('没有可隔离的文件', 'err'); return; }
  const keys = importGroupKeysFor(paths);
  const body = { paths, group_key: keys[0] || '', group_keys: keys };
  const title = keys.length
    ? '移入隔离区预览（移走 ' + paths.length + ' 个，组内保留 ' + importRemainCount(paths, keys) + ' 个）'
    : '移入隔离区预览（移走 ' + paths.length + ' 个）';
  try {
    const pv = await postJSON('/api/trash/preview', Object.assign({ action:'trash' }, body));
    showPreviewModal(title, pv.items || [], '确认移入隔离区', async () => {
      let r;
      try { r = await postJSON('/api/trash', body); }
      catch(e) { closeModal(); toast(e.message, 'err'); return; }
      closeModal();
      const items = resultItemsFrom(r, 'moved');
      const fails = items.filter(x => !x.ok);
      if (fails.length) showResultModal('移入隔离区结果：部分失败', items);
      else toast('已移入隔离区 ' + (r.moved || []).length + ' 个文件', 'ok');
      showReconcile(r.reconcile);
      await refreshStatus();
    });
  } catch(e) { toast(e.message, 'err'); }
}

// presetListHtml v3.9.0：规则预设列表（应用 / 删除）。
function presetListHtml(list){
  if (!list || !list.length) return '<span class="hint">还没有预设</span>';
  return list.map((p, i) => `<div class="asrow" style="margin:4px 0">
    <span class="badge">${esc(p.name)}</span>
    <button class="btn ghost" data-act="preset-apply" data-idx="${i}">应用</button>
    <button class="btn ghost" data-act="preset-del" data-idx="${i}">删除</button>
  </div>`).join('');
}

// fillSettingsRules v3.9.0：把一条规则回填到设置页编辑器。
function fillSettingsRules(r){
  if (!r) return;
  if ($('sRuleBase')) $('sRuleBase').value = r.base || 'content';
  document.querySelectorAll('[data-act="srule-attr"]').forEach(x => {
    x.checked = (r.attrs || []).includes(x.dataset.attr);
  });
  if ($('sRuleSizeTol')) $('sRuleSizeTol').value = Number(r.size_tolerance_kb || 0);
  if ($('sRuleSimilar')) $('sRuleSimilar').value = Number(r.name_similar_pct || 90);
  if ($('sRuleFolderMode')) $('sRuleFolderMode').value = r.folder_name_mode || 'full';
  if ($('sRuleFolderDepth')) $('sRuleFolderDepth').value = Number(r.folder_name_depth || 1);
  if ($('sRuleIgnoreCopy')) $('sRuleIgnoreCopy').checked = !!r.ignore_copy_names;
  if ($('sRuleIgnoreSame')) $('sRuleIgnoreSame').checked = !!r.ignore_same_folder;
  if ($('sRuleCase')) $('sRuleCase').checked = !!r.case_sensitive;
}

async function saveSettingsPresets(list){
  try {
    await postJSON('/api/settings', { search_presets: list });
    toast('规则预设已保存', 'ok');
    await refreshStatus();
    renderSettings();
  } catch(e) { toast(e.message, 'err'); }
}

async function saveSettingsPreset(){
  const name = ($('sPresetName') ? $('sPresetName').value.trim() : '');
  if (!name) { toast('请填写预设名称', 'err'); return; }
  const list = (((S.status || {}).config || {}).search_presets || []).slice();
  list.push({ name, rules: readSettingsRules() });
  await saveSettingsPresets(list);
}

async function deleteSettingsPreset(idx){
  const list = (((S.status || {}).config || {}).search_presets || []).slice();
  if (idx < 0 || idx >= list.length) return;
  if (!confirm('删除预设「' + list[idx].name + '」？')) return;
  list.splice(idx, 1);
  await saveSettingsPresets(list);
}

function applySettingsPreset(idx){
  const list = (((S.status || {}).config || {}).search_presets || []);
  const p = list[idx];
  if (!p) return;
  fillSettingsRules(p.rules);
  toast('已应用预设「' + p.name + '」，点保存设置后生效', 'ok');
}

// v3.9.10：旧的 applyMatchModeLocks / syncSettingsKinds 已删除——v3.9.0 规则编辑器
// 取代匹配模式下拉后，页面不再渲染 sMatchMode / sMatchKinds，函数只剩空转。

// ---------- v3.2.0 定时清理计划 ----------
const CLEANUP_MODE_LABELS = { all:'全部类型', exact:'精确重复', name:'同名文件', video:'相似视频', audio:'重复音频' };
const CLEANUP_STRAT_LABELS = { modify_newest:'保留修改最新', largest_size:'保留体积最大', best_codec:'保留画质最好' };

function cleanupPlanListHtml(){
  const plans = S.cleanupPlans || [];
  if (!plans.length) return '<span class="hint">还没有计划。下面填好条件点「保存计划」即可。</span>';
  return plans.map(p => `<div class="scanrow" style="align-items:flex-start;flex-wrap:wrap;gap:6px">
    <span class="badge ${p.enabled ? 'keep' : ''}">${p.enabled ? '启用' : '停用'}</span>
    <b>${esc(p.name)}</b>
    <span class="hint mono">${esc(p.schedule)} · ${esc(CLEANUP_MODE_LABELS[p.match_mode] || p.match_mode)} · ${esc(CLEANUP_STRAT_LABELS[p.retain_strategy] || p.retain_strategy)}</span>
    <span class="hint" style="flex-basis:100%">${p.last_run ? fmtT(Number(p.last_run) * 1e9) + ' · ' + esc(p.last_result || '') + (p.last_error ? '（' + esc(p.last_error) + '）' : '') : '还没执行过'}</span>
    <button class="btn" data-act="cleanup-run" data-id="${p.id}">立即执行</button>
    <button class="btn ghost" data-act="cleanup-edit" data-id="${p.id}">编辑</button>
    <button class="btn ghost" data-act="cleanup-toggle" data-id="${p.id}" data-enabled="${p.enabled ? 0 : 1}">${p.enabled ? '停用' : '启用'}</button>
    <button class="btn ghost" data-act="cleanup-del" data-id="${p.id}">删除</button>
  </div>`).join('');
}

function renderCleanupList(){
  const box = $('cleanupList');
  if (box) box.innerHTML = cleanupPlanListHtml();
}

// fillCleanupForm 把计划回填到表单；传 null 表示新建。
function fillCleanupForm(p){
  S.cleanupEditID = p ? p.id : 0;
  $('cName').value = p ? p.name : '';
  $('cSchedule').value = p ? p.schedule : 'weekly fri 05:00';
  $('cMode').value = p ? p.match_mode : 'video';
  $('cMinSize').value = p ? Math.round((p.min_size || 0) / 1024) : 0;
  $('cMinCount').value = p ? (p.min_count || 0) : 2;
  $('cExt').value = p ? (p.extensions || '') : 'mp4,mkv,avi,mov,wmv';
  $('cStrategy').value = p ? p.retain_strategy : 'modify_newest';
  $('cMaxGB').value = p ? (p.max_keep_gb || 0) : 10;
  const hint = $('cleanupHint');
  if (hint) hint.textContent = p ? '正在编辑：' + p.name : '';
}

async function loadCleanupPlans(){
  try {
    const r = /** @type {CleanupPlansResp} */ (await api('/api/cleanup/plans'));
    S.cleanupPlans = r.plans || [];
    renderCleanupList();
  } catch(e) { toast(e.message, 'err'); }
}

async function saveCleanupPlan(){
  const cur = (S.cleanupPlans || []).find(x => x.id === S.cleanupEditID);
  // v3.9.6 修 S-27：清理计划 8 项零校验，现在加前端前置校验
  const name = $('cName').value.trim();
  if (!name) { toast('计划名称不能留空', 'err'); return; }
  const schedule = $('cSchedule').value.trim();
  if (schedule && !validQuietHours(schedule)) { toast('运行时段格式应为 HH:MM-HH:MM，例如 02:00-05:00；留空表示立即执行', 'err'); return; }
  const minSizeKB = Number($('cMinSize').value || 0);
  if (isNaN(minSizeKB) || minSizeKB < 0) { toast('最小文件大小不能是负数', 'err'); return; }
  const minCount = Math.max(0, Math.min(10000, Number($('cMinCount').value || 0)));
  const maxKeepGB = Math.max(0, Math.min(10240, Number($('cMaxGB').value || 0)));
  const p = {
    id: S.cleanupEditID || 0,
    name,
    enabled: cur ? cur.enabled : true,
    schedule,
    match_mode: $('cMode').value,
    min_size: minSizeKB * 1024,
    min_count: minCount,
    extensions: $('cExt').value.trim(),
    retain_strategy: $('cStrategy').value,
    max_keep_gb: maxKeepGB,
  };
  // v3.9.0：计划默认复用当前搜索规则；旧 match_mode 仍在表单里作为兼容回退。
  const sr = (S.status && S.status.config && S.status.config.search_rules) || null;
  p.rules_json = sr ? JSON.stringify(sr) : '';
  const hint = $('cleanupHint');
  if (hint) hint.textContent = '保存中…';
  try {
    await postJSON('/api/cleanup/plans', p);
    toast('清理计划已保存', 'ok');
    S.cleanupEditID = 0;
    if (hint) hint.textContent = '';
    await loadCleanupPlans();
  } catch(e) { if (hint) hint.textContent = ''; toast('保存失败：' + (e && e.message || e), 'err'); }
}

async function runCleanupPlan(id){
  const p = (S.cleanupPlans || []).find(x => x.id === id);
  if (!p) return;
  if (!confirm('立即执行「' + p.name + '」？\n\n只会把多余文件移入隔离区，不会永久删除；每组至少保留一个。')) return;
  try {
    const r = await postJSON('/api/cleanup/run', { id });
    toast('执行完成：移入 ' + (r.trashed || 0) + ' 个文件，可回收 ' + fmtB(r.freed || 0), 'ok', 5000);
    await loadCleanupPlans();
    await refreshStatus();
  } catch(e) { toast('执行失败：' + (e && e.message || e), 'err'); }
}

async function toggleCleanupPlan(id, enabled){
  const p = (S.cleanupPlans || []).find(x => x.id === id);
  if (!p) return;
  try {
    await postJSON('/api/cleanup/plans', Object.assign({}, p, { enabled }));
    await loadCleanupPlans();
  } catch(e) { toast(e.message, 'err'); }
}

async function deleteCleanupPlan(id){
  if (!confirm('删除这条清理计划？')) return;
  try {
    await postJSON('/api/cleanup/plans/delete', { id });
    if (S.cleanupEditID === id) S.cleanupEditID = 0;
    await loadCleanupPlans();
  } catch(e) { toast(e.message, 'err'); }
}

// applyCleanupTemplate 用预设模板填充表单（不自动保存）。
function applyCleanupTemplate(kind){
  const t = {
    video: { name:'每周清理重复视频', schedule:'weekly fri 05:00', mode:'video', ext:'mp4,mkv,avi,mov,wmv', strat:'modify_newest', maxGB:10 },
    exact: { name:'每天清理精确重复', schedule:'daily 03:00', mode:'exact', ext:'', strat:'largest_size', maxGB:0 },
    name:  { name:'每周清理同名文件', schedule:'weekly mon 04:00', mode:'name', ext:'', strat:'modify_newest', maxGB:0 },
  }[kind];
  if (!t) return;
  S.cleanupEditID = 0;
  $('cName').value = t.name;
  $('cSchedule').value = t.schedule;
  $('cMode').value = t.mode;
  $('cMinSize').value = 0;
  $('cMinCount').value = 2;
  $('cExt').value = t.ext;
  $('cStrategy').value = t.strat;
  $('cMaxGB').value = t.maxGB;
  const hint = $('cleanupHint');
  if (hint) hint.textContent = '已套用模板，确认后点「保存计划」';
}

// v3.10.0：设置项越来越多，四列并排在小屏上挤成一团。改成下级导航：
// 一次只看一个模块，表单字段仍全部保留在 DOM 里（切页只切显示），保存逻辑不用改。
const SET_TABS = [
  ['perf', '扫描与性能'], ['schedule', '后台任务与休眠'], ['basis', '查重依据'], ['paths', '路径与安全'],
  ['tags', '元数据标签'], ['cleanup', '定时清理'], ['sync', '跨设备指纹'], ['cache', '缓存管理']
];

function renderSettings(){
  setTimeout(bindQuietHoursCheck, 0);
  S.scanPaths = ((S.status && S.status.config && S.status.config.volumes) || []).slice();
  const c = S.status ? S.status.config : {};
  const ff = S.status ? S.status.ffmpeg : {};
  const paths = S.status ? (S.status.paths || {}) : {};
  const hash = S.status ? (S.status.hash || {}) : {};
  const minKB = Math.round((c.min_size || 0) / 1024);
  const excl = (c.exclude_dirs || []).join('\n');
  const protect = (c.protect_paths || []).join('\n');
  const pathMaps = (c.path_maps || []).map(m => m.nas + ' = ' + m.ext).join('\n');
  const exclPaths = (c.exclude_paths || []).join('\n');
  let hwLine = '';
  if (ff.available) {
    if (ff.hw_mode === 'qsv') {
      hwLine = '视频分析会用核显加速（QSV），比纯 CPU 快不少。';
    } else if (ff.render_node) {
      hwLine = '机器有核显，但当前视频工具用不上它，视频分析会用 CPU 跑，慢一些。';
    } else {
      hwLine = '没找到核显，视频分析会用 CPU 跑，慢一些。';
    }
  }
  $('view').innerHTML = `
    ${!ff.available ? '<div class="warnbox">没找到视频工具（ffmpeg / ffprobe），视频相关的查重用不了。飞牛系统一般自带，路径通常在 /usr/bin 下；找不到可以在下面手动填。</div>' : ''}
    <!-- 2026-11-08 unslop：三段说明原先各占一个浅蓝盒子（两个还是用内联样式改色的 warnbox），
         按同一件事合成一条中性提示；日志与缩略图的实际路径只在排查时看，
         「路径与安全」页签的「日志存放位置」「缩略图存放位置」已经写着，首屏不再重复 -->
    <div class="notice">
      ${ff.available ? `<div>${hwLine}</div>` : ''}
      <div>比对方式：先比头尾各 4KB，再整份逐字节校验（${esc(hash.algo || 'BLAKE3-256')}）。</div>
    </div>
    <div class="card" id="setForm">
      <div class="set-subnav" role="tablist">${SET_TABS.map(([k, label]) => `<button class="set-tab${(S.setPane || 'perf') === k ? ' on' : ''}" data-act="set-pane" data-pane="${k}" role="tab" aria-selected="${(S.setPane || 'perf') === k}">${label}</button>`).join('')}</div>
      <section class="set-pane" data-pane="perf"${(S.setPane || 'perf') === 'perf' ? '' : ' hidden'}><div class="set-col">
        <div class="set-title">扫描与性能<button class="helpbtn" data-act="help-link" data-sec="perf" title="看这一组怎么用" aria-label="查看扫描与性能的说明">?</button></div>
        <div class="field"><label>最小文件大小（KB）</label><input id="sMin" type="number" min="0" step="1" value="${minKB}"><span class="help">比这个小的文件直接跳过。想查重复的小图就填 0</span></div>
        <div class="field"><label>同时扫描几个文件</label><input id="sWorkers" type="number" min="1" max="32" value="${c.hash_workers||4}"><span class="help">数字越大越快，但机械硬盘开太大反而更慢，一般 4 就够</span></div>
        <div class="field"><label>每部视频抽几帧</label><select id="sVideoDensity"><option value="low"${c.video_density==='low'?' selected':''}>少（3 帧，最快）</option><option value="medium"${c.video_density!=='low'&&c.video_density!=='high'?' selected':''}>中（6 帧，推荐）</option><option value="high"${c.video_density==='high'?' selected':''}>多（10 帧，查得最狠）</option></select><span class="help">抽得越多，越容易发现「换过音轨」「中间剪过」的重复；第一次分析也更慢。改完不用重扫，新老结果能直接一起比</span></div>
        <div class="field"><label>同时分析几个视频</label><input id="sVWorkers" type="number" min="1" max="4" value="${c.video_workers||2}"><span class="help">用核显加速时建议 1–2；纯 CPU 可以调高，但别把机器占满</span></div>
        <div class="field"><label>跳过片头（秒）</label><input id="sIgnoreIntro" type="number" min="0" max="3600" value="${c.ignore_intro_seconds||0}"><span class="help">很多转载视频开头有统一片头。填上秒数，比较时跳过这一段，只看正片</span></div>
        <div class="field"><label>跳过片尾（秒）</label><input id="sIgnoreOutro" type="number" min="0" max="3600" value="${c.ignore_outro_seconds||0}"><span class="help">跳过片尾字幕、彩蛋那一段</span></div>
        <div class="field"><label>片头只比前几秒</label><input id="sIntroSec" type="number" min="1" max="300" value="${c.intro_seconds||10}"><span class="help">1–300 秒。改完到结果页切到「相同片头」类型，点「按当前 N 重新分析」即可，不用整库重扫</span></div>
        <div class="field"><label>视频工具路径（ffmpeg）</label><input id="sFF" placeholder="留空自动找，一般是 /usr/bin/ffmpeg" value="${esc(c.ffmpeg||'')}"></div>
        <div class="field"><label>视频信息工具路径（ffprobe）</label><input id="sFP" placeholder="留空自动找，一般是 /usr/bin/ffprobe" value="${esc(c.ffprobe||'')}"></div>
      </div></section>
      <section class="set-pane" data-pane="schedule"${(S.setPane || 'perf') === 'schedule' ? '' : ' hidden'}><div class="set-col">
        <div class="set-title">后台任务与硬盘休眠<button class="helpbtn" data-act="help-link" data-sec="schedule" title="看这一组怎么用" aria-label="查看后台任务与硬盘休眠的说明">?</button></div>
        <div class="field"><label>定时全量重扫（小时）</label><input id="sRescan" type="number" min="0" value="${c.full_rescan_hours||0}"${c.sleep_friendly !== false ? ' disabled' : ''}><span class="help" id="sRescanHelp">${c.sleep_friendly !== false ? '「硬盘休眠友好」开着，这项不运行（文件改动照样会更新）' : '0 表示关闭。平时靠实时监控就够，一般不用开'}</span></div>
        <div class="field"><label>监控失效时定时看一眼（分钟）</label><input id="sPoll" type="number" min="0" value="${c.poll_minutes||0}"><span class="help">系统同时盯的文件数有上限，超了就不再实时通知，改成每隔几分钟看一遍。0 表示不看</span></div>
        <div class="field"><label>硬盘休眠友好</label><label class="checkline"><input type="checkbox" id="sSleepFriendly"${c.sleep_friendly !== false ? ' checked' : ''}> 不做主动扫描，让硬盘该睡就睡</label><span class="help">开着的时候：定时重扫和轮询扫描都停掉，只靠系统事件跟着文件改动走，不会把睡着的硬盘叫醒</span></div>
        <div class="field"><label>静默时段</label><input id="sQuietHours" placeholder="例如 23:00-07:00，留空表示不限制" value="${esc(c.quiet_hours||'')}"><span class="help" id="quietHint">这段时间后台任务一律不启动；你手动点的扫描不受影响</span></div>
        <div class="field"><label>没人用时自动检查损坏文件</label><label class="checkline"><input type="checkbox" id="sBrokenAuto"${c.broken_auto === true ? ' checked' : ''}> 闲着的时候一批一批地查，查完就停</label><span class="help">默认关闭。第一次全量要跑几个小时，之后只查新增和改过的文件；「硬盘休眠友好」开着时会自动让路</span></div>
        <div class="field"><label>多久没人用算闲置（分钟）</label><input id="sBrokenIdle" type="number" min="1" max="720" value="${c.broken_idle_minutes||10}"><span class="help">这段时间内没有打开过这个页面，才算没人用</span></div>
        <div class="field"><label>一次检查多少个</label><input id="sBrokenBatch" type="number" min="10" max="5000" value="${c.broken_batch||200}"><span class="help">查完这一批就退出，下次接着来；磁盘正忙时会自动让路</span></div>
        <div class="field"><label>每天定时唤醒硬盘补一次</label><label class="checkline"><input type="checkbox" id="sWakeEnabled"${c.wake_enabled === true ? ' checked' : ''}> 在下面的时间段里叫醒硬盘对账</label><span class="help"><b>默认关闭，开了会叫醒硬盘。</b>开启后每天在设定时段里补一次增量，保证打开软件就能直接勾选；时段结束自动停，硬盘可以继续睡</span></div>
        <div class="field"><label>唤醒时段</label><input id="sWakeWindow" placeholder="04:00-08:00" value="${esc(c.wake_window || '04:00-08:00')}"><span class="help">格式 时:分-时:分，可以跨零点；建议放在夜里最闲的时候</span></div>
        <div class="checkline"><input id="sWatch" type="checkbox" ${c.watch?'checked':''}><label for="sWatch">实时监控文件变化</label><span class="hint">文件一有增删改就更新，不用等下一轮扫描。<b>注意：从关到开并保存设置，会立刻自动触发一次补扫</b>，大库下会占用一段时间磁盘</span></div>
      </div></section>
      <section class="set-pane" data-pane="basis"${(S.setPane || 'perf') === 'basis' ? '' : ' hidden'}><div class="set-col">
        <div class="set-title">查重依据与判定<button class="helpbtn" data-act="help-link" data-sec="basis" title="看这一组怎么用" aria-label="查看查重依据的说明">?</button></div>
        <div class="field"><label>搜索规则（DC5 式组合）</label>
          <select id="sRuleBase">
            ${Object.entries(SEARCH_BASE_LABELS).map(([v, label]) => `<option value="${esc(v)}"${(c.search_rules && c.search_rules.base === v) ? ' selected' : (c.search_rules ? '' : (deriveSearchRules(c.match_mode || 'all').base === v ? ' selected' : ''))}>${esc(label)}</option>`).join('')}
          </select>
          <div class="wbasis" style="margin-top:8px">${Object.entries(SEARCH_ATTR_LABELS).map(([k, label]) => `<div class="wopt mini"><label><input type="checkbox" data-act="srule-attr" data-attr="${esc(k)}" ${((c.search_rules && c.search_rules.attrs) || []).includes(k) ? 'checked' : ''}> ${esc(label)}</label></div>`).join('')}</div>
          <div class="asrow" style="flex-wrap:wrap;gap:8px;margin-top:8px">
            <span class="hint">大小容差</span><input id="sRuleSizeTol" type="number" min="0" value="${Number((c.search_rules && c.search_rules.size_tolerance_kb) || 0)}" style="width:90px"> <span class="hint">KB（0=完全相同）</span>
            <span class="hint">相似名阈值</span><input id="sRuleSimilar" type="number" min="50" max="100" value="${Number((c.search_rules && c.search_rules.name_similar_pct) || 90)}" style="width:70px"> <span class="hint">%</span>
          </div>
          <div class="asrow" style="flex-wrap:wrap;gap:8px;margin-top:8px">
            <span class="hint">文件夹名称口径</span>
            <select id="sRuleFolderMode" style="width:170px">
              <option value="full"${(!c.search_rules || c.search_rules.folder_name_mode === 'full') ? ' selected' : ''}>匹配完整名称</option>
              <option value="depth"${c.search_rules && c.search_rules.folder_name_mode === 'depth' ? ' selected' : ''}>从顶部匹配深度</option>
              <option value="scan_root"${c.search_rules && c.search_rules.folder_name_mode === 'scan_root' ? ' selected' : ''}>从搜索根目录匹配</option>
            </select>
            <span class="hint">深度</span><input id="sRuleFolderDepth" type="number" min="1" max="50" value="${Number((c.search_rules && c.search_rules.folder_name_depth) || 1)}" style="width:64px">
          </div>
          <div class="asrow" style="flex-wrap:wrap;gap:14px;margin-top:8px">
            <label class="checkline"><input id="sRuleIgnoreCopy" type="checkbox" ${c.search_rules && c.search_rules.ignore_copy_names ? 'checked' : ''}> 忽略带「副本」的文件名</label>
            <label class="checkline"><input id="sRuleIgnoreSame" type="checkbox" ${c.search_rules && c.search_rules.ignore_same_folder ? 'checked' : ''}> 忽略同一文件夹中的重复组</label>
            <label class="checkline"><input id="sRuleCase" type="checkbox" ${c.search_rules && c.search_rules.case_sensitive ? 'checked' : ''}> 区分大小写</label>
          </div>
          <span class="help">条件之间是「同时满足」。只选「忽略内容」时必须至少勾一个属性条件；「按文件大小」不读文件内容。规则只影响搜索分组，不改扫描期已算好的指纹。</span></div>
        <div class="field"><label>规则预设</label>
          <div id="sPresetList">${presetListHtml((c.search_presets) || [])}</div>
          <div class="inputrow" style="margin-top:6px"><input id="sPresetName" maxlength="64" placeholder="预设名称，如 纯大小去重"><button class="btn" data-act="preset-save">保存当前规则为预设</button></div>
          <span class="help">预设只存本地配置，不建数据库表；点「应用」把规则填回上方，再点保存设置才生效。</span></div>
        <div class="checkline"><input id="sContent" type="checkbox" ${c.content_enabled!==false?'checked':''}><label for="sContent">精确内容查重</label><span class="hint">整份文件逐字节比对，完全一样才算重复</span></div>
        <div class="checkline"><input id="sName" type="checkbox" ${c.name_enabled!==false?'checked':''}><label for="sName">同名文件分组</label><span class="hint">只按文件名分组，内容不一定一样，用来整理同名副本</span></div>
        <div class="checkline"><input id="sDupFolders" type="checkbox" ${c.compute_dup_folders!==false?'checked':''}><label for="sDupFolders">计算重复文件夹</label><span class="hint">关掉后不计算这一页，打开更快；扫描速度不受影响</span></div>
        <div class="checkline"><input id="sVideo" type="checkbox" ${c.video_enabled?'checked':''}><label for="sVideo">视频相似检测</label><span class="hint">按画面和声音判断，能认出转载、二次压缩的视频</span></div>
        <div class="checkline"><input id="sIntro" type="checkbox" ${c.intro_enabled?'checked':''}><label for="sIntro">相同片头检测</label><span class="hint">只比开头 N 秒，找共用片头但正片不同的视频</span></div>
        <div class="checkline"><input id="sPhoto" type="checkbox" ${c.photo_enabled!==false?'checked':''}><label for="sPhoto">相似图片检测</label><span class="hint">支持 jpg/png/gif/bmp/tiff/webp。<b>HEIC/AVIF/JXL/RAW/SVG 不支持</b>——iPhone 默认拍的 HEIC 要先转成 jpg 再放进来</span></div>
        <div class="checkline"><input id="sTraditional" type="checkbox" ${c.traditional_enabled!==false?'checked':''}><label for="sTraditional">繁体文件名检测</label><span class="hint">在「文件健康」页里查，查完可以一键转简体。日文汉字可能误判，介意就关掉</span></div>
        <div class="field"><label>临时文件名单</label><textarea id="sTempPatterns" placeholder=".tmp .bak 一行一个">${esc((c.temp_patterns||[]).join('\n'))}</textarea><span class="help">文件健康页按这份名单抓临时文件。以 . 开头表示按扩展名（x.tar.tmp 会命中 .tmp）；~ 表示编辑器备份；其余按完整文件名精确匹配，比如 Thumbs.db、desktop.ini。清空表示不查这一类。浏览器那种「(1)」「(2) 副本」删错了很麻烦，所以默认不放进来，需要可以自己加</span></div>
        <div class="field"><label>智能保留偏好学习</label><div class="inputrow"><span class="hint" id="prefStats">加载中…</span><button class="btn ghost" data-act="pref-clear">重置学习数据</button></div>
          <label class="checkline"><input type="checkbox" id="prefApply" data-act="pref-apply"> 让学习结果影响推荐权重</label>
          <span class="help" id="prefProfile">—</span>
          <span class="help">记录你对「智能保留建议」的实际选择（采纳了还是改选了），以后按你的习惯调整推荐。不足 20 条时仍用默认权重。</span></div>
        <div class="field"><label>AI 生成保留理由（可选）</label>
          <label class="checkline"><input type="checkbox" id="sAIEnabled"${c.ai_enabled === true ? ' checked' : ''}> 启用本地 AI（OpenAI 兼容接口）</label>
          <div class="inputrow"><input id="sAIEndpoint" placeholder="http://127.0.0.1:11434" value="${esc(c.ai_endpoint || '')}"><input id="sAIModel" placeholder="llama3.2" value="${esc(c.ai_model || 'llama3.2')}"></div>
          <span class="help">默认关闭。只把文件名、尺寸、时长发给这个地址，生成一句「为什么留这个」的说明；<b>不参与保留判定，也不会自动调用</b>。本地 Ollama 填 http://127.0.0.1:11434 即可；云端接口请自行评估隐私风险。</span></div>
        <div class="field"><label>文件名噪声词表</label><textarea id="sNoiseWords" placeholder="一行一个，如 1080p、x264、chs">${esc((c.noise_words||[]).join('\n'))}</textarea><span class="help">语义分组比较文件名前，会先把这些词去掉（分辨率、编码、片源、字幕等）。纯数字序号和一长串哈希会自动忽略，不用写。改完重新打开语义分组页生效</span></div>
        <div class="checkline"><input id="sAudioEnabled" type="checkbox" ${c.audio_enabled!==false?'checked':''}><label for="sAudioEnabled">重复音频检测</label><span class="hint">支持 mp3/flac/m4a/aac/ogg/wav/wma/ape。每个文件都要按下面的比较时长读一段音频，曲库大的话第一次会比较久</span></div>
        <div class="field"><label>音频相似度门槛</label><input id="sAudio" type="number" min="0.5" max="1" step="0.01" value="${c.audio_threshold||0.82}"><span class="help">默认 0.82，数字越大越严格；2 分钟/全片档会自动收紧一点</span></div>
        <div class="field"><label>音频比较时长</label>
          <select id="sAudioWindow">
            <option value="15"${(c.audio_window_seconds === 15 || c.audio_window_seconds === undefined) ? ' selected' : ''}>前 15 秒（推荐，最快）</option>
            <option value="120"${c.audio_window_seconds === 120 ? ' selected' : ''}>前 2 分钟（更准）</option>
            <option value="0"${c.audio_window_seconds === 0 ? ' selected' : ''}>全片（最准，最慢）</option>
          </select>
          <span class="help">改档会清空旧音频指纹，下次扫描重算；不影响视频与图片指纹</span></div>
        <div class="field"><label>视频画面容差（0–64）</label><input id="sPhash" type="number" min="0" max="64" value="${c.phash_hamming||10}"><span class="help">相似视频和相同片头用。数字越大越宽松，越小越严格</span></div>
        <div class="field"><label>图片画面容差（0–64）</label><input id="sImgHamming" type="number" min="0" max="64" value="${c.img_hamming ?? 10}"><span class="help">只影响相似图片页。数字越大越宽松，越小越严格；已经算好的不用重算</span></div>
        <div class="checkline"><label>扩展名统一写法</label><select id="sUnifyExt" style="margin:0 var(--space-xs)"><option value="off"${c.unify_ext!=='jpg'&&c.unify_ext!=='jpeg'?' selected':''}>不动（默认）</option><option value="jpg"${c.unify_ext==='jpg'?' selected':''}>统一成 .jpg</option><option value="jpeg"${c.unify_ext==='jpeg'?' selected':''}>统一成 .jpeg</option></select><span class="hint">把 jpeg/jpe/jfif/tiff/divx/dib/htm/mpeg 这些少见的写法改成标准写法。<b>默认关闭</b>——网页、播放列表、脚本按路径引用会断链，媒体库会重建缩略图、播放进度归零。建议只在纯素材归档目录开。mkv/mp4/ts/m4a 这类含义不同的扩展名不会动。</span></div>
        <div class="checkline"><input id="sHW" type="checkbox" ${c.hardware_decode!==false?'checked':''}><label for="sHW">用核显加速视频分析</label><span class="hint">核显不可用时自动退回 CPU，不会失败</span></div>
      </div></section>
      <section class="set-pane" data-pane="paths"${(S.setPane || 'perf') === 'paths' ? '' : ' hidden'}><div class="set-col">
        <div class="set-title">路径、目录与安全<button class="helpbtn" data-act="help-link" data-sec="paths" title="看这一组怎么用" aria-label="查看路径与目录的说明">?</button></div>
        <div class="field"><label>扫描目录</label>
          <div id="sVolsList">${(c.volumes||[]).map(v => `<div class="scanrow"><span class="mono scanpath" title="${esc(v)}">${esc(v)}</span><button class="scanx" data-act="scan-del" data-path="${esc(v)}" title="删除该目录">×</button></div>`).join('') || '<span class="hint">还没有添加目录</span>'}</div>
          <div class="asrow" style="margin-top:var(--space-2xs)"><button class="btn" data-act="pick-scan-dirs">选择文件夹</button><button class="btn ghost" data-act="scan-clear">清空</button></div>
          <span class="help">只扫这里列出的文件夹。飞牛只允许读被授权过的目录，所以先在「飞牛系统设置 → 应用 → 查重助手」里授权，再回来添加；没授权的目录在目录树里会显示锁</span></div>
        <div class="field"><label>不扫描的文件夹</label><textarea id="sExclPaths" placeholder="/vol1/1000/private">${esc(exclPaths)}</textarea>
          <div class="asrow" style="margin-top:var(--space-2xs)"><button class="btn" data-act="pick-excl-dirs">选择文件夹</button><button class="btn ghost" data-act="excl-clear">清空</button></div>
          <span class="help">整个文件夹连同子文件夹都不扫，也不会出现在结果里。每行一个绝对路径；也可以点「选择文件夹」从目录树里多选</span></div>
        <div class="field"><label>受保护的文件夹</label><textarea id="sProtect" placeholder="/vol1/1000/珍贵照片">${esc(protect)}</textarea>
          <div class="asrow" style="margin-top:var(--space-2xs)"><button class="btn" data-act="pick-protect-dirs">选择文件夹</button><button class="btn ghost" data-act="protect-clear">清空</button></div>
          <span class="help">和「不扫描」不一样：照样扫描、照样显示，但删不掉——移入隔离区和永久删除都会被拦住。隔离区本身永远受保护。每行一个绝对路径；也可以点「选择文件夹」从目录树里多选</span></div>
        <div class="field"><label>导出时的路径换算</label><textarea id="sPathMaps" placeholder="/vol1/media = \\\\FNOS\\media&#10;/vol1/1000/电影 = Z:////电影">${esc(pathMaps)}</textarea><span class="help">只在导出 DC5 格式时生效。把 NAS 路径换成 Windows 能看到的共享路径或盘符，Duplicate Cleaner 才能打开文件、显示缩略图。每行一条「NAS 路径 = 外部路径」，配多条时按最长前缀匹配；没配到的保持原样。导入 DC5 清单时会自动换回来</span></div>
        <div class="field"><label>按名字排除的文件夹</label><textarea id="sExcl">${esc(excl)}</textarea><span class="help">只写文件夹名，不管在哪一层都会被跳过，比如 @eaDir、#recycle。每行一个</span></div>
        <div class="field"><label>日志存放位置</label><div class="inputrow"><input id="sLogDir" placeholder="留空用数据目录下的 logs" value="${esc(c.log_dir||'')}"><button class="btn" data-act="pick-log-dir">选择</button></div><span class="help">现在写在：${esc(paths.log_file||'未启用')}。单个文件到 10MB 自动换新，保留最近 5 个</span></div>
        <div class="field"><label>操作记录（谁在什么时候做了什么）</label>
          <div class="inputrow">
            <label class="hint"><input type="checkbox" id="sOpLogEnabled" ${c.op_log_enabled!==false?'checked':''}> 记录所有写操作</label>
            <label class="hint">保留 <input id="sOpLogDays" type="number" min="0" max="3650" step="30" value="${c.op_log_retention_days!=null?c.op_log_retention_days:180}" style="width:88px"> 天（0=永久）</label>
            <label class="hint">明细上限 <input id="sOpLogLimit" type="number" min="0" max="1000" step="50" value="${c.op_log_detail_limit!=null?c.op_log_detail_limit:200}" style="width:88px"></label>
            <label class="hint"><input type="checkbox" id="sOpLogReads" ${c.op_log_reads?'checked':''}> 也记录只读请求</label>
          </div>
          <span class="help">操作记录存在数据库里，不受上面日志文件轮转影响，可在「操作记录」页查看和导出 CSV。永久删除、清空隔离区的记录不受总开关影响；记录写不进去时会直接拒绝删除。只读请求数量很大，默认关闭。</span></div>
        <div class="field"><label>缩略图存放位置</label><div class="inputrow"><input id="sCacheDir" placeholder="留空用数据目录下的 cache" value="${esc(c.cache_dir||'')}"><button class="btn" data-act="pick-cache-dir">选择</button></div><span class="help">现在存在：${esc(paths.cache_dir||'未设置')}。换位置后旧缩略图不会自动搬过去，会重新生成</span></div>
        <div class="field"><label>缩略图可用内存（MB）</label><input id="sMemBudget" type="number" min="0" max="65536" step="64" value="${c.mem_budget_mb||0}"><span class="help">0 表示按机器内存自动算（32G 的机器给 4096）。只影响缩略图这类内存缓存，超了自动淘汰最旧的，不会把内存撑爆</span></div>
        <div class="field"><label>缩略图占用磁盘上限（MB）</label><input id="sThumbMax" type="number" min="0" max="4096" step="64" value="${c.thumb_cache_max_mb!=null?c.thumb_cache_max_mb:256}"><span class="help">0 表示不限制。超过上限会删最旧的缩略图，需要时再生成</span></div>
        <div class="field"><label>访问令牌</label>
          <span class="help">当前状态：<b>${S.status && S.status.auth && S.status.auth.required ? '已设置，打开页面要先登录' : '未设置，局域网免登录'}</b>。</span>
          <div class="inputrow" style="margin-top:6px">
            <input id="sTokenView" readonly placeholder="点「查看」显示当前令牌">
            <button class="btn" data-act="token-view"${S.status && S.status.auth && S.status.auth.required ? '' : ' disabled'}>查看</button>
          </div>
          <span class="help">首次启动会自动生成令牌，登录前可在数据目录的 access-token.txt 查看；这里显示的是当前生效值，请勿随意分享。</span>
          <input id="sToken" type="password" autocomplete="new-password" placeholder="输入新令牌（不修改请留空）" style="margin-top:6px">
          <input id="sTokenConfirm" type="password" autocomplete="new-password" placeholder="再次输入新令牌" style="margin-top:6px">
          <label class="checkline" style="margin-top:6px"><input type="checkbox" id="sTokenClear"${S.status && S.status.auth && S.status.auth.required ? '' : ' disabled'}> 清除现有令牌，改为免登录</label>
          <span class="help">设置或清除都要在保存时二次确认；改了之后旧的登录状态会失效，要用新令牌重新登录。</span>
        </div>
      </div></section>
      <div class="form-actions" id="saveSettingsRow"${['perf','schedule','basis','paths'].includes(S.setPane || 'perf') ? '' : ' hidden'}><button class="btn primary" data-act="save-settings">保存设置</button><span class="hint" id="saveHint"></span></div>
    </div>
    <div class="card set-pane" data-pane="tags"${(S.setPane || 'perf') === 'tags' ? '' : ' hidden'}><div class="set-title">元数据标签（辅助分组）</div>
      <div class="hint">按「目录名」或「文件名规则」自动给每个文件打标签，把同一剧集、同一系列的文件聚在一起。只做辅助查看，不参与删除判定，删不删仍按内容查重结果走。</div>
      <div id="tagList"><span class="hint">加载中…</span></div>
      <div id="tagGroups"></div>
      <div class="field"><label>标签名</label><input id="tagName" maxlength="32" placeholder="如 剧集名、年份"><span class="help">最多 32 个字符。同名标签再次保存会覆盖规则。</span></div>
      <div class="field"><label>匹配模式</label><select id="tagMode">
        <option value="exact">精确：值完全相同才算一组</option>
        <option value="similar">相似文本：容错空格、错字、子串</option>
        <option value="exact_ws">含空白变体：忽略空格差异</option>
        <option value="display_only">仅显示：不打散成组</option>
      </select><span class="help">「相似文本」会把「某剧集」和「某剧集 第二季」认成一组；「仅显示」只记录标签值，不聚组。</span></div>
      <div class="field"><label>取值来源</label><select id="tagSource">
        <option value="dirname">目录名（文件所在文件夹名）</option>
        <option value="filename_regex">文件名规则（正则第一个括号）</option>
      </select><span class="help">目录名最省事：同一个文件夹里的文件算同一个标签值。</span></div>
      <div class="field" id="tagPatternField" style="display:none"><label>文件名规则</label><input id="tagPattern" placeholder="例如 ^(.+?)\.S\d{2}E\d{2}"><span class="help">用第 1 个括号里的内容作为标签值，例如上面这条会把「某剧集.S01E02.mkv」认成「某剧集」。</span></div>
      <div class="form-actions"><button class="btn primary" data-act="tag-add">保存标签</button><span class="hint" id="tagHint"></span></div>
    </div>
    <div class="card set-pane" data-pane="cleanup"${(S.setPane || 'perf') === 'cleanup' ? '' : ' hidden'}><div class="set-title">定时清理计划<button class="helpbtn" data-act="help-link" data-sec="schedule" title="看这一组怎么用" aria-label="查看定时清理计划的说明">?</button></div>
      <div class="hint">到点自动把重复组里多余的文件移入隔离区，<b>不会永久删除</b>；每组至少保留一个，受保护目录会被拦下。扫描或健康检测在跑时自动顺延到下一次检查。</div>
      <div id="cleanupList" style="margin:var(--space-xs) 0">${cleanupPlanListHtml()}</div>
      <div class="asrow" style="flex-wrap:wrap;margin-bottom:var(--space-sm)">
        <button class="btn ghost" data-act="cleanup-tpl" data-tpl="video">模板：每周清理重复视频</button>
        <button class="btn ghost" data-act="cleanup-tpl" data-tpl="exact">模板：每天清理精确重复</button>
        <button class="btn ghost" data-act="cleanup-tpl" data-tpl="name">模板：每周清理同名文件</button>
      </div>
      <div class="settings-grid">
        <div class="field"><label>计划名称</label><input id="cName" maxlength="64" placeholder="如 每周清理重复视频"></div>
        <div class="field"><label>执行时间</label><input id="cSchedule" class="mono" placeholder="weekly fri 05:00"><span class="help">daily 03:00 / weekly fri 05:00 / interval 86400（秒，最小 60）</span></div>
        <div class="field"><label>查重类型</label><select id="cMode">
          <option value="all">全部类型</option><option value="exact">精确重复</option><option value="name">同名文件</option><option value="video">相似视频</option><option value="audio">重复音频</option>
        </select><span class="help">「全部类型」= 精确 + 同名 + 相似视频 + 重复音频；建议先用模板逐步试。媒体类规则（视频 / 片头 / 音频 / 图片 / 文件夹）暂不参与清理计划，执行时按这里选的类型分组。</span></div>
        <div class="field"><label>最小文件大小（KB）</label><input id="cMinSize" type="number" min="0" value="0"><span class="help">比这个小的文件不参与自动清理</span></div>
        <div class="field"><label>组内最少文件数</label><input id="cMinCount" type="number" min="0" max="1000" value="2"><span class="help">0 表示按 2 处理；填 3 表示只清理 3 个以上重复的组</span></div>
        <div class="field"><label>只看这些扩展名</label><input id="cExt" placeholder="mp4,mkv，留空不限"><span class="help">逗号分隔，不带点</span></div>
        <div class="field"><label>保留哪一个</label><select id="cStrategy">
          <option value="modify_newest">保留修改时间最新的</option><option value="largest_size">保留体积最大的</option><option value="best_codec">保留分辨率/码率最高的</option>
        </select></div>
        <div class="field"><label>单次最多移入（GB）</label><input id="cMaxGB" type="number" min="0" value="10"><span class="help">0 表示不限；到上限就停，下次继续</span></div>
      </div>
      <div class="form-actions"><button class="btn primary" data-act="cleanup-save">保存计划</button><span class="hint" id="cleanupHint"></span></div>
    </div>
    <div class="card set-pane" data-pane="sync"${(S.setPane || 'perf') === 'sync' ? '' : ' hidden'}><div class="set-title">跨设备指纹包</div>
      <div class="hint">只交换文件指纹（路径 / 大小 / 哈希），<b>不传文件、不传凭据</b>，也不自动同步。导入后在结果页筛选器勾选「标记另一设备也有的文件」，命中行会显示徽标。</div>
      <div class="asrow" style="flex-wrap:wrap;margin:var(--space-xs) 0">
        <button class="btn" data-act="sync-export">导出本机指纹包</button>
        <button class="btn" data-act="sync-import-pick">导入另一台机器的指纹包</button>
        <input type="file" id="syncFile" data-act="sync-file" accept=".json,application/json" style="display:none">
      </div>
      <div id="syncPeers"><span class="hint">加载中…</span></div>
    </div>
    <div class="card set-pane" data-pane="cache"${(S.setPane || 'perf') === 'cache' ? '' : ' hidden'}><div class="set-title">缓存管理<button class="helpbtn" data-act="help-link" data-sec="cache" title="看这一组怎么用" aria-label="查看缓存管理的说明">?</button></div>
      <div id="cacheMediaHint"></div>
      <div id="cacheStats"><span class="hint">加载中…</span></div>
    </div>`;

  setTimeout(loadCacheStats, 0);
  setTimeout(loadPrefStats, 0);
  setTimeout(loadVideoTags, 0);
  setTimeout(loadCleanupPlans, 0);
  setTimeout(loadSyncPeers, 0);
}

// loadSyncPeers 拉取已导入的跨设备指纹包（v3.4.0）。
async function loadSyncPeers(){
  const box = $('syncPeers');
  if (!box) return;
  try {
    const r = /** @type {SyncPeersResp} */ (await api('/api/sync/peers'));
    const peers = r.peers || [];
    const list = peers.length
      ? peers.map(p => `<div class="scanrow"><span class="mono">${esc(p.machine)}</span><span class="hint">${esc(fmtT(Number(p.imported_at) * 1e9))} · ${p.row_count} 条</span><button class="btn ghost" data-act="sync-peer-del" data-machine="${esc(p.machine)}">删除</button></div>`).join('')
      : '<span class="hint">还没有导入其他设备的指纹包</span>';
    box.innerHTML = `<div class="hint">本机：<b>${esc(r.machine || '')}</b></div>` + list;
  } catch(e) {
    box.innerHTML = '<span class="hint">加载失败：' + esc(e && e.message || e) + '</span>';
  }
}

// v1.41.5：扫描目录行列表（每行可删）
function renderScanList(){
  const box = $('sVolsList');
  if (!box) return;
  const list = S.scanPaths || [];
  box.innerHTML = list.map(v => `<div class="scanrow"><span class="mono scanpath" title="${esc(v)}">${esc(v)}</span><button class="scanx" data-act="scan-del" data-path="${esc(v)}" title="删除该目录">×</button></div>`).join('') || '<span class="hint">尚未添加扫描目录</span>';
}

function addScanPaths(paths){
  S.scanPaths = S.scanPaths || [];
  (paths || []).forEach(p => { if (p && !S.scanPaths.includes(p)) S.scanPaths.push(p); });
  renderScanList();
}

// v2.7.0 元数据标签：定义标签 → 自动取标签值 → 按标签值辅助聚组。
// 只读展示，不提供任何删除按钮——标签分组不参与删除判定。
const TAG_MODE_LABELS = { exact:'精确', similar:'相似文本', exact_ws:'含空白变体', display_only:'仅显示' };

// ensureVideoTags 确保助手筛选器能拿到标签清单（v2.9.0）。
// 只拉一次；拉完后若助手正展开则重渲染，让标签下拉出现。
function ensureVideoTags(){
  if (S.videoTags || S.videoTagsLoading) return;
  S.videoTagsLoading = true;
  api('/api/video_tags').then(r => {
    S.videoTags = r.tags || [];
    S.videoTagsLoading = false;
    if (S.assistOpen) renderAssistant();
  }).catch(() => { S.videoTagsLoading = false; });
}

// selectPaths 把指定路径加入选择助手的选择集（v2.9.0）。
// 只作用于当前结果页已加载的组，找不到的文件会如实提示数量。
function selectPaths(paths){
  const want = new Set(paths || []);
  let n = 0;
  (S.groups || []).forEach(g => {
    // v3.11.1：批量选中同样只收可操作文件，坏路径不进选择集
    operableFiles(g).forEach(f => {
      if (!want.has(f.path)) return;
      if (!S.sel[g.key]) S.sel[g.key] = new Set();
      S.sel[g.key].add(f.path);
      n++;
    });
  });
  updateSelHint();
  renderGroups();
  toast(n ? ('已选中 ' + n + ' 个文件') : '当前结果页里没有这些文件', n ? 'ok' : 'err');
}

async function loadVideoTags(){
  const box = $('tagList'); if (!box) return;
  try {
    const r = /** @type {TagsResp} */ (await api('/api/video_tags'));
    const tags = r.tags || [];
    S.videoTags = tags;
    if (!tags.length) {
      box.innerHTML = '<span class="hint">还没有标签。在下面新建一个，比如「剧集名」用目录名取值。</span>';
      return;
    }
    const rows = tags.map(t =>
      '<tr><td>' + esc(t.name) + '</td>' +
      '<td>' + esc(TAG_MODE_LABELS[t.match_mode] || t.match_mode) + '</td>' +
      '<td>' + (t.value_source === 'filename_regex'
        ? '文件名规则 <span class="mono">' + esc(t.value_pattern) + '</span>'
        : '目录名') + '</td>' +
      '<td class="mono">' + (t.hits || 0) + ' 个文件</td>' +
      '<td><button class="btn" data-act="tag-groups" data-id="' + t.id + '">查看分组</button> ' +
      '<button class="btn ghost" data-act="tag-recompute" data-id="' + t.id + '">重算</button> ' +
      '<button class="btn ghost" data-act="tag-del" data-id="' + t.id + '">删除</button></td></tr>'
    ).join('');
    // v3.9.11：标签表也接入统一表头排序（命中按数值，操作列不排序）
    const tagCols = [
      {id:'vt-name', label:'标签', sort:'text'},
      {id:'vt-mode', label:'匹配模式', sort:'text'},
      {id:'vt-src', label:'取值来源', sort:'text'},
      {id:'vt-hits', label:'命中', sort:'num'},
      {id:'vt-act', label:'操作'}
    ];
    box.innerHTML = '<table class="ctable" data-sortkey="video-tags"><thead><tr>' + thCells(tagCols) + '</tr></thead><tbody>' + rows + '</tbody></table>';
    applyTableSort('video-tags');
  } catch (e) {
    box.innerHTML = '<span class="hint">标签加载失败：' + esc(e && e.message || e) + '</span>';
  }
}

async function saveVideoTag(){
  const nameEl = $('tagName'), modeEl = $('tagMode'), srcEl = $('tagSource'), patEl = $('tagPattern'), hint = $('tagHint');
  const name = nameEl ? nameEl.value.trim() : '';
  if (!name) { if (hint) hint.textContent = '先填标签名'; return; }
  if (name.length > 32) { toast('标签名不能超过 32 个字符', 'err'); return; }
  const src = srcEl ? srcEl.value : 'dirname';
  // v3.9.6 修 S-24：filename_regex 模式下 pattern 必填 + 合法 regex + 至少一个捕获组
  let pattern = '';
  if (src === 'filename_regex') {
    pattern = patEl ? patEl.value.trim() : '';
    if (!pattern) { toast('文件名规则不能为空', 'err'); return; }
    try { new RegExp(pattern); } catch(e) { toast('文件名规则不是合法的正则表达式：' + (e && e.message || e), 'err'); return; }
    if (!/\([^)]*\)/.test(pattern)) { toast('文件名规则需要至少一个括号，括号里的内容作为标签值', 'err'); return; }
  }
  const body = {
    name,
    match_mode: modeEl ? modeEl.value : 'exact',
    value_source: src,
    value_pattern: pattern,
  };
  try {
    const r = await postJSON('/api/video_tags', body);
    if (hint) hint.textContent = '已保存' + (r.count != null ? '，命中 ' + r.count + ' 个文件' : '');
    if (nameEl) nameEl.value = '';
    if (patEl) patEl.value = '';
    toast('标签「' + name + '」已保存', 'ok');
    await loadVideoTags();
  } catch (e) {
    if (hint) hint.textContent = '';
    toast('保存失败：' + (e && e.message || e), 'err');
  }
}

async function recomputeTagItem(id){
  try {
    const r = await postJSON('/api/video_tags/recompute?id=' + id, {});
    toast('重算完成，命中 ' + (r.count || 0) + ' 个文件', 'ok');
    await loadVideoTags();
  } catch (e) {
    toast('重算失败：' + (e && e.message || e), 'err');
  }
}

async function deleteTagItem(id){
  const tag = (S.videoTags || []).find(t => String(t.id) === String(id));
  if (!confirm('删除标签「' + (tag ? tag.name : id) + '」？只会删除标签定义和已记录的标签值，不影响任何文件。')) return;
  try {
    /** @type {TagsResp} */ (await api('/api/video_tags?id=' + id, { method: 'DELETE' }));
    const bg = $('tagGroups'); if (bg) bg.innerHTML = '';
    toast('标签已删除', 'ok');
    await loadVideoTags();
  } catch (e) {
    toast('删除失败：' + (e && e.message || e), 'err');
  }
}

async function viewTagGroups(id){
  const box = $('tagGroups'); if (!box) return;
  const tag = (S.videoTags || []).find(t => String(t.id) === String(id));
  box.innerHTML = '<span class="hint">分组计算中…</span>';
  try {
    const r = /** @type {GroupsResp} */ (await api('/api/groups/tag?tag_id=' + id + '&page=1&page_size=20'));
    const groups = r.groups || [];
    if (!groups.length) {
      box.innerHTML = '<span class="hint">「' + esc(tag ? tag.name : '') + '」还没有形成重复组：至少要有 2 个文件的标签值相同。</span>';
      return;
    }
    const head = '<div class="hint">「' + esc(tag ? tag.name : '') + '」共 ' + r.total + ' 组，按可回收空间排序。</div>';
    // v2.9.0：把每组的文件路径缓存下来，供「选中这些文件」按钮加入选择助手
    S.tagGroupPaths = groups.map(g => (g.files || []).map(f => f.path));
    const body = groups.map((g, idx) =>
      '<div class="field"><label>' + esc(g.name) + ' · ' + g.count + ' 个文件 · ' + fmtB(g.total_size) +
      ' <button class="btn ghost" data-act="tag-select" data-idx="' + idx + '" title="把这些文件加入选择助手的选中集">选中这些文件</button></label>' +
      g.files.slice(0, 5).map(f => '<div class="hint mono" title="' + esc(f.path) + '">' + esc(f.path) + '</div>').join('') +
      (g.files.length > 5 ? '<div class="hint">…还有 ' + (g.files.length - 5) + ' 个文件</div>' : '') +
      '</div>'
    ).join('');
    box.innerHTML = head + body;
  } catch (e) {
    box.innerHTML = '<span class="hint">分组加载失败：' + esc(e && e.message || e) + '</span>';
  }
}

async function loadPrefStats(){
  const el = $('prefStats'); if (!el) return;
  try {
    const st = /** @type {AnyResp} */ (await api('/api/retain/pref/stats'));
    el.textContent = '已学习 ' + (st.total || 0) + ' 条（采纳 ' + (st.adopt || 0) + '，改选 ' + (st.changed || 0) + '）';
  } catch(e) { el.textContent = '统计加载失败'; }
  const profEl = $('prefProfile');
  if (!profEl) return;
  try {
    const p = /** @type {AnyResp} */ (await api('/api/retain/pref/profile'));
    const w = p.weights || {};
    profEl.textContent = '当前权重：质量 ' + Number(w.quality || 0).toFixed(2)
      + ' · 路径 ' + Number(w.path_tidy || 0).toFixed(2)
      + (p.enabled ? '（学习已启用）' : '（学习未启用，用默认权重）')
      + ((p.learned || 0) >= 20 ? '' : '；满 20 条后才会调整权重');
    const cb = $('prefApply');
    if (cb) cb.checked = !!p.enabled;
  } catch(e) { profEl.textContent = '学习档案加载失败'; }
}

// loadCacheStats 拉取缓存占用并渲染（v1.31 缓存管理）。
async function loadCacheStats(){
  const box = $('cacheStats'), hint = $('cacheMediaHint');
  if (!box) return;
  try {
    const st = /** @type {CacheStatsResp} */ (await api('/api/cache/stats'));
    if (hint) {
      hint.innerHTML = st.ssd_hint
        ? '<div class="warnbox">当前缓存目录位于<b>机械硬盘</b>（' + esc(st.cache_dir) + '）。建议把缓存目录移到固态硬盘：修改上方「缩略图缓存目录」为 SSD 路径即可，旧缓存会按需自动重建。</div>'
        : '';
    }
    const rows = (st.classes || []).map(k =>
      '<tr><td>' + esc(k.label) + '</td><td class="mono">' + (k.count >= 0 ? k.count : '—') + ' 项</td><td class="mono">' + fmtB(k.bytes) + '</td><td>' +
      (k.clearable ? '<button class="btn" data-act="cache-clear" data-kind="' + esc(k.kind) + '">清理</button>' : '<span class="hint">随重新分析更新</span>') + '</td></tr>'
    ).join('');
    // v3.9.11：缓存表接入统一表头排序（数量按数值、占用按字节）
    const cacheCols = [
      {id:'cc-label', label:'缓存类别', sort:'text'},
      {id:'cc-count', label:'数量', sort:'num'},
      {id:'cc-size', label:'占用', sort:'size'},
      {id:'cc-act', label:'操作'}
    ];
    box.innerHTML = '<table class="ctable" data-sortkey="cache-stats"><thead><tr>' + thCells(cacheCols) + '</tr></thead><tbody>' + rows + '</tbody></table>' +
      '<div class="hint">内存预算：' + (st.mem_budget_auto ? '自动（' + esc(st.mem_budget_mb) + ' MB）' : esc(st.mem_budget_mb) + ' MB') +
      '；磁盘上限：' + (st.thumb_max_mb > 0 ? esc(st.thumb_max_mb) + ' MB' : '不限') + '。清理后相关功能自动回退到重算，不影响扫描数据。</div>';
    applyTableSort('cache-stats');
  } catch (e) {
    box.innerHTML = '<span class="hint">缓存统计加载失败：' + esc(e && e.message || e) + '</span>';
  }
}

const HEALTH_KINDS = [
  {key:'empty', label:'空文件', hint:'大小是 0 的文件；也可以再查一遍内容全是 0 或全是乱码的'},
  {key:'empty_dir', label:'空目录', hint:'里面什么都没有的文件夹；清理时移进隔离区，可以恢复'},
  {key:'ext', label:'扩展名不符', hint:'只看文件开头几个字节，判断真实类型和扩展名对不对得上'},
  {key:'broken', label:'损坏文件', hint:'视频音频试着解一小段，图片和压缩包检查结构是否完整。只报告，不主动建议删；特别大的文件会跳过并标注'},
  {key:'name', label:'文件名不规范', hint:'表情符号、首尾空格、重复标点；另外两条更严的规则默认关闭'},
  {key:'traditional', label:'繁体文件名', hint:'文件名里有繁体字。检测到以后可以勾选，整批转成简体（可撤销）；不需要就在设置页关掉这项检测'},
  {key:'temp', label:'临时文件', hint:'命中临时文件名单（.tmp/.bak/.part/Thumbs.db 等，名单在设置页可改）。默认只报告，清理会进隔离区，可以恢复'}
];

// v3.9.3：一次扫描全部类型。它不是可查询的结果类别，因此不放进 chips。
const HEALTH_ALL_KEY = 'all';
const HEALTH_ALL_HINT = '一次遍历同时检查上面 7 类问题，只需点一次；耗时比单类长，损坏文件仍按探测缓存续跑';

// v3.15.0：每个类别的原因清单，用于「按原因筛选」与「按原因全选」。
// advisory=true 表示没能得出结论（内容认不出 / 探测超时 / 超过检测上限）：
// 只作提示，默认不列出，也不允许勾选、改名或清理。
const HEALTH_REASONS = {
  empty: [
    {key:'zero_size', label:'零字节'},
    {key:'all_zero', label:'内容全为 0'},
    {key:'unprintable', label:'全为不可打印字符（文本类扩展名不参与）'}
  ],
  empty_dir: [],
  ext: [
    {key:'mismatch', label:'内容与扩展名不符'},
    {key:'uppercase', label:'大写扩展名'},
    {key:'multi_ext', label:'多重扩展名'},
    {key:'family', label:'同族归一'},
    {key:'unknown', label:'内容无法识别', advisory:true}
  ],
  broken: [
    {key:'no_moov', label:'缺少 moov'},
    {key:'invalid_data', label:'数据无效'},
    {key:'truncated', label:'文件被截断'},
    {key:'no_codec_params', label:'编码参数无法识别'},
    {key:'decode_failed', label:'解码失败'},
    {key:'image_broken', label:'图片无法解码'},
    {key:'zip_broken', label:'压缩包损坏'},
    {key:'json_broken', label:'JSON 语法错误'},
    {key:'xml_broken', label:'XML 结构错误'},
    {key:'timeout', label:'探测超时', advisory:true},
    {key:'oversized', label:'过大未检测', advisory:true}
  ],
  name: [
    {key:'emoji', label:'含 emoji'},
    {key:'space', label:'首尾空格'},
    {key:'multi_space', label:'多余空格'},
    {key:'repeat', label:'重复标点'},
    {key:'charset', label:'受限字符'},
    {key:'non_cjk', label:'非中日韩字符'},
    {key:'invalid_encoding', label:'编码异常'}
  ],
  traditional: [],
  temp: []
};

// HEALTH_ADVISORY_REASONS 与后端 health.AdvisoryReason 同口径，改一处必须改另一处。
const HEALTH_ADVISORY_REASONS = ['unknown', 'timeout', 'oversized'];

// healthItemReasons 取一条结果的完整原因；旧数据没有 reasons 时回退到单值 reason。
function healthItemReasons(it){
  if (it && Array.isArray(it.reasons) && it.reasons.length) return it.reasons;
  return (it && it.reason) ? [it.reason] : [];
}

// healthItemAdvisory 判断一条结果是否只是「无法确定」的提示项。
function healthItemAdvisory(it){
  return healthItemReasons(it).some(r => HEALTH_ADVISORY_REASONS.includes(r));
}

// healthReasonMeta 返回当前类别里某个原因的定义；没有匹配时返回 null。
function healthReasonMeta(key){
  const list = HEALTH_REASONS[healthState().kind] || [];
  return list.find(m => m.key === key) || null;
}

function healthKindLabel(key){
  if (key === HEALTH_ALL_KEY) return '全部类型';
  return healthKindMeta(key).label;
}

// v3.7.2：健康结果一页加载多少条。
// v3.10.2：后端单页上限放宽到 2 万，这里固定 2000 一页并发拉取——
// 单请求体积可控，整体耗时接近一次大查询，还能避免一条巨响应把服务端卡住。
const HEALTH_PAGE = 2000;

// v3.10.2：并发拉取的页数。4 路足够把千兆网与 SQLite 读满，再多只会互相等。
const HEALTH_PAGE_CONCURRENCY = 4;

// HEALTH_OP_BATCH 是批量修正/清理时每批提交的文件数（v3.10.2）。
// 拆批是为了让进度条能持续往前走；每批后端内部还会再并发处理。
const HEALTH_OP_BATCH = 500;

function healthState(){
  if (!S.health) {
    S.health = {
      kind:'empty', items:[], total:0, sel:new Set(), progress:null, counts:{},
      ffmpeg:false, loading:false, lastTask:0, lastTasks:[], auto:null, probeCache:null,
      opts:{content_mode:'none', restrict_charset:false, non_cjk:false},
      upperOnly:false,
      loadingMore:false,
      // v3.10.4：记录并发分页里失败的页起始 offset。旧实现续拉只看 items.length，
      // 失败页之后的条目已经合并进来，缺口就永远补不上。
      missing:[],
      complete:false,
      // 2026-11-08：上次整体同步结果列表的时间（毫秒）。检测期间列表不再每轮全量重拉，
      // 用它把「拉行」限制在低频，进度条则靠轻量汇总接口单独更新。
      lastListSync:0,
      // v3.15.0：按原因筛选（空串=全部）与「无法确定」条目的显示开关。
      // uncertain 是 status 接口给的各类不确定条数，界面用它把可处理数量与提示数量分开。
      reasonFilter:'',
      showUncertain:false,
      uncertain:{},
      // v3.10.2：批量操作（修正/清理/转简体）的运行态，用于进度条与完成页。
      op:null
    };
  }
  return S.health;
}

function healthKindMeta(key){
  return HEALTH_KINDS.find(k => k.key === key) || HEALTH_KINDS[0];
}

function healthRunning(){
  const h = healthState();
  return !!(h.progress && h.progress.status === 'running');
}

async function loadHealth(){
  const h = healthState();
  h.loading = true; renderHealth();
  try {
    const st = /** @type {HealthStatusResp} */ (await api('/api/health/status'));
    h.progress = st.progress || null;
    h.counts = st.counts || {};
    h.uncertain = st.uncertain || {};
    h.ffmpeg = !!st.ffmpeg;
    h.auto = st.auto || null;
    h.probeCache = st.probe_cache || null;
    if (st.last_audit) { h.lastTask = st.last_audit; h.lastTasks = [st.last_audit]; }
    // v3.10.3：首屏失败只影响列表，不把状态接口的其他信息一起丢掉。
    let r;
    try {
      r = /** @type {HealthItemsResp} */ (await api('/api/health/results?kind=' + encodeURIComponent(h.kind) + '&limit=' + HEALTH_PAGE));
    } catch(e) {
      toast('加载检测结果失败：' + e.message, 'err');
      renderHealth();
      return;
    }
    h.items = r.items || [];
    h.total = r.total || 0;
    h.sel.clear();
    // 原因 key 跨类别不通用，重新加载时必须清掉筛选，避免筛出空列表。
    h.reasonFilter = '';
    h.missing = [];
    h.complete = false;
    // v3.10.2：所有类别都一次显示全部条目，不再让用户为几万条反复点「加载更多」。
    // v3.10.3：某一页拉取失败只提示，保留已加载部分，不再清空整页列表。
    try {
      await loadAllHealth(h);
    } catch(e) {
      toast(e.message, 'err');
    }
  } catch(e) {
    toast(e.message, 'err');
  } finally {
    h.loading = false; renderHealth();
  }
}

// loadAllHealth 把当前类别的剩余分页并发拉完（v3.10.2 起所有类别都走这里）。
//
// 用偏移分页而不是放大 limit：单页 2000 体积可控，4 路并发整体耗时接近一次大查询。
// 结果按页序合并，保证每次进入页面顺序一致；同路径去重防止扫描中途结果变动导致重复。
async function loadAllHealth(h){
  const total = h.total;
  if (total <= 0 || h.items.length >= total) { h.complete = true; return; }
  const offsets = [];
  for (let off = h.items.length; off < total; off += HEALTH_PAGE) offsets.push(off);
  if (!offsets.length) { h.complete = true; return; }

  const pages = new Array(offsets.length);
  let cursor = 0;
  const worker = async () => {
    while (cursor < offsets.length) {
      const k = cursor++;
      try {
        const r = /** @type {HealthItemsResp} */ (await api('/api/health/results?kind=' + encodeURIComponent(h.kind) +
          '&limit=' + HEALTH_PAGE + '&offset=' + offsets[k]));
        pages[k] = r.items || [];
      } catch(e) {
        // 单页失败只留空，其余页继续合并，避免一页抖动导致整页空白（v3.10.3）。
        pages[k] = null;
      }
    }
  };
  const workers = Math.min(HEALTH_PAGE_CONCURRENCY, offsets.length);
  await Promise.all(Array.from({ length: workers }, worker));

  const seen = new Set(h.items.map(it => it.path));
  const missing = [];
  pages.forEach((batch, k) => {
    // v3.10.4：失败页记下起始 offset，后续「加载更多」按缺口补齐。
    if (!batch) { missing.push(offsets[k]); return; }
    batch.forEach(it => {
      if (!seen.has(it.path)) { h.items.push(it); seen.add(it.path); }
    });
  });
  h.missing = missing;
  h.complete = missing.length === 0;
  if (missing.length) {
    throw new Error(`有 ${missing.length} 页检测结果加载失败（从第 ${missing[0] + 1} 条起），可点「加载更多」补齐`);
  }
}

// loadMoreHealth 续拉健康结果：先补失败的缺口页，再按列表末端继续。
// v3.10.2 起进入页面就会并发拉完全部条目，正常情况下不会再看到「加载更多」；
// 这里保留是为了某一页临时失败时不至于整页空白，且能真正补齐中间缺口（v3.10.4）。
async function loadMoreHealth(){
  const h = healthState();
  if (h.loadingMore || h.complete) return;
  h.loadingMore = true; renderHealth();
  try {
    // v3.10.4：失败页必须按记录的 offset 重拉。旧实现用 items.length 当偏移，
    // 会重复拉已加载区间，失败那几百条永远补不回来。
    if (h.missing && h.missing.length) {
      const rest = [];
      for (const off of h.missing) {
        try {
          const r = /** @type {HealthItemsResp} */ (await api('/api/health/results?kind=' + encodeURIComponent(h.kind) +
            '&limit=' + HEALTH_PAGE + '&offset=' + off));
          appendHealthItems(h, r.items || []);
          h.total = r.total || h.total;
        } catch(e) { rest.push(off); }
      }
      h.missing = rest;
      if (!rest.length) h.complete = true;
      else toast(`还有 ${rest.length} 页加载失败，可再点一次`, 'err');
      return;
    }
    const r = /** @type {HealthItemsResp} */ (await api('/api/health/results?kind=' + encodeURIComponent(h.kind) +
      '&limit=' + HEALTH_PAGE + '&offset=' + h.items.length));
    const batch = r.items || [];
    appendHealthItems(h, batch);
    h.total = r.total || h.total;
    if (!batch.length || h.items.length >= h.total) h.complete = true;
  } catch(e) { toast(e.message, 'err'); }
  finally { h.loadingMore = false; renderHealth(); }
}

// appendHealthItems 按路径去重追加，避免分页边界重复。
function appendHealthItems(h, batch){
  const seen = new Set(h.items.map(it => it.path));
  batch.forEach(it => { if (!seen.has(it.path)) { h.items.push(it); seen.add(it.path); } });
}

let healthPoll = null;

// scheduleHealthPoll 在检测期间按自适应节奏刷新进度与结果。
// 2026-11-08 重写（原实现固定 2 秒、每轮拉最多 2000 条结果）：
//  - 检测本身已占满 CPU，前端这层负载会反过来拖慢后端，于是把「进度」和「列表」拆开：
//    每轮只拉轻量汇总 /api/health/status 并只重绘进度卡；结果行只在当前类别能一次拉完
//    （条数 ≤ 后端单次上限 2000）时同步，且 5 秒最多一次；大类别只更新计数，
//    扫描结束由 health 事件触发 loadHealth 整体重载；
//  - 节奏交给 startAdaptivePoll：有进展 2 秒，无进展退避到 10 秒，页面隐藏时彻底停；
//  - 接近完成（剩余 ≤20 条）时即便有进展也放慢，收尾交给 health 事件的 loadHealth。
function scheduleHealthPoll(){
  if (healthPoll && healthPoll.active) return;
  healthPoll = startAdaptivePoll(async () => {
    const h = healthState();
    if (!healthRunning() || S.tab !== 'health') { healthPoll = null; return false; }
    const before = Number((h.progress && h.progress.processed) || 0);
    let p = null;
    try {
      const st = /** @type {HealthStatusResp} */ (await api('/api/health/status'));
      p = st.progress || null;
      if (st.counts) h.counts = st.counts;
      if (st.uncertain) h.uncertain = st.uncertain;
    } catch(e) { return undefined; }
    if (p) {
      h.progress = p;
      // 当前类别总数直接取分类计数，不必为了一个数字去拉结果行
      const cnt = Number((h.counts || {})[h.kind] || 0);
      if (cnt > 0) h.total = cnt;
      // 任务已不在跑：交回 SSE 的收尾（loadHealth），轮询自己退出
      if (p.status !== 'running') { healthPoll = null; return false; }
    }
    const changed = Number((p && p.processed) || 0) > before;
    const now = Date.now();
    // 结果行：只有当前类别整体能塞进一次请求（≤ 后端单次上限）时才同步，
    // 且 5 秒最多一次；大类别拉不全，干脆只更新计数，扫描结束由 health 事件整体重载。
    const fits = h.total > 0 && h.total <= HEALTH_PAGE;
    if (fits && now - (h.lastListSync || 0) >= 5000) {
      try {
        const r = /** @type {HealthItemsResp} */ (await api('/api/health/results?kind=' + encodeURIComponent(h.kind) + '&limit=' + h.total));
        h.items = r.items || [];
        h.total = r.total || h.total;
        h.lastListSync = now;
        renderHealth();
        return changed ? true : undefined;
      } catch(e) { refreshHealthProgCard(); return undefined; }
    }
    refreshHealthProgCard();
    if (p && p.total > 0 && p.total - p.processed <= 20) return undefined;
    // 注意：false 在控制器里表示「结束」，无变化必须返回 undefined（退避）
    return changed ? true : undefined;
  }, { base: 2000, max: 10000 });
}

async function healthScan(all){
  const h = healthState();
  // 选项控件是即时渲染的，扫描前统一从 DOM 回读，避免状态分叉
  const cm = $('healthContentMode'); if (cm) h.opts.content_mode = cm.value;
  const rc = $('healthRestrict'); if (rc) h.opts.restrict_charset = rc.checked;
  const nc = $('healthNonCjk'); if (nc) h.opts.non_cjk = nc.checked;
  const fp = $('healthForce'); if (fp) h.forceProbe = fp.checked;

  if (healthRunning()) { toast('已有检测在进行中', 'err'); return; }
  // v3.12.0：ffmpeg 类任务（视频/片头分析、裁剪、转码）在跑时，损坏探测要等它们全部结束
  // 才开始（互斥调度）。不提示的话，用户会把「进度停在原处」当成卡死，反复重点。
  const st = S.status || {};
  if (st.ffmpeg_running && !confirm('视频分析、片头分析、裁剪或转码正在使用 FFmpeg。\n健康检测会等这些任务全部结束后才开始，期间进度可能长时间停在原处。\n\n仍要现在开始吗？')) return;
  try {
    await postJSON('/api/health/scan', {
      kind: all ? HEALTH_ALL_KEY : h.kind,
      content_mode: h.opts.content_mode,
      restrict_charset: h.opts.restrict_charset,
      non_cjk: h.opts.non_cjk,
      force: !!h.forceProbe
    });
    toast('已开始检测：' + (all ? '全部类型' : healthKindMeta(h.kind).label), 'ok');
    await loadHealth();
    scheduleHealthPoll();
  } catch(e) { toast(e.message, 'err'); }
}

// v3.12.0：非 UTF-8 文件名无法逐条勾选修正（路径经 JSON 编码后原始字节已变成 U+FFFD），
// 这里一次提交，由服务端从索引里自己找候选并按「文件名不规范」规则改名。
async function healthFixEncoding(){
  if (healthRunning()) { toast('检测进行中，请等结束再修复', 'err'); return; }
  if (!confirm('将找出索引里所有非 UTF-8 文件名并改名：\n优先按 GB18030 转成 UTF-8，解不出的字节转成 %XX，重名自动加序号。\n\n改名会记入操作记录，可用「撤销上次操作」还原。现在开始吗？')) return;
  try {
    const r = await postJSON('/api/health/fix-encoding', {});
    const done = Number(r && r.done || 0);
    const failed = Number(r && r.failed || 0);
    if (!done && !failed) toast('没有需要修复的文件名', 'ok');
    else toast('编码修复完成：成功 ' + done + ' 个' + (failed ? '，失败 ' + failed + ' 个' : ''), failed ? 'warn' : 'ok');
    await loadHealth();
  } catch(e) { toast(e.message, 'err'); }
}

async function healthCancel(){
  try { await postJSON('/api/health/cancel', {}); toast('已请求取消，已处理的结果会保留', 'ok'); }
  catch(e) { toast(e.message, 'err'); }
}

// 勾选变化时统一刷新「需要选中才能点」的按钮与已选计数，避免新增按钮忘记接线。
function syncHealthSel(){
  const h = healthState();
  const hint = $('healthSelHint');
  if (hint) hint.textContent = h.sel.size ? '已选 ' + h.sel.size + ' 个' : '';
  // v3.10.5：扫描/检测进行中即便勾选也要保持禁用，否则用户会以为能操作。
  const blocked = !!opsBlockedReason();
  document.querySelectorAll('[data-sel-needs]').forEach(b => { b.disabled = !h.sel.size || blocked; });
}

function healthProgressHtml(){
  const h = healthState();
  const p = h.progress;
  if (!p || !p.kind) return '';
  const pct = p.total > 0 ? Math.min(100, Math.round(p.processed * 100 / p.total)) : 0;
  const label = {running:'检测中', done:'已完成', canceled:'已取消', failed:'失败'}[p.status] || p.status;
  // .pbar>i 用 transform:scaleX(var(--p)) 控制长度，直接写 width 会被缩放成 0。
  const bar = p.status === 'running' ? `<div class="pbar" style="width:100%;margin:0 12px 10px"><i style="--p:${(pct / 100).toFixed(3)}"></i></div>` : '';
  // 2026-11-08：message 只在外层 esc 一次；这里再 esc 会让「<」显示成「&lt;」
  const extra = (p.errors ? ' · 错误 ' + p.errors : '') + (p.message ? ' · ' + p.message : '');
  // v3.15.0：长时间检测容易被误以为卡死，刷新页面会丢掉当前视图与进度，明确提示。
  const runningTip = p.status === 'running'
    ? '<span class="hint">执行期间请不要刷新或关闭网页</span>' : '';
  return `<div class="card" id="healthProgCard" style="margin-bottom:12px">
    <div class="toolbar">
      <span>${esc(healthKindLabel(p.kind))} · ${esc(label)} · ${p.processed}/${p.total || '?'}${esc(extra)}</span>
      <div class="spacer"></div>
      ${runningTip}
      ${p.status === 'running' ? '<button class="btn danger" data-act="health-cancel">取消</button>' : ''}
    </div>
    ${bar}
    ${p.current ? `<div class="hint mono" style="padding:6px 12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(p.current)}</div>` : ''}
  </div>`;
}

// refreshHealthProgCard 只替换检测进度卡，不重绘结果表（同 v3.10.2 的批量操作卡）。
// 2026-11-08：检测期间的轮询每 2 秒一次，整表重绘在几千行时会明显卡顿；
// 进度本来就是独立卡片，没必要连带重建列表。
function refreshHealthProgCard(){
  const card = $('healthProgCard');
  if (!card) { renderHealth(); return; }
  card.outerHTML = healthProgressHtml();
}

// healthOpHtml 展示批量操作（修正/清理/转简体）的进度（v3.10.2）。
// 与 healthProgressHtml 分开：一个是「检测」进度，一个是「改文件」进度，两者可能先后发生。
// v3.19.0：卡片统一由 helper.js 的批量操作组件渲染，暂停 / 取消 / 刷新逻辑也共用。
function healthOpHtml(){
  return batchedOpCardHtml('health');
}

// refreshHealthOpCard 只替换进度卡片，不重绘整张结果表（v3.10.2）。
// 健康页可能有上万行，每批都整表重绘会让浏览器明显卡顿。
function refreshHealthOpCard(){
  if (!refreshBatchedOpCard('health')) renderHealth();
}

function healthOptionsHtml(){
  const h = healthState();
  if (h.kind === 'empty') {
    const notice = h.opts.content_mode !== 'none'
      ? '<span class="hint">扩展模式会读取文件内容，可能耗时较长</span>' : '';
    return `<div class="toolbar">
      <span class="hint">内容检查</span>
      <select id="healthContentMode" style="max-width:240px;padding:6px 8px;border-radius:8px">
        <option value="none"${h.opts.content_mode === 'none' ? ' selected' : ''}>仅零字节（最快）</option>
        <option value="zeroed"${h.opts.content_mode === 'zeroed' ? ' selected' : ''}>额外查全零内容</option>
        <option value="unprintable"${h.opts.content_mode === 'unprintable' ? ' selected' : ''}>额外查全不可打印（按字符判断，文本类扩展名跳过）</option>
      </select>${notice}
    </div>`;
  }
  if (h.kind === 'name') {
    return `<div class="toolbar">
      <label class="checkline"><input type="checkbox" id="healthRestrict"${h.opts.restrict_charset ? ' checked' : ''}> 启用字符集白名单（中文仍放行）</label>
      <label class="checkline"><input type="checkbox" id="healthNonCjk"${h.opts.non_cjk ? ' checked' : ''}> 标记非中日韩字符</label>
      <span class="hint">默认关闭：中文文件名是合法的</span>
    </div>`;
  }
  if (h.kind === 'broken') {
    if (!h.ffmpeg) {
      return '<div class="warnbox">未找到 ffmpeg，视频与音频类无法检测（图片/zip/JSON/XML 不受影响）。请先在设置页确认 ffmpeg 路径。</div>';
    }
    const pc = h.probeCache || {total: 0, broken: 0};
    const auto = h.auto || {};
    const autoText = auto.enabled
      ? `闲时巡检：${esc(auto.message || '')}${auto.idle_for_seconds ? '（已闲置 ' + Math.round(auto.idle_for_seconds / 60) + ' 分钟）' : ''}`
      : '闲时巡检未开启（可在设置页打开）';
    return `<div class="toolbar">
      <label class="checkline"><input type="checkbox" id="healthForce"${h.forceProbe ? ' checked' : ''}> 重新探测已出结论的文件</label>
      <span class="hint">默认续跑：缓存命中且文件未变时不再调用 ffmpeg</span>
    </div>
    <p class="hint page-note">
      探测缓存已覆盖 <b>${pc.total}</b> 个视频${pc.broken ? '（其中异常 ' + pc.broken + ' 个）' : ''} · ${autoText}
    </p>`;
  }
  return '';
}

function renderHealth(){
  const h = healthState();
  const running = healthRunning();
  const counts = h.counts || {};
  const kinds = HEALTH_KINDS.map(k => {
    const n = counts[k.key] || 0;
    const cls = h.kind === k.key ? 'btn seg on' : 'btn seg';
    return `<button class="${cls}" data-act="health-kind" data-kind="${k.key}">${k.label}${n ? ' (' + n + ')' : ''}</button>`;
  }).join('');

  const meta = healthKindMeta(h.kind);
  const canRename = h.kind === 'ext' || h.kind === 'name';
  // v3.10.2：视图 = 类别过滤 + 数据层排序（上万条时不再用 DOM 排序，见 healthViewItems）。
  const view = healthViewItems();
  const selCount = h.sel.size;
  const busy = !!(h.op && h.op.running);
  // v3.15.0：全选只覆盖「可操作」条目。无法确定的提示项与编码异常行永远不进选择集，
  // 否则会在执行时回传出错或改了不该改的文件。
  const isSelectable = it => !healthItemAdvisory(it) && it.path.indexOf('\uFFFD') < 0;
  const actionableAll = h.items.filter(isSelectable);
  const actionableView = view.filter(isSelectable);
  // v3.10.4：有页没加载完时「全选」只能覆盖已加载项，文案必须如实反映。
  const allLoaded = h.items.length >= h.total || h.complete;
  const selAllLabel = '全选可处理 ' + (allLoaded ? actionableAll.length : actionableView.length) + ' 条';
  const selAllTitle = allLoaded ? `选中本模块全部可处理条目（${actionableAll.length} 条，不含无法确定的提示项）`
    : `还有 ${h.total - h.items.length} 条未加载，先点「加载更多」补齐后再全选`;
  const reasonName = h.reasonFilter ? ((healthReasonMeta(h.reasonFilter) || {}).label || '') : '';

  if (h.loading) {
    $('view').innerHTML = `<div class="toolbar">${kinds}</div><div class="empty">加载中…</div>`;
    return;
  }

  const head = `<div class="toolbar">${kinds}</div>
    <p class="hint page-note">${esc(meta.hint)}</p>
    ${healthProgressHtml()}
    ${healthOpHtml()}
    <div class="toolbar">
      <button class="btn primary" data-act="health-scan" data-all="1"${running || busy ? ' disabled' : ''} title="${esc(HEALTH_ALL_HINT)}">一次扫描全部类型</button>
      <button class="btn ghost" data-act="health-scan"${running || busy ? ' disabled' : ''}>只检测「${esc(meta.label)}」</button>
      <span class="hint">可处理 ${actionableAll.length} 条${h.items.length < h.total ? '，已显示 ' + h.items.length + ' 条' : '（已全部显示）'}</span>
      <div class="spacer"></div>
      <span class="hint" id="healthSelHint">${selCount ? '已选 ' + selCount + ' 个' : ''}</span>
      ${h.kind === 'ext' ? `<label class="checkline"><input type="checkbox" data-act="health-upper-only"${h.upperOnly ? ' checked' : ''}> 只看大写扩展名</label>` : ''}
      <button class="btn ghost" data-act="health-sel-all"${busy ? ' disabled' : ''} title="${esc(selAllTitle)}">${selAllLabel}</button>
      ${h.reasonFilter ? `<button class="btn ghost" data-act="health-sel-reason"${busy ? ' disabled' : ''} title="只选中「${esc(reasonName)}」下的可处理条目">按当前原因全选 ${actionableView.length} 条</button>` : ''}
      <button class="btn ghost" data-act="health-sel-none"${busy ? ' disabled' : ''}>清空选择</button>
      ${canRename ? `<button class="btn" id="healthRenameSel" data-act="health-rename-sel" data-sel-needs${selCount && !busy ? '' : ' disabled'}>修正扩展名/文件名</button>` : ''}
      ${h.kind === 'name' ? `<button class="btn ghost" data-act="health-fix-encoding"${busy ? ' disabled' : ''} title="非 UTF-8 文件名在页面上勾选再回传会变成乱码，因此由服务端从索引里自己找：先按 GB18030 转 UTF-8，解不出的字节转 %XX，重名自动加序号">修复编码异常文件名</button>` : ''}
      ${h.kind === 'traditional' ? `<button class="btn" id="healthToSimp" data-act="health-to-simp" data-sel-needs${selCount && !busy ? '' : ' disabled'} title="把勾选的繁体文件名转成简体，改完可以用「撤销上次操作」还原">转简体</button>` : ''}
      ${h.kind !== 'traditional' ? `<button class="btn danger" id="healthTrashSel" data-act="health-trash-sel" data-sel-needs${selCount && !busy ? '' : ' disabled'}>移入隔离区</button>` : ''}
      ${h.kind === 'traditional' ? `<button class="btn danger" id="healthTrashSel" data-act="health-trash-sel" data-sel-needs${selCount && !busy ? '' : ' disabled'}>移入隔离区</button>` : ''}
      <button class="btn ghost" data-act="health-undo"${h.lastTask && !busy ? '' : ' disabled'} title="改名类操作（修正扩展名/繁简改名）可按任务撤销；移入隔离区的清理请到隔离区页恢复">撤销上次操作</button>
    </div>
    ${healthOptionsHtml()}
    ${healthReasonBar()}`;

  if (!view.length) {
    // v3.15.0：筛选把结果筛空时给明确提示，不要让人误以为扫描没结果。
    const filtered = h.items.length > 0;
    const empty = filtered
      ? `当前筛选条件下没有结果<br><span class="hint">试试切换原因，或勾选「显示无法确定」。</span>`
      : `暂无结果<br><span class="hint">点击"开始检测"扫描当前卷中的${esc(meta.label)}。</span>`;
    $('view').innerHTML = head + `<div class="empty">${empty}</div>`;
    return;
  }

  // 繁体页把「改后文件名」独立成列当预览，原因列只留判定理由，不再重复建议名
  const isTrad = h.kind === 'traditional';
  const rows = view.map(it => {
    const name = it.path.split('/').pop();
    const detail = esc(it.detail || it.reason || '');
    // v3.12.0：路径里出现 U+FFFD 说明原文件名不是合法 UTF-8，勾选/单条修正回传的是替换后的
    // 字符串，后端必然找不到文件（写入口护栏也会 400）。这类行只允许走上面的整体修复。
    const badEnc = it.path.indexOf('\uFFFD') >= 0;
    // v3.15.0：无法确定的条目只作提示——不可勾选、不可修正、不可清理。
    const adv = healthItemAdvisory(it);
    const suggest = (!isTrad && it.suggest) ? `<span class="hint"> → ${esc(it.suggest)}</span>` : '';
    const renameBtn = badEnc
      ? `<span class="hint">编码异常</span>`
      : (adv ? `<span class="hint">仅提示</span>`
        : (canRename ? `<button class="btn ghost" data-act="health-rename-one" data-path="${esc(it.path)}">修正</button>` : ''));
    const advNote = adv ? ' <span class="hint">· 无法确定，不参与批量处理</span>' : '';
    const simpCell = isTrad ? `<td class="simpname">${it.suggest ? esc(it.suggest) : '—'}</td>` : '';
    return `<tr>
      <td class="sel"><input type="checkbox" data-act="health-check" data-path="${esc(it.path)}"${h.sel.has(it.path) ? ' checked' : ''}${(badEnc || adv) ? ' disabled' : ''}></td>
      <td class="fname">${esc(name)}</td>
      ${simpCell}
      <td class="fpath" title="${esc(it.path)}">${esc(it.path)}</td>
      <td>${detail}${suggest}${advNote}</td>
      <td class="num">${fmtB(it.size)}</td>
      <td class="act">${renameBtn}<button class="btn danger" data-act="health-trash-one" data-path="${esc(it.path)}"${adv ? ' disabled' : ''}>移入隔离区</button></td>
    </tr>`;
  }).join('');

  // v3.7.2：结果可能上千条，底部提供「加载更多」按偏移续拉（P-13）。
  // v3.10.4：有失败页时按钮改为「补齐缺口」，避免用户以为已经拉完整。
  const gapN = (h.missing && h.missing.length) || 0;
  const moreHtml = (gapN || (!h.complete && h.items.length < h.total))
    ? `<div class="toolbar" style="justify-content:center;padding:8px 0">
        <button class="btn ghost" data-act="health-more"${h.loadingMore ? ' disabled' : ''}>${h.loadingMore ? '加载中…' : (gapN ? '补齐失败的 ' + gapN + ' 页' : '加载更多') + '（已显示 ' + h.items.length + ' / 共 ' + h.total + '）'}</button>
      </div>`
    : '';
  // v3.9.6：改成 rtable + colgroup + colresize，列宽可拖动；繁体页多一列独立处理。
  const hcols = healthColumns(isTrad);
  const hcolHtml = '<colgroup>' + hcols.map(c => { const w = colWidthFor(c.id, hcols); return `<col data-col="${c.id}"${w ? ` style="width:${w}px"` : ''}>`; }).join('') + '</colgroup>';
  const hheadHtml = '<thead><tr>' + thCells(hcols) + '</tr></thead>';
  $('view').innerHTML = head + `<div class="group"><div class="tscroll"><table class="rtable health-table" data-sortkey="health">${hcolHtml}${hheadHtml}<tbody>${rows}</tbody></table></div>${moreHtml}</div>`;
  // v3.10.2：健康页走数据层排序（TABLE_SORT_HOOKS.health），这里只恢复表头箭头。
  syncTableSortArrows(document.querySelector('table[data-sortkey="health"]'), tableSortState('health'));
  // v3.10.5：重绘后重新套用扫描/检测期的写操作置灰。
  applyOpsGuard();
}

// healthSortValue 取出某列用于排序的值；返回 null 表示空值，排序时统一沉底。
function healthSortValue(it, col){
  switch (col) {
    case 'h-name': return (it.path || '').split('/').pop();
    case 'h-simp': return it.suggest || '';
    case 'h-path': return it.path || '';
    case 'h-reason': return it.detail || it.reason || '';
    case 'h-size': return typeof it.size === 'number' ? it.size : null;
    default: return null;
  }
}

// healthViewItems 返回「当前类别 + 原因筛选 + 只看大写 + 排序后」的数据（v3.10.2）。
// 上万条时在数据层排序比逐行 appendChild 快一个数量级，也不会让页面假死。
function healthViewItems(){
  const h = healthState();
  let list = h.items.slice();
  // v3.15.0：「无法确定」的条目默认不列出，只有打开开关后才出现，且永远不可操作。
  if (!h.showUncertain) list = list.filter(it => !healthItemAdvisory(it));
  // 按原因筛选：一条结果可能命中多个原因，只要包含所选原因就算命中。
  if (h.reasonFilter) list = list.filter(it => healthItemReasons(it).includes(h.reasonFilter));
  // 扩展名检测里大写扩展名往往占绝大多数，给一个只看它们的开关，
  // 否则批量修 2944 条时根本没法只看真正"内容不符"的那些。
  if (h.kind === 'ext' && h.upperOnly) list = list.filter(it => (it.detail || '').includes('大写'));
  const st = tableSortState('health');
  if (!st.col) return list;
  const dir = st.dir;
  list.sort((a, b) => {
    const va = healthSortValue(a, st.col);
    const vb = healthSortValue(b, st.col);
    if (va == null && vb == null) return 0;
    if (va == null) return 1;   // 空值始终沉底，与升降序无关
    if (vb == null) return -1;
    if (va < vb) return -dir;
    if (va > vb) return dir;
    return 0;
  });
  return list;
}

// healthDynamicReasons 收集「清单项即原因」的类别（临时文件）的原因列表（v3.15.0）。
// 临时文件清单可由用户自定义，因此只能从结果里收集命中项，无法写死。
function healthDynamicReasons(){
  const h = healthState();
  if (h.kind !== 'temp') return [];
  const counts = new Map();
  h.items.forEach(it => healthItemReasons(it).forEach(r => {
    if (r === 'temp') return;
    counts.set(r, (counts.get(r) || 0) + 1);
  }));
  return Array.from(counts.keys()).sort().map(k => ({key:k, label:k}));
}

// healthReasonBar 渲染「按原因筛选」工具栏（v3.15.0）。
// 只列出当前类别真实产生的原因；「无法确定」的原因只在开关打开后出现。
function healthReasonBar(){
  const h = healthState();
  const meta = (HEALTH_REASONS[h.kind] || []).concat(healthDynamicReasons());
  if (!meta.length) return '';
  const uncertainN = Number((h.uncertain || {})[h.kind] || 0);
  const counts = {};
  let actionable = 0;
  h.items.forEach(it => {
    const adv = healthItemAdvisory(it);
    if (!adv) actionable++;
    healthItemReasons(it).forEach(r => { counts[r] = (counts[r] || 0) + 1; });
  });
  const chips = meta.map(m => {
    const n = counts[m.key] || 0;
    // 确定原因没有命中就不占位置；不确定原因按总开关决定是否给入口
    if (m.advisory && !h.showUncertain) return '';
    if (!m.advisory && !n) return '';
    const on = h.reasonFilter === m.key ? ' on' : '';
    const title = m.advisory ? '无法确定：只作提示，不能勾选或批量处理' : '只看这个原因';
    return `<button class="btn seg${on}" data-act="health-reason" data-reason="${m.key}" title="${esc(title)}">${esc(m.label)}${n ? ' (' + n + ')' : ''}</button>`;
  }).join('');
  if (!chips && !uncertainN) return '';
  const allOn = h.reasonFilter ? '' : ' on';
  return `<div class="toolbar" style="flex-wrap:wrap;gap:6px">
    <span class="hint">按原因</span>
    <button class="btn seg${allOn}" data-act="health-reason" data-reason="">全部${actionable ? ' (' + actionable + ')' : ''}</button>
    ${chips}
    ${uncertainN ? `<label class="checkline"><input type="checkbox" data-act="health-toggle-uncertain"${h.showUncertain ? ' checked' : ''}> 显示无法确定（${uncertainN}）</label>` : ''}
  </div>`;
}

// v3.10.2：健康页排序交给数据层，点击列头时重绘整表而不是排序 DOM。
TABLE_SORT_HOOKS.health = () => { renderHealth(); };

// showCurrentToken v3.7.2：向服务端取当前生效令牌并回填只读输入框。
// 接口只对已登录会话返回，避免登录前就能从界面拿到；输入框用 text 便于复制。
async function showCurrentToken(){
  // v3.27 类型层：显式标注为 HTMLInputElement，
  // 否则 $() 的返回值是 HTMLElement，el.select() / el.value 会被 tsc 判为不存在。
  const el = /** @type {HTMLInputElement|null} */ ($('sTokenView'));
  if (!el) return;
  if (!S.status || !S.status.auth || !S.status.auth.required) { toast('当前未启用访问令牌', 'warn'); return; }
  try {
    const r = /** @type {TokenResp} */ (await api('/api/auth/token'));
    el.value = r.token || '';
    el.select();
  } catch(e) { toast(e.message, 'err'); }
}

// validQuietHours 与后端 ParseQuietHours 同规则：HH:MM-HH:MM，允许留空与跨零点。
function validQuietHours(v){
  v = (v || '').trim();
  if (!v) return true;
  // v3.7.1：小时允许 1-2 位，与后端 config.ParseQuietHours 同口径。
  // 后端一直接受 "23:00-7:00"，前端若只认两位会把真机已有配置误判为非法。
  const m = /^(\d{1,2}):(\d{2})-(\d{1,2}):(\d{2})$/.exec(v);
  if (!m) return false;
  const h1 = Number(m[1]), m1 = Number(m[2]), h2 = Number(m[3]), m2 = Number(m[4]);
  if (h1 > 23 || h2 > 23 || m1 > 59 || m2 > 59) return false;
  return !(h1 === h2 && m1 === m2);
}

// bindQuietHoursCheck 输入时就地校验，不等到保存才报错。
function bindQuietHoursCheck(){
  const el = $('sQuietHours'), hint = $('quietHint');
  if (!el || !hint) return;
  el.addEventListener('input', () => {
    const ok = validQuietHours(el.value);
    hint.textContent = ok ? '区间内后台自动任务一律不启动；你手动点的扫描不受影响' : '格式应为 HH:MM-HH:MM，例如 23:00-07:00';
    hint.style.color = ok ? '' : '#c0392b';
  });
}

// healthConfirm 展示操作预览并要求二次确认。
// v3.10.2：全选后一次可能几千上万条，预览最多列 300 行——
// 把几万行塞进模态框会卡死浏览器，而用户确认的是「这一批」，不是逐条核名单。
const HEALTH_CONFIRM_PREVIEW = 300;

function healthConfirm(title, paths, action){
  const shown = paths.slice(0, HEALTH_CONFIRM_PREVIEW);
  const rows = shown.map(it => `<tr><td class="fpath" title="${esc(it.path)}">${esc(it.path)}</td>
    <td class="hint">${it.suggest ? esc(it.suggest) : '—'}</td></tr>`).join('');
  const more = paths.length > shown.length
    ? `<div class="hint" style="padding:6px 0">仅预览前 ${shown.length} 项，其余 ${paths.length - shown.length} 项会一并执行。</div>`
    : '';
  setModal(title,
    `<div class="hint" style="margin-bottom:8px">共 ${paths.length} 个文件，确认后才执行。</div>
     <div class="preview-list" style="max-height:320px;overflow:auto"><table>
       <thead><tr><th>原路径</th><th>目标名</th></tr></thead><tbody>${rows}</tbody></table></div>
     ${more}
     <div class="form-actions">
       <button class="btn ghost" data-act="modal-cancel">取消</button>
       <button class="btn primary" data-act="modal-confirm">确认执行</button>
     </div>`, action);
}

// healthRunBatches 把一次大批量操作拆成多批提交，边执行边刷新进度（v3.10.2）。
//
// 为什么在前端拆批：后端一次调用要等整批文件处理完才返回，几条万时界面会
// 长时间没有任何反馈。拆成 500 个一批后，每批返回就推进进度条，
// 中途出错也能保留已完成的部分，并给出准确的「成功/失败」数字。
// v3.19.0：分批循环、暂停 / 取消、进度卡与完成页统一走 helper.js 的 runBatchedOp，
// 本函数只保留健康页特有的对账合并与任务号收集。
async function healthRunBatches(label, paths, call){
  const h = healthState();
  // 确认页先关掉，进度卡片才露得出来；同时把视图滚回顶部，避免用户在表格中部看不到进度。
  closeModal();
  const scroller = document.querySelector('.tscroll');
  if (scroller) scroller.scrollTop = 0;
  if (window.scrollY) window.scrollTo({ top: 0 });
  let taskId = 0;
  // v3.10.3：后端每批都是独立任务号，只留最后一个会让「撤销」只能还原最后 500 个。
  // 这里把每批的任务号都收起来，撤销时逐个回放。
  const taskIds = [];
  const op = await runBatchedOp({
    id: 'health', label, items: paths, batchSize: HEALTH_OP_BATCH,
    // v3.10.5：每批一份对账报告，这里边跑边合并，完成页统一展示差异。
    op: { reconcile: { checked: 0, mismatched: 0, items: [], truncated: false, advice: '' } },
    call: async (chunk, o) => {
      const r = await call(chunk) || {};
      if (r.task_id) { taskId = r.task_id; taskIds.push(r.task_id); }
      if (r.reconcile) o.reconcile = mergeReconcile(o.reconcile, r.reconcile);
      return { done: r.done || 0, failed: r.failed || 0, items: r.items || [] };
    },
    // 只刷新进度卡片：整表重绘一万多行会让下一批迟迟发不出去。
    onProgress: (o) => { h.op = o; refreshHealthOpCard(); },
    onDone: async (o) => {
      h.op = o;
      await loadHealth();
      showHealthOpDone(o);
    }
  });
  return { done: op.done, failed: op.failed, items: op.items, taskId, taskIds, cancelled: op.cancelled };
}

// showHealthOpDone 是批量操作的「完成页面」：汇总数字 + 失败数量（v3.15.0）。
// v3.19.0：完成页由公共组件渲染，这里只补健康页特有的对账差异区块。
function showHealthOpDone(op){
  showBatchedOpDone(op, reconcileHTML(op.reconcile), '健康操作失败明细');
}

// csvCell 按 RFC 4180 转义：含逗号、引号或换行时用双引号包起来，内部引号翻倍。
function csvCell(v){
  const s = String(v == null ? '' : v);
  return /[",\r\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
}

// healthExportFailures 把本次批量操作的失败明细导出为 CSV（v3.15.0）。
// v3.19.0：导出实现统一到 helper.js，健康页只传操作 id 与文件名前缀。
function healthExportFailures(){
  batchedOpExportFailures('health', '健康操作失败明细');
}

// healthRenamePaths 分批执行「修正扩展名/文件名」。
async function healthRenamePaths(paths){
  const h = healthState();
  // 参数在开始时快照：批量执行期间用户若切了类别，后续批次仍应按发起时的规则处理。
  const kind = h.kind;
  const onlyUpper = !!(kind === 'ext' && h.upperOnly);
  const opt = { restrict_charset: h.opts.restrict_charset, non_cjk: h.opts.non_cjk };
  const r = await healthRunBatches('批量修正文件名', paths, async (chunk) => {
    const res = await postJSON('/api/health/rename', {
      kind, paths: chunk,
      restrict_charset: opt.restrict_charset,
      non_cjk: opt.non_cjk,
      only_uppercase: onlyUpper
    });
    return { done: res.done || 0, failed: res.failed || 0, items: res.items || [], task_id: res.task_id || 0, reconcile: res.reconcile };
  });
  if (r.taskIds.length) { h.lastTasks = r.taskIds.slice(); h.lastTask = r.taskIds[r.taskIds.length - 1]; }
}

// healthToSimpPaths 分批把繁体文件名转成简体（v3.10.2 从 app.js 迁入，统一走进度条）。
async function healthToSimpPaths(paths){
  const h = healthState();
  const r = await healthRunBatches('转简体', paths, async (chunk) => {
    const res = await postJSON('/api/fanjian', { paths: chunk, to_traditional: false });
    const items = Object.entries(res.errors || {}).map(([path, error]) => ({ path, error }));
    return {
      done: (res.done || []).length,
      failed: (res.failed || []).length,
      items,
      task_id: res.task_id || 0,
      reconcile: res.reconcile
    };
  });
  if (r.taskIds.length) { h.lastTasks = r.taskIds.slice(); h.lastTask = r.taskIds[r.taskIds.length - 1]; }
  // 整批失败且原因都是权限时，先给一条能照做的中文指引（飞牛用 Windows ACL）。
  if (!r.done && r.items.some(it => /权限|permission/i.test(it.error || ''))) {
    toast('应用对该文件夹没有写入权限：请到飞牛「文件管理 → 文件夹 → 详细信息 → 权限」给查重助手加读写权限后重试', 'err');
  }
}

// healthTrashPaths 分批把文件/空目录移入隔离区（不可撤销，恢复请到隔离区页）。
async function healthTrashPaths(paths){
  const h = healthState();
  // 空目录走专用清理：深度降序 + 执行前重验，整目录移入隔离区
  if (h.kind === 'empty_dir') {
    await healthRunBatches('清理空目录', paths, async (chunk) => {
      const res = await postJSON('/api/emptydir/clean', { paths: chunk });
      const errors = res.errors || {};
      const items = [];
      (res.removed || []).forEach(p => items.push({ path: p }));
      Object.entries(errors).forEach(([path, error]) => items.push({ path, error }));
      (res.failed || []).forEach(p => { if (!errors[p]) items.push({ path: p, error: '移入隔离区失败' }); });
      return { done: (res.removed || []).length, failed: (res.failed || []).length, items, reconcile: res.reconcile };
    });
    return;
  }
  // v3.9.8 修 F-02：清理类操作不支持 /api/health/undo，不污染 lastTask，改由隔离区页恢复
  await healthRunBatches('移入隔离区', paths, async (chunk) => {
    const res = await postJSON('/api/health/trash', { paths: chunk });
    return { done: res.done || 0, failed: res.failed || 0, items: res.items || [], task_id: res.task_id || 0, reconcile: res.reconcile };
  });
}

async function healthUndo(){
  const h = healthState();
  // v3.10.3：批量操作按 500 个一批，后端每批一个任务号；撤销必须回放全部批次，
  // 否则全选 5000 个改名后只能还原最后 500 个。旧会话没有 lastTasks 时退回单个任务。
  const tasks = (h.lastTasks && h.lastTasks.length) ? h.lastTasks.slice() : (h.lastTask ? [h.lastTask] : []);
  if (!tasks.length) { toast('没有可撤销的操作', 'err'); return; }
  let done = 0, failed = 0, errs = 0;
  try {
    // 逆序撤销：后发生的批次先还原，避免同一文件被多批引用时互相覆盖。
    for (let i = tasks.length - 1; i >= 0; i--) {
      try {
        const r = await postJSON('/api/health/undo', { task_id: tasks[i] });
        done += r.done || 0;
        failed += r.failed || 0;
      } catch(e) {
        errs++;
      }
    }
    const msg = `撤销完成：还原 ${done} 个，失败 ${failed} 个` + (errs ? `，${errs} 批任务无法撤销` : '');
    toast(msg, (failed || errs) ? 'err' : 'ok');
    h.lastTask = 0;
    h.lastTasks = [];
    await loadHealth();
    // 状态接口的 last_audit 仍指向刚撤销的任务；这里再清一次，
    // 避免按钮立刻又亮起来、点了却提示无可撤销内容。
    h.lastTask = 0;
    h.lastTasks = [];
    renderHealth();
  } catch(e) { toast(e.message, 'err'); }
}
