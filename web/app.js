/* ══════════════════════════════════════════════════════════════════════════
   Mistral 路由控制台 · 应用逻辑
   ══════════════════════════════════════════════════════════════════════════ */

const API = '/admin/api';
const $ = (sel, root) => (root || document).querySelector(sel);
const $$ = (sel, root) => Array.from((root || document).querySelectorAll(sel));

/* ── 工具 ─────────────────────────────────────────────────────────────── */
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) =>
  ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

const num = (n) => (n === null || n === undefined ? '0' : Number(n).toLocaleString('zh-CN'));

function fmtTokens(n) {
  n = Number(n || 0);
  if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B';
  if (n >= 1e6) return (n / 1e6).toFixed(2) + 'M';
  if (n >= 1e4) return (n / 1e3).toFixed(1) + 'K';
  return num(n);
}

/**
 * 上下文长度用 1024 进制显示：262144 → 256K，131072 → 128K。
 * 业界说「256K 上下文」指的就是 262144，用 1000 进制会显示成 262K，对不上。
 */
function fmtCtx(n) {
  n = Number(n || 0);
  if (!n) return '—';
  if (n >= 1024 * 1024) return (n / 1024 / 1024).toFixed(n % (1024 * 1024) ? 1 : 0) + 'M';
  if (n >= 1024) return Math.round(n / 1024) + 'K';
  return String(n);
}

function fmtPct(v, digits = 1) {
  if (v === null || v === undefined) return '—';
  return (Number(v) * 100).toFixed(digits) + '%';
}

/** 相对时间：3 分钟前 / 2 小时前 */
function relTime(ts) {
  if (!ts) return '—';
  const t = new Date(String(ts).replace(' ', 'T') + (String(ts).includes('Z') ? '' : 'Z'));
  if (isNaN(t)) return ts;
  const diff = (Date.now() - t.getTime()) / 1000;
  if (diff < 0) return '刚刚';
  if (diff < 60) return '刚刚';
  if (diff < 3600) return Math.floor(diff / 60) + ' 分钟前';
  if (diff < 86400) return Math.floor(diff / 3600) + ' 小时前';
  if (diff < 2592000) return Math.floor(diff / 86400) + ' 天前';
  return t.toLocaleDateString('zh-CN');
}

function fmtTime(ts) {
  if (!ts) return '—';
  return String(ts).replace('T', ' ').replace('Z', '').slice(0, 19);
}

/** 统一请求：非 2xx 抛错，401 自动回登录页 */
async function api(path, opts) {
  const res = await fetch(API + path, opts);
  if (res.status === 401) { showLogin(); throw new Error('未登录或会话已过期'); }
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!res.ok) {
    const msg = (data && (data.error || data.message)) || ('HTTP ' + res.status);
    throw new Error(typeof msg === 'string' ? msg : JSON.stringify(msg));
  }
  return data;
}

const get = (p) => api(p);
const post = (p, body) => api(p, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body || {}),
});

/* ── Toast ────────────────────────────────────────────────────────────── */
const ICONS = {
  success: '<path d="M20 6 9 17l-5-5"/>',
  error: '<circle cx="12" cy="12" r="10"/><path d="m15 9-6 6M9 9l6 6"/>',
  warning: '<path d="m21.7 18-8-14a2 2 0 0 0-3.4 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.7-3Z"/><path d="M12 9v4M12 17h.01"/>',
  info: '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/>',
};

function toast(type, title, desc) {
  const wrap = $('#toast-wrap');
  const el = document.createElement('div');
  el.className = 'toast ' + type;
  el.innerHTML = `
    <svg class="t-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
         stroke-linecap="round" stroke-linejoin="round">${ICONS[type] || ICONS.info}</svg>
    <div class="t-body">
      <div class="t-title">${esc(title)}</div>
      ${desc ? `<div class="t-desc">${esc(desc)}</div>` : ''}
    </div>`;
  wrap.appendChild(el);
  setTimeout(() => {
    el.style.transition = 'opacity .2s, transform .2s';
    el.style.opacity = '0';
    el.style.transform = 'translateX(16px)';
    setTimeout(() => el.remove(), 220);
  }, type === 'error' ? 5200 : 3200);
}

/* ── 对话框 ───────────────────────────────────────────────────────────── */
function openModal(id) { $('#' + id).classList.add('open'); }
function closeModal(id) { $('#' + id).classList.remove('open'); }

$$('.modal-mask').forEach((m) => {
  m.addEventListener('click', (e) => { if (e.target === m) m.classList.remove('open'); });
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') $$('.modal-mask.open').forEach((m) => m.classList.remove('open'));
});

/** 确认框：返回 Promise<boolean> */
function confirmBox(title, desc, danger) {
  return new Promise((resolve) => {
    const mask = document.createElement('div');
    mask.className = 'modal-mask open';
    mask.innerHTML = `
      <div class="modal" style="max-width:400px">
        <div class="modal-head">
          <h3>${esc(title)}</h3>
          ${desc ? `<div class="card-desc">${esc(desc)}</div>` : ''}
        </div>
        <div class="modal-foot">
          <button class="btn" data-no>取消</button>
          <button class="btn ${danger ? 'btn-danger' : 'btn-primary'}" data-yes>确认</button>
        </div>
      </div>`;
    document.body.appendChild(mask);
    const done = (v) => { mask.remove(); resolve(v); };
    mask.querySelector('[data-no]').onclick = () => done(false);
    mask.querySelector('[data-yes]').onclick = () => done(true);
    mask.addEventListener('click', (e) => { if (e.target === mask) done(false); });
  });
}

/** 复制到剪贴板 */
async function copyText(text, label) {
  try {
    await navigator.clipboard.writeText(text);
    toast('success', '已复制', label || '内容已写入剪贴板');
  } catch {
    const ta = document.createElement('textarea');
    ta.value = text; document.body.appendChild(ta); ta.select();
    document.execCommand('copy'); ta.remove();
    toast('success', '已复制');
  }
}

/* ── 主题 ─────────────────────────────────────────────────────────────── */
function currentTheme() {
  return localStorage.getItem('mr-theme') || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
}
function applyTheme(theme) {
  document.documentElement.classList.toggle('dark', theme === 'dark');
  localStorage.setItem('mr-theme', theme);
  const btn = $('#theme-btn');
  if (btn) {
    btn.innerHTML = theme === 'dark'
      ? '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M6.3 17.7l-1.4 1.4M19.1 4.9l-1.4 1.4"/></svg>'
      : '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/></svg>';
  }
}
function toggleTheme() {
  applyTheme(currentTheme() === 'dark' ? 'light' : 'dark');
}

/* ── 认证 ─────────────────────────────────────────────────────────────── */
function showLogin() {
  $('#login-wrap').classList.add('show');
  $('#app').style.display = 'none';
}
function showApp() {
  $('#login-wrap').classList.remove('show');
  $('#app').style.display = '';
  navigate(location.hash || '#dashboard');
}

async function doLogin() {
  const pw = $('#login-pw').value;
  const errEl = $('#login-err');
  errEl.textContent = '';
  try {
    await post('/login', { password: pw });
    showApp();
  } catch (e) {
    errEl.textContent = e.message || '密码错误';
  }
}

async function doLogout() {
  try { await post('/logout'); } catch {}
  showLogin();
}

/* ── 路由 ─────────────────────────────────────────────────────────────── */
const PAGES = [
  { id: 'dashboard', title: '仪表盘', sub: '账号池与调用概览', group: '总览' },
  { id: 'accounts', title: '账号管理', sub: 'Mistral 账号池', group: '资源' },
  { id: 'keys', title: '密钥管理', sub: '下游分发与策略', group: '资源' },
  { id: 'models', title: '模型中心', sub: '可用模型与限速', group: '资源' },
  { id: 'logs', title: '调用日志', sub: '全量请求审计', group: '观测' },
  { id: 'usage', title: '用量统计', sub: '按天 / 模型 / 账号', group: '观测' },
  { id: 'playground', title: '在线试玩', sub: '直接验证链路', group: '观测' },
  { id: 'farm', title: '注册机', sub: '批量造号与取码', group: '运维' },
  { id: 'settings', title: '设置', sub: '安全与系统参数', group: '运维' },
];

const ICON = {
  dashboard: '<rect x="3" y="3" width="7" height="9" rx="1"/><rect x="14" y="3" width="7" height="5" rx="1"/><rect x="14" y="12" width="7" height="9" rx="1"/><rect x="3" y="16" width="7" height="5" rx="1"/>',
  accounts: '<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/>',
  keys: '<path d="M15.5 7.5 19 4a3.5 3.5 0 1 1 1 1l-3.5 3.5"/><circle cx="9.5" cy="14.5" r="4.5"/>',
  models: '<path d="M12 3 3 8l9 5 9-5-9-5Z"/><path d="m3 16 9 5 9-5M3 12l9 5 9-5"/>',
  logs: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6M8 13h8M8 17h5"/>',
  usage: '<path d="M3 3v18h18"/><path d="m19 9-5 5-4-4-3 3"/>',
  playground: '<path d="m6 8 4 4-4 4"/><path d="M14 16h6"/><rect x="2" y="3" width="20" height="18" rx="2"/>',
  farm: '<path d="M12 2v6M12 22v-6M4.9 4.9l4.2 4.2M14.9 14.9l4.2 4.2M2 12h6M16 12h6M4.9 19.1l4.2-4.2M14.9 9.1l4.2-4.2"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-2.9 1.2 2 2 0 1 1-4 0 1.7 1.7 0 0 0-2.9-1.2l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1A1.7 1.7 0 0 0 3 15a2 2 0 1 1 0-4 1.7 1.7 0 0 0 1.2-2.9l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1A1.7 1.7 0 0 0 10 4.6a2 2 0 1 1 4 0 1.7 1.7 0 0 0 2.9 1.2l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1A1.7 1.7 0 0 0 21 11a2 2 0 1 1 0 4Z"/>',
};

function svg(path) {
  return `<svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">${path}</svg>`;
}

function buildDock() {
  const dock = $('#dock');
  dock.innerHTML = PAGES.map((p) =>
    `<a class="dock-item" href="#${p.id}" data-page="${p.id}">
       ${svg(ICON[p.id] || '')}
       <span class="tip">${p.title}</span>
     </a>`).join('');
}

let currentPage = '';
function navigate(hash) {
  const page = (hash || '').replace('#', '') || 'dashboard';
  const meta = PAGES.find((p) => p.id === page);
  if (!meta) return navigate('#dashboard');

  currentPage = page;
  $$('.page').forEach((el) => el.classList.remove('active'));
  const el = $('#page-' + page);
  if (el) el.classList.add('active');
  $$('.dock-item').forEach((a) => a.classList.toggle('active', a.dataset.page === page));

  $('#page-title').textContent = meta.title;
  $('#page-sub').textContent = meta.sub;

  (RENDER[page] || (() => {}))();
}
window.addEventListener('hashchange', () => navigate(location.hash));

/* ── 页面：仪表盘 ─────────────────────────────────────────────────────── */
function statCard({ label, value, hint, icon, tone }) {
  tone = tone || 'neutral';
  return `
    <div class="stat-card">
      <div class="stat-head">
        <div class="stat-label">${esc(label)}</div>
        ${icon ? `<div class="stat-ico tone-${tone}">${svg(icon)}</div>` : ''}
      </div>
      <div class="stat-value tone-${tone}">${value}</div>
      ${hint ? `<div class="stat-hint">${hint}</div>` : ''}
    </div>`;
}

async function renderDashboard() {
  const grid = $('#stats-grid');
  grid.innerHTML = Array.from({ length: 6 }, () => '<div class="stat-card skeleton" style="border-radius:20px"></div>').join('');

  let stats = {}, series = [], proxy = null, usage = [], scheduleInfo = null;
  try {
    [stats, series, proxy, usage, scheduleInfo] = await Promise.all([
      get('/stats'),
      get('/stats/series?days=14').catch(() => []),
      get('/proxy/status').catch(() => null),
      get('/usage').catch(() => []),
      get('/scheduling').catch(() => null),
    ]);
  } catch (e) {
    toast('error', '加载失败', e.message);
  }

  const aliveRate = stats.total_accounts ? stats.alive_accounts / stats.total_accounts : 0;
  grid.innerHTML = [
    statCard({
      label: '存活账号', value: num(stats.alive_accounts), tone: 'success',
      hint: `共 ${num(stats.total_accounts)} 个 · 健康度 ${fmtPct(aliveRate, 0)}`,
      icon: '<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/>',
    }),
    statCard({
      label: '失效账号', value: num(stats.dead_accounts),
      tone: stats.dead_accounts > 0 ? 'danger' : 'neutral',
      hint: stats.dead_accounts > 0 ? '可在账号页恢复或删除' : '暂无失效账号',
      icon: '<circle cx="12" cy="12" r="10"/><path d="m15 9-6 6M9 9l6 6"/>',
    }),
    statCard({
      label: '路由密钥', value: num(stats.active_keys),
      hint: `共 ${num(stats.total_keys)} 把 · 停用 ${num((stats.total_keys || 0) - (stats.active_keys || 0))} 把`,
      icon: '<path d="M15.5 7.5 19 4a3.5 3.5 0 1 1 1 1l-3.5 3.5"/><circle cx="9.5" cy="14.5" r="4.5"/>',
    }),
    statCard({
      label: '今日请求', value: num(stats.today_requests), tone: 'info',
      hint: `累计 ${num(stats.total_requests)} 次`,
      icon: '<path d="M3 3v18h18"/><path d="m19 9-5 5-4-4-3 3"/>',
    }),
    statCard({
      label: '今日 Token', value: fmtTokens(stats.today_tokens), tone: 'accent',
      hint: `累计 ${fmtTokens(stats.total_tokens)}`,
      icon: '<path d="M12 3 3 8l9 5 9-5-9-5Z"/><path d="m3 16 9 5 9-5"/>',
    }),
    statCard({
      label: '成功率', value: fmtPct(stats.success_rate, 1),
      tone: stats.success_rate >= 0.95 ? 'success' : stats.success_rate >= 0.8 ? 'warning' : 'danger',
      hint: stats.avg_latency_ms ? `平均延迟 ${stats.avg_latency_ms} ms` : '暂无延迟数据',
      icon: '<path d="M20 6 9 17l-5-5"/>',
    }),
  ].join('');

  // 趋势图
  const chart = $('#trend-chart');
  if (!series.length) {
    chart.innerHTML = '<div class="empty"><div class="e-title">暂无调用数据</div><div class="e-desc">发起一次 /v1 调用后这里会显示每日趋势</div></div>';
  } else {
    const max = Math.max(...series.map((s) => s.requests), 1);
    chart.innerHTML = series.map((s) => {
      const h = Math.max((s.requests / max) * 100, 2);
      const fail = s.failures > 0;
      return `<div class="bar-col" title="${s.date}：${num(s.requests)} 次请求 / ${fmtTokens(s.tokens)} tokens${fail ? ' / ' + s.failures + ' 次失败' : ''}">
        <div class="bar-track"><div class="bar" style="height:${h}%;background:${fail ? 'var(--warning)' : 'var(--chart-1)'}"></div></div>
        <div class="bar-label">${s.date.slice(5)}</div>
      </div>`;
    }).join('');
  }

  // 上游健康
  const health = $('#upstream-health');
  if (proxy) {
    const ok = proxy.total_accounts > 0;
    const sch = scheduleInfo || {};
    const rateRow = sch.enabled
      ? `<span class="tabular">${num(sch.limit_per_min)}/min</span>
         <span class="text-xs text-muted">突发 ${num(sch.burst || 0)}${sch.wait_count ? ' · 排队 ' + num(sch.wait_count) : ''}</span>`
      : '<span class="text-xs text-muted">未启用</span>';
    const weightTotal = Object.values(sch.weights || {}).reduce((a, b) => a + b, 0);
    const weightRow = weightTotal
      ? Object.entries(sch.weights).map(([id, w]) =>
          `<span class="model-chip">#${id} ${((w / weightTotal) * 100).toFixed(0)}%</span>`).join('')
      : '<span class="text-xs text-muted">—</span>';
    health.innerHTML = `
      <div class="list-row">
        <span class="lr-label">轮询状态</span>
        <span class="flex items-center gap-2">
          <span class="dot ${ok ? 'dot-success' : 'dot-danger'}"></span>
          <span class="font-medium">${ok ? '在线' : '无可用账号'}</span>
        </span>
      </div>
      <div class="list-row"><span class="lr-label">参与轮询账号</span><span class="tabular">${num(proxy.total_accounts)}</span></div>
      <div class="list-row"><span class="lr-label">上游限流</span><span class="flex items-center gap-2">${rateRow}</span></div>
      <div class="list-row"><span class="lr-label">流量分配</span><span class="cap-grid" style="justify-content:flex-end">${weightRow}</span></div>
      <div class="list-row"><span class="lr-label">下一个账号</span><span class="mono truncate" style="max-width:60%">${esc(proxy.next_account || '—')}</span></div>`;
  } else {
    health.innerHTML = '<div class="empty"><div class="e-title">无法读取上游状态</div></div>';
  }

  // 最近请求
  const recent = $('#recent-usage');
  if (!usage || !usage.length) {
    recent.innerHTML = '<div class="empty"><div class="e-title">暂无数据</div><div class="e-desc">调用 /v1 接口后这里会出现汇总</div></div>';
  } else {
    recent.innerHTML = `<div class="table-wrap"><table class="tbl">
      <thead><tr><th>日期</th><th>模型</th><th>请求数</th><th>Token</th></tr></thead>
      <tbody>${usage.slice(0, 8).map((u) => `
        <tr>
          <td class="cell-muted">${esc(u.date)}</td>
          <td class="mono">${esc(u.model)}</td>
          <td class="num tabular">${num(u.requests)}</td>
          <td class="num tabular">${fmtTokens((u.prompt_tokens || 0) + (u.completion_tokens || 0))}</td>
        </tr>`).join('')}</tbody></table></div>`;
  }
}

/* ── 账号额度 ─────────────────────────────────────────────────────────── */
let quotaMap = {};
let quotaLoading = false;

async function loadQuota(force) {
  if (quotaLoading) return;
  quotaLoading = true;
  toast('info', '正在查询额度', '需登录 console 并探测可用模型，约十几秒');
  try {
    const rows = await get('/quota' + (force ? '?force=1' : ''));
    quotaMap = {};
    rows.forEach((q) => { quotaMap[q.account_id] = q; });
    const okCount = rows.filter((q) => q.status === 'ok').length;
    toast('success', '额度查询完成',
      `共 ${rows.length} 个账号，${okCount} 个取到计划与余额`);
    renderAccounts();
  } catch (e) {
    toast('error', '额度查询失败', e.message);
  } finally {
    quotaLoading = false;
  }
}

function planBadge(q) {
  if (!q) return '<span class="text-xs text-muted">未查询</span>';
  if (q.status === 'no_password') return '<span class="badge badge-warning">缺密码</span>';
  if (q.status === 'error') return `<span class="badge badge-danger">查询失败</span>`;
  const plan = q.plan || '未知';
  const cls = plan === 'FREE' ? 'badge-info' : plan === 'PAY_AS_YOU_GO' ? 'badge-success' : '';
  return `<span class="badge ${cls}">${esc(plan)}</span>`;
}

function quotaCell(q) {
  if (!q) return '<span class="text-xs text-muted">未查询</span>';
  if (!q.rate_limit_req_min) {
    return q.status === 'no_password'
      ? '<span class="badge badge-warning">无数据</span>'
      : '<span class="badge badge-danger">查询失败</span>';
  }
  // 速率额度是实测出来的，最可靠，优先展示
  const limTok = Number(q.rate_limit_tokens_min || 0);
  const remTok = Number(q.rate_remain_tokens || 0);
  const usedPct = limTok ? Math.min(((limTok - remTok) / limTok) * 100, 100) : 0;
  return `<div>
      <div class="quota-line">
        <span class="badge badge-info">${fmtTokens(limTok)} tok/min</span>
        <span class="text-xs text-muted tabular">剩 ${fmtTokens(remTok)}</span>
      </div>
      <div class="quota-bar mt-1" title="本分钟已用 ${usedPct.toFixed(1)}%"><i style="width:${Math.max(usedPct, 1)}%"></i></div>
      <div class="text-xs text-muted mt-1">${num(q.rate_limit_req_min)} req/min</div>
    </div>`;
}

function modelChips(q) {
  if (!q || !q.model_count) return '<span class="text-xs text-muted">—</span>';
  const list = q.models || [];
  const shown = list.slice(0, 2).map((m) => `<span class="model-chip">${esc(m)}</span>`).join('');
  const more = list.length > 2 ? `<span class="model-chip">+${list.length - 2}</span>` : '';
  return `<div class="flex items-center gap-1" style="flex-wrap:nowrap">
      ${shown}${more}
      <button class="btn btn-sm btn-ghost" onclick="openQuotaDetail(${q.account_id})" title="查看详情">…</button>
    </div>`;
}

function openQuotaDetail(id) {
  const q = quotaMap[id];
  if (!q) return;
  $('#quota-title').textContent = '账号额度与能力';
  $('#quota-sub').textContent = q.email;

  const limTok = Number(q.rate_limit_tokens_min || 0);
  const remTok = Number(q.rate_remain_tokens || 0);
  const usedPct = limTok ? ((limTok - remTok) / limTok) * 100 : 0;

  const rows = [
    ['实时速率额度', limTok
      ? `<span class="tabular">${fmtTokens(limTok)} tok/min</span>`
      : '—'],
    ['本分钟剩余', limTok
      ? `<span class="tabular">${fmtTokens(remTok)}（已用 ${usedPct.toFixed(2)}%）</span>`
      : '—'],
    ['请求频率', q.rate_limit_req_min
      ? `<span class="tabular">${num(q.rate_limit_req_min)} req/min，剩 ${num(q.rate_remain_req)}</span>`
      : '—'],
    ['探测模型', q.probe_model ? `<span class="mono">${esc(q.probe_model)}</span>` : '—'],
    ['可用模型', `${q.model_count || 0} 个（最高 ${num(q.max_rate)} req/min）`],
    ['API 计划', q.status === 'ok'
      ? planBadge(q)
      : `<span class="text-xs text-muted">未取到（${esc(q.status === 'no_password' ? '账号未存密码' : 'console 登录失败')}）</span>`],
    ['钱包余额', q.status === 'ok' ? `${Number(q.wallet_amount || 0).toFixed(2)} ${q.currency || 'USD'}` : '—'],
    ['在途用量', q.status === 'ok' ? `${Number(q.ongoing_usage || 0).toFixed(2)} ${q.currency || 'USD'}` : '—'],
    ['订阅类型', (q.subscriptions || []).length ? q.subscriptions.join(' · ') : (q.status === 'ok' ? '无（免费模式）' : '—')],
    ['查询时间', q.checked_at || '—'],
  ];

  $('#quota-body').innerHTML =
    rows.map(([k, v]) => `<div class="list-row"><span class="lr-label">${k}</span><span>${v}</span></div>`).join('')
    + (q.error ? `<div class="mt-3 text-xs text-muted">${esc(q.error)}</div>` : '')
    + ((q.models || []).length
      ? `<div class="mt-4">
           <div class="text-xs text-muted mb-2">该账号可调用的模型（实测探测）</div>
           <div class="cap-grid">${q.models.map((m) => `<span class="model-chip">${esc(m)}</span>`).join('')}</div>
         </div>`
      : '');

  openModal('quota-modal');
}

/* ── 页面：账号 ───────────────────────────────────────────────────────── */
let acctTab = 'alive';

async function renderAccounts() {
  const isAlive = acctTab === 'alive';
  const tbody = $('#acct-body');
  tbody.innerHTML = '<tr><td colspan="7"><div class="skeleton" style="height:20px"></div></td></tr>';

  let rows = [];
  try {
    rows = await get('/accounts/stats?dead=' + (isAlive ? '0' : '1'));
  } catch (e) { toast('error', '加载失败', e.message); }

  $('#acct-count').textContent = rows.length;

  if (!rows.length) {
    tbody.innerHTML = `<tr><td colspan="7"><div class="empty">
      <div class="e-ico">${svg(ICON.accounts)}</div>
      <div class="e-title">${isAlive ? '还没有可用账号' : '没有失效账号'}</div>
      <div class="e-desc">${isAlive ? '去「注册机」页跑一批，或从 results.json 导入' : '账号认证失败后会自动落到这里'}</div>
    </div></td></tr>`;
    return;
  }

  tbody.innerHTML = rows.map((a) => {
    const health = a.is_dead ? 'danger' : (a.requests > 0 ? 'success' : 'muted');
    const healthText = a.is_dead ? '失效' : (a.requests > 0 ? '活跃' : '未使用');
    const q = quotaMap[a.id];
    return `<tr>
      <td class="cell-muted">${a.id}</td>
      <td>
        <div class="cell-strong truncate" style="max-width:220px">${esc(a.email)}</div>
        <div class="text-xs text-muted">${esc(a.api_key)} · ${esc(a.source || '—')}</div>
      </td>
      <td>${quotaCell(q)}</td>
      <td>${modelChips(q)}</td>
      <td>
        <span class="badge ${health === 'success' ? 'badge-success' : health === 'danger' ? 'badge-danger' : ''}">
          <i class="dot dot-${health === 'muted' ? 'muted' : health}"></i>${healthText}
        </span>
      </td>
      <td class="num tabular">${num(a.requests)}<div class="text-xs text-muted">${fmtTokens(a.total_tokens)} tokens</div></td>
      <td class="cell-muted">${a.last_used_at ? relTime(a.last_used_at) : '—'}</td>
      <td>
        <div class="row-actions">
          <button class="btn btn-sm" onclick="queryOneQuota(${a.id})" title="查询该账号额度">额度</button>
          ${a.is_dead ? `<button class="btn btn-sm" onclick="reviveAccount(${a.id})">恢复</button>` : ''}
          <button class="btn btn-sm btn-danger" onclick="deleteAccount(${a.id}, '${esc(a.email)}')">删除</button>
        </div>
      </td>
    </tr>`;
  }).join('');
}

/** 单独查一个账号的额度 */
async function queryOneQuota(id) {
  toast('info', '查询中…', '需要登录 console，约十几秒');
  try {
    const rows = await get('/quota?id=' + id + '&force=1');
    if (rows && rows.length) {
      quotaMap[id] = rows[0];
      openQuotaDetail(id);
      renderAccounts();
    }
  } catch (e) {
    toast('error', '查询失败', e.message);
  }
}

function switchAcctTab(tab) {
  acctTab = tab;
  $$('#acct-tabs .tab').forEach((t) => t.classList.toggle('active', t.dataset.tab === tab));
  renderAccounts();
}

async function reviveAccount(id) {
  await post('/accounts/revive', { id });
  toast('success', '已恢复', '账号重新加入轮询');
  renderAccounts();
}

async function deleteAccount(id, email) {
  if (!(await confirmBox('删除账号', `确认删除 ${email}？该操作不可撤销。`, true))) return;
  await post('/accounts/delete', { id });
  toast('success', '已删除');
  renderAccounts();
}

/* ── 页面：密钥 ───────────────────────────────────────────────────────── */
const KEY_STATE = {
  ok: { cls: 'badge-success', text: '正常' },
  disabled: { cls: '', text: '已停用' },
  expired: { cls: 'badge-warning', text: '已过期' },
  over_quota: { cls: 'badge-danger', text: '额度用尽' },
};

async function renderKeys() {
  const tbody = $('#keys-body');
  tbody.innerHTML = '<tr><td colspan="7"><div class="skeleton" style="height:20px"></div></td></tr>';
  let rows = [];
  try { rows = await get('/keys'); } catch (e) { toast('error', '加载失败', e.message); }

  if (!rows.length) {
    tbody.innerHTML = `<tr><td colspan="7"><div class="empty">
      <div class="e-ico">${svg(ICON.keys)}</div>
      <div class="e-title">还没有密钥</div>
      <div class="e-desc">创建一把密钥，配合 Base URL 即可接入任何 OpenAI 兼容客户端</div>
    </div></td></tr>`;
    return;
  }

  tbody.innerHTML = rows.map((k) => {
    const st = KEY_STATE[k.state] || KEY_STATE.ok;
    const quota = k.quota_tokens > 0
      ? `<div class="progress ${k.used_tokens >= k.quota_tokens ? 'danger' : ''}" style="width:90px">
           <i style="width:${Math.min((k.used_tokens / k.quota_tokens) * 100, 100)}%"></i>
         </div>
         <div class="text-xs text-muted tabular">${fmtTokens(k.used_tokens)} / ${fmtTokens(k.quota_tokens)}</div>`
      : '<span class="text-xs text-muted">不限</span>';
    const policy = [];
    if (k.allowed_models) policy.push(`模型 ${k.allowed_models.split(',').length} 项`);
    if (k.allowed_ips) policy.push(`IP ${k.allowed_ips.split(',').length} 项`);
    return `<tr>
      <td>
        <div class="flex items-center gap-2">
          <span class="mono">${esc(k.key.slice(0, 16))}…</span>
          <button class="btn btn-sm btn-ghost btn-icon" title="复制完整密钥" onclick="copyText('${esc(k.key)}','密钥已复制')">
            ${svg('<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>')}
          </button>
        </div>
      </td>
      <td class="cell-strong">${esc(k.label || '—')}</td>
      <td><span class="badge ${st.cls}">${st.text}</span></td>
      <td class="num tabular">${num(k.requests)}</td>
      <td>${quota}</td>
      <td>${policy.length ? `<span class="text-xs">${policy.join(' · ')}</span>` : '<span class="text-xs text-muted">跟随全局</span>'}</td>
      <td class="cell-muted">${k.expires_at ? fmtTime(k.expires_at).slice(0, 10) : '永不过期'}</td>
      <td class="cell-muted">${k.last_used_at ? relTime(k.last_used_at) : '—'}</td>
      <td>
        <div class="row-actions">
          <button class="btn btn-sm" onclick='editKey(${JSON.stringify(k).replace(/'/g, "&#39;")})'>编辑</button>
          <button class="btn btn-sm btn-danger" onclick="deleteKey('${esc(k.key)}', '${esc(k.label)}')">删除</button>
        </div>
      </td>
    </tr>`;
  }).join('');
}

function openKeyModal() {
  $('#key-modal-title').textContent = '创建密钥';
  $('#key-edit-target').value = '';
  $('#k-label').value = '';
  $('#k-days').value = '';
  $('#k-quota').value = '';
  $('#k-models').value = '';
  $('#k-ips').value = '';
  $('#k-active-wrap').classList.add('hidden');
  openModal('key-modal');
}

function editKey(k) {
  $('#key-modal-title').textContent = '编辑密钥';
  $('#key-edit-target').value = k.key;
  $('#k-label').value = k.label || '';
  $('#k-days').value = '';
  $('#k-quota').value = k.quota_tokens || '';
  $('#k-models').value = k.allowed_models || '';
  $('#k-ips').value = k.allowed_ips || '';
  $('#k-active-wrap').classList.remove('hidden');
  $('#k-active').checked = !!k.active;
  openModal('key-modal');
}

async function saveKey() {
  const target = $('#key-edit-target').value;
  const payload = {
    label: $('#k-label').value.trim() || '默认',
    expires_in_days: parseInt($('#k-days').value, 10) || 0,
    quota_tokens: parseInt($('#k-quota').value, 10) || 0,
    allowed_models: $('#k-models').value.trim(),
    allowed_ips: $('#k-ips').value.trim(),
  };
  try {
    if (target) {
      payload.key = target;
      payload.active = $('#k-active').checked;
      await post('/keys/update', payload);
      toast('success', '已保存');
    } else {
      const r = await post('/keys/generate', payload);
      await copyText(r.key, '新密钥已复制到剪贴板');
    }
    closeModal('key-modal');
    renderKeys();
  } catch (e) { toast('error', '保存失败', e.message); }
}

async function deleteKey(key, label) {
  if (!(await confirmBox('删除密钥', `确认删除「${label || key.slice(0, 12)}」？使用该密钥的客户端会立即失效。`, true))) return;
  await post('/keys/delete', { key });
  toast('success', '已删除');
  renderKeys();
}

/* ── 页面：日志 ───────────────────────────────────────────────────────── */
let logOffset = 0;
const LOG_LIMIT = 50;

async function renderLogs() {
  const tbody = $('#logs-body');
  tbody.innerHTML = '<tr><td colspan="9"><div class="skeleton" style="height:20px"></div></td></tr>';

  const qs = new URLSearchParams({
    limit: LOG_LIMIT,
    offset: logOffset,
    key: $('#log-key').value.trim(),
    model: $('#log-model').value.trim(),
    status: $('#log-status').value,
    since: $('#log-since').value,
  });

  let data = { items: [], total: 0 };
  try { data = await get('/logs?' + qs.toString()); }
  catch (e) { toast('error', '加载失败', e.message); }

  $('#logs-total').textContent = num(data.total);

  if (!data.items.length) {
    tbody.innerHTML = `<tr><td colspan="9"><div class="empty">
      <div class="e-ico">${svg(ICON.logs)}</div>
      <div class="e-title">没有匹配的调用记录</div>
      <div class="e-desc">调整筛选条件，或先去「在线试玩」发一次请求</div>
    </div></td></tr>`;
    $('#logs-pager').innerHTML = '';
    return;
  }

  tbody.innerHTML = data.items.map((l) => {
    const ok = l.status >= 200 && l.status < 300;
    const keyText = l.key_label || (l.key_prefix ? l.key_prefix + '…' : '—');
    return `<tr>
      <td class="cell-muted nowrap">${fmtTime(l.ts)}</td>
      <td><span class="badge badge-outline">${esc(keyText)}</span></td>
      <td class="mono truncate" style="max-width:180px">${esc(l.account_email || ('#' + l.account_id))}</td>
      <td class="mono">${esc(l.model || '—')}</td>
      <td class="cell-muted mono">${esc(l.ip || '—')}</td>
      <td><span class="badge ${ok ? 'badge-success' : 'badge-danger'}">${l.status}</span>
          ${l.error_code ? `<div class="text-xs text-muted">${esc(l.error_code)}</div>` : ''}</td>
      <td class="num tabular">${fmtTokens(l.total_tokens)}</td>
      <td class="num tabular">${l.latency_ms ? l.latency_ms + ' ms' : '—'}</td>
      <td class="cell-muted truncate" style="max-width:220px" title="${esc(l.error_message || '')}">${esc(l.error_message || '—')}</td>
    </tr>`;
  }).join('');

  const pages = Math.ceil(data.total / LOG_LIMIT);
  const cur = Math.floor(logOffset / LOG_LIMIT) + 1;
  $('#logs-pager').innerHTML = `
    <span class="text-xs text-muted">第 ${cur} / ${pages || 1} 页</span>
    <div class="flex gap-2">
      <button class="btn btn-sm" ${logOffset <= 0 ? 'disabled' : ''} onclick="logPage(-1)">上一页</button>
      <button class="btn btn-sm" ${cur >= pages ? 'disabled' : ''} onclick="logPage(1)">下一页</button>
    </div>`;
}

function logPage(dir) {
  logOffset = Math.max(0, logOffset + dir * LOG_LIMIT);
  renderLogs();
}

function resetLogFilter() {
  $('#log-key').value = '';
  $('#log-model').value = '';
  $('#log-status').value = '';
  $('#log-since').value = '';
  logOffset = 0;
  renderLogs();
}

async function clearLogs() {
  if (!(await confirmBox('清空调用日志', '所有审计记录会被永久删除，账号用量统计不受影响。', true))) return;
  const r = await post('/logs/clear');
  toast('success', '已清空', `删除 ${num(r.deleted)} 条记录`);
  logOffset = 0;
  renderLogs();
}

/* ── 页面：模型中心 ───────────────────────────────────────────────────── */
let modelInfo = null;

// 上游 capabilities 字段的中文名。有值的才显示，没值的不显示。
const CAP_NAME = {
  completion_chat: '对话',
  function_calling: '函数调用',
  reasoning: '推理',
  completion_fim: '代码补全',
  vision: '视觉',
  ocr: 'OCR',
  classification: '分类',
  moderation: '内容审核',
  audio: '音频',
  audio_transcription: '语音转写',
  audio_transcription_realtime: '实时转写',
  audio_speech: '语音合成',
  fine_tuning: '可微调',
  unified_resources: '统一资源',
};

async function renderModels() {
  const wrap = $('#models-wrap');
  wrap.innerHTML = '<div class="skeleton" style="height:120px"></div>';

  try { modelInfo = await get('/models/available'); }
  catch (e) { toast('error', '加载失败', e.message); return; }

  const meta = modelInfo.meta || {};
  const limits = meta.limits_req_per_minute || {};
  const blocked = meta.blocked || {};
  const details = modelInfo.details || [];
  const byId = {};
  details.forEach((d) => { byId[d.id] = d; });

  const cards = (modelInfo.allowed || []).map((id) => {
    const d = byId[id] || {};
    const lim = limits[id];
    const isDefault = id === modelInfo.default;
    const caps = d.capabilities || {};
    const capChips = Object.entries(CAP_NAME)
      .filter(([k]) => caps[k])
      .map(([, label]) => `<span class="cap on">${label}</span>`)
      .join('') || '<span class="cap">基础对话</span>';
    const aliases = (d.aliases || []).filter((x) => x !== id);
    return `<div class="card" style="padding:14px">
      <div class="flex items-center justify-between gap-2 mb-1">
        <span class="mono cell-strong truncate" title="${esc(id)}">${esc(id)}</span>
        ${isDefault ? '<span class="badge badge-info">默认</span>' : ''}
      </div>
      ${d.description ? `<div class="text-xs text-muted mb-2" style="min-height:30px">${esc(d.description)}</div>` : ''}
      <div class="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted mb-2">
        ${d.max_context_length ? `<span>上下文 ${fmtCtx(d.max_context_length)}</span>` : ''}
        ${lim ? `<span>限速 ${num(lim)}/min</span>` : ''}
        ${d.type ? `<span>${esc(d.type)}</span>` : ''}
      </div>
      <div class="cap-grid mb-2">${capChips}</div>
      ${aliases.length ? `<div class="text-xs text-muted mb-2 truncate" title="${esc(aliases.join(', '))}">别名：${esc(aliases.join(', '))}</div>` : ''}
      <div class="flex items-center justify-between gap-2">
        <button class="btn btn-sm btn-ghost" onclick="testModel('${esc(id)}')">测试连通</button>
        <span id="model-test-${esc(id)}" class="text-xs truncate" style="max-width:150px"></span>
      </div>
    </div>`;
  }).join('');

  const blockedHtml = Object.entries(blocked).map(([reason, list]) => {
    const label = reason === 'rate_limit_zero' ? '限额为 0（需付费）' : '无权限 403';
    return `<div class="mb-3">
      <div class="text-xs text-muted mb-2">${label} · ${list.length} 个</div>
      <div class="cap-grid">${list.map((m) => `<span class="model-chip">${esc(m)}</span>`).join('')}</div>
    </div>`;
  }).join('');

  // 能力汇总：哪些能力在本账号可用
  const capSummary = {};
  details.forEach((d) => {
    Object.entries(d.capabilities || {}).forEach(([k, v]) => {
      if (v) capSummary[k] = (capSummary[k] || 0) + 1;
    });
  });
  const summaryChips = Object.entries(CAP_NAME)
    .filter(([k]) => capSummary[k])
    .map(([k, label]) => `<span class="cap on">${label} × ${capSummary[k]}</span>`)
    .join('');

  wrap.innerHTML = `
    <div class="card">
      <div class="card-head">
        <div>
          <div class="card-title">可用模型（${(modelInfo.allowed || []).length} 个）</div>
          <div class="card-desc">
            白名单来自 ${esc(modelInfo.path || 'config/models.available.json')}
            ${modelInfo.upstream_total ? ` · 上游共 ${modelInfo.upstream_total} 个，已按免费层实测裁剪` : ''}
          </div>
        </div>
        <button class="btn btn-sm" onclick="renderModels()">刷新</button>
      </div>
      ${summaryChips ? `<div class="cap-grid mb-3">${summaryChips}</div>` : ''}
      <div class="grid gap-3" style="grid-template-columns:repeat(auto-fill,minmax(280px,1fr))">
        ${cards || '<div class="empty"><div class="e-title">白名单为空</div></div>'}
      </div>
    </div>
    <div class="card">
      <div class="card-head"><div class="card-title">被拦截的模型</div></div>
      <div class="card-desc mb-3">这些模型在当前账号的免费层下不可用，白名单已把它们拦在外面</div>
      ${blockedHtml || '<div class="text-sm text-muted">无</div>'}
    </div>`;
}

async function testModel(id) {
  const el = $('#model-test-' + id);
  el.innerHTML = '<span class="text-muted">测试中…</span>';
  try {
    const key = await ensureTestKey();
    const t0 = performance.now();
    const res = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + key },
      body: JSON.stringify({ model: id, messages: [{ role: 'user', content: '回复 OK' }], max_tokens: 8 }),
    });
    const ms = Math.round(performance.now() - t0);
    const data = await res.json();
    if (res.ok) {
      el.innerHTML = `<span class="text-success">✓ ${ms} ms · ${esc(data.choices?.[0]?.message?.content?.trim() || 'ok')}</span>`;
    } else {
      el.innerHTML = `<span class="text-danger">✗ ${res.status} ${esc(data.error?.message || '')}</span>`;
    }
  } catch (e) {
    el.innerHTML = `<span class="text-danger">✗ ${esc(e.message)}</span>`;
  }
}

/* ── 页面：在线试玩 ───────────────────────────────────────────────────── */
let chatHistory = [];

async function ensureTestKey() {
  let key = localStorage.getItem('mr-test-key');
  if (key) return key;
  const r = await post('/keys/generate', { label: '控制台试玩' });
  localStorage.setItem('mr-test-key', r.key);
  toast('info', '已自动创建试玩密钥', '标签：控制台试玩');
  return r.key;
}

async function renderPlayground() {
  const sel = $('#pg-model');
  try {
    const info = await get('/models/available');
    const models = info.allowed || [];
    sel.innerHTML = models.map((m) => `<option value="${esc(m)}" ${m === info.default ? 'selected' : ''}>${esc(m)}</option>`).join('');
  } catch {}
  $('#pg-key').value = localStorage.getItem('mr-test-key') || '';
  renderChat();
}

function renderChat() {
  const box = $('#pg-chat');
  if (!chatHistory.length) {
    box.innerHTML = `<div class="empty">
      <div class="e-ico">${svg(ICON.playground)}</div>
      <div class="e-title">发一条消息试试</div>
      <div class="e-desc">请求会经路由器转发到 Mistral，结果同时写入调用日志</div>
    </div>`;
    return;
  }
  box.innerHTML = chatHistory.map((m) => `
    <div class="flex gap-3 mb-3" style="align-items:flex-start">
      <div class="stat-ico" style="background:${m.role === 'user' ? 'var(--info-bg)' : 'var(--success-bg)'};color:${m.role === 'user' ? 'var(--info)' : 'var(--success)'}">
        ${svg(m.role === 'user' ? '<path d="M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/>' : '<path d="M12 3 3 8l9 5 9-5-9-5Z"/><path d="m3 16 9 5 9-5"/>')}
      </div>
      <div class="flex-1">
        <div class="text-xs text-muted mb-1">${m.role === 'user' ? '你' : 'Mistral'}${m.meta ? ' · ' + esc(m.meta) : ''}</div>
        <div style="white-space:pre-wrap;font-size:13.5px">${esc(m.content)}</div>
      </div>
    </div>`).join('');
  box.scrollTop = box.scrollHeight;
}

async function sendChat() {
  const input = $('#pg-input');
  const text = input.value.trim();
  if (!text) return;
  const model = $('#pg-model').value;
  const key = $('#pg-key').value.trim() || await ensureTestKey();
  localStorage.setItem('mr-test-key', key);

  chatHistory.push({ role: 'user', content: text });
  input.value = '';
  renderChat();

  const btn = $('#pg-send');
  btn.disabled = true;
  btn.textContent = '生成中…';

  try {
    const t0 = performance.now();
    const res = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + key },
      body: JSON.stringify({
        model,
        messages: chatHistory.map((m) => ({ role: m.role, content: m.content })),
        max_tokens: parseInt($('#pg-max').value, 10) || 256,
      }),
    });
    const ms = Math.round(performance.now() - t0);
    const data = await res.json();
    if (res.ok) {
      chatHistory.push({
        role: 'assistant',
        content: data.choices?.[0]?.message?.content || '(空回复)',
        meta: `${data.model || model} · ${ms} ms · ${data.usage?.total_tokens || 0} tokens`,
      });
    } else {
      chatHistory.push({ role: 'assistant', content: '请求失败：' + (data.error?.message || res.status), meta: 'error' });
    }
  } catch (e) {
    chatHistory.push({ role: 'assistant', content: '请求异常：' + e.message, meta: 'error' });
  } finally {
    btn.disabled = false;
    btn.textContent = '发送';
    renderChat();
  }
}

/* ── 页面：用量统计 ───────────────────────────────────────────────────── */
let usageTab = 'models';

async function renderUsage() {
  const wrap = $('#usage-wrap');
  wrap.innerHTML = '<div class="skeleton" style="height:120px"></div>';
  const byModel = usageTab === 'models';
  let rows = [];
  try { rows = await get(byModel ? '/usage' : '/usage/accounts'); }
  catch (e) { toast('error', '加载失败', e.message); }

  if (!rows.length) {
    wrap.innerHTML = `<div class="card"><div class="empty">
      <div class="e-ico">${svg(ICON.usage)}</div>
      <div class="e-title">暂无用量数据</div>
      <div class="e-desc">调用 /v1 接口后这里会按维度汇总</div>
    </div></div>`;
    return;
  }

  const totalReq = rows.reduce((s, r) => s + (r.requests || 0), 0);
  const totalTok = rows.reduce((s, r) => s + (r.prompt_tokens || 0) + (r.completion_tokens || 0), 0);

  const body = byModel
    ? rows.map((r) => `<tr>
        <td class="cell-muted">${esc(r.date)}</td>
        <td class="mono">${esc(r.model)}</td>
        <td class="num tabular">${num(r.requests)}</td>
        <td class="num tabular">${fmtTokens(r.prompt_tokens)}</td>
        <td class="num tabular">${fmtTokens(r.completion_tokens)}</td>
        <td class="num tabular cell-strong">${fmtTokens((r.prompt_tokens || 0) + (r.completion_tokens || 0))}</td>
      </tr>`).join('')
    : rows.map((r) => `<tr>
        <td class="mono truncate" style="max-width:260px">${esc(r.email)}</td>
        <td class="num tabular">${num(r.requests)}</td>
        <td class="num tabular">${fmtTokens(r.prompt_tokens)}</td>
        <td class="num tabular">${fmtTokens(r.completion_tokens)}</td>
        <td class="num tabular cell-strong">${fmtTokens(r.total_tokens)}</td>
        <td class="cell-muted">${esc(r.last_used || '—')}</td>
      </tr>`).join('');

  const head = byModel
    ? '<tr><th>日期</th><th>模型</th><th>请求数</th><th>输入 Token</th><th>输出 Token</th><th>总 Token</th></tr>'
    : '<tr><th>账号</th><th>请求数</th><th>输入 Token</th><th>输出 Token</th><th>总 Token</th><th>最近使用</th></tr>';

  wrap.innerHTML = `
    <div class="stats-grid" style="grid-template-columns:repeat(auto-fit,minmax(160px,1fr))">
      ${statCard({ label: '汇总请求数', value: num(totalReq), tone: 'info' })}
      ${statCard({ label: '汇总 Token', value: fmtTokens(totalTok), tone: 'accent' })}
      ${statCard({ label: byModel ? '模型数' : '账号数', value: num(rows.length) })}
    </div>
    <div class="card">
      <div class="card-head"><div class="card-title">${byModel ? '按模型 × 日期' : '按账号'}</div></div>
      <div class="table-wrap"><table class="tbl"><thead>${head}</thead><tbody>${body}</tbody></table></div>
    </div>`;
}

function switchUsageTab(tab) {
  usageTab = tab;
  $$('#usage-tabs .tab').forEach((t) => t.classList.toggle('active', t.dataset.tab === tab));
  renderUsage();
}

/* ── 页面：注册机 ─────────────────────────────────────────────────────── */
let farmPoll = null;
const FARM_FIELDS = [
  { group: '取码通道', keys: ['OTP_PROVIDER', 'EMAIL_DOMAIN'] },
  { group: '邮件 API 服务（OTP_PROVIDER=mailapi）', keys: ['MAILAPI_BASE', 'MAILAPI_KEY', 'MAILAPI_DOMAIN_ID', 'MAILAPI_EXPIRES_HOURS', 'MAILAPI_PERMANENT'] },
  { group: '自建邮箱服务（OTP_PROVIDER=selfhost）', keys: ['SELFHOST_BASE', 'SELFHOST_ADMIN', 'SELFHOST_PASSWORD', 'SELFHOST_CREATE_USER'] },
  { group: 'IMAP（OTP_PROVIDER=imap）', keys: ['IMAP_HOST', 'IMAP_PORT', 'IMAP_USER', 'IMAP_PASS'] },
  { group: '运行参数', keys: ['MAX_ATTEMPTS', 'CONCURRENCY', 'FARM_MODE'] },
];
const FARM_HINTS = {
  OTP_PROVIDER: 'mailapi / selfhost / imap',
  EMAIL_DOMAIN: '收验证码的域名，如 your-domain.com',
  MAILAPI_BASE: '接口地址',
  MAILAPI_KEY: 'sk_live_ 开头',
  MAILAPI_DOMAIN_ID: '域名 ID',
  MAILAPI_EXPIRES_HOURS: '有效期（小时）',
  MAILAPI_PERMANENT: '1 = 永久邮箱',
  SELFHOST_BASE: '站点地址',
  SELFHOST_ADMIN: '管理员邮箱',
  SELFHOST_PASSWORD: '管理员密码',
  SELFHOST_CREATE_USER: '1 = 注册前建号',
  MAX_ATTEMPTS: '本次最多注册数',
  CONCURRENCY: '并发（1-32）',
  FARM_MODE: 'http / browser',
};
const SECRET_FIELDS = ['MAILAPI_KEY', 'SELFHOST_PASSWORD', 'IMAP_PASS'];

async function renderFarm() {
  let cfg = {};
  try { cfg = await get('/farm/config'); } catch (e) { toast('error', '读取配置失败', e.message); }

  $('#farm-config-form').innerHTML = FARM_FIELDS.map((g) => `
    <div style="grid-column:1/-1" class="sidebar-group">${g.group}</div>
    ${g.keys.map((k) => `
      <div class="field">
        <label>${k} <span class="hint">${FARM_HINTS[k] || ''}</span></label>
        <input class="input ${SECRET_FIELDS.includes(k) ? 'mono' : ''}"
               type="${SECRET_FIELDS.includes(k) ? 'password' : (['MAX_ATTEMPTS', 'CONCURRENCY', 'IMAP_PORT', 'MAILAPI_DOMAIN_ID', 'MAILAPI_EXPIRES_HOURS'].includes(k) ? 'number' : 'text')}"
               id="cfg-${k}" value="${esc(cfg[k] || '')}">
      </div>`).join('')}`).join('');

  await refreshFarmStatus();
  await refreshFarmRuns();
  startFarmPoll();
}

async function refreshFarmStatus() {
  let runs = [];
  try { runs = await get('/farm/runs'); } catch {}
  const running = runs.length && runs[0].status === 'running';
  const el = $('#farm-status');
  el.innerHTML = running
    ? '<span class="dot dot-warning"></span><span class="font-medium text-warning">注册中…</span>'
    : '<span class="dot dot-muted"></span><span class="text-muted">空闲</span>';
  $('#farm-start-btn').disabled = running;
  $('#farm-stop-btn').disabled = !running;
}

async function refreshFarmRuns() {
  let runs = [];
  try { runs = await get('/farm/runs'); } catch {}
  const tbody = $('#farm-runs-body');
  if (!runs.length) {
    tbody.innerHTML = '<tr><td colspan="6"><div class="empty"><div class="e-title">暂无运行记录</div></div></td></tr>';
    return;
  }
  const map = { success: ['badge-success', '成功'], failed: ['badge-danger', '失败'], running: ['badge-warning', '运行中'], stopped: ['', '已停止'] };
  tbody.innerHTML = runs.map((r) => {
    const [cls, txt] = map[r.status] || ['', r.status];
    return `<tr>
      <td class="cell-muted">${r.id}</td>
      <td class="cell-muted">${fmtTime(r.started_at)}</td>
      <td class="cell-muted">${r.ended_at ? fmtTime(r.ended_at) : '—'}</td>
      <td><span class="badge ${cls}">${txt}</span></td>
      <td class="num tabular">${r.max_attempts || 0}</td>
      <td class="num tabular cell-strong">${r.new_accounts || 0}</td>
    </tr>`;
  }).join('');
}

function startFarmPoll() {
  clearInterval(farmPoll);
  farmPoll = setInterval(async () => {
    if (currentPage !== 'farm') { clearInterval(farmPoll); return; }
    try {
      const log = await fetch(API + '/farm/log?lines=200').then((r) => r.text());
      const box = $('#farm-log');
      if (box.value !== log) { box.value = log; box.scrollTop = box.scrollHeight; }
    } catch {}
    refreshFarmStatus();
  }, 2500);
}

async function saveFarmConfig() {
  const cfg = {};
  FARM_FIELDS.forEach((g) => g.keys.forEach((k) => {
    const el = $('#cfg-' + k);
    if (el) cfg[k] = el.value;
  }));
  try {
    await post('/farm/config', cfg);
    toast('success', '配置已保存', '下次启动注册机时生效');
  } catch (e) { toast('error', '保存失败', e.message); }
}

async function startFarm() {
  const max = parseInt($('#cfg-MAX_ATTEMPTS').value, 10) || 10;
  const conc = parseInt($('#cfg-CONCURRENCY').value, 10) || 1;
  try {
    await post('/farm/start', { max_attempts: max, concurrency: conc });
    toast('success', '已启动注册', `目标 ${max} 个账号，并发 ${conc}`);
    refreshFarmStatus();
  } catch (e) { toast('error', '启动失败', e.message); }
}

async function stopFarm() {
  try {
    await post('/farm/stop');
    toast('info', '已停止注册');
    refreshFarmStatus();
  } catch (e) { toast('error', '停止失败', e.message); }
}

/* ── 页面：设置 ───────────────────────────────────────────────────────── */
async function renderSettings() {
  try {
    const s = await get('/settings');
    $('#set-proxy-limit').value = s.proxy_limit || '1';
  } catch {}
  const base = location.origin + '/v1';
  const a = $('#set-base-url'), b = $('#set-base-url-2');
  if (a) a.textContent = base;
  if (b) b.textContent = base;
  applyTheme(currentTheme());
}

/** 清理早于 N 天的调用日志 */
async function cleanupLogs() {
  const days = parseInt($('#set-keep-days').value, 10);
  if (isNaN(days) || days < 0) return toast('warning', '请输入有效天数（0 = 不清理）');
  if (days === 0) return toast('info', '保留天数设为 0，未执行清理');
  const ok = await confirmBox('清理调用日志', `将删除 ${days} 天前的日志记录，此操作不可撤销。`, true);
  if (!ok) return;
  try {
    const r = await post('/logs/cleanup', { keep_days: days });
    toast('success', '清理完成', `删除 ${num(r.deleted)} 条记录`);
  } catch (e) {
    toast('error', '清理失败', e.message);
  }
}

async function saveProxyLimit() {  const lim = $('#set-proxy-limit').value;
  try {
    await post('/settings/proxy-limit', { limit: lim });
    toast('success', '已保存', '同一账号连续处理 ' + lim + ' 个请求后轮换');
  } catch (e) { toast('error', '保存失败', e.message); }
}

async function changePassword() {
  const pw = $('#set-new-pw').value;
  const confirm = $('#set-confirm-pw').value;
  if (!pw) return toast('warning', '请输入新密码');
  if (pw !== confirm) return toast('warning', '两次输入的密码不一致');
  if (pw.length < 4) return toast('warning', '密码至少 4 位');
  try {
    await post('/settings/password', { password: pw });
    $('#set-new-pw').value = '';
    $('#set-confirm-pw').value = '';
    toast('success', '密码已更新');
  } catch (e) { toast('error', '更新失败', e.message); }
}

/* ── 渲染表 ───────────────────────────────────────────────────────────── */
const RENDER = {
  dashboard: renderDashboard,
  accounts: renderAccounts,
  keys: renderKeys,
  logs: renderLogs,
  models: renderModels,
  usage: renderUsage,
  playground: renderPlayground,
  farm: renderFarm,
  settings: renderSettings,
};

/* ── 初始化 ───────────────────────────────────────────────────────────── */
async function init() {
  applyTheme(currentTheme());
  buildDock();

  $('#login-pw').addEventListener('keydown', (e) => { if (e.key === 'Enter') doLogin(); });
  $('#pg-input')?.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) sendChat();
  });

  try {
    await get('/auth-check');
    showApp();
  } catch {
    showLogin();
  }
}

document.addEventListener('DOMContentLoaded', init);
