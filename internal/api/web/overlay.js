/* GoPlay floating island: compact pill <-> expanded card. */
const $ = (id) => document.getElementById(id);
let state = { instances: [], current: null, collapsed: localStorage.getItem('gp.island.collapsed') === '1', drag: null };
const esc = (v) => String(v ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const num = (v, d=0) => (Number.isFinite(Number(v)) ? Number(v) : 0).toFixed(d);
const pct = (v) => Math.max(0, Math.min(100, Math.round((Number(v)||0)*100)));

async function api(path, opts={}) {
  const res = await fetch(path, {headers:{'Content-Type':'application/json'}, ...opts});
  const txt = await res.text();
  let data = {}; try { data = txt ? JSON.parse(txt) : {}; } catch { data = {error:txt}; }
  if (!res.ok) throw new Error(data.error || ('HTTP '+res.status));
  return data;
}

function applyCollapsed() {
  $('island').classList.toggle('collapsed', state.collapsed);
  $('collapseBtn').textContent = state.collapsed ? '+' : '–';
}

function instance() { return state.current; }

function render() {
  const inst = instance();
  const sel = $('instSelect');
  const prev = sel.value;
  sel.innerHTML = state.instances.length
    ? state.instances.map(i => '<option value="' + esc(i.id) + '">' + esc(i.name || i.id) + '</option>').join('')
    : '<option value="">无实例</option>';
  const urlID = new URLSearchParams(location.search).get('instance');
  sel.value = state.current ? state.current.id : (urlID && state.instances.some(i => i.id === urlID) ? urlID : prev);
  if (!inst) {
    $('islandTitle').textContent = 'GoPlay';
    $('islandStatus').textContent = '无实例';
    $('islandAction').textContent = '创建一个实例后，AI 会开始自动游玩。';
    $('islandBar').style.width = '0%';
    $('islandHp').textContent = 'HP —';
    $('islandRoom').textContent = '房间 —';
    $('islandChat').innerHTML = '';
    $('pulse').className = 'island-pulse';
    return;
  }
  const st = inst.status || 'stopped';
  const v = (inst.state && inst.state.values) || {};
  const stats = inst.stats || {};
  const dec = inst.decision || {};
  const act = dec.action || {};
  $('islandTitle').textContent = inst.name || inst.id;
  $('islandStatus').textContent = ({running:'AUTO', human_control:'HUMAN', paused:'PAUSED', stopped:'STOPPED', starting:'START', error:'ERROR'}[st] || st);
  $('pulse').className = 'island-pulse' + (st === 'human_control' ? ' human' : (st === 'error' ? ' error' : ''));
  $('islandBar').style.width = pct(stats.progress ?? 0) + '%';
  $('islandHp').textContent = 'HP ' + num(v.hp) + '/' + num(v.max_hp);
  $('islandRoom').textContent = '房间 ' + num(v.room) + '/' + num(v.max_rooms || 10);
  const last = stats.last_action || (act.kind ? act.kind + (act.target ? ' → ' + act.target : '') : '等待决策');
  $('islandAction').textContent = last + (act.reason ? ' · ' + act.reason : '');
  const chat = (inst.chat || []).slice(-4);
  $('islandChat').innerHTML = chat.map(m => '<div class="msg ' + esc(m.role) + '"><span class="who">' + esc(m.role === 'user' ? '你' : 'AI') + '</span><span class="body">' + esc(m.text) + '</span></div>').join('');
}

async function refresh() {
  try {
    const data = await api('/api/instances');
    state.instances = data.instances || [];
    const urlID = new URLSearchParams(location.search).get('instance');
    const sel = $('instSelect').value;
    const wanted = sel || urlID;
    state.current = state.instances.find(i => i.id === wanted) || state.instances[0] || null;
    render();
  } catch (e) {
    $('islandStatus').textContent = 'DAEMON 离线';
    $('islandAction').textContent = e.message;
    $('pulse').className = 'island-pulse error';
  }
}

async function send(text) {
  if (!state.current || !text.trim()) return;
  try {
    await api('/api/instances/' + encodeURIComponent(state.current.id) + '/message', {method:'POST', body: JSON.stringify({text: text.trim()})});
    $('islandInput').value = '';
    $('islandAction').textContent = '已缓冲，将在下一决策边界生效';
    setTimeout(refresh, 300);
  } catch (e) { $('islandAction').textContent = e.message; }
}

async function control(name) {
  if (!state.current) return;
  try { await api('/api/instances/' + encodeURIComponent(state.current.id) + '/' + name, {method:'POST'}); refresh(); }
  catch (e) { $('islandAction').textContent = e.message; }
}

function boot() {
  applyCollapsed();
  $('collapseBtn').addEventListener('click', (e) => {
    e.stopPropagation();
    state.collapsed = !state.collapsed;
    localStorage.setItem('gp.island.collapsed', state.collapsed ? '1' : '0');
    applyCollapsed();
  });
  $('instSelect').addEventListener('change', (e) => {
    const found = state.instances.find(i => i.id === e.target.value);
    if (found) { state.current = found; history.replaceState(null, '', '/overlay?instance=' + encodeURIComponent(found.id)); render(); }
  });
  $('islandForm').addEventListener('submit', (e) => { e.preventDefault(); send($('islandInput').value); });
  $('pauseBtn').addEventListener('click', () => control('pause'));
  $('resumeBtn').addEventListener('click', () => control('resume'));

  const drag = $('drag');
  drag.addEventListener('pointerdown', (e) => {
    if (e.target.closest('button')) return;
    state.drag = {x:e.clientX, y:e.clientY, rect:$('island').getBoundingClientRect()};
    drag.setPointerCapture(e.pointerId);
  });
  drag.addEventListener('pointermove', (e) => {
    if (!state.drag) return;
    const dx = e.clientX - state.drag.x, dy = e.clientY - state.drag.y;
    const el = $('island');
    el.style.position = 'fixed';
    el.style.left = Math.max(4, state.drag.rect.left + dx) + 'px';
    el.style.top = Math.max(4, state.drag.rect.top + dy) + 'px';
    el.style.right = 'auto';
  });
  const endDrag = () => { state.drag = null; };
  drag.addEventListener('pointerup', endDrag);
  drag.addEventListener('pointercancel', endDrag);

  refresh();
  setInterval(refresh, 1200);
}
document.addEventListener('DOMContentLoaded', boot);
