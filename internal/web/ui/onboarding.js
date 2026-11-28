// v4.5.0：首次使用引导。三步把「选范围 → 扫描 → 看结果并隔离」串起来，
// 只讲主路径，高级选项留给向导里的「高级模式」。
// 状态存 localStorage；设置页可随时重新打开（重新打开不算首次，不会改标记）。
const ONBOARD_KEY = 'verify.onboarded';
let onboardStep = 0;

const ONBOARD_STEPS = [
  {
    title: '先选要扫描的文件夹',
    body: () => `
      <p>查重只扫你授权的目录，不会碰其它盘。第一次用建议先选一两个卷试水，确认结果后再扩范围。</p>
      <ul class="onboard-list">
        <li><b>先授权</b>：飞牛「系统设置 → 应用 → 查重助手」里把要查的文件夹授权给本应用；没授权的目录在选择器里会带锁，扫不进去。</li>
        <li><b>添加扫描范围</b>：回到本应用「设置 → 路径与安全 → 扫描目录」，粘贴已授权的路径，或点「选择文件夹」勾选。</li>
        <li><b>排除目录</b>：备份盘、回收站、系统目录这类不必查的，加进排除清单。</li>
        <li><b>保护目录</b>：绝对不想被误删的（如家庭照片原盘），标成「保护」，隔离和删除都会跳过。</li>
      </ul>
      <p class="hint">没授权就添加会提示「不可访问」。授权后如果列表还没刷新，重新打开一次设置页即可。</p>`,
    primary: ['去设置扫描范围', () => switchTab('settings')]
  },
  {
    title: '开始一次查重',
    body: () => `
      <p>点右上角「新建查重」。第一步默认显示上次的设置；如果不是<b>精确重复 + 同名文件</b>，点「用推荐组合」即可一键切到最常用的组合，再选文件夹开始。</p>
      <ul class="onboard-list">
        <li><b>精确重复</b>：内容逐字节一致，可以放心处理，删除先进隔离区。</li>
        <li><b>同名文件</b>：只是名字一样，内容不一定相同 —— 它只帮你整理，不要当成重复直接删。</li>
        <li>相似视频 / 相同片头 / 相似图片 / 重复音频在向导「高级模式」里按需打开。</li>
      </ul>
      <p class="hint">扫描中可以暂停或停止；硬盘休眠友好开启时，后台任务会主动让路。</p>`,
    primary: ['现在新建查重', () => { closeOnboarding(); openWizard(); }]
  },
  {
    title: '看结果，删除先入隔离区',
    body: () => `
      <p>扫描完成后会自动进入结果页：左侧是分组，右侧是文件与预览。默认只显示主操作，导出、移动、繁简转换在「更多操作」里。</p>
      <ul class="onboard-list">
        <li><b>先看「可释放」</b>：概览只把精确重复算进可释放空间，同名单独列。</li>
        <li><b>隔离而不是直接删</b>：勾选后「移入隔离区」，随时能恢复；确认无误再到隔离区永久删除。</li>
        <li><b>留一份再删</b>：每组会标出建议保留的那份，批量处理前先抽查一两组。</li>
      </ul>
      <p class="hint">想再看一遍引导：设置 → 页面顶部「重新查看新手引导」。</p>`,
    primary: ['去看结果', () => switchTab('results')]
  }
];

function onboardDone(){
  try { return localStorage.getItem(ONBOARD_KEY) === '1'; } catch(e) { return true; }
}

function markOnboardDone(){
  try { localStorage.setItem(ONBOARD_KEY, '1'); } catch(e) {}
}

function renderOnboarding(){
  const box = $('onboard');
  if (!box) return;
  const step = ONBOARD_STEPS[onboardStep] || ONBOARD_STEPS[0];
  const dots = ONBOARD_STEPS.map((_, i) => `<i class="${i === onboardStep ? 'on' : ''}"></i>`).join('');
  if ($('onboardDots')) $('onboardDots').innerHTML = dots;
  const body = $('onboardBody');
  if (body) body.innerHTML = `<h2 id="onboardTitle">${esc(step.title)}</h2>${step.body()}${step.primary
    ? `<p class="onboard-primary"><button class="btn" data-act="onboard-primary">${esc(step.primary[0])}</button></p>`
    : ''}`;
  const prev = box.querySelector('[data-act="onboard-prev"]');
  if (prev) prev.style.visibility = onboardStep === 0 ? 'hidden' : '';
  const next = box.querySelector('[data-act="onboard-next"]');
  if (next) next.textContent = onboardStep === ONBOARD_STEPS.length - 1 ? '开始使用' : '下一步';
  box.hidden = false;
  box.classList.add('show');
  document.body.classList.add('onboard-open');
}

function openOnboarding(fromStart){
  onboardStep = 0;
  if (fromStart) markOnboardDone();
  renderOnboarding();
}

function closeOnboarding(){
  const box = $('onboard');
  if (box) { box.hidden = true; box.classList.remove('show'); }
  document.body.classList.remove('onboard-open');
}

function onboardNext(){
  const step = ONBOARD_STEPS[onboardStep];
  if (onboardStep >= ONBOARD_STEPS.length - 1) { markOnboardDone(); closeOnboarding(); return; }
  onboardStep += 1;
  renderOnboarding();
  // 第二步的「现在新建查重」是主动作，其余步骤下一步只翻页
  if (step && step.primary && onboardStep === ONBOARD_STEPS.length - 1) { /* 保持翻页 */ }
}

function onboardPrev(){
  if (onboardStep > 0) { onboardStep -= 1; renderOnboarding(); }
}

function maybeShowOnboarding(){
  if (onboardDone()) return;
  // 首屏稳定后再弹，避免和登录/状态刷新抢渲染
  setTimeout(() => { if (!onboardDone()) openOnboarding(false); }, 600);
}

document.addEventListener('click', (e) => {
  const el = e.target.closest('[data-act]');
  if (!el) return;
  const act = el.dataset.act;
  if (act === 'onboard-next') { e.preventDefault(); onboardNext(); }
  else if (act === 'onboard-prev') { e.preventDefault(); onboardPrev(); }
  else if (act === 'onboard-skip') { e.preventDefault(); markOnboardDone(); closeOnboarding(); }
  else if (act === 'onboard-restart') { e.preventDefault(); openOnboarding(true); }
  else if (act === 'onboard-primary') {
    e.preventDefault();
    const step = ONBOARD_STEPS[onboardStep];
    if (step && step.primary) { step.primary[1](); }
  }
});
