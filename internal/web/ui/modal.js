// 查重助手 verify Web UI · 弹窗与交互面板（v3.0.0 从 app.js 拆分）
// 加载顺序第 5：通用弹窗、预览、向导、目录选择器、双图对比、片头剪除与路径穿梭框。
// 这些组件都由事件触发，不参与启动阶段的立即执行。

function readAssistUI(){
  const c = $('aScopeSel'), u = $('aScopeUnmarked'), k = $('aKeepCrit');
  if (c) S.scopeSelOnly = c.checked;
  if (u) S.scopeUnmarkedOnly = u.checked;
  if (k) S.keepCriterion = k.value;
  const t = $('aText');
  if (t) S.textPattern = t.value;
  const tf = $('aTextField'); if (tf) S.textField = tf.value;
  const tr = $('aTextRegex'); if (tr) S.textRegex = tr.checked;
  const tc = $('aTextCase'); if (tc) S.textCase = tc.checked;
  const tw = $('aTextWhole'); if (tw) S.textWhole = tw.checked;
  const rows = Array.from(document.querySelectorAll('[data-pref-index]'));
  if (rows.length) {
    S.prefs = rows.map(r => ({
      key: r.dataset.prefKey,
      dir: (r.querySelector('[data-pref-dir]') || {}).value || 'max',
      on: !!(r.querySelector('[data-pref-on]') || {}).checked
    }));
  }
}

function assistBodyHtml(){
  const groups = S.assistGroups || S.groups || [];
  const allMarked = allMarkedInfo(groups);
  const scope = filtersActive() ? '仅当前筛选后的结果' : '作用于全部结果';
  // 2026-11-08：批量操作只覆盖“已加载”的分组，把覆盖数放到标题旁，
  // 避免用户以为「标记全部」就是整库（未加载的部分不会被处理）。
  const total = Number(S.total || 0);
  const gap = Math.max(0, total - groups.length);
  const coverage = gap > 0
    ? `<span class="badge warn" title="批量标记/保留建议只作用于已加载的分组">覆盖 ${groups.length.toLocaleString()} / ${total.toLocaleString()} 组</span>`
    : '';
  const head = `<div class="assist-head"><b>选择助手</b><span class="badge">${esc(scope)}</span>${coverage}<span class="help-link" data-act="help-link" data-sec="assist" title="查看手册：选择助手与筛选器">?</span>
    <div class="spacer"></div>
    <button class="btn ghost" data-act="assist-toggle">${S.assistOpen?'收起':'展开'}</button></div>`;
  if (!S.assistOpen) return head;
  const warn = allMarked.length ? `<div class="allwarn"><b>⚠ ${allMarked.length} 个组所有文件均被标记</b>
    <div class="hint" style="color:var(--color-error)">${allMarked.slice(0,5).map(x=>esc(x.name)).join('、')}${allMarked.length>5?' …':''}</div>
    <button class="btn danger" data-act="assist-jump-allmarked">跳转到第一个全标记组</button></div>` : '';
  const prefs = S.prefs.map((p,i)=>`<div class="pref-row" draggable="true" data-pref-index="${i}" data-pref-key="${esc(p.key)}">
      <span class="drag">≡</span>
      <input type="checkbox" data-pref-on ${p.on?'checked':''}>
      <span class="pname">${esc(prefName(p.key))}</span>
      <select data-pref-dir>${prefDirOptions(p.key,p.dir)}</select>
    </div>`).join('');
  const paths = S.keepPaths.map((p,i)=>`<span class="chip">${esc(p)}<button data-act="assist-remove-path" data-idx="${i}" title="移除">×</button></span>`).join('');
  const critOptions = [
    ['quality','综合质量（分辨率 + 大小 + 时长 + 修改时间）'],
    ['resolution_max','分辨率最高'],['resolution_min','分辨率最低'],
    ['duration_max','时长最长'],['duration_min','时长最短'],
    ['size_max','文件最大'],['size_min','文件最小'],
    ['mtime_max','修改最新'],['mtime_min','修改最旧'],
    ['ctime_max','创建最新'],['ctime_min','创建最旧'],
    ['bitrate_max','码率最高'],['bitrate_min','码率最低'],
    ['samplerate_max','采样率最高'],['samplerate_min','采样率最低'],
    ['depth_min','路径最浅'],['depth_max','路径最深'],
    ['name_max','文件名最长'],['name_min','文件名最短']
  ].map(([v,t])=>`<option value="${v}" ${S.keepCriterion===v?'selected':''}>${t}</option>`).join('');
  return `${head}${warn}
    <div class="asec" data-sec-title="列表排序"><label class="rp-head" data-act="asec-fold" data-sec="列表排序" style="cursor:pointer"><span class="rp-arrow">▾</span>列表排序</label>
      <div class="asrow">
        <select id="aSort" data-act="assist-sort" style="flex:1;min-width:0">
          <option value="waste"${S.sort==='waste'?' selected':''}>可节省空间</option>
          <option value="total_size"${S.sort==='total_size'?' selected':''}>组总大小</option>
          <option value="count"${S.sort==='count'?' selected':''}>文件数量</option>
          <option value="name"${S.sort==='name'?' selected':''}>名称</option>
          <option value="path"${S.sort==='path'?' selected':''}>路径（同目录凑堆）</option>
          <option value="type"${S.sort==='type'?' selected':''}>文件类型</option>
        </select>
        <select id="aSortOrder" data-act="assist-sort-order" style="flex:1;min-width:0">
          <option value="desc"${S.sortOrder!=='asc'?' selected':''}>降序</option>
          <option value="asc"${S.sortOrder==='asc'?' selected':''}>升序</option>
        </select>
      </div>
      <div class="asrow" style="justify-content:center"><button class="btn ghost" data-act="assist-sort-apply">应用</button></div>
      <div class="hint">全局排序（跨页一致）；路径排序会把同目录的组凑在一起</div>
    </div>
    <div class="asec" data-sec-title="筛选器"><label class="rp-head" data-act="filter-toggle" data-sec="筛选器" style="cursor:pointer"><span class="rp-arrow">${S.listFilter.open?'▾':'▸'}</span>筛选器<span class="spacer"></span><span class="hint">${S.listFilter.open?'收起':'展开'}</span></label>
      ${S.listFilter.open ? `<div>
        <div class="asrow" style="flex-wrap:wrap;gap:6px">
          <label class="checkline" style="font-size:var(--text-xs)"><input type="checkbox" data-act="filter-set" data-k="marked"${S.listFilter.marked?' checked':''}> 已标记</label>
          <label class="checkline" style="font-size:var(--text-xs)"><input type="checkbox" data-act="filter-set" data-k="unmarked"${S.listFilter.unmarked?' checked':''}> 未标记</label>
          <label class="checkline" style="font-size:var(--text-xs)"><input type="checkbox" data-act="filter-set" data-k="protected"${S.listFilter.protected?' checked':''}> 已保护</label>
        </div>
        <div class="asrow" style="flex-wrap:wrap;gap:6px">
          <label class="checkline" style="font-size:var(--text-xs)"><input type="checkbox" data-act="filter-set" data-k="partial"${S.listFilter.partial?' checked':''}> 存在部分标记</label>
          <label class="checkline" style="font-size:var(--text-xs)"><input type="checkbox" data-act="filter-set" data-k="allMarked"${S.listFilter.allMarked?' checked':''}> 全部已标记</label>
        </div>
        <label class="checkline" style="font-size:var(--text-xs);margin-top:4px"><input type="checkbox" data-act="filter-adv"${S.listFilter.advOpen?' checked':''}> 高级条件</label>
        ${S.listFilter.advOpen ? `<div style="border-left:2px solid var(--line);padding-left:8px;margin-top:4px">
          <div class="asrow" style="flex-wrap:wrap;gap:6px;font-size:var(--text-xs)">
            <span>组内文件数</span><input id="fCountMin" data-act="filter-num" data-k="countMin" type="number" min="0" value="${esc(S.listFilter.countMin)}" style="width:64px" placeholder="从">
            <span>至</span><input id="fCountMax" data-act="filter-num" data-k="countMax" type="number" min="0" value="${esc(S.listFilter.countMax)}" style="width:64px" placeholder="至">
          </div>
          <div class="asrow" style="flex-wrap:wrap;gap:6px;font-size:var(--text-xs)">
            <span>组总大小(MB)</span><input id="fSizeMin" data-act="filter-num" data-k="sizeMin" type="number" min="0" value="${esc(S.listFilter.sizeMin)}" style="width:72px" placeholder="从">
            <span>至</span><input id="fSizeMax" data-act="filter-num" data-k="sizeMax" type="number" min="0" value="${esc(S.listFilter.sizeMax)}" style="width:72px" placeholder="至">
          </div>
          <div class="asrow" style="flex-wrap:wrap;gap:6px;font-size:var(--text-xs)">
            <span>单文件大小(MB)</span><input id="fFileMin" data-act="filter-num" data-k="fileMin" type="number" min="0" value="${esc(S.listFilter.fileMin)}" style="width:72px" placeholder="从">
            <span>至</span><input id="fFileMax" data-act="filter-num" data-k="fileMax" type="number" min="0" value="${esc(S.listFilter.fileMax)}" style="width:72px" placeholder="至">
          </div>
          <div class="asrow" style="flex-wrap:wrap;gap:6px;font-size:var(--text-xs)">
            <span>扩展名</span><input data-act="lf-input" data-k="exts" type="text" value="${esc(S.listFilter.exts)}" style="width:170px" placeholder="逗号分隔，如 jpg,png">
          </div>
          <div class="asrow" style="flex-wrap:wrap;gap:6px;font-size:var(--text-xs)">
            <span>关键词</span><input data-act="lf-input" data-k="q" type="text" value="${esc(S.listFilter.q)}" style="width:140px" placeholder="文件名包含">
          </div>
          <div class="asrow" style="flex-wrap:wrap;gap:6px;font-size:var(--text-xs)">
            <span>修改时间</span><input data-act="lf-input" data-k="since" type="date" value="${esc(S.listFilter.since)}" style="width:140px">
            <span>至</span><input data-act="lf-input" data-k="until" type="date" value="${esc(S.listFilter.until)}" style="width:140px">
            <label class="checkline" style="font-size:var(--text-xs);margin-left:4px"><input type="checkbox" data-act="filter-set" data-k="dateOnly"${S.listFilter.dateOnly?' checked':''}> 仅日期（忽略时分）</label>
          </div>
          <div class="asrow" style="flex-wrap:wrap;gap:6px;font-size:var(--text-xs)">
            <span>时长容差(秒)</span><input data-act="lf-input" data-k="maxDurDiff" type="number" min="0" value="${esc(S.listFilter.maxDurDiff)}" style="width:80px" placeholder="不限">
            <span>同盘</span>
            <select data-act="lf-input" data-k="sameDrive" style="width:100px">
              <option value="">不限</option>
              ${((S.status && S.status.config && S.status.config.volumes) || []).map(v => {
                const n = normPath(v); const parts = n.split('/').filter(Boolean);
                return `<option value="${esc(n)}"${S.listFilter.sameDrive===n?' selected':''}>${esc(parts[0]||n)}</option>`;
              }).join('')}
            </select>
            <label class="checkline" style="font-size:var(--text-xs)"><input type="checkbox" data-act="filter-set" data-k="ignoreSameFolder"${S.listFilter.ignoreSameFolder?' checked':''}> 忽略同目录重复</label>
          </div>
          <div class="asrow" style="flex-wrap:wrap;gap:6px;font-size:var(--text-xs)">
            <span>文件夹名</span><input data-act="lf-input" data-k="folderName" type="text" value="${esc(S.listFilter.folderName)}" style="width:120px" placeholder="如 某剧集">
            <span>层级</span><input data-act="lf-input" data-k="folderDepth" type="number" min="0" max="50" value="${esc(S.listFilter.folderDepth)}" style="width:64px" placeholder="不限">
            <span class="hint" style="font-size:var(--text-xs)">层级 0 表示任意层</span>
          </div>
          <div class="asrow" style="flex-wrap:wrap;gap:6px;font-size:var(--text-xs)">
            <span>大小容差(KB)</span><input data-act="lf-input" data-k="sizeTolKB" type="number" min="0" value="${esc(S.listFilter.sizeTolKB)}" style="width:88px" placeholder="不限">
            <label class="checkline" style="font-size:var(--text-xs)"><input type="checkbox" data-act="filter-set" data-k="similarName"${S.listFilter.similarName?' checked':''}> 只保留文件名相似</label>
            <label class="checkline" style="font-size:var(--text-xs)"><input type="checkbox" data-act="filter-set" data-k="crossDevice"${S.listFilter.crossDevice?' checked':''}> 标记另一设备也有的文件</label>
          </div>
          <div class="asrow" style="flex-wrap:wrap;gap:6px;font-size:var(--text-xs)">
            <span>标签</span>
            <select data-act="lf-input" data-k="tagName" style="width:110px">
              <option value="">不限</option>
              ${(S.videoTags || []).map(t => `<option value="${esc(t.name)}"${S.listFilter.tagName===t.name?' selected':''}>${esc(t.name)}</option>`).join('')}
            </select>
            <span>值</span><input data-act="lf-input" data-k="tagValue" type="text" value="${esc(S.listFilter.tagValue)}" style="width:130px" placeholder="留空表示任意值">
          </div>
          <label class="checkline" style="font-size:var(--text-xs)"><input type="checkbox" data-act="filter-set" data-k="showAll"${S.listFilter.showAll?' checked':''}> 显示筛选组中的所有文件</label>
        </div>` : ''}
        <div class="asrow" style="justify-content:center;margin-top:8px"><button class="btn ghost" data-act="filter-clear" style="padding:2px 12px">清除全部筛选</button></div>
        <div class="hint">大小/扩展名/关键词/修改时间由服务端过滤（重拉列表），标记/勾选只在筛选结果内生效</div>
      </div>` : ''}
    </div>
    <div class="asec" data-sec-title="作用范围"><label class="rp-head" data-act="asec-fold" data-sec="作用范围" style="cursor:pointer"><span class="rp-arrow">▾</span>作用范围</label>
      <label><input id="aScopeSel" type="checkbox" ${S.scopeSelOnly?'checked':''}> 仅作用于当前选中的行所在组</label>
      <label><input id="aScopeUnmarked" type="checkbox" ${S.scopeUnmarkedOnly?'checked':''}> 仅作用于尚无标记的组</label>
      <div class="hint">点击结果行可选中/取消选中一行；与文件标记互不影响。</div>
    </div>
    <div class="asec" data-sec-title="组级标记"><label class="rp-head" data-act="asec-fold" data-sec="组级标记" style="cursor:pointer"><span class="rp-arrow">▾</span>组级标记</label>
      <select id="aKeepCrit">${critOptions}</select>
      <button class="btn primary" data-act="retain-suggest" style="width:100%;margin:6px 0" title="按质量/完整性/路径/文件名打分，建议每组保留哪个；应用建议只改选择标记，可撤销">智能保留建议</button>
      <div class="asrow">
        <button class="btn" data-act="assist-keep-one" style="flex:1">每组保留一个</button>
        <button class="btn" data-act="assist-mark-one" style="flex:1">每组标记一个</button>
      </div>
      <div class="asrow" style="justify-content:center">
        <button class="btn" data-act="assist-keep-one-folder">每组 + 每文件夹保留一个</button>
      </div>
      ${S.resultType === 'exact' ? `<div class="asrow"><button class="btn warn" data-act="relink-selected" title="把勾选的文件变成指向保留者的硬链接，数据只存一份，文件都还在">硬链接合并（勾选为目标）</button></div>
      <div class="hint">只在结果页切到「精确重复」类型时可用：先「每组保留一个」，勾选其余作为合并目标。合并后文件都还在、实际只占一份空间；<b>此操作无法撤销</b>。</div>` : ''}
    </div>
    <div class="asec" data-sec-title="基于偏好的标记（可拖拽排序）"><label class="rp-head" data-act="asec-fold" data-sec="基于偏好的标记（可拖拽排序）" style="cursor:pointer"><span class="rp-arrow">▾</span>基于偏好的标记（可拖拽排序）</label>
      <div id="aPrefs">${prefs}</div>
      <div class="asrow">
        <button class="btn" data-act="assist-pref" style="flex:1" title="每组保留偏好最优的一个，标记其余">按偏好标记其余</button>
        <button class="btn" data-act="assist-pref-exclude" style="flex:1" title="每组只标记偏好最差的一个">按偏好标记排除</button>
      </div>
      <div class="asrow" style="justify-content:center"><button class="btn ghost" data-act="assist-reset-prefs">默认偏好</button><span class="help-link" data-act="help-link" data-sec="assist" title="查看手册：偏好标记">?</span></div>
      <div class="hint">按顺序逐项比较；全部偏好相同则跳过该组。</div>
    </div>
    <div class="asec" data-sec-title="优先保留路径"><label class="rp-head" data-act="asec-fold" data-sec="优先保留路径" style="cursor:pointer"><span class="rp-arrow">▾</span>优先保留路径</label>
      <div class="asrow"><input id="aKeepPath" type="text" style="flex:1;min-width:0" placeholder="" value="${esc(S.keepPathDraft)}"><button class="btn" data-act="assist-add-path">添加</button></div>
      <div class="asrow" style="justify-content:flex-end"><button class="btn ghost" data-act="assist-fill-row-path" ${S.rowSel.size===1?'':'disabled'} title="${S.rowSel.size===1?'将选中行路径填入上方输入框':'请只选一行'}">填充选中行路径</button></div>
      <div class="asrow">
        <button class="btn" data-act="assist-location" style="flex:1" title="把保留路径之外的重复文件全部标记">按路径标记</button>
        <button class="btn ghost" data-act="assist-unmark-path" style="flex:1" title="取消输入框路径下所有文件的标记">取消当前路径下的标记</button>
      </div>
      <div>${paths || '<span class="hint">尚未设置保留路径</span>'}</div>
    </div>
    <div class="asec" data-sec-title="按文本标记（不检查组完整性）"><label class="rp-head" data-act="asec-fold" data-sec="按文本标记（不检查组完整性）" style="cursor:pointer"><span class="rp-arrow">▾</span>按文本标记（不检查组完整性）</label>
      <input id="aText" type="text" placeholder="支持正则" value="${esc(S.textPattern)}">
      <div class="asrow">
        <select id="aTextField" style="flex:1"><option value="name" ${S.textField==='name'?'selected':''}>文件名</option><option value="path" ${S.textField==='path'?'selected':''}>完整路径</option></select>
        <label><input id="aTextRegex" type="checkbox" ${S.textRegex?'checked':''}> 正则</label>
      </div>
      <div class="asrow"><label><input id="aTextCase" type="checkbox" ${S.textCase?'checked':''}> 区分大小写</label>
        <label><input id="aTextWhole" type="checkbox" ${S.textWhole?'checked':''}> 整列匹配</label></div>
      <div class="asrow"><button class="btn" data-act="assist-text-mark">标记匹配项</button>
        <button class="btn ghost" data-act="assist-text-unmark">取消匹配项</button></div>
    </div>
    <div class="asec" data-sec-title="辅助操作"><label class="rp-head" data-act="asec-fold" data-sec="辅助操作" style="cursor:pointer"><span class="rp-arrow">▾</span>辅助操作</label>
      <div class="asrow">
        <button class="btn ghost" data-act="assist-undo" ${S.undo.length?'':'disabled'} title="快照保留最近 20 步，连点可逐级回退">撤销选择${S.undo.length ? '（' + Math.min(S.undo.length, 20) + ' 步可回退）' : ''}</button>
        <button class="btn ghost" data-act="assist-invert">反转标记</button>
        <button class="btn ghost" data-act="assist-clear">取消全部标记</button>
      </div>
      ${S.assistTruncated?`<div class="hint" style="color:var(--color-error)">助手只覆盖 ${(S.assistCovered||groups.length).toLocaleString()} / ${(S.assistTotal||S.total||0).toLocaleString()} 组，未覆盖的分组不会被批量操作处理；请用筛选器收窄范围。</div>`:''}
    </div>`;
}

function renderAssistant(){
  const el = $('assist');
  if (!el) return;
  // v2.9.0：筛选器的标签下拉需要标签清单，首次渲染时异步补一次
  ensureVideoTags();
  el.innerHTML = assistBodyHtml();
  el.classList.toggle('collapsed', !S.assistOpen);
  // v3.9.1：展开/收起时同步布局类，收起后主列表立即占满剩余宽度。
  const layout = document.querySelector('.resultlayout');
  if (layout) layout.classList.toggle('assist-closed', !S.assistOpen);
  updateRowPathBtn();
  applyAssistWidth();
  applyAsecFold(el);
  bindAssistResize(el);
}

// bindAssistResize v1.41：助手左缘 8px 拖拽调宽（240–640px，记忆到 localStorage）。
// 用户手动拖过之后覆盖"按输入内容自动调宽"的逻辑（S.assistWidthManual）。
function bindAssistResize(el){
  if (el.dataset.resizerBound) return;
  el.dataset.resizerBound = '1';
  el.addEventListener('mousedown', (e) => {
    const rect = el.getBoundingClientRect();
    if (e.clientX - rect.left > 8) return;
    e.preventDefault();
    const layout = document.querySelector('.resultlayout');
    if (!layout) return;
    const startW = parseFloat(getComputedStyle(layout).getPropertyValue('--assist-w')) || 240;
    const startX = e.clientX;
    const move = (ev) => {
      // v3.9.1：收窄助手可调范围，把更多横向空间留给主列表。
      const wr = Math.min(420, Math.max(200, Math.round(startW + ev.clientX - startX)));
      S.assistWidthManual = wr;
      layout.style.setProperty('--assist-w', wr + 'px');
    };
    const up = () => {
      document.removeEventListener('mousemove', move);
      document.removeEventListener('mouseup', up);
      try { localStorage.setItem('assistWidth', String(S.assistWidthManual || 0)); } catch(err) {}
    };
    document.addEventListener('mousemove', move);
    document.addEventListener('mouseup', up);
  });
}

// applyAsecFold v1.24.0：按 S.asecFold 恢复容器内助手各段的折叠状态。
function applyAsecFold(root){
  if (!root) return;
  root.querySelectorAll('[data-sec-title]').forEach(d => {
    d.classList.toggle('folded', !!S.asecFold[d.dataset.secTitle]);
  });
}

function setModal(title, html, action){
  $('mTitle').textContent = title;
  $('mBody').innerHTML = html;
  S.modalAction = action || null;
  $('modal').classList.add('show');
  // v3.10.5：弹窗里的写操作按钮同样要跟着扫描/检测状态置灰。
  applyOpsGuard();
}

function showPreviewModal(title, items, confirmLabel, action){
  const ok = (items||[]).filter(i => i.ok).length;
  const html = `${previewTable(items)}<div class="hint">共 ${(items||[]).length} 项，可执行 ${ok} 项。</div>
    <div class="pm-actions"><button class="btn ghost" data-act="modal-cancel">取消</button>
    ${ok?`<button class="btn primary" data-act="modal-confirm">${esc(confirmLabel)}</button>`:''}</div>`;
  setModal(title, html, action);
}

function showResultModal(title, items){
  setModal(title, previewTable(items), null);
}

function scrollToGroup(key){
  const idx = S.groups.findIndex(g => g.key === key);
  const list = $('groupList');
  const v = S.vlist;
  if (idx < 0 || !list || !v) return false;
  list.scrollTop = Math.max(0, (v.prefix[idx] || 0) - 20);
  renderVirtual(true);
  setTimeout(() => {
    const el = Array.from(document.querySelectorAll('.group')).find(x => x.dataset.gkey === key);
    if (!el) return;
    el.style.outline = '2px solid var(--danger)';
    setTimeout(() => { el.style.outline = ''; }, 1600);
  }, 80);
  return true;
}

function wizardBaseList(w){
  if (w && w.mode === 'audio') return ['仅音频相似'];
  const out = [];
  if (w.content) out.push('精确内容');
  if (w.name) out.push('同名文件');
  if (w.video) out.push('视频相似');
  if (w.intro) out.push('相同片头');
  return out;
}

function wizardChips(paths, kind){
  if (!paths.length) return '<span class="hint">尚未选择</span>';
  // v1.41.1：路径包一层 chip-path，长路径省略号截断（title 悬停看全），不再撑破容器
  return paths.map(p => `<span class="chip"><span class="chip-path" title="${esc(p)}">${esc(p)}</span><button data-act="wiz-remove-${kind}" data-path="${esc(p)}" title="移除">×</button></span>`).join('');
}

function wizardStepHtml(step, title){
  return `<div class="wstep ${step === S.wizard.step ? 'on' : ''}"><b>${step}</b>${title}</div>`;
}

function renderWizard(){
  const w = S.wizard;
  if (!w) return;
  const ff = S.status && S.status.ffmpeg;
  const ffOk = !!(ff && ff.available);
  if (!ffOk) w.video = false;
  const r = w.rules || defaultSearchRules('content');
  const attrKeys = Object.keys(SEARCH_ATTR_LABELS);
  const attrHtml = attrKeys.map(k => `<div class="wopt mini"><label><input type="checkbox" data-act="wiz-attr" data-attr="${esc(k)}" ${(r.attrs||[]).includes(k)?'checked':''}> ${esc(SEARCH_ATTR_LABELS[k])}</label></div>`).join('');
  const bases = [];
  if (w.content) bases.push('精确内容');
  if (w.name) bases.push('名称索引');
  if (w.video) bases.push('视频指纹');
  if (w.intro) bases.push('片头指纹');
  if (w.audio) bases.push('音频指纹');
  if (w.photo) bases.push('图片指纹');
  const step1Advanced = `
    <p><button class="btn ghost" data-act="wiz-advanced-off">← 返回推荐组合</button></p>
    ${(() => {
      const presets = (((S.status || {}).config || {}).search_presets) || [];
      if (!presets.length) return '';
      return `<div class="field" style="max-width:420px;margin-bottom:10px"><label>使用预设</label>
        <select id="wizPreset" data-act="wiz-preset"><option value="">不使用预设</option>
        ${presets.map((p, i) => `<option value="${i}">${esc(p.name)}</option>`).join('')}</select></div>`;
    })()}
    <div class="field" style="max-width:420px;margin-bottom:14px"><label>匹配模式</label>
      <select id="wizBase" data-act="wiz-base">
        ${Object.entries(SEARCH_BASE_LABELS).map(([v, label]) => `<option value="${esc(v)}"${r.base === v ? ' selected' : ''}>${esc(label)}</option>`).join('')}
      </select>
      <span class="help">DC5 式规则：先选主依据，再用下面的条件按 AND 组合。规则只影响搜索分组，不改变已算好的指纹。</span></div>
    <div class="section-title">更多搜索选项（同时满足才成一组）</div>
    <div class="wbasis">${attrHtml}</div>
    <div class="asrow" style="flex-wrap:wrap;gap:8px;margin-top:10px">
      <span class="hint">大小容差</span><input id="wizSizeTol" type="number" min="0" value="${Number(r.size_tolerance_kb||0)}" style="width:90px"> <span class="hint">KB（0=完全相同）</span>
      <span class="hint">相似名阈值</span><input id="wizSimilarPct" type="number" min="50" max="100" value="${Number(r.name_similar_pct||90)}" style="width:70px"> <span class="hint">%</span>
    </div>
    <div class="asrow" style="flex-wrap:wrap;gap:8px;margin-top:8px">
      <span class="hint">文件夹名称口径</span>
      <select id="wizFolderMode" style="width:170px">
        <option value="full"${r.folder_name_mode==='full'?' selected':''}>匹配完整名称</option>
        <option value="depth"${r.folder_name_mode==='depth'?' selected':''}>从顶部匹配深度</option>
        <option value="scan_root"${r.folder_name_mode==='scan_root'?' selected':''}>从搜索根目录匹配</option>
      </select>
      <span class="hint">深度</span><input id="wizFolderDepth" type="number" min="1" max="50" value="${Number(r.folder_name_depth||1)}" style="width:64px">
    </div>
    <div class="asrow" style="flex-wrap:wrap;gap:14px;margin-top:8px">
      <label class="checkline"><input id="wizIgnoreCopy" type="checkbox" ${r.ignore_copy_names?'checked':''}> 忽略带「副本」的文件名</label>
      <label class="checkline"><input id="wizIgnoreSame" type="checkbox" ${r.ignore_same_folder?'checked':''}> 忽略同一文件夹中的重复组</label>
      <label class="checkline"><input id="wizCase" type="checkbox" ${r.case_sensitive?'checked':''}> 区分大小写</label>
    </div>
    <div class="hint" style="margin-top:12px">将自动计算：${esc(bases.join('、') || '仅元数据')}。元数据类规则不会重算 BLAKE3，切回内容查重时需要重扫。</div>`;
  // v4.5.0：默认只展示「推荐组合」，规则组合收进高级模式；
  // 大多数用户只需要「选文件夹 → 开始」，不必先理解 DC5 规则。
  const recBases = !!w.recommend;
  const step1Simple = `
    <div class="field" style="max-width:560px">
      <label>${recBases ? '推荐组合（精确内容 + 同名文件）' : '本次将按上次设置扫描'}</label>
      <div class="wsumcard">
        <div><b>将扫描：</b>${esc(bases.join('、') || '仅元数据')}</div>
        <div><b>规则：</b>${esc(SEARCH_BASE_LABELS[r.base] || r.base)}${(r.attrs||[]).length ? ' · ' + (r.attrs||[]).map(a => SEARCH_ATTR_LABELS[a] || a).join(' + ') : ''}</div>
        <div class="hint">精确重复按内容逐字节比对；同名文件只按文件名归组，内容不一定相同。相似视频/片头/音频/图片是否启用，取决于这里的扫描依据与设置页开关。</div>
      </div>
    </div>
    ${recBases ? '' : '<p><button class="btn" data-act="wiz-recommend">用推荐组合（精确内容 + 同名文件）</button><span class="hint"> 只扫这两类，最快出第一批结果</span></p>'}
    <p class="hint">需要改大小容差、相似名阈值、忽略副本这类组合规则，再打开高级模式。</p>
    <p><button class="btn ghost" data-act="wiz-advanced">打开高级模式</button></p>`;
  const step1 = w.advanced ? step1Advanced : step1Simple;
  const step2 = `
    <div class="section-title">扫描文件夹与排除（左侧点选目录 → › 加入右侧；右侧可标「排除」「保护」）</div>
    <div id="tfScan"></div>
    <div class="warnbox" style="margin-top:14px">排除目录及其所有子目录都不会被扫描；「保护」写入全局保护文件夹（隔离/删除操作默认排除并二次提示）。目录树只显示你在应用内添加或浏览过的目录——先在飞牛系统设置给本应用授权，再把授权路径粘贴到输入框进入。</div>`;
  const ruleSummary = `${SEARCH_BASE_LABELS[r.base] || r.base}${(r.attrs||[]).length ? ' · ' + (r.attrs||[]).map(a => SEARCH_ATTR_LABELS[a] || a).join(' + ') : ''}${r.ignore_copy_names?' · 忽略副本':''}${r.ignore_same_folder?' · 忽略同目录':''}`;
  const step3 = `
    <div class="warnbox" style="background:var(--color-accent-wash);border-color:var(--color-accent-edge);color:var(--color-accent-deep)">
      <b>搜索规则：</b>${esc(ruleSummary)}<br>
      <b>将计算：</b>${esc(bases.join('、') || '仅元数据')}<br>
      <b>扫描文件夹：</b>${w.scanDirs.length} 个<br>
      <b>排除文件夹：</b>${w.excludeDirs.length} 个<br>
      <b>片头长度：</b>${w.intro ? esc(String(w.introSeconds)) + ' 秒' : '不检测'}
    </div>
    <div class="field" style="max-width:260px"><label>最小文件大小（KB）</label>
      <input id="wizMin" type="number" min="0" value="${Number(w.minKB||0)}">
      <span class="help">0 表示不限制；小于该值的文件不参与查重</span></div>
    <div class="checkline" style="margin-top:10px"><input id="wizDupFolders" type="checkbox" ${w.dupFolders !== false ? 'checked' : ''}><label for="wizDupFolders">计算重复文件夹</label><span class="hint">关掉后不计算这一页，打开更快；扫描速度不受影响</span></div>
    <div class="hint" style="margin-top:10px">点击开始后会保存这套规则并启动扫描；扫描完成后自动进入搜索阶段。</div>`;
  const body = w.step === 1 ? step1 : (w.step === 2 ? step2 : step3);
  const nextLabel = w.step === 3 ? '开始查重' : '下一步';
  $('view').innerHTML = `<div class="wizard card">
    <h2 style="margin:0 0 6px">新建查重</h2>
    <div class="hint" style="margin-bottom:16px">按步骤选择查重依据和扫描范围，设置会保存到全局配置。</div>
    <div class="wsteps">${wizardStepHtml(1,'选择依据')}<div class="wline"></div>${wizardStepHtml(2,'选择文件夹')}<div class="wline"></div>${wizardStepHtml(3,'确认开始')}</div>
    ${body}
    <div class="wfooter">
      <button class="btn ghost" data-act="wiz-cancel">取消</button>
      ${w.step > 1 ? '<button class="btn ghost" data-act="wiz-prev">上一步</button>' : ''}
      <button class="btn primary" data-act="wiz-next" ${w.starting?'disabled':''}>${nextLabel}</button>
    </div>
  </div>`;
  document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  if (w.step === 2) {
    setTimeout(() => { const box = $('tfScan'); if (box) tfInit(box, { target: 'scanDirs' }); }, 30);
  }
}

async function openWizard(){
  if (S.status && S.status.state && S.status.state.running) { toast('查重正在进行中，请先停止或等待完成', 'err'); return; }
  // v3.9.0：预设列表可能刚在设置页改过，打开向导前刷新一次状态。
  try { await refreshStatus(); } catch(e) { /* 离线时用旧状态继续 */ }
  const c = S.status ? (S.status.config || {}) : {};
  const ff = S.status && S.status.ffmpeg;
  const ffOk = !!(ff && ff.available);
  S.wizard = {
    step: 1,
    advanced: false,
    recommend: false,
    content: c.content_enabled !== false,
    name: c.name_enabled !== false,
    video: ffOk && c.video_enabled !== false,
    intro: c.intro_enabled === true,
    audio: c.audio_enabled !== false,
    photo: c.photo_enabled !== false,
    introSeconds: Math.min(300, Math.max(1, Number(c.intro_seconds || 10))),
    minKB: Math.round(Number(c.min_size || 0) / 1024),
    dupFolders: c.compute_dup_folders !== false,
    rules: c.search_rules ? JSON.parse(JSON.stringify(c.search_rules)) : deriveSearchRules(c.match_mode || 'all'),
    scanDirs: (c.volumes || []).slice(),
    excludeDirs: (c.exclude_paths || []).slice(),
    starting: false
  };
  applyWizardRulesBases(S.wizard);
  renderWizard();
}

function cancelWizard(){
  S.wizard = null;
  switchTab('overview');
}

function filtersHtml(kind){
  // v3.8.0 合并结果页：类型筛选 + 统一排序 + 重新搜索。
  // v3.17.2：唯一调用点 renderGroups 传入的 S.tab 恒为 results，旧分页面
  // （intro / video / exact 等）的工具栏分支随标签合并失去入口，已整体移除。
  if (kind === 'results') {
    const rs = ruleSearchState();
    const byKind = (rs.totals && rs.totals.by_kind) || {};
    // v3.10.13：facets 到位后用它给类型计数补更准的「当前目录下」口径；
    // 接口未就位（404）时 S.facets 为空，自动退回快照总量，界面无差异。
    const facetTypes = (S.facets && S.facets.types) || [];
    const facetMap = {};
    facetTypes.forEach(t => { if (t && t.type) facetMap[t.type] = Number(t.count || 0); });
    const known = ['exact','samevideo','name','size','video','intro','audio','photo','folder','fuzzy','semantic'];
    const present = known.filter(k => byKind[k] > 0 || facetMap[k] > 0 || (S.resultType || 'all') === k);
    const chips = ['all'].concat(present).map(k => {
      const active = (S.resultType || 'all') === k;
      const label = k === 'all' ? '全部' : resultKindLabel(k);
      // v3.11.1：总数统一口径——「全部」chip 与列表「已显示 N / 共 M 组」都取 results 的 total。
      // 关键：0 是合法值（结果为空），不能再 `|| 0`，只有 null/undefined（尚未加载）才回退，
      // 否则选中类型时 chip 会显示该类型的数量、结果为空时又会跳回整库组数。
      // v3.12.1（UI 审查 P3-1）：改名 totalKnown，避免遮蔽外层的类型数组 known。
      const totalKnown = S.total !== null && S.total !== undefined && Number.isFinite(Number(S.total));
      const allTotal = totalKnown ? Number(S.total)
        : (Number(S.facets && S.facets.total) || Number(rs.totals && rs.totals.groups) || 0);
      const n = k === 'all'
        ? allTotal
        : (facetMap[k] !== undefined ? facetMap[k] : (byKind[k] || 0));
      // v3.11.1：结果为 0 时「全部」也要显示 (0)，不能再按真假值隐藏数字
      const showN = k === 'all' ? (totalKnown || n > 0) : n > 0;
      return `<button class="btn seg${active?' on':''}" data-act="result-type" data-type="${esc(k)}">${esc(label)}${showN ? ' (' + n + ')' : ''}</button>`;
    }).join('');
    // v3.16.0：v3.9.0 合并结果页时遗落的入口补回工具栏。
    // 当前类型决定显示哪些工具：移动/繁简只在精确重复与同名分组下有意义，
    // 片头参数只在「相同片头」下有意义，HTML 报告与类型无关。
    const type = S.resultType || 'all';
    const canMove = type === 'exact' || type === 'name';
    // v3.17.1：补回合并时漏掉的另外三个入口。视频全量重算只在能看到视频分组的类型出现；
    // 老格式导出菜单只在后端报告/导出支持的模式里出现，避免点了被后端拒绝。
    const videoTools = (type === 'all' || type === 'video' || type === 'samevideo')
      ? `<button class="btn ghost" data-act="reanalyze-all" title="清空所有视频相似度指纹并重新分析，耗时较长；片头指纹请用「按当前 N 重新分析」">重新分析全部视频</button>` : '';
    const exportTools = REPORT_MODES.includes(type)
      ? `<span class="expmenu-wrap"><button class="btn ghost" data-act="export-menu" title="按格式与范围导出：DC5 兼容 / 通用可读，全部行或仅标记行（旁边「导出 CSV」导的是当前筛选快照）">导出 CSV ▾</button><div class="expmenu">${exportMenuHtml()}</div></span>` : '';
    const introTools = type === 'intro' ? `<span class="hint">片头长度</span>
        <input id="fIntroN" type="number" min="1" max="300" value="${Number(S.introN || 10)}" style="width:80px">
        <span class="hint">秒</span>
        <button class="btn ghost" data-act="reanalyze-intro" title="按上面填的秒数清空并重新分析所有视频的片头指纹，耗时较长">按当前 N 重新分析</button>
        <span class="badge">已分析 ${(S.introAnalyzed || 0).toLocaleString()}</span>
        <span class="badge warn">待分析 ${(S.introPending || 0).toLocaleString()}</span>
        <span class="hint">重新分析完成后会自动重新搜索</span>` : '';
    const selTools = canMove ? `<button class="btn" data-act="move-selected" title="把勾选的文件移动到指定目录（同卷，冲突自动改名，可撤销）">移动到…</button>
        <button class="btn" data-act="fanjian-menu" title="把勾选文件的文件名做繁简转换（OpenCC 词典，可撤销）">繁简转换</button>` : '';
    return `<div class="toolbar"><div class="tb-row" style="flex-wrap:wrap;gap:6px">${chips}</div></div>
      <div class="toolbar" id="facetsBarHost">${facetsInnerHtml()}</div>
      <div class="toolbar">
        <select data-act="result-sort" style="width:150px">
          <option value="waste"${S.sort==='waste'?' selected':''}>可节省空间</option>
          <option value="total_size"${S.sort==='total_size'?' selected':''}>组总大小</option>
          <option value="count"${S.sort==='count'?' selected':''}>文件数量</option>
          <option value="name"${S.sort==='name'?' selected':''}>名称</option>
          <option value="type"${S.sort==='type'?' selected':''}>类型</option>
        </select>
        <select data-act="result-order" style="width:90px">
          <option value="desc"${S.sortOrder!=='asc'?' selected':''}>降序</option>
          <option value="asc"${S.sortOrder==='asc'?' selected':''}>升序</option>
        </select>
        <button class="btn danger" data-act="trash-selected" title="把当前勾选的全部文件移入隔离区（先预览再确认）">把当前勾选移入隔离区</button>
        <span class="hint" id="selHint"></span>
        <div class="spacer"></div>
        ${rs.stale ? '<span class="badge warn">结果可能已过期</span>' : ''}
        <span class="hint">${(rs.totals && rs.totals.files || 0).toLocaleString()} 个文件 · 可释放 ${fmtB(rs.totals && rs.totals.waste || 0)}</span>
      </div>
      <details class="more-ops"${(selTools || introTools || videoTools) ? '' : ''}>
        <summary>更多操作</summary>
        <div class="more-ops-body">
          <button class="btn ghost" data-act="rule-search-again">重新搜索</button>
          <button class="btn ghost" data-act="rule-search-export" title="导出当前类型与关键字筛选后的快照（CSV）">导出 CSV</button>
          ${exportTools}
          <button class="btn ghost" data-act="report" title="生成当前结果的 HTML 报告（在服务端生成，可在浏览器新窗口打开）">HTML 报告</button>
          <button class="btn" data-act="gen-thumbs">${S.genThumbs?'收起缩略图':'生成缩略图'}</button>
          ${selTools}${introTools}${videoTools}
        </div>
      </details>`;
  }
}

// openAssistFilter v1.25.0：单一筛选入口——展开助手栏并定位到筛选器段。
function openAssistFilter(){
  S.assistOpen = true;
  S.listFilter.open = true;
  S.asecFold['筛选器'] = false;
  renderAssistant();
  const el = $('assist');
  if (el) {
    el.classList.remove('collapsed');
    const sec = el.querySelector('[data-sec-title="筛选器"]');
    if (sec) sec.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
  toast('筛选条件已并入选择助手「筛选器」，在此设置并即时生效', 'ok');
}

// ---------- 双图对比（v1.14.1 引入 10.6，纯前端）----------
// 组内勾选两张 → 左右并排，滚轮缩放 / 拖动平移双向同步（CSS transform）。
// 图片走 /api/raw 现有通道，不生成新尺寸；大图由浏览器自行解码。
function openCompare(pathA, pathB){
  const raw = p => '/api/raw?path=' + encodeURIComponent(p);
  const name = p => p.split('/').pop();
  const html = `<div class="cmpwrap">
    <div class="cmppane"><div class="cmpname" title="${esc(pathA)}">${esc(name(pathA))}</div>
      <div class="cmpview"><img class="cmpimg" src="${raw(pathA)}" draggable="false" alt=""></div></div>
    <div class="cmppane"><div class="cmpname" title="${esc(pathB)}">${esc(name(pathB))}</div>
      <div class="cmpview"><img class="cmpimg" src="${raw(pathB)}" draggable="false" alt=""></div></div>
  </div>
  <div class="hint">滚轮缩放 · 拖动平移 · 两侧同步；Shift+滚轮或双击复位</div>`;
  setModal('图片对比', html, null);
  bindCompareSync($('mBody'));
}

function bindCompareSync(root){
  if (!root) return;
  const state = { zoom: 1, tx: 0, ty: 0 };
  const imgs = Array.from(root.querySelectorAll('.cmpimg'));
  const apply = () => {
    imgs.forEach(img => {
      img.style.transformOrigin = 'center center';
      img.style.transform = `translate(${state.tx}px, ${state.ty}px) scale(${state.zoom})`;
    });
  };
  const reset = () => { state.zoom = 1; state.tx = 0; state.ty = 0; apply(); };
  const view = root.querySelector('.cmpview');
  root.querySelectorAll('.cmpview').forEach(v => {
    v.addEventListener('wheel', e => {
      e.preventDefault();
      if (e.shiftKey) { reset(); return; }
      const factor = e.deltaY < 0 ? 1.15 : 1 / 1.15;
      state.zoom = Math.min(20, Math.max(0.05, state.zoom * factor));
      apply();
    }, { passive: false });
    let drag = null;
    v.addEventListener('mousedown', e => { drag = { x: e.clientX, y: e.clientY, tx: state.tx, ty: state.ty }; e.preventDefault(); });
    window.addEventListener('mousemove', e => {
      if (!drag) return;
      state.tx = drag.tx + (e.clientX - drag.x);
      state.ty = drag.ty + (e.clientY - drag.y);
      apply();
    });
    window.addEventListener('mouseup', () => { drag = null; });
    v.addEventListener('dblclick', reset);
  });
  apply();
  void view;
}

// ---------- 选择助手 · 路径栏宽度自适应（v1.14.2 §4.1）----------
// .resultlayout 左栏默认 240px，长路径被截；按 aKeepPath 值长度放宽（420px 封顶），
// 清空回退 240px。宽度写成 CSS 变量 --assist-w，窄屏媒体查询直接改
// grid-template-columns 覆盖它——写死内联样式会压过媒体查询，手机上就会两列挤爆。
function applyAssistWidth(){
  if (S.assistWidthManual == null) {
    // v3.9.1：记忆值也限制在新范围内，避免旧版拖得太宽继续挤压主列表。
    try { const v = parseInt(localStorage.getItem('assistWidth') || '0', 10); if (v >= 200) S.assistWidthManual = Math.min(420, v); } catch(err) {}
  }
  const input = $('aKeepPath');
  const layout = document.querySelector('.resultlayout');
  if (!input || !layout) return;
  // v1.41：手动拖拽宽度优先（记忆），否则按输入内容自适应
  if (S.assistWidthManual) {
    layout.style.setProperty('--assist-w', S.assistWidthManual + 'px');
    return;
  }
  const w = Math.min(420, Math.max(240, Math.round((input.value || '').length * 7.5) + 28));
  layout.style.setProperty('--assist-w', (input.value ? w : 240) + 'px');
}

// ---------- 片头剪除（v1.16 引入 16.4-16.6）----------
// 快速模式流复制秒级；精确模式重编码帧级；边界探测二分（1 秒精度）按组缓存；
// 替换原文件默认关；原文件走隔离区可恢复；trim_log 全量记录。
function openTrimPanel(key){
  const g = (S.groups || []).find(x => x.key === key);
  if (!g) { toast('找不到分组数据', 'err'); return; }
  const files = g.files || [];
  if (files.length < 2) { toast('组内成员不足', 'err'); return; }
  S.trimPending = { key, files };
  const cfgN = Number(S.introN || 10);
  const rows = files.map(f => `<tr><td class="fname">${esc(f.name)}</td><td class="num">${fmtDur(f.duration)}</td></tr>`).join('');
  const html = `<div class="asec" data-sec-title="裁剪模式"><label class="rp-head" data-act="asec-fold" data-sec="裁剪模式" style="cursor:pointer"><span class="rp-arrow">▾</span>裁剪模式</label>
      <label class="checkline" style="font-size:var(--text-control)"><input type="radio" name="trimMode" value="fast" checked> 快速（流复制，秒级；关键帧间隔过长时自动改用精确模式，可能多等一会）</label>
      <label class="checkline" style="font-size:var(--text-control)"><input type="radio" name="trimMode" value="precise"> 精确（重编码，帧级对齐，耗时长）</label>
    </div>
    <div class="asec" data-sec-title="裁剪点（秒）"><label class="rp-head" data-act="asec-fold" data-sec="裁剪点（秒）" style="cursor:pointer"><span class="rp-arrow">▾</span>裁剪点（秒）</label>
      <div class="asrow"><input id="trimOffset" type="number" min="1" step="1" value="${cfgN}" style="width:90px">
      <button class="btn ghost" data-act="trim-probe">探测精确边界</button><span id="trimProbeHint" class="hint"></span></div>
      <div class="hint">预填配置值 ${cfgN}（下界，实际片头可能更长）；探测用二分查找，精度 1 秒，结果按组缓存</div>
    </div>
    <div class="asec" data-sec-title="输出"><label class="rp-head" data-act="asec-fold" data-sec="输出" style="cursor:pointer"><span class="rp-arrow">▾</span>输出</label>
      <label class="checkline" style="font-size:var(--text-control)"><input type="checkbox" id="trimReplace"> 替换原文件（默认关：生成 &lt;主名&gt;_trimmed 新文件；开启后原文件进隔离区可恢复）</label>
      <label class="checkline" style="font-size:var(--text-control)"><input type="checkbox" id="trimSkipDone" checked> 跳过已裁剪的文件</label>
    </div>
    <div class="hint">组内 ${files.length} 个文件：${rows ? '' : ''}</div>
    <div class="pm-actions"><button class="btn ghost" data-act="modal-cancel">取消</button>
    <button class="btn warn" data-act="trim-confirm">开始剪除</button></div>`;
  setModal('剪除片头（' + esc(g.name || key) + '）', html, null);
}

// ---------- 操作手册（v1.30.0）：内嵌章节，可折叠，? 深链接 ----------
const HELP_SECTIONS = [
  { id:'terms', title:'术语表（先看这页）', body:`
    <p>界面里只用下面这四个说法，看到同义词一律按这里理解：</p>
    <p><b>隔离区</b>：各卷根的 <code>.verify-trash</code>。移进来的文件还能恢复，不算删除。<br>
    <b>永久删除</b>：真正从磁盘删除，不可撤销；只在隔离区页提供，且要先勾选确认。<br>
    <b>剪除</b>：只针对视频，把片头/片尾那一段去掉，生成新文件或把原文件放进隔离区。<br>
    <b>整理</b>：批量移动、重命名、繁简转换、入转码队列这类「不删只改」的动作。</p>
    <p><b>可释放空间</b>：只统计精确重复（内容完全一致）能省下的空间。同名文件只是名字一样，内容不一定相同，单独列成「待判断」，不计入可释放。</p>
    <p>旧文档或截图里出现的「回收站 / 垃圾箱」都指隔离区；「清理」「去重」是泛称，具体看按钮文字。</p>` },
  { id:'permissions', title:'权限与隐私', body:`
    <p>应用包按飞牛开放平台的「最小权限」口径声明（见 developer.fnnas.com《应用权限》《应用资源》《Manifest》）：</p>
    <p><b>运行身份</b>：专用 <code>package</code> 用户，<b>不请求 root</b>；没有 Docker、共享目录、<code>/usr/local</code> 命令链接，也不注册系统服务。</p>
    <p><b>文件访问</b>：只读写你在飞牛「系统设置 → 应用 → 查重助手」里显式授权的目录。未授权目录在选择器里带锁；后端还会对每个路径做软链接解析与卷前缀校验，拒绝 <code>..</code> 越界。授权可随时收回，收回后对应目录立即不可扫描。</p>
    <p><b>图形设备（超出最小权限的唯一一项）</b>：为了 Intel 核显 QSV 硬解抽帧，声明了 <code>render</code> 附加组以访问 <code>/dev/dri/renderD128</code>。没有核显、设备不可用或初始化失败时自动回退 <code>-hwaccel none</code> 软解，查重不受影响。不需要硬解可从安装包 <code>config/privilege</code> 移除该组。</p>
    <p><b>网络</b>：只监听安装时选定的一个 HTTP 端口（默认 18898），不主动外联、不上传文件、无第三方统计。访问令牌可选；设置后登录页校验，Cookie 不存明文。入口默认仅管理员可见（<code>allUsers=false</code>），设置项只读。</p>
    <p><b>数据落点</b>：索引、日志、缓存与隔离区都在应用数据目录，卸载不删除；隔离区在各扫描卷根的 <code>.verify-trash</code>。</p>
    <p>完整说明与核对命令见仓库 <code>docs/权限说明.md</code>；核对硬件解码可看 <code>/api/status</code> 的 <code>hw_mode</code>（<code>qsv</code> 为启用，空为软解）。</p>` },
  { id:'flow', title:'工作方式', body:`
    <p>首次安装后可手动开始扫描；之后文件变化会被实时监控并增量更新索引，不用反复全盘重扫。</p>
    <p><b>精确重复</b>先对头尾各 4KB 做预哈希，再对候选做 BLAKE3 整文件校验，保证字节级一致；「快速查重」只跑这两步，跳过视频分析。</p>
    <p><b>相似视频</b>结合视频开头 5 秒音频指纹与三帧缩略图 dHash，适合识别转载、二次压缩的重复视频。</p>
    <p><b>相同片头</b>独立分析视频开头 N 秒（1–300 秒可调），只保留片头相同但正片不同的视频；整段重复的文件会被剔除。</p>
    <p>删除默认移入各卷的 <code>.verify-trash</code> 隔离区，可随时恢复；永久删除不可撤销。</p>` },
  { id:'basis', title:'查重依据与结果页', body:`
    <p><b>精确内容</b>：按字节比较（BLAKE3-256 哈希），识别完全相同的文件。</p>
    <p><b>同名文件</b>：只按文件名归组，适合整理不同目录的同名副本。</p>
    <p><b>相似视频</b>：音频指纹 + 画面缩略图，识别转载、二次压缩的视频；「忽略片头/片尾」可避开片头差异。</p>
    <p><b>相同片头</b>：只比较视频开头 N 秒，找共用片头但正片不同的视频。</p>
    <p><b>结果页通用</b>：顶部统计条显示组数/文件数/总大小/可释放空间；可按可节省空间、组总大小、文件数量、名称、类型排序（后端全局排序，翻页一致）；点组标题折叠/展开；勾选后可用顶部操作（移入隔离区 / 移动到… / 繁简转换 / 入转码队列等），组内还可「统一命名」。</p>` },
  { id:'assist', title:'选择助手与筛选器', body:`
    <p>左侧「选择助手」是批量标记工具：<b>组级标记</b>（每组保留一个/只标记一个）、<b>偏好标记</b>（按大小/时间等偏好挑保留者）、<b>按路径标记</b>（优先保留指定目录）；窄屏时列表在前、助手在后。</p>
    <p><b>筛选器</b>：按标记状态（已标记/未标记/部分勾选/已保护）与范围（组数/组大小/单文件大小/文件大小/扩展名/关键词/修改时间）收窄列表；筛选后操作只作用于筛选结果，支持「标记→收窄→再标记」多轮工作流。</p>
    <p>各段标题可点击独立折叠；「清除全部筛选」一键清空。列表与助手操作同源——看到的就是能操作的。</p>` },
  { id:'trash', title:'隔离区与保护文件夹', body:`
    <p><b>隔离区</b>：删除默认移入各卷根的 <code>.verify-trash</code>，随时可恢复；「永久删除」不可撤销，操作前有红字确认。</p>
    <p><b>保护文件夹</b>（全局）：里面的文件照常扫描展示，但禁止移入隔离区、禁止永久删除；隔离区自身永远受保护。可在设置页或新建查重的穿梭框里维护。</p>` },
  { id:'health', title:'文件健康检测', body:`
    <p>七类检测：<b>空文件</b>（零字节）、<b>空目录</b>（清理移入隔离区可恢复）、<b>扩展名不符</b>（按文件头判断真实类型，可一键修正）、<b>损坏文件</b>（视频/音频/图片/zip/JSON/XML 校验，只报告不推荐删除）、<b>文件名不规范</b>（emoji/多余空格/重复标点）、<b>繁体文件名</b>（OpenCC 判定，一键转简体）、<b>临时文件</b>（.tmp/.bak/.part 等可编辑清单，勾选清理进隔离区可恢复）。</p>
    <p>检测有探测缓存与断点续跑；磁盘忙时自动让路。繁体文件名与临时文件名单在设置页「查重依据」里维护，其余类型点「开始检测」手动触发。</p>` },
  { id:'transcode', title:'视频转码与片头剪除', body:`
    <p><b>视频转码</b>：结果页勾选 →「入转码队列」→ 转码页统一发起；编码器自动探测（核显 VAAPI/QSV → 软编回退）。转码会打散旧的重复关系（预期）；原文件进隔离区可恢复；音轨全转 AAC 128k。</p>
    <p><b>片头剪除</b>：相同片头组内一键剪除——快速模式（流复制秒级）或精确模式（重编码帧级）；快速模式遇到关键帧间隔过长、剪不动的文件会自动改用精确模式重做一次；边界探测自动推测片头长度；替换原文件默认关（生成 _trimmed 新文件），开启后原文件进隔离区。</p>` },
  { id:'io', title:'导入导出（DC5 兼容）', body:`
    <p><b>导出 CSV</b>：支持 DC5 兼容格式（Duplicate Cleaner 5 的文件清单格式，含标记状态）与通用可读格式；路径映射（设置页「导出时的路径换算」）可把 NAS 路径换成 SMB/盘符路径，供 Windows 端 Duplicate Cleaner 定位文件。</p>
    <p><b>导入 CSV</b>：导入 DC5 导出的清单，恢复分组与标记（独立会话，不影响扫描库）。</p>` },
  { id:'perf', title:'扫描与性能怎么调', body:`
    <p><b>最小文件大小</b>：比这个数小的文件直接跳过。想找重复的小图、小图标就填 0。</p>
    <p><b>同时扫描几个文件</b>：机械硬盘一般 4，SSD 可以到 8。数字太大反而更慢，因为硬盘要来回找位置。</p>
    <p><b>每部视频抽几帧</b>：抽得越多，越容易发现「换过音轨」「中间剪过」的重复，但第一次分析更慢。改完不用重扫，新旧结果能直接一起比。</p>
    <p><b>同时分析几个视频</b>：用核显加速建议 1–2；纯 CPU 可以调高，但别把机器占满。</p>
    <p><b>跳过片头 / 片尾</b>：转载视频常常顶着一模一样的片头。填上秒数，比较时跳过这一段，只看正片。</p>
    <p><b>片头只比前几秒</b>：只比开头这一小段。改完到结果页切到「相同片头」类型，点「按当前 N 重新分析」就行，不用整库重扫。</p>
    <p><b>视频工具路径</b>：飞牛一般自带 ffmpeg / ffprobe，留空会自动找；找不到时页面顶部有提示。</p>` },
  { id:'schedule', title:'什么时候自己干活（后台任务与硬盘休眠）', body:`
    <p>这一组决定软件什么时候自己动手，直接关系到硬盘会不会被反复叫醒。</p>
    <p><b>硬盘休眠友好（默认开）</b>：开着就不做任何主动扫描，只跟着系统的文件变动通知走。代价是：文件改动的更新依赖实时监控，监控没跑起来时会滞后。</p>
    <p><b>定时全量重扫</b>：每隔几小时把整个库重扫一遍。休眠友好开着时这项不生效，避免把睡着的硬盘叫醒；0 表示关闭。</p>
    <p><b>监控失效时定时看一眼</b>：系统能同时盯的文件数有上限，超了就不再实时通知。这里填分钟数，超限后改成定时检查一遍；0 表示不检查。</p>
    <p><b>静默时段</b>：比如 23:00-07:00，这段时间后台任务一律不启动。你手动点的扫描不受影响。</p>
    <p><b>没人用时自动检查损坏文件</b>：默认关。只在你完全不打开页面的情况下才跑，一批一批地查，查完就停。</p>
    <p><b>每天定时唤醒硬盘补一次</b>：默认关，<b>开了会叫醒硬盘</b>。适合「不想干等扫描、又希望打开就能勾选」的场景：每天在指定时段补一次增量，时段一过让硬盘继续睡。</p>` },
  { id:'paths', title:'扫描范围、授权与保护', body:`
    <p><b>目录树里的锁是什么意思？</b>飞牛只允许应用读被授权过的目录。先去「飞牛系统设置 → 应用 → 查重助手」把要查的文件夹授权，再回到这里点「选择文件夹」。没授权的目录会显示锁，点不进去。</p>
    <p><b>扫描目录</b>：只扫这里列出的文件夹，子文件夹自动包含，可以加多个。</p>
    <p><b>不扫描的文件夹</b>：整个目录连同子目录都不看，结果里也不会出现。适合放私有目录、缓存目录。</p>
    <p><b>受保护的文件夹</b>：照样扫描、照样显示，但删不掉——「移入隔离区」和「永久删除」都会被拦住。隔离区本身永远受保护。</p>
    <p><b>按名字排除的文件夹</b>：只写名字不管层级，比如 @eaDir、#recycle，任何位置出现都会跳过。</p>
    <p><b>导出时的路径换算</b>：只对 DC5 格式导出有效，写法是「NAS 路径 = Windows 能看到的路径」。配好之后，Windows 上的 Duplicate Cleaner 才能打开文件、显示缩略图。</p>
    <p><b>访问令牌</b>：填了就要先输密码才能进。局域网里自己用，留空最省事。</p>` },
  { id:'cache', title:'缓存与内存', body:`
    <p><b>缩略图可用内存</b>：给缩略图留多少内存。0 表示按机器内存自动算：8G 给 1G、16G 给 2G、32G 给 4G、64G 给 8G。超了自动淘汰最旧的，不会把内存撑爆。</p>
    <p><b>缩略图占用磁盘上限</b>：缩略图在磁盘上最多占多少，0 表示不限制。超了删最旧的，下次看到时再生成。</p>
    <p><b>缓存管理</b>（设置页最下面）：看清每类缓存占了多少，可以单独清。清掉不影响查重结果，只是下次要重新生成缩略图。</p>
    <p><b>存放位置</b>：日志和缩略图默认放在数据目录下的 logs / cache。换位置后旧文件不会自动搬过去。</p>` },
  { id:'faq', title:'常见问题', body:`
    <p><b>目录选择器里目录带 🔒？</b>fnOS 应用授权只放行被授权目录子树。去飞牛系统设置给本应用授权该目录；或在输入框直接粘贴已授权目录的完整路径点「转到」。</p>
    <p><b>转码用的什么编码器？</b>自动探测：核显 VAAPI（老世代）→ QSV（新世代）→ 软编 libx265。老世代 iGPU（如 E3 至强）走 VAAPI。</p>
    <p><b>扫描会唤醒休眠硬盘吗？</b>默认「硬盘休眠友好」开启：绝不主动扫描，只靠内核事件跟踪；定时重扫被禁用。</p>
    <p><b>误删了怎么办？</b>删除默认进隔离区（各卷根 .verify-trash），隔离区页可一键恢复；「永久删除」才不可逆。</p>` },
];

let helpFold = {};

function renderHelp(){
  const v = $('view');
  const anchor = S.helpAnchor || '';
  S.helpAnchor = null;
  const secs = HELP_SECTIONS.map((s, i) => {
    const folded = helpFold[s.id] === true && s.id !== anchor;
    return `<div class="section help-sec" id="help-${s.id}">
      <h2 class="section-title rp-head" data-act="help-fold" data-id="${s.id}">
        <span class="rp-arrow">${folded ? '▸' : '▾'}</span><span class="help-no">${i + 1}</span>${esc(s.title)}
        <span class="hint" style="margin-left:8px">${folded ? '点击展开' : ''}</span>
      </h2>
      ${folded ? '' : `<div class="help-body">${s.body}</div>`}
    </div>`;
  }).join('');
  // v3.10.0：章节导航做成可点的胶囊，宽屏两栏并排显示，手机端横向滑动。
  const chips = HELP_SECTIONS.map((s, i) => `<button class="help-chip" data-act="help-jump" data-id="${s.id}">${i + 1}. ${esc(s.title)}</button>`).join('');
  v.innerHTML = `<div class="help-hero">
    <b class="help-hero-title">操作手册</b>
    <p class="hint">每章对应一个功能模块；点下面的章节直达，标题可折叠。各页面右上角的 <b>?</b> 也会跳到对应章节。</p>
    <div class="help-chips">${chips}</div>
  </div><div class="helpgrid">${secs}</div>`;
  if (anchor) {
    const el = document.getElementById('help-' + anchor);
    if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
}

function helpLink(sec){
  S.helpAnchor = sec;
  helpFold[sec] = false;
  switchTab('help');
}

// ---------- 视频转码（v1.17 引入 17.4-17.5）----------
// ---------- 路径穿梭框（v1.28.0，设计文档 §9 迁入；trimacl 适配：locked 节点不可展开）----------
const T = {
  provider: null, root: null, expanded: new Set(), nodeMap: new Map(),
  leftSel: null, selected: [], rightSel: new Set(), loading: new Set(), target: 'scanDirs',
};

async function fsListProvider(path){
  const url = '/api/fs/list?path=' + encodeURIComponent(path || '');
  const res = await fetch(url);
  if (!res.ok){
    const e = await res.json().catch(() => ({}));
    throw new Error(e.error || ('加载失败(' + res.status + ')'));
  }
  return res.json();
}

function listingToNodes(listing){
  return (listing.entries || []).map(e => ({
    id: normPath(e.path), name: e.name, path: normPath(e.path),
    loaded: false, hasChildren: !e.locked, children: e.locked ? [] : null,
    locked: !!e.locked, reason: e.reason || '',
  }));
}

function tfIsPicked(path){ const p = normPath(path); return T.selected.some(s => s.path === p); }

async function tfLoadChildren(node){
  if (node.loaded || node.locked || T.loading.has(node.id)) return;
  if (node.path) T.loading.add(node.id);
  tfRenderTree();
  try {
    const listing = await T.provider(node.path);
    node.children = listingToNodes(listing);
    node.hasChildren = node.children.length > 0;
    node.children.forEach(c => T.nodeMap.set(c.id, c));
  } catch (e) {
    toast('加载目录失败：' + e.message, 'err');
    node.children = []; node.hasChildren = false;
  } finally {
    node.loaded = true;
    T.loading.delete(node.id);
    tfRenderTree();
  }
}

function tfRenderTree(){
  const box = $('tfTreeBox'); if (!box) return;
  const rows = [];
  const walk = (node, depth) => {
    if (node.isRoot){
      (node.children||[]).forEach(c => walk(c, 0));
      return;
    }
    const sel = T.leftSel === node.id ? ' sel' : '';
    const pickedItem = T.selected.find(s => s.path === normPath(node.path));
    const picked = pickedItem ? ' picked' : '';
    const isExcl = pickedItem && pickedItem.exclude;
    const expanded = T.expanded.has(node.id);
    const spinning = T.loading.has(node.id);
    const caret = node.locked ? `<span class="tf-caret">🔒</span>`
      : (node.hasChildren || !node.loaded
        ? `<span class="tf-caret" data-act="tf-expand" data-id="${esc(node.id)}">${spinning?'⋯':(expanded?'▾':'▸')}</span>`
        : `<span class="tf-caret empty"></span>`);
    const badge = isExcl ? `<span class="badge warn">已排除</span>` : (pickedItem ? `<span class="badge good">已选</span>` : '');
    rows.push(`<div class="tf-row${sel}${picked}${isExcl?' excl':''}" data-act="tf-row" data-id="${esc(node.id)}" data-path="${esc(node.path)}" style="padding-left:${8+depth*16}px">
      ${caret}<span class="tf-ico">${tfFolderSvg}</span><span class="tf-name" title="${esc(node.path)}">${esc(node.name)}</span>${badge}
    </div>`);
    if (expanded && node.children) node.children.forEach(c => walk(c, depth+1));
  };
  walk(T.root, 0);
  box.innerHTML = rows.length ? rows.join('') : `<div class="tf-empty">暂无目录</div>`;
}

function tfRenderList(){
  const box = $('tfListBox'); if (!box) return;
  if (!T.selected.length){ box.innerHTML = `<div class="tf-empty">尚未选择任何路径</div>`; tfUpdateButtons(); return; }
  box.innerHTML = T.selected.map(s => `
    <div class="tf-list-row${T.rightSel.has(s.id)?' sel':''}${s.exclude?' excl':''}" data-act="tf-rrow" data-id="${esc(s.id)}">
      <span class="tf-list-path" title="${esc(s.path)}">${esc(s.path)}</span>
      <button class="tf-chip${s.exclude?' on':''}" data-act="tf-excl" data-id="${esc(s.id)}" title="排除出扫描">排除</button>
      <button class="tf-chip prot${s.protect?' on':''}" data-act="tf-prot" data-id="${esc(s.id)}" title="加入保护文件夹（全局生效）">保护</button>
      <button class="tf-x" data-act="tf-rx" data-id="${esc(s.id)}" title="移除">×</button>
    </div>`).join('');
  tfUpdateButtons();
}

function tfUpdateButtons(){
  const add = $('tfAddBtn'), rem = $('tfRemBtn');
  if (add) add.disabled = !T.leftSel;
  if (rem) rem.disabled = T.rightSel.size === 0;
}

function tfAdd(node){
  if (!node) return;
  const p = normPath(node.path);
  if (node.locked) { toast(node.reason || '该目录无访问权限——请先在飞牛系统设置中授权', 'err'); return; }
  if (tfIsPicked(p)){ toast('该路径已在已选列表中', 'warn'); return; }
  T.selected.push({ id:node.id, path:p, name:node.name, exclude:false, protect:false });
  T.leftSel = null;
  tfRenderTree(); tfRenderList(); tfUpdateButtons();
}

// tfAddManual v3.5.3：把粘贴的已授权路径作为顶层节点插入目录树。
// 与设置页目录选择器的“粘贴后转到”共用同一个 /api/fs/list 授权探测逻辑，
// 直达飞牛只授权子目录、自动发现看不到的场景。
async function tfAddManual(path){
  // v3.7.2：格式校验先行，权限由 provider（/api/fs/list）做真实授权探测（P-20）
  const raw = String(path || '').trim();
  const bad = fsPathError(raw);
  if (bad) { toast(bad, 'err'); return; }
  const p = normPath(raw);
  if (T.nodeMap.has(p)) {
    T.expanded.add(p);
    T.leftSel = p;
    tfRenderTree(); tfUpdateButtons();
    toast('已定位到该目录，点中间 › 加入已选', 'ok');
    return;
  }
  try {
    const listing = await T.provider(p);
    const node = {
      id: p, name: p.split('/').pop() || p, path: p, loaded: true,
      children: listingToNodes(listing), hasChildren: (listing.entries || []).length > 0
    };
    node.children.forEach(c => T.nodeMap.set(c.id, c));
    T.nodeMap.set(p, node);
    if (!T.root.children) T.root.children = [];
    T.root.children.unshift(node);
    T.expanded.add(p);
    T.leftSel = p;
    tfRenderTree(); tfUpdateButtons();
    toast('已定位到该目录，点中间 › 加入已选', 'ok');
  } catch (e) {
    toast('该路径不可用：' + (e.message || '未在飞牛系统设置中授权'), 'err');
  }
}

function tfManualGo(){
  const inp = $('tfManual');
  tfAddManual(inp ? inp.value : '');
}

function tfRemove(ids){
  if (!ids || !ids.length) return;
  const set = new Set(ids);
  T.selected = T.selected.filter(s => !set.has(s.id));
  T.rightSel = new Set([...T.rightSel].filter(id => !set.has(id)));
  tfRenderList(); tfRenderTree(); tfUpdateButtons();
}

function tfOnClick(e){
  const el = e.target.closest('[data-act]'); if (!el) return;
  const act = el.dataset.act, id = el.dataset.id;
  if (act === 'tf-expand'){
    const node = T.nodeMap.get(id) || (T.root.id===id?T.root:null);
    if (!node) return;
    if (T.expanded.has(id)){ T.expanded.delete(id); }
    else { T.expanded.add(id); if (node.children===null) tfLoadChildren(node); }
    tfRenderTree();
  } else if (act === 'tf-row'){
    // v1.41.8：单击行任意区域即选中；有子目录时延迟切换展开（给双击留判定窗口）
    T.leftSel = id; tfRenderTree(); tfUpdateButtons();
    const node = T.nodeMap.get(id);
    if (T.clickTimer) clearTimeout(T.clickTimer);
    T.clickTimer = setTimeout(() => {
      T.clickTimer = null;
      const n = T.nodeMap.get(id);
      if (!n || n.locked) return;
      if (T.expanded.has(id)) { T.expanded.delete(id); }
      else {
        T.expanded.add(id);
        if (n.children === null) tfLoadChildren(n);
      }
      tfRenderTree();
    }, 220);
  } else if (act === 'tf-rrow'){
    if (T.rightSel.has(id)) T.rightSel.delete(id); else T.rightSel.add(id);
    tfRenderList(); tfUpdateButtons();
  } else if (act === 'tf-rx'){
    tfRemove([id]);
  } else if (act === 'tf-excl'){
    const it = T.selected.find(s => s.id === id); if (it){ it.exclude = !it.exclude; tfRenderList(); tfRenderTree(); }
  } else if (act === 'tf-prot'){
    const it = T.selected.find(s => s.id === id); if (it){ it.protect = !it.protect; tfRenderList(); }
  } else if (act === 'tf-add'){
    const node = T.leftSel && (T.nodeMap.get(T.leftSel) || (T.root.id===T.leftSel?T.root:null));
    tfAdd(node);
  } else if (act === 'tf-rem'){
    tfRemove([...T.rightSel]);
  } else if (act === 'tf-manual-go'){
    tfManualGo();
  }
}

function tfOnDblClick(e){
  const el = e.target.closest('[data-act]'); if (!el) return;
  const act = el.dataset.act, id = el.dataset.id;
  // v1.41.8：双击行 = 左右转移；取消单击的展开定时器，避免"展开又收起"
  if (act === 'tf-row'){
    if (T.clickTimer) { clearTimeout(T.clickTimer); T.clickTimer = null; }
    const node = T.nodeMap.get(id) || (T.root.id===id?T.root:null);
    tfAdd(node);
  } else if (act === 'tf-rrow'){
    if (T.clickTimer) { clearTimeout(T.clickTimer); T.clickTimer = null; }
    tfRemove([id]);
  }
}

function tfShellHtml(){
  // v3.5.3：与设置页目录选择器一致，支持粘贴已授权路径直接定位
  // （飞牛只授权共享下子目录时，自动发现可能看不到，粘贴是可靠入口）
  return `<div class="dirbar" style="margin-bottom:8px">
      <input id="tfManual" placeholder="粘贴已授权路径，如 /vol3/1000/共享目录/子目录" style="flex:1;min-width:0">
      <button class="btn ghost" data-act="tf-manual-go">转到</button>
    </div>
    <div class="transfer">
    <div class="tf-pane"><h4>目录树（只显示应用内已添加/浏览过的目录；去飞牛系统设置授权后可粘贴路径进入）</h4><div class="tf-box" id="tfTreeBox"></div></div>
    <div class="tf-mid">
      <button class="tf-btn" id="tfAddBtn" data-act="tf-add" title="添加到已选" disabled>›</button>
      <button class="tf-btn" id="tfRemBtn" data-act="tf-rem" title="从已选移除" disabled>‹</button>
    </div>
    <div class="tf-pane"><h4>已选路径（可标记 排除/保护）</h4><div class="tf-box" id="tfListBox"></div></div>
  </div>`;
}

const tfFolderSvg = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>';

// tfInit：渲染到 container 并挂事件（委托）
// T.clickTimer 单击延迟展开定时器（双击转移时取消，v1.41.8）
async function tfInit(container, opts){
  T.provider = (opts && opts.provider) || fsListProvider;
  T.target = (opts && opts.target) || 'scanDirs';
  T.selected = ((S.wizard && S.wizard[T.target]) || []).map(p => {
    const np = normPath(p); return { id:np, path:np, name: p.split('/').pop()||p, exclude:false, protect:false };
  });
  // 排除目录回显为「已排除」开关项
  ((S.wizard && S.wizard.excludeDirs) || []).forEach(p => {
    const np = normPath(p);
    if (!T.selected.some(s => s.path === np)) {
      T.selected.push({ id:np, path:np, name: p.split('/').pop()||p, exclude:true, protect:false });
    }
  });
  T.root = { id:'__root__', name:'根', path:'', loaded:false, hasChildren:true, children:null, isRoot:true };
  T.expanded = new Set(); T.nodeMap = new Map(); T.leftSel = null; T.rightSel = new Set(); T.loading = new Set();
  container.innerHTML = tfShellHtml();
  container.addEventListener('click', tfOnClick);
  container.addEventListener('dblclick', tfOnDblClick);
  // 粘贴路径后回车直接转到，与设置页目录弹窗一致
  container.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && e.target && e.target.id === 'tfManual') {
      e.preventDefault();
      tfManualGo();
    }
  });
  await tfLoadChildren(T.root);
  tfRenderTree(); tfRenderList(); tfUpdateButtons();
  if (!(T.root.children || []).length) {
    toast('目录树为空：先在飞牛系统设置授权，再到 设置 → 扫描范围 添加路径', 'err');
  }
}

// tfWriteBack：按开关拆分写回 wizard（扫描/排除/保护三类路径）
function tfWriteBack(){
  if (!S.wizard) return;
  const scan = [], excl = [], prot = [];
  for (const s of T.selected){
    if (s.exclude) excl.push(s.path); else scan.push(s.path);
    if (s.protect) prot.push(s.path);
  }
  S.wizard[T.target] = scan;
  S.wizard.excludeDirs = excl;
  S.wizard.protectPaths = prot;
}

function openPreview(path, name){
  $('mTitle').textContent = name || path.split('/').pop();
  const body = $('mBody');
  const ext = nameExt(path);
  const raw = '/api/raw?path=' + encodeURIComponent(path);
  let html = '';
  if (IMG_EXT.includes(ext)) html = `<img src="${raw}" alt="">`;
  else if (VID_EXT.includes(ext)) html = `<video controls autoplay muted playsinline preload="metadata" poster="/api/thumb?path=${encodeURIComponent(path)}&t=1"><source src="${raw}"></video>`;
  else html = '<div class="empty" style="width:100%">该类型不支持预览</div>';
  setModal(name || path.split('/').pop(), html + `<div class="meta">${esc(path)}</div>`, null);
}

// closeModal v3.7.2：关闭弹窗时统一清理事务态（P-21）。
// 之前只清 DOM 与 S.modalAction，S.movePending / trimPending / trimJobs 等
// 会残留到下一次弹窗；执行中的弹窗则禁止关闭——请求无法中途取消，
// 提前关掉只会让完成回调再次弹窗、打断用户正在做的事。
function closeModal(){
  if (S.modalBusy) { toast('操作正在执行，请等待完成', 'warn'); return; }
  $('modal').classList.remove('show');
  $('mBody').innerHTML = '';
  S.modalAction = null;
  S.movePending = null;
  S.groupRenamePending = null;
  S.relinkJobs = null;
  S.trimPending = null;
  S.trimJobs = null;
  S.trimBoundary = null;
}

function openDirPicker(mode, start, initial, target){
  const t = target || mode;
  S.dir = { mode: mode, target: t, path: '', selected: new Set(initial || []), listing: null };
  const titles = { log: '选择日志目录', cache: '选择缩略图缓存目录', scan: '选择扫描目录', wizardScan: '选择扫描文件夹', wizardExclude: '选择排除文件夹', excludeDir: '选择不扫描的文件夹', protectDir: '选择受保护的文件夹', move: '选择目标目录（进入后点确定）', movedir: '选择目标位置（目录将移到这下面）', healthmove: '选择目标目录（健康页选中文件移到这里）', searchdir: '选择搜索目录（仅限扫描范围内）' };
  $('dirTitle').textContent = titles[t] || '选择目录';
  // v3.5.3：每次打开清空上一次粘贴的路径，避免误用旧值
  const manual = $('dirManual');
  if (manual) manual.value = '';
  $('dirModal').classList.add('show');
  loadDir(start || '');
}

function closeDirPicker(){ $('dirModal').classList.remove('show'); S.dir = null; }

async function loadDir(path){
  const d = S.dir; if (!d) return;
  try {
    const r = /** @type {FsListResp} */ (await api('/api/fs/list?path=' + encodeURIComponent(path || '')));
    d.path = r.path || '';
    d.listing = r;
    renderDirPicker();
  } catch(e) { toast(e.message, 'err'); }
}

function renderDirPicker(){
  const d = S.dir; if (!d || !d.listing) return;
  const l = d.listing;
  const multi = d.mode === 'scan' || d.mode === 'excludeDir' || d.mode === 'protectDir' || d.target === 'wizardScan' || d.target === 'wizardExclude';
  $('dirPath').textContent = d.path || '（请选择入口）';
  // v1.41.2：禁止粘贴——入口只来自应用内已配置且已授权的目录
  $('dirRoots').innerHTML = (l.entries || []).length ? '' :
    '<span class="hint">没有可用的授权目录：先在飞牛系统设置授权，再到 设置 → 扫描范围 添加路径</span>';
  // v1.24.1：trimacl 授权只放行被授权子树——不可读目录显示锁标，可读才可进入；
  // path 为空时是入口视图（卷根 + 快捷入口），locked 项点「转到」按钮提示。
  // v3.9.6 修 G-21 P1：rows_pre 先声明再赋值，消除 TDZ（之前 if 在 let 之前，隐藏锁定目录出现时 ReferenceError 崩溃）
  let rows_pre = '';
  if (l.hidden_locked > 0) {
    rows_pre = `<div class="diritem" style="opacity:.55"><span class="dname">🔒 另有 ${l.hidden_locked} 个已配置目录当前不可访问（未授权或位于 WebDAV/云盘挂载），已隐藏</span></div>`;
  }
  const rows = (l.entries || []).map(e => {
    const locked = e.locked;
    const click = locked ? '' : ` data-act="dir-enter" data-path="${esc(e.path)}" style="cursor:pointer"`;
    const chk = (multi && !locked) ? `<input type="checkbox" data-act="dir-check" data-path="${esc(e.path)}" ${d.selected.has(e.path) ? 'checked' : ''}>` : '';
    const icon = locked ? '🔒' : '📁';
    const reason = locked ? `<span class="hint">${esc(e.reason || '无访问权限')}</span>` : '';
    // v3.5.3：入口显示“最下级文件夹名”，完整路径放 title 悬停查看；
    // 自动发现或粘贴进来的深层授权目录不再显示整串 /volN/...
    return `<div class="diritem"${click}>
      ${chk}<span class="dname" title="${esc(e.path)}">${icon} ${esc(e.name)}</span>${reason}</div>`;
  }).join('');
  $('dirList').innerHTML = (rows_pre || '') + rows || '<div class="empty" style="padding:18px">此目录下没有子文件夹</div>';
  $('dirHint').textContent = l.locked
    ? (l.reason || '该目录无访问权限')
    : (!d.path ? '目录树只显示已授权的目录（从 设置 → 扫描范围 添加）。未授权或位于 WebDAV/云盘的路径不会出现。'
      : multi ? '勾选文件夹，或点“添加当前目录”，可跨层级选择。' : '点文件夹进入下一级，点“选择此目录”使用当前路径。');
  $('dirSel').innerHTML = multi ? Array.from(d.selected).map(p =>
    `<span class="chip">${esc(p)}<button data-act="dir-unselect" data-path="${esc(p)}" title="移除">×</button></span>`).join('') : '';
  const ok = $('dirModal').querySelector('[data-act="dir-ok"]');
  if (ok) ok.textContent = multi ? '确定' : '选择此目录';
  const add = $('dirModal').querySelector('[data-act="dir-add-current"]');
  if (add) add.style.display = multi ? '' : 'none';
  const up = $('dirModal').querySelector('[data-act="dir-up"]');
  if (up) {
    // 入口视图没有上一级；进入任意目录后即使父目录未授权，也允许“返回入口”。
    up.disabled = !l.path && !l.parent;
    up.textContent = (l.path && !l.parent) ? '返回入口' : '上一级';
  }
}

// v3.9.6 修 G-20：加顶层 try/catch，子调用抛错不冒泡成未处理 reject
async function finishDirPicker(){
  try {
  const d = S.dir; if (!d) return;
  if (d.mode === 'move') {
    if (!d.path) { toast('请先进入目标目录再点确定', 'err'); return; }
    closeDirPicker();
    const target = d.path;
    const paths = S.movePaths || [];
    S.movePaths = null;
    await moveExecute(target, paths);
    return;
  }
  if (d.mode === 'healthmove') {
    if (!d.path) { toast('请先进入目标目录再点确定', 'err'); return; }
    closeDirPicker();
    const target = d.path;
    const paths = S.healthMovePaths || [];
    S.healthMovePaths = null;
    await healthMoveExecute(target, paths);
    return;
  }
  if (d.mode === 'movedir') {
    // v3.14.0：目录移动——先选目标位置，再拼出「目标位置/源目录名」做预检。
    if (!d.path) { toast('请先进入目标位置再点确定', 'err'); return; }
    closeDirPicker();
    await moveDirTargetChosen(d.path);
    return;
  }
  // v3.9.10：文件搜索的目录限定只改搜索条件，不写全局设置
  if (d.mode === 'searchdir') {
    if (!d.path) { toast('请先进入目标目录再点确定', 'err'); return; }
    const st = searchState();
    st.dir = d.path;
    closeDirPicker();
    if ($('sDir')) $('sDir').value = d.path;
    doSearch();
    return;
  }
  if (d.mode === 'folder_rename') {
    if (!d.path) { toast('请先进入要整理的目录再点确定', 'err'); return; }
    S.frDir = d.path; S.frPrefixA = false; S.frTask = 0;
    closeDirPicker();
    await frPreview();
    return;
  }
  // v3.10.0：不扫描/受保护文件夹只写回设置页的文本框，保存仍由「保存设置」统一做，
  // 这样用户能先看清最终清单再决定是否生效。
  if (d.mode === 'excludeDir' || d.mode === 'protectDir') {
    const paths = Array.from(d.selected);
    const box = $(d.mode === 'excludeDir' ? 'sExclPaths' : 'sProtect');
    if (box) box.value = paths.join('\n');
    closeDirPicker();
    toast('已更新文件夹列表，点「保存设置」生效', 'ok');
    return;
  }
  if (d.mode === 'scan') {
    if (!d.selected.size) { toast('请先勾选文件夹，或点“添加当前目录”', 'err'); return; }
    const paths = Array.from(d.selected);
    if (d.target === 'wizardScan') {
      S.wizard.scanDirs = paths;
      closeDirPicker();
      renderWizard();
      toast('已更新扫描文件夹', 'ok');
      return;
    }
    if (d.target === 'wizardExclude') {
      S.wizard.excludeDirs = paths;
      closeDirPicker();
      renderWizard();
      toast('已更新排除文件夹', 'ok');
      return;
    }
    addScanPaths(paths);
    closeDirPicker();
    toast('已添加扫描目录，点击“保存设置”生效', 'ok');
    return;
  }
  const path = d.path;
  closeDirPicker();
  if (path) await applyDirSetting(d.mode, path);
  } catch(e) { toast('操作失败：' + (e && e.message || e), 'err'); }
}
