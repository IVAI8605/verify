// 查重助手 verify Web UI · 入口与事件分发（v3.0.0 从 app.js 拆分）
// 加载顺序第 6（最后）：全局状态入口、tab 切换、document/window 事件监听与 boot()。
// boot() 放在本文件末尾，确保前五个脚本的函数都已声明后再启动。

const S = {
  tab: 'overview',
  status: null,
  // v3.7.0 操作记录页状态（筛选条件与分页）
  opLog: { page:1, pageSize:50, items:[], total:0, action:'', result:'', keyword:'' },
  // v3.10.13：操作记录页的第二视图（写操作快照）；接口未就位时 error 会写进这里
  opView: 'log',
  // v3.10.13：写操作快照分页按后端定稿——page 默认 1、page_size 默认 20（上限 100）；
  // result 取值 all / ok / mismatch，默认 all
  opSnap: { page:1, pageSize:20, items:[], total:0, kind:'', result:'all', keyword:'', error:'' },
  // 2026-11-08：未完成改名批次（崩溃残留），操作记录页提示条与导航角标共用
  unfinished: [],
  // v3.10.12：部分撤销/撤销失败的批次。v3.10.13 起主数据源是后端 residual 段（跨刷新保留），
  // 这里的本地列表只在老包没有 residual 时兜底。
  unfinResidual: [],
  // v3.13.0（UI 审查 P3-7）：目录收窄默认只显示前 8 个，用户点「更多目录」后展开全部；
  // 换快照时重置，避免把上一轮展开状态带到新目录集合上。
  dirsExpanded: false,
  // v3.13.0（UI 审查 P3-8）：重扫就绪后收窄统计重取期间显示固定占位，避免目录条闪烁。
  facetsLoading: false,
  photoGroups: [],
  photoSel: new Set(),
  listFilter: { marked:false, unmarked:false, partial:false, allMarked:false, protected:false,
    countMin:'', countMax:'', sizeMin:'', sizeMax:'', fileMin:'', fileMax:'', showAll:true, open:false,
    // v1.25.0：原顶栏快捷筛选六维度并入（服务端过滤参数源 + 前端收窄）
    minKB:'', maxKB:'', exts:'', q:'', since:'', until:'',
    // v2.4.0：高级过滤补齐
    dateOnly:false, sameDrive:'', maxDurDiff:'', ignoreSameFolder:false, advOpen:false,
    folderName:'', folderDepth:'', similarName:false, sizeTolKB:'',
    // v3.4.0：标记另一台设备也有的文件（需先在设置页导入指纹包）
    crossDevice:false,
    // v2.9.0：按标签值过滤
    tagName:'', tagValue:'' },
  sort: 'waste',
  sortOrder: 'desc',
  tcTasks: [],
  tcEncoder: '',
  rpCollapsed: new Set(),   // v1.24.0 结果组折叠（key = tab:gkey；folder 默认折叠）
  asecFold: {},             // v1.24.0 助手各段独立折叠 {段标题:bool}
  rpSort: {},               // v1.24.0 各结果页排序键（folder/audio/photo 分开记忆）
  rpSortOrder: {},
  tcQuality: 'balanced',
  tcMaxRes: '',
  tcSel: new Set(),
  photoThreshold: 10,
  audioGroups: [],
  audioSel: new Set(),
  folderGroups: [],
  folderSel: new Set(),
  bigFiles: [],
  bigSel: new Set(),
  bigOrder: 'big',
  page: 1,
  pageSize: 100,
  loadedPage: 1,
  hasMore: false,
  loadingMore: false,
  vlist: null,
  prefetch: null,
  prefetching: false,
  prefetchScanAt: 0,
  scanRunning: false,
  scanDoneAt: 0,
  scanFinishAt: 0,
  wizard: null,
  jobTabs: [],
  collapsed: new Set(),
  colWidths: null,
  // v3.11.1：null 表示「结果总数尚未拿到」，与「确实为 0 组」区分开。
  // 「全部」chip 只有拿到真实数字才用它（0 也照用），否则回退到 totals.groups。
  total: null,
  groups: [],
  sel: {},
  undo: [],
  assistOpen: true,
  assistGroups: null,
  assistPromise: null,
  assistLoading: false,
  assistTruncated: false,
  genThumbs: false,
  modalAction: null,
  // v3.7.2：弹窗内正在执行不可取消的请求，此时禁止关闭弹窗（P-21）
  modalBusy: false,
  // v3.8.0：合并结果页的类型筛选与规则搜索快照状态
  resultType: 'all',
  // v3.10.13：收窄口径（按类型/目录计数）与当前目录筛选；facets 未就位时保持 null
  facets: null,
  // 无快照时 facets 返回 409，这里存「暂不可用」的说明文字，空串表示正常
  facetsDown: '',
  dirFilter: '',
  // v3.11.1：结果列表请求代次。切换类型/目录/关键字/排序时自增，
  // 在飞的旧请求回来时代次对不上就丢弃（防止旧类别数据追加进新列表）
  ruleGen: 0,
  // 上一次的满载状态，用来避免每次滚动都重绘 facets 条（v3.10.13）
  _facetsCapped: false,
  ruleSearch: null,
  pendingRuleSearch: false,
  dir: null,
  scopeSelOnly: false,
  scopeUnmarkedOnly: false,
  rowSel: new Set(),
  keepCriterion: 'quality',
  keepPaths: [],
  keepPathDraft: '',
  textPattern: '',
  textField: 'name',
  textRegex: false,
  textCase: false,
  textWhole: false,
  prefs: [
    {key:'resolution', dir:'max', on:true},
    {key:'size', dir:'max', on:true},
    {key:'duration', dir:'max', on:false},
    {key:'mtime', dir:'max', on:false},
    {key:'ctime', dir:'max', on:false},
    {key:'bitrate', dir:'max', on:false},
    {key:'samplerate', dir:'max', on:false},
    {key:'depth', dir:'min', on:false},
    {key:'name', dir:'max', on:false}
  ],
  trash: [],
  trashSel: new Set(),
  videoFails: [],
  showFails: false,
  // v3.2.0：定时清理计划列表与当前编辑的条目
  cleanupPlans: [],
  cleanupEditID: 0,
  introN: null,
  introAnalyzed: 0,
  introPending: 0,
  // v3.16.0：结果页片头计数上次拉取时间（毫秒），30 秒内复用，避免切排序反复请求。
  introStatsAt: 0,
  filters: { minKB: '', ext: '', q: '' },
  loading: false
};

let colDrag = null;

document.addEventListener('pointerdown', (ev) => {
  const handle = ev.target.closest('.colresize');
  if (!handle || ev.button !== 0 || colDrag) return;
  const table = handle.closest('table.rtable');
  if (!table) return;
  const col = table.querySelector(`col[data-col="${handle.dataset.col}"]`);
  if (!col) return;
  ev.preventDefault();
  colDrag = {
    id: handle.dataset.col, layout: colLayoutKey(),
    startX: ev.clientX, startW: col.getBoundingClientRect().width, width: 0, moved: false
  };
  document.body.classList.add('col-resizing');
  try { handle.setPointerCapture(ev.pointerId); } catch(e) {}
});

document.addEventListener('pointermove', (ev) => {
  if (!colDrag) return;
  const dx = ev.clientX - colDrag.startX;
  if (Math.abs(dx) > 1) colDrag.moved = true;
  const w = Math.max(40, Math.round(colDrag.startW + dx));
  colDrag.width = w;
  setColWidth(colDrag.layout, colDrag.id, w);
});

document.addEventListener('pointerup', endColDrag);

document.addEventListener('pointercancel', endColDrag);

document.addEventListener('dblclick', (ev) => {
  const handle = ev.target.closest('.colresize');
  if (!handle) return;
  const layout = colLayoutKey();
  const m = loadColWidths();
  if (m[layout]) { delete m[layout][handle.dataset.col]; saveColWidths(); }
  const cols = columnsForTab(S.tab);
  const def = cols.find(c => c.id === handle.dataset.col);
  applyColWidth(handle.dataset.col, def ? def.width : 0);
  requestAnimationFrame(measureVirtualGroups);
});

// v4.5.0：侧栏 5 个主入口 + 「更多功能」，底部导航同源高亮。
// 低频页打开时自动展开「更多功能」，并把所属主入口一起点亮，用户不会迷路。
function syncNavActive(name){
  const btn = document.querySelector(`.tab[data-tab="${name}"]`);
  // data-minor / data-main 都是无值布尔属性，dataset 取到空字符串；必须用 hasAttribute。
  const minor = !!(btn && btn.hasAttribute('data-minor'));
  document.querySelectorAll('.tab').forEach(t => {
    const hit = t.dataset.tab === name || (minor && t.hasAttribute('data-main') && t.dataset.tab === btn.dataset.parent);
    t.classList.toggle('active', !!hit);
  });
  document.querySelectorAll('.bottomnav button').forEach(b => {
    const hit = b.dataset.tab === name || (minor && b.dataset.tab === btn.dataset.parent);
    b.classList.toggle('active', hit);
  });
  if (minor) {
    const more = $('navMore'), tog = $('navMoreToggle');
    if (more) more.hidden = false;
    if (tog) { tog.setAttribute('aria-expanded', 'true'); tog.textContent = '收起更多功能'; }
  }
}

async function switchTab(name){  // v3.8.0：旧结果标签统一映射到「结果」页 + 类型筛选，保证书签/深链仍可用。
  // v3.9.5：写入 URL hash，刷新/前进后退保留当前页。
  const legacyKind = {
    exact:'exact', samevideo:'samevideo', name:'name', video:'video', intro:'intro',
    photo:'photo', audio:'audio', folder:'folder', fuzzy:'fuzzy', semantic:'semantic'
  }[name];
  if (legacyKind) { S.resultType = legacyKind; name = 'results'; }
  if (!tabEnabled(name)) { toast('本次查重未启用该类型', 'err'); return; }
  S.tab = name; S.page = 1; S.loading = false; S.genThumbs = false;
  S.rowSel.clear(); rowAnchor = null;
  resetAssistCache();
  // 写 hash：避免 history 栈堆积，replaceState 足够
  try { history.replaceState(null, '', '#' + name); } catch(e) {}
  document.body.dataset.tab = name;
  syncNavActive(name);
  document.body.classList.remove('nav-open');
  const activeTab = document.querySelector(`.tab[data-tab="${name}"]`) || document.querySelector('.tab.active');
  if (activeTab && $('screenTitle')) $('screenTitle').textContent = activeTab.textContent.trim();
  const sh = $('screenHelp');
  if (sh) {
    const sec = TAB_HELP[name] || '';
    sh.style.display = sec ? '' : 'none';
    if (sec) sh.dataset.sec = sec;
  }
  syncScanBtn(name);
  const navToggle = $('navToggle');
  if (navToggle) navToggle.setAttribute('aria-expanded', 'false');
  if (name === 'overview') { const st = await refreshStatus(); renderOverview(st); return; }
  if (name === 'help') { renderHelp(); return; }
  if (name === 'results') { await loadRuleResults(true); return; }
  if (name === 'trash') { await loadTrash(); return; }
  if (name === 'import') { S.importDetail = null; await loadImport(); return; }
  if (name === 'search') { renderSearchPage(); return; }
  if (name === 'rename') { loadRenamePresets(); renderRenamePage(); return; }
  if (name === 'settings') { await refreshStatus(); renderSettings(); return; }
  if (name === 'oplog') { await loadOpLog(); return; }
  if (name === 'health') { healthState().pruned = false; await loadHealth(); return; }
  if (name === 'video') { await loadVideo(); return; }
  if (name === 'intro') { await loadIntro(); return; }
  if (name === 'photo') { await loadPhoto(); return; }
  if (name === 'audio') { await loadAudio(); return; }
  if (name === 'folder') { await loadFolder(); return; }
  if (name === 'fuzzy') { await loadFuzzy(); return; }
  if (name === 'samevideo') { await loadGroups('samevideo'); return; }
  if (name === 'semantic') { await loadSemantic(); return; }
  if (name === 'big') { await loadBig(); return; }
  if (name === 'transcode') { await loadTranscode(); return; }
  await loadGroups(name);
}

async function refreshView(){
  if (S.tab === 'overview') { const st = await refreshStatus(); renderOverview(st); }
  else if (S.tab === 'help') renderHelp();
  // v3.10.0 UI-01：合并结果页补进刷新入口，否则 SSE 全页签刷新会误走 loadGroups('results')
  else if (S.tab === 'results') await loadRuleResults(true);
  else if (S.tab === 'trash') await loadTrash();
  else if (S.tab === 'import') await loadImport();
  else if (S.tab === 'search') renderSearchPage();
  else if (S.tab === 'rename') { loadRenamePresets(); renderRenamePage(); }
  else if (S.tab === 'settings') { await refreshStatus(); renderSettings(); }
  else if (S.tab === 'health') await loadHealth();
  // v3.17.0：旧类型标签已由 switchTab 统一映射为 results，S.tab 不会再取这些值。
  else if (S.tab === 'big') await loadBig();
  else if (S.tab === 'transcode') await loadTranscode();
  else if (S.tab === 'oplog') await loadOpLog();
  else await loadGroups(S.tab);
}

document.addEventListener('change', (ev) => {
  const el = ev.target.closest('[data-act]');
  if (!el) return;
  if (el.dataset.act === 'check') {
    const key = el.dataset.key, p = el.dataset.path;
    if (!S.sel[key]) S.sel[key] = new Set();
    if (el.checked) S.sel[key].add(p); else S.sel[key].delete(p);
    readAssistUI();
    updateSelHint();
    renderAssistant();
  }
  if (el.dataset.act === 'trash-check') {
    const id = Number(el.dataset.id);
    if (el.checked) S.trashSel.add(id); else S.trashSel.delete(id);
  }
});

document.addEventListener('change', async (ev) => {
  const t = ev.target;
  // 下拉框统一在 change 时取值：click 阶段重绘会把原生下拉菜单直接销毁
  // （新建查重的匹配模式就是因为这个原因“一闪而过”）。
  if (t.dataset && t.dataset.act === 'wiz-preset') {
    if (!S.wizard || t.value === '') return;
    const presets = (((S.status || {}).config || {}).search_presets) || [];
    const p = presets[Number(t.value)];
    if (!p) return;
    S.wizard.rules = JSON.parse(JSON.stringify(p.rules));
    applyWizardRulesBases(S.wizard);
    renderWizard();
    return;
  }
  if (t.dataset && t.dataset.act === 'wiz-base') {
    if (!S.wizard) return;
    readWizardInputs();
    S.wizard.rules.base = t.value;
    applyWizardRulesBases(S.wizard);
    renderWizard();
    return;
  }
  if (t.dataset && t.dataset.act === 'wiz-attr') {
    if (!S.wizard) return;
    readWizardInputs();
    renderWizard();
    return;
  }
  if (t.dataset && t.dataset.act === 'lf-input') { applyLfInput(t); return; }
  if (t.dataset && (t.dataset.act === 'assist-sort' || t.dataset.act === 'assist-sort-order')) {
    // 选择助手排序是服务端全局排序，修改任一下拉立即重拉（P-05）
    S.sort = $('aSort') ? $('aSort').value : 'waste';
    S.sortOrder = $('aSortOrder') ? $('aSortOrder').value : 'desc';
    await refreshView();
    return;
  }
  if (t.dataset && t.dataset.act === 'rp-sort') {
    S.rpSort[t.dataset.page] = t.value;
    // 排序字段本身也参与服务端查询，必须立即重拉，不能只改控件显示（P-05）
    if (S.tab === 'folder') await loadFolder();
    else if (S.tab === 'audio') await loadAudio();
    else if (S.tab === 'photo') await loadPhoto();
    return;
  }
  if (t.dataset && t.dataset.act === 'rp-sort-order') {
    S.rpSortOrder[t.dataset.page] = t.value;
    if (S.tab === 'folder') await loadFolder();
    else if (S.tab === 'audio') await loadAudio();
    else if (S.tab === 'photo') await loadPhoto();
    return;
  }
  if (t.id === 'aScopeSel') S.scopeSelOnly = t.checked;
  else if (t.id === 'aScopeUnmarked') S.scopeUnmarkedOnly = t.checked;
  else if (t.id === 'aKeepCrit') S.keepCriterion = t.value;
  else if (t.id === 'aTextRegex') S.textRegex = t.checked;
  else if (t.id === 'aTextCase') S.textCase = t.checked;
  else if (t.id === 'aTextWhole') S.textWhole = t.checked;
  else if (t.id === 'aTextField') S.textField = t.value;
  else if (t.matches('[data-pref-on],[data-pref-dir]')) readAssistUI();
  else if (t.dataset.act === 'search-check') {
    // v3.20.0：全选模式下取消勾选只往排除集里加一条，不物化整份路径。
    searchToggleOne(t.dataset.path, t.checked);
  } else if (t.dataset.act === 'rename-target') {
    const r = renameState();
    const i = Number(t.dataset.idx);
    if (r.list[i]) r.list[i].target = t.value;
  } else if (t.id === 'tagSource') {
    // 只有选"文件名规则"时才显示正则输入框，避免用户填一个用不上的字段
    const f = document.getElementById('tagPatternField');
    if (f) f.style.display = t.value === 'filename_regex' ? '' : 'none';
  } else if (t.id === 'sSleepFriendly') {
    // v1.18 修复①：休眠友好开启时置灰定时重扫并说明（方案 a，R1）
    const inp = document.getElementById('sRescan'), help = document.getElementById('sRescanHelp');
    if (inp) { inp.disabled = t.checked; inp.style.opacity = t.checked ? '.5' : '1'; }
    if (help) help.textContent = t.checked ? '硬盘休眠友好已开启，定时全量重扫不运行（实时监控照常）' : '0 表示关闭，实时监控照常工作';
  }
});

document.addEventListener('input', (ev) => {
  const t = ev.target;
  if (t.id === 'aText') S.textPattern = t.value;
  else if (t.id === 'aKeepPath') { S.keepPathDraft = t.value; applyAssistWidth(); }
  else if (t.dataset && t.dataset.act === 'filter-num') {
    S.listFilter[t.dataset.k] = t.value;
    clearTimeout(window.__ft);
    window.__ft = setTimeout(() => refreshView(), 400);
  } else if (t.dataset && t.dataset.act === 'lf-input' && t.tagName !== 'SELECT') {
    applyLfInput(t);
  }
});

let prefDragIndex = null;

document.addEventListener('dragstart', (ev) => {
  const el = ev.target.closest('[data-pref-index]');
  if (!el) return;
  prefDragIndex = Number(el.dataset.prefIndex);
  el.classList.add('dragging');
  if (ev.dataTransfer) { ev.dataTransfer.effectAllowed = 'move'; ev.dataTransfer.setData('text/plain', String(prefDragIndex)); }
});

document.addEventListener('dragend', (ev) => {
  const el = ev.target.closest('[data-pref-index]');
  if (el) el.classList.remove('dragging');
  prefDragIndex = null;
});

document.addEventListener('dragover', (ev) => {
  if (prefDragIndex != null && ev.target.closest('#aPrefs')) ev.preventDefault();
});

document.addEventListener('drop', (ev) => {
  const el = ev.target.closest('[data-pref-index]');
  if (prefDragIndex == null || !el) return;
  ev.preventDefault();
  readAssistUI();
  const from = prefDragIndex, to = Number(el.dataset.prefIndex);
  if (Number.isFinite(from) && Number.isFinite(to) && from !== to) {
    const [item] = S.prefs.splice(from, 1);
    S.prefs.splice(to, 0, item);
  }
  prefDragIndex = null;
  renderAssistant();
});

function loadedRowPaths(){
  const out = [];
  (S.groups || []).forEach(g => (g.files || []).forEach(f => out.push(f.path)));
  return out;
}

let rowAnchor = null;

document.addEventListener('click', (ev) => {
  const tr = ev.target.closest('tr[data-path]');
  if (!tr) return;
  if (ev.target.closest('input,button,img,a,label')) return;
  const rows = Array.from(document.querySelectorAll('tr[data-path]'));
  const path = tr.dataset.path;
  const shift = ev.shiftKey;
  const multi = ev.ctrlKey || ev.metaKey;
  if (shift && rowAnchor) {
    const all = loadedRowPaths();
    const i = all.indexOf(path);
    const j = all.indexOf(rowAnchor);
    if (i >= 0 && j >= 0) {
      const range = all.slice(Math.min(i, j), Math.max(i, j) + 1);
      const next = multi ? new Set(S.rowSel) : new Set();
      range.forEach(p => next.add(p));
      S.rowSel = next;
    } else {
      S.rowSel = new Set([path]);
    }
  } else if (multi) {
    if (S.rowSel.has(path)) S.rowSel.delete(path); else S.rowSel.add(path);
    rowAnchor = path;
  } else {
    S.rowSel = new Set([path]);
    rowAnchor = path;
  }
  rows.forEach(r => r.classList.toggle('rowsel', S.rowSel.has(r.dataset.path)));
  updateRowPathBtn();
});

document.addEventListener('click', async (ev) => {
  const el = ev.target.closest('[data-act]');
  if (!el) return;
  const act = el.dataset.act;
  // v3.10.5：扫描/检测进行中直接拦下写操作（按钮已置灰，这里兜住旧 DOM 与键盘触发）。
  if (OPS_GUARD_ACTS.has(act)) {
    const blocked = opsBlockedReason();
    if (blocked) { toast(blocked, 'err'); return; }
  }
  // 原生 select：单击只是展开选项，任何重绘都会把下拉菜单直接销毁。
  // 下拉取值统一交给 change 事件（见上方 change 监听）。
  if (el.tagName === 'SELECT') return;
  if (act === 'check' || act === 'trash-check') return;
  if (act === 'modal-cancel') { closeModal(); return; }
  if (act === 'modal-confirm') {
    const fn = S.modalAction;
    S.modalAction = null;
    if (fn) { try { await fn(); } catch(e) { toast(e.message, 'err'); } }
    return;
  }
  if (act === 'big-crop') {
    S.bigCrop = el.value; S.bigCrop = S.bigCrop === 'all' ? '' : S.bigCrop; S.bigSel = new Set();
    await loadBig(); return;
  }
  if (act === 'big-cropdetect') {
    // v3.19.0：改为分批 + 进度卡 + 暂停 / 取消，循环逻辑收敛到 api.js。
    await bigCropdetectRun(); return;
  }
  if (act === 'retain-suggest') { await retainSuggest(); return; }
  if (act === 'scan-del') {
    S.scanPaths = (S.scanPaths || []).filter(v => v !== el.dataset.path);
    renderScanList(); return;
  }
  if (act === 'scan-clear') {
    if (!(S.scanPaths || []).length) return;
    if (!confirm('清空全部扫描目录？保存后目录树将为空。')) return;
    S.scanPaths = []; renderScanList(); return;
  }
  // v3.10.0：不扫描/受保护文件夹支持目录树多选，省得手打长路径
  if (act === 'pick-excl-dirs') { openDirPicker('excludeDir', '', pathLinesOf('sExclPaths')); return; }
  if (act === 'pick-protect-dirs') { openDirPicker('protectDir', '', pathLinesOf('sProtect')); return; }
  if (act === 'excl-clear' || act === 'protect-clear') {
    const box = $(act === 'excl-clear' ? 'sExclPaths' : 'sProtect');
    if (!box || !box.value.trim()) return;
    if (!confirm('清空这个列表？点「保存设置」后才会真正生效。')) return;
    box.value = '';
    return;
  }
  if (act === 'pref-clear') {
    if (!confirm('确认清空全部偏好学习数据？')) return;
    try { const r = await postJSON('/api/retain/pref/clear', {}); toast('已清空 ' + (r.cleared || 0) + ' 条', 'ok'); loadPrefStats(); } catch(e) { toast(e.message, 'err'); }
    return;
  }
  if (act === 'retain-ai') {
    const groups = (S.groups || []).filter(g => (g.files || []).length >= 2).slice(0, 50);
    if (!groups.length) { toast('当前页没有可分析的重复组', 'err'); return; }
    try {
      const r = await postJSON('/api/retain/explain', { groups: groups.map(g => ({
        key: g.key,
        files: (g.files || []).map(f => ({ path: f.path, name: f.name, size: f.size, width: f.width || 0, height: f.height || 0, bit_rate: f.bit_rate || 0, duration: f.duration || 0, mtime: f.mtime || 0 }))
      })) });
      const reasonByKey = {};
      (r.reasons || []).forEach(x => { reasonByKey[x.key] = x.reason; });
      const sugByKey = {};
      (S.retainSugs || []).forEach(x => { sugByKey[x.key] = x; });
      const rows = groups.map(g => {
        const sug = sugByKey[g.key];
        const keep = sug ? sug.suggestion.retain : '';
        const reason = reasonByKey[g.key] || '（AI 未返回）';
        return '<tr><td class="mono">' + esc((keep.split('/').pop() || '')) + '</td><td>' + esc(reason) + '</td></tr>';
      }).join('');
      setModal('AI 生成的保留理由（' + (r.reasons || []).length + ' 组）',
        '<div class="hint" style="margin-bottom:8px">仅用于解释，不改变建议与选择标记；AI 不可用时规则理由仍然可用。</div>' +
        '<div class="preview-list" style="max-height:50vh;overflow:auto"><table><thead><tr><th>建议保留</th><th>AI 理由</th></tr></thead><tbody>' + rows + '</tbody></table></div>' +
        '<div class="form-actions" style="margin-top:10px"><button class="btn ghost" data-act="modal-cancel">关闭</button></div>', null);
    } catch(e) { toast('AI 不可用，已保留规则理由：' + (e && e.message || e), 'err', 5000); }
    return;
  }
  if (act === 'retain-apply') {
    S.retainApplied = (S.retainSugs || []).map(sg => ({ group_key: sg.key, suggested: sg.suggestion.retain }));
    // 应用建议：把每组非保留文件加入选择标记（标记通道，助手撤销栈可回退）
    let n = 0;
    (S.retainSugs || []).forEach(sg => {
      const g = (S.groups || []).find(x => x.key === sg.key);
      if (!g) return;
      S.sel[sg.key] = S.sel[sg.key] || new Set();
      // v3.11.1：保留建议只把可操作文件放进选择集，编码异常行照常展示但不可选
      operableFiles(g).forEach(f => {
        if (f.path !== sg.suggestion.retain) { S.sel[sg.key].add(f.path); n++; }
      });
    });
    renderAssistant(); syncCheckboxes(); renderGroups();
    toast('已按建议标记 ' + n + ' 个文件（可撤销选择），确认后走隔离流程', 'ok');
    closeModal();
    return;
  }
  if (act === 'open-folder-rename') { openDirPicker('folder_rename', ''); return; }
  if (act === 'fr-prefix') { S.frPrefixA = $('frPrefixA').checked; await frPreview(); return; }
  if (act === 'fr-apply') {
    if (!S.frDir) return;
    if (!confirm('确认执行改名？冲突自动加序号，操作可在弹窗内撤销。')) return;
    try {
      const r = await postJSON('/api/folder_rename', { dir: S.frDir, prefix_a: S.frPrefixA });
      S.frTask = r.task_id || 0;
      frRenderResult(r);
      toast('已改名 ' + (r.rename_count || 0) + ' 项', 'ok');
    } catch(e) { toast(e.message, 'err'); }
    return;
  }
  if (act === 'fr-undo') {
    if (!S.frTask) { toast('没有可撤销的任务', 'err'); return; }
    try {
      const r = await postJSON('/api/folder_rename/undo', { task_id: S.frTask });
      toast('已撤销 ' + (r.done || 0) + ' 项' + (r.failed ? '，失败 ' + r.failed + ' 项' : ''), r.failed ? 'err' : 'ok');
      S.frTask = 0;
      closeModal();
    } catch(e) { toast(e.message, 'err'); }
    return;
  }
  // v3.7.0 操作记录页交互
  if (act === 'oplog-search') { S.opLog.page = 1; await loadOpLog(); return; }
  // v3.10.13：操作记录 / 写操作快照 两个视图切换
  if (act === 'op-view') {
    S.opView = el.dataset.view === 'snap' ? 'snap' : 'log';
    if (S.opView === 'snap') await loadOpSnapshots(); else await loadOpLog();
    return;
  }
  if (act === 'snap-search') {
    const st = opSnapState();
    st.keyword = (($('snapKeyword') && $('snapKeyword').value) || '').trim();
    st.kind = ($('snapKind') && $('snapKind').value) || '';
    st.result = ($('snapResult') && $('snapResult').value) || '';
    st.page = 1;
    await loadOpSnapshots();
    return;
  }
  if (act === 'snap-page') {
    const st = opSnapState();
    st.page = Math.max(1, Number(st.page || 1) + Number(el.dataset.delta || 0));
    await loadOpSnapshots();
    return;
  }
  if (act === 'oplog-page') {
    S.opLog.page = Math.max(1, S.opLog.page + Number(el.dataset.delta || 0));
    await loadOpLog(); return;
  }
  if (act === 'oplog-export') { await exportOpLog(); return; }
  if (act === 'oplog-clear') { await clearOpLog(); return; }
  // 2026-11-08：撤销崩溃残留的改名批次（入口在操作记录页顶部提示条）
  if (act === 'unfinished-undo') { await undoUnfinishedBatch(Number(el.dataset.id || 0)); return; }
  // v3.10.12：用户确认看过「没撤干净」的残项后手动移除，避免它一直占着提示条与角标。
  // v3.10.13：residual 改由后端持续返回，必须同时写 localStorage 忽略集合，否则刷新就回来。
  if (act === 'unfin-residual-dismiss') {
    const id = Number(el.dataset.id || 0);
    unfinDismiss(id);
    S.unfinResidual = (S.unfinResidual || []).filter(b => Number(b.batch_id) !== id);
    refreshUnfinishedMarks();
    syncUnfinishedBar();
    return;
  }
  if (act === 'pick-log-dir') { openDirPicker('log', $('sLogDir') ? $('sLogDir').value.trim() : ''); return; }
  if (act === 'pick-cache-dir') { openDirPicker('cache', $('sCacheDir') ? $('sCacheDir').value.trim() : ''); return; }
  // v3.7.2：设置页查看当前访问令牌（仅已登录会话可用）
  if (act === 'token-view') { await showCurrentToken(); return; }
  // v3.8.0：合并结果页的类型筛选、重新搜索与取消
  if (act === 'result-type') {
    S.resultType = el.dataset.type || 'all';
    S.vlist = null; S.groups = [];
    // v3.10.8 安全修复：筛选条件一变，旧勾选就不再对应任何可见行；
    // 不清空会让批量「移入隔离区（选中）」处理到用户看不见的文件。
    clearSelection();
    // 助手缓存的是上一批结果，换类型后必须重取，否则「按偏好标记」会算在别的类型上
    resetAssistCache();
    await loadRuleResults(true);
    return;
  }
  // v3.10.13：目录收窄。口径与类型筛选一致：清空已加载结果与勾选，重新取首页与 facets。
  if (act === 'result-dir') {
    S.dirFilter = el.dataset.dir || '';
    S.vlist = null; S.groups = [];
    clearSelection();
    resetAssistCache();
    await loadRuleResults(true);
    return;
  }
  // v3.13.0（UI 审查 P3-7）：只重绘目录条本身，避免整页重排打断滚动位置。
  if (act === 'result-dir-more') {
    S.dirsExpanded = true;
    const host = $('facetsBarHost');
    if (host) host.innerHTML = facetsInnerHtml();
    return;
  }
  if (act === 'rule-search-again') { await startRuleSearch(); return; }
  if (act === 'rule-search-export') { await exportRuleSearch(); return; }
  // v3.9.0：设置页规则预设
  if (act === 'preset-save') { await saveSettingsPreset(); return; }
  if (act === 'preset-apply') { applySettingsPreset(Number(el.dataset.idx)); return; }
  if (act === 'preset-del') { await deleteSettingsPreset(Number(el.dataset.idx)); return; }
  if (act === 'rule-search-cancel') {
    try { await postJSON('/api/rule-search/cancel', {}); toast('已请求取消搜索', 'ok'); }
    catch(e) { toast(e.message, 'err'); }
    return;
  }
  if (act === 'wiz-pick-exclude') { openDirPicker('scan', S.wizard.excludeDirs[0] || '', S.wizard.excludeDirs, 'wizardExclude'); return; }
  if (act === 'wiz-remove-scan' || act === 'wiz-remove-exclude') {
    if (!S.wizard) return;
    readWizardInputs();
    const key = act === 'wiz-remove-scan' ? 'scanDirs' : 'excludeDirs';
    S.wizard[key] = S.wizard[key].filter(p => p !== el.dataset.path);
    renderWizard();
    return;
  }
  if (act === 'go-paths') { S.setPane = 'paths'; await switchTab('settings'); return; }
  if (act === 'typechart-more') {
    S.typeChartAll = !S.typeChartAll;
    const box = $('typeChart');
    if (box) { box.dataset.key = ''; box.innerHTML = renderTypeChart((S.status && S.status.state) || {}); }
    return;
  }
  if (act === 'wiz-prev') { readWizardInputs(); S.wizard.step--; renderWizard(); return; }
  if (act === 'wiz-advanced') {
    if (S.wizard) {
      const w = S.wizard;
      // 记住进入高级模式前的简单模式选择，「返回推荐组合」时原样还原。
      w._simple = { content: w.content, name: w.name, video: w.video, intro: w.intro, audio: w.audio, photo: w.photo,
        recommend: w.recommend, base: w.rules.base, attrs: (w.rules.attrs || []).slice() };
      w.recommend = false;
      w.advanced = true;
      renderWizard();
    }
    return;
  }
  if (act === 'wiz-recommend') {
    const w = S.wizard;
    if (w) {
      w.content = true; w.name = true; w.video = false; w.intro = false; w.audio = false; w.photo = false;
      w.rules.base = 'content'; w.rules.attrs = [];
      w.recommend = true;
      renderWizard();
    }
    return;
  }
  if (act === 'wiz-advanced-off') {
    if (S.wizard) {
      const w = S.wizard;
      readWizardInputs();
      if (w._simple) {
        w.content = w._simple.content; w.name = w._simple.name;
        w.video = w._simple.video; w.intro = w._simple.intro;
        w.audio = w._simple.audio; w.photo = w._simple.photo;
        w.recommend = w._simple.recommend;
        // v4.5.3：高级模式里改过的 base/attrs 不能被带回简单模式，
        // 否则界面显示「精确内容 + 同名」，实际却按上次的高级 base 扫描。
        w.rules.base = w._simple.base;
        w.rules.attrs = (w._simple.attrs || []).slice();
        w._simple = null;
      }
      w.advanced = false;
      renderWizard();
    }
    return;
  }
  if (act === 'wiz-next') { await wizardNext(); return; }
  if (act === 'wiz-cancel') { cancelWizard(); return; }
  if (act === 'pick-scan-dirs') {
    const cur = (S.scanPaths || []).slice();
    openDirPicker('scan', cur[0] || '', cur);
    return;
  }
  if (act === 'dir-close') { closeDirPicker(); return; }
  if (act === 'dir-up') {
    if (S.dir && S.dir.listing) {
      // 已在该目录但上方没有授权父目录时，返回入口视图；入口视图本身保持禁用。
      loadDir(S.dir.listing.parent || '');
    }
    return;
  }
  if (act === 'dir-manual-go') {
    const raw = (($('dirManual') || {}).value || '').trim();
    // v3.7.2：先做格式校验，再交给 /api/fs/list 做授权探测（P-20）；
    // 探测失败会带服务端给出的明确原因，而不是默默跳到半截。
    const bad = fsPathError(raw);
    if (bad) { toast(bad, 'err'); return; }
    const p = normPath(raw);
    await loadDir(p);
    // GET /api/fs/list 现在只读；记住授权目录改由这里显式 POST（带 CSRF 头）。
    // 只有服务端探测通过（返回的 path 与输入一致）才发，失败不打扰用户浏览。
    if (S.dir && S.dir.path === p) {
      try { await postJSON('/api/fs/roots', { path: p }); }
      catch (e) { toast('目录可浏览，但记住失败：' + e.message, 'warn'); }
    }
    return;
  }
  if (act === 'dir-enter') { loadDir(el.dataset.path); return; }
  if (act === 'dir-check') {
    if (!S.dir) return;
    const p = el.dataset.path;
    if (S.dir.selected.has(p)) S.dir.selected.delete(p); else S.dir.selected.add(p);
    renderDirPicker();
    return;
  }
  if (act === 'dir-unselect') {
    if (S.dir) { S.dir.selected.delete(el.dataset.path); renderDirPicker(); }
    return;
  }
  if (act === 'dir-add-current') {
    if (S.dir && S.dir.path) { S.dir.selected.add(S.dir.path); renderDirPicker(); }
    return;
  }
  if (act === 'dir-mkdir') { await mkdirInPicker(); return; }
  if (act === 'dir-ok') { await finishDirPicker(); return; }
  if (act === 'open-assist-filter') {
    openAssistFilter();
  } else if (act === 'apply-intro') {
    const v = Math.min(300, Math.max(1, Number(($('fIntroN') || {}).value || 10)));
    S.introN = v; S.page = 1;
    resetAssistCache();
    await loadIntro();
  } else if (act === 'reanalyze-intro') {
    const v = Math.min(300, Math.max(1, Number(($('fIntroN') || {}).value || S.introN || 10)));
    S.introN = v;
    if (!confirm(`将按前 ${v} 秒清空并重新分析所有视频的片头指纹，耗时较长，确定继续？`)) return;
    try {
      await postJSON('/api/intro/reanalyze', { seconds: v });
      toast('已开始片头分析（前 ' + v + ' 秒）', 'ok');
      resetAssistCache();
      await refreshStatus();
      // v3.16.0：结果页没有独立片头页，重新分析后要重建搜索结果快照，
      // 否则列表仍是按旧片头指纹算出来的分组；统计缓存也一并作废。
      if (S.tab === 'results') {
        S.introStatsAt = 0;
        await startRuleSearch();
      } else {
        await loadIntro();
      }
    } catch(e) { toast(e.message, 'err'); }
  } else if (act === 'keep-first') {
    const key = el.dataset.key;
    const g = S.groups.find(x => x.key === key);
    if (g) {
      snapshotSel('只保留第一个');
      S.sel[key] = new Set(g.files.slice(1).map(f => f.path));
      renderGroups();
    }
  } else if (act === 'load-rest') {
    // 2026-11-08：结果多于首屏时，用户显式要求把剩余分组全部取回
    await loadAllGroups();
  } else if (act === 'trash-group') {
    const key = el.dataset.key;
    // v3.10.8：只取该组当前仍然存在的勾选，避免残留路径被一起隔离。
    const picked = currentSelectionByGroup().find(x => x.key === key);
    await doTrash(picked ? picked.paths : [], key);
  } else if (act === 'trash-selected') {
    // v3.9.1：结果页统一入口——把当前所有已勾选文件一次性移入隔离区，预览确认后再动手。
    // v3.10.8 安全修复：只取当前结果集里的勾选，界面上数到多少就处理多少，
    // 不允许把切换筛选后残留的、看不见的文件一起带进去。
    const picked = currentSelectionByGroup();
    const paths = [];
    const keys = [];
    picked.forEach(x => { keys.push(x.key); x.paths.forEach(p => paths.push(p)); });
    if (!paths.length) { toast('请先勾选要移入隔离区的文件', 'err'); return; }
    await doTrash(paths, keys);
  } else if (act === 'preview') {
    // 缩略图在 <label> 里，不拦这一下会顺带把行勾选切了
    ev.preventDefault();
    openPreview(el.dataset.path, el.dataset.name);
  } else if (act === 'collapse-group') {
    const key = el.dataset.key;
    if (S.collapsed.has(key)) S.collapsed.delete(key); else S.collapsed.add(key);
    S.vlist = null;
    renderGroups();
  } else if (act === 'toggle-all-groups') {
    const all = S.groups.length > 0 && S.groups.every(g => S.collapsed.has(g.key));
    if (all) S.collapsed.clear(); else S.groups.forEach(g => S.collapsed.add(g.key));
    S.vlist = null;
    renderGroups();
  } else if (act === 'gen-thumbs') {
    S.genThumbs = !S.genThumbs;
    S.vlist = null;
    renderGroups();
    if (S.genThumbs) toast('正在按行生成预览图…', 'ok');
  } else if (act === 'report') {
    await doReport();
  } else if (act === 'assist-toggle') {
    S.assistOpen = !S.assistOpen;
    renderAssistant();
  } else if (act === 'assist-keep-one') {
    await runGroupMark('keep-one');
  } else if (act === 'assist-keep-one-folder') {
    await runGroupMark('keep-one-folder');
  } else if (act === 'assist-mark-one') {
    await runGroupMark('mark-one');
  } else if (act === 'assist-pref') {
    await runGroupMark('pref');
  } else if (act === 'assist-pref-exclude') {
    await runGroupMark('pref-exclude');
  } else if (act === 'assist-unmark-path') {
    readAssistUI();
    const p = (S.keepPathDraft || '').trim();
    if (!p) { toast('请先在输入框填入路径', 'err'); return; }
    snapshotSel('取消路径标记');
    const pre = normPath(p);
    let n = 0;
    Object.keys(S.sel).forEach(k => {
      const set = S.sel[k];
      Array.from(set).forEach(path => {
        if (path === pre || path.startsWith(pre.endsWith('/') ? pre : pre + '/')) { set.delete(path); n++; }
      });
    });
    renderAssistant(); syncCheckboxes(); renderGroups();
    toast('已取消 ' + n + ' 个标记', 'ok');
    return;
  } else if (act === 'assist-location') {
    await runLocationMark();
  } else if (act === 'assist-text-mark') {
    await runTextMark(true);
  } else if (act === 'assist-text-unmark') {
    await runTextMark(false);
  } else if (act === 'assist-invert') {
    await assistInvert();
  } else if (act === 'assist-clear') {
    await assistClear();
  } else if (act === 'assist-undo') {
    assistUndo();
  } else if (act === 'assist-jump-allmarked') {
    await jumpFirstAllMarked();
  } else if (act === 'assist-add-path') {
    const v = $('aKeepPath') ? $('aKeepPath').value.trim() : '';
    readAssistUI();
    if (!v) { toast('请输入保留路径', 'err'); return; }
    const np = normPath(v);
    if (!S.keepPaths.includes(np)) S.keepPaths.push(np);
    S.keepPathDraft = '';
    renderAssistant();
  } else if (act === 'assist-fill-row-path') {
    if (S.rowSel.size !== 1) { toast('请只选一行', 'err'); return; }
    const p = Array.from(S.rowSel)[0];
    const input = $('aKeepPath');
    if (input) input.value = p;
    S.keepPathDraft = p;
    applyAssistWidth();
    toast('已填入选中行路径', 'ok');
  } else if (act === 'assist-remove-path') {
    readAssistUI();
    S.keepPaths.splice(Number(el.dataset.idx), 1);
    renderAssistant();
  } else if (act === 'assist-reset-prefs') {
    S.prefs = [
      {key:'resolution', dir:'max', on:true},
      {key:'size', dir:'max', on:true},
      {key:'duration', dir:'max', on:false},
      {key:'mtime', dir:'max', on:false},
      {key:'ctime', dir:'max', on:false},
      {key:'bitrate', dir:'max', on:false},
      {key:'samplerate', dir:'max', on:false},
      {key:'depth', dir:'min', on:false},
      {key:'name', dir:'max', on:false}
    ];
    renderAssistant();
  } else if (act === 'health-kind') {
    const h = healthState();
    // 批量操作进行中不允许切类别：进度卡片与结果列表都绑定在当前模块上。
    if (h.op && h.op.running) { toast('批量操作进行中，请等它结束再切换模块', 'warn'); return; }
    h.kind = el.dataset.kind; h.sel.clear();
    // 切换模块时把上一个模块的批量进度卡片清掉，避免张冠李戴。
    h.op = null;
    // v4.2.0：幽灵行复核按类型做，切到别的类型要允许重新复核一次。
    h.pruned = false;
    await loadHealth();
  } else if (act === 'health-scan') {
    // v3.9.3：主按钮一次扫全部 7 类，次要按钮只扫当前查看的类型。
    await healthScan(el.dataset.all === '1');
  } else if (act === 'health-fix-encoding') {
    await healthFixEncoding();
  } else if (act === 'health-cancel') {
    await healthCancel();
  } else if (act === 'health-upper-only') {
    const h = healthState();
    h.upperOnly = el.checked;
    // v3.10.3：已选集合独立于显示列表，不清理会导致「先全选、再勾只看大写」
    // 执行时把看不见的文件也一起改掉。切过滤时只保留当前可见项。
    const visible = new Set(healthViewItems().map(it => it.path));
    h.sel.forEach(p => { if (!visible.has(p)) h.sel.delete(p); });
    renderHealth();
  } else if (act === 'health-reason') {
    // v3.15.0：按原因筛选。切换后只保留当前可见项，避免把看不见的文件一起执行。
    const h = healthState();
    h.reasonFilter = el.dataset.reason || '';
    const visible = new Set(healthViewItems().map(it => it.path));
    h.sel.forEach(p => { if (!visible.has(p)) h.sel.delete(p); });
    renderHealth();
  } else if (act === 'health-toggle-uncertain') {
    // v3.15.0：显示/隐藏「无法确定」的提示项。隐藏时把它们从选择集里清掉。
    const h = healthState();
    h.showUncertain = !!el.checked;
    if (!h.showUncertain) {
      h.items.forEach(it => { if (healthItemAdvisory(it)) h.sel.delete(it.path); });
      const visible = new Set(healthViewItems().map(it => it.path));
      h.sel.forEach(p => { if (!visible.has(p)) h.sel.delete(p); });
    }
    renderHealth();
  } else if (act === 'health-sel-reason') {
    // v3.15.0：只全选当前原因筛选出的可处理条目；不确定项永远不进选择集。
    const h = healthState();
    healthViewItems().forEach(it => {
      if (!healthItemAdvisory(it) && it.path.indexOf('\uFFFD') < 0) h.sel.add(it.path);
    });
    document.querySelectorAll('input[data-act="health-check"]').forEach(cb => { if (!cb.disabled) cb.checked = true; });
    syncHealthSel();
  } else if (act === 'op-pause' || act === 'op-cancel') {
    // v3.19.0：批量操作进度卡统一由 helper.js 渲染，暂停 / 取消按操作 id 分发。
    const id = el.dataset.op || '';
    if (act === 'op-pause') batchedOpTogglePause(id);
    else {
      batchedOpCancel(id);
      // v3.22.0：改名还要通知服务端在片边界停下，否则当前分片跑完才知道。
      if (id === 'rename') renameRequestCancel();
    }
    refreshOpCardFor(id);
  } else if (act === 'op-export-failures') {
    batchedOpExportFailures(el.dataset.op || '', el.dataset.name || '');
  } else if (act === 'health-export-failures') {
    healthExportFailures();
  } else if (act === 'health-more') {
    // v3.7.2：健康结果分页续拉（P-13）
    await loadMoreHealth();
  } else if (act === 'health-sel-all') {
    // v3.10.2：全选的是本模块全部条目（数据已全量加载），不只是屏幕内可见的行。
    // 直接改 DOM 勾选状态而不重绘整表：一万多行重绘会明显卡顿。
    const h = healthState();
    // v4.1.2：按钮文案按「全模块可处理数」算，点击却只选 healthViewItems()（当前筛选视图），
    // 开着「按原因」筛选时执行集合会小于用户以为的集合。这里与文案同判据：
    // 数据已全量加载就选 h.items 全模块，否则退回当前视图（文案此时也提示先加载更多）。
    const allLoaded = h.items.length >= h.total || h.complete;
    const source = allLoaded ? h.items : healthViewItems();
    // v3.12.0：编码异常的行（路径含 U+FFFD）勾选后回传必然失效，全选时跳过，走整体修复入口。
    // v3.15.0：无法确定的提示项也不进入选择集，它们不可操作。
    source.forEach(it => {
      if (it.path.indexOf('\uFFFD') < 0 && !healthItemAdvisory(it)) h.sel.add(it.path);
    });
    document.querySelectorAll('input[data-act="health-check"]').forEach(cb => { if (!cb.disabled) cb.checked = true; });
    syncHealthSel();
  } else if (act === 'health-sel-none') {
    const h = healthState();
    h.sel.clear();
    document.querySelectorAll('input[data-act="health-check"]').forEach(cb => { cb.checked = false; });
    syncHealthSel();
  } else if (act === 'health-check') {
    const h = healthState();
    if (el.checked) h.sel.add(el.dataset.path); else h.sel.delete(el.dataset.path);
    syncHealthSel();
  } else if (act === 'health-rename-one') {
    const h = healthState();
    const p = el.dataset.path;
    const it = h.items.find(x => x.path === p) || {path:p};
    healthConfirm('确认修正文件名', [it], () => healthRenamePaths([p]));
  } else if (act === 'health-rename-sel') {
    const h = healthState();
    const paths = Array.from(h.sel);
    if (!paths.length) { toast('先选择文件', 'err'); return; }
    healthConfirm('确认批量修正', h.items.filter(x => h.sel.has(x.path)), () => healthRenamePaths(paths));
  } else if (act === 'health-trash-one') {
    healthConfirm('确认移入隔离区', [{path: el.dataset.path}], () => healthTrashPaths([el.dataset.path]), false);
  } else if (act === 'health-trash-sel') {
    const h = healthState();
    const paths = Array.from(h.sel);
    if (!paths.length) { toast('先选择文件', 'err'); return; }
    healthConfirm('确认移入隔离区', h.items.filter(x => h.sel.has(x.path)), () => healthTrashPaths(paths), false);
  } else if (act === 'health-to-simp') {
    const h = healthState();
    const paths = Array.from(h.sel);
    if (!paths.length) { toast('先选择文件', 'err'); return; }
    // v3.10.2：转简体也走进度条与完成页（与修正/清理一致），逻辑在 settings.js。
    healthConfirm('确认转简体', h.items.filter(x => h.sel.has(x.path)), () => healthToSimpPaths(paths));
  } else if (act === 'health-fix-all') {
    await healthFixAll();
  } else if (act === 'health-ignore-one') {
    healthConfirm('确认标为误报（不再上报）', [{path: el.dataset.path}], () => healthIgnorePaths([el.dataset.path]), false);
  } else if (act === 'health-ignore-sel') {
    const h = healthState();
    const paths = Array.from(h.sel);
    if (!paths.length) { toast('先选择文件', 'err'); return; }
    healthConfirm('确认标为误报（不再上报）', h.items.filter(x => h.sel.has(x.path)), () => healthIgnorePaths(paths), false);
  } else if (act === 'health-ignore-open') {
    await showHealthIgnores();
  } else if (act === 'health-unignore-one') {
    await healthUnignore([el.dataset.path]);
  } else if (act === 'health-unignore-all') {
    await healthUnignoreAll();
  } else if (act === 'health-move-sel') {
    healthMoveSel();
  } else if (act === 'health-undo') {
    await healthUndo();
  } else if (act === 'export-menu') {
    const wrap = el.closest('.expmenu-wrap');
    document.querySelectorAll('.expmenu-wrap.open').forEach(w => { if (w !== wrap) w.classList.remove('open'); });
    if (wrap) wrap.classList.toggle('open');
    return;
  } else if (act === 'export-run') {
    const wrap = el.closest('.expmenu-wrap'); if (wrap) wrap.classList.remove('open');
    await doExport(el.dataset.fmt, el.dataset.scope);
    return;
  } else if (act === 'import-upload') {
    await importUpload();
  } else if (act === 'import-open') {
    await loadImportGroups(Number(el.dataset.id));
  } else if (act === 'import-del') {
    await importDelete(Number(el.dataset.id), el.dataset.name || '');
  } else if (act === 'import-back') {
    S.importDetail = null; renderImport();
  } else if (act === 'import-more') {
    if (S.importDetail) { S.importDetail.shown = (S.importDetail.shown || IMPORT_PAGE) + IMPORT_PAGE; renderImport(); }
  } else if (act === 'import-trash') {
    await importTrashPaths([el.dataset.path]);
  } else if (act === 'import-trash-marked') {
    const d = S.importDetail;
    const paths = d ? (d.groups || []).flatMap(g => (g.rows || [])).filter(r => r.matched && r.chk && !r.protect).map(r => r.path) : [];
    if (!paths.length) { toast('没有已标记且已匹配、未受保护的文件', 'err'); return; }
    await importTrashPaths(paths);
  } else if (act === 'photo-sel') {
    if (el.checked) S.photoSel.add(el.dataset.path); else S.photoSel.delete(el.dataset.path);
    const cell = el.closest('.pcell');
    if (cell) cell.classList.toggle('pcell-sel', el.checked);
    const badges = document.querySelectorAll('.toolbar .badge.warn');
    badges.forEach(b => b.textContent = '已选 ' + S.photoSel.size);
    // v3.7.1：勾选数变化要同步「对比所选」的可用性，
    // 否则按钮一直停在渲染时的旧状态，够 2 张了也点不动（P-07）
    const cmpBtn = document.querySelector('[data-act="photo-compare"]');
    if (cmpBtn) cmpBtn.disabled = S.photoSel.size !== 2;
  } else if (act === 'photo-compare') {
    if (S.photoSel.size !== 2) { toast('请恰好勾选 2 张图片', 'err'); return; }
    const it = Array.from(S.photoSel);
    openCompare(it[0], it[1]);
  } else if (act === 'photo-trash-sel') {
    await photoTrashSel();
  } else if (act === 'audio-sel' || act === 'folder-sel') {
    const selSet = act === 'audio-sel' ? S.audioSel : S.folderSel;
    if (el.checked) selSet.add(el.dataset.path); else selSet.delete(el.dataset.path);
    const row = el.closest('.mrow');
    if (row) row.classList.toggle('mrow-sel', el.checked);
    document.querySelectorAll('.toolbar .badge.warn').forEach(b => {
      const set = act === 'audio-sel' ? S.audioSel : S.folderSel;
      b.textContent = '已选 ' + set.size;
    });
  } else if (act === 'folder-dir') {
    // 目录级勾选：一键勾/取消该目录下全部文件（建议保留的目录 checkbox 已禁用）
    const dir = el.dataset.dir;
    // 从当前渲染的目录卡里找该目录的文件行
    document.querySelectorAll('.dgroup').forEach(dg => {
      const dp = dg.querySelector('.dpath');
      if (!dp || dp.title !== dir) return;
      dg.querySelectorAll('input[data-act="folder-sel"]').forEach(cb => {
        cb.checked = el.checked;
        if (el.checked) S.folderSel.add(cb.dataset.path); else S.folderSel.delete(cb.dataset.path);
        const row = cb.closest('.mrow');
        if (row) row.classList.toggle('mrow-sel', el.checked);
      });
    });
    document.querySelectorAll('.toolbar .badge.warn').forEach(b => b.textContent = '已选 ' + S.folderSel.size);
  } else if (act === 'audio-trash-sel') {
    await mediaTrashSel('audio');
  } else if (act === 'folder-trash-sel') {
    await mediaTrashSel('folder');
  } else if (act === 'big-sel') {
    if (el.checked) S.bigSel.add(el.dataset.path); else S.bigSel.delete(el.dataset.path);
    const row = el.closest('.mrow');
    if (row) row.classList.toggle('mrow-sel', el.checked);
    document.querySelectorAll('.toolbar .badge.warn').forEach(b => b.textContent = '已选 ' + S.bigSel.size);
  } else if (act === 'big-trash-sel') {
    await bigTrashSel();
  } else if (act === 'fanjian-menu') {
    // v3.10.8：只取当前结果集里的勾选，避免批量改名改到用户看不见的文件。
    const paths = currentSelectionPaths();
    if (!paths.length) { toast('先勾选要转换的文件', 'err'); return; }
    const html = `<div class="asec" data-sec-title="转换方向"><label class="rp-head" data-act="asec-fold" data-sec="转换方向" style="cursor:pointer"><span class="rp-arrow">▾</span>转换方向</label>
      <label class="checkline" style="font-size:var(--text-control)"><input type="radio" name="fjDir" value="s2t" checked> 简 → 繁（如：数据库 → 數據庫）</label>
      <label class="checkline" style="font-size:var(--text-control)"><input type="radio" name="fjDir" value="t2s"> 繁 → 简（如：資料庫 → 资料库）</label>
      <div class="hint">OpenCC 词典（纯 Go 内嵌，词级转换）；无中文字或已是目标体的文件自动跳过。冲突自动追加 _1；可整体撤销。注意：按路径引用的程序会断链。</div></div>
      <div class="pm-actions"><button class="btn ghost" data-act="modal-cancel">取消</button>
      <button class="btn primary" data-act="fanjian-run">确 认 转 换</button></div>`;
    setModal('繁简转换（' + paths.length + ' 个文件）', html, null);
    S.fjPaths = paths;
  } else if (act === 'fanjian-run') {
    const dir = (document.querySelector('input[name="fjDir"]:checked') || {}).value || 's2t';
    const paths = S.fjPaths || [];
    S.fjPaths = null;
    setModal('正在转换…', '<div class="hint">执行中。</div>', null);
    try {
      const r = await postJSON('/api/fanjian', { paths, to_traditional: dir === 's2t' });
      const items = [];
      (r.done || []).forEach(d => items.push({ path: d.from, ok: true, reason: '→ ' + d.to }));
      (r.skipped || []).forEach(p => items.push({ path: p, ok: true, reason: '无需转换' }));
      (r.failed || []).forEach(p => items.push({ path: p, ok: false, reason: r.errors[p] || '失败' }));
      const undoBtn = r.task_id ? `<button class="btn ghost" data-act="fanjian-undo" data-tid="${r.task_id}">撤销</button>` : '';
      setModal('繁简转换结果', `${previewTable(items)}<div class="hint">转换 ${okCount(items)} 个，跳过 ${(r.skipped || []).length} 个，失败 ${(r.failed || []).length} 个。</div>
      ${reconcileHTML(r.reconcile)}
      <div class="pm-actions">${undoBtn}<button class="btn primary" data-act="modal-cancel">关闭</button></div>`, null);
      S.sel = {};
      await refreshStatus();
      await refreshView();
    } catch(e) { closeModal(); toast(e.message, 'err'); }
  } else if (act === 'fanjian-undo') {
    try {
      const r = await postJSON('/api/fanjian/undo', { task_id: Number(el.dataset.tid) });
      toast(`撤销完成：还原 ${r.done} 个，失败 ${r.failed} 个` + undoFailReason(r), r.failed ? 'err' : 'ok');
      closeModal();
      await refreshView();
    } catch(e) { toast(e.message, 'err'); }
  } else if (act === 'tc-enqueue-sel') {
    // v1.26.0：结果页勾选行批量入队（只入队不执行，决策 C5 保持）
    // v3.10.8：只取当前结果集里的勾选，避免把筛选后看不见的旧文件悄悄排进队列。
    if (!S.tcSel) S.tcSel = new Set();
    const picked = currentSelectionPaths();
    let n = 0;
    picked.forEach(p => { if (!S.tcSel.has(p)) { S.tcSel.add(p); n++; } });
    S.sel = {};
    if (!n) { toast('先勾选要转码的文件', 'err'); return; }
    toast(`已批量加入转码队列（新增 ${n}，共 ${S.tcSel.size} 个），到「视频转码」页发起`, 'ok');
  } else if (act === 'tc-enqueue') {
    if (!S.tcSel) S.tcSel = new Set();
    S.tcSel.add(el.dataset.path);
    toast('已加入转码队列（' + S.tcSel.size + ' 个），到「视频转码」页发起', 'ok');
  } else if (act === 'tc-pause') {
    try {
      await postJSON(pausedRef() ? '/api/tasks/resume' : '/api/tasks/pause', {});
      toast(pausedRef() ? '转码已继续' : '转码暂停中：当前文件完成后冻结', 'ok');
      await refreshStatus();
      await loadTranscode();
    } catch(e) { toast(e.message, 'err'); }
  } else if (act === 'tc-start') {
    await transcodeStart();
  } else if (act === 'tc-cancel') {
    // v3.9.6 修 S-35：取消整批前确认（误点会丢失排队任务）
    if (!confirm('确定要取消全部排队中的转码任务？已完成的文件不受影响。')) return;
    try { await postJSON('/api/transcode/cancel', {}); toast('已请求取消', 'ok'); } catch(e) { toast(e.message, 'err'); }
  } else if (act === 'tc-cancel-one') {
    // v3.10.8：只取消这一条任务，参数按后端约定用 job_id；失败原因（如「任务不存在或已结束」）原样报给用户
    const id = Number(el.dataset.id);
    if (!id) { toast('任务 ID 无效，请刷新后再试', 'err'); return; }
    try {
      await postJSON('/api/transcode/cancel', { job_id: id });
      toast('已请求取消该任务', 'ok');
      await loadTranscode();
    } catch(e) { toast(e.message, 'err'); }
  } else if (act === 'tc-clear') {
    S.tcSel = new Set();
    await loadTranscode();
  } else if (act === 'tc-residue') {
    await transcodeCleanResidue();
  } else if (act === 'tc-export-failures') {
    transcodeExportFailures();
  } else if (act === 'trim-panel') {
    openTrimPanel(el.dataset.key);
  } else if (act === 'trim-probe') {
    await trimProbe();
  } else if (act === 'trim-confirm') {
    await trimConfirm();
  } else if (act === 'trim-confirm2') {
    await trimConfirm2();
  } else if (act === 'group-rename-base') {
    await groupRenameBase(el.dataset.key, el.dataset.path);
  } else if (act === 'file-rename') {
    await fileRename(el.dataset.path);
  } else if (act === 'group-rename-confirm') {
    await groupRenameConfirm();
  } else if (act === 'group-rename-undo') {
    await groupRenameUndo();
  } else if (act === 'rp-fold') {
    const k = el.dataset.key;
    if (S.rpCollapsed.has(k)) S.rpCollapsed.delete(k); else S.rpCollapsed.add(k);
    // 折叠是纯前端状态，直接重渲染当前页（不重拉数据——folder 重拉会重置默认折叠）
    if (S.tab === 'folder') renderFolder();
    else if (S.tab === 'audio') renderAudio();
    else if (S.tab === 'photo') renderPhoto();
  } else if (act === 'photo-thumbs') {
    const paths = [];
    (S.photoGroups || []).forEach(g => (g.files || []).forEach(f => paths.push(f.path)));
    batchThumbs(paths, '相似图片');
  } else if (act === 'new-scan') {
    openWizard();
  } else if (act === 'big-thumbs') {
    batchThumbs((S.bigFiles || []).map(f => f.path), '大文件');
  } else if (act === 'filter-toggle') {
    S.listFilter.open = !S.listFilter.open;
    renderAssistant();
  } else if (act === 'help-link') {
    helpLink(el.dataset.sec);
  } else if (act === 'help-fold') {
    helpFold[el.dataset.id] = !helpFold[el.dataset.id];
    renderHelp();
  } else if (act === 'help-jump') {
    // v3.10.0：手册顶部章节胶囊，平滑滚动定位并展开该章
    const t = document.getElementById('help-' + el.dataset.id);
    if (t) t.scrollIntoView({ behavior: 'smooth', block: 'start' });
  } else if (act === 'set-pane') {
    // v3.10.0：设置页下级导航。只切显示不重渲染，避免把用户还没保存的输入冲掉。
    S.setPane = el.dataset.pane;
    document.querySelectorAll('.set-pane').forEach(p => { p.hidden = p.dataset.pane !== S.setPane; });
    document.querySelectorAll('.set-tab').forEach(b => {
      const on = b.dataset.pane === S.setPane;
      b.classList.toggle('on', on);
      b.setAttribute('aria-selected', String(on));
    });
    // 保存按钮只属于前四个扫描/路径类模块，切到标签、清理等独立卡片时收起，
    // 否则会剩一张只有按钮的空卡片。
    const saveRow = document.getElementById('saveSettingsRow');
    if (saveRow) saveRow.hidden = !['perf', 'schedule', 'basis', 'paths'].includes(S.setPane);
  } else if (act === 'asec-fold') {
    // v1.24.0：助手各段独立折叠
    S.asecFold[el.dataset.sec] = !S.asecFold[el.dataset.sec];
    renderAssistant();
  } else if (act === 'filter-set') {
    S.listFilter[el.dataset.k] = el.checked;
    refreshView();
    } else if (act === 'filter-adv') {
    S.listFilter.advOpen = !S.listFilter.advOpen;
    renderAssistant();
    return;
  } else if (act === 'filter-clear') {
    S.listFilter = { marked:false, unmarked:false, partial:false, allMarked:false, protected:false,
      countMin:'', countMax:'', sizeMin:'', sizeMax:'', fileMin:'', fileMax:'', showAll:true, open:true,
      minKB:'', maxKB:'', exts:'', q:'', since:'', until:'',
      dateOnly:false, sameDrive:'', maxDurDiff:'', ignoreSameFolder:false, advOpen:true,
      folderName:'', folderDepth:'', similarName:false, sizeTolKB:'', crossDevice:false, tagName:'', tagValue:'' };
    renderAssistant();
    refreshView();
  } else if (act === 'assist-sort-apply') {
    S.sort = $('aSort') ? $('aSort').value : 'waste';
    S.sortOrder = $('aSortOrder') ? $('aSortOrder').value : 'desc';
    refreshView();
  } else if (act === 'move-selected') {
    await moveSelected();
  } else if (act === 'move-undo') {
    await moveUndo();
  } else if (act === 'move-confirm') {
    await moveConfirm();
  } else if (act === 'move-dir-start') {
    // v3.14.0：文件搜索页的目录级移动入口
    await moveDirStart();
  } else if (act === 'move-dir-confirm') {
    await moveDirConfirm();
  } else if (act === 'move-dir-undo-now') {
    await moveDirUndoNow();
  } else if (act === 'relink-selected') {
    await relinkSelected();
  } else if (act === 'relink-confirm') {
    await relinkConfirm();
  } else if (act === 'fuzzy-near') {
    S.fuzzy = S.fuzzy || { near: true, page: 1 };
    S.fuzzy.near = el.checked; S.fuzzy.page = 1;
    await loadFuzzy();
  } else if (act === 'semantic-page') {
    const p = Number(el.dataset.p || 1);
    if (p >= 1) { S.semantic.page = p; await loadSemantic(); }
  } else if (act === 'fuzzy-page') {
    const p = Number(el.dataset.p || 1);
    if (p >= 1) { S.fuzzy.page = p; await loadFuzzy(); }
  } else if (act === 'search-add-rename') {
    await addSelToRename();
  } else if (act === 'search-clear-sel') {
    searchClearSel();
  } else if (act === 'search-dir') {
    openDirPicker('searchdir');
  } else if (act === 'search-reset-filter') {
    // v3.9.10：清空筛选同时保留关键词，回到默认排序重新查询
    const st = searchState();
    Object.assign(st, { ext:'', minKB:'', maxKB:'', since:'', until:'', dir:'', sort:'name', order:'asc' });
    renderSearchPage();
    if (st.q) doSearch();
  } else if (act === 'rename-apply-rules') {
    await applyRenameRules();
  } else if (act === 'rename-execute') {
    await executeRenameBatch();
  } else if (act === 'rename-undo') {
    await undoRenameBatch();
  } else if (act === 'rename-add-file') {
    addRenameFile();
  } else if (act === 'goto-rename') {
    await switchTab('rename');
  } else if (act === 'rename-token-clear') {
    // v3.21.0：移除全选凭据条目，不影响显式清单。
    const r = renameState();
    r.token = null; r.planID = 0; r.planSig = ''; r.planTotal = 0;
    renderRenamePage();
  } else if (act === 'rename-undo-one') {
    await undoRenameOne(Number(el.dataset.id || 0));
  } else if (act === 'rename-remove') {
    const r = renameState();
    r.list.splice(Number(el.dataset.idx), 1);
    renderRenamePage();
  } else if (act === 'rename-method-add') {
    addRenameMethod();
  } else if (act === 'rename-method-del') {
    removeRenameMethod(Number(el.dataset.idx));
  } else if (act === 'rename-preview-methods') {
    await previewRenameMethods();
  } else if (act === 'rename-execute-methods') {
    await executeRenameMethods();
  } else if (act === 'rename-preset-load') {
    loadRenamePreset();
  } else if (act === 'rename-preset-save') {
    await saveRenamePreset();
  } else if (act === 'rename-preset-del') {
    await deleteRenamePreset();
  } else if (act === 'tag-add') {
    await saveVideoTag();
  } else if (act === 'tag-recompute') {
    await recomputeTagItem(Number(el.dataset.id));
  } else if (act === 'tag-del') {
    await deleteTagItem(Number(el.dataset.id));
  } else if (act === 'tag-groups') {
    await viewTagGroups(Number(el.dataset.id));
  } else if (act === 'tag-select') {
    const idx = Number(el.dataset.idx);
    selectPaths((S.tagGroupPaths || [])[idx] || []);
  } else if (act === 'save-settings') {
    await saveSettings();
  } else if (act === 'cache-clear') {
    const kind = el.dataset.kind;
    if (!confirm('清理该类缓存？清理后会自动重新计算，不影响扫描到的文件数据。')) return;
    try {
      const r = await postJSON('/api/cache/clear', { kind });
      toast('已清理 ' + (r.cleared != null ? r.cleared : '') + ' 项', 'ok');
      loadCacheStats();
    } catch (e) {
      toast('清理失败：' + (e && e.message || e), 'err');
    }
  } else if (act === 'restore-one') {
    await restoreIDs([Number(el.dataset.id)]);
  } else if (act === 'trash-group-toggle') {
    // v3.9.3：隔离区按目录聚合后，组头承担展开/折叠。
    if (!S.trashOpen) S.trashOpen = new Set();
    const dir = el.dataset.dir;
    if (S.trashOpen.has(dir)) S.trashOpen.delete(dir); else S.trashOpen.add(dir);
    renderTrash();
  } else if (act === 'trash-group-check') {
    // 整组勾选/取消：同一路径下的文件通常一起处理。
    if (!S.trashSel) S.trashSel = new Set();
    const list = (S.trash || []).filter(t => trashDirOf(t.orig_path) === el.dataset.dir);
    const allChecked = list.length > 0 && list.every(t => S.trashSel.has(t.id));
    list.forEach(t => { if (allChecked) S.trashSel.delete(t.id); else S.trashSel.add(t.id); });
    renderTrash();
  } else if (act === 'trash-toggle-all') {
    if (!S.trashOpen) S.trashOpen = new Set();
    const dirs = Array.from(new Set((S.trash || []).map(t => trashDirOf(t.orig_path))));
    const allOpen = dirs.length > 0 && dirs.every(d => S.trashOpen.has(d));
    S.trashOpen = allOpen ? new Set() : new Set(dirs);
    renderTrash();
  } else if (act === 'trash-load-more') {
    // v3.25.2：分页追加下一页，不清空已勾选。
    await loadMoreTrash();
  } else if (act === 'delete-one') {
    await deleteIDs([Number(el.dataset.id)]);
  } else if (act === 'restore-sel') {
    await restoreIDs(Array.from(S.trashSel));
  } else if (act === 'export-trash-sel') {
    trashExport(Array.from(S.trashSel), false);
  } else if (act === 'export-trash-all') {
    trashExport([], true);
  } else if (act === 'cleanup-save') {
    await saveCleanupPlan();
  } else if (act === 'cleanup-run') {
    await runCleanupPlan(Number(el.dataset.id));
  } else if (act === 'cleanup-edit') {
    const p = (S.cleanupPlans || []).find(x => x.id === Number(el.dataset.id));
    if (p) fillCleanupForm(p);
  } else if (act === 'cleanup-toggle') {
    await toggleCleanupPlan(Number(el.dataset.id), el.dataset.enabled === '1');
  } else if (act === 'cleanup-del') {
    await deleteCleanupPlan(Number(el.dataset.id));
  } else if (act === 'cleanup-tpl') {
    applyCleanupTemplate(el.dataset.tpl);
  } else if (act === 'sync-export') {
    // v3.9.6 修 S-29：原来 `<a download>` 无法检测 HTTP 成败，改成 fetch 能读 response.status
    try {
      toast('正在生成指纹包…');
      const r = /** @type {AnyResp} */ (await api('/api/sync/export')); // api() 已自动处理 401→showLogin
      const blob = await r.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url; a.download = '';
      document.body.appendChild(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 5000);
      toast('指纹包已开始下载', 'ok');
    } catch(e) { toast('导出失败：' + (e && e.message || e), 'err'); }
  } else if (act === 'sync-import-pick') {
    $('syncFile').click();
  } else if (act === 'sync-peer-del') {
    if (!confirm('删除这台设备的指纹包？删除后跨设备标记会消失。')) return;
    try {
      /** @type {SyncPeersResp} */ (await api('/api/sync/peers?machine=' + encodeURIComponent(el.dataset.machine), { method: 'DELETE' }));
      toast('已删除', 'ok');
      loadSyncPeers();
    } catch(e) { toast(e.message, 'err'); }
  } else if (act === 'delete-sel') {
    await deleteIDs(Array.from(S.trashSel));
  } else if (act === 'empty-trash') {
    await emptyTrash();
  } else if (act === 'reanalyze-all') {
    if (!confirm('将清空所有视频指纹并重新分析，耗时较长，确定继续？')) return;
    try {
      await postJSON('/api/video/reanalyze', { all: true });
      toast('已开始重新分析全部视频', 'ok');
      resetAssistCache();
      await refreshStatus();
      // v3.17.1：结果页没有独立视频页，重新分析后要重建搜索结果快照，
      // 否则列表仍是按旧视频指纹算出来的分组。
      if (S.tab === 'results') await startRuleSearch();
      else await loadVideo();
    } catch(e) { toast(e.message, 'err'); }
  } else if (act === 'toggle-fails') {
    S.showFails = !S.showFails; renderGroups();
  } else if (act === 'copy-fail') {
    const f = (S.videoFails || []).find(x => String(x.id) === String(el.dataset.id));
    if (f) await copyText(f.error || '', '已复制失败原因');
    else toast('未找到失败记录', 'err');
  } else if (act === 'copy-last-error') {
    const err = S.status && S.status.state ? (S.status.state.last_error || '') : '';
    await copyText(err, '已复制最近错误');
  } else if (act === 'repair-index') {
    // v3.10.12（P1-02）：扫描因幽灵索引行整体失败时，给用户一个可点击的自救入口。
    // 只删「磁盘上已不存在」的索引行，不动任何磁盘文件。按钮就挨着错误文案、点击成本低，
    // 所以保留二次确认，并把「不动文件、不动隔离区」写在确认框里，避免误点后以为文件被删。
    if (!confirm('将逐条核对索引，只删除「磁盘上已不存在」的记录。\n不会删除、移动或修改任何磁盘文件，隔离区内容也不受影响。\n\n现在开始检查吗？')) return;
    try {
      toast('正在检查索引，文件较多时需要几分钟…');
      const r = await postJSON('/api/index/repair', { dry_run: false });
      toast(`索引修复完成：检查 ${r.checked} 条，清理 ${r.removed} 条${r.skipped ? '，跳过 ' + r.skipped + ' 条（无法判断）' : ''}`, 'ok');
      await refreshStatus();
      if (S.tab === 'overview') renderOverview(S.status);
    } catch(e) { toast(e.message, 'err'); }
  } else if (act === 'retry-fail-one') {
    try {
      await postJSON('/api/video/reanalyze', { ids: [Number(el.dataset.id)] });
      toast('已加入重试队列', 'ok');
      await loadVideo();
    } catch(e) { toast(e.message, 'err'); }
  } else if (act === 'retry-all-fails') {
    const ids = (S.videoFails || []).map(f => f.id);
    if (!ids.length) return;
    try {
      await postJSON('/api/video/reanalyze', { ids });
      toast('已加入重试队列：' + ids.length + ' 个文件', 'ok');
      await loadVideo();
    } catch(e) { toast(e.message, 'err'); }
  }
});

$('scanBtn').addEventListener('click', async () => {
  openWizard();
});
$('stopBtn').addEventListener('click', async () => {
  // 防止重复点击连发多个停止请求；请求落地前按钮置灰、结束后恢复（P-15）
  if ($('stopBtn').dataset.busy === '1') return;
  $('stopBtn').dataset.busy = '1';
  try {
    await postJSON('/api/scan/stop', {});
    S.manualStopFlag = true; // v1.28.2：手动停止不走自动 toast
    // v1.28.2：手动停止 → 弹窗提示日志位置（需手动关闭）
    const lf = (S.status && S.status.paths && S.status.paths.log_file) || '<数据目录>/logs/verify.log';
    setModal('已请求停止扫描', `<div class="hint">扫描正在停止（当前批完成后冻结）。本次运行与历史日志都在：<br><span class="mono">${esc(lf)}</span></div>
      <div class="pm-actions"><button class="btn primary" data-act="modal-cancel">知道了</button></div>`, null);
  } catch(e) { toast(e.message, 'err'); }
  finally { delete $('stopBtn').dataset.busy; }
});

// v1.28.2：自动完成 → 5 秒 toast 提示日志位置（检测 running→idle 转变，排除手动停止）
let prevRunning = false;

const _refreshStatusOrig = refreshStatus;
// @ts-ignore —— TS2630：refreshStatus 是函数声明，绑定不可重新赋值。
// 这里是有意的 monkey-patch：helper.js 定义原函数，本文件在脚本末尾包一层，
// 补「自动完成提示」与「扫描完成后自动进入搜索阶段」两段逻辑。
// 加载顺序由 index.html 的 <script defer> 保证：helper.js 先执行、app.js 后执行，
// 所以重赋值一定发生在定义之后。改成 var 会牵动所有引用点，风险大于收益。
// 已知技术债：若将来引入模块化前端，这里应改成显式的中间层而不是猴补。
refreshStatus = async function(){
  const wasRunning = prevRunning;
  const st = await _refreshStatusOrig.apply(this, arguments);
  const nowRunning = !!(S.status && S.status.state && S.status.state.running);
  if (wasRunning && !nowRunning && !S.manualStopFlag) {
    const lf = (S.status && S.status.paths && S.status.paths.log_file) || '';
    if (lf) toast(`查重完成。运行日志：${lf}`, 'ok', 5000);
  }
  // v3.8.0：新建查重扫描完成后自动进入搜索阶段，搜索完成后展示合并结果页。
  if (wasRunning && !nowRunning && S.pendingRuleSearch && !S.manualStopFlag) {
    S.pendingRuleSearch = false;
    await switchTab('results');
  }
  prevRunning = nowRunning;
  if (S.manualStopFlag && !nowRunning) S.manualStopFlag = false;
  return st;
};
$('pauseBtn').addEventListener('click', async () => {
  try {
    const paused = !!(S.status && S.status.paused);
    await postJSON(paused ? '/api/tasks/resume' : '/api/tasks/pause', {});
    toast(paused ? '已继续' : '暂停中：当前批完成后冻结', 'ok');
    await refreshStatus();
  } catch(e) { toast(e.message, 'err'); }
});
$('loginBtn').addEventListener('click', async () => {
  const btn = $('loginBtn'), token = $('tokenInput').value.trim();
  // v3.9.6 修 G-10：空 token 前置拦截 + 提交期间 disabled + 文案反馈
  if (!token) { toast('请先填写访问令牌', 'err'); return; }
  btn.disabled = true; btn.textContent = '登录中…';
  try {
    await postJSON('/api/login', { token });
    hideLogin(); await boot();
  } catch(e) { toast(e.message, 'err'); }
  finally { btn.disabled = false; btn.textContent = '登录'; }
});
$('tokenInput').addEventListener('keydown', (e) => { if (e.key === 'Enter') $('loginBtn').click(); });

// v4.5.0：导航统一事件委托（侧栏、更多功能、底部导航共用一套 data-tab）
document.addEventListener('click', (ev) => {
  const nav = ev.target.closest('.bottomnav button[data-tab], .tab[data-tab]');
  if (!nav) return;
  ev.preventDefault();
  switchTab(nav.dataset.tab);
});
(function bindNavMore(){
  const tog = $('navMoreToggle'), more = $('navMore');
  if (!tog || !more) return;
  tog.addEventListener('click', () => {
    const open = more.hidden;
    more.hidden = !open;
    tog.setAttribute('aria-expanded', open ? 'true' : 'false');
    tog.textContent = open ? '收起更多功能' : '更多功能';
  });
})();
// v4.5.0：概览的两处小交互——令牌提示只提示一次、跳过明细可展开
document.addEventListener('click', (ev) => {
  const el = ev.target.closest('[data-act]');
  if (!el) return;
  const act = el.dataset.act;
  if (act === 'token-notice-close') {
    ev.preventDefault();
    try { localStorage.setItem('verify.tokenNotice', '1'); } catch(e) {}
    const n = el.closest('.notice'); if (n) n.remove();
  } else if (act === 'skip-detail') {
    ev.preventDefault();
    showSkipDetail();
  }
});

document.addEventListener('change', async e => {
  if (e.target && e.target.dataset && e.target.dataset.act === 'sync-file') {
    const file = e.target.files && e.target.files[0];
    if (!file) return;
    try {
      const pkg = JSON.parse(await file.text());
      const r = await postJSON('/api/sync/import', pkg);
      toast('已导入 ' + (r.imported || 0) + ' 条（忽略 ' + (r.invalid || 0) + ' 条）', 'ok', 5000);
      loadSyncPeers();
    } catch(err) { toast('导入失败：' + (err && err.message || err), 'err', 5000); }
    e.target.value = '';
    return;
  }
  if (e.target && e.target.dataset && e.target.dataset.act === 'pref-apply') {
    try {
      const r = await postJSON('/api/retain/pref/apply', { enable: e.target.checked });
      toast(r.enabled ? '已启用偏好反哺' : '已停用偏好反哺，恢复默认权重', 'ok');
      loadPrefStats();
    } catch(err) {
      toast(err.message, 'err');
      e.target.checked = !e.target.checked;
    }
    return;
  }
  if (e.target && e.target.dataset && e.target.dataset.act === 'big-crop') {
    S.bigCrop = e.target.value === 'all' ? '' : e.target.value;
    S.bigSel = new Set();
    await loadBig();
    return;
  }
  if (e.target && e.target.dataset && e.target.dataset.act === 'big-order') {
    S.bigOrder = e.target.value;
    loadBig();
  }
  // v3.8.0：合并结果页排序
  if (e.target && e.target.dataset && e.target.dataset.act === 'result-sort') {
    S.sort = e.target.value;
    await loadRuleResults(true);
  }
  if (e.target && e.target.dataset && e.target.dataset.act === 'result-order') {
    S.sortOrder = e.target.value;
    await loadRuleResults(true);
  }
});

let reloadTimer = null;
/* ===== 文件健康检测 ===== */

function connectSSE(){
  // 断线自动重连，但非首次断线要提示用户，避免静默丢失进度更新而没察觉（P-16）
  let _everConnected = false;
  const es = new EventSource('/api/events');
  es.addEventListener('progress', (ev) => {
    try {
      const state = JSON.parse(ev.data);
      // 记住最新进度，顶部计时器每秒用同一份数据重绘，避免只有 SSE 到达时才走时间。
      if (S.status) S.status.state = state;
      const was = S.scanRunning;
      S.scanRunning = !!state.running;
      if (!was && state.running) { S.prefetch = null; S.prefetchScanAt = 0; S.scanFinishAt = 0; }
      if (was && !state.running) onScanFinished(state);
      else if (state.running) maybePrefetch(state);
      renderProgress(state);
    } catch(e) {}
  });
  es.addEventListener('transcode_progress', (ev) => {
    // v3.23.0：单文件转码时后端每行 ffmpeg stderr 都推一次，界面据此实时前进。
    try {
      const p = JSON.parse(ev.data);
      const t = (S.tcTasks || []).find(x => x.id === p.id);
      if (!t) return;
      t.progress = Number(p.progress) || 0;
      if (S.tab === 'transcode') renderTranscode();
    } catch(e) {}
  });
  es.addEventListener('health', (ev) => {
    try {
      const p = JSON.parse(ev.data);
      const h = healthState();
      // v3.10.2：批量改文件期间后端每批都会推一次健康事件，
      // 这里若跟着重拉，一万多条的列表会被反复重建，进度条反而卡住。
      if (h.op && h.op.running) return;
      h.progress = p;
      if (S.tab === 'health') {
        if (p.status === 'running') scheduleHealthPoll();
        else loadHealth();
      }
    } catch(e) {}
  });
  es.addEventListener('update', () => {
    if (reloadTimer) return;
    reloadTimer = setTimeout(async () => {
      reloadTimer = null;
      try {
        resetAssistCache();
        await refreshStatus();
        // 扫描收尾已由预取逻辑渲染首屏，跳过这次重复刷新。
        if (Date.now() - (S.scanFinishAt || 0) < 3000) return;
        // v3.10.0 UI-01：从 6 个页签白名单改成「全页签仅排除有本地编辑态/进度的页面」。
        // settings 重绘会清掉未保存的输入；health 检测中重拉会打乱进度轮询（另见后端运行态门禁）；
        // import 与 rename 有勾选、方法链等本地状态，重绘会丢，保留手动切页刷新。
        const noAuto = ['settings', 'import', 'rename'];
        const hp = healthState().progress;
        if (S.tab === 'health') {
          // 检测进行中不重拉，避免打断当轮进度；结束时由 health 事件负责收尾刷新。
          // v3.10.2：批量修正/清理期间同样不重拉，结束时由 healthRunBatches 自己收尾。
          const op = healthState().op;
          if (op && op.running) return;
          if (!hp || hp.status !== 'running') await loadHealth();
        } else if (!noAuto.includes(S.tab)) await refreshView();
      } catch(e) {}
    }, 1600);
  });
  es.onopen = () => { _everConnected = true; };
  es.onerror = () => {
    es.close();
    // 只提示「建连后断开」的情况，首次建连失败或服务端未就绪不打扰（P-16）
    if (_everConnected) toast('与服务器的实时更新连接中断，正在自动重连…', 'err', 4000);
    setTimeout(connectSSE, 5000);
  };
}

async function boot(){
  try {
    // v3.9.5：启动时读 URL hash，支持刷新/深链恢复当前页
    const h = (location.hash || '').slice(1);
    if (h && !S.tab && ['overview','help','results','trash','import','search','rename','settings','oplog','health','video','intro','photo','audio','folder','fuzzy','semantic','samevideo','big','transcode'].includes(h)) S.tab = h;
    const st = await refreshStatus();
    if (st.auth && st.auth.required && !st.auth.ok) { showLogin(); return; }
    renderProgress(st.state);
    // 2026-11-08：启动时探一次崩溃残留的改名批次，给左侧导航挂角标；
    // 空数组时无任何可见变化，失败也不弹 toast（不能因为探测失败挡住首屏）
    loadUnfinishedBatches();
    if (S.tab === 'overview') renderOverview(st);
    else await switchTab(S.tab);
    // v4.5.0：首次使用引导（设置里可重新打开）
    if (typeof maybeShowOnboarding === 'function') maybeShowOnboarding();
  } catch(e) {
    if (String(e.message).includes('令牌')) { showLogin(); return; }
    toast(e.message, 'err');
  }
}

// v3.9.5：浏览器前进后退触发
window.addEventListener('popstate', () => {
  const h = (location.hash || '').slice(1);
  if (h) switchTab(h);
});

// ---------- v3.10.1：返回顶部（所有页面通用） ----------
// 长结果页的滚动可能发生在窗口，也可能在表格（.tscroll）或结果列表（.virtual-list）里，
// 且这两类容器是渲染后才出现的，所以用捕获阶段的 scroll 监听统一判断，
// 点击时把窗口和所有已滚动的容器一起拉回顶部。
function initBackToTop(){
  const btn = $('toTop');
  if (!btn) return;
  const scrollers = () => Array.from(document.querySelectorAll('.virtual-list,.tscroll'));
  let ticking = false;
  const update = () => {
    if (ticking) return;
    ticking = true;
    requestAnimationFrame(() => {
      ticking = false;
      const inner = scrollers().some(el => el.scrollTop > 200);
      btn.classList.toggle('show', window.scrollY > 200 || inner);
    });
  };
  document.addEventListener('scroll', update, true);
  window.addEventListener('resize', update);
  btn.addEventListener('click', () => {
    window.scrollTo({ top: 0, behavior: 'smooth' });
    scrollers().forEach(el => { if (el.scrollTop > 0) el.scrollTo({ top: 0, behavior: 'smooth' }); });
  });
  update();
}

boot();
initBackToTop();
connectSSE();
// 2026-11-08：定时刷新统一走 startAdaptivePoll（helper.js）：页面隐藏时全部停手，
// 切回前台立即补一轮，后台标签页不再每秒/每 15 秒白跑。
// 顶部计时器只读内存里的扫描进度、不发请求，固定 1 秒。
startAdaptivePoll(async () => {
  if (S.status && S.status.state && S.status.state.running) renderProgress(S.status.state);
  return true;
}, { base: 1000, max: 1000 });
// v3.10.0 UI-04：这里只补顶部状态栏，不刷列表。列表刷新只走 SSE 与切页，
// 15 秒轮询刻意不碰列表——避免定时唤醒磁盘，也不能在用户输入时重绘表单。
startAdaptivePoll(async () => {
  if (S.tab !== 'settings') await refreshStatus().catch(() => {});
  return true;
}, { base: 15000, max: 15000 });

// v3.9.6：统一兜底 bthumb + rowthumb（V-06 真机坐实：7/7 损坏视频在 /api/thumb 上返回 500，之前只处理了 bthumb）
document.addEventListener('error', (e) => {
  const t = e.target;
  if (!t || t.tagName !== 'IMG') return;
  let cls = null;
  if (t.classList.contains('bthumb')) cls = 'bthumb ph';
  else if (t.classList.contains('rowthumb')) cls = 'rowthumb ph';
  else return;
  const ph = document.createElement('span');
  ph.className = cls;
  ph.textContent = t.dataset.ext || '—';
  t.replaceWith(ph);
}, true);

document.addEventListener('keydown', (e) => { if (e.key === 'Escape') { closeModal(); closeDirPicker(); document.body.classList.remove('nav-open'); /* v3.9.6 修 G-02：Escape 关抽屉时同步复位 aria-expanded，让读屏准确知道抽屉关闭 */ const nt = $('navToggle'); if (nt) nt.setAttribute('aria-expanded', 'false'); } });
// v3.5.3：目录弹窗里粘贴路径后回车直接转到（与新建查重的粘贴框一致）
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Enter' || !e.target || e.target.id !== 'dirManual') return;
  const btn = $('dirModal') ? $('dirModal').querySelector('[data-act="dir-manual-go"]') : null;
  if (btn) { e.preventDefault(); btn.click(); }
});
// 窄屏：侧边栏收成抽屉，菜单按钮开合，点遮罩关闭
(function bindNavToggle(){
  const t = $('navToggle'), scrim = $('scrim');
  if (!t) return;
  t.addEventListener('click', () => {
    const open = document.body.classList.toggle('nav-open');
    t.setAttribute('aria-expanded', open ? 'true' : 'false');
  });
  if (scrim) scrim.addEventListener('click', () => {
    document.body.classList.remove('nav-open');
    t.setAttribute('aria-expanded', 'false');
  });
})();
$('modal').addEventListener('click', (e) => { if (e.target === $('modal')) closeModal(); });
$('dirModal').addEventListener('click', (e) => { if (e.target === $('dirModal')) closeDirPicker(); });

window.addEventListener('resize', () => { if (isResultTab()) renderVirtual(true); });
