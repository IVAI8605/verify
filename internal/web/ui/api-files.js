// ---------- v4.5.0 前端模块拆分：文件搜索 / 清单式重命名 / 批量改名状态 ----------
// 从 api.js 原样切出，零构建形态不变（仍由 index.html 按顺序 <script defer> 引入）。
// 跨文件只通过全局函数与 S 状态交互，不要在这里定义新的全局初始化副作用。
// ===== v2.10.0 文件搜索 + 清单式重命名 =====
// v3.20.0：搜索改成 Everything 式——游标分页滚动加载、服务端排序、全选走凭据。
function searchState(){
  // v3.9.10：补 6 类筛选条件，扩展名/体积/时间/目录与排序都能在界面上改
  if (!S.search) S.search = {
    q:'', mode:'substring', caseSensitive:false, sort:'name', order:'asc',
    ext:'', minKB:'', maxKB:'', since:'', until:'', dir:'',
    items:[], total:0, took:0, loading:false, ready:false, notice:'',
    // v3.20.0：游标与滚动加载；sel 只存显式勾选，全选时用 token + excludes。
    nextCursor:'', loadingMore:false, rowH:36,
    sel:new Set(), selAll:false, selToken:'', selCount:0, excludes:new Set()
  };
  return S.search;
}

function renameState(){
  // v3.21.0：token 是「全选全部结果」加入的凭据条目，不把几十万条路径放进内存。
  if (!S.rename) S.rename = { list:[], batchID:0, methods:[], presets:[], presetID:0, token:null, planID:0, planSig:'', batchIDs:[] };
  return S.rename;
}

// renameTokenCount / renameTotalCount 统一计算「显式条目 + 全选凭据」的总数。
function renameTokenCount(){
  const r = renameState();
  return r.token ? Number(r.token.count || 0) : 0;
}
function renameTotalCount(){
  const r = renameState();
  return r.list.length + renameTokenCount();
}

// ---------- v3.5.0 重命名规则引擎：方法链 / 预览 / 预设 ----------
function renameMethodFromForm(){
  const type = $('rmType').value;
  const m = { type, apply_to: $('rmApply').value };
  const text = $('rmText').value || '';
  const repl = $('rmRepl').value || '';
  const count = Number($('rmCount').value || 0);
  const start = Number($('rmStart').value || 0);
  const step = Number($('rmStep').value || 0);
  switch(type){
    case 'remove': m.count = count; m.from_end = $('rmFromEnd').checked; break;
    case 'remove_pattern': m.pattern = text; m.regex = $('rmRegex').checked; break;
    case 'replace': m.pattern = text; m.replacement = repl; m.occurrence = count; m.regex = $('rmRegex').checked; break;
    case 'list_replace':
      m.pairs = text.split(',').map(x => x.trim()).filter(Boolean).map(x => {
        const i = x.indexOf('=');
        return i < 0 ? null : { from: x.slice(0, i), to: x.slice(i + 1) };
      }).filter(Boolean);
      break;
    case 'add':
      m.text = text; m.position = $('rmPosition').value;
      if (m.position === 'match') m.pattern = repl;
      break;
    case 'renumber': m.start = start; m.step = step || 1; m.digits = count; m.text = text; break;
    case 'new_case': m.mode = (text || 'title').trim(); break;
    case 'trim': m.chars = text; break;
    case 'swap':
      m.separator = $('rmSep').value || '_'; m.index_a = count; m.index_b = Number(repl || 0);
      break;
    case 'list': m.names = text.split(',').map(x => x.trim()).filter(Boolean); break;
    case 'timestamp': break;
  }
  return m;
}

function addRenameMethod(){
  const r = renameState();
  const m = renameMethodFromForm();
  r.methods.push(m);
  renderRenamePage();
}

function removeRenameMethod(idx){
  const r = renameState();
  r.methods.splice(idx, 1);
  renderRenamePage();
}

function addRenameFile(){
  const r = renameState();
  const p = ($('renameAddPath') || {}).value || '';
  const path = p.trim();
  if (!path) { toast('请填写文件路径', 'err'); return; }
  if (r.list.some(x => x.path === path)) { toast('该文件已在清单里', 'err'); return; }
  const name = path.split('/').pop() || path;
  r.list.push({ path, orig: name, target: name, state: '待执行' });
  renderRenamePage();
}

// renamePlanCreate 生成服务端改名计划（v3.21.0）。
// 显式清单与全选凭据一起提交；方法链没变时复用已有计划，保证「取消后再次执行」能续跑。
async function renamePlanCreate(){
  const r = renameState();
  const sig = JSON.stringify(r.methods || []);
  if (r.planID && r.planSig === sig) return r.planID;
  const body = { methods: r.methods, files: r.list.map(x => ({ path: x.path, name: '' })) };
  if (r.token) { body.selection_token = r.token.token; body.excludes = r.token.excludes || []; }
  const res = await postJSON('/api/rename/plan', body);
  r.planID = Number(res.plan_id || 0);
  r.planTotal = Number(res.total || 0);
  r.planSig = sig;
  return r.planID;
}

// previewRenameToken 用计划接口预览（只展示总数，避免几十万条响应）。
async function previewRenameToken(){
  const r = renameState();
  if (!r.methods.length) { toast('先添加方法', 'err'); return; }
  try {
    await renamePlanCreate();
    const total = r.planTotal || renameTotalCount();
    setModal('预览新名（共 ' + total.toLocaleString() + ' 条）',
      `<div class="hint" style="margin-bottom:8px">计划已生成，执行时按 2000 条一片处理；撞名会自动加 _1，绝不覆盖。</div>
       <div class="form-actions"><button class="btn primary" data-act="modal-cancel">知道了</button></div>`, null);
  } catch(e) { toast('预览失败：' + (e && e.message || e), 'err'); }
}

// executeRenameTokenPlan 两阶段执行（v3.21.0）：
// 先建计划，再按 2000 条一片执行；取消后计划保留，再次执行从剩下的 pending 继续。
async function executeRenameTokenPlan(){
  const r = renameState();
  const total = renameTotalCount();
  if (!total || !r.methods.length) return;
  if (!confirm('确认按方法链执行 ' + total.toLocaleString() + ' 个文件的改名？\n\n撞名会自动加 _1，绝不覆盖；执行期间请不要刷新或关闭页面。')) return;
  try {
    await renamePlanCreate();
  } catch(e) { toast('创建计划失败：' + (e && e.message || e), 'err'); return; }
  // 续跑前清掉上一次的取消标记，否则第一个分片会立刻返回「已取消」。
  // v3.25.0（P2-5）：必须等清除请求真正落库再发第一个分片，旧实现 fire-and-forget，
  // 两个请求在服务端可能乱序，分片先到就会白跑一轮。
  try { await postJSON('/api/rename/cancel', { plan_id: r.planID, clear: true }); }
  catch(e) { toast('清除取消标记失败，请重新开始：' + (e && e.message || e), 'err'); return; }
  r.batchIDs = [];
  await runBatchedOp({
    id:'rename', label:'批量改名', batchSize:1, maxBatches:100000,
    op: { total },
    call: async (chunk, op) => {
      const res = await postJSON('/api/rename/execute-chunk', { plan_id: r.planID, limit: 2000 });
      if (res.cancelled) {
        // v3.25.1：服务端说「已取消」（另一个标签页取消或取消标记残留）时，
        // 必须把取消写回运行态。旧实现直接返回 stop，onDone 会当成正常结束，
        // 把续跑要用的 planID 清掉，界面还显示「完成 0 个」。
        if (op) op.cancelled = true;
        return { done: 0, failed: 0, processed: 0, stop: true };
      }
      if (res.batch_id) { r.batchID = Number(res.batch_id); r.batchIDs.push(r.batchID); }
      const processed = Number(res.done || 0) + Number(res.failed || 0);
      return { done: res.done || 0, failed: res.failed || 0, items: res.items || [], processed, stop: Number(res.remaining || 0) <= 0 };
    },
    onProgress: () => { const host = $('renameOpHost'); if (host) host.innerHTML = batchedOpCardHtml('rename'); },
    onDone: async (o) => {
      if (!o.cancelled) {
        // 全部执行完后搜索结果已被改名打散，旧凭据必然失效，直接清掉避免继续误用。
        r.planID = 0; r.planSig = ''; r.planTotal = 0; r.token = null;
      }
      r.recentLoaded = false;
      renderRenamePage();
      const h = $('renameHint');
      if (h) h.textContent = '完成 ' + o.done + ' 个，失败 ' + o.failed + ' 个' + (o.cancelled ? '（已中止，再次执行会从剩余项继续）' : '');
      showBatchedOpDone(o, '', '改名失败明细');
    }
  });
}

async function previewRenameMethods(){
  const r = renameState();
  if (!renameTotalCount() || !r.methods.length) return;
  if (r.token) { await previewRenameToken(); return; }
  try {
    const res = await postJSON('/api/rename/preview', {
      files: r.list.map(x => ({ path: x.path, name: '' })), methods: r.methods
    });
    (res.items || []).forEach((it, i) => {
      if (r.list[i]) { r.list[i].target = it.target; r.list[i].state = it.error || '待执行'; }
    });
    renderRenamePage();
    toast('已生成预览（未执行）', 'ok');
  } catch(e) { toast('预览失败：' + (e && e.message || e), 'err'); }
}

async function executeRenameMethods(){
  const r = renameState();
  const total = renameTotalCount();
  if (!total || !r.methods.length) return;
  if (r.token) { await executeRenameTokenPlan(); return; }
  if (!confirm('确认按方法链执行 ' + r.list.length + ' 个文件的改名？\n\n撞名会自动加 _1，绝不覆盖。')) return;
  try {
    const res = await postJSON('/api/rename/execute', {
      files: r.list.map(x => ({ path: x.path, name: '' })), methods: r.methods, preset_id: r.presetID || 0
    });
    r.batchID = Number(res.batch_id || 0);
    r.batchIDs = r.batchID ? [r.batchID] : [];
    (res.items || []).forEach((it, i) => { if (r.list[i]) r.list[i].state = it.error ? ('失败：' + it.error) : '已完成'; });
    renderRenamePage();
    const h = $('renameHint');
    if (h) h.textContent = '完成 ' + (res.done || 0) + ' 个，失败 ' + (res.failed || 0) + ' 个，批次 ' + r.batchID;
    toast('改名完成：成功 ' + (res.done || 0) + '，失败 ' + (res.failed || 0), res.failed ? 'err' : 'ok');
    // v3.10.5：规则引擎改名后按快照核对磁盘（路径、大小、mtime）。
    showReconcile(res.reconcile);
  } catch(e) { toast('执行失败：' + (e && e.message || e), 'err'); }
}

async function loadRenamePresets(){
  const r = renameState();
  try {
    const res = /** @type {RenamePresetsResp} */ (await api('/api/rename/presets'));
    r.presets = res.presets || [];
    if (S.tab === 'rename') renderRenamePage();
  } catch(e) { /* 预设加载失败不阻塞页面 */ }
}

function loadRenamePreset(){
  const r = renameState();
  const id = Number(($('rmPreset') || {}).value || 0);
  const p = (r.presets || []).find(x => x.id === id);
  if (!p) { toast('请选择预设', 'err'); return; }
  try {
    r.methods = JSON.parse(p.methods || '[]');
    r.presetID = id;
    renderRenamePage();
    toast('已加载预设：' + p.name, 'ok');
  } catch(e) { toast('预设数据损坏', 'err'); }
}

async function saveRenamePreset(){
  const r = renameState();
  if (!r.methods.length) { toast('先添加方法', 'err'); return; }
  const name = (($('rmPresetName') || {}).value || '').trim();
  if (!name) { toast('请填写预设名称', 'err'); return; }
  try {
    await postJSON('/api/rename/presets', { id: 0, name, description: '', methods: JSON.stringify(r.methods) });
    toast('预设已保存', 'ok');
    await loadRenamePresets();
  } catch(e) { toast('保存失败：' + (e && e.message || e), 'err'); }
}

async function deleteRenamePreset(){
  const r = renameState();
  const id = Number(($('rmPreset') || {}).value || 0);
  if (!id) { toast('请选择预设', 'err'); return; }
  if (!confirm('删除该预设？')) return;
  try {
    /** @type {RenamePresetsResp} */ (await api('/api/rename/presets/' + id, { method: 'DELETE' }));
    toast('已删除', 'ok');
    await loadRenamePresets();
  } catch(e) { toast('删除失败：' + (e && e.message || e), 'err'); }
}

let searchTimer = 0;

// 每次向服务端要多少条（v3.20.0）。2000 条约 600KB，滚动到底自动续拉。
const SEARCH_BATCH = 2000;
// 搜索行等高；可视区上下各多渲染几行，快速滚动时不露白。
const SEARCH_ROW_BUFFER = 12;

// searchParams 统一构造 /api/search 的查询串（游标为空表示第一页）。
function searchParams(st, cursor){
  const p = new URLSearchParams({ q: st.q, mode: st.mode, case: st.caseSensitive ? '1' : '0', sort: st.sort, order: st.order, limit: String(SEARCH_BATCH) });
  // 筛选条件只在有值时才传，避免 0 被后端当成「限制为 0 字节」
  if (st.ext) p.set('ext', st.ext);
  if (st.minKB !== '' && Number(st.minKB) > 0) p.set('min_size', String(Math.round(Number(st.minKB) * 1024)));
  if (st.maxKB !== '' && Number(st.maxKB) > 0) p.set('max_size', String(Math.round(Number(st.maxKB) * 1024)));
  // 日期按本地时区取整天边界，和用户看到的「起止日期」一致
  if (st.since) p.set('since_ms', String(new Date(st.since + 'T00:00:00').getTime()));
  if (st.until) p.set('until_ms', String(new Date(st.until + 'T23:59:59').getTime()));
  if (st.dir) p.set('dir', st.dir);
  if (cursor) p.set('cursor', cursor);
  return p;
}

// searchBody 把同样的条件包成 JSON，供 /api/search/selection 使用（v3.20.0）。
function searchBody(st){
  return {
    q: st.q, mode: st.mode, case: !!st.caseSensitive, ext: st.ext || '',
    min_size: st.minKB !== '' && Number(st.minKB) > 0 ? Math.round(Number(st.minKB) * 1024) : 0,
    max_size: st.maxKB !== '' && Number(st.maxKB) > 0 ? Math.round(Number(st.maxKB) * 1024) : 0,
    since_ms: st.since ? new Date(st.since + 'T00:00:00').getTime() : 0,
    until_ms: st.until ? new Date(st.until + 'T23:59:59').getTime() : 0,
    dir: st.dir || '', sort: st.sort, order: st.order
  };
}

// searchResetSelection 新查询等于新的结果集，旧的显式选择与全选凭据都必须作废。
function searchResetSelection(st){
  st.sel.clear(); st.selAll = false; st.selToken = ''; st.selCount = 0; st.excludes.clear();
}

async function doSearch(){
  const st = searchState();
  if (!st.q) {
    st.items = []; st.total = 0; st.nextCursor = '';
    searchResetSelection(st);
    renderSearchResults(); updateSearchInfo(); updateSearchButtons();
    return;
  }
  st.loading = true; updateSearchInfo();
  // v3.25.0（P1-6）：新查询要重置上一轮可能残留的 loadingMore。
  // 旧实现只在 loadMore 的 finally 里清，且要求 reqId 未变；上一轮 loadMore
  // 还在飞的时候用户又发起搜索，reqId 变了，finally 不生效，loadingMore 永久为 true，
  // 之后滚到底再也不加载。
  st.loadingMore = false;
  const reqId = (st.reqId || 0) + 1; st.reqId = reqId;
  searchResetSelection(st);
  st.items = []; st.nextCursor = '';
  try {
    const r = /** @type {SearchSQLResp} */ (await api('/api/search?' + searchParams(st, '').toString()));
    if (st.reqId !== reqId) return; // 只渲染最新一次请求，防止乱序覆盖
    st.items = r.items; st.total = Number(r.total || 0); st.took = Number(r.took_ms || 0);
    st.nextCursor = r.next_cursor || '';
    st.ready = !!r.ready; st.notice = r.notice || '';
  } catch(e) {
    if (st.reqId !== reqId) return;
    st.items = []; st.total = 0; st.nextCursor = ''; st.notice = '搜索失败：' + (e && e.message || e);
  }
  st.loading = false;
  renderSearchResults(); updateSearchInfo(); updateSearchButtons();
}

// loadMoreSearch 按游标拉下一批并追加；滚动到底时由 view.js 调用（v3.20.0）。
async function loadMoreSearch(){
  const st = searchState();
  if (st.loadingMore || !st.nextCursor || !st.q) return;
  st.loadingMore = true;
  const reqId = st.reqId;
  renderSearchWindow();
  try {
    const r = /** @type {SearchSQLResp} */ (await api('/api/search?' + searchParams(st, st.nextCursor).toString()));
    if (st.reqId !== reqId) return;
    const seen = new Set(st.items.map(x => x.path));
    r.items.forEach(it => { if (!seen.has(it.path)) { seen.add(it.path); st.items.push(it); } });
    st.nextCursor = r.next_cursor || '';
    st.total = Number(r.total || st.total);
    st.took = Number(r.took_ms || st.took);
  } catch(e) {
    if (st.reqId === reqId) { toast('加载更多失败：' + (e && e.message || e), 'err'); st.nextCursor = ''; }
  } finally {
    if (st.reqId === reqId) { st.loadingMore = false; renderSearchWindow(); updateSearchInfo(); }
  }
}

// searchSelCount 当前选择数：显式模式看集合，全选模式看总数减排除项。
function searchSelCount(){
  const st = searchState();
  return st.selAll ? Math.max(0, (st.selCount || 0) - st.excludes.size) : st.sel.size;
}

// searchSelectAll 全选/取消全选（v3.20.0）。
// 开启时向服务端换一个凭据（只记条件与总数），不把几十万条路径搬到前端；
// 配置关闭凭据模式时退回旧行为——只选已加载的行。
async function searchSelectAll(on){
  const st = searchState();
  if (!on) {
    searchResetSelection(st);
    renderSearchWindow(); updateSearchButtons();
    return;
  }
  const cfg = (S.status && S.status.config) || {};
  if (cfg.search_token_enabled === false) {
    st.items.forEach(it => { if (!badName(it)) st.sel.add(it.path); });
    renderSearchWindow(); updateSearchButtons();
    return;
  }
  try {
    const r = await postJSON('/api/search/selection', searchBody(st));
    if (!r.token) throw new Error('服务端未返回选择凭据');
    st.selAll = true; st.selToken = r.token; st.selCount = Number(r.count || 0);
    st.excludes.clear(); st.sel.clear();
    toast('已选中全部 ' + st.selCount + ' 条', 'ok');
  } catch(e) {
    // 索引未就绪等场景下退回只选已加载页，避免全选按钮完全失效
    st.items.forEach(it => { if (!badName(it)) st.sel.add(it.path); });
    toast('全选全部结果不可用，已改为选择已加载的 ' + st.sel.size + ' 条：' + (e && e.message || e), 'warn', 4000);
  }
  renderSearchWindow(); updateSearchButtons();
}

// searchToggleOne 勾选单行：全选模式下勾回=从排除集移除，取消=加入排除集。
function searchToggleOne(path, checked){
  const st = searchState();
  if (st.selAll) {
    if (checked) st.excludes.delete(path); else st.excludes.add(path);
  } else {
    if (checked) st.sel.add(path); else st.sel.delete(path);
  }
  updateSearchButtons();
}

// searchClearSel 清空全部选择（含全选凭据与排除项）。
function searchClearSel(){
  searchResetSelection(searchState());
  if ($('sSelAll')) $('sSelAll').checked = false;
  renderSearchWindow(); updateSearchButtons();
}

// addSelToRename 把选择加入重命名列表（v3.20.0 / v3.21.0）。
// 全选模式只记服务端凭据，不再把几十万条路径搬进浏览器内存；
// 显式勾选仍然只处理已加载且勾选的行。
async function addSelToRename(){
  const st = searchState(); const r = renameState();
  if (st.selAll) {
    if (!st.selToken) { toast('选择已失效，请重新全选', 'err'); return; }
    r.token = { token: st.selToken, excludes: Array.from(st.excludes), count: searchSelCount() };
    r.planID = 0; r.planSig = '';
    renderRenamePanel();
    toast('已加入重命名列表（全部 ' + r.token.count.toLocaleString() + ' 条，执行时由服务端分批处理）', 'ok');
    return;
  }
  const exist = new Set(r.list.map(x => x.path));
  let added = 0;
  st.items.forEach(it => {
    if (!st.sel.has(it.path) || exist.has(it.path)) return;
    exist.add(it.path);
    r.list.push({ path: it.path, orig: it.name, target: it.name, state: '待执行' });
    added++;
  });
  renderRenamePanel();
  toast(added ? ('已加入 ' + added + ' 个文件') : '所选文件都已在列表里', added ? 'ok' : 'err');
}

// 搜索页表头排序走服务端：点击列头只改查询条件并重查，不排 DOM（v3.20.0）。
TABLE_SORT_HOOKS.search = (ts) => {
  const map = { 'sr-name':'name', 'sr-path':'path', 'sr-ext':'ext', 'sr-size':'size', 'sr-time':'mtime', 'sr-vol':'volume' };
  const key = map[ts.col];
  if (!key) return;
  const st = searchState();
  const order = ts.dir === 1 ? 'asc' : 'desc';
  if (st.sort === key && st.order === order) return;
  st.sort = key; st.order = order;
  if (st.q) doSearch();
};

async function applyRenameRules(){
  const r = renameState(); if (!r.list.length) return;
  const rules = {
    prefix: ($('rPrefix')||{}).value || '',
    suffix: ($('rSuffix')||{}).value || '',
    start: Number(($('rStart')||{}).value || 0),
    digits: Number(($('rDigits')||{}).value || 2),
    replace_from: ($('rFrom')||{}).value || '',
    replace_to: ($('rTo')||{}).value || '',
    case: ($('rCase')||{}).value || '',
    trim: !!($('rTrim') && $('rTrim').checked),
  };
  try {
    const res = await postJSON('/api/rename/preview', { files: r.list.map(x => ({ path: x.path, name: '' })), rules });
    (res.items || []).forEach((it, i) => { if (r.list[i]) r.list[i].target = it.target; });
    renderRenamePanel();
    toast('已按规则生成新名（未执行）', 'ok');
  } catch(e) { toast('生成失败：' + (e && e.message || e), 'err'); }
}

async function executeRenameBatch(){
  const r = renameState(); if (!r.list.length) {
    // v3.21.0：全选条目没有逐条目标名，必须到重命名页配方法链后走两阶段执行。
    if (r.token) { toast('已加入全选结果，请到「重命名」页配置方法链后执行', 'warn'); await switchTab('rename'); return; }
    return;
  }
  if (!confirm('确认执行 ' + r.list.length + ' 个文件的改名？失败项会逐条标注。')) return;
  try {
    const res = await postJSON('/api/rename/execute', { files: r.list.map(x => ({ path: x.path, name: x.target })), rules: {} });
    r.batchID = Number(res.batch_id || 0);
    (res.items || []).forEach((it, i) => { if (r.list[i]) r.list[i].state = it.error ? ('失败：' + it.error) : '已完成'; });
    renderRenamePanel();
    const h = $('renameHint'); if (h) h.textContent = '完成 ' + (res.done||0) + ' 个，失败 ' + (res.failed||0) + ' 个，批次 ' + r.batchID;
    toast('改名完成：成功 ' + (res.done||0) + '，失败 ' + (res.failed||0), (res.failed ? 'err' : 'ok'));
    showReconcile(res.reconcile);
  } catch(e) { toast('执行失败：' + (e && e.message || e), 'err'); }
}

// v3.10.4：撤销失败时把首条原因带出来，用户才知道是「原文件名已被占用」而不是笼统失败。
function undoFailReason(r){
  const bad = ((r && r.items) || []).find(x => x && x.error);
  return bad ? '（' + bad.error + '）' : '';
}

// v3.10.12：批次撤销三态（undone / undone_partial / undone_failed）统一转中文与提示颜色。
// 后端从 v3.10.12 起在 undo 响应里带 status；老包没有该字段时按 done/failed 推导，
// 避免「部分撤销」被当成「撤销完成」显示成绿色。type 直接作为 toast 的 class。
function undoOutcome(res){
  const done = Number(res && res.done) || 0;
  const failed = Number(res && res.failed) || 0;
  let status = String((res && res.status) || '');
  if (status !== 'undone' && status !== 'undone_partial' && status !== 'undone_failed') {
    status = failed === 0 ? 'undone' : (done === 0 ? 'undone_failed' : 'undone_partial');
  }
  const reason = undoFailReason(res);
  if (status === 'undone') return { status, type:'ok', text:`撤销完成：${done} 个文件已恢复原名` };
  if (status === 'undone_failed') return { status, type:'err', text:`撤销失败：${failed} 个文件均未能撤回${reason}` };
  return { status, type:'warn', text:`部分撤销：${done} 个已恢复原名，${failed} 个未能完全撤回${reason}` };
}

// markUndoRows 把撤销结果逐条写回重命名面板的状态列。
// items[].path 是改名前的原路径（后端 HealthOpItem.Path = 审计里的 OriginalPath），与清单的 path 一致。
// 此前只有整批成功才标记，部分失败时失败行仍停在「已完成」，看起来像撤销成功。
function markUndoRows(list, res){
  const bad = new Map();
  ((res && res.items) || []).forEach(x => { if (x && x.path) bad.set(x.path, String(x.error || '未撤回')); });
  const done = Number(res && res.done) || 0;
  (list || []).forEach(x => {
    if (bad.has(x.path)) x.state = '未撤回：' + bad.get(x.path);
    else if (done > 0 && x.state === '已完成') x.state = '已撤销';
  });
}

// undoRenameBatch 逆序撤销本次执行的批次（v3.22.0）。
// r.batchIDs 记录了两阶段执行产生的每一片批次；只撤最后一片会留下一半旧名。
async function undoRenameBatch(){
  const r = renameState();
  const ids = (r.batchIDs && r.batchIDs.length) ? r.batchIDs.slice() : (r.batchID ? [r.batchID] : []);
  if (!ids.length) return;
  if (!confirm('撤销本次改名的 ' + ids.length + ' 个批次？会按逆序把文件改回原名。')) return;
  try {
    const res = await postJSON('/api/rename/undo-batch', { batch_ids: ids });
    markUndoRows(r.list, res);
    r.batchIDs = []; r.batchID = 0; r.recentLoaded = false;
    renderRenamePanel();
    if (S.tab === 'rename') renderRenamePage();
    const out = undoOutcome(res);
    toast(out.text, out.type, 4000);
  } catch(e) { toast('撤销失败：' + (e && e.message || e), 'err'); }
}

// undoRenameOne 撤销「最近批次」里的单个批次。
async function undoRenameOne(id){
  if (!confirm('撤销批次 #' + id + '？')) return;
  try {
    const res = await postJSON('/api/rename/undo-batch', { batch_ids: [id] });
    const out = undoOutcome(res);
    toast(out.text, out.type, 4000);
    const r = renameState();
    r.recentLoaded = false;
    r.batchIDs = (r.batchIDs || []).filter(x => x !== id);
    if (r.batchID === id) r.batchID = 0;
    renderRenamePage();
  } catch(e) { toast('撤销失败：' + (e && e.message || e), 'err'); }
}

// loadRecentRenameBatches 关页面后重新拿到撤销入口（v3.22.0）。
async function loadRecentRenameBatches(){
  const r = renameState();
  if (r.recentLoading) return;
  r.recentLoading = true;
  try { const res = /** @type {RenameBatchesResp} */ (await api('/api/rename/batches/recent?limit=10')); r.recent = res.batches || []; }
  catch(e) { r.recent = []; }
  finally { r.recentLoading = false; if (S.tab === 'rename') renderRenamePage(); }
}

function renameBatchStatusText(s){
  return ({done:'已完成', partial:'部分执行', failed:'失败', running:'进行中',
    undone:'已撤销', undone_partial:'部分撤销', undone_failed:'撤销失败',
    repaired:'已修复', repaired_partial:'部分修复'})[s] || s;
}

// renameRequestCancel 通知服务端在片边界停下（v3.22.0）。
// 正在跑的 2000 条会跑完，剩余 pending 保留，下次执行可续跑。
function renameRequestCancel(){
  const r = renameState();
  if (!r.planID) return;
  postJSON('/api/rename/cancel', { plan_id: r.planID }).catch(() => {});
}
