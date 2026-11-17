// 查重助手 verify Web UI · 接口调用与数据操作（v3.0.0 从 app.js 拆分）
// 加载顺序第 2：所有 load/do/save 类请求、扫描控制、设置保存与搜索结果操作。
// 依赖 helper.js 的全局工具；函数体内引用后续文件里的渲染函数，调用时已就绪。

function applyScope(groups){
  let out = groups.slice();
  if (S.scopeSelOnly) out = out.filter(selectedGroup);
  if (S.scopeUnmarkedOnly) out = out.filter(unmarkedGroup);
  return out;
}

function applyFilters(groups){
  const lf = S.listFilter;
  const anyState = lf.marked || lf.unmarked || lf.partial || lf.allMarked || lf.protected;
  const hasRange = lf.countMin !== '' || lf.countMax !== '' || lf.sizeMin !== '' || lf.sizeMax !== '' || lf.fileMin !== '' || lf.fileMax !== '';
  if (!anyState && !hasRange) return groups;
  const sizeMin = lf.sizeMin !== '' ? Number(lf.sizeMin) * 1024 * 1024 : null; // UI 单位 MB
  const sizeMax = lf.sizeMax !== '' ? Number(lf.sizeMax) * 1024 * 1024 : null;
  const fileMin = lf.fileMin !== '' ? Number(lf.fileMin) * 1024 * 1024 : null;
  const fileMax = lf.fileMax !== '' ? Number(lf.fileMax) * 1024 * 1024 : null;
  const out = [];
  for (const g of groups) {
    const files = g.files || [];
    if (anyState && !groupMatchState(g)) continue;
    if (lf.countMin !== '' || lf.countMax !== '') {
      if (!numInRange(files.length, lf.countMin === '' ? null : Number(lf.countMin), lf.countMax === '' ? null : Number(lf.countMax))) continue;
    }
    if (!numInRange(g.total_size, sizeMin, sizeMax)) continue;
    if (lf.fileMin !== '' || lf.fileMax !== '') {
      const kept = files.filter(f => numInRange(f.size, fileMin, fileMax));
      if (!kept.length) continue;
      if (!lf.showAll) { out.push(Object.assign({}, g, { files: kept })); continue; }
    }
    out.push(g);
  }
  return out;
}

function resetAssistCache(){
  S.assistGroups = null;
  S.assistPromise = null;
  S.assistTruncated = false;
}

async function ensureAssistGroups(){
  if (S.assistGroups) return S.assistGroups;
  if (S.assistPromise) return S.assistPromise;
  if (!isResultTab()) return [];
  // v3.8.0：合并结果页直接使用快照已加载/可见的分组，不再走旧的逐页预取接口。
  if (S.tab === 'results') return S.groups || [];
  const tab = S.tab, filters = listFilters(), introN = S.introN;
  S.assistPromise = (async () => {
    S.assistLoading = true; renderAssistant();
    const out = [];
    let total = 0;
    try {
      for (let page = 1; page <= 60; page++) {
        const params = new URLSearchParams({ page: String(page), page_size: '200' });
        params.set('sort', S.sort || 'waste');
        params.set('order', S.sortOrder || 'desc');
        let url;
        if (tab === 'video') url = '/api/groups/video?' + params.toString();
        else if (tab === 'intro') { params.set('n', String(introN || 10)); url = '/api/groups/intro?' + params.toString(); }
        else if (tab === 'folder') url = '/api/groups/folder?' + params.toString();
        else if (tab === 'audio') url = '/api/groups/audio?' + params.toString();
        else {
          params.set('mode', tab);
          const minB = filters.minKB ? String(Math.round(Number(filters.minKB) * 1024)) : '';
          if (minB) params.set('min_size', minB);
          if (filters.ext) params.set('ext', filters.ext);
          if (filters.q) params.set('q', filters.q);
          url = '/api/groups?' + params.toString();
        }
        const r = /** @type {GroupsResp} */ (await api(url));
        const list = r.groups || [];
        out.push(...list); total = Number(r.total || 0);
        if (out.length >= total || !list.length || out.length >= ASSIST_MAX_GROUPS) break;
      }
      if (S.tab === tab) {
        S.assistGroups = out;
        // 2026-11-08：记下实际覆盖组数，界面用它显示「覆盖 N / 共 M 组」，不再写死 3000
        S.assistCovered = out.length;
        S.assistTotal = total;
        S.assistTruncated = out.length < total;
      }
      return out;
    } catch(e) {
      toast('加载全部结果失败：' + e.message, 'err');
      return S.groups || [];
    } finally {
      S.assistLoading = false; S.assistPromise = null; renderAssistant();
    }
  })();
  return S.assistPromise;
}

async function confirmBigGroups(groups){
  const big = (groups || []).filter(g => (g.files || []).length > 20);
  if (!big.length) return true;
  const max = Math.max.apply(null, big.map(g => g.files.length));
  return confirm(`有 ${big.length} 个组超过 20 个文件（最大 ${max} 个），批量标记可能涉及大量文件，是否继续？`);
}

async function jumpFirstAllMarked(){
  const groups = await ensureAssistGroups();
  const info = allMarkedInfo(groups);
  if (!info.length) { toast('没有全标记组', 'ok'); return; }
  const key = info[0].key;
  let guard = 0;
  while (!S.groups.some(g => g.key === key) && S.hasMore && guard++ < 40) await loadMoreGroups();
  if (!S.groups.some(g => g.key === key)) { toast('该分组尚未加载，请先向下滚动加载更多结果', 'err'); return; }
  scrollToGroup(key);
}

async function runGroupMark(kind){
  readAssistUI();
  const all = await ensureAssistGroups();
  const groups = applyFilters(applyScope(all));
  if (!groups.length) { toast('当前范围内没有可操作的分组', 'err'); return; }
  if (!(await confirmBigGroups(groups))) return;
  // 2026-11-08：批量标记只作用于「已加载」的分组。库比已加载多时先说明清楚，
  // 否则用户会以为「标记全部」= 整库，未加载的部分被静默跳过。
  const knownTotal = Number(S.total || 0);
  const missing = Math.max(0, knownTotal - all.length);
  if (missing > 0 && !confirm(`当前只加载了 ${all.length} / ${knownTotal} 组，批量操作不会处理未加载的 ${missing} 组。\n可先取消，点列表上方的「加载剩余全部」后再执行。仍要继续吗？`)) return;
  snapshotSel(kind === 'pref' || kind === 'pref-exclude' ? '偏好标记' : '组级标记');
  let changed = 0, skipped = 0;
  for (const g of groups) {
    // v3.11.1：编码异常文件不参与任何按组标记，避免坏路径混进选择集导致整批提交被拒
    const files = operableFiles(g);
    if (!files.length) continue;
    let next = new Set();
    let keep = null, worst = null;
    if (kind === 'keep-one' || kind === 'keep-one-folder') {
      const prefs = criterionPrefs(S.keepCriterion);
      keep = chooseKeep(files, prefs);
      if (!keep) { skipped++; continue; }
      // v1.13.0：组内 (volume,inode) 相同的已链接行默认不标记——删它们不省空间；
      // 用户显式勾选仍允许（删一个名字本身合法）
      const kc = {};
      files.forEach(f => { const k = (f.volume||'')+':'+(f.inode ?? ''); kc[k] = (kc[k]||0)+1; });
      const linked = new Set(files.filter(f => kc[(f.volume||'')+':'+(f.inode ?? '')] > 1).map(f => f.path));
      files.forEach(f => { if (f.path !== keep.path && !linked.has(f.path)) next.add(f.path); });
      if (kind === 'keep-one-folder') {
        const folders = {};
        files.forEach(f => {
          const d = f.path.slice(0, f.path.lastIndexOf('/'));
          (folders[d] = folders[d] || []).push(f);
        });
        Object.values(folders).forEach(arr => {
          if (arr.every(f => next.has(f.path))) {
            const k = chooseKeep(arr, prefs) || arr[0];
            if (k) next.delete(k.path);
          }
        });
      }
    } else if (kind === 'mark-one') {
      worst = chooseWorst(files, criterionPrefs(S.keepCriterion));
      if (worst) next = new Set([worst.path]);
    } else if (kind === 'pref') {
      keep = chooseKeep(files, S.prefs);
      if (!keep) { skipped++; continue; }
      files.forEach(f => { if (f.path !== keep.path) next.add(f.path); });
    } else if (kind === 'pref-exclude') {
      // v1.41：按偏好标记排除——每组只标记偏好最差的一个
      worst = chooseWorst(files, S.prefs);
      if (!worst) { skipped++; continue; }
      next = new Set([worst.path]);
    }
    if (S.tab === 'folder') {
      // v1.24.0：文件夹页目录级语义——keep-one/pref 保留 keep 所在目录、
      // mark-one 标记 worst 所在目录；结果写入 S.folderSel（扁平勾选集合）。
      const dirOf = p => p.slice(0, p.lastIndexOf('/'));
      const anchorDir = (kind === 'mark-one') ? (worst ? dirOf(worst.path) : null) : (keep ? dirOf(keep.path) : null);
      const byDir = {};
      files.forEach(f => { const d = dirOf(f.path); (byDir[d] = byDir[d] || []).push(f); });
      Object.entries(byDir).forEach(([d, arr]) => {
        const markDir = (kind === 'mark-one') ? (d === anchorDir) : (d !== anchorDir);
        if (markDir) arr.forEach(f => { next.add(f.path); S.folderSel.add(f.path); });
        else arr.forEach(f => S.folderSel.delete(f.path));
      });
      changed += S.folderSel.size;
    } else {
      S.sel[g.key] = next;
      changed += next.size;
    }
  }
  renderAssistant(); syncCheckboxes();
  toast(`已更新标记，共标记 ${changed} 个文件${skipped?`；${skipped} 组无法按偏好区分，已跳过`:''}`, skipped?'err':'ok');
}

async function runLocationMark(){
  readAssistUI();
  if (!S.keepPaths.length) { toast('请先添加优先保留路径', 'err'); return; }
  const groups = applyFilters(applyScope(await ensureAssistGroups()));
  if (!groups.length) { toast('当前范围内没有可操作的分组', 'err'); return; }
  if (!(await confirmBigGroups(groups))) return;
  snapshotSel('按路径标记');
  let changed = 0, skipped = 0, noOperable = 0;
  for (const g of groups) {
    // v3.11.1：只有可操作文件参与按路径标记
    const files = operableFiles(g);
    // v3.13.0（UI 审查 P3-3）：整组都是编码异常文件时要与「没有保留路径」分开报，
    // 否则用户会去检查保留路径设置，而真正原因是这组文件不可操作。
    if (!files.length) { noOperable++; continue; }
    const keepFiles = files.filter(f => S.keepPaths.some(k => {
      const p = normPath(f.path);
      return p === k || p.startsWith(k + '/');
    }));
    if (!keepFiles.length) { skipped++; continue; }
    const next = new Set();
    files.forEach(f => { if (!keepFiles.some(k => k.path === f.path)) next.add(f.path); });
    S.sel[g.key] = next;
    changed += next.size;
  }
  renderAssistant(); syncCheckboxes();
  const parts = [];
  if (skipped) parts.push(`${skipped} 组没有保留路径`);
  if (noOperable) parts.push(`${noOperable} 组没有可操作文件`);
  toast(`按路径更新标记 ${changed} 个文件${parts.length ? '；' + parts.join('，') + '，已跳过' : ''}`, parts.length ? 'err' : 'ok');
}

async function runTextMark(mark){
  readAssistUI();
  const re = compileTextPattern();
  if (!re) { toast('请输入匹配文本', 'err'); return; }
  const groups = applyFilters(applyScope(await ensureAssistGroups()));
  if (!groups.length) { toast('当前范围内没有可操作的分组', 'err'); return; }
  if (mark && !(await confirmBigGroups(groups))) return;
  snapshotSel(mark ? '按文本标记' : '按文本取消标记');
  let changed = 0;
  for (const g of groups) {
    const sel = S.sel[g.key] || new Set();
    // v3.11.1：标记只作用于可操作文件；取消标记时不主动剔除历史残留的坏路径，
    // 那些由 initSelections 兜底清理（v3.12.1 按界面审查修正注释，行为不变）。
    operableFiles(g).forEach(f => {
      const text = S.textField === 'path' ? f.path : f.name;
      re.lastIndex = 0;
      if (!re.test(text)) return;
      if (mark) sel.add(f.path); else sel.delete(f.path);
      changed++;
    });
    S.sel[g.key] = sel;
  }
  renderAssistant(); syncCheckboxes();
  toast((mark?'已标记匹配项 ':'已取消匹配项 ') + changed + ' 个', 'ok');
}

async function assistInvert(){
  const groups = applyFilters(applyScope(await ensureAssistGroups()));
  if (!groups.length) { toast('当前范围内没有可操作的分组', 'err'); return; }
  snapshotSel('反转标记');
  let changed = 0;
  groups.forEach(g => {
    const sel = S.sel[g.key] || new Set();
    // v3.11.1：反转只翻可操作文件；坏路径顺带从集合里剔除（旧版本可能残留）
    const files = operableFiles(g);
    ((g && g.files) || []).forEach(f => { if (badName(f)) sel.delete(f.path); });
    files.forEach(f => {
      if (sel.has(f.path)) sel.delete(f.path); else { sel.add(f.path); changed++; }
    });
    S.sel[g.key] = sel;
  });
  renderAssistant(); syncCheckboxes();
  toast('已反转标记', 'ok');
}

async function assistClear(){
  const groups = applyFilters(applyScope(await ensureAssistGroups()));
  if (!groups.length) return;
  if (!confirm('取消当前范围内全部标记？')) return;
  snapshotSel('取消全部标记');
  groups.forEach(g => { S.sel[g.key] = new Set(); });
  renderAssistant(); syncCheckboxes();
  toast('已取消全部标记', 'ok');
}

function assistUndo(){
  const last = S.undo.pop();
  if (!last) { toast('没有可撤销的选择操作', 'err'); return; }
  S.sel = last.sel;
  renderAssistant(); syncCheckboxes();
  toast('已撤销：' + last.label + (S.undo.length ? '（还可再撤 ' + S.undo.length + ' 步，连点逐级回退）' : '（已到最早一步）'), 'ok');
}

function applyWizardModeBases(w){
  if (!w) return;
  // v3.6.0：自定义组合时由勾选的条件联动扫描依据；预设沿用旧映射
  if (w.mode === 'custom') { syncWizardBasesFromKinds(w); return; }
  if (w.mode === 'all') return;
  const only = w.mode === 'exact' ? 'content'
    : w.mode === 'similar' ? 'video'
    : w.mode === 'ignore_content' ? 'name' : '';
  ['content', 'name', 'video', 'intro'].forEach(k => { w[k] = (k === only); });
}

// syncWizardBasesFromKinds 只补开对应扫描依据，不强行关掉用户已勾的项：
// 依据是“扫描时算不算”，多算不会影响结果正确性，只会多花一点时间。
function syncWizardBasesFromKinds(w){
  const kinds = new Set(w.kinds || []);
  if (kinds.has('exact')) w.content = true;
  if (kinds.has('name') || kinds.has('fuzzy') || kinds.has('semantic')) w.name = true;
  if (kinds.has('video')) w.video = true;
  if (kinds.has('intro')) w.intro = true;
}

// applyWizardRulesBases v3.8.0：按搜索规则 base 自动决定扫描依据与媒体开关。
// 规则是“比什么”，扫描依据是“数据算不算”；这里保证选了类型就一定有对应数据。
function applyWizardRulesBases(w){
  if (!w || !w.rules) return;
  const b = w.rules.base;
  w.content = b === 'content' || b === 'folder' || b === 'all';
  w.name = b === 'all' || b === 'name' || b === 'fuzzy' || b === 'semantic';
  w.video = b === 'video' || b === 'all';
  w.intro = b === 'intro' || b === 'all';
  w.audio = b === 'audio' || b === 'all';
  w.photo = b === 'photo' || b === 'all';
}

function readWizardInputs(){
  const w = S.wizard;
  if (!w) return;
  if ($('wizBase')) w.rules.base = $('wizBase').value;
  const attrEls = document.querySelectorAll('[data-act="wiz-attr"]');
  if (attrEls.length) w.rules.attrs = [...attrEls].filter(x => x.checked).map(x => x.dataset.attr);
  if ($('wizSizeTol')) w.rules.size_tolerance_kb = Math.max(0, Math.min(1048576, Number($('wizSizeTol').value || 0)));
  if ($('wizSimilarPct')) w.rules.name_similar_pct = Math.max(50, Math.min(100, Number($('wizSimilarPct').value || 90)));
  if ($('wizFolderMode')) w.rules.folder_name_mode = $('wizFolderMode').value;
  if ($('wizFolderDepth')) w.rules.folder_name_depth = Math.max(1, Math.min(50, Number($('wizFolderDepth').value || 1)));
  if ($('wizIgnoreCopy')) w.rules.ignore_copy_names = $('wizIgnoreCopy').checked;
  if ($('wizIgnoreSame')) w.rules.ignore_same_folder = $('wizIgnoreSame').checked;
  if ($('wizCase')) w.rules.case_sensitive = $('wizCase').checked;
  if ($('wizMin')) w.minKB = Math.max(0, Number($('wizMin').value || 0));
  if ($('wizDupFolders')) w.dupFolders = $('wizDupFolders').checked;
  applyWizardRulesBases(w);
}

async function wizardNext(){
  const w = S.wizard;
  if (!w) return;
  readWizardInputs();
  if (w.step === 1) {
    // v3.8.0：忽略内容模式必须至少选一个属性条件，否则分组键为空。
    if (w.rules.base === 'attributes' && !w.rules.attrs.length) {
      toast('忽略内容模式至少要勾选一个比较条件', 'err'); return;
    }
    w.step = 2; renderWizard(); return;
  }
  if (w.step === 2) {
    tfWriteBack(); // v1.28.0：穿梭框写回 扫描/排除/保护 三类路径
    if (!w.scanDirs.length) { toast('请至少选择一个扫描文件夹（左侧点选 → › 加入）', 'err'); return; }
    w.step = 3; renderWizard(); return;
  }
  await startWizardScan();
}

async function startWizardScan(){
  const w = S.wizard;
  if (!w || w.starting) return;
  readWizardInputs();
  if (w.rules.base === 'attributes' && !w.rules.attrs.length) {
    toast('忽略内容模式至少要勾选一个比较条件', 'err'); w.step = 1; renderWizard(); return;
  }
  if (!w.scanDirs.length) { toast('请至少选择一个扫描文件夹', 'err'); w.step = 2; renderWizard(); return; }
  const base = w.rules.base;
  const needFull = base === 'video' || base === 'intro' || base === 'audio' || base === 'photo' || base === 'all';
  w.starting = true; renderWizard();
  try {
    await postJSON('/api/settings', {
      content_enabled: w.content,
      name_enabled: w.name,
      video_enabled: w.video,
      intro_enabled: w.intro,
      intro_seconds: w.introSeconds,
      min_size: Math.round(w.minKB * 1024),
      compute_dup_folders: (w.dupFolders !== false) || base === 'folder',
      audio_enabled: w.audio ? true : undefined,
      photo_enabled: w.photo ? true : undefined,
      search_rules: w.rules,
      volumes: w.scanDirs,
      exclude_paths: w.excludeDirs,
      protect_paths: Array.isArray(w.protectPaths) ? w.protectPaths : undefined
    });
    S.jobTabs = [];
    // v3.10.8：重新扫描后结果集会整体换掉，先清空旧勾选，避免新结果里同名键带着旧路径。
    clearSelection();
    await postJSON('/api/scan', { mode: needFull ? 'full' : 'quick' });
    // 扫描完成后由 refreshStatus 自动进入搜索阶段（见 app.js）。
    S.pendingRuleSearch = true;
    S.wizard = null;
    await refreshStatus();
    updateTabVisibility();
    await switchTab('overview');
    toast('已开始查重，完成后会自动打开结果', 'ok');
  } catch(e) {
    w.starting = false;
    if (S.wizard) renderWizard();
    toast(e.message, 'err');
  }
}

// 2026-11-08：上限从 30 页（3000 组）抬到 120 页（12000 组）。
// 依据：单组 JSON 约 0.6KB（2 个文件 × 220B + 组字段 200B），12000 组约 7.5MB 文本、
// V8 堆约 18MB；虚拟列表只渲染可视区约 30 行，DOM 不随组数增长。
// 再往上（>2 万组）建议用筛选器收窄，而不是继续堆内存。
const MAX_LOADED_PAGES = 120;
// 选择助手全量预取上限：与列表上限保持一致，超限时界面显示实际覆盖组数。
const ASSIST_MAX_GROUPS = 12000;

async function loadMoreGroups(){
  if (S.tab === 'results') { await loadMoreRuleResults(); return; }
  if (S.loadingMore || !S.hasMore) return;
  if (S.loadedPage >= MAX_LOADED_PAGES) {
    S.hasMore = false;
    updateListStatus();
    toast(`结果过多，已加载前 ${MAX_LOADED_PAGES * 100} 组；还有 ${Math.max(0, (S.total || 0) - S.groups.length)} 组未加载，请用「筛选器」按大小/目录/时间收窄后再翻页`, 'warn', 6000);
    return;
  }
  S.loadingMore = true;
  updateListStatus();
  try {
    const r = await fetchChunk(S.tab, S.loadedPage + 1, filtersForTab(S.tab));
    const list = r.groups || [];
    if (list.length) {
      const seen = new Set(S.groups.map(g => g.key));
      list.forEach(g => { if (!seen.has(g.key)) S.groups.push(g); });
      S.loadedPage++;
      // 第二页起后端 total=-1（跳过 COUNT），保留第一页算出的总数
      if (Number(r.total) >= 0) S.total = Number(r.total);
      extendVirtualList();
      initSelections();
      renderVirtual(true);
    }
    if (!list.length || S.groups.length >= S.total) S.hasMore = false;
  } catch(e) { toast(e.message, 'err'); S.hasMore = false; }
  finally { S.loadingMore = false; updateListStatus(); }
}

// ---------- v3.8.0 规则搜索与全量快照 ----------

function ruleSearchState(){
  if (!S.ruleSearch) {
    S.ruleSearch = { state:'none', loaded:0, total:0, totals:null, stale:false, error:'', id:'' };
  }
  return S.ruleSearch;
}

// ensureIntroStats 拉取「已分析 / 待分析」计数（v3.16.0）。
// 结果页的数据来自规则搜索快照，本身不带这两个数字；独立片头页合并后，
// 只能从只读的 /api/groups/intro 取一次。force=true 用于重新分析后立即刷新。
async function ensureIntroStats(force){
  const now = Date.now();
  if (!force && S.introStatsAt && now - S.introStatsAt < 30000) return;
  try {
    const n = Math.min(300, Math.max(1, Number(S.introN || 10)));
    const r = /** @type {GroupsResp} */ (await api('/api/groups/intro?n=' + n + '&page=1&page_size=1'));
    S.introAnalyzed = Number(r.analyzed || 0);
    S.introPending = Number(r.pending || 0);
    S.introStatsAt = Date.now();
    if (S.tab === 'results' && (S.resultType || 'all') === 'intro') renderGroups();
  } catch(e) {
    // 片头检测未启用或接口不可用时保持原值：重新分析按钮仍可用，由后端给出具体原因。
  }
}

// loadRuleResults 进入「结果」页：无快照就发起一次搜索，构建中显示进度，就绪则拉首屏。
async function loadRuleResults(reset){
  const rs = ruleSearchState();
  try {
    const st = /** @type {RuleSearchStatusResp} */ (await api('/api/rule-search/status'));
    Object.assign(rs, st || {});
    if (st.state === 'none') { renderRuleSearchProgress({ state:'none' }); await startRuleSearch(); return; }
    if (st.state === 'building') { renderRuleSearchProgress(st); scheduleRulePoll(); return; }
    if (st.state === 'error') { renderRuleSearchError(st.error || '搜索失败'); return; }
    // ready：从快照取首页
    if (rulePoll) { rulePoll.stop(); rulePoll = null; }
    await loadRuleChunk(1, true);
    // v3.10.13：收窄条独立请求，不阻塞首屏渲染；接口未就位时内部静默降级
    loadFacets();
    // v3.16.0：片头类型的「已分析 / 待分析」来自独立接口，首屏渲染后补一次。
    if ((S.resultType || 'all') === 'intro') ensureIntroStats();
  } catch(e) { toast(e.message, 'err'); renderRuleSearchError(e.message); }
}

async function startRuleSearch(){
  const rs = ruleSearchState();
  try {
    await postJSON('/api/rule-search/run', {});
    // v3.9.1：新一次搜索从零开始，清掉上一轮勾选，避免默认“看似已勾选”。
    S.sel = {};
    rs.state = 'building';
    renderRuleSearchProgress({ state:'building', loaded:0, total:0 });
    // 2026-11-08：新一轮搜索从 0 开始，旧轮询的 lastLoaded 还停在上一轮，
    // 必须停掉重建，否则会误判「无进展」直接退避。
    if (rulePoll) { rulePoll.stop(); rulePoll = null; }
    scheduleRulePoll();
  } catch(e) {
    rs.state = 'error'; rs.error = e.message;
    renderRuleSearchError(e.message);
  }
}

let rulePoll = null;

function scheduleRulePoll(){
  if (rulePoll && rulePoll.active) return;
  let lastLoaded = -1;
  // 2026-11-08：搜索构建进度也走统一的自适应节奏（有进展 1 秒，连续无进展退避到 4 秒），
  // 页面隐藏时停手。构建阶段后端在跑全库比对，前端没必要死守 1 秒一次。
  rulePoll = startAdaptivePoll(async () => {
    const rs = ruleSearchState();
    const st = /** @type {RuleSearchStatusResp} */ (await api('/api/rule-search/status'));
    Object.assign(rs, st || {});
    if (st.state === 'ready') {
      rulePoll = null;
      // v3.11.1：重扫/新建搜索完成后快照已换，旧的收窄统计（chip 计数、目录条）必须作废重取，
      // 否则界面会停在上一轮的计数上。
      // v3.12.1（UI 审查 P2-1）：旧快照的目录在新快照里可能已经没有重复组，
      // 继续带着 dir 请求只会得到空列表；新快照从「全部」开始，让用户自己重新收窄。
      S.facets = null; S.facetsDown = ''; S.facetsLoading = true;
      S.dirFilter = '';
      S.dirsExpanded = false;
      if (S.tab === 'results') { await loadRuleChunk(1, true); await loadFacets(); }
      toast('搜索完成：共 ' + (st.totals && st.totals.groups || 0) + ' 组，可释放 ' + fmtB(st.totals && st.totals.waste || 0), 'ok', 5000);
      return false;
    }
    if (st.state === 'error') { rulePoll = null; renderRuleSearchError(st.error || '搜索失败'); return false; }
    if (st.state === 'canceled') { rulePoll = null; renderRuleSearchError('搜索已取消'); return false; }
    if (S.tab === 'results') renderRuleSearchProgress(st);
    const loaded = Number(st.loaded || 0);
    const changed = loaded > lastLoaded;
    lastLoaded = loaded;
    // 注意：false 在控制器里表示「结束」，无变化必须返回 undefined（退避）
    return changed ? true : undefined;
  }, { base: 1000, max: 4000 });
}

async function loadRuleChunk(page, reset, quiet){
  const rs = ruleSearchState();
  // v3.11.1：请求代次。切类型 / 切目录 / 改关键字 / 换排序都会 reset=true 并自增，
  // 之前在飞的滚动加载回来时代次已变，结果直接丢弃——否则旧类别数据会追加进新列表。
  const gen = reset ? ++S.ruleGen : S.ruleGen;
  const p = new URLSearchParams({
    page: String(page), page_size: String(S.pageSize || 100),
    type: S.resultType || 'all', sort: S.sort || 'waste', order: S.sortOrder || 'desc',
  });
  if (S.listFilter.q) p.set('q', S.listFilter.q);
  // v3.10.13：目录收窄走约定参数 dir（后端 facets 的键），语义是「该顶层目录及其子目录」
  if (S.dirFilter) p.set('dir', S.dirFilter);
  S.loading = true;
  // v3.11.1：重置期间把总数置回 null（未拿到），而不是 0——否则「全部」chip 会把
  // 「还没加载」误当成「确实是 0 组」显示出来
  if (reset) { S.groups = []; S.total = null; S.loadedPage = 1; S.hasMore = false; S.vlist = null; renderGroups(); }
  try {
    const r = await api('/api/rule-search/results?' + p.toString());
    if (gen !== S.ruleGen) return; // 已被更新的一次切换取代，旧数据不落地
    const list = r.groups || [];
    if (reset) S.groups = list;
    else {
      const seen = new Set(S.groups.map(g => g.key));
      list.forEach(g => { if (!seen.has(g.key)) S.groups.push(g); });
      // v3.10.8：追加第二页后同步虚拟列表高度，否则新组永远落在渲染区间之外，滚到底也看不到。
      extendVirtualList();
    }
    S.total = Number(r.total || 0);
    S.loadedPage = page;
    // v3.10.13：优先用后端 has_more（两万组以上分段加载）；老包没有该字段时退回数量比较
    S.hasMore = r.has_more !== undefined ? !!r.has_more : S.groups.length < S.total;
    initSelections();
    rs.state = 'ready';
  } catch(e) {
    if (gen === S.ruleGen) toast(e.message, 'err');
  }
  finally { if (gen === S.ruleGen) { S.loading = false; if (!quiet) renderGroups(); } }
}

// v3.10.13：结果快照的收窄口径（按类型 / 按顶层目录计数）。
// 约定 GET /api/rule-search/facets?type=&dir=&q= → { total, types:[{type,count}], dirs:[{dir,count}] }
// types 始终是「按目录 + 关键字过滤后」的全量分布（切换类别用），type 只影响 dirs 与 total；dir 含子目录。
// v3.11.1：q 必须一并传给 facets，否则「共 M 组」与列表总数会分叉；同一 (type,dir,q) 下
// facets.total 与 results.total 由后端保证相等。
// 无快照时后端整个接口返回 409：这是「暂不可用」，不能当成「没有目录」的空结果。
async function loadFacets(){
  if (S.tab !== 'results') return;
  // v3.11.1：facets 是独立请求，和结果列表共用同一个代次。切目录/类型/关键字会自增
  // S.ruleGen，旧的 facets 响应后到时直接丢弃，否则旧统计会覆盖新数据（计数对不上列表）。
  const gen = S.ruleGen;
  try {
    const p = new URLSearchParams({ type: S.resultType || 'all' });
    if (S.dirFilter) p.set('dir', S.dirFilter);
    if (S.listFilter.q) p.set('q', S.listFilter.q);
    const f = await api('/api/rule-search/facets?' + p.toString());
    if (gen !== S.ruleGen) return;
    S.facets = {
      types: Array.isArray(f && f.types) ? f.types : [],
      dirs: Array.isArray(f && f.dirs) ? f.dirs : [],
      total: Number(f && f.total) || 0
    };
    S.facetsDown = '';
  } catch(e) {
    if (gen !== S.ruleGen) return;
    S.facets = null;
    // 409 = 快照还没生成，给一句「暂不可用」；404 等（老包没有该接口）保持静默不占版面
    S.facetsDown = (e && e.status === 409)
      ? '结果快照尚未就绪，按目录收窄暂不可用（重新搜索后再试）' : '';
  }
  // v3.13.0：代次校验通过才收掉「更新中」占位，被新请求取代时交给新请求收尾。
  S.facetsLoading = false;
  // 只刷新收窄条本身，避免整页重排打断滚动位置
  const host = $('facetsBarHost');
  if (host) host.innerHTML = facetsInnerHtml();
}

// v3.9.10：规则搜索快照导出。后端按当前类型/关键字筛选后直接产出 CSV，
// 让下载内容与界面所见一致；快照未就绪时后端仍返回 409。
async function exportRuleSearch(){
  try {
    const p = new URLSearchParams({
      type: S.resultType || 'all', sort: S.sort || 'waste', order: S.sortOrder || 'desc',
    });
    if (S.listFilter.q) p.set('q', S.listFilter.q);
    const r = await fetch('/api/rule-search/export?' + p.toString());
    if (r.status === 401) { showLogin(); return; }
    if (!r.ok) { let m = '导出失败'; try { const j = await r.json(); m = j.error || m; } catch(e){} throw new Error(m); }
    const blob = await r.blob();
    let name = 'verify-rule-search.csv';
    const cd = r.headers.get('Content-Disposition') || '';
    const mt = cd.match(/filename="([^"]+)"/);
    if (mt) name = mt[1];
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 5000);
    toast('已导出当前筛选结果（CSV）', 'ok');
  } catch(e) { toast(e.message, 'err'); }
}

async function loadMoreRuleResults(){
  if (S.loadingMore || !S.hasMore) return;
  S.loadingMore = true; updateListStatus();
  try {
    await loadRuleChunk(S.loadedPage + 1, false);
    renderVirtual(true);
  } finally { S.loadingMore = false; updateListStatus(); }
}

// 2026-11-08：显式「加载剩余全部」。默认仍只取首屏 100 组，避免一进结果页
// 就把整库塞进内存；用户点按钮时才逐页续载，中途只更新进度条不重绘整页
// （loadRuleChunk 的 quiet 参数），全部到位后统一渲染一次。
async function loadAllGroups(){
  if (S.loadingAll) return;
  S.loadingAll = true; updateListStatus();
  try {
    let guard = 0;
    // 与滚动加载共用同一个内存上限：结果页单页 100 组，120 页 = 12,000 组。
    // 到了上限就停下，由列表上方提示条引导用筛选器收窄，而不是继续堆内存。
    const ceiling = MAX_LOADED_PAGES * (S.pageSize || 100);
    while (S.hasMore && S.groups.length < ceiling && guard++ < 400) {
      const before = S.groups.length;
      if (S.tab === 'results') await loadRuleChunk(S.loadedPage + 1, false, true);
      else await loadMoreGroups();
      if (S.groups.length === before) break; // 没有进展（到达上限或接口报错）就停，避免死循环
      updateListStatus();
    }
    if (S.tab === 'results') renderGroups();
  } finally {
    S.loadingAll = false; updateListStatus();
  }
  const gap = Math.max(0, Number(S.total || 0) - S.groups.length);
  if (gap > 0) toast(`已加载 ${S.groups.length} / ${S.total} 组，剩余 ${gap} 组请用筛选器收窄`, 'warn', 6000);
  else toast(`已加载全部 ${S.groups.length} 组`, 'ok');
}

async function loadGroups(mode){
  S.loading = true; S.groups = []; S.total = 0; S.loadedPage = 1; S.hasMore = false; S.vlist = null; renderGroups();
  const filters = Object.assign({}, filtersForTab(mode));
  const pf = takePrefetch(mode, filters);
  if (pf) { applyPrefetch(pf); return; }
  try {
    const r = await fetchChunk(mode, 1, filters);
    S.groups = r.groups || []; S.total = Number(r.total || 0);
    S.hasMore = S.groups.length < S.total;
    initSelections();
  } catch(e) {
    toast(e.message, 'err'); S.groups = []; S.total = 0;
  } finally { S.loading = false; renderGroups(); }
}

async function loadVideo(){
  S.loading = true; S.groups = []; S.total = 0; S.loadedPage = 1; S.hasMore = false; S.vlist = null; renderGroups();
  const pf = takePrefetch('video', {});
  if (pf) {
    applyPrefetch(pf);
    loadVideoFailures().then(() => { if (S.tab === 'video') renderGroups(); });
    return;
  }
  try {
    const r = await fetchChunk('video', 1, {});
    S.groups = r.groups || []; S.total = Number(r.total || 0);
    S.hasMore = S.groups.length < S.total;
    initSelections();
    await loadVideoFailures();
  } catch(e) {
    toast(e.message, 'err'); S.groups = []; S.total = 0;
  } finally { S.loading = false; renderGroups(); }
}

async function loadVideoFailures(){
  try {
    const fr = await api('/api/video/failures');
    S.videoFails = fr.items || [];
  } catch(e) { S.videoFails = []; }
}

async function loadIntro(){
  S.loading = true; S.groups = []; S.total = 0; S.loadedPage = 1; S.hasMore = false; S.vlist = null; renderGroups();
  const pf = takePrefetch('intro', {});
  if (pf) { applyPrefetch(pf); return; }
  try {
    const r = await fetchChunk('intro', 1, {});
    S.groups = r.groups || []; S.total = Number(r.total || 0);
    S.introAnalyzed = r.analyzed || 0;
    S.introPending = r.pending || 0;
    S.hasMore = S.groups.length < S.total;
    initSelections();
  } catch(e) {
    toast(e.message, 'err'); S.groups = []; S.total = 0;
  } finally { S.loading = false; renderGroups(); }
}

function reportMetric(name, ms, detail){
  try {
    fetch('/api/metric', {
      method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'verify' },
      body: JSON.stringify({ name, ms: Math.round(ms || 0), detail: detail || '' })
    }).catch(() => {});
  } catch(e) {}
}

// 扫描接近收尾时，结果分组已基本稳定；提前取第一块，扫描完成后直接渲染。
function nearScanDone(state, mode){
  const p = state.total > 0 ? state.done / state.total : 0;
  const remain = state.total - state.done;
  if (state.mode === 'quick') {
    return (state.phase === 'quick' || state.phase === 'full') && (p >= 0.85 || remain <= 8);
  }
  if (mode === 'video') {
    if (state.phase === 'intro') return true;
    const workers = Number((S.status && S.status.config && S.status.config.video_workers) || 2);
    return state.phase === 'video' && (p >= 0.85 || remain <= workers * 2);
  }
  if (mode === 'intro') return state.phase === 'intro' && (p >= 0.85 || remain <= 4);
  return state.phase === 'video' || state.phase === 'intro' ||
    ((state.phase === 'quick' || state.phase === 'full') && p >= 0.9);
}

function maybePrefetch(state){
  if (!state || !state.running || S.prefetching) return;
  if (S.prefetchScanAt === state.started_at) return;
  const mode = (S.jobTabs && S.jobTabs[0]) || (isResultTab() ? S.tab : 'exact');
  if (!nearScanDone(state, mode)) return;
  S.prefetchScanAt = state.started_at;
  S.prefetching = true;
  const filters = Object.assign({}, filtersForTab(mode));
  const sig = filterSig(mode, filters);
  const t0 = performance.now();
  fetchChunk(mode, 1, filters).then(r => {
    S.prefetch = {
      mode, filters, sig,
      groups: r.groups || [], total: Number(r.total || 0), at: Date.now(),
      meta: { analyzed: r.analyzed, pending: r.pending }
    };
    reportMetric('prefetch_first_chunk', performance.now() - t0, mode);
  }).catch(() => { S.prefetchScanAt = 0; })
    .finally(() => { S.prefetching = false; });
}

async function onScanFinished(state){
  S.scanDoneAt = Date.now();
  S.scanFinishAt = S.scanDoneAt;
  S.collapsed.clear();
  resetAssistCache();
  const gl = $('groupList');
  if (gl) gl.scrollTop = 0;
  const jobTab = (S.jobTabs && S.jobTabs.length) ? S.jobTabs[0] : '';
  if (jobTab) {
    S.jobTabs = [];
    await refreshStatus();
    updateTabVisibility();
    const filters = Object.assign({}, filtersForTab(jobTab));
    const pf = takePrefetch(jobTab, filters);
    if (pf && (pf.groups || []).length && tabEnabled(jobTab)) {
      S.tab = jobTab;
      document.querySelectorAll('.tab').forEach(t => t.classList.toggle('active', t.dataset.tab === jobTab));
      applyPrefetch(pf);
      reportMetric('scan_complete_to_first_screen', Date.now() - S.scanDoneAt, 'job-prefetch:' + jobTab);
    } else if (tabEnabled(jobTab)) {
      await switchTab(jobTab);
      reportMetric('scan_complete_to_first_screen', Date.now() - S.scanDoneAt, 'job:' + jobTab);
    }
    return;
  }
  if (isResultTab()) {
    const mode = S.tab;
    const filters = Object.assign({}, filtersForTab(mode));
    const pf = takePrefetch(mode, filters);
    if (pf && (pf.groups || []).length) {
      applyPrefetch(pf);
      reportMetric('scan_complete_to_first_screen', Date.now() - S.scanDoneAt, 'prefetch:' + mode);
      if (mode === 'video') loadVideoFailures().then(() => { if (S.tab === 'video') renderGroups(); });
    } else {
      await refreshStatus();
      await reloadTab();
      reportMetric('scan_complete_to_first_screen', Date.now() - S.scanDoneAt, 'normal:' + mode);
    }
    return;
  }
  await refreshStatus();
  if (S.tab === 'overview') renderOverview(S.status);
}

// ---------- 相似图片（v1.11.0）----------
// 网格布局 + 缩略图（/api/raw 直出原图，NAS 局域网加载可接受）。
// 只有 matched 概念不存在：图片页数据来自当前扫描库，全部可操作。
async function loadPhoto(){
  S.photoSel = S.photoSel || new Set();
  S.loading = true; renderPhoto();
  try {
    const r = await api('/api/groups/photo?sort=' + encodeURIComponent(S.rpSort.photo||'waste') + '&order=' + encodeURIComponent(S.rpSortOrder.photo||'desc'));
    S.photoGroups = r.groups || [];
    S.photoSel = new Set();
    S.photoThreshold = r.threshold;
  } catch(e) { toast(e.message, 'err'); S.photoGroups = []; }
  finally { S.loading = false; renderPhoto(); }
}

// v3.10.10（第六轮 P1-3）：图片/音频/文件夹页单独隔离时补分组键。
// 不带键时后端取不到成员集合，「同组至少保留一份」会直接放行，能整组清空。
// 返回 keys（逐组校验用）与 remain（这些组里未被勾选的成员数，确认框展示用）。
function mediaTrashPlan(groups, sel){
  const keys = [];
  const remain = new Set();
  (groups || []).forEach(g => {
    const files = g.files || [];
    if (files.length < 2 || !g.key) return;      // 单文件组不存在「留一份」的问题
    if (!files.some(f => sel.has(f.path))) return;
    keys.push(g.key);
    files.forEach(f => { if (!sel.has(f.path)) remain.add(f.path); });
  });
  return { keys, remain: remain.size };
}

function trashBody(paths, keys){
  const list = (keys || []).filter(Boolean);
  return { paths, group_key: list[0] || '', group_keys: list };
}

// 确认框标题写明「移走多少 / 组内还剩多少」，避免用户误以为会清空整组。
function trashPreviewTitle(paths, plan){
  if (!plan || !plan.keys.length) return '移入隔离区预览（移走 ' + paths.length + ' 个）';
  return '移入隔离区预览（移走 ' + paths.length + ' 个，组内保留 ' + plan.remain + ' 个）';
}

async function photoTrashSel(){
  const paths = Array.from(S.photoSel);
  if (!paths.length) { toast('先勾选要隔离的图片', 'err'); return; }
  const plan = mediaTrashPlan(S.photoGroups, S.photoSel);
  try {
    const pv = await postJSON('/api/trash/preview', Object.assign({ action:'trash' }, trashBody(paths, plan.keys)));
    showPreviewModal(trashPreviewTitle(paths, plan), pv.items || [], '确认移入隔离区', async () => {
      let r;
      try { r = await postJSON('/api/trash', trashBody(paths, plan.keys)); }
      catch(e) { closeModal(); toast(e.message, 'err'); return; }
      closeModal();
      const items = resultItemsFrom(r, 'moved');
      const fails = items.filter(x => !x.ok);
      if (fails.length) showResultModal('移入隔离区结果：部分失败', items);
      else { await reportPrefOnTrash(r.moved || []); toast('已移入隔离区 ' + (r.moved || []).length + ' 张', 'ok'); }
      // v3.10.5：对账有差异时弹窗列出路径与下一步建议。
      showReconcile(r.reconcile);
      S.photoSel.clear();
      await refreshStatus();
      await loadPhoto();
    });
  } catch(e) { toast(e.message, 'err'); }
}

// batchThumbs 预热视频缩略图（v1.41.10）。
// 只处理视频：图片走 /api/raw 直接用原图，其它类型 ffmpeg 也解不了，
// 以前对 49 个文件一律起 ffmpeg，其中大半白跑一次进程。
// 进度写在工具栏（S.thumbJob），不再靠一闪而过的 toast。
async function batchThumbs(paths, label){
  if (S.thumbJob && S.thumbJob.running) { toast('上一批还在生成，等它跑完再点', 'err'); return; }
  const all = (paths || []).filter(Boolean);
  if (!all.length) { toast('没有可生成的项目', 'err'); return; }
  const vids = all.filter(p => VID_EXT.includes(nameExt(p)));
  const skip = all.length - vids.length;
  if (!vids.length) { toast(`${label}里没有视频，不需要预生成缩略图`, 'err'); return; }
  S.thumbJob = { label, total: vids.length, done: 0, fail: 0, skip, running: true };
  paintThumbProg();
  const CONC = 6;
  let i = 0;
  await Promise.all(Array.from({ length: CONC }, async () => {
    while (i < vids.length) {
      const p = vids[i++];
      try { await api('/api/thumb?path=' + encodeURIComponent(p) + '&t=1'); S.thumbJob.done++; }
      catch(e) { S.thumbJob.fail++; }
      if ((S.thumbJob.done + S.thumbJob.fail) % 3 === 0) paintThumbProg();
    }
  }));
  S.thumbJob.running = false;
  paintThumbProg();
  toast(`缩略图完成：成功 ${S.thumbJob.done}，失败 ${S.thumbJob.fail}` + (skip ? `，跳过 ${skip} 个非视频` : ''), S.thumbJob.fail ? 'err' : 'ok');
  // 生成完换一个批次号重绘，让之前失败的图重新发一次请求（浏览器不会自动重试坏图）
  S.thumbStamp = (S.thumbStamp || 0) + 1;
  if (S.tab === 'big') renderBig();
  else if (S.tab === 'photo') renderPhoto();
}

async function loadAudio(){
  S.audioSel = S.audioSel || new Set();
  S.loading = true; renderAudio();
  try {
    const r = await api('/api/groups/audio?sort=' + encodeURIComponent(S.rpSort.audio||'waste') + '&order=' + encodeURIComponent(S.rpSortOrder.audio||'desc') + advFilterParams());
    S.audioGroups = r.groups || [];
    S.audioSel = new Set();
  } catch(e) { toast(e.message, 'err'); S.audioGroups = []; }
  finally { S.loading = false; renderAudio(); }
}

async function loadFolder(){
  S.folderSel = S.folderSel || new Set();
  S.loading = true; renderFolder();
  try {
    const r = await api('/api/groups/folder?sort=' + encodeURIComponent(S.rpSort.folder||'waste') + '&order=' + encodeURIComponent(S.rpSortOrder.folder||'desc'));
    S.folderDisabled = !!r.disabled;
    S.folderGroups = r.disabled ? [] : (r.groups || []);
    S.folderSel = new Set();
    if (!r.disabled) {
      rpDefaultFold('folder', S.folderGroups); // v1.24.0：重复文件夹组默认折叠
      // 智能预勾选：每组保留字典序第一个目录（同签名目录内容完全相同），其余目录文件全部预选
      (S.folderGroups || []).forEach(g => {
        const byDir = {};
        (g.files || []).forEach(f => {
          const d = f.path.replace(/\/[^/]*\/?$/, '') || '/';
          (byDir[d] = byDir[d] || []).push(f);
        });
        const dirs = Object.keys(byDir).sort();
        dirs.forEach((d, i) => { if (i > 0) byDir[d].forEach(f => S.folderSel.add(f.path)); });
      });
    }
  } catch(e) { toast(e.message, 'err'); S.folderGroups = []; }
  finally { S.loading = false; renderFolder(); }
}

async function mediaTrashSel(kind){
  const selSet = kind === 'audio' ? S.audioSel : S.folderSel;
  const paths = Array.from(selSet);
  if (!paths.length) { toast('先勾选要隔离的项目', 'err'); return; }
  // v3.10.10（第六轮 P1-3）：音频组 / 重复文件夹组也要带分组键，否则整组可被清空。
  const plan = mediaTrashPlan(kind === 'audio' ? S.audioGroups : S.folderGroups, selSet);
  try {
    const pv = await postJSON('/api/trash/preview', Object.assign({ action:'trash' }, trashBody(paths, plan.keys)));
    showPreviewModal(trashPreviewTitle(paths, plan), pv.items || [], '确认移入隔离区', async () => {
      let r;
      try { r = await postJSON('/api/trash', trashBody(paths, plan.keys)); }
      catch(e) { closeModal(); toast(e.message, 'err'); return; }
      closeModal();
      const items = resultItemsFrom(r, 'moved');
      const fails = items.filter(x => !x.ok);
      if (fails.length) showResultModal('移入隔离区结果：部分失败', items);
      else { await reportPrefOnTrash(r.moved || []); toast('已移入隔离区 ' + (r.moved || []).length + ' 项', 'ok'); }
      showReconcile(r.reconcile);
      selSet.clear();
      await refreshStatus();
      if (kind === 'audio') await loadAudio(); else await loadFolder();
    });
  } catch(e) { toast(e.message, 'err'); }
}

// ---------- 硬链接合并（v1.13.0，设计文档 9.2/9.4）----------
// 每组：未勾选的那一行是保留者，勾选行变成指向保留者的硬链接。
// 全程后端逐项预检（同卷/非保护/非隔离区/未链接/当场重算哈希），任何一项不过即跳过。
async function relinkSelected(){
  const jobs = [], problems = [];
  let bytes = 0;
  // v3.10.8 安全修复：硬链接合并会真实改写文件，只能动当前界面上看得到的组；
  // 原先进来的是 S.groups 全量，被筛选器隐藏的组也会被一起合并。
  for (const g of visibleGroups()) {
    const sel = S.sel[g.key] || new Set();
    const files = g.files || [];
    if (!files.length) continue;
    const targets = files.filter(f => sel.has(f.path));
    if (!targets.length) continue;
    const keepers = files.filter(f => !sel.has(f.path));
    if (keepers.length !== 1) {
      problems.push({ path: g.name || g.key, ok: false, reason: keepers.length === 0 ? '组内没有未勾选的保留者' : '组内有 ' + keepers.length + ' 个未勾选行，无法确定唯一保留者' });
      continue;
    }
    targets.forEach(f => bytes += f.size || 0);
    jobs.push({ keep: keepers[0].path, paths: targets.map(f => f.path) });
  }
  if (!jobs.length) { toast('没有可合并的组：需要每组恰好保留一个未勾选行', 'err'); return; }
  // 预检（不产生变更）
  const items = [];
  for (const j of jobs) {
    try {
      const r = await postJSON('/api/relink/preview', j);
      (r.items || []).forEach(it => items.push(it));
    } catch(e) { toast(e.message, 'err'); return; }
  }
  const okN = items.filter(i => i.ok).length;
  if (!okN) { showResultModal('硬链接合并预检：无可执行项', items); return; }
  // 红字确认（设计文档 9.4.5）
  const html = `<div class="relink-warn">⚠ 此操作无法撤销！</div>
    ${previewTable(items)}
    <div class="hint">将合并 ${okN} 个文件到各自保留者，预计释放约 ${fmtB(bytes)} 磁盘空间（按当前文件大小估算）。
    合并后所有路径都还在、都能正常打开；但它们共享同一份数据——修改任何一个，其余全部同步变化。
    如需恢复独立副本：对该路径复制一份替换即可（代价是重新占用空间）。</div>
    <div class="pm-actions"><button class="btn ghost" data-act="modal-cancel">取消</button>
    <button class="btn warn" data-act="relink-confirm">确认合并（不可撤销）</button></div>`;
  setModal('硬链接合并确认', html, null);
  S.relinkJobs = jobs;
}

async function relinkConfirm(){
  const jobs = S.relinkJobs || [];
  // v3.7.2：执行期间标记弹窗忙碌，禁止关闭（请求无法中途取消，P-21）
  S.modalBusy = true;
  setModal('正在合并…', '<div class="hint">执行中，请稍候。</div>', null);
  const items = [];
  try {
    for (const j of jobs) {
      try {
        const r = await postJSON('/api/relink', j);
        (r.items || []).forEach(it => items.push(it));
      } catch(e) { items.push({ path: j.keep, ok: false, reason: e.message }); }
    }
  } finally {
    S.modalBusy = false;
    S.relinkJobs = null;
  }
  showResultModal('硬链接合并结果', items);
  S.sel = {};
  await refreshStatus();
  await refreshView();
}

// ---------- 大文件 Top N（v1.14.0 引入 10.1）----------
// 盘满找大户：按大小排序的已索引文件，勾选移入隔离区（复用预检）。
// 只覆盖已索引文件，受最小体积与扫描根范围限制。
async function loadBig(){
  S.bigSel = S.bigSel || new Set();
  S.loading = true; renderBig();
  try {
    // 画面比例是服务端筛选条件；漏传会让下拉框看似生效、列表却不变（P-03）
    const q = new URLSearchParams({ limit: '100', order: S.bigOrder || 'big' });
    if (S.bigCrop) q.set('crop', S.bigCrop);
    const r = await api('/api/big?' + q.toString());
    S.bigFiles = r.files || [];
    S.bigSel = new Set();
  } catch(e) { toast(e.message, 'err'); S.bigFiles = []; }
  finally { S.loading = false; renderBig(); }
}

// 大文件页批量隔离每批 500 个（v3.19.0）。拆批是为了让进度条持续前进，
// 中途取消时已完成批次各自落库，不回滚。
const BIG_TRASH_BATCH = 500;

// bigCropdetectRun 分批识别竖横屏（v3.19.0）：每批 50 个，进度按服务端返回的
// remaining 推进；可暂停 / 取消，取消后已打标的保留、未处理的不动。
async function bigCropdetectRun(){
  const prev = BATCHED_OPS['big-crop'];
  if (prev && prev.running) { toast('识别正在进行中', 'warn'); return; }
  let labeled = 0;
  await runBatchedOp({
    id: 'big-crop', label: '识别竖横屏', batchSize: 50, maxBatches: 10000,
    call: async (chunk, op) => {
      const r = await postJSON('/api/video/cropdetect', { limit: 50 });
      const n = Number(r.labeled) || 0;
      const remaining = Number(r.remaining) || 0;
      labeled += n;
      // 总数 = 已处理 + 本批 + 剩余；服务端每批重算，只有拿到 remaining 后分母才准确。
      op.total = op.processed + n + remaining;
      return { done: n, processed: n, stop: n === 0 || remaining === 0 };
    },
    onProgress: () => renderBig(),
    onDone: async (op) => {
      if (op.cancelled) toast('已停止识别：本次识别 ' + labeled + ' 个，未处理的保持原样', 'warn');
      else toast('已识别 ' + labeled + ' 个视频' + (op.total > op.processed ? '，还有 ' + (op.total - op.processed) + ' 个未处理' : ''), 'ok');
      await loadBig();
    }
  });
}

async function bigTrashSel(){
  // v3.10.8 安全修复：列表换过一次（改排序/裁剪条件/刷新）就得重新勾，
  // 这里再按当前列表过滤一遍，保证隔离条数和「已选 N」完全对得上。
  const alive = new Set((S.bigFiles || []).map(f => f.path));
  const paths = Array.from(S.bigSel).filter(p => alive.has(p));
  if (!paths.length) { toast('先勾选要隔离的文件', 'err'); return; }
  try {
    const pv = await postJSON('/api/trash/preview', { action: 'trash', paths });
    showPreviewModal('移入隔离区预览（' + paths.length + ' 个文件）', pv.items || [], '确认移入隔离区', async () => {
      closeModal();
      // v3.19.0：改走公共分批进度组件，每批 500 个；失败明细在完成页导出。
      let moved = [];
      // 对账结果逐批合并，完成页统一展示差异（v3.10.5 起隔离接口会带回 reconcile）。
      let reconcile = null;
      await runBatchedOp({
        id: 'big-trash', label: '移入隔离区', items: paths, batchSize: BIG_TRASH_BATCH,
        call: async (chunk) => {
          const r = await postJSON('/api/trash', { paths: chunk });
          moved = moved.concat(r.moved || []);
          if (r.reconcile) reconcile = mergeReconcile(reconcile, r.reconcile);
          // 批量接口 HTTP 200 也可能部分失败，按 items 统计成功与失败。
          const items = resultItemsFrom(r, 'moved');
          return { done: (r.moved || []).length, failed: items.filter(x => !x.ok).length, items };
        },
        onProgress: () => renderBig(),
        onDone: async (op) => {
          if (moved.length) await reportPrefOnTrash(moved);
          showBatchedOpDone(op, reconcileHTML(reconcile), '大文件隔离失败明细');
          await refreshStatus();
          await loadBig();
        }
      });
    });
  } catch(e) { toast(e.message, 'err'); }
}

// ---------- 移动文件（v1.14.0 引入 10.4）----------
// 结果页勾选行 →「移动到…」→ 目录选择器（复用 fsbrowse 组件）→ 预检确认 → 执行。
// 第一版仅同卷 os.Rename；冲突自动追加 _1；审计可撤销。
async function moveSelected(){
  // v3.10.8 安全修复：只取当前结果集里的勾选，移动范围和界面上看到的行保持一致。
  const paths = currentSelectionPaths();
  if (!paths.length) { toast('先勾选要移动的文件', 'err'); return; }
  S.movePaths = paths;
  openDirPicker('move', '');
}

async function moveExecute(target, paths){
  let pv;
  try {
    pv = await postJSON('/api/move/preview', { target, paths });
  } catch(e) { toast(e.message, 'err'); return; }
  const items = [];
  (pv.done || []).forEach(d => items.push({ path: d.from, ok: true, reason: '→ ' + d.to }));
  (pv.failed || []).forEach(p => items.push({ path: p, ok: false, reason: pv.errors[p] || '不可移动' }));
  const okN = items.filter(i => i.ok).length;
  if (!okN) { showResultModal('移动预检：无可执行项', items); return; }
  const html = `${previewTable(items)}
    <div class="hint">将移动 ${okN} 个文件到 ${esc(target)}（同卷重命名，瞬时完成；冲突自动追加 _1）。目标目录必须已存在且在受管卷内。</div>
    <div class="pm-actions"><button class="btn ghost" data-act="modal-cancel">取消</button>
    <button class="btn primary" data-act="move-confirm">确认移动</button></div>`;
  setModal('移动到 ' + target, html, null);
  S.movePending = { target, paths };
}

async function moveConfirm(){
  const j = S.movePending; if (!j) return;
  // v3.7.2：执行期间禁止关闭弹窗，失败时先复位标记再关闭（P-21）
  S.modalBusy = true;
  setModal('正在移动…', '<div class="hint">执行中，请稍候。</div>', null);
  let r;
  try { r = await postJSON('/api/move', { target: j.target, paths: j.paths }); }
  catch(e) { S.modalBusy = false; closeModal(); toast(e.message, 'err'); return; }
  S.modalBusy = false;
  const items = [];
  (r.done || []).forEach(d => items.push({ path: d.from, ok: true, reason: '→ ' + d.to }));
  (r.failed || []).forEach(p => items.push({ path: p, ok: false, reason: r.errors[p] || '失败' }));
  S.moveUndoTask = r.task_id || 0;
  const undoBtn = S.moveUndoTask ? `<button class="btn ghost" data-act="move-undo">撤销本次移动</button>` : '';
  const html = `${previewTable(items)}
    <div class="hint">成功 ${okCount(items)} 个，失败 ${items.length - okCount(items)} 个。移动可用审计撤销（恢复到原目录）。</div>
    <div class="pm-actions">${undoBtn}<button class="btn primary" data-act="modal-cancel">关闭</button></div>`;
  setModal('移动结果', html, null);
  S.sel = {};
  await refreshStatus();
  await refreshView();
}

function okCount(items){ return (items || []).filter(i => i.ok).length; }

// ---------- 目录级移动（v3.14.0）----------
// 语义与 mv src dst 一致：把 src 目录整个搬到 dst（含目录名）。
// 入口在文件搜索页：选中一个目录行后点「移动目录」。
async function moveDirStart(){
  const st = searchState();
  const dir = st && st.dir;
  if (!dir) { toast('先在文件搜索页选择一个目录', 'err'); return; }
  S.moveDirSrc = dir;
  openDirPicker('movedir', '');
}

// 目标位置选好后：拼出「目标/源目录名」，先预检再确认。
async function moveDirTargetChosen(targetParent){
  const src = S.moveDirSrc; if (!src) return;
  const name = src.split('/').filter(Boolean).pop() || src;
  const dst = (targetParent.replace(/\/+$/, '')) + '/' + name;
  let pv;
  try { pv = await postJSON('/api/move/dir/preview', { src, dst }); }
  catch(e) { toast(e.message, 'err'); return; }
  if (pv && pv.error) { toast('无法移动：' + pv.error, 'err'); return; }
  const n = Number(pv && pv.files) || 0;
  const html = `<div class="hint" style="margin-bottom:10px">
      源目录：${esc(src)}<br>目标位置：${esc(dst)}</div>
    <div class="hint">索引里 ${n} 个文件将随目录一起换到新位置；只做同卷重命名，瞬时完成。
    目录移动会记入操作记录，完成弹窗里可一键撤销。</div>
    <div class="pm-actions"><button class="btn ghost" data-act="modal-cancel">取消</button>
    <button class="btn primary" data-act="move-dir-confirm">确认移动目录</button></div>`;
  S.moveDirPending = { src, dst };
  setModal('移动目录', html, null);
}

async function moveDirConfirm(){
  const j = S.moveDirPending; if (!j) return;
  S.modalBusy = true;
  setModal('正在移动目录…', '<div class="hint">执行中，请稍候。</div>', null);
  let r;
  try { r = await postJSON('/api/move/dir', { src: j.src, dst: j.dst }); }
  catch(e) { S.modalBusy = false; closeModal(); toast(e.message, 'err'); return; }
  S.modalBusy = false;
  if (r && r.error) {
    const html = `<div class="hint">${esc(r.error)}</div>
      <div class="pm-actions">${r.task_id ? `<button class="btn ghost" data-act="move-dir-undo-now">撤销本次移动</button>` : ''}
      <button class="btn primary" data-act="modal-cancel">关闭</button></div>`;
    setModal('移动未完成', html, null);
    S.moveDirTask = r.task_id || 0;
    return;
  }
  S.moveDirTask = r.task_id || 0;
  const html = `<div class="hint" style="margin-bottom:10px">${esc(j.src)}<br>→ ${esc(j.dst)}</div>
    <div class="hint">索引同步 ${Number(r.files) || 0} 行，健康结论 ${Number(r.health) || 0} 条。${Number(r.missing) ? Number(r.missing) + ' 个文件原本不在索引（如零字节），随目录移动无需改写。' : ''}</div>
    <div class="pm-actions">${S.moveDirTask ? `<button class="btn ghost" data-act="move-dir-undo-now">撤销本次移动</button>` : ''}
    <button class="btn primary" data-act="modal-cancel">关闭</button></div>`;
  setModal('目录已移动', html, null);
  S.moveDirPending = null;
  S.moveDirSrc = null;
  await refreshStatus();
  await refreshView();
}

async function moveDirUndoNow(){
  const tid = S.moveDirTask; if (!tid) return;
  try {
    const r = await postJSON('/api/move/dir/undo', { task_id: tid });
    if (Number(r.failed) > 0) toast(`目录已移回，但 ${r.failed} 个任务项失败，详见操作记录`, 'warn');
    else toast('已撤销：目录回到原位置', 'ok');
    S.moveDirTask = 0;
    closeModal();
    await refreshStatus();
    await refreshView();
  } catch(e) { toast(e.message, 'err'); }
}

async function moveUndo(){
  const tid = S.moveUndoTask; if (!tid) return;
  try {
    const r = await postJSON('/api/move/undo', { task_id: tid });
    toast(`撤销完成：还原 ${r.done} 个，失败 ${r.failed} 个`, r.failed ? 'err' : 'ok');
    S.moveUndoTask = 0;
    closeModal();
    await refreshStatus();
    await refreshView();
  } catch(e) { toast(e.message, 'err'); }
}

// ---------- 结果页重命名（v1.15 引入 15.4）----------
// 组内统一命名：标杆主名 + 各自扩展名；冲突追加 _1；audit 可撤销。
// 单文件改名：服务端校验非法字符。
async function groupRenameBase(key, basePath){
  const g = (S.groups || []).find(x => x.key === key);
  if (!g) { toast('找不到分组数据', 'err'); return; }
  const files = (g.files || []).filter(f => f.path !== basePath);
  if (!files.length) { toast('组内没有其他文件', 'err'); return; }
  const baseName = basePath.split('/').pop();
  const stem = baseName.replace(/\.[^.]+$/, '');
  const items = files.map(f => {
    const name = f.path.split('/').pop();
    const ext = (name.match(/\.[^.]+$/) || [''])[0];
    return { path: f.path, ok: true, reason: '→ ' + stem + ext };
  });
  const html = `<div class="relink-warn">以「${esc(baseName)}」的主名统一命名（扩展名各自保留）</div>
    ${previewTable(items)}
    <div class="hint">冲突自动追加 _1 绝不覆盖；受保护/隔离区/同目录目标会被跳过并写明原因。操作可整体撤销。</div>
    <div class="pm-actions"><button class="btn ghost" data-act="modal-cancel">取消</button>
    <button class="btn primary" data-act="group-rename-confirm">确认统一命名</button></div>`;
  setModal('组内统一命名', html, null);
  S.groupRenamePending = { base: basePath, paths: files.map(f => f.path) };
}

async function groupRenameConfirm(){
  const j = S.groupRenamePending; if (!j) return;
  // v3.7.2：执行期间禁止关闭弹窗，失败时先复位标记再关闭（P-21）
  S.modalBusy = true;
  setModal('正在统一命名…', '<div class="hint">执行中，请稍候。</div>', null);
  let r;
  try { r = await postJSON('/api/groups/rename', { base: j.base, paths: j.paths }); }
  catch(e) { S.modalBusy = false; closeModal(); toast(e.message, 'err'); return; }
  S.modalBusy = false;
  const items = [];
  (r.done || []).forEach(d => items.push({ path: d.from, ok: true, reason: '→ ' + d.to }));
  (r.failed || []).forEach(p => items.push({ path: p, ok: false, reason: r.errors[p] || '失败' }));
  S.groupRenameUndoTask = r.task_id || 0;
  const undoBtn = S.groupRenameUndoTask ? `<button class="btn ghost" data-act="group-rename-undo">撤销本次改名</button>` : '';
  const html = `${previewTable(items)}
    <div class="hint">成功 ${okCount(items)} 个，失败 ${items.length - okCount(items)} 个。</div>
    ${reconcileHTML(r.reconcile)}
    <div class="pm-actions">${undoBtn}<button class="btn primary" data-act="modal-cancel">关闭</button></div>`;
  setModal('统一命名结果', html, null);
  await refreshStatus();
  await refreshView();
}

async function groupRenameUndo(){
  const tid = S.groupRenameUndoTask; if (!tid) return;
  try {
    const r = await postJSON('/api/groups/rename/undo', { task_id: tid });
    toast(`撤销完成：还原 ${r.done} 个，失败 ${r.failed} 个` + undoFailReason(r), r.failed ? 'err' : 'ok');
    S.groupRenameUndoTask = 0;
    closeModal();
    await refreshStatus();
    await refreshView();
  } catch(e) { toast(e.message, 'err'); }
}

async function fileRename(path){
  const name = path.split('/').pop();
  const input = prompt('新文件名（含扩展名）：', name);
  if (input === null || input.trim() === '' || input.trim() === name) return;
  const newName = input.trim();
  // 行内改名直接改真实路径，确认层级应与批量改名一致（P-04）
  if (!confirm('确认将「' + name + '」改名为「' + newName + '」？\n\n引用旧路径的播放列表或媒体库可能需要更新。')) return;
  try {
    const r = await postJSON('/api/file/rename', { path, name: newName });
    toast('已改名：' + (r.path || '').split('/').pop(), 'ok');
    await refreshStatus();
    await refreshView();
  } catch(e) { toast(e.message, 'err'); }
}

async function trimProbe(){
  const j = S.trimPending; if (!j) return;
  const hint = $('trimProbeHint');
  if (hint) hint.textContent = '探测中（每成员约 10 次窗口解码）…';
  try {
    const r = await postJSON('/api/trim/probe', {
      group_key: j.key,
      paths: j.files.map(f => f.path),
      durations: j.files.map(f => Number(f.duration) || 0),
      threshold: 0
    });
    S.trimBoundary = r.status === 'ok' ? Number(r.boundary) : null;
    if (hint) {
      hint.textContent = r.status === 'ok'
        ? `推测边界 ≈ ${Number(r.boundary).toFixed(1)} 秒${r.cached ? '（缓存）' : ''}，已填入`
        : '边界探测失败（' + r.status + '），请手动确认裁剪点';
    }
    if (r.status === 'ok') { const inp = $('trimOffset'); if (inp) inp.value = Math.max(1, Math.round(Number(r.boundary))); }
  } catch(e) { if (hint) hint.textContent = '探测失败：' + e.message; }
}

async function trimConfirm(){
  const j = S.trimPending; if (!j) return;
  const offset = Number($('trimOffset') ? $('trimOffset').value : 0);
  if (!(offset > 0)) { toast('裁剪点必须大于 0', 'err'); return; }
  const mode = (document.querySelector('input[name="trimMode"]:checked') || {}).value || 'fast';
  const replace = !!( $('trimReplace') && $('trimReplace').checked );
  const skipDone = !( $('trimSkipDone') && !$('trimSkipDone').checked );
  const jobs = j.files.filter(f => !(skipDone && f.trimmed)).map(f => ({
    path: f.path, group_key: j.key, offset, mode, replace, duration: Number(f.duration) || 0
  }));
  if (!jobs.length) { toast('没有可处理的文件', 'err'); return; }
  if (replace) {
    const html = `<div class="relink-warn">⚠ 将替换 ${jobs.length} 个原文件！</div>
      <div class="hint">替换前会多层验证输出（结构 + 首尾解码抽检）；原文件进隔离区可恢复；保护目录内文件自动改为只生成新文件。确认继续？</div>
      <div class="pm-actions"><button class="btn ghost" data-act="modal-cancel">取消</button>
      <button class="btn danger" data-act="trim-confirm2">确认替换</button></div>`;
    setModal('确认替换原文件', html, null);
    S.trimJobs = jobs;
    return;
  }
  // 非替换模式也会真实写出新文件，执行前同样明确确认，避免按钮一点就动盘（P-19）
  const html = `<div class="hint">将为 ${jobs.length} 个文件生成剪除片头后的新文件，不覆盖原文件；需要额外磁盘空间。确认继续？</div>
    <div class="pm-actions"><button class="btn ghost" data-act="modal-cancel">取消</button>
    <button class="btn primary" data-act="trim-confirm2">确认开始剪除</button></div>`;
  setModal('确认生成剪除副本', html, null);
  S.trimJobs = jobs;
}

async function trimConfirm2(){
  const jobs = S.trimJobs || [];
  S.trimJobs = null;
  await trimRun(jobs);
}

async function trimRun(jobs){
  // v3.7.2：执行期间禁止关闭弹窗，失败时先复位标记再关闭（P-21）
  S.modalBusy = true;
  setModal('正在剪除…', '<div class="hint">执行中，进度通过状态推送（trim_progress）。快速模式通常每个文件数秒；碰到关键帧间隔过长的文件会自动改用精确模式，耗时更长。</div>', null);
  let r;
  try { r = await postJSON('/api/trim', { jobs }); }
  catch(e) { S.modalBusy = false; closeModal(); toast(e.message, 'err'); return; }
  S.modalBusy = false;
  const items = [];
  (r.done || []).forEach(d => items.push({ path: d.path, ok: true, reason: (d.replaced ? '已替换原位 ← ' : '→ ') + d.output }));
  (r.failed || []).forEach(p => { items.push({ path: p, ok: false, reason: r.errors[p] || '失败' }); });
  const html = `${previewTable(items)}
    <div class="hint">成功 ${okCount(items)} 个，失败 ${items.length - okCount(items)} 个。剪除记录见 /api/trim/log；裁剪后的文件会自动重算指纹并重新参与查重。</div>
    <div class="pm-actions"><button class="btn primary" data-act="modal-cancel">关闭</button></div>`;
  setModal('剪除结果', html, null);
  await refreshStatus();
  await refreshView();
}

// v3.7.0 操作记录页：筛选参数、列表加载、CSV 导出与旧记录清理。
function opLogParams(){
  const st = S.opLog;
  const p = new URLSearchParams();
  if ($('opAction')) st.action = $('opAction').value;
  if ($('opResult')) st.result = $('opResult').value;
  if ($('opKeyword')) st.keyword = $('opKeyword').value.trim();
  if (st.action) p.set('action', st.action);
  if (st.result) p.set('result', st.result);
  if (st.keyword) p.set('keyword', st.keyword);
  return p;
}

async function loadOpLog(){
  // v3.10.13：操作记录页有两个视图，切页/刷新时按当前视图取数据
  if (S.opView === 'snap') return loadOpSnapshots();
  S.loading = true; renderOpLog();
  // 2026-11-08：未完成批次与审计列表并行拉取，互不阻塞；失败只记控制台，不弹 toast
  loadUnfinishedBatches();
  const st = S.opLog;
  const p = opLogParams();
  p.set('page', String(st.page || 1));
  p.set('page_size', String(st.pageSize || 50));
  try {
    const r = await api('/api/oplog?' + p.toString());
    st.items = r.items || []; st.total = r.total || 0;
    st.page = r.page || 1; st.pageSize = r.page_size || 50;
  } catch(e) { toast(e.message, 'err'); st.items = []; st.total = 0; }
  finally { S.loading = false; renderOpLog(); }
}

function opSnapState(){
  if (!S.opSnap) S.opSnap = { page:1, pageSize:20, items:[], total:0, kind:'', result:'all', keyword:'', error:'' };
  return S.opSnap;
}

// v3.10.13：写操作快照列表。接口未就位时把错误存进 state，由页面渲染成「暂不可用」，
// 不用 toast 打断——用户只是切到这一栏看看，并没有发起写操作。
// 约定 GET /api/op-snapshots?page=&page_size=&kind=&result=&keyword=
// page 默认 1；page_size 默认 20、上限 100；result 只取 all / ok / mismatch；
// 每个快照的 items 最多 200 条差异项；created_at 是 Unix 秒。
async function loadOpSnapshots(){
  const st = opSnapState();
  // 上限 100 由后端约束，前端先夹一次，避免翻页算出来后端又默默截断
  st.pageSize = Math.min(100, Math.max(1, Number(st.pageSize) || 20));
  S.loading = true; renderOpLog();
  try {
    const p = new URLSearchParams({ page:String(st.page || 1), page_size:String(st.pageSize) });
    if (st.kind) p.set('kind', st.kind);
    if (st.result) p.set('result', st.result);
    if (st.keyword) p.set('keyword', st.keyword);
    const r = await api('/api/op-snapshots?' + p.toString());
    st.items = r.items || r.snapshots || [];
    st.total = Number(r.total || 0);
    st.page = Number(r.page || st.page || 1);
    st.error = '';
  } catch(e) {
    st.items = []; st.total = 0;
    st.error = (e && e.message) || String(e);
  } finally { S.loading = false; renderOpLog(); }
}

async function exportOpLog(){
  const p = opLogParams();
  try {
    // v3.10.7：改走 api()，让写请求带上 CSRF 校验所需的 X-Requested-With
    const r = await api('/api/oplog/export?' + p.toString(), { method:'POST' });
    if (!r.ok) throw new Error('导出失败（' + r.status + '）');
    const blob = await r.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url; a.download = 'verify-oplog.csv';
    document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 3000);
    toast('已导出操作记录', 'ok');
  } catch(e) { toast(e.message, 'err'); }
}

async function clearOpLog(){
  const cfg = (S.status && S.status.config) || {};
  const days = Number(cfg.op_log_retention_days || 180);
  if (!confirm('清理 ' + days + ' 天前的操作记录？清理后不可恢复。')) return;
  try {
    const r = await postJSON('/api/oplog/clear', { before_ts: Date.now() - days * 86400000, confirm: true });
    toast('已清理 ' + (r.removed || 0) + ' 条', 'ok');
    S.opLog.page = 1;
    await loadOpLog();
  } catch(e) { toast(e.message, 'err'); }
}

// 2026-11-08：未完成改名批次（崩溃/被强杀残留的 running 行）。
// 约定：只读探测，失败不挡页面、不弹 toast，只在控制台留痕；空数组时角标与提示条都消失。
// v3.10.13：后端定稿把未完成批次拆成两段——batches 只含 running，
// residual 含 undone_partial / undone_failed / repaired_partial。
// 字段同时认新写法 started_at / total 与老包的 created_at / files_count，
// 老包刷新时不会因为少字段显示成 1970-01-01 或 0 个文件。
function normUnfinishedBatch(b){
  const o = Object.assign({}, b || {});
  o.batch_id = Number(o.batch_id) || 0;
  // v3.13.0（UI 审查 P3-2）：缺 status 时保留空串，由渲染层给中文「未完成」；
  // 原先默认 'running' 会让 residual 段把英文状态词直接显示给用户。
  o.status = (o.status === undefined || o.status === null) ? '' : String(o.status);
  o.done = Number(o.done) || 0;
  o.failed = Number(o.failed) || 0;
  o.total = Number(o.total !== undefined ? o.total : (o.files_count || 0)) || 0;
  o.started_at = Number(o.started_at !== undefined ? o.started_at : (o.created_at || 0)) || 0;
  o.preset_name = String(o.preset_name || '');
  return o;
}

// v3.10.13：residual 由后端持续返回，「知道了」只删本地数组的话刷新就回来。
// 这里把已确认过的 batch_id 记在本机（localStorage），加载时过滤掉；
// 存储不可用（隐私模式/禁用）时静默退回「仅本次会话有效」。
const UNFIN_DISMISS_KEY = 'verify.unfinDismiss';
function unfinDismissed(){
  try {
    const raw = JSON.parse(localStorage.getItem(UNFIN_DISMISS_KEY) || '[]');
    return new Set((Array.isArray(raw) ? raw : []).map(Number).filter(Boolean));
  } catch(e) { return new Set(); }
}
function unfinDismiss(id){
  const n = Number(id) || 0;
  if (!n) return;
  const set = unfinDismissed();
  if (set.has(n)) return;
  set.add(n);
  try { localStorage.setItem(UNFIN_DISMISS_KEY, JSON.stringify([...set])); } catch(e) {}
}

async function loadUnfinishedBatches(){
  try {
    const r = /** @type {RenameBatchesResidualResp} */ (await api('/api/rename/batches/unfinished'));
    S.unfinished = (Array.isArray(r.batches) ? r.batches : []).map(normUnfinishedBatch);
    // v3.10.13：residual 来自后端，刷新后也能看到撤销没干净的批次；
    // 本地残项作为兜底（老包没有 residual 时仍能显示本次会话内刚失败的撤销），按 batch_id 去重。
    // v3.12.1（UI 审查 P2-2）：后端成功响应后以 remote 为准；本地条目只在
    // 「本次响应没有 + 产生于 30 分钟内」时保留，避免文件早已被其它入口恢复时
    // 提示条与角标挂着假告警不放。
    const dismissed = unfinDismissed();
    const remote = (Array.isArray(r.residual) ? r.residual : []).map(normUnfinishedBatch);
    const freshLocal = (S.unfinResidual || []).filter(b =>
      b._localAt && (Date.now() - b._localAt) < 30 * 60 * 1000);
    const seen = new Set();
    S.unfinResidual = remote.concat(freshLocal).map(normUnfinishedBatch).filter(b => {
      if (!b.batch_id || seen.has(b.batch_id)) return false;
      if (dismissed.has(b.batch_id)) return false;
      seen.add(b.batch_id);
      return true;
    });
  } catch(e) {
    console.warn('读取未完成改名批次失败：', (e && e.message) || e);
    // v3.10.12：本地残项不依赖接口成功，探测失败时也要把角标/提示条刷成最新值
    refreshUnfinishedMarks();
    if (S.tab === 'oplog') syncUnfinishedBar();
    return;
  }
  refreshUnfinishedMarks();
  if (S.tab === 'oplog') syncUnfinishedBar();
}

// v3.10.12：角标与提示条同时统计「后端仍 running 的批次」和「本地残项」。
// 残项来自部分撤销/撤销失败——那类批次已被后端摘出 running 列表，只看接口会漏掉。
function refreshUnfinishedMarks(){
  paintUnfinishedBadge((S.unfinished || []).length + (S.unfinResidual || []).length);
}

// 2026-11-08：撤销单个遗留批次。复用现有撤销接口与「首条失败原因」文案，
// 成功后重新探测一次，让提示条与导航角标同步消失。
// v3.10.12：按后端三态分别提示；部分撤销/全部失败时在本地留下残项，否则刷新后
// 后端只返回 running 批次，这条「没撤干净」的记录会直接消失。
async function undoUnfinishedBatch(id){
  if (!id) return;
  if (!confirm(`撤销批次 #${id} 的改名？会按逆序把文件改回原名。`)) return;
  let res;
  try {
    res = await postJSON('/api/rename/undo/' + id, {});
  } catch(e) { toast('撤销失败：' + ((e && e.message) || e), 'err'); return; }
  const out = undoOutcome(res);
  toast(out.text, out.type);
  if (out.status !== 'undone') {
    const prev = (S.unfinished || []).find(b => Number(b.batch_id) === Number(id)) || {};
    // 同一批次不能既留在 running 列表又出现在残项里；先摘 running 再塞残项
    S.unfinished = (S.unfinished || []).filter(b => Number(b.batch_id) !== Number(id));
    S.unfinResidual = (S.unfinResidual || []).filter(b => Number(b.batch_id) !== Number(id));
    S.unfinResidual.unshift({
      batch_id: Number(id),
      preset_name: prev.preset_name || '',
      done: Number(res.done) || 0,
      failed: Number(res.failed) || 0,
      // v3.10.13：统一成定稿字段名，老包的 files_count/created_at 兜底读取
      total: Number(prev.total !== undefined ? prev.total : (prev.files_count || 0)) || 0,
      started_at: Number(prev.started_at !== undefined ? prev.started_at : (prev.created_at || 0)) || 0,
      status: out.status,
      error: (((res.items || []).find(x => x && x.error)) || {}).error || '',
      // v3.12.1（UI 审查 P2-2）：本地残项的落表时间，30 分钟后由 loadUnfinishedBatches 过期
      _localAt: Date.now()
    });
  }
  await loadUnfinishedBatches();
}

function pausedRef(){ return !!(S.status && S.status.paused); }

// v3.25.2：隔离区分页。首屏只拉 200 条，游标交给「加载更多」。
// 后端若返回老结构（没有 next_cursor），按「没有更多」处理，可平滑回退到旧路径。
async function loadTrash(){
  S.loading = true; renderTrash();
  try {
    const r = /** @type {TrashListResp} */ (await api('/api/trash?limit=200'));
    S.trash = r.items || [];
    S.trashTotal = (typeof r.total === 'number') ? r.total : S.trash.length;
    S.trashCursor = r.next_cursor || '';
    S.trashSel = new Set();
  } catch(e) { toast(e.message, 'err'); S.trash = []; S.trashTotal = 0; S.trashCursor = ''; }
  finally { S.loading = false; renderTrash(); }
}

// loadMoreTrash 追加下一页：保留已勾选与已展开，按 id 去重防止翻页时出现重复行。
async function loadMoreTrash(){
  if (!S.trashCursor || S.trashMoreLoading) return;
  S.trashMoreLoading = true;
  try {
    const r = await api('/api/trash?limit=200&cursor=' + encodeURIComponent(S.trashCursor));
    const seen = new Set((S.trash || []).map(t => t.id));
    const add = (r.items || []).filter(t => !seen.has(t.id));
    S.trash = (S.trash || []).concat(add);
    S.trashTotal = (typeof r.total === 'number') ? r.total : S.trash.length;
    S.trashCursor = r.next_cursor || '';
  } catch(e) { toast(e.message, 'err'); }
  finally { S.trashMoreLoading = false; renderTrash(); }
}

function resultItemsFrom(res, successKey){
  const ok = (res[successKey] || []).map(p => ({path:p, ok:true}));
  const errs = res.errors;
  const fail = (res.failed || []).map((p,i) => {
    // 各批量接口的错误格式并不统一：隔离区用数组，移动/改名用路径到原因的映射（P-22）
    const reason = Array.isArray(errs) ? (errs[i] || '') : (errs && typeof errs === 'object' ? (errs[p] || '') : '');
    return {path:p, ok:false, reason};
  });
  return ok.concat(fail);
}

async function doTrash(paths, groupKey){
  if (!paths.length) { toast('请先选择要移入隔离区的文件'); return; }
  // v3.9.1：groupKey 支持数组，结果页「把当前勾选移入隔离区」会跨多个分组。
  // v3.10.8（P1-2）：group_keys 逐组校验「至少保留一个」，group_key 仍带第一个键供审计与隔离区记录。
  const groupKeys = Array.isArray(groupKey) ? groupKey : (groupKey ? [groupKey] : []);
  const logKey = groupKeys[0] || '';
  try {
    const pv = await postJSON('/api/trash/preview', { action:'trash', paths, group_key: logKey, group_keys: groupKeys });
    // v3.10.8：标题直接写出真实条数，用户确认前就知道会动多少个文件。
    showPreviewModal('移入隔离区预览（' + paths.length + ' 个文件）', pv.items || [], '确认移入隔离区', async () => {
      let r;
      try { r = await postJSON('/api/trash', { paths, group_key: logKey, group_keys: groupKeys }); }
      catch(e) { closeModal(); toast(e.message, 'err'); return; }
      closeModal();
      const items = resultItemsFrom(r, 'moved');
      const fails = items.filter(x => !x.ok);
      if (fails.length) {
        showResultModal('移入隔离区结果：部分失败', items);
        toast('移入完成：成功 ' + (r.moved || []).length + ' 个，失败 ' + fails.length + ' 个', 'err');
      } else toast('已移入隔离区 ' + (r.moved || []).length + ' 个文件', 'ok');
      // v3.10.5：对账有差异时，直接把不一致的路径和下一步建议摆在完成页上。
      showReconcile(r.reconcile);
      // v3.10.8（P2-2）：部分失败时不能整组删勾选，否则没移成功的文件也丢了标记，
      // 用户要重新逐行找回。按组保留未成功的路径，成功移走的才清掉。
      const movedSet = new Set(r.moved || []);
      groupKeys.forEach(k => {
        const sel = S.sel[k];
        if (!sel) return;
        const remain = new Set([...sel].filter(p => !movedSet.has(p)));
        if (remain.size) S.sel[k] = remain; else delete S.sel[k];
      });
      resetAssistCache();
      await refreshStatus();
      await reloadTab();
    });
  } catch(e) { toast(e.message, 'err'); }
}

async function doExport(format, scope){
  try {
    const fmtEl = $('expFormat'), scopeEl = $('expScope');
    const body = Object.assign(reportBody(), {
      format: format || (fmtEl ? fmtEl.value : 'dc5'),
      scope: scope || (scopeEl ? scopeEl.value : 'all')
    });
    if (body.scope === 'marked' && !(body.marked || []).length) {
      toast('仅标记行导出需要先勾选文件', 'err'); return;
    }
    // v3.10.7：改走 api()，让写请求带上 CSRF 校验所需的 X-Requested-With
    const r = await api('/api/export', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body) });
    if (!r.ok) { let m = '导出失败'; try { const j = await r.json(); m = j.error || m; } catch(e){} throw new Error(m); }
    const blob = await r.blob();
    // 优先用服务端给出的文件名（含格式标识），取不到再本地拼
    let name = '';
    const cd = r.headers.get('Content-Disposition') || '';
    const mt = cd.match(/filename="([^"]+)"/);
    if (mt) name = mt[1];
    if (!name) name = 'verify-' + body.format + '-' + S.tab + '-' + new Date().toISOString().slice(0,19).replace(/[:T]/g,'-') + '.csv';
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 5000);
    toast('CSV 已导出', 'ok');
  } catch(e) { toast(e.message, 'err'); }
}

async function doReport(){
  try {
    // v3.10.7：改走 api()，让写请求带上 CSRF 校验所需的 X-Requested-With
    const r = await api('/api/report', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(reportBody()) });
    if (!r.ok) { let m = '生成报告失败'; try { const j = await r.json(); m = j.error || m; } catch(e){} throw new Error(m); }
    const blob = await r.blob();
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'verify-report-' + S.tab + '-' + new Date().toISOString().slice(0,19).replace(/[:T]/g,'-') + '.html';
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 5000);
    toast('HTML 报告已生成（含预览图）', 'ok');
  } catch(e) { toast(e.message, 'err'); }
}

async function applyDirSetting(mode, path){
  try {
    const body = mode === 'log' ? { log_dir: path } : { cache_dir: path };
    await postJSON('/api/settings', body);
    const input = $(mode === 'log' ? 'sLogDir' : 'sCacheDir');
    if (input) input.value = path;
    const hint = $('saveHint'); if (hint) hint.textContent = '已生效';
    toast((mode === 'log' ? '日志目录' : '缓存目录') + '已更新并生效', 'ok');
    await refreshStatus();
  } catch(e) { toast(e.message, 'err'); }
}

async function mkdirInPicker(){
  const d = S.dir; if (!d || !d.path) return;
  const name = prompt('新建文件夹名称');
  if (!name || !name.trim()) return;
  try {
    const r = await postJSON('/api/fs/mkdir', { parent: d.path, name: name.trim() });
    toast('已创建文件夹', 'ok');
    await loadDir(r.path);
  } catch(e) { toast(e.message, 'err'); }
}

// readSettingsRules v3.8.0：从设置页规则编辑器回读规则对象。
function readSettingsRules(){
  const attrs = [...document.querySelectorAll('[data-act="srule-attr"]')]
    .filter(x => x.checked).map(x => x.dataset.attr);
  const base = $('sRuleBase') ? $('sRuleBase').value : 'content';
  return {
    base,
    attrs,
    size_tolerance_kb: Math.max(0, Math.min(1048576, Number($('sRuleSizeTol') ? $('sRuleSizeTol').value : 0))),
    name_similar_pct: Math.max(50, Math.min(100, Number($('sRuleSimilar') ? $('sRuleSimilar').value : 90))),
    folder_name_mode: $('sRuleFolderMode') ? $('sRuleFolderMode').value : 'full',
    folder_name_depth: Math.max(1, Math.min(50, Number($('sRuleFolderDepth') ? $('sRuleFolderDepth').value : 1))),
    ignore_copy_names: !!($('sRuleIgnoreCopy') && $('sRuleIgnoreCopy').checked),
    copy_patterns: ['副本', '(1)', '(2)', 'copy'],
    ignore_same_folder: !!($('sRuleIgnoreSame') && $('sRuleIgnoreSame').checked),
    case_sensitive: !!($('sRuleCase') && $('sRuleCase').checked),
    video_mode: 'frames',
    audio_window: '15s',
    duration_tolerance_sec: 0
  };
}

async function saveSettings(){
  const vols = (S.scanPaths || []).slice();
  const excl = $('sExcl').value.split('\n').map(s => s.trim()).filter(Boolean);
  // v3.8.0：搜索规则取代旧匹配模式；忽略内容模式必须至少一个属性条件。
  const rules = readSettingsRules();
  if (rules.base === 'attributes' && !rules.attrs.length) { toast('忽略内容模式至少要勾选一个比较条件', 'err'); return; }
  // v3.7.1：令牌、时段、AI 地址在保存前就地校验并二次确认，避免留空/写错被静默吞掉（P-10/P-18）
  const token = $('sToken').value.trim();
  const tokenConfirm = $('sTokenConfirm') ? $('sTokenConfirm').value.trim() : '';
  const tokenClear = $('sTokenClear') && $('sTokenClear').checked;
  if (tokenClear && token) { toast('设置新令牌与清除令牌不能同时选择', 'err'); return; }
  if ((token || tokenConfirm) && token !== tokenConfirm) { toast('两次输入的新令牌不一致', 'err'); return; }
  // v3.17.5（F-18）：令牌是唯一鉴权手段，非空时至少 16 位，避免 123456 这类弱值。
  if (token && token.length < 16) { toast('新访问令牌至少 16 位', 'err'); return; }
  if (token && !confirm('确认更换访问令牌？保存后所有旧登录状态都会失效。')) return;
  if (tokenClear && !confirm('确认清除访问令牌？局域网内能访问本应用的人都将无需登录。')) return;
  const quietHours = $('sQuietHours') ? $('sQuietHours').value.trim() : '';
  if (quietHours && !validQuietHours(quietHours)) { toast('静默时段格式应为 HH:MM-HH:MM，例如 23:00-07:00', 'err'); return; }
  const wakeEnabled = $('sWakeEnabled') && $('sWakeEnabled').checked;
  const wakeWindow = $('sWakeWindow') ? $('sWakeWindow').value.trim() : '';
  if (wakeEnabled && !validQuietHours(wakeWindow)) { toast('唤醒时段格式应为 HH:MM-HH:MM，例如 04:00-08:00', 'err'); return; }
  // v3.9.6 修 S-08：叫醒硬盘从关→开违背睡眠友好，二次确认
  const wasWakeOn = !!(S.status && S.status.config && S.status.config.wake_enabled);
  if (!wasWakeOn && wakeEnabled && !confirm('开启「定时唤醒硬盘补扫」会违背睡眠友好设置，每天在设定时段叫醒硬盘对账。确定继续？')) return;
  const unifyExt = ($('sUnifyExt') ? $('sUnifyExt').value : 'off');
  // v3.9.6 修 S-18：统一扩展名会断链 + 重建缩略图/播放进度，二次确认
  const wasUnifyExt = (S.status && S.status.config && S.status.config.unify_ext) || 'off';
  if (unifyExt !== 'off' && unifyExt !== wasUnifyExt && !confirm(`扩展名统一成 ${unifyExt} 会让依赖原扩展名的路径/播放列表断链、媒体库重建缩略图、播放进度归零。确定继续？`)) return;
  // v3.9.6 修 S-02：ffmpeg/ffprobe 路径校验（非空时不能含空白，飞牛路径无空格）
  const ffmpegPath = $('sFF').value.trim();
  if (ffmpegPath && /\s/.test(ffmpegPath)) { toast('FFmpeg 路径不能含空格（飞牛路径无空格）', 'err'); return; }
  const ffprobePath = $('sFP').value.trim();
  if (ffprobePath && /\s/.test(ffprobePath)) { toast('FFprobe 路径不能含空格', 'err'); return; }
  const aiEnabled = $('sAIEnabled') && $('sAIEnabled').checked;
  const aiEndpoint = $('sAIEndpoint') ? $('sAIEndpoint').value.trim() : '';
  if (aiEnabled || aiEndpoint) {
    let urlOK = false;
    try {
      const u = new URL(aiEndpoint);
      urlOK = (u.protocol === 'http:' || u.protocol === 'https:') && !!u.host;
    } catch(e) { /* URL 解析失败即非法 */ }
    if (!urlOK) { toast('AI 服务地址必须是完整的 http:// 或 https:// 地址', 'err'); return; }
  }
  // v3.9.6 修 S-20：路径类 textarea 无格式校验（S-06/S-16 已加，此处漏）
  // v3.10.0：只要求以 / 开头，允许空格——「/vol1/1000/My Photos」是合法目录，
  // 选择文件夹按钮也可能直接带空格进来，之前按空白切分会把正常路径判成非法。
  const nasAbsOK = p => p.startsWith('/') && !/[\r\n\t]/.test(p);
  const exclPaths = ($('sExclPaths') ? $('sExclPaths').value.split('\n').map(s => s.trim()).filter(Boolean) : []);
  for (const p of exclPaths) { if (!nasAbsOK(p)) { toast(`排除路径「${p}」不是合法绝对路径（应以 / 开头）`, 'err'); return; } }
  const protectPaths = ($('sProtect') ? $('sProtect').value.split('\n').map(s => s.trim()).filter(Boolean) : []);
  for (const p of protectPaths) { if (!nasAbsOK(p)) { toast(`保护路径「${p}」不是合法绝对路径（应以 / 开头）`, 'err'); return; } }
  // path_maps 每行应该是 左路径=右路径 格式
  // v3.10.0 修 bug：外部侧是 Windows 形态，不能套 NAS 的「/ 开头」规则。
  // 允许盘符（V: / V:\电影）、UNC（\\NAS\share）和 / 开头三种写法，且允许空格。
  const extPathOK = r => /^(?:[A-Za-z]:|\\\\|\/)/.test(r) && !/[\r\n\t]/.test(r);
  const pathMapsText = ($('sPathMaps') ? $('sPathMaps').value.trim() : '');
  if (pathMapsText) {
    for (let i = 0; i < pathMapsText.split('\n').length; i++) {
      const line = pathMapsText.split('\n')[i].trim();
      if (!line) continue;
      const eq = line.indexOf('=');
      if (eq <= 0 || eq === line.length - 1) { toast(`路径映射第 ${i+1} 行格式应为 旧路径=新路径`, 'err'); return; }
      const [l, r] = [line.slice(0, eq).trim(), line.slice(eq+1).trim()];
      if (!nasAbsOK(l)) { toast(`路径映射第 ${i+1} 行「${line}」：左侧必须是以 / 开头的 NAS 路径`, 'err'); return; }
      if (!extPathOK(r)) { toast(`路径映射第 ${i+1} 行「${line}」：右侧应为盘符（如 V:）、共享路径（\\\\NAS\\目录）或以 / 开头`, 'err'); return; }
    }
  }
  const body = {
    min_size: Math.round(Number($('sMin').value || 0) * 1024),
    hash_workers: Number($('sWorkers').value || 4),
    video_workers: Number($('sVWorkers').value || 2),
    intro_enabled: $('sIntro').checked,
    intro_seconds: Math.min(300, Math.max(1, Number($('sIntroSec').value || 10))),
    content_enabled: $('sContent').checked,
    name_enabled: $('sName').checked,
    compute_dup_folders: ($('sDupFolders') ? $('sDupFolders').checked : true),
    search_rules: rules,
    hardware_decode: $('sHW').checked,
    full_rescan_hours: Number($('sRescan').value || 0),
    poll_minutes: Number($('sPoll').value || 0),
    audio_threshold: Number($('sAudio').value || 0.82),
    audio_window_seconds: ($('sAudioWindow') ? Number($('sAudioWindow').value) : 15),
    phash_hamming: Number($('sPhash').value || 10),
    // v3.9.6：复用前面预校验的变量
    ffmpeg: ffmpegPath,
    ffprobe: ffprobePath,
    exclude_dirs: excl,
    exclude_paths: exclPaths,
    protect_paths: protectPaths,
    path_maps_text: pathMapsText,
    volumes: vols,
    video_enabled: $('sVideo').checked,
    ignore_intro_seconds: Math.max(0, Math.min(3600, Number($('sIgnoreIntro').value || 0))),
    ignore_outro_seconds: Math.max(0, Math.min(3600, Number($('sIgnoreOutro').value || 0))),
    photo_enabled: ($('sPhoto') ? $('sPhoto').checked : true),
    img_hamming: ($('sImgHamming') ? Math.max(0, Math.min(64, Number($('sImgHamming').value || 10))) : 10),
    audio_enabled: ($('sAudioEnabled') ? $('sAudioEnabled').checked : true),
    video_density: ($('sVideoDensity') ? $('sVideoDensity').value : 'medium'),
    traditional_enabled: ($('sTraditional') ? $('sTraditional').checked : true),
    temp_patterns: ($('sTempPatterns') ? $('sTempPatterns').value.split('\n').map(x=>x.trim()).filter(Boolean) : undefined),
    noise_words: ($('sNoiseWords') ? $('sNoiseWords').value.split('\n').map(x=>x.trim()).filter(Boolean) : undefined),
    unify_ext: unifyExt, // v3.9.6：复用前面已读的值（二次确认也用这个）
    log_dir: ($('sLogDir') ? $('sLogDir').value.trim() : ''),
    cache_dir: ($('sCacheDir') ? $('sCacheDir').value.trim() : ''),
    mem_budget_mb: ($('sMemBudget') ? Math.max(0, Math.min(65536, Number($('sMemBudget').value || 0))) : 0),
    thumb_cache_max_mb: ($('sThumbMax') ? Math.max(0, Math.min(4096, Number($('sThumbMax').value || 0))) : 256),
    watch: $('sWatch').checked,
    sleep_friendly: $('sSleepFriendly').checked,
    quiet_hours: quietHours,
    broken_auto: $('sBrokenAuto').checked,
    broken_auto_idle_minutes: Math.max(1, Math.min(720, Number($('sBrokenIdle').value || 10))),
    broken_auto_batch: Math.max(10, Math.min(5000, Number($('sBrokenBatch').value || 200))),
    wake_enabled: wakeEnabled,
    wake_window: wakeWindow,
    ai_enabled: aiEnabled,
    ai_endpoint: aiEndpoint,
    ai_model: ($('sAIModel') ? $('sAIModel').value.trim() : 'llama3.2'),
    op_log_enabled: ($('sOpLogEnabled') ? $('sOpLogEnabled').checked : true),
    op_log_retention_days: ($('sOpLogDays') ? Math.max(0, Math.min(3650, Number($('sOpLogDays').value || 180))) : 180),
    op_log_reads: ($('sOpLogReads') ? $('sOpLogReads').checked : false),
    op_log_detail_limit: ($('sOpLogLimit') ? Math.max(0, Math.min(1000, Number($('sOpLogLimit').value || 200))) : 200)
  };
  // 空值只有用户明确勾选“清除”才提交，避免留空不改被误解成清空令牌
  if (token || tokenClear) body.access_token = tokenClear ? '' : token;
  // v3.7.2：提交期间禁用保存按钮并只保留一个状态提示，避免连点造成重复提交、
  // 以及「已保存」与失败 toast 同时出现（P-12）。
  const saveBtn = document.querySelector('[data-act="save-settings"]');
  if (saveBtn && saveBtn.disabled) return;
  const saveHint = $('saveHint');
  if (saveBtn) saveBtn.disabled = true;
  if (saveHint) { saveHint.textContent = '保存中…'; saveHint.style.color = ''; }
  try {
    const r = await postJSON('/api/settings', body);
    if (saveHint) saveHint.textContent = '已保存';
    let extra = r.restart_watch ? '，监控设置将在 1 分钟内生效' : '';
    if (r.intro_reset) extra += '，片头长度已变更，请重新扫描以重算片头指纹';
    if (r.video_reset) extra += '，忽略区间已变更，请重新分析视频';
    if (r.audio_reset) extra += '，音频比较时长已变更，请重新扫描以重算音频指纹';
    toast('设置已保存' + extra, 'ok');
    S.introN = Math.min(300, Math.max(1, Number($('sIntroSec').value || 10)));
    await refreshStatus();
    updateTabVisibility();
  } catch(e) {
    if (saveHint) { saveHint.textContent = '保存失败'; saveHint.style.color = '#c0392b'; }
    toast(e.message, 'err');
  } finally {
    if (saveBtn) saveBtn.disabled = false;
  }
}

async function restoreIDs(ids){
  if (!ids.length) { toast('请先选择'); return; }
  try {
    const pv = await postJSON('/api/trash/preview', { action:'restore', ids });
    showPreviewModal('恢复预览', pv.items || [], '确认恢复', async () => {
      let r;
      try { r = await postJSON('/api/trash/restore', { ids }); }
      catch(e) { closeModal(); toast(e.message, 'err'); return; }
      closeModal();
      const items = resultItemsFrom(r, 'restored');
      const fails = items.filter(x => !x.ok);
      if (fails.length) showResultModal('恢复结果：部分失败', items);
      else toast('已恢复 ' + (r.restored || []).length + ' 个文件', 'ok');
      resetAssistCache();
      await loadTrash();
      await refreshStatus();
    });
  } catch(e) { toast(e.message, 'err'); }
}

async function deleteIDs(ids){
  if (!ids.length) { toast('请先选择'); return; }
  try {
    const pv = await postJSON('/api/trash/preview', { action:'delete', ids });
    // v3.17.3（F-17）：后端要求显式 confirm，避免跨站 GET/裸请求直接触发永久删除。
    showPreviewModal('永久删除预览（不可恢复）', pv.items || [], '确认永久删除', async () => {
      let r;
      try { r = await postJSON('/api/trash/delete', { ids, confirm: true }); }
      catch(e) {
        closeModal(); toast(e.message, 'err');
        // 请求失败也要重拉，避免界面继续显示已经变化的旧清单
        await loadTrash(); await refreshStatus(); return;
      }
      closeModal();
      const items = resultItemsFrom(r, 'moved');
      const fails = items.filter(x => !x.ok);
      if (fails.length) {
        showResultModal('永久删除结果：部分失败', items);
        toast('永久删除完成：成功 ' + (r.moved || []).length + ' 个，失败 ' + fails.length + ' 个', 'err');
      } else toast('已永久删除 ' + (r.moved || []).length + ' 个文件', 'ok');
      // v3.10.5：永久删除后必须核对「预期消失的文件是否真的没了」。
      showReconcile(r.reconcile);
      resetAssistCache();
      await loadTrash();
      await refreshStatus();
    });
  } catch(e) { toast(e.message, 'err'); }
}

// trashExport 触发浏览器下载隔离区 zip（v3.1.0）。
// 大批量导出用直链而不是 fetch+blob，避免把整个 zip 读进浏览器内存。
function trashExport(ids, all){
  if (!all && !ids.length) { toast('请先勾选要导出的条目'); return; }
  const q = all ? 'all=true' : ('ids=' + ids.join(','));
  const a = document.createElement('a');
  a.href = '/api/trash/export?' + q;
  a.download = '';
  document.body.appendChild(a);
  a.click();
  a.remove();
  // 直链下载无法读取流式响应中的错误，不能提前承诺“导出成功”
  toast('已请求导出备份，请以浏览器下载结果为准；完成后列表会显示「已备份」', 'ok', 4000);
  // 后端在开始下载前已写入 exported_at，稍后刷新即可看到徽标。
  setTimeout(() => { loadTrash().catch(() => {}); }, 2000);
}

async function emptyTrash(){
  const ids = (S.trash || []).map(x => x.id).filter(Boolean);
  if (!ids.length) { toast('隔离区已经是空的'); return; }
  try {
    // 清空与逐条删除走同一份预览，让用户先看到每一项及不可执行原因（P-02）
    const pv = await postJSON('/api/trash/preview', { action:'delete', ids });
    showPreviewModal('清空隔离区预览（永久删除，不可恢复）', pv.items || [], '确认清空隔离区', async () => {
      let r;
      // v3.10.6：后端要求显式 confirm，避免跨站 GET 触发永久删除。
      try { r = await postJSON('/api/trash/empty', { confirm: true }); }
      catch(e) { closeModal(); toast(e.message, 'err'); return; }
      closeModal();
      const errors = r.errors || [];
      if (errors.length) {
        const items = errors.map(reason => ({ path:'隔离区条目', ok:false, reason }));
        showResultModal('清空隔离区结果：部分失败', items);
        toast('清空完成：删除 ' + (r.removed || 0) + ' 个，失败或跳过 ' + errors.length + ' 个', 'err');
      } else toast('已清空隔离区，删除 ' + (r.removed || 0) + ' 个文件', 'ok');
      await loadTrash(); await refreshStatus();
    });
  } catch(e) { toast(e.message, 'err'); }
}

// ===== 模糊文件名分组（v1.34 F3）：音序键相同或单字差异成组；仅分组不删 =====
async function loadFuzzy(){
  S.fuzzy = S.fuzzy || { near: true, page: 1, groups: [], total: 0, loading: false };
  const fz = S.fuzzy;
  fz.loading = true; renderFuzzy();
  try {
    const r = await api('/api/groups/fuzzy?near=' + (fz.near ? 1 : 0) + '&page=' + fz.page + '&page_size=20' + advFilterParams());
    fz.groups = r.groups || [];
    fz.total = r.total || 0;
    fz.loading = false;
    renderFuzzy();
  } catch(e) {
    fz.loading = false;
    $('view').innerHTML = '<div class="empty">加载失败：' + esc(e && e.message || e) + '</div>';
  }
}

// ===== 语义分组（v1.36 F7）：噪声剥离后核心键精确分组；仅辅助展示 =====
async function loadSemantic(){
  S.semantic = S.semantic || { page: 1, groups: [], total: 0, loading: false };
  const fz = S.semantic;
  fz.loading = true; renderSemantic();
  try {
    const r = await api('/api/groups/semantic?page=' + fz.page + '&page_size=20' + advFilterParams());
    fz.groups = r.groups || [];
    fz.total = r.total || 0;
    fz.loading = false;
    renderSemantic();
  } catch(e) {
    fz.loading = false;
    $('view').innerHTML = '<div class="empty">加载失败：' + esc(e && e.message || e) + '</div>';
  }
}

// ===== 按文件夹改名（v1.37 F5）：B 类串行名替换为 <文件夹名>_001 =====
async function frPreview(){
  try {
    const r = await postJSON('/api/folder_rename/preview', { dir: S.frDir, prefix_a: S.frPrefixA });
    const rows = (r.items || []).map(it => {
      const cls = it.class === 'serial' ? '<span class="badge">串行名</span>' : '<span class="hint">保留</span>';
      const st = it.error ? '<span style="color:var(--danger)">' + esc(it.error) + '</span>'
        : (it.new === it.old ? '<span class="hint">不变</span>'
        : esc(it.old) + ' → <b>' + esc(it.new) + '</b>');
      return '<tr><td>' + cls + '</td><td class="mono">' + esc(it.old) + '</td><td>' + st + '</td></tr>';
    }).join('');
    setModal('按文件夹改名：' + S.frDir,
      '<div class="hint" style="margin-bottom:8px">预计改名 ' + (r.rename_count || 0) + ' 项。</div>' +
      '<label class="checkline" style="margin-bottom:8px"><input type="checkbox" id="frPrefixA" data-act="fr-prefix"' + (S.frPrefixA ? ' checked' : '') + '> 给有描述的文件也统一加「文件夹名 - 」前缀</label>' +
      '<div class="preview-list" style="max-height:50vh;overflow:auto"><table><thead><tr><th>类别</th><th>原文件名</th><th>改名结果</th></tr></thead><tbody>' + rows + '</tbody></table></div>' +
      '<div class="form-actions" style="margin-top:10px"><button class="btn danger" data-act="fr-apply"' + ((r.rename_count || 0) ? '' : ' disabled') + '>执行改名</button>' +
      (S.frTask ? '<button class="btn ghost" data-act="fr-undo">撤销刚才的改名</button>' : '') +
      '<button class="btn ghost" data-act="modal-cancel">关闭</button></div>', null);
  } catch(e) { toast(e.message, 'err'); }
}

function frRenderResult(r){
  const rows = (r.items || []).filter(it => it.error).map(it =>
    '<tr><td class="mono">' + esc(it.old) + '</td><td style="color:var(--danger)">' + esc(it.error) + '</td></tr>').join('');
  setModal('改名结果',
    '<div class="hint">完成 ' + (r.rename_count || 0) + ' 项。' +
    (r.task_id ? '可撤销（任务 ' + r.task_id + '）。' : '') + '</div>' +
    (rows ? '<div class="preview-list" style="max-height:40vh;overflow:auto"><table><thead><tr><th>文件</th><th>失败原因</th></tr></thead><tbody>' + rows + '</tbody></table></div>' : '<div style="height:12px"></div>') +
    '<div class="form-actions" style="margin-top:10px">' +
    (r.task_id ? '<button class="btn ghost" data-act="fr-undo">撤销本次改名</button>' : '') +
    '<button class="btn primary" data-act="modal-cancel">关闭</button></div>', null);
}

// ===== 智能保留建议（v1.40 F4 一期）：无状态打分 + 标记通道应用，永不自动删除 =====
async function retainSuggest(){
  const groups = (S.groups || []).filter(g => (g.files || []).length >= 2);
  if (!groups.length) { toast('当前页没有可分析的重复组', 'err'); return; }
  // v3.17.6（F-25）：后端最多接受 200 组，超限先在前端提示，避免整批 400。
  if (groups.length > 200) { toast('当前有 ' + groups.length + ' 组，最多一次分析 200 组，请先收窄筛选', 'err'); return; }
  try {
    const r = await postJSON('/api/retain/score', { groups: groups.map(g => ({
      key: g.key,
      files: (g.files || []).map(f => ({ path: f.path, name: f.name, size: f.size, width: f.width || 0, height: f.height || 0, bit_rate: f.bit_rate || 0, duration: f.duration || 0, mtime: f.mtime || 0 }))
    })) });
    S.retainSugs = r.suggestions || [];
    const confBadge = { high: '<span class="badge" style="background:#e6f7ef;color:#059669">高置信</span>', medium: '<span class="badge" style="background:#fff7e6;color:#d97706">中置信</span>', low: '<span class="badge" style="background:#fee2e2;color:#dc2626">需人工</span>' };
    const rows = S.retainSugs.map(sg => {
      const s = sg.suggestion;
      return '<tr><td>' + (confBadge[s.confidence] || '') + '</td><td class="mono">' + esc(s.retain.split('/').pop()) + '</td><td>' + esc(s.reason) + '</td></tr>';
    }).join('');
    const nHigh = S.retainSugs.filter(s => s.suggestion.confidence === 'high').length;
    setModal('智能保留建议（本页 ' + S.retainSugs.length + ' 组，高置信 ' + nHigh + ' 组）',
      '<div class="hint" style="margin-bottom:8px">应用建议只把每组其余文件<b>加入选择标记</b>，之后照常走隔离流程（可恢复）；不会自动删除。</div>' +
      '<div class="preview-list" style="max-height:50vh;overflow:auto"><table><thead><tr><th>置信</th><th>建议保留</th><th>理由</th></tr></thead><tbody>' + rows + '</tbody></table></div>' +
      '<div class="form-actions" style="margin-top:10px"><button class="btn primary" data-act="retain-apply">应用建议到选择标记</button><button class="btn ghost" data-act="retain-ai" title="调用设置页配置的本地 AI，仅生成解释，不改变建议">AI 生成理由（可选）</button><button class="btn ghost" data-act="modal-cancel">关闭</button></div>', null);
  } catch(e) { toast(e.message, 'err'); }
}

// reportPrefOnTrash 隔离成功后上报偏好（F4 二期学习闭环）：
// 被隔离的路径若与"智能保留建议"的保留项冲突，记为改选。
async function reportPrefOnTrash(paths){
  const applied = S.retainApplied || [];
  if (!applied.length || !paths || !paths.length) return;
  const entries = [];
  applied.forEach(a => {
    if (paths.includes(a.suggested)) {
      // 建议保留的文件反而被清理 = 改选
      entries.push({ group_key: a.group_key, suggested: a.suggested, chosen: paths.find(p => p !== a.suggested) || paths[0] });
    } else if (paths.some(p => applied.some(b => b.group_key === a.group_key && b.suggested !== a.suggested)) || paths.some(p => !applied.some(b => b.suggested === p))) {
      // 清理的是组内非保留文件 = 采纳（只记一次）
      if (!entries.some(e => e.group_key === a.group_key)) {
        entries.push({ group_key: a.group_key, suggested: a.suggested, chosen: '' });
      }
    }
  });
  if (!entries.length) return;
  try { await postJSON('/api/retain/pref', { entries }); } catch(e) { /* 学习记录失败不影响主流程 */ }
}

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
    const r = await api('/api/search?' + searchParams(st, '').toString());
    if (st.reqId !== reqId) return; // 只渲染最新一次请求，防止乱序覆盖
    st.items = r.items || []; st.total = Number(r.total || 0); st.took = Number(r.took_ms || 0);
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
    const r = await api('/api/search?' + searchParams(st, st.nextCursor).toString());
    if (st.reqId !== reqId) return;
    const seen = new Set(st.items.map(x => x.path));
    (r.items || []).forEach(it => { if (!seen.has(it.path)) { seen.add(it.path); st.items.push(it); } });
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
