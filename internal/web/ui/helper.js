// 查重助手 verify Web UI · 基础工具与共享状态（v3.0.0 从 app.js 拆分）
// 加载顺序第 1：格式化/路径/筛选偏好等纯函数、全局状态 S、请求封装与状态刷新。
// 不依赖后续文件；所有函数仍是全局声明，其他脚本可直接调用。

const $ = (id) => document.getElementById(id);

const esc = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

// v3.10.0：把 textarea 里「每行一个路径」的内容读成数组，供目录选择器预勾选
function pathLinesOf(id){
  const el = document.getElementById(id);
  if (!el) return [];
  return (el.value || '').split('\n').map(s => s.trim()).filter(Boolean);
}

// v3.10.0：列宽可以拖到比内容更窄，被省略号截断的单元格在鼠标悬浮时自动补 title。
// 逐处渲染手写 title 容易漏，这里统一兜底；带按钮的单元格跳过，免得提示里混进操作文案。
document.addEventListener('mouseover', ev => {
  const cell = ev.target.closest('td, th');
  if (!cell || cell.title || !cell.closest('table.rtable')) return;
  if (cell.querySelector('button, input, select, a')) return;
  // 名称/路径列实际被裁的是 td 里的 .fname/.fpath 省略号节点，td 本身量不到溢出，
  // 所以两者都要查，任一出现裁切就补 title。
  const nodes = [cell, ...cell.querySelectorAll('.fname, .fpath, .simpname')];
  if (!nodes.some(n => n.scrollWidth > n.clientWidth + 1)) return;
  cell.title = (cell.textContent || '').trim();
});

const fmtB = (n) => {
  n = Number(n || 0);
  if (n < 1024) return n + ' B';
  const u = ['KB','MB','GB','TB','PB'];
  let i = -1, v = n;
  do { v /= 1024; i++; } while (v >= 1024 && i < u.length - 1);
  return v.toFixed(v >= 100 ? 0 : 1) + ' ' + u[i];
};

const fmtT = (ns) => {
  if (!ns) return '—';
  const d = new Date(Number(ns) / 1e6);
  const p = x => String(x).padStart(2, '0');
  return d.getFullYear() + '-' + p(d.getMonth()+1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
};

// v3.13.0（UI 审查 P3-4）：结果页「已到前端内存上限」的判断原先在 view/api 多处各写一遍，
// 口径一改就容易漂移，这里收成唯一入口。
function isCapped(){
  return (S.groups || []).length >= MAX_LOADED_PAGES * (S.pageSize || 100);
}

const fmtDur = (sec) => {
  if (!sec) return '';
  const s = Math.round(sec);
  return Math.floor(s/60) + ':' + String(s%60).padStart(2,'0');
};

const nameExt = (p) => (p.split('.').pop() || '').toLowerCase();

function isResultTab(){ return RESULT_TABS.includes(S.tab); }

function filtersActive(){
  return listFilterActive();
}

function normPath(p){ return String(p || '').replace(/[\\\\]+/g, '/'); }

// fsPathError v3.7.2：粘贴路径的格式校验，返回空串表示格式通过。
// 只查「写法」不管权限——是否真的被飞牛授权由 /api/fs/list 的服务端探测决定；
// 前端先挡掉明显无效的输入（相对路径、.. 逃逸、反斜杠），避免把垃圾值写进配置（P-20）。
function fsPathError(p){
  const raw = String(p || '').trim();
  if (!raw) return '请先粘贴路径';
  if (raw.includes('\\')) return '路径请用 / 分隔，不要用反斜杠';
  if (!raw.startsWith('/')) return '请填写以 / 开头的绝对路径，如 /vol1/1000/电影';
  const segs = raw.split('/');
  if (segs.some(s => s === '..')) return '路径不能包含 ..';
  if (segs.some(s => s === '.')) return '路径不能包含 .';
  return '';
}

// isProtectedPath 依据设置页的保护目录判断结果行是否要加保护标记。
function isProtectedPath(p){
  const list = (S.status && S.status.config && S.status.config.protect_paths) || [];
  const n = normPath(p);
  return (list || []).some(r => { r = normPath(r); return r && (n === r || n.startsWith(r + '/')); });
}

function pathDepth(p){ return normPath(p).split('/').filter(Boolean).length; }

// v3.10.13：非 UTF-8 文件名（口径 B：入库但暂不支持操作）。
// 后端定稿只发 name_encoding="invalid"，合法行不带该字段。
const BAD_NAME_TIP = '文件名编码异常，暂不支持操作';
function badName(f){
  return !!(f && f.name_encoding === 'invalid');
}

// v3.11.1：可操作文件的统一过滤。编码异常行可以展示、参与统计，但不能进选择集——
// 写接口只拦坏路径，混进一批里会让整个提交被拒。所有写 S.sel 的入口都必须走这里，
// 不要在各自循环里再散着判断 badName。
function operableFiles(g){
  return ((g && g.files) || []).filter(f => !badName(f));
}

function cloneSel(){
  const out = {};
  Object.keys(S.sel).forEach(k => { out[k] = new Set(S.sel[k]); });
  return out;
}

function snapshotSel(label){
  S.undo.push({ sel: cloneSel(), label: label, at: Date.now() });
  if (S.undo.length > 20) S.undo.shift();
}

function markedPaths(){
  const out = [];
  Object.values(S.sel).forEach(s => s.forEach(p => out.push(p)));
  return out;
}

function selectedGroup(g){
  const files = g.files || [];
  return files.some(f => S.rowSel.has(f.path));
}

function unmarkedGroup(g){ const s = S.sel[g.key]; return !(s && s.size); }

// ---------- 筛选器面板（v1.14.2 §4.3，图 1 筛选器落地）----------
// v1.25.0：筛选条件统一存于 S.listFilter（原顶栏 S.filters 已并入），列表与助手操作同源收窄。
// 显示与操作同源：列表渲染与选择助手操作都经 visibleGroups() 收窄。
function groupProtected(g){
  return (g.files || []).some(f => isProtectedPath(f.path));
}

function groupMatchState(g){
  const files = g.files || [];
  const sel = S.sel[g.key] || new Set();
  const n = sel.size;
  const st = [];
  if (S.listFilter.marked && n > 0) st.push(true);
  if (S.listFilter.unmarked && n === 0) st.push(true);
  if (S.listFilter.partial && n > 0 && n < files.length) st.push(true);
  if (S.listFilter.allMarked && files.length > 0 && n === files.length) st.push(true);
  if (S.listFilter.protected && groupProtected(g)) st.push(true);
  return st.length > 0; // 状态类内 OR；未勾选任何状态则不筛
}

function numInRange(v, min, max){
  const n = Number(v);
  if (v === '' || v === null || v === undefined || isNaN(n)) return true;
  if (min !== null && n < min) return false;
  if (max !== null && n > max) return false;
  return true;
}

function visibleGroups(){
  return applyFilters(applyScope(S.groups || []));
}

function allMarkedInfo(groups){
  const out = [];
  (groups || []).forEach((g, idx) => {
    const files = g.files || [];
    const sel = S.sel[g.key];
    if (files.length && sel && files.every(f => sel.has(f.path))) {
      out.push({ key: g.key, index: idx, name: g.name || g.key });
    }
  });
  return out;
}

function fileVal(f, key){
  switch(key){
    case 'resolution': return Number(f.width||0) * Number(f.height||0);
    case 'duration': return Number(f.duration||0);
    case 'size': return Number(f.size||0);
    case 'mtime': return Number(f.mtime||0);
    case 'ctime': return Number(f.ctime||0);
    case 'bitrate': return Number(f.bit_rate||0);
    case 'samplerate': return Number(f.sample_rate||0);
    case 'depth': return pathDepth(f.path);
    case 'name': return String(f.name||'').length;
    default: return 0;
  }
}

function compareFiles(a, b, prefs){
  for (const p of (prefs || [])) {
    if (!p.on) continue;
    const va = fileVal(a, p.key), vb = fileVal(b, p.key);
    if (va === vb) continue;
    if (p.dir === 'max') return va > vb ? 1 : -1;
    return va < vb ? 1 : -1;
  }
  return 0;
}

function chooseKeep(files, prefs){
  const enabled = (prefs || []).filter(p => p.on);
  if (!enabled.length || !files.length) return null;
  let best = files[0], allTie = true;
  for (let i = 1; i < files.length; i++) {
    const c = compareFiles(files[i], best, enabled);
    if (c !== 0) allTie = false;
    if (c > 0) best = files[i];
  }
  return allTie ? null : best;
}

function chooseWorst(files, prefs){
  const enabled = (prefs || []).filter(p => p.on);
  if (!files.length) return null;
  if (!enabled.length) return files[0];
  let worst = files[0];
  for (let i = 1; i < files.length; i++) {
    if (compareFiles(files[i], worst, enabled) < 0) worst = files[i];
  }
  return worst;
}

function criterionPrefs(crit){
  if (crit === 'quality') return [
    {key:'resolution', dir:'max', on:true},
    {key:'size', dir:'max', on:true},
    {key:'duration', dir:'max', on:true},
    {key:'mtime', dir:'max', on:true}
  ];
  const m = String(crit || '').split('_');
  const dir = m.pop();
  const key = m.join('_');
  return [{key: key, dir: dir, on:true}];
}

function prefName(key){
  return {resolution:'分辨率', size:'文件大小', duration:'时长', mtime:'修改时间', ctime:'创建时间', bitrate:'码率', samplerate:'采样率', depth:'路径深度', name:'文件名长度'}[key] || key;
}

function prefDirOptions(key, dir){
  const labels = {
    resolution:{max:'最高',min:'最低'}, size:{max:'最大',min:'最小'},
    duration:{max:'最长',min:'最短'}, mtime:{max:'最新',min:'最旧'},
    ctime:{max:'最新',min:'最旧'}, bitrate:{max:'最高',min:'最低'}, samplerate:{max:'最高',min:'最低'},
    depth:{min:'最浅',max:'最深'}, name:{max:'最长',min:'最短'}
  }[key] || {max:'优先最大',min:'优先最小'};
  return ['max','min'].map(d => `<option value="${d}" ${d===dir?'selected':''}>${labels[d]}</option>`).join('');
}

function thumbSrc(f){
  const ext = nameExt(f.path);
  if (S.tab === 'video' || S.tab === 'intro' || VID_EXT.includes(ext)) {
    return '/api/thumb?path=' + encodeURIComponent(f.path) + '&t=1';
  }
  if (IMG_EXT.includes(ext)) return '/api/raw?path=' + encodeURIComponent(f.path);
  return '';
}

function toast(msg, type, duration){
  const t = document.createElement('div');
  t.className = 'toast ' + (type || '');
  t.textContent = msg;
  $('toasts').appendChild(t);
  setTimeout(() => { t.style.opacity = '0'; t.style.transition = 'opacity .3s'; setTimeout(() => t.remove(), 320); }, duration || 3200);
}

// ---- v3.10.5：写操作守卫（置灰）+ 操作后对账展示 ----

// 扫描 / 健康检测进行中不能执行的写操作。后端对这些接口会回 409，
// 前端提前置灰并写出原因，用户不用点一次才知道为什么失败。
const OPS_GUARD_ACTS = new Set([
  'trash-selected', 'trash-group', 'delete-one', 'delete-sel', 'empty-trash',
  'rename-execute', 'rename-execute-methods', 'rename-undo', 'group-rename-confirm',
  'group-rename-undo',
  'fanjian-menu', 'fanjian-run', 'fanjian-undo',
  'health-rename-one', 'health-rename-sel', 'health-trash-one', 'health-trash-sel',
  'health-to-simp', 'health-undo',
  'photo-trash-sel', 'big-trash-sel',
  'import-trash', 'import-trash-marked',
  // v3.10.8（P2-3）：补上同样会动文件的写动作，扫描/健康检测期间一并置灰。
  'move-confirm', 'move-undo', 'relink-confirm', 'relink-selected',
  'fr-apply', 'fr-undo', 'trim-confirm', 'cleanup-run',
  // v3.16.0：结果页补回的入口同样要置灰——「移动到…」打开的对话框最终会改文件，
  // 「重新分析片头」会清空并重算全部片头指纹，扫描/健康检测期间都不该执行。
  'move-selected', 'reanalyze-intro',
  // v3.17.1：视频全量重算同样会清空并重算指纹，扫描/检测期间不该执行。
  'reanalyze-all'
]);

function opsBlockedReason(){
  if (S.status && S.status.state && S.status.state.running) {
    return '正在扫描，写操作已暂停，扫描结束后自动恢复';
  }
  // v3.10.8（P2-3）：health_running 由 /api/status 下发，任何页面都能判断；
  // healthRunning() 仅在前端访问过健康页后才有状态，作为补充。
  if ((S.status && S.status.health_running) || (typeof healthRunning === 'function' && healthRunning())) {
    return '健康检测进行中，写操作已暂停，检测结束后自动恢复';
  }
  return '';
}

// applyOpsGuard 把守卫状态同步到当前 DOM。
// 记下按钮原本的禁用状态，解除后原样还原，避免把「本来就不能点」的按钮误点亮。
function applyOpsGuard(){
  const reason = opsBlockedReason();
  document.querySelectorAll('button[data-act]').forEach(btn => {
    if (!OPS_GUARD_ACTS.has(btn.dataset.act)) return;
    if (reason) {
      if (btn.dataset.opsGuard !== '1') {
        btn.dataset.opsGuard = '1';
        btn.dataset.opsGuardWas = btn.disabled ? '1' : '0';
        btn.dataset.opsGuardTitle = btn.getAttribute('title') || '';
      }
      btn.disabled = true;
      btn.title = reason;
      btn.classList.add('ops-blocked');
    } else if (btn.dataset.opsGuard === '1') {
      btn.disabled = btn.dataset.opsGuardWas === '1';
      // 置灰期间用户可能新勾了文件：这类按钮的可用性由勾选数决定，勾上了就放开。
      if (btn.disabled && btn.hasAttribute('data-sel-needs')) {
        const h = typeof healthState === 'function' ? healthState() : null;
        if (h && h.sel && h.sel.size) btn.disabled = false;
      }
      btn.title = btn.dataset.opsGuardTitle || '';
      btn.classList.remove('ops-blocked');
      delete btn.dataset.opsGuard;
      delete btn.dataset.opsGuardWas;
      delete btn.dataset.opsGuardTitle;
    }
  });
}

// v3.10.5 真机验证修复：结果页、隔离区页重绘时会整块替换工具栏按钮，
// 只在 refreshStatus / setModal 后套守卫会漏掉新生成的节点——实测扫描中
// 「移入隔离区」仍可点。这里观察新增节点兜底重套一次。
// 只监听 childList：若监听 disabled 属性，applyOpsGuard 自身的写入会再次触发观察器。
// v3.10.8：结果页虚拟滚动会持续增删行，先按「新增节点里有没有按钮」过滤，
// 避免每次滚动都全量扫一遍 DOM；没有新按钮时本就不需要重套守卫。
const OPS_GUARD_SELECTOR = 'button[data-act]';
function opsGuardRelevant(records){
  return records.some(r => Array.from(r.addedNodes).some(n =>
    n.nodeType === 1 && (n.matches(OPS_GUARD_SELECTOR) || !!n.querySelector(OPS_GUARD_SELECTOR))));
}
let opsGuardTimer = 0;
const opsGuardObserver = new MutationObserver(records => {
  if (opsGuardTimer || !opsGuardRelevant(records)) return;
  opsGuardTimer = setTimeout(() => { opsGuardTimer = 0; applyOpsGuard(); }, 150);
});
if (document.body) opsGuardObserver.observe(document.body, { childList: true, subtree: true });

// 对账清单在界面上最多列这么多条，完整清单引导用户去「操作记录」看。
const RECONCILE_LIST_LIMIT = 50;

// mergeReconcile 把多批操作的对账结果合成一份（健康页按 500 个拆批提交，每批一份报告）。
function mergeReconcile(acc, rc){
  if (!acc) acc = { checked: 0, mismatched: 0, items: [], truncated: false, advice: '' };
  if (!rc) return acc;
  acc.checked += rc.checked || 0;
  acc.mismatched += rc.mismatched || 0;
  if (rc.truncated) acc.truncated = true;
  (rc.items || []).forEach(it => {
    if (acc.items.length < RECONCILE_LIST_LIMIT) acc.items.push(it);
    else acc.truncated = true;
  });
  // 同一类差异的建议一样，重复追加会让提示很长。
  if (rc.advice && acc.advice.indexOf(rc.advice) < 0) {
    acc.advice = acc.advice ? acc.advice + '；' + rc.advice : rc.advice;
  }
  return acc;
}

// reconcileHTML 渲染「差异清单 + 下一步建议」；没有差异时返回空串。
function reconcileHTML(rc){
  if (!rc || !rc.mismatched) return '';
  const items = (rc.items || []).slice(0, RECONCILE_LIST_LIMIT);
  const rows = items.map(it => `<tr>
      <td class="fpath" title="${esc(it.path || '')}">${esc(it.path || '')}</td>
      <td>${esc(it.expected || '')}</td>
      <td>${esc(it.actual || '')}</td>
      <td class="hint">${esc(it.problem || '')}</td>
    </tr>`).join('');
  const more = (rc.truncated || (rc.items || []).length > items.length)
    ? `<div class="hint" style="padding:6px 0">仅显示前 ${items.length} 条，共 ${rc.mismatched} 项与预期不符；完整清单见「操作记录」。</div>`
    : '';
  const advice = rc.advice ? `<div class="hint" style="margin:8px 0;padding:8px 10px;border-left:3px solid var(--color-warn);background:var(--color-warn-wash)">下一步：${esc(rc.advice)}</div>` : '';
  return `<div class="hint" style="margin:8px 0">已按操作前快照核对 <b>${rc.checked || 0}</b> 项，其中 <b>${rc.mismatched}</b> 项与预期不符。</div>
    ${advice}
    <div class="preview-list" style="max-height:300px;overflow:auto"><table>
      <thead><tr><th>文件</th><th>预期</th><th>实际</th><th>说明</th></tr></thead><tbody>${rows}</tbody></table></div>
    ${more}`;
}

// showReconcile 在操作完成页之后弹出对账差异；没有差异时不打扰用户。
function showReconcile(rc){
  if (!rc || !rc.mismatched) return false;
  setModal('操作后对账：' + rc.mismatched + ' 项与预期不符',
    reconcileHTML(rc) + '<div class="form-actions"><button class="btn primary" data-act="modal-cancel">知道了</button></div>', null);
  return true;
}

// ---------- 批量操作进度组件（v3.19.0 从健康页抽出） ----------
// 背景：健康页 v3.10.2 就把大批量操作拆成多批提交，v3.15.0 加了暂停 / 取消；
// 大文件页的批量隔离与竖横屏识别需要同一套进度、暂停、取消与失败明细导出，
// 这里抽成公共实现，各页面只提供「每批怎么调接口」和「卡片挂在哪」。
// 约定：暂停与取消只在两批之间生效，正在提交的一批会正常跑完；已完成批次各自落库，不回滚。
const BATCHED_OPS = {};

// runBatchedOp 执行一次分批操作。
// cfg：
//   id          进度卡标识，同一 id 同时只允许一个操作（重新发起会覆盖旧记录）
//   label       显示名称
//   items       显式条目数组；不传时按 maxBatches 轮询（每批处理量由后端决定）
//   batchSize   每批条数，默认 500
//   maxBatches  items 缺失时的最大批数
//   call(chunk, op, round) 必填，返回 {done, failed, items, processed, stop}
//   op          额外并入运行态的字段（如对账累计器）
//   onProgress(op) 每批结束（含开始时）刷新进度卡
//   onDone(op)     结束（正常 / 取消 / 异常）后的收尾，异常信息在 op.error
async function runBatchedOp(cfg){
  const size = cfg.batchSize || 500;
  const op = Object.assign({
    total: (cfg.items || []).length, processed: 0, done: 0, failed: 0,
    items: [], running: true, cancelled: false, startedAt: Date.now(),
    paused: false, cancelRequested: false, resumeWait: null
  }, cfg.op || {});
  op.id = cfg.id || '';
  op.label = cfg.label || '';
  BATCHED_OPS[op.id] = op;
  const progress = () => { if (cfg.onProgress) cfg.onProgress(op); };
  progress();
  const rounds = cfg.items ? Math.ceil(cfg.items.length / size) : (cfg.maxBatches || 1);
  // waitIfPaused 在暂停时挂起，继续或取消时被唤醒。
  const waitIfPaused = async () => {
    while (op.paused && !op.cancelRequested) {
      await new Promise(resolve => { op.resumeWait = resolve; });
      op.resumeWait = null;
    }
  };
  try {
    for (let i = 0; i < rounds; i++) {
      if (op.cancelRequested) { op.cancelled = true; break; }
      await waitIfPaused();
      if (op.cancelRequested) { op.cancelled = true; break; }
      const chunk = cfg.items ? cfg.items.slice(i * size, i * size + size) : null;
      const r = await cfg.call(chunk, op, i) || {};
      if (chunk) op.processed = Math.min(op.total, op.processed + chunk.length);
      else op.processed += r.processed || 0;
      op.done += r.done || 0;
      op.failed += r.failed || 0;
      if (r.items && r.items.length) op.items.push(...r.items);
      progress();
      if (r.stop) break;
    }
  } catch(e) {
    op.cancelled = true;
    op.error = e.message || String(e);
    toast('批量操作中断：' + op.error, 'err');
  } finally {
    op.running = false;
    op.paused = false;
    op.elapsed = Math.max(1, Math.round((Date.now() - op.startedAt) / 1000));
    if (cfg.onDone) await cfg.onDone(op);
  }
  return op;
}

// batchedOpTogglePause 暂停或继续指定批量操作（v3.19.0）。
function batchedOpTogglePause(id){
  const op = BATCHED_OPS[id];
  if (!op || !op.running) return false;
  op.paused = !op.paused;
  if (!op.paused && op.resumeWait) {
    const resume = op.resumeWait;
    op.resumeWait = null;
    resume();
  }
  return true;
}

// batchedOpCancel 取消指定批量操作尚未执行的批次（v3.19.0）。
function batchedOpCancel(id){
  const op = BATCHED_OPS[id];
  if (!op || !op.running) return false;
  op.cancelRequested = true;
  op.paused = false;
  if (op.resumeWait) {
    const resume = op.resumeWait;
    op.resumeWait = null;
    resume();
  }
  return true;
}

// batchedOpCardHtml 渲染进度卡；卡片 id 由操作 id 派生，刷新时整卡替换即可。
function batchedOpCardHtml(id){
  const op = BATCHED_OPS[id];
  if (!op) return '';
  const pct = op.total > 0 ? Math.min(100, Math.round(op.processed * 100 / op.total)) : 0;
  const state = op.running ? (op.paused ? '已暂停' : '执行中') : (op.cancelled ? '已中止' : '已完成');
  const bar = op.running && op.total > 0
    ? `<div class="pbar" style="width:100%;margin:0 12px 10px"><i style="--p:${(pct / 100).toFixed(3)}"></i></div>` : '';
  const ctrl = op.running
    ? `<button class="btn ghost" data-act="op-pause" data-op="${esc(id)}">${op.paused ? '继续' : '暂停'}</button>
       <button class="btn danger" data-act="op-cancel" data-op="${esc(id)}">取消</button>`
    : '';
  const tip = op.running
    ? '<div class="hint" style="padding:0 12px 10px">执行期间请不要刷新或关闭网页；暂停与取消在下一批开始前生效，已完成批次保留</div>'
    : '';
  return `<div class="card" id="opCard-${esc(id)}" style="margin-bottom:12px">
    <div class="toolbar">
      <span>${esc(op.label)} · ${state} · ${op.processed}/${op.total || '?'}</span>
      <div class="spacer"></div>
      <span class="hint">成功 ${op.done} · 失败 ${op.failed}</span>
      ${ctrl}
    </div>
    ${bar}
    ${tip}
  </div>`;
}

// refreshBatchedOpCard 只替换进度卡，不重绘整页；卡片还没挂上时返回 false。
function refreshBatchedOpCard(id){
  const card = $('opCard-' + id);
  if (!card) return false;
  card.outerHTML = batchedOpCardHtml(id);
  return true;
}

// refreshOpCardFor 刷新指定操作的卡片；页面还没渲染出卡片时按页面整体重绘兜底。
function refreshOpCardFor(id){
  if (refreshBatchedOpCard(id)) return;
  if (id === 'health') renderHealth();
  else if (id === 'rename') renderRenamePage();
  else if (id === 'big-trash' || id === 'big-crop') renderBig();
}

// showBatchedOpDone 批量操作完成页：汇总数字 + 可选补充说明 + 失败明细导出入口。
// 失败项只显示数量，逐条路径与原因导出 CSV 核对，避免几百条把弹窗撑成一张长表。
function showBatchedOpDone(op, extraHtml, exportName){
  const failed = (op.items || []).filter(it => it.error || it.ok === false);
  // v4.1.2：取消时 processed 只算已跑的批次，total-done-failed 是「未处理」条目。
  // 旧实现明明中止了却还输出「全部成功，没有失败项」，用户以为做完了不会补跑。
  const unprocessed = Math.max(0, (op.total || 0) - (op.done || 0) - (op.failed || 0));
  const failHtml = failed.length || (op.cancelled && unprocessed > 0)
    ? `${failed.length ? `<div class="hint" style="margin:6px 0">失败 ${failed.length} 个。逐条路径与原因可导出 CSV 另行核对。</div>
        <div class="form-actions" style="justify-content:flex-start">
          <button class="btn ghost" data-act="op-export-failures" data-op="${esc(op.id || '')}" data-name="${esc(exportName || '失败明细')}">导出失败明细（CSV）</button>
        </div>` : ''}
       ${op.cancelled && unprocessed > 0 ? `<div class="hint" style="margin:6px 0">另有 <b>${unprocessed}</b> 个未处理（ cancelled 时剩余批次未执行），这些文件保持原状，需要时请重新操作。</div>` : ''}`
    : '<div class="okbox">全部成功，没有失败项。</div>';
  const title = (op.label || '批量操作') + (op.cancelled ? '已中止' : '完成');
  const cancelHint = op.cancelled
    ? `<div class="hint" style="margin:6px 0">操作在中间中断${op.error ? '：' + esc(op.error) : ''}，已完成的部分保持生效；继续之前建议先重新检测，避免对旧路径重复处理。</div>`
    : '';
  setModal(title,
    `<div class="hint" style="margin-bottom:8px">
       共 ${op.total} 个 · 成功 <b>${op.done}</b> 个 · 失败 <b>${op.failed}</b> 个${op.elapsed ? ' · 用时 ' + op.elapsed + ' 秒' : ''}
     </div>
     ${cancelHint}
     ${extraHtml || ''}
     ${failHtml}
     <div class="form-actions"><button class="btn primary" data-act="modal-cancel">完成</button></div>`, null);
}

// batchedOpExportFailures 把指定批量操作的失败明细导出为 CSV（v3.15.0 首版，v3.19.0 通用化）。
// 只包含已经跑过的批次；取消后未执行的批次本来就没有失败项。
function batchedOpExportFailures(id, prefix){
  const op = BATCHED_OPS[id];
  if (!op) { toast('没有可导出的操作', 'warn'); return; }
  const failed = (op.items || []).filter(it => it.error || it.ok === false);
  if (!failed.length) { toast('本次没有失败项', 'warn'); return; }
  const lines = ['路径,原因'];
  failed.forEach(it => lines.push(csvCell(it.path || '') + ',' + csvCell(it.error || it.reason || '')));
  const blob = new Blob(['\ufeff' + lines.join('\r\n')], {type:'text/csv;charset=utf-8'});
  const a = document.createElement('a');
  const d = new Date();
  const pad = n => String(n).padStart(2, '0');
  a.download = (prefix || '失败明细') + '-' + d.getFullYear() + pad(d.getMonth() + 1) + pad(d.getDate()) +
    '-' + pad(d.getHours()) + pad(d.getMinutes()) + '.csv';
  a.href = URL.createObjectURL(blob);
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  // 立即 revoke 在部分浏览器里会打断下载，延迟释放。
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  toast('已导出 ' + failed.length + ' 条失败明细', 'ok');
}

// 局域网多通过 http 访问，navigator.clipboard 在非安全上下文不可用，这里保留 execCommand 回退。
async function copyText(text, okMsg){
  const val = String(text == null ? '' : text);
  if (!val) { toast('没有可复制的内容', 'err'); return; }
  try {
    if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(val);
    else {
      const ta = document.createElement('textarea');
      ta.value = val;
      ta.setAttribute('readonly', '');
      ta.style.cssText = 'position:fixed;left:-9999px;top:0;opacity:0';
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand('copy');
      ta.remove();
      if (!ok) throw new Error('浏览器拒绝复制');
    }
    toast(okMsg || '已复制', 'ok');
  } catch(e) {
    toast('复制失败：' + e.message, 'err');
  }
}

/**
 * 统一请求封装。
 *
 * v3.27 类型层：加泛型 <T>，调用点可标注期望的响应类型，
 * 让「后端改了字段名 → 前端还在用旧字段」在 tsc --noEmit 期就被发现，
 * 而不是等运行时被 `|| []` 静默兜底成空列表。
 *
 * 标注只在编译期生效，运行时代码与产物完全不变（零构建约束不变）。
 *
 * @template {any} [T=any] 期望的响应结构；不标注时按 any 处理（保持既有调用点行为不变）
 * @param {string} url
 * @param {RequestInit} [opts]
 * @returns {Promise<T|Response>}
 */
async function api(url, opts){
  // v3.10.7：所有请求在这里统一补自定义头。v3.10.6 只在 postJSON 里加，
  // 导出 CSV / HTML 报告 / 操作记录导出 / CSV 导入 / 各类删除都直接调 api() 或裸 fetch，
  // 在「已设令牌 + Cookie 登录」状态下会被后端的 CSRF 校验一路 403。
  // v3.10.8：GET 也带上——敏感读接口（/api/auth/token）要求它，跨站的图片/导航请求设不了头。
  const o = Object.assign({}, opts);
  const h = new Headers(o.headers || {});
  if (!h.has('X-Requested-With')) h.set('X-Requested-With', 'verify');
  o.headers = h;
  const r = await fetch(url, o);
  if (r.status === 401) { showLogin(); throw new Error('需要访问令牌'); }
  const ct = r.headers.get('content-type') || '';
  if (!r.ok) {
    let msg = '请求失败（' + r.status + '）';
    try { const j = await r.json(); if (j.error) msg = j.error; } catch(e) {}
    // v3.10.13：把状态码挂到 Error 上，调用方需要按 404 / 409 区别降级
    // （如 facets 无快照时返回 409，要显示「暂不可用」而不是「没有目录」）
    const err = new Error(msg);
    err.status = r.status;
    throw err;
  }
  if (!ct.includes('application/json')) return r;
  return /** @type {Promise<T>} */ (await r.json());
}

function postJSON(url, obj){
  // v3.10.6 SEC-1：写请求统一带自定义头。跨站表单带 Cookie 也设不了它（简单请求不进预检），
  // 后端据此拒绝同站跨源（同 NAS 其他端口）的 CSRF 写操作。
  return api(url, { method:'POST', headers:{'Content-Type':'application/json', 'X-Requested-With':'verify'}, body: JSON.stringify(obj || {}) });
}

function showLogin(){ $('login').classList.add('show'); }

function hideLogin(){ $('login').classList.remove('show'); }

// v3.18.0：顶部状态区显示当前占用独占槽的任务（扫描 / 转码 / 健康检测等）。
// 数据来自 /api/status 的 exclusive_task；没有任务时隐藏徽标，不额外轮询。
function renderExclusiveTask(st){
  const el = $('exclusiveTask');
  if (!el) return;
  const t = st && st.exclusive_task;
  if (!t) { el.style.display = 'none'; el.textContent = ''; return; }
  el.style.display = '';
  el.textContent = '正在运行：' + t.kind + '（' + (t.group_text || '占用系统资源') + '）';
  el.title = '同一时间只允许一个占用磁盘或解码资源的长任务；结束前其它同类任务会被拒绝。';
}

async function refreshStatus(){
  const st = /** @type {StatusResp} */ (await api('/api/status'));
  S.status = st;
  if (S.introN == null && st.config) S.introN = Number(st.config.intro_seconds || 10);
  $('ver').textContent = 'v' + (st.version || '1.0.0');
  renderProgress(st.state);
  // v3.18.0：顶部状态区显示当前占用独占槽的任务。
  renderExclusiveTask(st);
  // v3.10.5：状态一变就同步写操作按钮的可用性（扫描中置灰，结束自动恢复）。
  applyOpsGuard();
  updateTabVisibility();
  if (st.auth && st.auth.required && !st.auth.ok) { showLogin(); return st; }
  hideLogin();
  if (!st.ffmpeg || !st.ffmpeg.available) {
    // 仅提示一次
    if (!S.warnedFF) { S.warnedFF = true; if (st.config.video_enabled) toast('未找到 ffmpeg，视频相似检测暂不可用', 'err'); }
  }
  return st;
}

// 2026-11-08：统一的自适应轮询控制器（健康检测 / 规则搜索 / 状态栏三处共用）。
// 之前的写法是各自 setInterval 固定间隔，检测把 CPU 占满时前端还在按原节奏发请求，
// 等于自己给自己加压。这里收口成一套节奏：
//  1. 单飞：上一轮没返回不排下一轮，慢接口不会堆积并发；
//  2. 可见性：document.hidden 时直接停手（后台标签页 2 秒拉一次毫无意义），
//     切回前台立刻补一轮并恢复基准节奏；
//  3. 退避：fn 返回 true=有进展，回到 base；返回 undefined/其它=无进展，间隔翻倍到 max；
//     返回 false=任务已结束，控制器自行停止并摘掉监听。
// 返回 {stop, kick, active}，stop 之后可安全丢弃。
function startAdaptivePoll(fn, opts){
  const o = opts || {};
  const base = Math.max(200, Number(o.base) || 2000);
  const max = Math.max(base, Number(o.max) || base * 5);
  let timer = null, delay = base, stopped = false, running = false;

  const clearTimer = () => { if (timer) { clearTimeout(timer); timer = null; } };
  const stop = () => { stopped = true; clearTimer(); document.removeEventListener('visibilitychange', onVisible); };
  const schedule = (ms) => { clearTimer(); if (!stopped) timer = setTimeout(tick, ms); };

  async function tick(){
    timer = null;
    // 后台标签页不排下一轮，等 visibilitychange 再把节奏接回来
    if (stopped || document.hidden || running) return;
    running = true;
    let result;
    try { result = await fn(); }
    catch(e) { result = undefined; }   // 异常按「无进展」退避，不中断轮询
    finally { running = false; }
    if (result === false) { stop(); return; }
    delay = result === true ? base : Math.min(max, delay * 2);
    schedule(delay);
  }

  function onVisible(){
    if (document.hidden || stopped) return;
    delay = base;
    if (!timer && !running) tick();
  }

  document.addEventListener('visibilitychange', onVisible);
  schedule(0);
  return {
    stop,
    kick(){ if (stopped) return; delay = base; if (!timer && !running) tick(); },
    get active(){ return !stopped; }
  };
}

// v3.8.0：DC5 式搜索规则的中文标签（向导与设置页共用）。
const SEARCH_BASE_LABELS = {
  all: '全部（多类型）', content: '精确内容', attributes: '忽略内容（按属性组合）',
  name: '按文件名', size: '按文件大小', video: '相似视频', intro: '相同片头',
  audio: '重复音频', photo: '相似图片', folder: '重复文件夹',
  fuzzy: '模糊文件名', semantic: '语义分组'
};
const SEARCH_ATTR_LABELS = {
  name: '相同文件名', name_similar: '相似文件名', size: '相同文件大小',
  ext: '相同扩展名', volume: '相同驱动器', mtime_date: '相同修改日期',
  folder_name: '相同文件夹名称'
};

// defaultSearchRules 返回与后端 config.DefaultSearchRules 一致的默认值。
function defaultSearchRules(base){
  return {
    base: base || 'content', attrs: [], size_tolerance_kb: 0, name_similar_pct: 90,
    folder_name_mode: 'full', folder_name_depth: 1, ignore_copy_names: false,
    copy_patterns: ['副本', '(1)', '(2)', 'copy'], ignore_same_folder: false,
    case_sensitive: false, video_mode: 'frames', audio_window: '15s', duration_tolerance_sec: 0
  };
}

// deriveSearchRules 与后端 config.DeriveSearchRules 对齐，用于旧配置首次打开编辑器。
function deriveSearchRules(mode){
  const r = defaultSearchRules('all');
  if (mode === 'exact') r.base = 'content';
  else if (mode === 'similar') r.base = 'video';
  else if (mode === 'audio') r.base = 'audio';
  else if (mode === 'thumbs') { r.base = 'video'; r.video_mode = 'thumbs'; }
  else if (mode === 'ignore_content') r.base = 'name';
  return r;
}


// v3.6.0：DC5 式条件组合——结果类型清单与各预设对应的组合。
// 自定义组合（custom）时由 match_kinds 逐项决定结果页是否开放。
const MATCH_KIND_LABELS = {
  exact: '精确内容（字节哈希）',
  name: '同名文件',
  video: '相似视频（画面+声音）',
  intro: '相同片头',
  audio: '重复音频',
  photo: '相似图片',
  folder: '重复文件夹',
  fuzzy: '模糊文件名',
  semantic: '语义分组'
};
const MATCH_KIND_KEYS = Object.keys(MATCH_KIND_LABELS);
const MATCH_MODE_PRESETS = {
  all: MATCH_KIND_KEYS,
  exact: ['exact', 'folder'],
  similar: ['video', 'intro', 'audio'],
  thumbs: ['video'],
  audio: ['audio'],
  ignore_content: ['name', 'fuzzy', 'semantic'],
  custom: []
};

function matchKindsForMode(mode, customKinds){
  const m = mode || 'all';
  if (m === 'custom') return (customKinds || []).slice();
  return (MATCH_MODE_PRESETS[m] || MATCH_KIND_KEYS).slice();
}

function matchModeAllows(kind){
  const c = (S.status && S.status.config) || {};
  return matchKindsForMode(c.match_mode, c.match_kinds).includes(kind);
}

// 自定义组合勾选面板：预设模式下只读展示，选「自定义组合」后可编辑。
function matchKindsGridHtml(checked, disabled, act){
  const set = new Set(checked || []);
  return MATCH_KIND_KEYS.map(k => `<div class="wopt mini">
    <label><input type="checkbox" data-act="${esc(act)}" data-kind="${esc(k)}" ${set.has(k) ? 'checked' : ''} ${disabled ? 'disabled' : ''}> ${esc(MATCH_KIND_LABELS[k])}</label>
  </div>`).join('');
}

function fmtElapsed(sec){
  sec = Math.max(0, Math.floor(sec));
  const h = Math.floor(sec / 3600), m = Math.floor(sec / 60) % 60, s = sec % 60;
  const p = n => String(n).padStart(2, '0');
  return (h > 0 ? p(h) + ':' : '') + p(m) + ':' + p(s);
}

function phaseSequence(){
  const c = (S.status && S.status.config) || {};
  const quick = !(c.content_enabled !== false);
  const seq = ['scan', quick ? 'quick' : 'full'];
  if (!quick) { // quick 模式止于 BLAKE3，无媒体阶段
    if (c.photo_enabled) seq.push('photo');
    if (c.video_enabled) { seq.push('video'); seq.push('audio'); }
    if (c.intro_enabled) seq.push('intro');
  }
  return seq;
}

function estimateGroupHeight(g){
  if (S.collapsed.has(g.key)) return 68;
  return 96 + ((g.files || []).length || 0) * 38;
}

function resetVirtualList(){
  const heights = (S.groups || []).map(estimateGroupHeight);
  const prefix = [0];
  let sum = 0;
  heights.forEach(h => { sum += h; prefix.push(sum); });
  S.vlist = { heights, prefix, sum, range: [-1, -1] };
}

function extendVirtualList(){
  if (!S.vlist) { resetVirtualList(); return; }
  const v = S.vlist;
  for (let i = v.heights.length; i < S.groups.length; i++) v.heights.push(estimateGroupHeight(S.groups[i]));
  rebuildPrefix();
}

function rebuildPrefix(){
  const v = S.vlist; if (!v) return;
  v.prefix = [0];
  let sum = 0;
  for (let i = 0; i < v.heights.length; i++) { sum += v.heights[i]; v.prefix.push(sum); }
  v.sum = sum;
}

function indexForOffset(v, offset){
  if (!v || !v.heights.length) return 0;
  let lo = 0, hi = v.heights.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (v.prefix[mid + 1] <= offset) lo = mid + 1; else hi = mid;
  }
  return Math.min(lo, v.heights.length - 1);
}

// 列宽按“文件/视频 + 是否显示预览图”分开记忆：两种表格列数不同，共用一份会互相挤乱。
// v3.9.6：新增 'import' / 'health' 独立记忆键，与已有页面互不干扰。
function colLayoutKey(){
  if (S.tab === 'oplog') return 'oplog';
  if (S.tab === 'trash') return 'trash';
  if (S.tab === 'big') return 'big';
  if (S.tab === 'search') return 'search';
  if (S.tab === 'rename') return 'rename';
  if (S.tab === 'import') return S.importDetail ? 'import-detail' : 'import';
  if (S.tab === 'health') return 'health';
  const base = S.tab === 'results'
    ? 'results'
    : ((S.tab === 'video' || S.tab === 'intro') ? 'video' : 'files');
  return base + (S.genThumbs ? '-thumb' : '');
}

function loadColWidths(){
  if (S.colWidths) return S.colWidths;
  try { S.colWidths = JSON.parse(localStorage.getItem('verify.colWidths.v1') || '{}') || {}; }
  catch(e) { S.colWidths = {}; }
  return S.colWidths;
}

function saveColWidths(){
  try { localStorage.setItem('verify.colWidths.v1', JSON.stringify(S.colWidths || {})); } catch(e) {}
}

// resultColumns v3.9.2：kind 用于合并结果页的分组类型。
// 不同类型的组可出现的列不同（预览图、操作列），表头/colgroup 必须与行结构一致，否则列会错位。
/**
 * 结果页/分组页的列定义。
 *
 * v3.27 类型层：cols 显式标注为 ResultColumn[]，避免 TS 从第一个元素
 * 推断出「cls 必填、不能有 align」的过窄类型。运行时行为不变。
 *
 * @param {string} [kind]
 * @returns {ResultColumn[]}
 */
function resultColumns(kind){
  const results = S.tab === 'results';
  const isVid = results ? (kind === 'video' || kind === 'intro') : (S.tab === 'video' || S.tab === 'intro');
  /** @type {ResultColumn[]} */
  const cols = [{id:'sel', label:'', width:32, cls:'sel'}];
  // v3.9.2 修复：缩略图是全局开关，任何分组都应有预览列；没有可预览扩展名时行内显示“无预览”。
  if (S.genThumbs) {
    cols.push({id:'thumb', label:'预览', width:96});
  }
  cols.push({id:'name', label: results ? '文件名' : '文件', width:210, cls:'fname', sort:'text'});
  cols.push({id:'path', label:'路径', width:0, cls:'fpath', sort:'text'}); // 0 表示自动占满剩余宽度
  cols.push({id:'size', label:'大小', width:74, align:'right', sort:'size'});
  if (results) {
    // 合并结果页每行只显示一列差异信息（分辨率/编码、时长或修改时间），表头统一叫“时间”。
    cols.push({id:'meta', label:'时间', width:130});
    // 不传 kind（如双击重置列宽）时给出全部列，便于查找操作列的默认宽度。
    if (kind == null || kind === 'exact' || kind === 'video') cols.push({id:'act', label:'', width:176, cls:'act'});
  } else {
    cols.push({id:'meta', label:'分辨率/编码', width:122});
    cols.push({id:'date', label: isVid ? '时长' : '修改时间', width:110, align:'right', sort:'time'});
    // 操作列必须占住宽度：table-layout:fixed 下没有列宽的行末会被裁掉
    if (S.tab === 'exact' || S.tab === 'video') cols.push({id:'act', label:'', width:176, cls:'act'});
  }
  return cols;
}

// v3.9.4：操作记录页列定义，与 renderOpLog 的 colgroup/thead 对齐。
function oplogColumns(){
  return [
    {id:'ts', label:'时间', width:180, sort:'time'},
    {id:'actor', label:'来源 IP', width:130, sort:'text'},
    {id:'action', label:'动作', width:140, sort:'text'},
    {id:'result', label:'结果', width:80, sort:'text'},
    {id:'detail', label:'说明与明细', width:0, sort:'text'}, // 0 = 自动占满剩余宽度
    {id:'target', label:'条数', width:70, align:'right', sort:'num'},
    {id:'duration', label:'耗时(ms)', width:90, align:'right', sort:'num'}
  ];
}

// v3.9.4：双击恢复默认时需要知道当前 tab 的列定义来源。
// v3.9.6：import 页主列表 vs 详情页列不同，health 页繁体/普通也不同——根据状态选对 columns。
function columnsForTab(tab){
  if (tab === 'oplog') return oplogColumns();
  if (tab === 'trash') return trashColumns();
  if (tab === 'big') return bigColumns();
  if (tab === 'search') return searchResultColumns();
  if (tab === 'rename') return renamePanelColumns();
  if (tab === 'import') return S.importDetail ? importDetailColumns() : importColumns();
  if (tab === 'health') {
    // healthState() 定义在 settings.js（helper 先加载），直接读 S.health 避免时序问题
    return healthColumns(S.health && S.health.kind === 'traditional');
  }
  return resultColumns();
}

// v3.9.6 新增：各页面的列默认宽度定义。
function trashColumns(){
  return [
    {id:'t-sel', label:'', width:32, cls:'sel'},
    {id:'t-name', label:'文件名', width:210, sort:'text'},
    {id:'t-path', label:'原路径', width:0, sort:'text'}, // 0 = 自动占满剩余
    {id:'t-src', label:'来源', width:120, sort:'text'},
    {id:'t-size', label:'大小', width:80, align:'right', sort:'size'},
    {id:'t-time', label:'移入时间', width:160, align:'right', sort:'time'},
    {id:'t-act', label:'', width:140, cls:'act'}
  ];
}

function bigColumns(){
  return [
    {id:'b-sel', label:'', width:32, cls:'sel'},
    {id:'b-thumb', label:'', width:64, cls:'bthumb-head'},
    {id:'b-name', label:'文件名', width:260, sort:'text'},
    {id:'b-path', label:'路径', width:0, sort:'text'},
    {id:'b-size', label:'大小', width:100, align:'right', sort:'size'},
    {id:'b-time', label:'修改时间', width:160, align:'right', sort:'time'}
  ];
}

// v3.20.0：搜索列补齐扩展名与所在卷，六列都可在表头点击排序（服务端排序）。
function searchResultColumns(){
  return [
    {id:'sr-sel', label:'', width:32, cls:'sel'},
    {id:'sr-name', label:'名称', width:240, sort:'text'},
    {id:'sr-path', label:'路径', width:0, sort:'text'},
    {id:'sr-ext', label:'扩展名', width:88, sort:'text'},
    {id:'sr-size', label:'大小', width:88, align:'right', sort:'size'},
    {id:'sr-time', label:'修改时间', width:160, align:'right', sort:'time'},
    {id:'sr-vol', label:'所在卷', width:130, sort:'text'}
  ];
}

// v3.20.0：固定行高虚拟滚动（搜索页可能命中几十万条）。
// 与结果页按「分组估算高度」的 vlist 不同，这里每行等高，只需上下两个占位行，
// 滚动时只重建可视区附近的行，DOM 数量与命中总数无关。
function virtualWindow(total, rowH, scrollTop, viewH, buffer){
  const h = Math.max(1, rowH || 1);
  const start = Math.max(0, Math.floor(Math.max(0, scrollTop) / h) - buffer);
  const count = Math.ceil((viewH || h) / h) + buffer * 2;
  return { start, end: Math.min(total, start + count) };
}

function virtualSpacerRow(colSpan, height){
  if (height <= 0) return '';
  return `<tr class="vspacer" aria-hidden="true"><td colspan="${colSpan}" style="height:${height}px;padding:0;border:0"></td></tr>`;
}

function renamePanelColumns(){
  return [
    {id:'rp-path', label:'路径', width:0, sort:'text'},
    {id:'rp-src', label:'原名', width:200, sort:'text'},
    {id:'rp-dst', label:'目标名', width:200}, // 目标名是可编辑输入框，文本为空，不参与排序
    {id:'rp-status', label:'状态', width:100, sort:'text'}
  ];
}

function renamePageColumns(){
  return [
    {id:'rg-path', label:'路径', width:0, sort:'text'},
    {id:'rg-src', label:'原名', width:200, sort:'text'},
    {id:'rg-dst', label:'目标名', width:200}, // 目标名是可编辑输入框，不参与排序
    {id:'rg-status', label:'状态', width:100, sort:'text'},
    {id:'rg-act', label:'', width:100}
  ];
}

function searchMethodColumns(){
  return [
    {id:'sm-type', label:'类型', width:80, sort:'text'},
    {id:'sm-scope', label:'作用范围', width:160, sort:'text'},
    {id:'sm-params', label:'参数', width:0, sort:'text'},
    {id:'sm-act', label:'', width:100}
  ];
}

// v3.9.6：导入记录列表页列定义（7 列）。
function importColumns(){
  return [
    {id:'im-name', label:'会话名称', width:200, sort:'text'},
    {id:'im-src', label:'来源', width:100, sort:'text'},
    {id:'im-time', label:'导入时间', width:160, sort:'time'},
    {id:'im-rows', label:'行数', width:80, align:'right', sort:'num'},
    {id:'im-grps', label:'组数', width:80, align:'right', sort:'num'},
    {id:'im-match', label:'已匹配', width:80, align:'right', sort:'pct'},
    {id:'im-act', label:'', width:160, cls:'act'}
  ];
}

// v3.9.6：导入详情页——每组独立表格的列（6 列），独立记忆避免覆盖主列表。
function importDetailColumns(){
  return [
    {id:'id-file', label:'文件', width:220, sort:'text'},
    {id:'id-path', label:'路径', width:0, sort:'text'},
    {id:'id-size', label:'大小', width:80, align:'right', sort:'size'},
    {id:'id-time', label:'修改时间', width:120, align:'right', sort:'time'},
    {id:'id-state', label:'状态', width:80, sort:'text'},
    {id:'id-act', label:'', width:120, cls:'act'}
  ];
}

// v3.9.6：文件健康页列定义——繁体页多一列「改后文件名」。
// v3.9.6：文件健康页列定义——繁体页多一列「改后文件名」。
// v3.27 类型层：同 resultColumns，cols 显式标注为 ResultColumn[]。
/**
 * @param {boolean} isTrad
 * @returns {ResultColumn[]}
 */
function healthColumns(isTrad){
  /** @type {ResultColumn[]} */
  const cols = [
    {id:'h-sel', label:'', width:32, cls:'sel'},
    {id:'h-name', label:'文件名', width:220, sort:'text'}
  ];
  if (isTrad) cols.push({id:'h-simp', label:'改后文件名', width:180, sort:'text'});
  cols.push(
    {id:'h-path', label:'路径', width:0, sort:'text'},
    {id:'h-reason', label:'原因', width:260, sort:'text'},
    {id:'h-size', label:'大小', width:80, align:'right', sort:'size'},
    {id:'h-act', label:'', width:180, cls:'act'}
  );
  return cols;
}

function applyColWidth(id, w){
  // 虚拟列表里同时存在多个分组表格，宽度必须一起改，否则滚动时列会跳。
  document.querySelectorAll(`table.rtable col[data-col="${id}"]`).forEach(c => {
    c.style.width = w > 0 ? w + 'px' : '';
  });
}

function setColWidth(layout, id, w){
  const m = loadColWidths();
  if (!m[layout]) m[layout] = {};
  if (w > 0) m[layout][id] = Math.round(w); else delete m[layout][id];
  applyColWidth(id, w);
}

function endColDrag(){
  if (!colDrag) return;
  document.body.classList.remove('col-resizing');
  if (colDrag.moved) saveColWidths();
  colDrag = null;
  // 列宽变窄会让文件名换行，当前可见分组的高度需要重新测量。
  requestAnimationFrame(measureVirtualGroups);
}

// ===== v3.9.11 表头排序 =====
// 设计取舍：数据分页与虚拟滚动都在别处管理，若在各页面数据流里插入排序，
// 改动面大且容易与「加载更多」冲突。这里统一在渲染完成后按单元格文本排序，
// 状态按表格 data-sortkey 记忆，页面重新渲染后再调用 applyTableSort 恢复。
// 列定义用 sort:'size'/'num'/'time'/'pct'/'text' 声明可排序及比较方式。

const TABLE_SORTS = {};

// TABLE_SORT_HOOKS 让页面接管排序（v3.10.2）。注册后 applyTableSort 不再动 DOM，
// 由页面自己按 tableSortState 在数据层排序并重绘。
const TABLE_SORT_HOOKS = {};

function tableSortState(key){
  if (!TABLE_SORTS[key]) TABLE_SORTS[key] = { col: '', dir: 1 };
  return TABLE_SORTS[key];
}

// parseSortVal 把单元格文本还原成可比值；空值与占位符返回 null，排序时统一沉底。
function parseSortVal(text, type){
  const s = String(text == null ? '' : text).trim();
  if (!s || s === '—' || s === '-') return null;
  if (type === 'size'){
    const m = /^([\d.]+)\s*(B|KB|MB|GB|TB|PB)?$/i.exec(s);
    if (!m) return null;
    const unit = { B:0, KB:1, MB:2, GB:3, TB:4, PB:5 }[(m[2] || 'B').toUpperCase()];
    return Number(m[1]) * Math.pow(1024, unit);
  }
  if (type === 'pct'){
    const n = parseFloat(s);
    return isNaN(n) ? null : n;
  }
  if (type === 'num'){
    const n = Number(s.replace(/,/g, ''));
    return isNaN(n) ? null : n;
  }
  if (type === 'time'){
    // 时长（m:ss / h:mm:ss）与日期文本都兼容
    if (/^\d+(:\d{1,2}){1,2}$/.test(s)){
      return s.split(':').reduce((acc, v) => acc * 60 + Number(v), 0);
    }
    const t = Date.parse(s.replace(' ', 'T'));
    return isNaN(t) ? null : t;
  }
  return s;
}

// sortTableRows 就地重排一张表的 tbody 行，并更新表头箭头。
function sortTableRows(table, st){
  const head = table.querySelector('thead');
  const body = table.querySelector('tbody');
  if (!head || !body) return;
  const ths = Array.from(head.querySelectorAll('th'));
  ths.forEach(th => {
    const on = !!st.col && th.dataset.sortCol === st.col;
    th.classList.toggle('sorted', on);
    th.dataset.sortDir = on ? String(st.dir) : '';
  });
  if (!st.col) return;
  const idx = ths.findIndex(th => th.dataset.sortCol === st.col);
  if (idx < 0) return;
  const type = ths[idx].dataset.sortType || 'text';
  const rows = Array.from(body.rows);
  // 先记录原始序号，保证同值行不会因为排序来回跳动
  rows.forEach((r, i) => r.dataset.sortIdx = i);
  rows.sort((a, b) => {
    const va = parseSortVal(a.cells[idx] ? a.cells[idx].textContent : '', type);
    const vb = parseSortVal(b.cells[idx] ? b.cells[idx].textContent : '', type);
    if (va == null && vb == null) return Number(a.dataset.sortIdx) - Number(b.dataset.sortIdx);
    if (va == null) return 1;  // 空值始终沉底，与升降序无关
    if (vb == null) return -1;
    if (va < vb) return -st.dir;
    if (va > vb) return st.dir;
    return Number(a.dataset.sortIdx) - Number(b.dataset.sortIdx);
  });
  rows.forEach(r => body.appendChild(r));
}

// syncTableSortArrows 只更新表头箭头，不做行排序。
// v3.10.2：文件健康页上万条结果改成数据层排序，重绘后用它恢复箭头状态。
function syncTableSortArrows(table, st){
  const head = table && table.querySelector('thead');
  if (!head) return;
  head.querySelectorAll('th').forEach(th => {
    const on = !!st.col && th.dataset.sortCol === st.col;
    th.classList.toggle('sorted', on);
    th.dataset.sortDir = on ? String(st.dir) : '';
  });
}

// applyTableSort 对同一 key 的所有表格应用当前排序状态（分组页可能同时有多张表）。
// v3.10.2：页面可注册 TABLE_SORT_HOOKS[key]，在大数据量下改为数据层排序后整体重绘，
// 避免上万行逐个 appendChild 把主线程卡住。
function applyTableSort(key){
  const st = tableSortState(key);
  const hook = TABLE_SORT_HOOKS[key];
  if (hook) { hook(st); return; }
  document.querySelectorAll('table[data-sortkey="' + key + '"]').forEach(t => sortTableRows(t, st));
}

// thCells 生成可排序表头单元格；保留原有的 colresize 拖拽手柄。
function thCells(cols, opts){
  opts = opts || {};
  return cols.map(c => {
    const align = c.align === 'right' ? ' style="text-align:right"' : '';
    // 列定义目前都是内部常量，但属性值统一走 esc()：以后有人把后端字段塞进列定义也不会破属性
    const sort = c.sort ? ` data-sort-col="${esc(c.id)}" data-sort-type="${esc(c.sort)}"` : '';
    const skip = opts.noResize && opts.noResize(c);
    const grip = skip ? '' : `<span class="colresize" data-col="${esc(c.id)}" title="拖动调整列宽，双击恢复默认"></span>`;
    return `<th class="${esc(c.cls || '')}"${align}${sort}>${esc(c.label)}${grip}</th>`;
  }).join('');
}

// 点击列头切换排序：同一列再次点击反转方向，换列则从升序开始。
document.addEventListener('click', ev => {
  const th = ev.target.closest('th[data-sort-col]');
  if (!th || ev.target.closest('.colresize')) return;
  const table = th.closest('table[data-sortkey]');
  if (!table) return;
  const key = table.dataset.sortkey;
  const st = tableSortState(key);
  if (st.col === th.dataset.sortCol) st.dir = -st.dir;
  else { st.col = th.dataset.sortCol; st.dir = 1; }
  applyTableSort(key);
});

function maybeLoadMore(){
  const list = $('groupList');
  if (!list || !S.hasMore || S.loadingMore) return;
  if (list.scrollHeight - list.scrollTop - list.clientHeight < 600) loadMoreGroups();
}

function filtersForTab(mode){
  if (mode === 'video' || mode === 'intro' || mode === 'samevideo') return {};
  return listFilters();
}

function filterSig(mode, filters){
  filters = filters || {};
  const intro = mode === 'intro' ? Math.min(300, Math.max(1, Number(S.introN || 10))) : 0;
  return `${mode}|${filters.minKB || ''}|${filters.ext || ''}|${filters.exts || ''}|${filters.q || ''}|${filters.maxKB || ''}|${filters.since || ''}|${filters.until || ''}|${intro}`;
}

// listFilterActive v1.25.0：助手筛选组是否有任何激活条件（顶栏筛选按钮点亮用）。
function listFilterActive(){
  const f = S.listFilter;
  return !!(f.marked || f.unmarked || f.partial || f.allMarked || f.protected ||
    f.countMin !== '' || f.countMax !== '' || f.sizeMin !== '' || f.sizeMax !== '' ||
    f.fileMin !== '' || f.fileMax !== '' || f.minKB !== '' || f.maxKB !== '' ||
    f.exts !== '' || f.q !== '' || f.since !== '' || f.until !== '');
}

// listFilters v1.25.0：服务端过滤参数统一取自助手筛选组（原 S.filters 已并入）。
function listFilters(){
  const f = S.listFilter;
  return { minKB: f.minKB || '', ext: '', exts: f.exts || '', q: f.q || '', maxKB: f.maxKB || '', since: f.since || '', until: f.until || '' };
}

// applyLfInput 统一处理筛选器输入（v1.25.0 + v3.5 修复）：
// 原来绑在 click 上，点一下 select 就会重绘面板、把展开的下拉菜单直接销毁。
// 现在由 input/change 事件调用，文本输入 400ms 防抖，下拉选择立即生效。
function applyLfInput(el){
  S.listFilter[el.dataset.k] = el.value;
  clearTimeout(window.__lfT);
  window.__lfT = setTimeout(() => refreshView(), el.tagName === 'SELECT' ? 0 : 400);
}

// advFilterParams 返回 listFilter 里的高级过滤参数字符串（v2.4.0）。
// 所有分组接口（video/intro/audio/fuzzy/semantic/exact/name）都可以追加这个字符串，
// 避免在每个 URL 构造处重复写参数名。
function advFilterParams(){
  const lf = S.listFilter;
  const parts = [];
  if (lf.dateOnly) parts.push('date_only=1');
  if (lf.sameDrive) parts.push('same_drive=' + encodeURIComponent(lf.sameDrive));
  if (lf.maxDurDiff && Number(lf.maxDurDiff) > 0) parts.push('max_dur_diff=' + Number(lf.maxDurDiff));
  if (lf.ignoreSameFolder) parts.push('ignore_same_folder=1');
  // v2.8.0 补充条件
  if (lf.folderName) parts.push('folder_name=' + encodeURIComponent(lf.folderName));
  if (lf.folderDepth && Number(lf.folderDepth) > 0) parts.push('folder_depth=' + Number(lf.folderDepth));
  if (lf.similarName) parts.push('similar_name=1');
  if (lf.sizeTolKB && Number(lf.sizeTolKB) > 0) parts.push('size_tol_kb=' + Number(lf.sizeTolKB));
  // v3.4.0 跨设备标记
  if (lf.crossDevice) parts.push('cross=1');
  // v2.9.0 标签过滤
  if (lf.tagName) parts.push('tag_name=' + encodeURIComponent(lf.tagName));
  if (lf.tagValue) parts.push('tag_value=' + encodeURIComponent(lf.tagValue));
  return parts.length ? '&' + parts.join('&') : '';
}

// listFilterQuery 返回列表筛选器的查询串（体积/日期/扩展名/关键词），v3.10.10。
// 视频页与片头页以前在拼接这些参数前就 return 了，「最大体积」等条件从未生效。
// 注意后端字段是 max_kb（KB），不是 max_size（字节）。
function listFilterQuery(){
  const lf = listFilters();
  const parts = [];
  if (lf.minKB) parts.push('min_size=' + Math.round(Number(lf.minKB) * 1024));
  if (lf.maxKB) parts.push('max_kb=' + Math.round(Number(lf.maxKB)));
  const exts = String(lf.exts || '').split(',').map(s => s.trim().toLowerCase().replace(/^\./, '')).filter(Boolean);
  if (exts.length) parts.push('exts=' + encodeURIComponent(exts.join(',')));
  if (lf.q) parts.push('q=' + encodeURIComponent(lf.q));
  if (lf.since) parts.push('since_ms=' + new Date(lf.since + 'T00:00:00').getTime());
  if (lf.until) parts.push('until_ms=' + new Date(lf.until + 'T00:00:00').getTime());
  return parts.length ? '&' + parts.join('&') : '';
}

async function fetchChunk(mode, page, filters){
  filters = filters || {};
  const so = '&sort=' + encodeURIComponent(S.sort || 'waste') + '&order=' + encodeURIComponent(S.sortOrder || 'desc');
  const adv = advFilterParams();
  // v3.10.10（第六轮 P1-1）：先统一拼好筛选串，三个分支共用，避免只有通用分组页带上条件。
  const lfq = listFilterQuery();
  if (mode === 'video') return api('/api/groups/video?page=' + page + '&page_size=' + S.pageSize + so + adv + lfq);
  if (mode === 'intro') return api('/api/groups/intro?n=' + Math.min(300, Math.max(1, Number(S.introN || 10))) + '&page=' + page + '&page_size=' + S.pageSize + so + adv + lfq);
  // v1.41.5：相同视频 = 精确重复 + 仅视频扩展名；用户填了扩展名时不重复设置，避免两段参数都带 exts。
  const VIDEO_EXTS = 'mp4,mkv,avi,mov,wmv,flv,ts,m2ts,webm,mpg,mpeg,m4v,rmvb,rm,vob,3gp,asf,divx,ogv';
  const params = new URLSearchParams({ mode: mode === 'samevideo' ? 'exact' : mode, page: String(page), page_size: String(S.pageSize) });
  // v3.10.10（第七轮 P1-2）：第二页起不再让后端重算总数，复用第一页的值。
  if (page > 1) params.set('skip_total', '1');
  if (mode === 'samevideo' && !String(S.listFilter.exts || '').trim()) params.set('exts', VIDEO_EXTS);
  params.set('sort', S.sort || 'waste');
  params.set('order', S.sortOrder || 'desc');
  const lf = Object.assign({}, S.listFilter, filters);
  // v2.4.0：高级过滤参数
  if (lf.dateOnly) params.set('date_only', '1');
  if (lf.sameDrive) params.set('same_drive', lf.sameDrive);
  if (lf.maxDurDiff && Number(lf.maxDurDiff) > 0) params.set('max_dur_diff', String(Number(lf.maxDurDiff)));
  if (lf.ignoreSameFolder) params.set('ignore_same_folder', '1');
  // v2.8.0 补充条件
  if (lf.folderName) params.set('folder_name', lf.folderName);
  if (lf.folderDepth && Number(lf.folderDepth) > 0) params.set('folder_depth', String(Number(lf.folderDepth)));
  if (lf.similarName) params.set('similar_name', '1');
  if (lf.sizeTolKB && Number(lf.sizeTolKB) > 0) params.set('size_tol_kb', String(Number(lf.sizeTolKB)));
  // v3.4.0 跨设备标记
  if (lf.crossDevice) params.set('cross', '1');
  // v2.9.0 标签过滤
  if (lf.tagName) params.set('tag_name', lf.tagName);
  if (lf.tagValue) params.set('tag_value', lf.tagValue);
  return api('/api/groups?' + params.toString() + lfq);
}

function takePrefetch(mode, filters){
  const pf = S.prefetch;
  if (!pf) return null;
  if (pf.mode !== mode || pf.sig !== filterSig(mode, filters) || Date.now() - pf.at > 600000) {
    if (pf.mode === mode) S.prefetch = null;
    return null;
  }
  S.prefetch = null;
  return pf;
}

function applyPrefetch(pf){
  S.loading = false;
  S.groups = pf.groups || [];
  S.total = Number(pf.total || 0);
  S.loadedPage = 1;
  S.hasMore = S.groups.length < S.total;
  S.vlist = null;
  if (pf.mode === 'intro' && pf.meta) {
    S.introAnalyzed = Number(pf.meta.analyzed || 0);
    S.introPending = Number(pf.meta.pending || 0);
  }
  initSelections();
  renderGroups();
  reportMetric('prefetch_hit', Date.now() - pf.at, pf.mode);
  silentRefreshFirstChunk(pf.mode);
}

async function silentRefreshFirstChunk(mode){
  const filters = Object.assign({}, filtersForTab(mode));
  try {
    const r = await fetchChunk(mode, 1, filters);
    if (S.tab !== mode) return;
    const list = $('groupList');
    if (list && list.scrollTop > 300) { S.total = Number(r.total || S.total); updateListStatus(); return; }
    const fresh = r.groups || [];
    const seen = new Set(fresh.map(g => g.key));
    // 首块以最新结果为准，已加载的后续块保留，避免用户已滚动的内容消失。
    S.groups = fresh.concat(S.groups.filter(g => !seen.has(g.key)));
    S.total = Number(r.total || S.total);
    S.hasMore = S.groups.length < S.total;
    S.vlist = null;
    initSelections();
    renderGroups();
  } catch(e) { /* 静默刷新失败时保留预取内容，下一次手动刷新即可 */ }
}

async function reloadTab(){
  // v3.17.0：旧类型标签已由 switchTab 统一映射为 results，S.tab 只剩非结果页取值。
  if (S.tab === 'trash') return loadTrash();
  return loadGroups(S.tab);
}

function photoCellHtml(f, gkey){
  const sel = S.photoSel.has(f.path);
  return `<div class="pcell${sel?' pcell-sel':''}">
    <label class="pcheck"><input type="checkbox" data-act="photo-sel" data-path="${esc(f.path)}" data-gkey="${esc(gkey)}"${sel?' checked':''}></label>
    <img class="pimg" loading="lazy" src="/api/raw?path=${encodeURIComponent(f.path)}" alt="">
    <div class="pname" title="${esc(f.path)}">${esc(f.name)}</div>
    <div class="pmeta">${fmtB(f.size)}${isProtectedPath(f.path) ? ' · <span class="badge">保护</span>' : ''}</div>
  </div>`;
}

// v3.10.6：CSP 收紧到 script-src 'self' 后，内联 onerror 会被浏览器拦下，
// 改用捕获阶段的全局监听隐藏加载失败的图片，行为与原来一致。
document.addEventListener('error', ev => {
  const el = ev.target;
  if (el && el.tagName === 'IMG') el.style.visibility = 'hidden';
}, true);

// v3.9.6 修 G-27：删掉 :800 重复声明——它是 bug（两个正则相同 `replace(/\\/g,'/').replace(/\\/g,'/')` 无效重复），保留 L39 更简洁的版本

