// 查重助手 verify Web UI · 结果页渲染与布局（v3.0.0 从 app.js 拆分）
// 加载顺序第 4：render*/update*、结果页虚拟滚动、列宽与各查重结果页的 DOM 构建。
// 只负责把数据变成 HTML/布局，网络请求留在 api.js。

const IMG_EXT = ['jpg','jpeg','png','gif','bmp','webp','heic','avif','tiff'];

const VID_EXT = ['mp4','mkv','avi','mov','wmv','flv','m4v','ts','mts','m2ts','mpg','mpeg','rmvb','rm','3gp','webm','vob','ogv','asf','f4v','divx','m2v','mxf','dat'];

const RESULT_TABS = ['results', 'folder', 'exact','name','video','intro','samevideo','fuzzy','semantic'];

// 每个页面顶栏问号跳哪一章：查重类都落到「查重依据与结果页」，其余各归各章。
const TAB_HELP = {
  overview:'flow', results:'basis', exact:'basis', samevideo:'basis', name:'basis', video:'basis', intro:'basis',
  photo:'basis', audio:'basis', folder:'basis', fuzzy:'basis', semantic:'basis', big:'basis',
  transcode:'transcode', health:'health', trash:'trash', import:'io', settings:'perf', oplog:'', help:''
};

// v3.8.0：合并结果页的类型标签。kind 与服务端快照返回的 kind 对齐。
const RESULT_KIND_LABELS = {
  exact: '精确内容', name: '同名文件', size: '按大小', attributes: '属性组合',
  video: '相似视频', intro: '相同片头', audio: '重复音频', photo: '相似图片',
  folder: '重复文件夹', fuzzy: '模糊文件名', semantic: '语义分组', samevideo: '相同视频'
};

function resultKindLabel(kind){
  return RESULT_KIND_LABELS[kind] || kind || '重复组';
}

// v3.10.13：目录收窄条的展示名。目录完整路径放进 title，chip 上只留最后两级，
// 避免 /vol1/媒体/电影/… 这类长路径把整行撑开。
function shortDir(dir){
  const parts = String(dir || '').split('/').filter(Boolean);
  if (parts.length <= 2) return '/' + parts.join('/');
  return '…/' + parts.slice(-2).join('/');
}

// 结果快照的目录收窄（facets.dirs）。接口未就位时 S.facets 为空，整条工具栏不占位；
// v3.10.13：无快照时后端返回 409，S.facetsDown 有值时显示「暂不可用」而不是空白。
function facetsInnerHtml(){
  // v3.12.1（UI 审查 P2-1）：收窄中的清除按钮必须在任何分支都能渲染。
  // 原先 dirs 为空或 facets 加载失败时整条工具栏直接 return，
  // 用户被锁在空结果页里，只能刷新整页才能解除收窄。
  const f = S.facets;
  const allDirs = (f && f.dirs || []).filter(d => d && d.dir);
  // v3.13.0（UI 审查 P3-7）：目录超过 8 个时给「更多目录…」入口，不再默默截断。
  const shown = S.dirsExpanded ? allDirs : allDirs.slice(0, 8);
  const moreBtn = !S.dirsExpanded && allDirs.length > 8
    ? `<button class="btn ghost" data-act="result-dir-more">更多目录（还有 ${allDirs.length - 8} 个）</button>` : '';
  const clear = S.dirFilter ? `<button class="btn ghost" data-act="result-dir" data-dir="">清除目录收窄</button>` : '';
  // v3.13.0（UI 审查 P3-8）：重扫就绪后收窄统计要重新取。给一行固定占位而不是直接消失，
  // 否则目录条会「消失→重取→出现」闪一下，滚动位置也跟着抖。
  if (S.facetsLoading) return `<span class="hint">目录分布更新中…</span>${clear}`;
  if (S.facetsDown) return `<span class="hint">${esc(S.facetsDown)}</span>${clear}`;
  if (!f) return clear ? `<span class="hint">已按目录收窄：${esc(S.dirFilter)}</span>${clear}` : '';
  if (!shown.length) {
    // 收窄的目录在新快照/新关键字下 0 组：明确告诉用户这是收窄的结果，并给出出口
    return clear ? `<span class="hint">「${esc(shortDir(S.dirFilter))}」下没有结果</span>${clear}` : '';
  }
  const chips = shown.map(d => {
    const active = (S.dirFilter || '') === d.dir;
    return `<button class="btn seg${active?' on':''}" data-act="result-dir" data-dir="${esc(d.dir)}" title="${esc(d.dir)}">${esc(shortDir(d.dir))} (${Number(d.count || 0).toLocaleString()})</button>`;
  }).join('');
  const capped = isCapped();
  const warn = capped ? '<span class="badge warn">已到内存上限，请先收窄再加载</span>' : '';
  return `<span class="hint">按目录收窄</span>${chips}${moreBtn}${clear}${warn}`;
}

function updateRowPathBtn(){
  const b = document.querySelector('[data-act="assist-fill-row-path"]');
  if (!b) return;
  b.disabled = S.rowSel.size !== 1;
  b.title = S.rowSel.size === 1 ? '将选中行路径填入上方输入框' : '请只选一行';
}

function previewTable(items){
  return `<div class="preview-list"><table><thead><tr><th>文件</th><th>结果</th><th>说明</th></tr></thead><tbody>` +
    (items || []).map(it => `<tr><td class="fpath" title="${esc(it.path||('#'+it.id))}">${esc(it.path||('#'+it.id))}</td>
      <td>${it.ok?'<span class="badge good">可执行</span>':'<span class="badge warn">不可执行</span>'}</td>
      <td class="hint">${esc(it.reason||'')}</td></tr>`).join('') + `</tbody></table></div>`;
}

function syncCheckboxes(){
  document.querySelectorAll('input[data-act="check"]').forEach(el => {
    const set = S.sel[el.dataset.key];
    el.checked = !!(set && set.has(el.dataset.path));
  });
  updateSelHint();
}

function compileTextPattern(){
  if (!S.textPattern) return null;
  let src = S.textPattern;
  if (!S.textRegex) src = src.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  if (S.textWhole) src = '^(' + src + ')$';
  try { return new RegExp(src, S.textCase ? '' : 'i'); }
  catch(e) { toast('正则表达式无效：' + e.message, 'err'); return null; }
}

// v2.1.0：匹配模式只影响分组/展示入口，不改变扫描期特征，所以切换不需要重扫。
const MATCH_MODE_LABELS = {
  all: '全部（多依据）', exact: '完全相同', similar: '相似视频',
  audio: '仅音频相似', thumbs: '仅缩略图（开头画面）', ignore_content: '忽略内容（只按文件名）',
  custom: '自定义组合'
};

// 按本次全局查重依据控制结果页标签，避免展示本次没有扫描的类型。
// v3.9.10 备注：v3.9.0 把类型标签合并进结果页 chips 后，这里的选择器只对侧边栏残留的
// data-tab 生效，属兼容保留；tabEnabled 仍被扫描入队逻辑用作前置判断，不能删。
function updateTabVisibility(){
  const c = S.status ? (S.status.config || {}) : {};
  const show = {
    results: true,
    exact: matchModeAllows('exact') && c.content_enabled !== false,
    name: matchModeAllows('name') && c.name_enabled !== false,
    video: matchModeAllows('video') && c.video_enabled !== false,
    intro: matchModeAllows('intro') && c.intro_enabled === true,
    audio: matchModeAllows('audio'),
    folder: matchModeAllows('folder') && c.compute_dup_folders !== false,
    photo: matchModeAllows('photo'),
    fuzzy: matchModeAllows('fuzzy'),
    semantic: matchModeAllows('semantic')
  };
  document.querySelectorAll('button.tab[data-tab]').forEach(t => {
    const k = t.dataset.tab;
    if (k in show) t.style.display = show[k] ? '' : 'none';
  });
  // 侧边栏分组：组内全部入口都关掉时，整个分组（含标题）一并隐藏
  document.querySelectorAll('.ngroup').forEach(g => {
    const any = [...g.querySelectorAll('button.tab[data-tab]')].some(t => t.style.display !== 'none');
    g.style.display = any ? '' : 'none';
  });
  if (isResultTab() && show[S.tab] === false) switchTab('overview');
}

function tabEnabled(name){
  if (name === 'help' || name === 'results') return true; // 操作手册与合并结果页恒可用
  const c = S.status ? (S.status.config || {}) : {};
  if (name === 'exact') return matchModeAllows('exact') && c.content_enabled !== false;
  if (name === 'name') return matchModeAllows('name') && c.name_enabled !== false;
  if (name === 'video') return matchModeAllows('video') && c.video_enabled !== false;
  if (name === 'intro') return matchModeAllows('intro') && c.intro_enabled === true;
  if (name === 'folder') return matchModeAllows('folder') && c.compute_dup_folders !== false;
  if (name === 'audio') return matchModeAllows('audio');
  if (name === 'photo') return matchModeAllows('photo');
  if (name === 'fuzzy') return matchModeAllows('fuzzy');
  if (name === 'semantic') return matchModeAllows('semantic');
  return true;
}

function renderProgress(state){
  if (!state) return;
  updateScanSummary(state);
  updateTypeChart(state);
  const running = state.running;
  const prog = $('prog');
  prog.style.display = running ? 'flex' : 'none';
  $('stopBtn').style.display = running ? '' : 'none';
  const pb = $('pauseBtn');
  if (pb) {
    pb.style.display = running ? '' : 'none';
    pb.textContent = (S.status && S.status.paused) ? '继续' : '暂停';
  }
  $('scanBtn').disabled = running;
  $('scanBtn').textContent = running ? '查重进行中…' : '新建查重';
  if (!running) return;
  renderPhaseSteps(state);
  const elapsed = state.started_at ? Date.now() / 1000 - state.started_at : 0;
  const bar = $('pbar'), fill = bar.querySelector('i');
  if (state.total > 0) {
    bar.classList.remove('indet');
    const pct = Math.min(100, Math.max(0, state.done / state.total * 100));
    fill.style.setProperty('--p', (pct / 100).toFixed(4));
    $('ptext').textContent = '已用 ' + fmtElapsed(elapsed) + ' · ' + state.done + ' / ' + state.total;
  } else {
    bar.classList.add('indet');
    fill.style.removeProperty('--p');
    $('ptext').textContent = '已用 ' + fmtElapsed(elapsed) + (state.done > 0 ? ' · 已处理 ' + state.done : '');
  }
}

// v2.0.0：本次扫描概况卡。扫描中每秒刷新，结束后保留最后一次结果；
// 只更新固定容器，不整页重绘，避免打断用户操作。
function scanSummaryHtml(state){
  if (!state || (!state.running && !state.finished_at)) return '';
  const phase = PHASE_LABELS[state.phase] || '准备中';
  const title = state.message || (state.running ? '正在扫描' : '本次扫描');
  const files = Number(state.scanned_files || 0);
  const bytes = Number(state.scanned_bytes || 0);
  const skipped = Number(state.skipped || 0);
  const skippedDirs = Number(state.skipped_dirs || 0);
  const done = Number(state.done || 0);
  const total = Number(state.total || 0);
  const elapsed = state.started_at
    ? (state.running ? Date.now() / 1000 - state.started_at : Math.max(0, (state.finished_at || 0) - state.started_at))
    : 0;
  const stage = total > 0
    ? `${done.toLocaleString()} / ${total.toLocaleString()}（${Math.round(done / total * 100)}%）`
    : (state.running ? '统计中' : '—');
  const skipText = state.skip_reason
    || (skipped || skippedDirs ? `跳过 ${skipped} 个文件、${skippedDirs} 个文件夹` : '没有跳过文件');
  return `<section class="section scan-summary" aria-label="本次扫描概况">
    <h2 class="section-title">本次扫描</h2>
    <p class="hint">${esc(title)}${state.running ? ' · 随时可以停止' : ''}</p>
    <dl class="facts">
      <div class="fact"><dt>已扫描文件</dt><dd>${files.toLocaleString()}<span class="sub">范围共 ${fmtB(bytes)}</span></dd></div>
      <div class="fact"><dt>当前阶段</dt><dd>${esc(stage)}<span class="sub">${esc(phase)}</span></dd></div>
      <div class="fact"><dt>已用时</dt><dd>${fmtElapsed(elapsed)}<span class="sub">${state.running ? '进行中' : '已结束'}</span></dd></div>
      <div class="fact"><dt>跳过</dt><dd>${skipped.toLocaleString()} 个文件<span class="sub">${esc(skipText)}</span></dd></div>
    </dl>
  </section>`;
}

function updateScanSummary(state){
  const box = $('scanSummary');
  if (box) box.innerHTML = scanSummaryHtml(state);
}

// v2.2.0：文件类型分布环形图（自绘 SVG，零依赖）。数据来自 State.types，
// 枚举阶段每 500 个文件刷新一次；这里再做 1 秒节流，避免频繁重绘。
let typeChartAt = 0;

function typeChartLabel(ext){
  return ext ? '.' + ext : '无扩展名';
}

function renderTypeChart(state){
  const types = (state && state.types) ? state.types.filter(t => Number(t.files || 0) > 0) : [];
  if (!types.length) return '';
  const totalBytes = types.reduce((a, t) => a + Number(t.bytes || 0), 0);
  const totalFiles = types.reduce((a, t) => a + Number(t.files || 0), 0);
  const useBytes = totalBytes > 0;
  const total = useBytes ? totalBytes : totalFiles;
  let items = types.slice(0, 8);
  const rest = types.slice(8);
  if (rest.length) {
    items.push({
      ext: '', other: true,
      files: rest.reduce((a, t) => a + Number(t.files || 0), 0),
      bytes: rest.reduce((a, t) => a + Number(t.bytes || 0), 0)
    });
  }
  const C = 2 * Math.PI * 42;
  let offset = 0;
  const colorOf = (t, i) => t.other ? 'var(--chart-8)' : `var(--chart-${i % 7 + 1})`;
  const segs = items.map((t, i) => {
    const v = useBytes ? Number(t.bytes || 0) : Number(t.files || 0);
    const len = total > 0 ? C * v / total : 0;
    const label = t.other ? '其他' : typeChartLabel(t.ext);
    const seg = `<circle cx="60" cy="60" r="42" fill="none" stroke="${colorOf(t, i)}" stroke-width="18" stroke-dasharray="${len} ${C - len}" stroke-dashoffset="${-offset}" transform="rotate(-90 60 60)"><title>${esc(label)}：${Number(t.files || 0).toLocaleString()} 个 · ${fmtB(t.bytes)}</title></circle>`;
    offset += len;
    return seg;
  }).join('');
  const legend = items.map((t, i) => {
    const label = t.other ? '其他' : typeChartLabel(t.ext);
    return `<li><span class="tc-dot" style="background:${colorOf(t, i)}"></span><span class="tc-ext">${esc(label)}</span><span class="tc-meta">${Number(t.files || 0).toLocaleString()} 个 · ${fmtB(t.bytes)}</span></li>`;
  }).join('');
  return `<section class="section type-chart" aria-label="文件类型分布">
    <h2 class="section-title">文件类型分布</h2>
    <div class="tc-wrap">
      <svg class="tc-donut" viewBox="0 0 120 120" role="img" aria-label="本次扫描文件类型占比">
        ${segs}
        <text x="60" y="57" text-anchor="middle" class="tc-total">${fmtB(totalBytes || totalFiles)}</text>
        <text x="60" y="72" text-anchor="middle" class="tc-sub">${useBytes ? '按占用' : '按文件数'}</text>
      </svg>
      <ul class="tc-legend">${legend}</ul>
    </div>
  </section>`;
}

function updateTypeChart(state){
  const box = $('typeChart');
  if (!box) return;
  const key = JSON.stringify((state && state.types) || []);
  const now = Date.now();
  if (now - typeChartAt < 1000 && box.dataset.key === key) return;
  typeChartAt = now;
  box.dataset.key = key;
  box.innerHTML = renderTypeChart(state);
}

// v2.2.0：悬停存储卷显示该目录下已索引文件数与总大小（非原生 title）。
function hideVolTip(){
  const el = $('volTip');
  if (el) el.remove();
}

function showVolTip(anchor, path, stat){
  hideVolTip();
  const tip = document.createElement('div');
  tip.id = 'volTip';
  tip.className = 'stat-tip card';
  const ok = stat && !stat.error;
  tip.innerHTML = `<b>${esc(path)}</b><div class="hint">${ok
    ? `已索引 ${Number(stat.files || 0).toLocaleString()} 个文件，合计 ${fmtB(stat.bytes)}`
    : '统计暂不可用'}</div>`;
  document.body.appendChild(tip);
  const r = anchor.getBoundingClientRect();
  const top = Math.max(8, Math.min(window.innerHeight - tip.offsetHeight - 8, r.bottom + 6));
  const left = Math.max(8, Math.min(window.innerWidth - tip.offsetWidth - 8, r.left));
  tip.style.top = top + 'px';
  tip.style.left = left + 'px';
}

function bindVolHover(){
  const view = $('view');
  if (!view) return;
  view.querySelectorAll('.vol[data-path]').forEach(el => {
    if (el.dataset.bound) return;
    el.dataset.bound = '1';
    el.addEventListener('mouseenter', async () => {
      const p = el.dataset.path;
      S.volStats = S.volStats || {};
      if (!S.volStats[p]) {
        try { S.volStats[p] = await api('/api/folder/stats?path=' + encodeURIComponent(p)); }
        catch(e) { S.volStats[p] = {error: true}; }
      }
      showVolTip(el, p, S.volStats[p]);
    });
    el.addEventListener('mouseleave', hideVolTip);
  });
}

// renderPhaseSteps v1.28.1：动态步骤条——按查重依据与开关生成阶段序列，高亮当前。
const PHASE_LABELS = { scan:'枚举文件', quick:'预哈希', full:'BLAKE3 校验', photo:'图片指纹', video:'视频指纹', audio:'音频指纹', intro:'片头指纹' };

function renderPhaseSteps(state){
  const seq = phaseSequence();
  const cur = state.phase || 'scan';
  const curIdx = seq.indexOf(cur);
  const chips = seq.map((p, i) => {
    let cls = 'pstep';
    if (curIdx < 0 ? i === 0 : i < curIdx) cls += ' done';
    else if (i === curIdx || (curIdx < 0 && i === 0)) cls += ' cur';
    const pct = (i === curIdx && state.total > 0) ? Math.round(state.done / state.total * 100) + '%' : '';
    return `<span class="${cls}">${PHASE_LABELS[p] || p}${pct ? ' ' + pct : ''}</span>`;
  }).join('<span class="psep">›</span>');
  $('phase').innerHTML = chips + (state.message ? `<span class="hint">${esc(state.message)}</span>` : '');
}

// 概览页自带一个更大的「新建查重」，顶栏那个同功能主按钮要收起来，
// 同一屏不该出现两个一模一样的实心主操作。首屏走的是 boot → renderOverview，
// 不经过 switchTab，所以两边都要调它。
function syncScanBtn(tab){
  const sb = $('scanBtn');
  if (sb) sb.style.display = tab === 'overview' ? 'none' : '';
}

function renderOverview(st){
  syncScanBtn('overview');
  const c = st.counts || {}, state = st.state || {};
  const vols = st.volumes || [];
  const reclaim = (c.exact_waste || 0) + (c.name_waste || 0);
  const exactGroups = c.exact_groups || 0, nameGroups = c.name_groups || 0;
  const volHtml = vols.length ? vols.map(v => {
    const used = Math.max(0, (v.total||0) - (v.free||0));
    const pct = v.total ? (used / v.total * 100) : 0;
    return `<div class="vol" data-path="${esc(v.path)}"><span class="vol-name" title="${esc(v.path)}">${esc(v.path)}</span>
      <span class="vol-meter"><i style="width:${pct.toFixed(1)}%"></i></span>
      <span class="vol-figs">${fmtB(used)} / ${fmtB(v.total)}　剩余 ${fmtB(v.free)}</span></div>`;
  }).join('') : `<p class="vol-empty">还没有扫描目录。存储卷只列出扫描范围内的卷，到「设置 → 扫描范围」添加后显示占用。</p>`;
  const noteHtml = (exactGroups + nameGroups) > 0
    ? `<div class="ledger-note">
        <span>精确重复 ${exactGroups.toLocaleString()} 组，${(c.exact_files||0).toLocaleString()} 个文件，${fmtB(c.exact_waste)}</span>
        <span>同名文件 ${nameGroups.toLocaleString()} 组，${fmtB(c.name_waste)}</span>
      </div>`
    : `<div class="ledger-note"><span>库里还没有重复文件。新建一次查重，这里会给出能收回多少空间。</span></div>`;
  // v3.9.0：当前搜索规则的结果摘要（概览的旧计数不含“按大小”等新规则）。
  const rs = (S.ruleSearch && S.ruleSearch.state === 'ready') ? S.ruleSearch : null;
  const ruleNote = rs ? `<div class="notice">
    当前搜索规则结果：<b>${(rs.totals && rs.totals.groups || 0).toLocaleString()}</b> 组 · ${(rs.totals && rs.totals.files || 0).toLocaleString()} 个文件 · 可释放 <b>${fmtB(rs.totals && rs.totals.waste || 0)}</b>
    ${rs.stale ? '<span class="badge warn">结果可能已过期</span>' : ''}
    <button class="btn ghost" data-act="rule-search-again">重新搜索</button></div>` : '';

  $('view').innerHTML = `
    <section class="ledger" aria-label="可释放空间与存储卷">
      <div class="ledger-fig">
        <dl class="fig-pair"><dt>可释放空间</dt><dd>${fmtB(reclaim)}</dd></dl>
        ${noteHtml}
        <button class="btn primary btn-lg ov-newscan" data-act="new-scan">新建查重</button>
      </div>
      <div class="ledger-vols">
        <div class="ledger-head"><span>存储卷</span><span>已用</span><span></span></div>
        ${volHtml}
      </div>
    </section>
    ${ruleNote}
    ${(st.auth && st.auth.required) ? `<div class="notice">已启用访问令牌：局域网内打开页面需要先输入令牌。首次启动自动生成的令牌保存在数据目录的 <span class="mono">access-token.txt</span>；登录后可在「设置 → 访问令牌」查看、更换或清除。</div>` : ''}
    ${(state.running || state.finished_at > 0) ? `<div id="scanSummary">${scanSummaryHtml(state)}</div>` : ''}
    ${st.cleanup ? `<div class="notice">
      上次自动清理：${fmtT(Number(st.cleanup.last_run) * 1e9)} · ${esc(st.cleanup.name || '')} · ${esc(st.cleanup.last_result || '')}${st.cleanup.last_error ? '（错误：' + esc(st.cleanup.last_error) + '）' : ''}
    </div>` : ''}
    <div id="typeChart">${renderTypeChart(state)}</div>
    <div id="taskHistoryCard">${taskHistoryHtml()}</div>
    <dl class="facts">
      <div class="fact"><dt>已索引文件</dt><dd>${(c.files||0).toLocaleString()}<span class="sub">合计 ${fmtB(c.bytes)}</span></dd></div>
      <div class="fact"><dt>已分析视频</dt><dd>${(c.videos||0).toLocaleString()}<span class="sub">音频指纹与缩略图</span></dd></div>
      <div class="fact"><dt>隔离区</dt><dd>${(c.trash_files||0).toLocaleString()}<span class="sub">占用 ${fmtB(c.trash_bytes)}</span></dd></div>
      <div class="fact"><dt>最近扫描</dt><dd>${state.last_scan ? fmtT(state.last_scan * 1e9) : '尚未扫描'}<span class="sub">${state.last_error
        ? `<span class="last-error"><span class="last-error-text">最近错误：${esc(state.last_error)}（如果提示文件或目录不存在，可点「清理失效索引」只清理索引记录、不删任何文件，然后重试）</span><button class="btn ghost" data-act="copy-last-error">复制</button><button class="btn ghost" data-act="repair-index" title="只从数据库删除「磁盘上已不存在」的索引记录，不删除、不移动任何文件">清理失效索引</button></span>`
        : '索引随文件变动自动更新'}</span></dd></div>
    </dl>
    <section class="section">
      <h2 class="section-title">工具</h2>
      <div class="toolrow">
        <button class="btn" data-act="open-folder-rename">按文件夹改名</button>
        <p>把 IMG_1234、DSC_0001、001.png 这类相机码或纯序号文件名批量改成 <span class="mono">&lt;文件夹名&gt;_001</span>，有描述的文件默认保留原名。预览确认后执行，可撤销。</p>
      </div>
    </section>`;
  bindVolHover();
  // v3.24.0：概览展示最近长任务历史（/api/tasks），进入页面时拉一次。
  if (!S.taskHistoryLoaded) loadTaskHistory();
}

// taskHistoryHtml 渲染最近 5 条长任务收尾记录（v3.24.0）。
// 数据来自 /api/tasks 的 history，只读展示，不参与任何调度。
function taskHistoryHtml(){
  const list = (S.taskHistory || []).slice(-5).reverse();
  if (!list.length) return '';
  const kindText = {scan:'扫描', health:'健康检测', video:'视频分析', trim:'剪除片头', transcode:'转码',
    crop:'识别竖横屏', thumb:'缩略图', index_repair:'索引体检', cleanup:'清理计划', trash:'批量隔离',
    rename:'改名', import:'导入'};
  const rows = list.map(t => `<div class="asrow" style="justify-content:space-between;align-items:center;padding:2px 0;gap:8px">
    <span>${esc(t.name || kindText[t.kind] || t.kind || '任务')}</span>
    <span class="hint">${esc(t.result === 'canceled' ? '已取消' : (t.result === 'done' ? '已完成' : (t.result || '')))} · ${fmtT(Number(t.finished_at || 0) * 1e9)}</span>
  </div>`).join('');
  return `<div class="card" style="margin-top:var(--space-sm)"><div class="toolbar"><b>最近长任务</b><span class="hint">扫描、转码、改名等任务的收尾结果</span></div>${rows}</div>`;
}

async function loadTaskHistory(){
  try {
    const r = await api('/api/tasks');
    S.taskHistory = r.history || [];
  } catch(e) {
    S.taskHistory = [];
  } finally {
    S.taskHistoryLoaded = true;
    const box = $('taskHistoryCard');
    if (box) box.innerHTML = taskHistoryHtml();
  }
}

function wrapResult(head, inner){
  // v3.9.1：助手收起时不再为整列预留宽度，让主列表尽量向左铺满。
  return `<div class="resultlayout${S.assistOpen?'':' assist-closed'}">
    <aside class="assist ${S.assistOpen?'':'collapsed'}" id="assist">${assistBodyHtml()}</aside>
    <div class="assist-tools">${head}<div id="loadGapBar" class="loadgap" hidden></div><div id="groupList" class="virtual-list">${inner||''}</div><div id="listStatus" class="hint"></div></div>
  </div>`;
}

function colWidthFor(id, cols){
  const stored = loadColWidths()[colLayoutKey()] || {};
  const w = Number(stored[id]);
  if (w > 0) return w;
  const def = (cols || resultColumns()).find(c => c.id === id);
  return def ? (def.width || 0) : 0;
}

function resultColgroupHtml(kind){
  const cols = resultColumns(kind);
  return '<colgroup>' + cols.map(c => {
    const w = colWidthFor(c.id, cols);
    return `<col data-col="${c.id}"${w ? ` style="width:${w}px"` : ''}>`;
  }).join('') + '</colgroup>';
}

function resultHeadHtml(kind){
  // v3.9.11：表头统一走 thCells，按列定义的 sort 类型附加 data-sort 属性
  return '<thead><tr>' + thCells(resultColumns(kind)) + '</tr></thead>';
}

// unifiedGroupCardHtml v3.8.0：合并结果页的统一组卡片。
// 与旧分页面的区别：类型来自 g.kind，不再依赖当前标签；媒体类只在行内补差异信息。
function unifiedGroupCardHtml(g, gi){
  const kind = g.kind || 'exact';
  const sel = S.sel[g.key] || new Set();
  const collapsed = S.collapsed.has(g.key);
  const mediaKind = kind === 'video' || kind === 'intro' || kind === 'audio' || kind === 'photo';
  // v3.10.13：口径 B——编码异常的文件照样计入分组与可释放空间，但不允许任何写操作。
  // 组里只要有一个异常文件，组级批量按钮一并禁用，避免“点了才报索引不存在”。
  const badCount = (g.files || []).filter(badName).length;
  const groupLock = badCount > 0;
  const rows = (g.files || []).map(f => {
    const checked = sel.has(f.path);
    const locked = badName(f);
    const thumbCell = S.genThumbs
      ? `<td>${thumbSrc(f)
          ? `<img class="rowthumb" loading="lazy" src="${thumbSrc(f)}"${locked ? '' : ` data-act="preview" data-path="${esc(f.path)}" data-name="${esc(f.name)}"`} alt="">`
          : `<div class="rowthumb ph">无预览</div>`}</td>`
      : '';
    const meta = f.width && f.height
      ? `${f.width}×${f.height}${f.codec ? ' · ' + esc(f.codec) : ''}`
      : (f.codec ? esc(f.codec) : '—');
    const actions = (kind === 'exact' || kind === 'video')
      ? `<td class="act">
          <button class="btn ghost" data-act="group-rename-base" data-key="${esc(g.key)}" data-path="${esc(f.path)}" ${locked ? `disabled title="${BAD_NAME_TIP}"` : 'title="把组内其他文件的主名改成与该文件一致（可撤销）"'}>统一命名</button>
          <button class="btn ghost" data-act="file-rename" data-path="${esc(f.path)}" ${locked ? `disabled title="${BAD_NAME_TIP}"` : 'title="单独修改该文件名"'}>改名</button>
          <button class="btn ghost" data-act="tc-enqueue" data-path="${esc(f.path)}" ${locked ? `disabled title="${BAD_NAME_TIP}"` : 'title="加入视频转码队列"'}>转码</button>
        </td>`
      : '';
    return `<tr data-path="${esc(f.path)}" class="${S.rowSel.has(f.path)?'rowsel':''}${locked?' badrow':''}">
      <td class="sel"><input type="checkbox" data-act="check" data-key="${esc(g.key)}" data-path="${esc(f.path)}" ${checked ? 'checked' : ''}${locked ? ` disabled title="${BAD_NAME_TIP}"` : ''}></td>
      ${thumbCell}
      <td class="fname${isProtectedPath(f.path) ? ' is-protected' : ''}">${esc(f.name)}${locked ? ` <span class="badge del" title="${BAD_NAME_TIP}">编码异常</span>` : ''}${isProtectedPath(f.path) ? ' <span class="badge">保护</span>' : ''}${f.peer ? ` <span class="badge keep">另一设备也有</span>` : ''}</td>
      <td class="fpath" title="${esc(f.path)}">${esc(f.path)}</td>
      <td class="num">${fmtB(f.size)}</td>
      <td class="meta">${mediaKind && f.duration ? fmtDur(f.duration) : (meta !== '—' ? meta : fmtT(f.mtime))}</td>
      ${actions}
    </tr>`;
  }).join('');
  const sim = g.similarity ? `<span class="badge good">相似度 ${(g.similarity*100).toFixed(1)}%</span>` : '';
  const hash = g.hash ? `<span class="badge mono">${esc(String(g.hash).slice(0,12))}</span>` : '';
  return `<div class="group" data-gkey="${esc(g.key)}" data-gindex="${gi}">
    <div class="ghead">
      <div class="gmeta">
        <span class="tag">#${gi + 1}</span>
        <span class="gkind">${esc(resultKindLabel(kind))}</span>
        <span class="gtitle" title="${esc(g.name || '')}">${esc(g.name || '重复组')}</span>
        ${hash}${sim}
        <span class="hint">${g.count} 个文件</span>
        <span class="gwaste">可释放 ${fmtB(g.waste)}</span>
        <span class="hint">合计 ${fmtB(g.total_size)}</span>
        ${badCount ? `<span class="badge del" title="${BAD_NAME_TIP}">含 ${badCount} 个编码异常</span>` : ''}
      </div>
      <div class="gactions">
        ${kind === 'intro' ? `<button class="btn warn" data-act="trim-panel" data-key="${esc(g.key)}" ${groupLock ? `disabled title="组内含编码异常文件，暂不支持批量操作"` : ''}>剪除片头</button>` : ''}
        <button class="btn ghost" data-act="collapse-group" data-key="${esc(g.key)}">${collapsed?'展开':'折叠'}</button>
        <button class="btn ghost" data-act="keep-first" data-key="${esc(g.key)}" ${groupLock ? `disabled title="组内含编码异常文件，暂不支持批量操作"` : ''}>只保留第一个</button>
      </div>
    </div>
    ${collapsed ? '' : `<div class="tscroll"><table class="rtable" data-sortkey="${esc(S.tab)}">
      ${resultColgroupHtml(kind)}${resultHeadHtml(kind)}
      <tbody>${rows}</tbody>
    </table></div>`}
  </div>`;
}

// v3.17.2：旧分页面卡片路径随标签合并失去入口，S.tab 恒为 results，这里只留统一卡片转发。
function groupCardHtml(g, gi){
  if (S.tab === 'results') return unifiedGroupCardHtml(g, gi);
}

function renderVirtual(force){
  const list = $('groupList');
  const v = S.vlist;
  if (!list || !v || !S.groups.length) return;
  const viewTop = list.scrollTop;
  const viewH = list.clientHeight || 600;
  const start = indexForOffset(v, Math.max(0, viewTop - 300));
  const end = Math.min(v.heights.length, indexForOffset(v, viewTop + viewH + 300) + 1);
  if (!force && v.range[0] === start && v.range[1] === end) { updateListStatus(); return; }
  v.range = [start, end];
  const top = v.prefix[start] || 0;
  const bottom = Math.max(0, v.sum - (v.prefix[end] || v.sum));
  list.innerHTML = `<div class="vtop" style="height:${top}px"></div>`
    + S.groups.slice(start, end).map((g, i) => groupCardHtml(g, start + i)).join('')
    + `<div class="vbot" style="height:${bottom}px"></div>`;
  // 重新渲染后恢复各分组表格的排序状态（虚拟滚动会反复重建卡片）
  applyTableSort(S.tab);
  requestAnimationFrame(measureVirtualGroups);
  updateListStatus();
}

function measureVirtualGroups(){
  const list = $('groupList');
  const v = S.vlist;
  if (!list || !v) return;
  let changed = false;
  list.querySelectorAll('.group[data-gindex]').forEach(el => {
    const i = Number(el.dataset.gindex);
    const h = el.offsetHeight;
    if (h > 0 && i >= 0 && i < v.heights.length && Math.abs(h - v.heights[i]) > 1) {
      v.heights[i] = h;
      changed = true;
    }
  });
  if (!changed) return;
  // 已渲染组在可视区内，调整总高度即可；不动顶部 spacer，避免滚动位置跳动。
  rebuildPrefix();
  const bot = list.querySelector('.vbot');
  if (bot) bot.style.height = Math.max(0, v.sum - (v.prefix[v.range[1]] || v.sum)) + 'px';
  updateListStatus();
}

// v3.9.6 修 V-18：截断到 3000 组时文案不再与总数矛盾——区分「真的全加载完」和「被上限截断」
function updateListStatus(){
  const el = $('listStatus'); if (!el) return;
  // v3.10.13：满载状态要同步到 facets 收窄条；此函数在滚动时高频调用，只在状态翻转时重绘
  syncFacetsCap();
  if (!S.groups.length) { el.textContent = ''; return; }
  let tail;
  if (S.loadingMore) tail = '正在加载…';
  else if (S.hasMore) tail = '滚动到底自动加载';
  else if (S.groups.length < S.total) tail = `已加载前 ${S.groups.length} 组，共 ${S.total} 组（当前匹配模式已用尽或达到上限）`;
  else tail = '已全部加载';
  // 2026-11-08：组数加千分位，和上方未加载提示条保持同一种写法
  el.textContent = `${S.groups.length.toLocaleString()} / ${Number(S.total || 0).toLocaleString()} 组 · ${tail}`;
  // 2026-11-08：未加载数量提到列表上方的常驻条里。只在底部放灰字，用户很容易
  // 以为「就这么多」；这里给明确数字 + 一键加载，加载中按钮转成进度文案。
  const bar = $('loadGapBar');
  if (bar) {
    const total = Number(S.total || 0);
    const gap = Math.max(0, total - S.groups.length);
    if (!S.groups.length || gap === 0) { bar.hidden = true; bar.innerHTML = ''; }
    else {
      bar.hidden = false;
      const busy = S.loadingAll || S.loadingMore;
      // 达到前端内存上限后不再给「加载」按钮，避免用户反复点却没有进展（v3.10.13：引导 facets 收窄）
      const capped = isCapped();
      bar.innerHTML = `<span>已显示 <b>${S.groups.length.toLocaleString()}</b> / ${total.toLocaleString()} 组，还有 <b>${gap.toLocaleString()} 组未加载</b></span>
        ${capped ? `<span class="hint">${S.facets ? '已达前端上限，请先用上方「按目录收窄」或切换类型缩小范围，再继续加载' : '已达前端上限，请用筛选器按大小/目录/类型收窄'}</span>`
          : `<button class="btn" data-act="load-rest"${busy ? ' disabled' : ''}>${busy ? '正在加载…' : '加载剩余全部'}</button>`}`;
    }
  }
}

function syncFacetsCap(){
  const capped = isCapped();
  if (S._facetsCapped === capped) return;
  S._facetsCapped = capped;
  const host = $('facetsBarHost');
  if (host) host.innerHTML = facetsInnerHtml();
}

function bindVirtualList(){
  const list = $('groupList'); if (!list) return;
  list.onscroll = () => { renderVirtual(false); maybeLoadMore(); };
}

// renderRuleSearchProgress v3.8.0：搜索阶段进度。搜索阶段可以慢，展示必须秒开。
function renderRuleSearchProgress(st){
  st = st || {};
  const total = Number(st.total || 0), loaded = Number(st.loaded || 0);
  const pct = total > 0 ? Math.min(100, Math.round(loaded * 100 / total)) : 0;
  $('view').innerHTML = `<div class="card" style="margin:20px 0">
    <div class="toolbar"><b>正在整理搜索结果</b><span class="hint">已读 ${loaded.toLocaleString()} / 预计 ${total?total.toLocaleString():'?'} 个文件</span>
      <div class="spacer"></div><button class="btn ghost" data-act="rule-search-cancel">取消</button></div>
    <div class="pbar" style="width:100%;margin:10px 0"><i style="--p:${(pct / 100).toFixed(3)}"></i></div>
    <div class="hint">搜索阶段会把全部结果装进内存；完成后结果页秒开并显示全部。期间可以继续浏览其他页面。</div>
  </div>`;
}

function renderRuleSearchError(msg){
  $('view').innerHTML = `<div class="card" style="margin:20px 0">
    <div class="toolbar"><b>搜索未完成</b><div class="spacer"></div>
      <button class="btn" data-act="rule-search-again">重新搜索</button></div>
    <div class="warnbox" style="margin-top:10px">${esc(msg || '未知错误')}</div>
    <div class="hint">常见原因：扫描目录太大超出内存上限、数据正在变化、或扫描尚未完成。可缩小目录或提高最小文件大小后重试。</div>
  </div>`;
}

function renderGroups(){
  const view = $('view');
  if (S.loading && !visibleGroups().length) { view.innerHTML = '<div class="empty">加载中…</div>'; return; }
  // v3.17.0：旧类型标签已由 switchTab 统一映射为 results，S.tab 不会再是 video。
  const head = filtersHtml(S.tab);
  if (!visibleGroups().length) {
    const empty = S.tab === 'results'
      ? '当前筛选下没有重复分组<br><span class="hint">可以切换类型、调整筛选条件，或点「重新搜索」。</span>'
      : '没有找到重复分组<br><span class="hint">尝试调整筛选条件，或先执行一次扫描。</span>';
    view.innerHTML = wrapResult(head, `<div class="empty" style="margin:20px 0">${empty}</div>`);
    updateSelHint();
    // v3.16.0：重绘后重新套用扫描/检测期的写操作置灰（补回的入口同样是写操作）。
    applyOpsGuard();
    if (isResultTab() && !S.assistGroups && !S.assistPromise) ensureAssistGroups().catch(() => {});
    return;
  }
  const oldList = $('groupList');
  const oldScroll = oldList ? oldList.scrollTop : 0;
  // v3.10.8：用结果集签名判断这次是「同一批数据重绘」还是「换了一批数据」。
  // 切页面/切类型筛选/重新扫描/换排序后签名会变，此时沿用旧 scrollTop 会从半截开始显示，
  // 前面几组要往上滚才看得到（看起来像「结果丢了」）。
  const sig = [S.tab, S.resultType, S.groups.length,
    (S.groups[0] || {}).key || '', (S.groups[S.groups.length - 1] || {}).key || ''].join('|');
  const keepScroll = oldList && oldList.dataset.sig === sig ? oldScroll : 0;
  view.innerHTML = wrapResult(head, '');
  if (!S.vlist) resetVirtualList();
  bindVirtualList();
  updateSelHint();
  renderVirtual(true);
  const list = $('groupList');
  if (list) {
    list.dataset.sig = sig;
    if (keepScroll) { list.scrollTop = keepScroll; renderVirtual(true); }
  }
  setTimeout(maybeLoadMore, 80);
  // v3.16.0：同空结果分支，重绘后补一次置灰。
  applyOpsGuard();
  if (isResultTab() && !S.assistGroups && !S.assistPromise) ensureAssistGroups().catch(() => {});
}

function initSelections(){
  // v3.10.8 安全修复：勾选只保留「当前结果集里仍然存在」的条目。
  // 原先只按可见分组裁剪路径，切换类型筛选后旧分组整体从列表里消失，
  // 勾选却留在 S.sel 里，批量「移入隔离区（选中）」会把看不见的文件一起处理。
  const visible = visibleGroups();
  const aliveKeys = new Set(visible.map(g => g.key));
  Object.keys(S.sel).forEach(k => { if (!aliveKeys.has(k)) delete S.sel[k]; });
  const alivePaths = new Set();
  visible.forEach(g => {
    const files = g.files || [];
    // v3.10.13：编码异常文件不可勾选，历史勾选一并剔除，避免混进批量操作
    const operable = files.filter(f => !badName(f));
    operable.forEach(f => alivePaths.add(f.path));
    const set = S.sel[g.key];
    // v3.9.1：默认不勾选任何结果；已有勾选只保留仍存在的路径，避免分组变化后统计残留。
    if (!set) { S.sel[g.key] = new Set(); return; }
    const alive = new Set(operable.map(f => f.path));
    Array.from(set).forEach(p => { if (!alive.has(p)) set.delete(p); });
  });
  // v3.10.8：行选中也要按当前结果集裁剪，否则重扫/切类型后「只看已选行」会筛出空列表，
  // 「填入选中行路径」也会指到已经不存在的文件。
  Array.from(S.rowSel).forEach(p => { if (!alivePaths.has(p)) S.rowSel.delete(p); });
}

// v3.10.8 安全修复：把 S.sel 收敛成「当前结果集里的勾选」，批量操作用它取路径，
// 保证界面上数到多少就处理多少。返回 [{key, paths}]，key 用于逐组校验「至少保留一个」。
function currentSelectionByGroup(){
  const out = [];
  visibleGroups().forEach(g => {
    const set = S.sel[g.key];
    if (!set || !set.size) return;
    const alive = new Set((g.files || []).map(f => f.path));
    const paths = Array.from(set).filter(p => alive.has(p));
    if (paths.length) out.push({ key: g.key, paths });
  });
  return out;
}

// v3.10.8：批量操作统一从这里取路径，禁止再去遍历全量 S.sel——
// 那会把筛选后留在集合里、界面上已经看不到的旧勾选一起带进去。
function currentSelectionPaths(){
  const paths = [];
  currentSelectionByGroup().forEach(x => x.paths.forEach(p => paths.push(p)));
  return paths;
}

// v3.10.8：结果集整体被替换时（切类型筛选、重新扫描、换分页）清掉全部勾选。
// 只删「消失的键」不够用：同一个键在两次结果里都存在时，旧路径会残留下来。
function clearSelection(){
  S.sel = {};
  S.rowSel.clear();
  updateSelHint();
}

function updateSelHint(){
  // v3.10.8：只统计当前结果集里真正会被处理的勾选数，与批量操作实际取到的路径一致，
  // 避免出现「显示已选 N 个、实际只处理 M 个」。
  const n = currentSelectionPaths().length;
  const h = $('selHint');
  if (h) h.textContent = n ? ('已选 ' + n + ' 个文件') : '';
}

// v3.9.3：隔离区按原路径所在目录聚合，组默认折叠，组头直接显示该路径下的隔离数量。
function trashDirOf(p){
  const s = String(p || '');
  const i = s.lastIndexOf('/');
  return i > 0 ? s.slice(0, i) : '/';
}

function trashGroupRows(list){
  return list.map(t => `<tr>
    <td class="sel"><input type="checkbox" data-act="trash-check" data-id="${t.id}" ${S.trashSel.has(t.id)?'checked':''}></td>
    <td class="fname">${esc(t.orig_path.split('/').pop())}</td>
    <td class="fpath" title="${esc(t.orig_path)}">${esc(t.orig_path)}</td>
    <td>${t.reason === 'health' ? '<span class="badge warn">文件健康</span>' : '<span class="badge">查重清理</span>'}${t.exported_at ? ' <span class="badge keep" title="最近一次成功导出备份 ' + esc(fmtT(Number(t.exported_at) * 1e9)) + '">已备份</span>' : ''}</td>
    <td class="num">${fmtB(t.size)}</td>
    <td class="num">${fmtT(Number(t.deleted_at) * 1e9)}</td>
    <td class="act"><button class="btn ghost" data-act="restore-one" data-id="${t.id}">恢复</button>
      <button class="btn danger" data-act="delete-one" data-id="${t.id}">永久删除</button></td>
  </tr>`).join('');
}

function renderTrash(){
  if (S.loading) { $('view').innerHTML = '<div class="empty">加载中…</div>'; return; }
  const items = S.trash || [];
  if (!items.length) {
    $('view').innerHTML = '<div class="empty"><strong>隔离区是空的</strong>从重复分组移入的文件会出现在这里，可恢复或永久删除。</div>';
    return;
  }
  if (!S.trashOpen) S.trashOpen = new Set();
  const open = S.trashOpen;

  // 按目录归组：同一路径下的文件放在一起，默认折叠，避免上千条时一屏铺满。
  const buckets = new Map();
  items.forEach(t => {
    const dir = trashDirOf(t.orig_path);
    if (!buckets.has(dir)) buckets.set(dir, []);
    buckets.get(dir).push(t);
  });
  const dirs = Array.from(buckets.keys()).sort((a, b) => a.localeCompare(b, 'zh'));
  const allOpen = dirs.length > 0 && dirs.every(d => open.has(d));

  const body = dirs.map(dir => {
    const list = buckets.get(dir);
    const size = list.reduce((a, t) => a + (t.size || 0), 0);
    const expanded = open.has(dir);
    const allChecked = list.every(t => S.trashSel.has(t.id));
    return `<div class="group tgroup">
      <div class="tgroup-head">
        <span class="tag" aria-hidden="true">${expanded ? '▾' : '▸'}</span>
        <span class="tgroup-path mono" title="${esc(dir)}">${esc(dir)}</span>
        <span class="gwaste">${list.length} 个</span>
        <span class="hint">合计 ${fmtB(size)}</span>
        <div class="spacer"></div>
        <button class="btn ghost" data-act="trash-group-check" data-dir="${esc(dir)}">${allChecked ? '取消本组勾选' : '勾选本组'}</button>
        <button class="btn ghost" data-act="trash-group-toggle" data-dir="${esc(dir)}">${expanded ? '折叠' : '展开'}</button>
      </div>
      ${expanded ? `<div class="tscroll"><table class="rtable" data-sortkey="trash">
        <colgroup>
          <col data-col="t-sel" style="width:${colWidthFor('t-sel', trashColumns())}px">
          <col data-col="t-name" style="width:${colWidthFor('t-name', trashColumns())}px">
          <col data-col="t-path" style="width:${colWidthFor('t-path', trashColumns())}px">
          <col data-col="t-src" style="width:${colWidthFor('t-src', trashColumns())}px">
          <col data-col="t-size" style="width:${colWidthFor('t-size', trashColumns())}px">
          <col data-col="t-time" style="width:${colWidthFor('t-time', trashColumns())}px">
          <col data-col="t-act" style="width:${colWidthFor('t-act', trashColumns())}px">
        </colgroup>
        <thead><tr>${thCells(trashColumns())}</tr></thead>
        <tbody>${trashGroupRows(list)}</tbody></table></div>` : ''}
    </div>`;
  }).join('');

  // v3.25.2：分页后 items 可能只是总数的一部分，标题按后端 total 显示，并标注已加载条数。
  const total = (typeof S.trashTotal === 'number') ? S.trashTotal : items.length;
  const loadedNote = total > items.length ? `（已加载 ${items.length}）` : '';
  const moreBar = S.trashCursor
    ? `<div class="toolbar"><span class="hint">还有更多隔离记录</span><div class="spacer"></div>
        <button class="btn" data-act="trash-load-more">加载更多</button></div>`
    : '';
  $('view').innerHTML = `<div class="toolbar">
      <span class="hint">共 ${total} 个文件${loadedNote} · ${dirs.length} 个路径 · ${fmtB(items.reduce((a,b)=>a+(b.size||0),0))}</span>
      <div class="spacer"></div>
      <button class="btn ghost" data-act="trash-toggle-all">${allOpen ? '全部折叠' : '全部展开'}</button>
      <button class="btn" data-act="export-trash-sel">导出选中备份</button>
      <button class="btn" data-act="export-trash-all">导出全部备份</button>
      <button class="btn" data-act="restore-sel">恢复选中</button>
      <span class="tb-sep" aria-hidden="true"></span>
      <button class="btn danger" data-act="delete-sel">永久删除选中</button>
      <button class="btn danger" data-act="empty-trash">清空隔离区</button>
    </div>
    ${body}${moreBar}`;
  // 分组目录可能多张表，按同一状态恢复排序
  applyTableSort('trash');
}

// v3.7.0 操作记录页：只读审计列表，含筛选、分页、明细展开与导出。
// v3.10.0：操作记录页以前只有 9 个动作名有中文，其余直接显示 trash_add / api_post_xxx。
// 现在改为「固定表 + 词段表」两级翻译；没登记的接口按 api_<method>_<path> 拆词兜底，
// 实在认不出的片段才原样保留，避免整列都是英文。
const OPLOG_ACTION_LABELS = {
  trash_add:'移入隔离区', trash_restore:'恢复文件', trash_delete:'永久删除', trash_empty:'清空隔离区',
  trash_export:'导出隔离区清单', trash_preview:'隔离区预检',
  login_ok:'登录成功', login_fail:'登录失败', auth_denied:'令牌被拒', logout:'退出登录',
  settings_change:'修改设置', scan_start:'开始扫描', scan_stop:'停止扫描',
  tasks_pause:'暂停全部任务', tasks_resume:'继续全部任务',
  health_scan:'健康检测', health_cancel:'取消健康检测', health_rename:'健康检测·修正文件名',
  health_trash:'健康检测·移入隔离区', health_undo:'健康检测·撤销改名',
  move:'移动文件', move_undo:'撤销移动', relink:'重建硬链接',
  transcode:'提交转码', transcode_cancel:'取消转码', trim:'片头剪除',
  rename_execute:'执行重命名', folder_rename:'文件夹改名', group_rename:'分组改名', file_rename:'单文件改名',
  fanjian:'繁简转换', emptydir_clean:'清理空目录', cache_clear:'清理缓存',
  cleanup_run:'执行清理计划', cleanup_plan_delete:'删除清理计划',
  export:'导出 CSV', report:'生成报告', import:'导入 CSV',
  sync_import:'接收同步数据', sync_export:'导出同步数据',
  fs_mkdir:'新建目录', search_rebuild:'重建搜索索引',
  video_reanalyze:'重新分析视频', intro_reanalyze:'重新分析片头', video_tags_recompute:'重算视频标签',
  retain_pref_clear:'清空保留偏好', retain_pref_apply:'应用保留偏好',
  oplog_export:'导出操作记录', oplog_clear:'清理操作记录'
};

const OPLOG_WORDS = {
  scan:'扫描', stop:'停止', start:'开始', cancel:'取消', run:'执行', clear:'清理', rebuild:'重建',
  groups:'分组', rename:'改名', undo:'撤销', preview:'预览', folder_rename:'文件夹改名', file:'文件',
  folder:'文件夹', move:'移动', trash:'隔离区', restore:'恢复', delete:'删除', empty:'清空',
  health:'健康检测', reanalyze:'重新分析', video_tags:'视频标签', video:'视频', intro:'片头',
  trim:'剪除', probe:'探测', transcode:'转码', status:'状态', cache:'缓存',
  cleanup:'定时清理', plans:'计划', plan:'计划', import:'导入', sessions:'会话', export:'导出',
  sync:'同步', peers:'节点', search:'搜索', fs:'文件系统', mkdir:'新建目录',
  retain:'保留', pref:'偏好', profile:'方案', score:'评分', explain:'解释', apply:'应用',
  rule_search:'规则搜索', rule:'规则', auth:'授权', token:'令牌', tags:'标签', tag:'标签',
  recompute:'重新计算', oplog:'操作记录', tasks:'任务', pause:'暂停', resume:'继续',
  fanjian:'繁简转换', relink:'重建硬链接', report:'报告', metric:'指标', big:'大文件',
  audio:'音频', photo:'图片', cropdetect:'画面裁边探测', failures:'失败清单', settings:'设置'
};

function opLogActionLabel(a){
  if (!a) return '—';
  if (OPLOG_ACTION_LABELS[a]) return OPLOG_ACTION_LABELS[a];
  const m = /^api_(post|put|delete|patch)_(.+)$/.exec(a);
  if (!m) return a;
  const verb = m[1] === 'delete' ? '删除' : '写入';
  const segs = m[2].split('_');
  const out = [];
  for (let i = 0; i < segs.length; i++) {
    const two = segs[i] + '_' + (segs[i + 1] || '');
    if (OPLOG_WORDS[two]) { out.push(OPLOG_WORDS[two]); i++; continue; }
    out.push(OPLOG_WORDS[segs[i]] || segs[i]);
  }
  return verb + ' · ' + out.join('');
}

// 2026-11-08：未完成改名批次提示条。批量改写到一半崩溃/被强杀时，批次行会以 running 留在库里，
// 之前只能翻 verify.log 才知道；这里把它提到界面上，允许用户按批撤销（后端 v3.10.10 已就绪）。
// 空态渲染成 hidden 的空容器：用户没遇到崩溃时整块不占版面。
function unfinishedBatchRows(list){
  return list.map(b => {
    const id = Number(b.batch_id) || 0;
    // preset_name 为空表示预设已被删除，按后端约定显示「临时规则」
    const name = String(b.preset_name || '').trim() || '临时规则';
    const done = Number(b.done) || 0;
    // v3.10.13：后端定稿用 total / started_at，老包仍是 files_count / created_at，两种都认
    const total = Number(b.total !== undefined ? b.total : (b.files_count || 0)) || 0;
    // 时间字段是 Unix 秒；fmtT 接收纳秒，故乘 1e9
    const when = fmtT(Number(b.started_at !== undefined ? b.started_at : (b.created_at || 0)) * 1e9);
    return `<li><span class="mono">#${id}</span><span class="sep">·</span>${esc(name)}
      <span class="sep">·</span>已改 <b>${done}</b> / ${total}<span class="sep">·</span><span class="hint">${esc(when)}</span>
      <button class="btn ghost" data-act="unfinished-undo" data-id="${id}">撤销这批</button></li>`;
  }).join('');
}

// v3.10.13：residual 段的三态。后端只在这三种里出现；未知值原样显示并归到琥珀档，
// 避免将来加新状态时前端直接空白或误显示成成功。
function residualStatusMeta(status){
  return {
    undone_partial: ['部分撤销', 'unfin-warn'],
    undone_failed: ['撤销失败', 'unfin-bad'],
    repaired_partial: ['修复未完成', 'unfin-warn']
  }[status] || [String(status || '未完成'), 'unfin-warn'];
}

// v3.10.12：部分撤销/撤销失败的批次。v3.10.13 起改读后端 residual 段（跨刷新保留），
// 本地残项只在老包没有该段时兜底。
function unfinishedResidualRows(list){
  return list.map(b => {
    const id = Number(b.batch_id) || 0;
    const name = String(b.preset_name || '').trim() || '临时规则';
    const failed = Number(b.failed) || 0;
    const done = Number(b.done) || 0;
    const total = Number(b.total !== undefined ? b.total : (b.files_count || 0)) || 0;
    const when = fmtT(Number(b.started_at !== undefined ? b.started_at : (b.created_at || 0)) * 1e9);
    const [label, cls] = residualStatusMeta(b.status);
    const progress = total ? `<span class="hint">已处理 ${done} / ${total}</span><span class="sep">·</span>` : '';
    const reason = b.error ? `<span class="hint">：${esc(b.error)}</span>` : '';
    return `<li><span class="mono">#${id}</span><span class="sep">·</span>${esc(name)}
      <span class="sep">·</span><span class="${cls}">${esc(label)}</span><span class="sep">·</span>${progress}
      <span class="unfin-bad">${failed} 个文件未恢复</span>${reason}
      <span class="sep">·</span><span class="hint">${esc(when)}</span>
      <button class="btn ghost" data-act="unfin-residual-dismiss" data-id="${id}">知道了</button></li>`;
  }).join('');
}

function unfinishedBarInner(list, resid){
  // 接口固定最多返回 20 条，等于 20 说明可能还有更早的残留，提示用户先撤这批再刷新
  const more = list.length >= 20 ? '（最多显示最近 20 个，撤销后刷新可看到更早的）' : '';
  const running = list.length ? `<div class="unfin-head"><b>${list.length} 个改名批次未完成</b>
      <span class="hint">批量改写到一半中断（进程崩溃或被强制退出），已改名的文件可以按批撤销${more}。</span></div>
    <ul class="unfin-list">${unfinishedBatchRows(list)}</ul>` : '';
  // v3.10.13：这一段现在涵盖 undone_partial / undone_failed / repaired_partial 三类，
  // 统一说「有文件未恢复」比「没撤干净」更准确（修复未完成也走这里）。
  const residual = (resid && resid.length) ? `<div class="unfin-head unfin-head-bad"><b>${resid.length} 个批次有文件未恢复</b>
      <span class="hint">这些批次已不在「进行中」列表里，但仍可能有文件没改回原名或索引没跟上；核对处理后点「知道了」移除，本机会记住不再提示。</span></div>
    <ul class="unfin-list">${unfinishedResidualRows(resid)}</ul>` : '';
  return `<div class="unfin">${running}${residual}</div>`;
}

// 只更新提示条本身，不重绘整页：列表已经渲染好，重绘会丢滚动位置和筛选输入
function syncUnfinishedBar(){
  const host = $('unfinishedBar');
  if (!host) return;
  const list = S.unfinished || [];
  const resid = S.unfinResidual || [];
  const has = list.length + resid.length > 0;
  host.hidden = !has;
  host.innerHTML = has ? unfinishedBarInner(list, resid) : '';
}

// 左侧导航「操作记录」上的角标。用 data-badge + 伪元素而不是文本节点：
// 顶栏标题直接取 activeTab.textContent，塞文本会把数字带进标题。
function paintUnfinishedBadge(n){
  const tab = document.querySelector('.tab[data-tab="oplog"]');
  if (!tab) return;
  const c = Number(n) || 0;
  if (c > 0) {
    tab.dataset.badge = String(c);
    tab.setAttribute('aria-label', `操作记录（${c} 个未完成改名批次）`);
  } else {
    delete tab.dataset.badge;
    tab.removeAttribute('aria-label');
  }
}

function renderOpLog(){
  if (S.loading) { $('view').innerHTML = '<div class="empty">加载中…</div>'; return; }
  const view = S.opView === 'snap' ? 'snap' : 'log';
  // v3.10.13：写操作快照不新开导航项，挂在操作记录页作为第二个视图，
  // 复用这里的表格与筛选样式；两边的「未完成批次」提示条也共用一条。
  const tabs = `<div class="toolbar"><div class="tb-row" style="gap:6px">
      <button class="btn seg${view==='log'?' on':''}" data-act="op-view" data-view="log">操作记录</button>
      <button class="btn seg${view==='snap'?' on':''}" data-act="op-view" data-view="snap">写操作快照</button>
      <span class="hint">${view === 'snap' ? '快照记录每次写操作动了哪些文件，以及它们现在是否还在' : '按时间倒序记录删除、改名、转码等写操作'}</span>
    </div></div>`;
  $('view').innerHTML = `<div id="unfinishedBar" hidden></div>${tabs}${view === 'snap' ? opSnapBody() : opLogBody()}`;
  // 2026-11-08：宿主 div 默认 hidden，数据先到还是后到都要在这里落一次内容
  syncUnfinishedBar();
  if (view === 'log') applyTableSort('oplog');
}

function opLogBody(){
  const st = S.opLog || { items:[], total:0, page:1, pageSize:50 };
  const items = st.items || [];
  const actions = ['trash_add','trash_restore','trash_delete','trash_empty','login_ok','login_fail','auth_denied','settings_change','scan_start'];
  const rows = items.map(r => {
    // 2026-11-08 unslop：整列都是同一个绿色胶囊时，颜色不再传递信息只剩噪声；
    // 改成小圆点 + 文字，失败才用红色，成功保持中性
    const res = r.result === 'ok' ? '<span class="opl-res ok">成功</span>'
      : r.result === 'partial' ? '<span class="opl-res part">部分</span>'
      : '<span class="opl-res fail">失败</span>';
    const actLabel = opLogActionLabel(r.action);
    // 登录类记录的摘要与动作名同义（两列都写「登录成功」），只留一列，明细仍可展开
    const summary = (r.summary || '').trim();
    const showSummary = summary && summary !== actLabel;
    let detail = '';
    if (r.detail_json) {
      let pretty = r.detail_json;
      try { pretty = JSON.stringify(JSON.parse(r.detail_json), null, 2); } catch(e) {}
      detail = `<details><summary class="hint">明细（${r.target_count||0}）</summary><pre class="mono" style="white-space:pre-wrap;word-break:break-all;max-height:14em;overflow:auto">${esc(pretty)}</pre></details>`;
    }
    return `<tr>
      <td class="num">${esc(fmtT(Number(r.ts)*1e6))}</td>
      <td>${esc(r.actor||'—')}</td>
      <td class="opl-act">${esc(actLabel)}</td>
      <td>${res}</td>
      <td>${showSummary?esc(summary):''}${r.error?'<br><span class="hint">'+esc(r.error)+'</span>':''}${detail}</td>
      <td class="num">${r.target_count||0}</td>
      <td class="num">${r.duration_ms||0}</td>
    </tr>`;
  }).join('');
  const maxPage = Math.max(1, Math.ceil((st.total||0)/(st.pageSize||50)));
  return `<div class="toolbar">
      <input id="opKeyword" placeholder="搜索摘要或路径" value="${esc(st.keyword||'')}" style="max-width:210px">
      <select id="opAction" aria-label="动作筛选"><option value="">全部动作</option>${actions
        .map(a=>`<option value="${esc(a)}" ${st.action===a?'selected':''}>${esc(opLogActionLabel(a))}</option>`).join('')}</select>
      <select id="opResult" aria-label="结果筛选">
        <option value="">全部结果</option>
        <option value="ok" ${st.result==='ok'?'selected':''}>成功</option>
        <option value="partial" ${st.result==='partial'?'selected':''}>部分</option>
        <option value="fail" ${st.result==='fail'?'selected':''}>失败</option>
      </select>
      <button class="btn" data-act="oplog-search">查询</button>
      <div class="spacer"></div>
      <button class="btn ghost" data-act="oplog-page" data-delta="-1" ${st.page<=1?'disabled':''}>上一页</button>
      <span class="hint">第 ${st.page||1} / ${maxPage} 页 · 共 ${st.total||0} 条</span>
      <button class="btn ghost" data-act="oplog-page" data-delta="1" ${st.page>=maxPage?'disabled':''}>下一页</button>
      <button class="btn" data-act="oplog-export">导出 CSV</button>
      <span class="tb-sep" aria-hidden="true"></span>
      <button class="btn danger" data-act="oplog-clear">清理旧记录</button>
    </div>
    ${items.length ? (() => {
      const oc = oplogColumns();
      const cw = id => colWidthFor(id, oc);
      const st = id => { const w = cw(id); return w > 0 ? ` style="width:${w}px"` : ''; };
      return `<div class="group"><table class="rtable" data-sortkey="oplog">
      <colgroup>
        <col data-col="ts"${st('ts')}>
        <col data-col="actor"${st('actor')}>
        <col data-col="action"${st('action')}>
        <col data-col="result"${st('result')}>
        <col data-col="detail"${st('detail')}>
        <col data-col="target"${st('target')}>
        <col data-col="duration"${st('duration')}>
      </colgroup>
      <thead><tr>${thCells(oc)}</tr></thead>
      <tbody>${rows}</tbody></table></div>`;
    })()
      : '<div class="empty">还没有操作记录<br><span class="hint">记录从 v3.7.0 起保存；明细超过上限会被截断，完整清单请以隔离区备份为准。</span></div>'}`;
}

// v3.10.13：写操作快照。历史上这里没有对应接口，仅展示「未完成撤销」批次；
// 新接口 GET /api/op-snapshots 到位后，直接复用操作记录页的表格样式与分页习惯。
// 约定字段：{ total, snapshots:[{id, kind, status, created_at, checked, mismatched,
//   diff:{still_exists,missing,changed,path_mismatch,record_lost}, items:[{path,new_path,state,error}] }] }
// 同时兼容 items 命名与 diff 平铺在行上的写法，字段对不上时按 0 处理而不是整页报错。
function snapDiff(s){
  const d = (s && s.diff) || {};
  const n = k => Number(d[k] !== undefined ? d[k] : (s && s[k])) || 0;
  return {
    still_exists: n('still_exists'), missing: n('missing'), changed: n('changed'),
    path_mismatch: n('path_mismatch'), record_lost: n('record_lost')
  };
}

function snapStateMeta(state){
  // 三档色沿用撤销三态：正常中性、需要核对用琥珀、真丢了用红
  return {
    still_exists: ['仍在', 'ok'],
    changed: ['已变化', 'part'],
    path_mismatch: ['路径不符', 'part'],
    // v3.11.1：真机快照里会出现「路径不在受管存储卷内」这类未处理行，单独标「已跳过」
    skipped: ['已跳过', 'part'],
    missing: ['已丢失', 'fail'],
    record_lost: ['记录丢失', 'fail']
  }[state] || [String(state || '未知'), 'part'];
}

function opSnapBody(){
  const st = S.opSnap || { items:[], total:0, page:1, pageSize:20 };
  const items = st.items || [];
  const kinds = ['rename','trash','trim','health','transcode','cleanup','import'];
  const rows = items.map(s => {
    const d = snapDiff(s);
    const bad = d.missing + d.record_lost;
    const warn = d.changed + d.path_mismatch;
    // v3.11.1：finished_at 固定是 Unix 秒，0 表示快照没跑完（进程中断/被强杀）。
    // 未完成必须优先标出，绝不能落进绿色「全部仍在」；老包没有该字段时仍按差异三档显示。
    // result=ok 已排除未完成快照，但 result=all 里会出现，所以这里必须认出来。
    const hasFin = s.finished_at !== undefined && s.finished_at !== null;
    const unfinished = hasFin && Number(s.finished_at) === 0;
    const res = unfinished ? '<span class="opl-res wip">未完成（中断）</span>'
      : bad ? '<span class="opl-res fail">有文件丢失</span>'
      : warn ? '<span class="opl-res part">有变化</span>'
      : '<span class="opl-res ok">全部仍在</span>';
    // v3.10.13：created_at 是 Unix 秒；fmtT 接收纳秒，故乘 1e9
    const when = fmtT(Number(s.created_at || s.ts || 0) * 1e9);
    const list = s.items || s.files || [];
    // v3.13.0（UI 审查 P3-5）：是否截断由后端 items_truncated 判定；
    // 老包没有该字段时退回按 200 条估，避免旧版本一句话都不提示。
    const truncated = s.items_truncated !== undefined ? !!s.items_truncated : list.length >= 200;
    const cut = truncated ? `<span class="hint">（明细较多，仅显示前 ${list.length} 条）</span>` : '';
    const detail = list.length ? `<details><summary class="hint">动过的文件（${list.length}）${cut}</summary>
        <ul class="snaplist">${list.map(it => {
          const [label, cls] = snapStateMeta(it.state || it.status);
          const extra = it.error ? `<span class="hint"> · ${esc(it.error)}</span>` : '';
          return `<li><span class="opl-res ${cls}">${esc(label)}</span>
            <span class="mono">${esc(it.path || '')}</span>${extra}</li>`;
        }).join('')}</ul></details>` : '';
    const diffText = [
      d.still_exists ? `仍在 ${d.still_exists}` : '',
      d.changed ? `变化 ${d.changed}` : '',
      d.path_mismatch ? `路径不符 ${d.path_mismatch}` : '',
      d.missing ? `丢失 ${d.missing}` : '',
      d.record_lost ? `记录丢失 ${d.record_lost}` : ''
    ].filter(Boolean).join(' · ');
    // 未完成快照的差异计数不完整，明确告诉用户「别当成核对结论」
    const finNote = unfinished ? '<div class="hint">快照未完成，差异统计不完整，仅供参考</div>' : '';
    return `<tr>
      <td class="num">${esc(when)}</td>
      <td class="opl-act">${esc(opSnapKindLabel(s.kind || s.action))}</td>
      <td>${res}</td>
      <td class="num">${Number(s.checked || 0)}</td>
      <td>${diffText ? esc(diffText) : '<span class="hint">—</span>'}${detail}${finNote}</td>
    </tr>`;
  }).join('');
  const maxPage = Math.max(1, Math.ceil((st.total||0)/(st.pageSize||20)));
  const err = st.error ? `<div class="empty">快照接口暂不可用<br><span class="hint">${esc(st.error)}</span></div>` : '';
  return `<div class="toolbar">
      <input id="snapKeyword" placeholder="搜索路径" value="${esc(st.keyword||'')}" style="max-width:210px">
      <select id="snapKind" aria-label="操作类型筛选"><option value="">全部类型</option>${kinds
        .map(k=>`<option value="${esc(k)}" ${st.kind===k?'selected':''}>${esc(opSnapKindLabel(k))}</option>`).join('')}</select>
      <select id="snapResult" aria-label="核对结果筛选">
        <option value="all" ${st.result==='all'?'selected':''}>全部结果</option>
        <option value="ok" ${st.result==='ok'?'selected':''}>全部仍在</option>
        <option value="mismatch" ${st.result==='mismatch'?'selected':''}>有差异</option>
      </select>
      <button class="btn" data-act="snap-search">查询</button>
      <div class="spacer"></div>
      <button class="btn ghost" data-act="snap-page" data-delta="-1" ${st.page<=1?'disabled':''}>上一页</button>
      <span class="hint">第 ${st.page||1} / ${maxPage} 页 · 共 ${st.total||0} 条</span>
      <button class="btn ghost" data-act="snap-page" data-delta="1" ${st.page>=maxPage?'disabled':''}>下一页</button>
    </div>
    ${err || (items.length ? `<div class="group"><table class="rtable" data-sortkey="snap">
      <colgroup><col data-col="ts" style="width:160px"><col data-col="kind" style="width:120px"><col data-col="result" style="width:120px"><col data-col="checked" style="width:90px"><col data-col="detail"></colgroup>
      <thead><tr><th>时间</th><th>类型</th><th>核对结果</th><th>检查数</th><th>差异与明细</th></tr></thead>
      <tbody>${rows}</tbody></table></div>`
      : '<div class="empty">还没有写操作快照<br><span class="hint">快照在删除、改名、剪除等写操作执行后生成，可在这里核对文件是否还在。</span></div>')}`;
}

function opSnapKindLabel(kind){
  return {
    rename: '批量改名', trash: '移入隔离区', trim: '剪除片头', health: '健康修复',
    transcode: '视频转码', cleanup: '定时清理', import: '导入'
  }[kind] || (kind || '写操作');
}

function renderPhoto(){
  const v = $('view');
  const groups = S.photoGroups || [];
  const total = groups.reduce((a,g)=>a+(g.files||[]).length, 0);
  const selN = S.photoSel.size;
  // v3.9.6 修 V-29：相似图片页也加缩略图进度组件，复用全局 paintThumbProg（之前只有大文件页有）
  const head = `<div class="toolbar">
    ${rpStats(groups, '张')}
    <span class="badge">阈值 ${S.photoThreshold ?? 10}</span>
    ${selN ? `<span class="badge warn">已选 ${selN}</span>` : ''}
    ${rpSortBar('photo')}
    <div class="spacer"></div>
    <span class="thumbprog" id="thumbProg">${thumbProgInner()}</span>
    <button class="btn" id="thumbBtn" data-act="photo-thumbs" title="后台批量预生成全部相似图片的缩略图（并发 4，结果存缓存）">一键生成缩略图</button>
    <button class="btn" data-act="photo-compare" ${selN === 2 ? '' : ' disabled'} title="勾选组内两张图片后可左右对比">对比所选（选 2 张）</button>
    <button class="btn" data-act="photo-trash-sel">隔离所选</button>
  </div>`;
  if (!groups.length) {
    v.innerHTML = head + `<div class="card hint" style="padding:18px">
      没有发现相似图片。图片指纹随扫描自动计算（jpg/png/gif/bmp/tiff/webp）；
      HEIC/AVIF/JXL/RAW/SVG 不受支持——HEIC 是 iPhone 默认格式，相册同步前需先转换。</div>`;
    return;
  }
  const body = groups.map(g => {
    const gkey = g.key;
    const cells = (g.files||[]).map(f => photoCellHtml(f, gkey)).join('');
    return rpSec('photo', gkey, `${esc(g.name)} <span class="hint">等 ${g.count} 张相似 · 共 ${fmtB(g.total_size)}</span>`,
      `<div class="pgrid">${cells}</div>`);
  }).join('');
  v.innerHTML = head + body;
}

// ---------- 重复音频 / 重复文件夹（v1.12.0）----------
// 列表形态：组卡片 + 文件行勾选 + 批量隔离（复用保护预检）。
function mediaRowHtml(f, kind){
  const selSet = kind === 'audio' ? S.audioSel : S.folderSel;
  const sel = selSet.has(f.path);
  return `<label class="mrow${sel?' mrow-sel':''}">
    <input type="checkbox" data-act="${kind}-sel" data-path="${esc(f.path)}"${sel?' checked':''}>
    <span class="mname" title="${esc(f.path)}">${esc(f.name)}</span>
    <span class="msize">${fmtB(f.size)}</span>
    ${isProtectedPath(f.path) ? '<span class="badge">保护</span>' : ''}
  </label>`;
}

// ---------- 结果页统一基建（v1.24.0）：统计条 / 排序条 / 组折叠 / 批量缩略图 ----------
// rpStats 统计条：组数 + 文件数 + 总大小 + 可释放（waste 可选）。
function rpStats(groups, unit){
  const total = (groups || []).reduce((a, g) => a + (g.files || []).length, 0);
  const size = (groups || []).reduce((a, g) => a + (g.total_size || 0), 0);
  const waste = (groups || []).reduce((a, g) => a + (g.waste || 0), 0);
  let s = `<span class="badge">${(groups || []).length} 组 / ${total.toLocaleString()} ${unit || '个'}</span>`;
  if (size > 0) s += ` <span class="badge">共 ${fmtB(size)}</span>`;
  if (waste > 0) s += ` <span class="badge good">可释放 ${fmtB(waste)}</span>`;
  return s;
}

// rpSortBar 排序下拉（走后端全局排序，翻页跨页一致）。
function rpSortBar(pageKey){
  const keys = [['waste', '可释放空间'], ['total_size', '组总大小'], ['count', '文件数量'], ['name', '名称']];
  const cur = S.rpSort && S.rpSort[pageKey] ? S.rpSort[pageKey] : 'waste';
  const curOrd = S.rpSortOrder && S.rpSortOrder[pageKey] ? S.rpSortOrder[pageKey] : 'desc';
  return `<select data-act="rp-sort" data-page="${pageKey}" title="排序（后端全局排序，翻页一致）">
    ${keys.map(k => `<option value="${k[0]}"${cur === k[0] ? ' selected' : ''}>${k[1]}</option>`).join('')}
  </select>
  <select data-act="rp-sort-order" data-page="${pageKey}">
    <option value="desc"${curOrd !== 'asc' ? ' selected' : ''}>降序</option>
    <option value="asc"${curOrd === 'asc' ? ' selected' : ''}>升序</option>
  </select>`;
}

// rpSec 折叠组：点击标题行切换；folded 时正文不渲染（组数多时省 DOM）。
function rpSec(tab, gkey, titleHtml, bodyHtml){
  const k = tab + ':' + gkey;
  const folded = S.rpCollapsed.has(k);
  return `<div class="section rp-sec">
    <h2 class="section-title rp-head" data-act="rp-fold" data-key="${esc(k)}" title="点击折叠/展开">
      <span class="rp-arrow">${folded ? '▸' : '▾'}</span>${titleHtml}
    </h2>
    ${folded ? '' : bodyHtml}
  </div>`;
}

// rpDefaultFold tab 页默认折叠所有组（folder 用）；展开时逐个删除 key。
function rpDefaultFold(tab, groups){
  (groups || []).forEach(g => S.rpCollapsed.add(tab + ':' + g.key));
}

// thumbProgInner 工具栏里的进度片段：条 + 文字，能看出是在跑还是卡住了。
function thumbProgInner(){
  const j = S.thumbJob;
  if (!j) return '';
  const n = j.done + j.fail;
  const pct = j.total ? Math.min(1, n / j.total) : 0;
  const text = j.running
    ? `正在生成${esc(j.label || '')}缩略图 ${n}/${j.total}${j.fail ? `（失败 ${j.fail}）` : ''}`
    : `缩略图：成功 ${j.done}，失败 ${j.fail}` + (j.skip ? `，跳过 ${j.skip}` : '');
  return `<span class="pbar"><i style="--p:${pct.toFixed(3)}"></i></span><span class="hint">${text}</span>`;
}

// paintThumbProg 只刷新进度节点，避免整页重绘把滚动位置和已加载的图弄丢。
function paintThumbProg(){
  const el = document.getElementById('thumbProg');
  if (el) el.innerHTML = thumbProgInner();
  const btn = document.getElementById('thumbBtn');
  if (btn) {
    const running = !!(S.thumbJob && S.thumbJob.running);
    btn.disabled = running;
    btn.textContent = running ? '生成中…' : '一键生成缩略图';
  }
}

function mediaHead(groups, selSet, kind, label){
  const selN = selSet.size;
  return `<div class="toolbar">
    ${rpStats(groups, '个')}
    ${selN ? `<span class="badge warn">已选 ${selN}</span>` : ''}
    ${rpSortBar(kind)}
    <div class="spacer"></div>
    <button class="btn" data-act="${kind}-trash-sel">隔离所选</button>
  </div>`;
}

function mediaBodyHtml(kind){
  if (kind === 'folder' && S.folderDisabled) {
    return `<div class="card hint" style="padding:18px">已在设置里关闭「计算重复文件夹」。需要时到「设置 → 查重依据与判定」重新打开；关掉只影响这一页的打开速度，扫描速度不受影响。</div>`;
  }
  const groups = kind === 'audio' ? S.audioGroups : S.folderGroups;
  if (!groups.length) {
    const hint = kind === 'audio'
      ? '没有发现重复音频。音频指纹随扫描自动计算（mp3/flac/m4a/aac/ogg/wav/wma/ape，ffmpeg 可解码者）；判定阈值同「相似视频」的音频相似度设置。'
      : '没有发现重复文件夹。文件夹查重按「目录内全部已哈希文件的名字+大小+哈希签名」判定（单层，不含子目录内容），至少 2 个文件参与。';
    return `<div class="card hint" style="padding:18px">${hint}</div>`;
  }
  if (kind === 'audio') {
    return groups.map(g => rpSec('audio', g.key,
      `${esc(g.name)} <span class="hint">${g.count} 个 · 共 ${fmtB(g.total_size)} · 可释放 ${fmtB(g.waste)}</span>`,
      `<div class="mlist">${(g.files||[]).map(f => mediaRowHtml(f, 'audio')).join('')}</div>`)).join('');
  }
  // 文件夹组：组内按目录二级分组；每组保留一个目录（字典序第一个路径，同签名目录内容完全相同），
  // 其余目录默认全选——打开页面直接「隔离所选」即完成一键处理。
  return groups.map(g => {
    const byDir = {};
    (g.files || []).forEach(f => {
      const d = f.path.replace(/\/[^/]*\/?$/, '') || '/';
      (byDir[d] = byDir[d] || []).push(f);
    });
    const dirs = Object.keys(byDir).sort();
    const keep = dirs[0];
    const dirHtml = dirs.map(d => {
      const isKeep = d === keep;
      const files = byDir[d];
      const allSel = files.every(f => S.folderSel.has(f.path));
      return `<div class="dgroup${isKeep ? ' dgroup-keep' : ''}">
        <div class="dhead">
          <input type="checkbox" data-act="folder-dir" data-dir="${esc(d)}"${allSel ? ' checked' : ''}${isKeep ? ' disabled' : ''} title="${isKeep ? '建议保留的目录' : '勾选/取消整个目录'}">
          <span class="dpath" title="${esc(d)}">${esc(d)}</span>
          ${isKeep ? '<span class="badge keep">建议保留</span>' : '<span class="badge del">重复</span>'}
          <span class="hint">${files.length} 个 · ${fmtB(files.reduce((a, f) => a + f.size, 0))}</span>
        </div>
        <div class="mlist">${files.map(f => mediaRowHtml(f, 'folder')).join('')}</div>
      </div>`;
    }).join('');
    return rpSec('folder', g.key,
      `互为副本的 ${dirs.length} 个文件夹 <span class="hint">${g.count} 个文件 · 共 ${fmtB(g.total_size)} · 可释放 ${fmtB(g.waste)}</span>`,
      dirHtml);
  }).join('');
}

function renderAudio(){
  $('view').innerHTML = mediaHead(S.audioGroups || [], S.audioSel, 'audio', '音频') + mediaBodyHtml('audio');
}

function renderFolder(){
  // v1.24.0：文件夹页接入 resultlayout——选择助手栏常驻（用户已确认：不依赖检测结果）
  const head = mediaHead(S.folderGroups || [], S.folderSel, 'folder', '文件夹');
  const inner = mediaBodyHtml('folder');
  $('view').innerHTML = `<div class="resultlayout">
    <aside class="assist ${S.assistOpen ? '' : 'collapsed'}" id="assist">${assistBodyHtml()}</aside>
    <div class="assist-tools">${head}<div>${inner}</div></div>
  </div>`;
  applyAsecFold($('view'));
}

function renderBig(){
  const files = S.bigFiles || [];
  // v3.9.6：骨架屏改成 table 结构，保证列宽与真正渲染一致
  if (S.loading && !files.length) {
    const head = `<div class="toolbar">
      <span class="badge">加载中…</span>
    </div>`;
    const skel = '<div class="tscroll"><table class="rtable" data-sortkey="big">' +
      '<colgroup>' + bigColumns().map(c => { const w = colWidthFor(c.id, bigColumns()); return `<col data-col="${c.id}"${w ? ` style="width:${w}px"` : ''}>`; }).join('') + '</colgroup>' +
      '<thead><tr>' + thCells(bigColumns()) + '</tr></thead>' +
      '<tbody>' + Array.from({length: 8}, () =>
        '<tr>' + bigColumns().map(c => {
          if (c.id === 'b-sel') return '<td class="sel"><input disabled></td>';
          if (c.id === 'b-thumb') return '<td><div class="skeleton skeleton-sq"></div></td>';
          return '<td><div class="skeleton skeleton-row-ph"></div></td>';
        }).join('') + '</tr>').join('') +
      '</tbody></table></div>';
    $('view').innerHTML = head + skel;
    return;
  }
  const totalSize = files.reduce((a, f) => a + (f.size || 0), 0);
  // v3.19.0：竖横屏识别与批量隔离走公共进度卡，运行中禁用发起按钮，避免重复提交。
  const cropRunning = !!(BATCHED_OPS['big-crop'] && BATCHED_OPS['big-crop'].running);
  const trashRunning = !!(BATCHED_OPS['big-trash'] && BATCHED_OPS['big-trash'].running);
  const opCards = batchedOpCardHtml('big-trash') + batchedOpCardHtml('big-crop');
  const head = `<div class="toolbar">
    <span class="badge">${files.length.toLocaleString()} 个文件</span>
    <span class="badge">共 ${fmtB(totalSize)}</span>
    <select id="bigOrder" data-act="big-order" style="margin-left:8px">
      <option value="big"${S.bigOrder !== 'small' ? ' selected' : ''}>从大到小</option>
      <option value="small"${S.bigOrder === 'small' ? ' selected' : ''}>从小到大</option>
    </select>
    <select id="bigCrop" data-act="big-crop" style="margin-left:8px" title="按有效画面长宽比筛选（需先做黑边识别）">
      <option value=""${!S.bigCrop ? ' selected' : ''}>全部画面比例</option>
      <option value="landscape"${S.bigCrop === 'landscape' ? ' selected' : ''}>横屏</option>
      <option value="portrait"${S.bigCrop === 'portrait' ? ' selected' : ''}>竖屏</option>
      <option value="square"${S.bigCrop === 'square' ? ' selected' : ''}>方形</option>
    </select>
    <button class="btn" data-act="big-cropdetect" title="对未检测的视频跑 ffmpeg cropdetect（每批 50 个，多点采样取众数），可暂停或取消"${cropRunning ? ' disabled' : ''}>${cropRunning ? '识别中…' : '识别竖横屏'}</button>
    ${S.bigSel.size ? `<span class="badge warn">已选 ${S.bigSel.size}</span>` : ''}
    <div class="spacer"></div>
    <span class="thumbprog" id="thumbProg">${thumbProgInner()}</span>
    <button class="btn" id="thumbBtn" data-act="big-thumbs" title="批量预生成视频缩略图（并发 6，结果存缓存；图片直接用原图，不需要生成）"${S.thumbJob && S.thumbJob.running ? ' disabled' : ''}>${S.thumbJob && S.thumbJob.running ? '生成中…' : '一键生成缩略图'}</button>
    <!-- 2026-11-08 unslop 打磨：危险操作与工具按钮之间加发丝分隔，避免同权重并排 -->
    <span class="tb-sep" aria-hidden="true"></span>
    <button class="btn" data-act="big-trash-sel"${trashRunning ? ' disabled' : ''}>隔离所选</button>
  </div>`;
  if (!files.length) {
    $('view').innerHTML = opCards + head + `<div class="card hint" style="padding:var(--space-md)">没有可显示的文件。大文件列表只覆盖已索引文件——先完成一次扫描，或调整扫描根范围。</div>`;
    return;
  }
  const stamp = S.thumbStamp ? '&r=' + S.thumbStamp : '';
  // v3.9.6：改成 table-layout:fixed + colgroup + colresize，列宽可拖动
  const cols = bigColumns();
  // 2026-11-08 unslop 打磨：路径列常年用不满而文件名整列被截断；缩略图列放宽到 88 容纳 80px 图（用户拖过的宽度仍优先）
  cols.find(c => c.id === 'b-thumb').width = 88;
  cols.find(c => c.id === 'b-name').width = 360;
  const colHtml = '<colgroup>' + cols.map(c => {
    const w = colWidthFor(c.id, cols);
    return `<col data-col="${c.id}"${w ? ` style="width:${w}px"` : ''}>`;
  }).join('') + '</colgroup>';
  const thead = '<thead><tr>' + thCells(cols) + '</tr></thead>';
  const rows = files.map(f => {
    const sel = S.bigSel.has(f.path);
    const ext = nameExt(f.path);
    let thumb;
    if (VID_EXT.includes(ext)) {
      thumb = `<img class="rowthumb" loading="lazy" decoding="async" alt="" data-ext="${esc(ext)}" src="/api/thumb?path=${encodeURIComponent(f.path)}&t=1${stamp}" data-act="preview" data-path="${esc(f.path)}" data-name="${esc(f.name)}">`;
    } else if (IMG_EXT.includes(ext)) {
      thumb = `<img class="rowthumb" loading="lazy" decoding="async" alt="" data-ext="${esc(ext)}" src="/api/raw?path=${encodeURIComponent(f.path)}${stamp}" data-act="preview" data-path="${esc(f.path)}" data-name="${esc(f.name)}">`;
    } else {
      thumb = `<div class="rowthumb ph">${esc(ext || '—')}</div>`;
    }
    return `<tr class="${sel ? 'rowsel' : ''}">
      <td class="sel"><input type="checkbox" data-act="big-sel" data-path="${esc(f.path)}"${sel ? ' checked' : ''}></td>
      <td>${thumb}</td>
      <td class="fname${isProtectedPath(f.path) ? ' is-protected' : ''}" title="${esc(f.path)}">${esc(f.name)}${isProtectedPath(f.path) ? ' <span class="badge">保护</span>' : ''}</td>
      <td class="fpath hint" title="${esc(f.path)}">${esc(f.volume || '')}</td>
      <td class="num">${fmtB(f.size)}</td>
      <td class="num">${fmtT(f.mtime)}</td>
    </tr>`;
  }).join('');
  // v3.9.6：用 table 容器，和其他有 colresize 的页面统一
  $('view').innerHTML = opCards + head + `<div class="tscroll"><table class="rtable" data-sortkey="big">${colHtml}${thead}<tbody>${rows}</tbody></table></div>`;
  applyTableSort('big');
}

// 导出菜单（v1.41.8）：点「导出 CSV」展开格式/范围组合，选一项直接导出
function exportMenuHtml(){
  return ['dc5','readable'].map(f => ['all','marked'].map(sc =>
    `<button class="expitem" data-act="export-run" data-fmt="${f}" data-scope="${sc}">${f === 'dc5' ? 'DC5 兼容' : '通用可读'} · ${sc === 'all' ? '全部行' : '仅标记行'}${f === 'dc5' ? '（Duplicate Cleaner 5）' : ''}</button>`
  ).join('')).join('');
}

// v3.17.1：结果页 S.tab 恒为 results，而后端报告/导出只认旧页面模式名，
// 直接传 results 会被拒（400）。按当前结果类型翻译，不支持的类型回退精确重复。
const REPORT_MODES = ['exact', 'name', 'video', 'intro', 'samevideo'];
function reportMode(){
  if (REPORT_MODES.includes(S.resultType)) return S.resultType;
  if (REPORT_MODES.includes(S.tab)) return S.tab;
  return 'exact';
}

function reportBody(){
  // v3.10.10（第六轮 P1-2）：导出/HTML 报告以前只带 min_size，最大体积、日期、扩展名
  // 筛选全丢，用户以为导出的是筛过的结果，实际导出全库。这里补齐后端已支持的字段。
  const lf = S.listFilter || {};
  return {
    mode: reportMode(),
    marked: markedPaths(),
    min_size: lf.minKB ? Math.round(Number(lf.minKB) * 1024) : 0,
    max_size: lf.maxKB ? Math.round(Number(lf.maxKB) * 1024) : 0,
    ext: '',
    exts: String(lf.exts || '').split(',').map(s => s.trim().toLowerCase().replace(/^\./, '')).filter(Boolean),
    q: lf.q || '',
    // files.mtime 是纳秒，JS getTime() 是毫秒
    since: lf.since ? new Date(lf.since + 'T00:00:00').getTime() * 1e6 : 0,
    until: lf.until ? new Date(lf.until + 'T00:00:00').getTime() * 1e6 : 0,
    n: Number(S.introN || 10)
  };
}

function renderFuzzy(){
  const fz = S.fuzzy;
  if (!fz) return;
  if (fz.loading) { $('view').innerHTML = '<div class="empty">计算中…（按库大小可能需要几十秒，完成后有缓存）</div>'; return; }
  const pages = Math.max(1, Math.ceil(fz.total / 20));
  const rows = (fz.groups || []).map(g => {
    const files = (g.files || []).map(f =>
      '<tr><td class="mono">' + fmtB(f.size) + '</td><td>' + esc(f.name) + '</td><td class="mono" style="color:var(--muted)">' + esc(f.path) + '</td></tr>'
    ).join('');
    return '<div class="card" style="margin-bottom:12px"><div style="display:flex;gap:10px;align-items:baseline;padding:8px 12px;border-bottom:1px solid var(--line)">' +
      '<b>' + esc(g.name) + '</b><span class="hint">音序键：' + esc(g.key) + ' · ' + g.count + ' 个 · 合计 ' + fmtB(g.total_size) + '（可回收 ' + fmtB(g.waste) + '）</span></div>' +
      '<table class="ctable"><tbody>' + files + '</tbody></table></div>';
  }).join('');
  const pager = pages > 1 ? '<div class="toolbar" style="justify-content:center;gap:8px;margin-top:12px">' +
    '<button class="btn ghost" data-act="fuzzy-page" data-p="' + (fz.page - 1) + '"' + (fz.page <= 1 ? ' disabled' : '') + '>上一页</button>' +
    '<span class="hint">' + fz.page + ' / ' + pages + '</span>' +
    '<button class="btn ghost" data-act="fuzzy-page" data-p="' + (fz.page + 1) + '"' + (fz.page >= pages ? ' disabled' : '') + '>下一页</button></div>' : '';
  $('view').innerHTML =
    '<div class="toolbar" style="gap:10px;flex-wrap:wrap;margin-bottom:10px">' +
    '<label class="checkline"><input type="checkbox" id="fuzzyNear" data-act="fuzzy-near"' + (fz.near ? ' checked' : '') + '> 近似匹配（音序键单字符差异也归组）</label>' +
    '<span class="hint">共 ' + fz.total + ' 组。文件主名统一转无调拼音后比较（"沙丘" 与 "shaqiu" 归同组）；纯数字/符号名不参与；仅分组展示，清理请在精确/同名页操作。</span></div>' +
    (rows || '<div class="empty">没有模糊重复组</div>') + pager;
}

function renderSemantic(){
  const fz = S.semantic;
  if (!fz) return;
  if (fz.loading) { $('view').innerHTML = '<div class="empty">计算中…（按库大小可能需要几十秒，完成后有缓存）</div>'; return; }
  const pages = Math.max(1, Math.ceil(fz.total / 20));
  const rows = (fz.groups || []).map(g => {
    const files = (g.files || []).map(f =>
      '<tr><td class="mono">' + fmtB(f.size) + '</td><td>' + esc(f.name) + '</td><td class="mono" style="color:var(--muted)">' + esc(f.path) + '</td></tr>'
    ).join('');
    return '<div class="card" style="margin-bottom:12px"><div style="display:flex;gap:10px;align-items:baseline;padding:8px 12px;border-bottom:1px solid var(--line)">' +
      '<b>' + esc(g.name) + '</b><span class="hint">核心键：' + esc(g.key) + ' · ' + g.count + ' 个 · 合计 ' + fmtB(g.total_size) + '</span></div>' +
      '<table class="ctable"><tbody>' + files + '</tbody></table></div>';
  }).join('');
  const pager = pages > 1 ? '<div class="toolbar" style="justify-content:center;gap:8px;margin-top:12px">' +
    '<button class="btn ghost" data-act="semantic-page" data-p="' + (fz.page - 1) + '"' + (fz.page <= 1 ? ' disabled' : '') + '>上一页</button>' +
    '<span class="hint">' + fz.page + ' / ' + pages + '</span>' +
    '<button class="btn ghost" data-act="semantic-page" data-p="' + (fz.page + 1) + '"' + (fz.page >= pages ? ' disabled' : '') + '>下一页</button></div>' : '';
  $('view').innerHTML =
    '<div class="hint" style="margin-bottom:10px">文件名剥掉分辨率/编码/片源/语言等噪声后按核心键分组（"沙丘.2016.1080p" 与 "shaqiu 2016" 归同组）；仅辅助展示，<b>不参与删除判定</b>，清理请在精确/同名页操作。噪声词典在设置页编辑。</div>' +
    (rows || '<div class="empty">没有语义重复组</div>') + pager;
}

function renderSearchPage(){
  const st = searchState();
  // v3.20.0：筛选与排序不再折叠，进入页面就能看到全部条件。
  $('view').innerHTML = `
  <div class="toolbar" style="flex-wrap:wrap;gap:8px">
    <input id="sQuery" placeholder="文件名或路径关键词（支持 * ? 通配符，可选正则）" value="${esc(st.q)}" style="flex:1;min-width:220px">
    <select id="sMode">
      <option value="substring"${st.mode==='substring'?' selected':''}>包含</option>
      <option value="wildcard"${st.mode==='wildcard'?' selected':''}>通配符 * ?</option>
      <option value="regex"${st.mode==='regex'?' selected':''}>正则</option>
    </select>
    <label class="checkline" style="font-size:12px"><input type="checkbox" id="sCase"${st.caseSensitive?' checked':''}> 区分大小写</label>
    <label class="checkline" style="font-size:12px" title="选中当前查询的全部结果，不只是已经滚动加载出来的行"><input type="checkbox" id="sSelAll"${st.selAll?' checked':''}> 全选全部结果</label>
    <button class="btn" data-act="search-add-rename">加入重命名列表（${searchSelCount()}）</button>
    <button class="btn ghost" data-act="search-clear-sel">清空选择</button>
    <span class="hint" id="sSelInfo"></span>
    <span class="hint" id="sInfo"></span>
  </div>
  <div class="searchfilter">
    <div class="filtergrid">
      <label>扩展名<input id="sExt" placeholder="mp4, mkv（逗号分隔）" value="${esc(st.ext)}"></label>
      <label>最小体积 KB<input id="sMin" type="number" min="0" placeholder="不限" value="${esc(st.minKB)}"></label>
      <label>最大体积 KB<input id="sMax" type="number" min="0" placeholder="不限" value="${esc(st.maxKB)}"></label>
      <label>起始日期<input id="sSince" type="date" value="${esc(st.since)}"></label>
      <label>结束日期<input id="sUntil" type="date" value="${esc(st.until)}"></label>
      <label>目录<span class="inline"><input id="sDir" readonly placeholder="全部扫描目录" value="${esc(st.dir)}"><button class="btn ghost" data-act="search-dir">选择…</button></span></label>
      <span class="inline"><button class="btn ghost" data-act="move-dir-start" title="把当前选定的目录（上面的「目录」筛选值）整体移动到别的位置：同卷瞬时完成，索引与操作记录同步，可撤销">移动目录…</button></span>
      <label>排序<select id="sSort">
        <option value="name"${st.sort==='name'?' selected':''}>文件名</option>
        <option value="path"${st.sort==='path'?' selected':''}>路径</option>
        <option value="ext"${st.sort==='ext'?' selected':''}>扩展名</option>
        <option value="size"${st.sort==='size'?' selected':''}>体积</option>
        <option value="mtime"${st.sort==='mtime'?' selected':''}>修改时间</option>
        <option value="volume"${st.sort==='volume'?' selected':''}>所在卷</option>
      </select></label>
      <label>顺序<select id="sOrder">
        <option value="asc"${st.order==='asc'?' selected':''}>升序</option>
        <option value="desc"${st.order==='desc'?' selected':''}>降序</option>
      </select></label>
      <button class="btn ghost" data-act="search-reset-filter">清空筛选</button>
    </div>
  </div>
  <div id="sResults"></div>
  <div id="renamePanel"></div>`;
  renderSearchResults();
  renderRenamePanel();
  const inp = $('sQuery');
  if (inp) {
    inp.addEventListener('input', () => {
      clearTimeout(searchTimer);
      searchTimer = setTimeout(() => { searchState().q = inp.value; doSearch(); }, 300);
    });
    inp.addEventListener('keydown', (e) => { if (e.key === 'Enter') { clearTimeout(searchTimer); searchState().q = inp.value; doSearch(); } });
  }
  if ($('sMode')) $('sMode').addEventListener('change', () => { searchState().mode = $('sMode').value; doSearch(); });
  if ($('sCase')) $('sCase').addEventListener('change', () => { searchState().caseSensitive = $('sCase').checked; doSearch(); });
  if ($('sSelAll')) $('sSelAll').addEventListener('change', () => searchSelectAll($('sSelAll').checked));
  // v3.9.10：6 类筛选与排序统一接线，任一条件变化都从头重查
  const bindFilter = (id, key, read) => {
    const el = $(id); if (!el) return;
    el.addEventListener('change', () => {
      const v = read(el);
      if (st[key] === v) return;
      st[key] = v; doSearch();
    });
  };
  bindFilter('sExt', 'ext', el => el.value.trim());
  bindFilter('sMin', 'minKB', el => el.value);
  bindFilter('sMax', 'maxKB', el => el.value);
  bindFilter('sSince', 'since', el => el.value);
  bindFilter('sUntil', 'until', el => el.value);
  bindFilter('sSort', 'sort', el => el.value);
  bindFilter('sOrder', 'order', el => el.value);
  updateSearchInfo();
  updateSearchButtons();
}

function updateSearchInfo(){
  const st = searchState(); const el = $('sInfo'); if (!el) return;
  if (st.loading) { el.textContent = '搜索中…'; return; }
  // 降级态由后端回退 SQL，只支持子串且忽略排序与目录，必须明说否则用户以为筛选失效
  if (st.notice) { el.textContent = st.notice + (st.ready ? '' : '；降级模式下排序与目录筛选不生效'); return; }
  if (!st.q) { el.textContent = st.ready ? '索引已就绪' : '索引构建中…'; return; }
  el.textContent = '命中 ' + st.total.toLocaleString() + ' 个 · 用时 ' + st.took + ' ms' + (st.ready ? '' : ' · 索引未就绪（降级）');
}

// v3.20.0：搜索表头排序由服务端负责，这里只同步箭头状态。
const SEARCH_SORT_COLS = { name:'sr-name', path:'sr-path', ext:'sr-ext', size:'sr-size', mtime:'sr-time', volume:'sr-vol' };
function searchSyncSortArrows(){
  const t = document.querySelector('table[data-sortkey="search"]');
  if (!t) return;
  const st = searchState();
  const ts = tableSortState('search');
  ts.col = SEARCH_SORT_COLS[st.sort] || '';
  ts.dir = st.order === 'desc' ? -1 : 1;
  syncTableSortArrows(t, ts);
}

function searchRowHtml(it, idx, st){
  const bad = badName(it);
  const checked = !bad && (st.selAll ? !st.excludes.has(it.path) : st.sel.has(it.path));
  return `<tr data-ridx="${idx}" class="${checked ? 'rowsel' : ''}">
    <td class="sel"><input type="checkbox" data-act="search-check" data-path="${esc(it.path)}"${checked ? ' checked' : ''}${bad ? ' disabled title="文件名编码异常，暂不支持操作"' : ''}></td>
    <td class="fname">${esc(it.name)}</td>
    <td class="fpath" title="${esc(it.path)}">${esc(it.path)}</td>
    <td class="hint">${esc(it.ext || '')}</td>
    <td class="num">${fmtB(it.size)}</td>
    <td class="num">${fmtT(it.mtime)}</td>
    <td class="fpath" title="${esc(it.volume || '')}">${esc(it.volume || '')}</td>
  </tr>`;
}

// renderSearchWindow 只重建可视区附近的行（v3.20.0）。
// 命中几万条时全部塞进 DOM 会卡死；这里用上下占位行撑出滚动高度。
function renderSearchWindow(){
  const st = searchState();
  const sc = $('sScroll'), body = $('sBody');
  if (!sc || !body) return;
  const rowH = st.rowH || 36;
  const win = virtualWindow(st.items.length, rowH, sc.scrollTop, sc.clientHeight || 480, SEARCH_ROW_BUFFER);
  const span = searchResultColumns().length;
  let html = virtualSpacerRow(span, win.start * rowH);
  for (let i = win.start; i < win.end; i++) html += searchRowHtml(st.items[i], i, st);
  html += virtualSpacerRow(span, (st.items.length - win.end) * rowH);
  body.innerHTML = html;
  const hint = $('sLoadHint');
  if (hint) {
    if (st.loadingMore) hint.textContent = '正在加载更多…';
    else if (st.nextCursor) hint.textContent = '滚动到底自动加载更多 · 已加载 ' + st.items.length.toLocaleString() + ' / ' + st.total.toLocaleString() + ' 条';
    else hint.textContent = '已加载全部 ' + st.total.toLocaleString() + ' 条';
  }
}

// searchOnScroll 滚动时按帧重绘可视区；接近底部时按游标拉下一批。
let searchScrollRaf = 0;
function searchOnScroll(){
  if (searchScrollRaf) return;
  searchScrollRaf = requestAnimationFrame(() => {
    searchScrollRaf = 0;
    const st = searchState();
    const sc = $('sScroll');
    if (!sc) return;
    renderSearchWindow();
    if (!st.loadingMore && st.nextCursor && sc.scrollTop + sc.clientHeight >= sc.scrollHeight - (st.rowH || 36) * 8) {
      loadMoreSearch();
    }
  });
}

function renderSearchResults(){
  const box = $('sResults'); if (!box) return;
  const st = searchState();
  // 2026-11-08 unslop：原文案与顶部状态条「索引已就绪，输入关键词开始搜索」重复，改为说明能搜什么
  if (!st.q) { box.innerHTML = '<div class="empty" style="margin:20px 0"><strong>输入关键词开始搜索</strong>匹配文件名与完整路径；* 和 ? 作通配符，切到「正则」后按正则表达式匹配</div>'; return; }
  if (!st.total && !st.items.length) { box.innerHTML = '<div class="empty" style="margin:20px 0">没有找到匹配的文件</div>'; return; }
  const cols = searchResultColumns();
  const colHtml = '<colgroup>' + cols.map(c => { const w = colWidthFor(c.id, cols); return `<col data-col="${c.id}"${w ? ` style="width:${w}px"` : ''}>`; }).join('') + '</colgroup>';
  const headHtml = '<thead><tr>' + thCells(cols) + '</tr></thead>';
  box.innerHTML = `<div class="tscroll" id="sScroll"><table class="rtable searchv" data-sortkey="search">${colHtml}${headHtml}<tbody id="sBody"></tbody></table></div>
    <div class="asrow" style="justify-content:center;gap:8px;margin-top:8px"><span class="hint" id="sLoadHint"></span></div>`;
  const sc = $('sScroll');
  if (sc) sc.addEventListener('scroll', searchOnScroll, { passive: true });
  renderSearchWindow();
  // 行高以真实渲染为准：占位高度算错会让滚动条跳动，这里量一次再重绘。
  const tr = document.querySelector('#sBody tr[data-ridx]');
  if (tr) {
    const h = tr.offsetHeight;
    if (h > 0 && Math.abs(h - st.rowH) > 1) { st.rowH = h; renderSearchWindow(); }
  }
  searchSyncSortArrows();
  // 首屏不足一屏时自动续拉，避免小窗口看不到滚动条也触发不了加载。
  if (sc && st.nextCursor && sc.scrollHeight <= sc.clientHeight + (st.rowH || 36) * 4) loadMoreSearch();
}

function updateSearchButtons(){
  const st = searchState();
  const b = document.querySelector('[data-act="search-add-rename"]');
  if (b) b.textContent = '加入重命名列表（' + searchSelCount() + '）';
  // v3.20.0：全选模式下选择数 = 总数 − 排除项，必须让用户一眼看到现在到底选了多少。
  const info = $('sSelInfo');
  if (info) {
    info.textContent = st.selAll
      ? ('已选全部 ' + searchSelCount().toLocaleString() + ' 条' + (st.excludes.size ? '（排除 ' + st.excludes.size + ' 条）' : ''))
      : (st.sel.size ? ('已选 ' + st.sel.size.toLocaleString() + ' 条') : '');
  }
  const all = $('sSelAll');
  if (all) all.checked = !!st.selAll;
}

function renderRenamePanel(){
  const box = $('renamePanel'); if (!box) return;
  const r = renameState();
  const totalN = renameTotalCount();
  const rows = r.list.map((it, i) => `<tr>
    <td class="fpath" title="${esc(it.path)}">${esc(it.path)}</td>
    <td class="fname">${esc(it.orig)}</td>
    <td><input data-act="rename-target" data-idx="${i}" value="${esc(it.target)}" style="width:180px"></td>
    <td class="hint">${esc(it.state || '待执行')}</td></tr>`).join('');
  const rcols = renamePanelColumns();
  const rcolsHtml = '<colgroup>' + rcols.map(c => { const w = colWidthFor(c.id, rcols); return `<col data-col="${c.id}"${w ? ` style="width:${w}px"` : ''}>`; }).join('') + '</colgroup>';
  const rheadHtml = '<thead><tr>' + thCells(rcols) + '</tr></thead>';
  // v3.21.0：全选条目只存凭据，这里给出总数与去处，不再把几十万行塞进表格。
  const tokenHtml = r.token
    ? `<div class="hint" style="margin:6px 0">已加入全选结果 <b>${r.token.count.toLocaleString()}</b> 条${r.token.excludes.length ? '（排除 ' + r.token.excludes.length + ' 条）' : ''}。请到「重命名」页配置方法链后执行，服务端会按 2000 条一片处理。</div>`
    : '';
  box.innerHTML = `<div class="card" style="margin-top:12px"><div class="set-title">重命名列表（${totalN.toLocaleString()}）</div>
    ${tokenHtml}
    <div class="rulergrid">
      <div class="field"><label for="rPrefix">前缀</label><input id="rPrefix"></div>
      <div class="field"><label for="rSuffix">后缀</label><input id="rSuffix"></div>
      <div class="field"><label for="rStart">序号起</label><input id="rStart" type="number" min="0" placeholder="不加"></div>
      <div class="field"><label for="rDigits">位数</label><input id="rDigits" type="number" min="1" max="8" value="2"></div>
      <div class="field"><label for="rFrom">查找</label><input id="rFrom" placeholder="要替换的文本"></div>
      <div class="field"><label for="rTo">替换为</label><input id="rTo" placeholder="留空即删除"></div>
      <div class="field"><label for="rCase">大小写</label>
        <select id="rCase"><option value="">不动</option><option value="upper">全大写</option><option value="lower">全小写</option><option value="title">首字母大写</option></select></div>
    </div>
    <div class="rulebar">
      <label class="checkline"><input type="checkbox" id="rTrim"> 去首尾空格</label>
      <span class="spacer"></span>
      <button class="btn" data-act="rename-apply-rules">按规则生成新名</button>
    </div>
    ${rows ? `<div class="tscroll"><table class="rtable" data-sortkey="rename-panel">${rcolsHtml}${rheadHtml}<tbody>${rows}</tbody></table></div>` : '<span class="hint">从搜索结果勾选后，点上方「加入重命名列表」</span>'}
    <div class="form-actions form-actions-start">
      <button class="btn primary" data-act="rename-execute"${r.list.length?'':' disabled'}>执行改名</button>
      ${r.token ? '<button class="btn" data-act="goto-rename">去重命名页配置方法</button>' : ''}
      <button class="btn ghost" data-act="rename-undo"${r.batchID?'':' disabled'}>撤销本批</button>
      <span class="hint" id="renameHint"></span>
    </div></div>`;
  applyTableSort('rename-panel');
}

// recentRenameHtml 最近改名批次（v3.22.0）：关页面后撤销入口仍在。
function recentRenameHtml(){
  const r = renameState();
  const list = (r.recent || []).slice(0, 5);
  if (!list.length) return '';
  const rows = list.map(b => `<div class="asrow" style="justify-content:space-between;align-items:center;padding:2px 0;gap:8px">
    <span class="mono">#${b.batch_id}</span>
    <span class="hint">${esc(renameBatchStatusText(b.status))} · ${Number(b.files_count || 0)} 条${b.failed ? ' · 失败 ' + b.failed : ''}</span>
    <span class="spacer"></span>
    <button class="btn ghost" data-act="rename-undo-one" data-id="${b.batch_id}">撤销</button>
  </div>`).join('');
  return `<div class="card" style="margin-bottom:var(--space-sm)"><div class="toolbar"><b>最近改名批次</b><span class="hint">关掉页面也能从这里撤销</span></div>${rows}</div>`;
}

// v3.5.0 独立重命名页：清单 + 方法链 + 预设 + 预览/执行/撤销。
// v3.10.0 重构布局：方法链与预设移到左侧（复用选择助手的位置），预览/执行/撤销升到顶部栏，
// 右侧只留清单。此前全部竖排，宽屏下方法链表单铺满一行很难扫读，预览按钮也离清单太远。
function renderRenamePage(){
  const v = $('view');
  const r = renameState();
  const rows = r.list.map((it, i) => `<tr>
    <td class="fpath" title="${esc(it.path)}">${esc(it.path)}</td>
    <td class="fname" title="${esc(it.orig)}">${esc(it.orig)}</td>
    <td><input data-act="rename-target" data-idx="${i}" value="${esc(it.target)}" style="width:100%;box-sizing:border-box"></td>
    <td class="hint">${esc(it.state || '待执行')}</td>
    <td><button class="btn ghost" data-act="rename-remove" data-idx="${i}">移除</button></td></tr>`).join('');
  const methodRows = (r.methods || []).map((m, i) => `<div class="asec" style="margin:0;padding:var(--space-2xs) 0">
    <div class="asrow" style="justify-content:space-between;align-items:center">
      <span class="mono">${i + 1}. ${esc(m.type)}</span>
      <span class="hint">${esc(m.apply_to || 'name')}</span>
      <button class="btn ghost" data-act="rename-method-del" data-idx="${i}">删</button>
    </div>
    <span class="hint" style="word-break:break-all">${esc(JSON.stringify(m))}</span>
  </div>`).join('');
  const vcols = renamePageColumns();
  const vcolsHtml = '<colgroup>' + vcols.map(c => { const w = colWidthFor(c.id, vcols); return `<col data-col="${c.id}"${w ? ` style="width:${w}px"` : ''}>`; }).join('') + '</colgroup>';
  // v3.10.0：路径列此前 noResize，导致「路径」和「原名」之间没有拖拽手柄；固定布局下路径列也能显式设宽。
  const vheadHtml = '<thead><tr>' + thCells(vcols) + '</tr></thead>';
  const presets = (r.presets || []).map(p => `<option value="${p.id}">${esc(p.name)}</option>`).join('');
  const totalN = renameTotalCount();
  const canRun = totalN > 0 && r.methods.length > 0;
  // v3.21.0：全选条目只显示总数，不展开成几十万行；执行时由服务端按片处理。
  const tokenCard = r.token ? `<div class="card" style="margin-bottom:var(--space-sm)">
    <div class="toolbar">
      <b>全选结果 ${r.token.count.toLocaleString()} 条</b>
      <span class="hint">${r.token.excludes.length ? '排除 ' + r.token.excludes.length + ' 条 · ' : ''}执行时服务端按 2000 条一片解析并改名</span>
      <div class="spacer"></div>
      <button class="btn ghost" data-act="rename-token-clear">移除全选条目</button>
    </div>
  </div>` : '';
  v.innerHTML = `
  <div class="group" style="margin-bottom:var(--space-sm)">
    <div class="ghead"><div class="gmeta">
      <b>重命名清单 · ${totalN.toLocaleString()} 个文件</b>
      <span class="hint">左侧配方法链，配好后先「预览新名」确认，再执行</span>
      <span class="hint" id="renameHint"></span>
    </div>
    <div class="gactions">
      <button class="btn" data-act="rename-preview-methods"${canRun ? '' : ' disabled'}>预览新名</button>
      <button class="btn primary" data-act="rename-execute-methods"${canRun ? '' : ' disabled'}>执行改名</button>
      <button class="btn ghost" data-act="rename-undo"${(r.batchID || (r.batchIDs || []).length) ? '' : ' disabled'}>撤销本次执行${(r.batchIDs || []).length > 1 ? '（' + r.batchIDs.length + ' 批）' : ''}</button>
    </div></div>
  </div>
  ${tokenCard}
  <div id="renameOpHost">${batchedOpCardHtml('rename')}</div>
  ${recentRenameHtml()}
  <div class="resultlayout" style="--assist-w:300px">
    <aside class="assist" id="renameSide">
      <div class="assist-head"><b>方法链</b><span class="hint">按顺序执行</span></div>
      <div class="asec">
        <label>方法</label>
        <select id="rmType">
          <option value="remove">Remove 删字符</option>
          <option value="remove_pattern">RemovePattern 删文本/正则</option>
          <option value="replace">Replace 替换</option>
          <option value="list_replace">ListReplace 多对替换</option>
          <option value="add">Add 插入文本</option>
          <option value="renumber">Renumber 序号</option>
          <option value="new_case">NewCase 大小写</option>
          <option value="trim">Trim 去首尾</option>
          <option value="swap">Swap 交换分段</option>
          <option value="list">List 按列表</option>
          <option value="timestamp">Timestamp 改时间戳</option>
        </select>
        <label>作用范围</label>
        <select id="rmApply"><option value="name">文件名</option><option value="extension">扩展名</option><option value="both">文件名+扩展名</option></select>
        <div class="chain c2">
          <div class="field"><label for="rmText">文本 / 模式</label><input type="text" id="rmText"></div>
          <div class="field"><label for="rmRepl">替换为</label><input type="text" id="rmRepl"></div>
        </div>
        <div class="chain c3">
          <div class="field"><label for="rmCount">N / 位数</label><input type="number" min="0" id="rmCount"></div>
          <div class="field"><label for="rmStart">起点</label><input type="number" min="0" id="rmStart"></div>
          <div class="field"><label for="rmStep">步长</label><input type="number" min="1" id="rmStep"></div>
        </div>
        <div class="chain c2">
          <div class="field"><label for="rmSep">分隔符</label><input type="text" id="rmSep"></div>
          <div class="field"><label for="rmPosition">插入位置</label><select id="rmPosition"><option value="tail">插到末尾</option><option value="head">插到开头</option><option value="match">插到匹配后</option></select></div>
        </div>
        <div class="asrow">
          <label class="checkline"><input type="checkbox" id="rmRegex"> 正则</label>
          <label class="checkline"><input type="checkbox" id="rmFromEnd"> 从末尾</label>
        </div>
        <button class="btn primary" data-act="rename-method-add">添加方法</button>
      </div>
      <div class="asec">
        <label>已添加的方法（${(r.methods || []).length}）</label>
        ${methodRows || '<span class="hint">还没有方法，先在上面配一条</span>'}
      </div>
      <div class="asec">
        <label>预设</label>
        <select id="rmPreset"><option value="0">（选择预设）</option>${presets}</select>
        <div class="asrow">
          <button class="btn ghost" data-act="rename-preset-load">加载</button>
          <button class="btn ghost" data-act="rename-preset-del">删除</button>
        </div>
        <label>另存为新预设</label>
        <input type="text" id="rmPresetName" placeholder="预设名称">
        <button class="btn ghost" data-act="rename-preset-save">保存为新预设</button>
      </div>
      <details class="asec"><summary class="hint">方法参数说明</summary>
        <span class="hint">remove=删 N 字符（勾「从末尾」从后数）；remove_pattern=删文本/正则；replace=替换（N=第几次，0=全部）；list_replace=文本填 a=b,c=d；add=插入文本；renumber=追加序号；new_case=文本填 upper/lower/title/sentence；trim=去首尾（文本填要删的字符）；swap=交换分段（N=段A，替换为=段B）；list=文本按逗号列表；timestamp=更新时间戳。</span>
      </details>
    </aside>
    <div class="assist-tools">
      <div class="card" style="flex:none;margin-bottom:var(--space-xs)">
        <div class="inputrow">
          <input type="text" id="renameAddPath" placeholder="/vol6/1000/…/文件.mp4">
          <button class="btn" data-act="rename-add-file">添加文件</button>
        </div>
        <div class="hint" style="margin-top:var(--space-2xs)">也可以到「文件搜索」勾选后加入清单</div>
      </div>
      <div class="virtual-list">
        ${rows ? `<div class="tscroll"><table class="rtable" data-sortkey="rename">${vcolsHtml}${vheadHtml}<tbody>${rows}</tbody></table></div>` : '<div class="card hint">清单为空，先在上面填路径添加文件</div>'}
      </div>
    </div>
  </div>`;
  applyTableSort('rename');
  // v3.22.0：进入重命名页时拉一次最近批次，刷新或重开页面后仍能看到撤销入口。
  if (!r.recentLoaded && !r.recentLoading) { r.recentLoaded = true; loadRecentRenameBatches(); }
}
