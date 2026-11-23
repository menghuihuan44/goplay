/* GoPlay dashboard: poll snapshots, stream events, drive controls. */
const $ = (id) => document.getElementById(id);
const S = { instances: [], metrics: null, prefs: [], profiles: [], events: [], filter: 'all', ws: null, wsDelay: 800, timer: null };

const esc = (v) => String(v ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const num = (v, d=0) => (Number.isFinite(Number(v)) ? Number(v) : 0).toFixed(d);
const pct = (v) => Math.max(0, Math.min(100, Math.round((Number(v)||0) * 100)));
const ago = (t) => {
  if (!t) return '';
  const s = Math.max(0, (Date.now() - new Date(t).getTime()) / 1000);
  if (s < 60) return Math.round(s) + 's';
  if (s < 3600) return Math.round(s/60) + 'm';
  return Math.round(s/3600) + 'h';
};

async function api(path, opts = {}) {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...opts });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = { error: text }; }
  if (!res.ok) throw new Error((data && data.error) || ('HTTP ' + res.status));
  return data;
}

function toast(msg, isErr = false) {
  const el = $('toast');
  el.textContent = msg;
  el.className = 'toast show' + (isErr ? ' err' : '');
  clearTimeout(toast._t);
  toast._t = setTimeout(() => { el.className = 'toast'; }, 2800);
}

function statusLabel(s) {
  return ({running:'auto', human_control:'human', paused:'paused', stopped:'stopped', starting:'starting', error:'error'}[s] || s || 'unknown');
}

function renderMetrics() {
  const m = S.metrics || {};
  const h = m.histograms || {};
  const c = m.counters || {};
  $('mDecision').textContent = h['decision_latency_ms'] ? num(h['decision_latency_ms'].p95, 1) : '—';
  $('mPerception').textContent = h['perception_latency_ms'] ? num(h['perception_latency_ms'].p95, 1) : '—';
  $('mAction').textContent = h['action_latency_ms'] ? num(h['action_latency_ms'].p95, 1) : '—';
  $('mHandover').textContent = h['handover_latency_ms'] ? num(h['handover_latency_ms'].p50, 0) : '—';
  $('statActions').textContent = Math.round(c['actions_total'] || 0);
}

function renderGlobal() {
  $('globalMini').innerHTML = S.prefs.length
    ? S.prefs.slice(0, 4).map(p => '<span>' + esc(p.text || Object.keys(p.weights || {}).join(',')) + '</span>').join('')
    : '<span class="muted">暂无偏好</span>';
  $('prefList').innerHTML = S.prefs.length ? S.prefs.map(p =>
    '<div class="pref-item"><i class="dot"></i><span>' + esc(p.text || '软权重偏好') + '</span>' +
    '<span class="tag">' + esc(Object.keys(p.weights || {}).join('·') || p.source || 'soft') + '</span>' +
    '<button title="删除" data-pref-del="' + esc(p.id) + '">×</button></div>'
  ).join('') : '<div class="muted" style="font-size:11px;padding:8px 0">全局记忆为空。这里只放跨游戏风格，例如“更谨慎”。</div>';
}

function episodesHTML(inst) {
  const eps = (inst.episodes || []).slice(-18);
  if (!eps.length) return '<div class="episodes"><span class="muted" style="font-size:9px;align-self:center">尚未完成回合</span></div>';
  return '<div class="episodes" title="最近回合：绿色=通关，红色=失败，高度=进度">' + eps.map(e => {
    const h = 5 + pct(e.progress) * 0.21;
    return '<i class="' + (e.won ? 'won' : (e.progress < 1 ? 'loss' : '')) + '" style="height:' + h.toFixed(1) + 'px"></i>';
  }).join('') + '</div>';
}

function chatHTML(inst) {
  const chat = (inst.chat || []).slice(-5);
  if (!chat.length) return '<div class="chat"><span class="muted" style="font-size:10px">对话会进入指令/偏好缓冲区，在下一个事件边界生效。</span></div>';
  return '<div class="chat">' + chat.map(m =>
    '<div class="msg ' + esc(m.role) + '"><span class="who">' + esc(m.role === 'user' ? '你' : 'ai') + '</span><span class="body">' + esc(m.text) + '</span></div>'
  ).join('') + '</div>';
}

function cardHTML(inst) {
  const st = inst.state || {}; const v = st.values || {};
  const hp = Number(v.hp ?? 0), max = Number(v.max_hp ?? 0) || 1;
  const ehp = Number(v.enemy_hp ?? 0), emax = Number(v.enemy_max_hp ?? 0) || 1;
  const stats = inst.stats || {};
  const dec = inst.decision || {};
  const act = dec.action || {};
  const mem = inst.memory || {};
  const status = inst.status || 'stopped';
  const prog = pct(stats.progress ?? 0);
  const best = pct(stats.best_progress ?? 0);
  const winRate = stats.episodes ? Math.round(stats.wins / stats.episodes * 100) : 0;
  const rawAction = stats.last_action || '';
  const actKind = act.kind || rawAction.split('|')[0] || '—';
  const actDetail = act.kind ? (act.target ? act.target + (act.reason ? ' · ' + act.reason : '') : (act.reason || '')) : rawAction;
  const source = dec.source ? dec.source + (dec.fallback ? ' · fallback' : '') : (inst.profile || '');
  const canPause = status === 'running' || status === 'human_control';
  return '<article class="card ' + esc(status) + '" data-inst="' + esc(inst.id) + '">' +
    '<div class="card-top"><div class="card-title"><h3>' + esc(inst.name || inst.id) + '</h3>' +
      '<span class="card-id">' + esc(inst.id) + ' · ' + esc(inst.profile || 'generic') + ' · seed ' + esc(inst.seed) + '</span></div>' +
      '<span class="status ' + esc(status) + '" title="' + esc(status) + '">' + esc(statusLabel(status)) + '</span></div>' +
    '<div class="vitals">' +
      '<div class="vital"><div class="vital-label"><span>生命</span><b>' + num(hp) + ' / ' + num(max) + '</b></div><div class="bar hp"><i style="width:' + pct(hp/max) + '%"></i></div></div>' +
      '<div class="vital"><div class="vital-label"><span>' + (ehp > 0 ? '敌人' : '药水 / 金币') + '</span><b>' + (ehp > 0 ? num(ehp) + ' / ' + num(emax) : num(v.potions) + ' · ' + num(v.gold)) + '</b></div>' +
        '<div class="bar enemy"><i style="width:' + (ehp > 0 ? pct(ehp/emax) : 100) + '%"></i></div></div>' +
    '</div>' +
    '<div class="big-progress"><span>房间 ' + num(v.room) + ' / ' + num(v.max_rooms || 10) + ' · 本局进度</span><b>' + prog + '%</b></div>' +
    '<div class="bar progress"><i style="width:' + prog + '%"></i></div>' +
    '<div class="stats-row">' +
      '<div class="stat"><b>' + Math.round(stats.decisions || 0) + '</b><span>决策</span></div>' +
      '<div class="stat"><b>' + Math.round(stats.actions || 0) + '</b><span>动作</span></div>' +
      '<div class="stat"><b>' + num(stats.reward, 1) + '</b><span>回报</span></div>' +
      '<div class="stat"><b>' + winRate + '%</b><span>胜率·最佳 ' + best + '%</span></div>' +
    '</div>' +
    episodesHTML(inst) +
    '<div class="action-line"><span class="action-kind">' + esc(actKind) + '</span>' +
      '<span class="action-target" title="' + esc(act.reason || '') + '">' + esc(actDetail || '等待第一个决策边界') + '</span>' +
      '<span class="source-tag">' + esc(source) + '</span></div>' +
    '<div class="action-line" style="border-top:0;padding-top:2px;margin-top:0;min-height:0">' +
      '<span class="muted" style="font-size:9px">记忆 ' + Math.round(mem.working || 0) + ' 工作 · ' + Math.round(mem.summaries || 0) + ' 摘要 · ' + Math.round(mem.facts || 0) + ' 事实 · ' + num((mem.bytes||0)/1024, 1) + ' KB / ' + Math.round(inst.max_episodes || 1) + ' 局</span>' +
      '<span class="source-tag">' + (inst.human_control ? '人工接管中' : '') + '</span></div>' +
    chatHTML(inst) +
    '<form class="chat-form" data-chat="' + esc(inst.id) + '"><input placeholder="对 AI 说：先别打 Boss / 你太激进了" maxlength="500" autocomplete="off"><button type="submit">发送</button></form>' +
    '<div class="card-controls">' +
      (canPause ? '<button data-act="pause" data-id="' + esc(inst.id) + '">暂停</button>' : '') +
      (status === 'paused' ? '<button data-act="resume" data-id="' + esc(inst.id) + '">继续</button>' : '') +
      (status === 'stopped' || status === 'error' ? '<button data-act="start" data-id="' + esc(inst.id) + '">开始</button>' : '') +
      (status !== 'stopped' ? '<button data-act="stop" data-id="' + esc(inst.id) + '">停止</button>' : '') +
      '<a href="/overlay?instance=' + encodeURIComponent(inst.id) + '" target="_blank">灵动岛</a>' +
      '<button class="danger" data-act="delete" data-id="' + esc(inst.id) + '">删除</button>' +
    '</div></article>';
}

function renderInstances() {
  const active = document.activeElement;
  const focusKey = active && active.closest ? active.closest('[data-chat]')?.dataset.chat : null;
  const focusVal = focusKey ? active.value : null;
  let list = S.instances;
  if (S.filter === 'running') list = list.filter(i => i.status === 'running');
  if (S.filter === 'human') list = list.filter(i => i.status === 'human_control');
  $('instances').innerHTML = list.length ? list.map(cardHTML).join('') :
    '<div class="empty">' + (S.instances.length ? '当前筛选下没有实例。' : '还没有游戏实例。<br><br><button class="primary-btn" onclick="document.getElementById(\'newBtn\').click()">创建第一个实例</button>') + '</div>';
  if (focusKey) {
    const box = document.querySelector('[data-chat="' + CSS.escape(focusKey) + '"]');
    if (box) { const inp = box.querySelector('input'); inp.value = focusVal; inp.focus(); }
  }
  $('statRunning').textContent = S.instances.filter(i => i.status === 'running' || i.status === 'human_control').length;
  $('statInstances').textContent = S.instances.length;
}

function renderEvents() {
  const feed = $('eventFeed');
  if (!S.events.length) { feed.innerHTML = '<div class="muted">等待事件…</div>'; return; }
  feed.innerHTML = S.events.slice(-40).reverse().map(e => {
    const d = e.data || {};
    let extra = e.message || d.reason || d.action || d.kind || '';
    if (typeof extra === 'object') extra = JSON.stringify(extra);
    const t = e.at ? new Date(e.at).toLocaleTimeString('zh-CN', {hour12:false}) : '';
    return '<div class="event"><time>' + esc(t) + '</time><b>' + esc(e.type || 'event') + '</b><span title="' + esc(e.instance_id || '') + '">' + esc((e.instance_id ? e.instance_id + ' · ' : '') + extra) + '</span></div>';
  }).join('');
}

function renderAll() { renderMetrics(); renderGlobal(); renderInstances(); renderEvents(); }

async function refresh() {
  try {
    const qs = await Promise.all([
      api('/api/instances').catch(() => null),
      api('/api/metrics').catch(() => null),
      api('/api/memory/global').catch(() => null)
    ]);
    if (qs[0]) S.instances = qs[0].instances || [];
    if (qs[1]) S.metrics = qs[1];
    if (qs[2]) S.prefs = qs[2].preferences || [];
    renderAll();
  } catch (e) { /* transient */ }
}

function connectEvents() {
  if (S.ws) { try { S.ws.close(); } catch {} }
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(proto + '//' + location.host + '/ws/events');
  S.ws = ws;
  ws.onopen = () => { S.wsDelay = 800; };
  ws.onmessage = (ev) => {
    let msg; try { msg = JSON.parse(ev.data); } catch { return; }
    if (msg.type === 'hello') {
      if (Array.isArray(msg.data)) S.events = msg.data.slice(-40);
      renderEvents(); return;
    }
    if (msg.type === 'event') {
      const e = msg.data || {};
      S.events.push(e); if (S.events.length > 80) S.events = S.events.slice(-80);
      renderEvents(); scheduleRefresh();
    }
  };
  ws.onclose = () => {
    S.ws = null;
    setTimeout(connectEvents, S.wsDelay);
    S.wsDelay = Math.min(15000, S.wsDelay * 2);
  };
  ws.onerror = () => { try { ws.close(); } catch {} };
}

let refreshTimer = null;
function scheduleRefresh() {
  if (refreshTimer) return;
  refreshTimer = setTimeout(() => { refreshTimer = null; refresh(); }, 250);
}

const ACTIONS = {
  pause: (id) => api('/api/instances/' + encodeURIComponent(id) + '/pause', {method:'POST'}),
  resume: (id) => api('/api/instances/' + encodeURIComponent(id) + '/resume', {method:'POST'}),
  start: (id) => api('/api/instances/' + encodeURIComponent(id) + '/start', {method:'POST'}),
  stop: (id) => api('/api/instances/' + encodeURIComponent(id) + '/stop', {method:'POST'}),
  delete: (id) => api('/api/instances/' + encodeURIComponent(id), {method:'DELETE'})
};

async function act(name, id) {
  if (name === 'delete' && !confirm('删除实例 ' + id + '？该实例的记忆也会被删除。')) return;
  try { await ACTIONS[name](id); toast(name === 'delete' ? '已删除' : '已' + ({pause:'暂停',resume:'继续',start:'开始',stop:'停止'}[name] || name)); await refresh(); }
  catch (e) { toast(e.message, true); }
}

function initEvents() {
  document.addEventListener('click', async (ev) => {
    const btn = ev.target.closest('[data-act]');
    if (btn) { ev.preventDefault(); await act(btn.dataset.act, btn.dataset.id); return; }
    const del = ev.target.closest('[data-pref-del]');
    if (del) {
      try { await api('/api/memory/global/preferences/' + encodeURIComponent(del.dataset.prefDel), {method:'DELETE'}); toast('偏好已删除'); await refresh(); }
      catch (e) { toast(e.message, true); }
      return;
    }
    const filt = ev.target.closest('[data-filter]');
    if (filt) {
      S.filter = filt.dataset.filter;
      document.querySelectorAll('[data-filter]').forEach(b => b.classList.toggle('active', b === filt));
      renderInstances();
    }
  });
  document.addEventListener('submit', async (ev) => {
    const chat = ev.target.closest('[data-chat]');
    if (chat) {
      ev.preventDefault();
      const input = chat.querySelector('input');
      const text = input.value.trim();
      if (!text) return;
      try {
        await api('/api/instances/' + encodeURIComponent(chat.dataset.chat) + '/message', {method:'POST', body: JSON.stringify({text})});
        input.value = '';
        toast('已进入指令/偏好缓冲区，下一决策周期生效');
        scheduleRefresh();
      } catch (e) { toast(e.message, true); }
    }
    if (ev.target.id === 'prefForm') {
      ev.preventDefault();
      const text = $('prefText').value.trim();
      const weight = $('prefWeight').value;
      const labels = {risk_aversion:'更谨慎', aggression:'更激进', exploration:'偏好探索', resource_aversion:'节省资源'};
      try {
        await api('/api/memory/global/preferences', {method:'POST', body: JSON.stringify({text: text || labels[weight], weights: {[weight]: 1}, source:'ui'})});
        $('prefText').value = '';
        $('prefForm').classList.add('hidden');
        toast('已写入全局偏好（软权重）');
        await refresh();
      } catch (e) { toast(e.message, true); }
    }
  });
}

function openModal() {
  $('modal').classList.remove('hidden');
  if (!$('fId').value || $('fId').value === 'dungeon') $('fId').value = 'game-' + Math.random().toString(36).slice(2, 6);
  setTimeout(() => $('fName').focus(), 30);
}
function closeModal() { $('modal').classList.add('hidden'); }

async function loadProfiles() {
  try {
    const data = await api('/api/config');
    S.profiles = data.profiles || [];
    $('fProfile').innerHTML = S.profiles.map(p =>
      '<option value="' + esc(p.id) + '">' + esc(p.name || p.id) + (p.games && p.games.length ? ' · ' + esc(p.games.join(', ')) : '') + '</option>'
    ).join('') || '<option value="mock_dungeon">Mock 地下城</option>';
    if (data.config?.addr) $('daemonAddr').textContent = data.config.addr;
  } catch {}
}

async function createInstance(ev) {
  ev.preventDefault();
  const auto = $('fAuto').checked;
  const body = {
    id: $('fId').value.trim(), name: $('fName').value.trim(),
    profile_id: $('fProfile').value, seed: Number($('fSeed').value) || 0,
    max_episodes: Number($('fEpisodes').value) || 10, auto_start: auto
  };
  try {
    await api('/api/instances', {method:'POST', body: JSON.stringify(body)});
    closeModal(); toast('实例已创建' + (auto ? '，AI 开始自动游玩' : ''));
    await refresh();
  } catch (e) { toast(e.message, true); }
}

function boot() {
  initEvents();
  $('newBtn').addEventListener('click', openModal);
  $('closeModal').addEventListener('click', closeModal);
  $('cancelCreate').addEventListener('click', closeModal);
  $('modal').addEventListener('click', (e) => { if (e.target === $('modal')) closeModal(); });
  $('createForm').addEventListener('submit', createInstance);
  $('prefToggle').addEventListener('click', () => $('prefForm').classList.toggle('hidden'));
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeModal();
    if (e.key === 'n' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); openModal(); }
  });
  loadProfiles();
  refresh();
  connectEvents();
  S.timer = setInterval(() => { if (!document.hidden) refresh(); }, 1500);
}

document.addEventListener('DOMContentLoaded', boot);
