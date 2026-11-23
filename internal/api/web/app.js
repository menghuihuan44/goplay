(function(){
'use strict';
var S={
  page:'overview', instances:[], metrics:null, prefs:[], profiles:[], llm:null, local:null,
  cfg:null, health:null, events:[], filter:'all', loading:false, suspendGames:false,
  voiceTarget:null, eventsAt:0, ws:null, poll:null, staticLoaded:false, visionDirty:false
};
var $=function(sel,root){return (root||document).querySelector(sel)};
var $$=function(sel,root){return Array.prototype.slice.call((root||document).querySelectorAll(sel))};

function esc(v){
  return String(v===undefined||v===null?'':v)
    .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
    .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}
function num(v,d){v=Number(v);return isFinite(v)?v:(d||0)}
function pct(v){
  if(v===undefined||v===null) return 0;
  v=Number(v); if(!isFinite(v)) return 0;
  return Math.max(0,Math.min(100, v<=1 ? v*100 : v));
}
function fmtPct(v){return Math.round(pct(v))+'%'}
function fmtMS(v){if(v===undefined||v===null||!isFinite(Number(v))) return '—'; return (Math.round(Number(v)*10)/10).toFixed(1)}
function fmtSize(b){
  b=Number(b)||0;
  if(b>=1073741824) return (b/1073741824).toFixed(2)+' GB';
  if(b>=1048576) return (b/1048576).toFixed(0)+' MB';
  if(b>=1024) return (b/1024).toFixed(0)+' KB';
  return b+' B';
}
function ago(at){
  if(!at) return '';
  var t=new Date(at); if(isNaN(t.getTime())) return '';
  var s=Math.max(0,(Date.now()-t.getTime())/1000);
  if(s<60) return '刚刚';
  if(s<3600) return Math.floor(s/60)+' 分钟前';
  if(s<86400) return Math.floor(s/3600)+' 小时前';
  return Math.floor(s/86400)+' 天前';
}
function toast(msg,isErr){
  var t=$('#toast'); if(!t) return;
  t.textContent=msg; t.className='toast show'+(isErr?' err':'');
  clearTimeout(toast._timer);
  toast._timer=setTimeout(function(){t.className='toast'+(isErr?' err':'')},2600);
}
async function api(path,opts){
  opts=opts||{};
  var init={method:opts.method||'GET',headers:{}};
  if(opts.body!==undefined){init.headers['Content-Type']='application/json';init.body=JSON.stringify(opts.body)}
  var res=await fetch(path,init);
  var text=await res.text();
  var data=null;
  if(text){try{data=JSON.parse(text)}catch(e){data={error:text}}}
  if(!res.ok){
    var msg=data&&data.error?data.error:('HTTP '+res.status);
    throw new Error(msg);
  }
  return data;
}
function fillIfIdle(id,val){
  var el=$('#'+id); if(!el) return;
  if(document.activeElement===el) return;
  if(el.value!==String(val===undefined?'':val)) el.value=String(val===undefined?'':val);
}
function sourceLabel(k){return k==='android'?'手机':k==='windows'?'电脑':k==='mock'?'演示':'外部'}
function sourceIcon(k){return k==='android'?'▣':k==='windows'?'▤':'◇'}
function statusInfo(v){
  var s=(v&&v.status)||'stopped';
  if(s==='human_control') return {key:'human',label:'人工接管'};
  if(s==='running') return {key:'running',label:'AUTO'};
  if(s==='paused') return {key:'paused',label:'已暂停'};
  if(s==='starting') return {key:'running',label:'启动中'};
  if(s==='error') return {key:'error',label:'错误'};
  return {key:'stopped',label:'已停止'};
}
function modeLabel(m){
  if(m==='api') return '云端 API';
  if(m==='local') return '本机小模型';
  if(m==='auto') return '自动选择';
  if(m==='offline') return '离线规则';
  return m||'未知';
}
function instanceName(id){
  for(var i=0;i<S.instances.length;i++){if(S.instances[i].id===id) return S.instances[i].name}
  return id||'';
}
function weightLabel(k){
  return ({risk_aversion:'更谨慎',aggression:'更激进',exploration:'偏好探索',resource_aversion:'节省资源',aggression_bias:'更激进'})[k]||k;
}
function vals(v){return (v&&v.state&&v.state.values)||{}}
function stat(v,k,d){var s=(v&&v.stats)||{};return num(s[k],d)}

function setupSteps(){
  var llm=S.llm||{}; var local=llm.local||{}; var router=llm.router||{};
  var apiOk=!!(llm.has_api_key&&router.api_available);
  var localOk=!!(local.enabled||(local.server&&local.server.running));
  var aiOk=llm.mode!=='offline'&&(apiOk||localOk);
  var gameOk=S.instances.length>0;
  var memOk=S.prefs.length>0;
  var items=[
    {done:aiOk,num:aiOk?'✓':'1',title:'选好 AI 大脑',desc:aiOk?(llm.mode==='api'?'API Key 已配置':llm.mode==='local'?'本机小模型运行中':'自动选择可用模型'):'填 API Key，或下载本机小模型',page:'ai',btn:'去设置'},
    {done:gameOk,num:gameOk?'✓':'2',title:'选一个游戏',desc:gameOk?('已有 '+S.instances.length+' 个实例'):'从手机已安装游戏里选一个',page:'games',btn:gameOk?'查看':'去选择'},
    {done:memOk,num:memOk?'✓':'3',title:'告诉它你的偏好',desc:memOk?('已记录 '+S.prefs.length+' 条跨游戏偏好'):'比如“宁可保守，也别频繁送死”',page:'memory',btn:'去添加'}
  ];
  return items.map(function(it){
    return '<div class="setup-step '+(it.done?'done':'')+'">'
      +'<span class="num">'+it.num+'</span>'
      +'<div><b>'+esc(it.title)+'</b><small>'+esc(it.desc)+'</small></div>'
      +'<button data-page="'+it.page+'">'+esc(it.btn)+'</button></div>';
  }).join('');
}

function compactCard(v){
  var st=statusInfo(v), vs=vals(v), progress=pct(stat(v,'progress',0));
  var hp=num(vs.hp,100), maxHp=num(vs.max_hp,100), enemies=num(vs.enemies,0), rooms=num(vs.rooms,0), room=num(vs.room,0);
  var action=stat(v,'last_action','')||((v.decision&&v.decision.action&&v.decision.action.kind)||'等待决策');
  var extra=rooms>0?('房间 '+room+'/'+rooms):(enemies>0?('敌人 '+enemies):('来源 '+sourceLabel(v.source_kind)));
  return '<article class="icard" data-card="'+esc(v.id)+'">'
    +'<div class="icard-top"><span class="dot '+(st.key==='running'?'on':'')+'"></span><h3>'+esc(v.name||v.id)+'</h3><span class="status '+st.key+'">'+st.label+'</span></div>'
    +'<div class="icard-sub">'+esc(v.profile||'')+' · '+esc(v.id)+'</div>'
    +'<div class="vitals">'
      +'<div class="vital"><span>生命 <b>'+hp+' / '+maxHp+'</b></span><div class="bar hp"><i style="width:'+Math.max(0,Math.min(100,hp/Math.max(1,maxHp)*100))+'%"></i></div></div>'
      +'<div class="vital"><span>'+esc(extra)+'</span><div class="bar"><i style="width:'+progress+'%"></i></div></div>'
    +'</div>'
    +'<div class="progline"><span>本局进度</span><b>'+progress+'%</b></div>'
    +'<div class="action-line"><b>'+esc(action)+'</b></div>'
    +'<div class="icard-actions">'
      +(st.key==='running'?'<button class="pill-btn" data-act="pause" data-id="'+esc(v.id)+'">暂停</button><button class="pill-btn" data-act="stop" data-id="'+esc(v.id)+'">停止</button>':'<button class="pill-btn" data-act="start" data-id="'+esc(v.id)+'">开始</button>')
      +'<button class="pill-btn" data-goto="'+esc(v.id)+'">打开</button>'
    +'</div></article>';
}

function fullCard(v){
  var st=statusInfo(v), vs=vals(v), stats=v.stats||{}, memory=v.memory||{};
  var hp=num(vs.hp,0), maxHp=num(vs.max_hp,0);
  var progress=pct(stats.progress||0);
  var action=stats.last_action||((v.decision&&v.decision.action&&v.decision.action.kind)||'等待决策');
  var chat=(v.chat||[]).slice(-2).map(function(m){
    return '<div class="chat-line '+(m.role==='user'?'user':'ai')+'"><b>'+(m.role==='user'?'你':'AI')+'</b>'+esc(m.text)+'</div>';
  }).join('');
  var running=st.key==='running';
  var controls='';
  if(running){
    controls='<button class="pill-btn" data-act="pause" data-id="'+esc(v.id)+'">暂停</button><button class="pill-btn" data-act="stop" data-id="'+esc(v.id)+'">停止</button>';
  }else if(st.key==='human'){
    controls='<button class="pill-btn" data-act="resume" data-id="'+esc(v.id)+'">恢复 AI</button><button class="pill-btn" data-act="stop" data-id="'+esc(v.id)+'">停止</button>';
  }else if(st.key==='paused'){
    controls='<button class="pill-btn" data-act="resume" data-id="'+esc(v.id)+'">继续</button><button class="pill-btn" data-act="stop" data-id="'+esc(v.id)+'">停止</button>';
  }else{
    controls='<button class="pill-btn" data-act="start" data-id="'+esc(v.id)+'">开始</button>';
  }
  return '<article class="game-card" data-card="'+esc(v.id)+'">'
    +'<div class="gc-head"><div style="min-width:0;flex:1"><h3>'+esc(v.name||v.id)+'</h3><div class="gc-meta">'+sourceIcon(v.source_kind)+' '+esc(v.profile||'')+' · seed '+esc(v.seed||0)+'</div></div><span class="status '+st.key+'">'+st.label+'</span></div>'
    +'<div class="gc-stats">'
      +'<div><span>生命</span><b>'+(maxHp>0?hp+' / '+maxHp:'—')+'</b></div>'
      +'<div><span>动作</span><b>'+Math.round(stat(v,'actions',0))+'</b></div>'
      +'<div><span>胜负</span><b class="lime">'+Math.round(stat(v,'wins',0))+' / '+Math.round(stat(v,'episodes',0))+'</b></div>'
    +'</div>'
    +'<div class="progress"><div class="labels"><span>本局进度 · '+esc(v.state&&v.state.summary?v.state.summary:'等待第一帧')+'</span><b>'+progress+'%</b></div><div class="bar"><i style="width:'+progress+'%"></i></div></div>'
    +'<div class="action-line"><b>'+esc(action)+'</b>'+(v.decision&&v.decision.reason?' · '+esc(v.decision.reason):'')+'</div>'
    +(chat?'<div class="chat-lines">'+chat+'</div>':'')
    +'<div class="gc-chat"><textarea rows="2" maxlength="500" data-input="'+esc(v.id)+'" placeholder="说话或输入约束，例如：先别打那个 Boss"></textarea><button class="mic-btn" data-mic="'+esc(v.id)+'" title="语音输入">◉</button><button class="send-btn" data-send="'+esc(v.id)+'">发送</button></div>'
    +'<div class="gc-chat-hint">Enter 发送 · Shift+Enter 换行 · 指令在下一个决策周期生效</div>'
    +'<div class="card-controls">'+controls+'<button class="pill-btn" data-mem="'+esc(v.id)+'">记忆 '+Math.round(num(memory.bytes,0)/1024)+' KB</button><button class="pill-btn danger-text" data-act="delete" data-id="'+esc(v.id)+'">删除</button></div>'
    +'</article>';
}

function renderOverview(){
  $('#setupSteps').innerHTML=setupSteps();
  var h=(S.metrics&&S.metrics.histograms)||{};
  function p(name,q){var x=h[name];if(!x)return '—';return fmtMS(q==='p50'?x.p50:x.p95)}
  $('#mDecision').textContent=p('decision_latency_ms');
  $('#mPerception').textContent=p('perception_latency_ms');
  $('#mAction').textContent=p('action_latency_ms');
  $('#mHandover').textContent=(h.handover_latency_ms?fmtMS(h.handover_latency_ms.p50):'—');
  $('#overviewInstances').innerHTML=S.instances.length?S.instances.slice(0,3).map(compactCard).join(''):'<div class="empty">还没有游戏。点右上角「＋ 新游戏」开始。</div>';
  $('#globalMini').innerHTML=S.prefs.length?S.prefs.slice(0,4).map(function(p){
    return '<div class="mini-pref"><i></i><span>'+esc(p.text||'（仅权重）')+'</span></div>';
  }).join(''):'<span class="muted">暂无偏好</span>';
  renderEvents();
}

function renderGames(){
  if(S.suspendGames) return;
  var list=S.instances.filter(function(v){
    var k=statusInfo(v).key;
    if(S.filter==='running') return k==='running'||k==='paused';
    if(S.filter==='human') return k==='human';
    if(S.filter==='stopped') return k==='stopped'||k==='error';
    return true;
  });
  $('#gameList').innerHTML=list.length?list.map(fullCard).join(''):'<div class="empty">'+(S.instances.length?'这个筛选下没有游戏。':'还没有游戏。创建一个，AI 就会开始看画面并决策。')+'</div>';
  var grid=$('#profileGrid');
  if(grid){
    grid.innerHTML=(S.profiles||[]).map(function(p){
      return '<button class="profile-card" data-profile="'+esc(p.id)+'" style="text-align:left"><div class="pid">'+esc(p.id)+'</div><b>'+esc(p.name||p.id)+'</b><p>'+esc(p.description||'通用 Lua 配置')+'</p></button>';
    }).join('');
  }
}

function renderAI(){
  var llm=S.llm||{}; var local=llm.local||{}; var router=llm.router||{};
  var apiState=llm.has_api_key?(router.api_available?'可用':'已保存 Key'):'未配置';
  var localState=(local.server&&local.server.running)?'运行中':(local.enabled?'已就绪':(local.download&&local.download.active?'下载中':'未启动'));
  $('#brainCard').innerHTML='<div class="brain-main"><div class="blabel">当前 AI 大脑</div><h2><span class="dot '+(llm.mode==='offline'?'err':'on')+'"></span>'+esc(modeLabel(llm.mode))+'</h2><p>'+esc(llm.model||'尚未选择模型')+' · '+(llm.mode==='api'?'请求发往你填写的接口':llm.mode==='local'?'全部在本机推理':'按可用性自动切换')+'</p></div>'
    +'<div class="brain-chips">'
      +'<span class="bchip">API<b>'+esc(apiState)+'</b></span>'
      +'<span class="bchip">本机模型<b>'+esc(localState)+'</b></span>'
      +'<span class="bchip">视觉<b>'+(llm.vision?'已开启':'文本状态')+'</b></span>'
      +'<span class="bchip">设备档位<b>'+esc(local.tier||'检测中')+'</b></span>'
    +'</div>';
  fillIfIdle('apiBase',llm.base_url||'');
  fillIfIdle('apiModel',llm.model||'');
  fillIfIdle('apiTemp',llm.temperature===undefined?'':llm.temperature);
  fillIfIdle('apiTokens',llm.max_tokens||'');
  if(!S.visionDirty) $('#apiVision').checked=!!llm.vision;
  var apiTag=$('#apiTag'); apiTag.textContent=apiState; apiTag.className='tag '+(llm.has_api_key?'ok':'warn');

  var ltag=$('#localTag'); ltag.textContent=localState; ltag.className='tag '+(localState==='运行中'?'ok':localState==='下载中'?'warn':'');
  var hw=local.hardware||{};
  var chips=[];
  if(local.tier) chips.push('<span class="bchip">推荐档位<b>'+esc(local.tier)+'</b></span>');
  if(hw.ram_gb) chips.push('<span class="bchip">内存<b>'+esc(Number(hw.ram_gb).toFixed(1))+' GB</b></span>');
  if(hw.cores) chips.push('<span class="bchip">核心<b>'+esc(hw.cores)+'</b></span>');
  if(hw.arch) chips.push('<span class="bchip">架构<b>'+esc(hw.arch)+'</b></span>');
  if(local.server&&local.server.running) chips.push('<span class="bchip">llama-server<b>127.0.0.1:'+esc(local.server.port||'')+'</b></span>');
  $('#localSummary').innerHTML=chips.join('')||'<span class="muted">正在检测设备…</span>';
  var models=local.models||[];
  var dl=local.download||{};
  if(!models.length){
    $('#localModels').innerHTML='<div class="muted">没有检测到可用模型档位。'+(local.error?' '+esc(local.error):'')+'</div>';
  }else{
    $('#localModels').innerHTML=models.map(function(m){
      var active=dl.active&&(dl.model_id===m.id||!dl.model_id);
      var needLabel=m.present?'已下载':(m.role==='strategy'?'需下载':'规则层');var canDl=m.role==='strategy'&&!m.present&&!active;var canCancel=active;var row='<div class="model-row'+(active?' dl':'')+'"><div class="mr-head"><span class="dot '+(m.present?'on':'')+'"></span><b>'+esc(m.id)+'</b><span class="tag">'+esc(m.role||'')+'</span><span class="tag '+(m.present?'ok':'')+'">'+needLabel+'</span>'+(canDl?'<button class="pill-btn model-action" data-dl="'+esc(m.id)+'">下载</button>':'')+(canCancel?'<button class="pill-btn model-action" data-cancel="1">取消</button>':'')+'</div>'
        +'<div class="mr-meta"><span>'+esc(m.license||'')+'</span><span>'+esc(fmtSize(m.size_bytes))+'</span></div>';
      if(active){
        row+='<div class="bar"><i style="width:'+Math.max(0,Math.min(100,num(dl.percent,0)))+'%"></i></div><div class="mr-meta"><span>'+esc(fmtSize(dl.downloaded))+' / '+esc(fmtSize(dl.total))+'</span><span>'+esc(num(dl.bytes_per_second,0).toFixed(0))+' B/s</span></div>';
      }
      row+='</div>';
      return row;
    }).join('');
  }
  $('#localNote').textContent=dl.error?('下载失败：'+dl.error):(local.note||'模型不入安装包，首次启用时按设备档位下载。');
  var starting=!!(local.download&&local.download.active);
  $('#localStart').disabled=!!((local.server&&local.server.running)||starting);
  $('#localStart').textContent=starting?'正在下载模型…':(local.server&&local.server.running?'本机 AI 运行中':'启用本机 AI');
  $('#localStop').disabled=!(local.server&&local.server.running);
  $('#localRefresh').disabled=false;
}

function renderMemory(){
  var pl=$('#prefList');
  pl.innerHTML=S.prefs.length?S.prefs.map(function(p){
    var ws=p.weights||{}; var keys=Object.keys(ws);
    var wt=keys.length?keys.map(weightLabel).join(' / '):'文字描述';
    return '<div class="pref-item"><div class="pt">'+esc(p.text||'（仅权重）')+'</div><div class="pm"><span>'+esc(wt)+' · '+esc(ago(p.updated_at||p.created_at))+'</span><button class="link-btn danger-text" data-pref="'+esc(p.id)+'">删除</button></div></div>';
  }).join(''):'<div class="muted">还没有跨游戏偏好。添加一条，AI 会把它当作软权重，而不是硬条件。</div>';
  var mi=$('#memoryInstances');
  mi.innerHTML=S.instances.length?S.instances.map(function(v){
    var m=v.memory||{};
    return '<div class="mem-card"><div class="mh"><span>'+esc(v.name||v.id)+'</span><span class="status '+statusInfo(v).key+'">'+statusInfo(v).label+'</span></div><div class="mm"><span>工作 '+esc(m.working||0)+'</span><span>摘要 '+esc(m.summaries||0)+'</span><span>事实 '+esc(m.facts||0)+'</span><span>'+esc(Math.round(num(m.bytes,0)/1024))+' KB / '+esc(v.memory_budget_kb||256)+' KB</span></div></div>';
  }).join(''):'<div class="muted">创建游戏后，这里会显示每个实例的分层记忆占用。</div>';
}

function renderSettings(){
  var cfg=S.cfg||{};
  $('#setAddr').textContent=cfg.addr||location.host||'—';
  $('#setData').textContent=cfg.data_dir||'—';
  $('#setVersion').textContent=(cfg.version||(S.health&&S.health.version)||'—');
  $('#setMode').textContent=modeLabel((S.llm&&S.llm.mode)||cfg.llm&&cfg.llm.mode);
  var host=$('#hostInfo');
  if(window.GoPlayHost){host.textContent='Android 原生壳 · 核心与模型都在本机运行';}
  else if(window.goplayDesktop){host.textContent='桌面 WebView · 核心在同一进程';}
  else{host.textContent='浏览器模式 · 连接到本机或局域网核心';}
  var hasDesktop=!!window.goplayDesktop;
  $('#desktopData').classList.toggle('hidden',!hasDesktop);
  $('#desktopQuit').classList.toggle('hidden',!hasDesktop);
  refreshHostStatus();
}

function renderEvents(){
  var feed=$('#eventFeed'); if(!feed) return;
  if(!S.events.length){feed.innerHTML='<div class="muted">等待事件…开始一个游戏后，这里会实时显示决策和动作。</div>';return}
  feed.innerHTML=S.events.slice(0,14).map(function(e){
    var d=e.data||{}; var kind=e.type||'event'; var text='';
    if(kind==='instance.created') text='创建了 '+esc(d.profile||'游戏');
    else if(kind==='instance.start_requested') text='开始自动游玩';
    else if(kind==='instance.stopped') text='已停止';
    else if(kind==='instance.paused') text='已暂停';
    else if(kind==='human.takeover'||kind==='instance.human') text='检测到人工操作，已交还控制权';
    else if(kind==='global.preference_added') text='新增跨游戏偏好';
    else if(kind==='instance.constraint_added') text='新增实例硬约束';
    else if(d.action) text=esc(d.action.kind||d.action);
    else if(d.text) text=esc(d.text);
    else text=esc(kind.replace(/^instance\./,'').replace(/_/g,' '));
    var name=e.instance_id?instanceName(e.instance_id):'全局';
    return '<div class="event"><span class="t">'+esc(ago(e.at))+'</span><span class="m"><span class="kind">'+esc(kind.split('.').pop())+'</span><b>'+esc(name)+'</b> '+text+'</span></div>';
  }).join('');
}

function renderShell(){
  var running=0;
  S.instances.forEach(function(v){var k=statusInfo(v).key;if(k==='running'||k==='human')running++});
  var actions=0; S.instances.forEach(function(v){actions+=stat(v,'actions',0)});
  $('#statRunning').textContent=running;
  $('#statActions').textContent=Math.round(actions);
  var addr=(S.health&&S.health.addr)||S.cfg&&S.cfg.addr||location.host;
  $('#daemonAddr').textContent=addr||'127.0.0.1:8420';
  var on=!!S.health;
  $('#daemonDot').className='dot '+(on?'on':'err');
  $('#sideDot').className='dot '+(on?'on':'err');
  $('#sideState').textContent=on?'本机核心在线':'核心离线';
  $('#modeEyebrow').textContent=on?('外部感知 · '+modeLabel((S.llm&&S.llm.mode)||'auto')):'正在连接本机核心';
}

function renderAll(){
  renderShell(); renderOverview(); renderGames(); renderAI(); renderMemory(); renderSettings();
}

function go(page){
  S.page=page;
  $$('.page').forEach(function(p){p.classList.toggle('active',p.id==='page-'+page)});
  $$('.nav-item').forEach(function(b){b.classList.toggle('active',b.getAttribute('data-page')===page)});
  var titles={overview:'总览',games:'游戏',ai:'AI 大脑',memory:'记忆',settings:'设置'};
  $('#topTitle').textContent=titles[page]||'总览';
  if(page==='overview') renderOverview();
  if(page==='games') renderGames();
  if(page==='ai') renderAI();
  if(page==='memory') renderMemory();
  if(page==='settings') renderSettings();
  window.scrollTo({top:0,behavior:'smooth'});
}

async function refresh(){
  if(S.loading) return;
  S.loading=true;
  try{
    var pack=await Promise.all([
      api('/api/instances'),
      api('/api/metrics'),
      api('/api/memory/global'),
      api('/api/llm')
    ]);
    S.instances=(pack[0]&&pack[0].instances)||[];
    S.metrics=pack[1]||null;
    S.prefs=(pack[2]&&pack[2].preferences)||[];
    S.llm=pack[3]||null;
    S.local=(pack[3]&&pack[3].local)||null;
    S.health=S.health||{addr:(S.cfg&&S.cfg.addr)||location.host};
    renderAll();
    refreshHostStatus();
    if(Date.now()-S.eventsAt>5000) refreshEvents();
  }catch(e){
    S.health=null;
    $('#sideState').textContent='核心离线';
    $('#sideDot').className='dot err';
    $('#daemonDot').className='dot err';
  }finally{
    S.loading=false;
  }
}

async function loadStatic(){
  try{
    var pack=await Promise.all([api('/api/health'),api('/api/config')]);
    S.health=pack[0]; S.cfg=pack[1].config; S.profiles=pack[1].profiles||[];
    if(S.cfg&&S.cfg.addr) S.health.addr=S.cfg.addr;
    populateProfiles();
    S.staticLoaded=true;
    renderShell(); renderSettings();
  }catch(e){
    toast('无法连接本机核心：'+e.message,true);
  }
}

async function refreshEvents(){
  S.eventsAt=Date.now();
  var ids=S.instances.slice(0,4).map(function(v){return v.id});
  if(!ids.length){renderEvents();return}
  try{
    var packs=await Promise.all(ids.map(function(id){return api('/api/instances/'+encodeURIComponent(id)+'/events?limit=12').catch(function(){return {events:[]}})}));
    var all=[]; packs.forEach(function(p){all=all.concat((p&&p.events)||[])});
    all.sort(function(a,b){return new Date(b.at||0)-new Date(a.at||0)});
    S.events=all.slice(0,30);
    renderEvents();
  }catch(e){}
}

function populateProfiles(){
  var sel=$('#fProfile'); if(!sel) return;
  sel.innerHTML=(S.profiles||[]).map(function(p){return '<option value="'+esc(p.id)+'">'+esc(p.name||p.id)+'</option>'}).join('');
}

function updateSourceUI(){
  var src=$('#fSource').value;
  $('#androidPickWrap').classList.toggle('hidden',src!=='android');
  $('#launchWrap').classList.toggle('hidden',src!=='android');
  $('#createNote').textContent=src==='android'?'Android 上需要先授予录屏和无障碍权限；悬浮球可随时打开对话。':src==='windows'?'电脑端使用全屏截屏 + SendInput，不注入游戏进程。':'Mock 地下城用于跑通流程和评测，不需要真实游戏。';
  var def=src==='mock'?'mock_dungeon':src==='windows'?'generic':'generic_touch';
  if($('#fProfile').querySelector('option[value="'+def+'"]')) $('#fProfile').value=def;
}

function openModal(profileId){
  if(!S.staticLoaded) loadStatic();
  var src=S.isAndroid?'android':(window.goplayDesktop?'windows':'mock');
  $('#fSource').value=src;
  $('#fName').value=''; $('#fId').value=nextID();
  $('#fEpisodes').value='1'; $('#fAuto').checked=true; $('#fLaunch').checked=true;
  $('#fAndroidGame').innerHTML='<option value="">加载已安装游戏…</option>';
  updateSourceUI();
  if(profileId&&$('#fProfile').querySelector('option[value="'+profileId+'"]')) $('#fProfile').value=profileId;
  $('#modal').classList.remove('hidden');
  if(src==='android') loadAndroidGames();
  setTimeout(function(){$('#fName').focus()},50);
}
function closeModal(){$('#modal').classList.add('hidden')}
function nextID(){return 'game-'+Date.now().toString(36)+Math.floor(Math.random()*900+100).toString(36)}

async function loadAndroidGames(){
  var sel=$('#fAndroidGame'); if(!sel) return;
  if(!window.GoPlayHost||!window.GoPlayHost.installedGames){
    sel.innerHTML='<option value="">原生壳未提供游戏列表，可手动填写名称</option>';
    return;
  }
  try{
    var raw=window.GoPlayHost.installedGames();
    var list=JSON.parse(raw||'[]');
    if(!Array.isArray(list)||!list.length){sel.innerHTML='<option value="">没有找到可启动的游戏</option>';return}
    sel.innerHTML='<option value="">选择已安装游戏…</option>'+list.map(function(g){
      var pkg=g.package||g.packageName||''; var label=g.label||g.name||pkg;
      return '<option value="'+esc(pkg)+'" data-label="'+esc(label)+'">'+esc(label)+' · '+esc(pkg)+'</option>';
    }).join('');
  }catch(e){
    sel.innerHTML='<option value="">读取游戏列表失败：'+esc(e.message)+'</option>';
  }
}

async function createGame(ev){
  ev.preventDefault();
  var src=$('#fSource').value;
  var pkg=$('#fAndroidGame').value;
  var name=$('#fName').value.trim();
  var id=$('#fId').value.trim();
  var profile=$('#fProfile').value;
  if(src==='android'&&!pkg){toast('先选择一个已安装的游戏',true);return}
  if(!name&&pkg){var opt=$('#fAndroidGame').selectedOptions[0];name=opt?opt.getAttribute('data-label')||pkg:''}
  if(!name){toast('给这个游戏起个名字',true);return}
  if(!/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(id)){toast('实例 ID 只能包含字母、数字、下划线和短横线',true);return}
  var body={id:id,name:name,profile_id:profile,source_kind:src,seed:0,max_episodes:Math.max(1,parseInt($('#fEpisodes').value||'1',10)),auto_start:$('#fAuto').checked};
  var btn=$('#createForm button[type="submit"]'); btn.disabled=true; btn.textContent='创建中…';
  try{
    await api('/api/instances',{method:'POST',body:body});
    if(src==='android') hostSetActive(id);
    closeModal();
    toast('已创建，AI 开始接管');
    if(src==='android'&&$('#fLaunch').checked&&window.GoPlayHost&&window.GoPlayHost.launchGame){
      setTimeout(function(){try{window.GoPlayHost.launchGame(pkg)}catch(e){toast('启动游戏失败：'+e.message,true)}},350);
    }
    setTimeout(function(){refresh();go('games')},350);
  }catch(e){toast('创建失败：'+e.message,true)}
  finally{btn.disabled=false;btn.textContent='创建并开始'}
}

async function act(id,action){
  if(action==='delete'){
    if(!confirm('删除这个游戏实例？它的专属记忆也会被删除。')) return;
    try{await api('/api/instances/'+encodeURIComponent(id),{method:'DELETE'});toast('已删除');refresh()}catch(e){toast(e.message,true)}
    return;
  }
  var path=action==='start'?'start':action==='stop'?'stop':action==='pause'?'pause':'resume';
  if((action==='start'||action==='resume')&&window.GoPlayHost) hostSetActive(id);
  try{
    await api('/api/instances/'+encodeURIComponent(id)+'/'+path,{method:'POST'});
    toast(action==='start'?'开始自动游玩':action==='stop'?'已停止':action==='pause'?'已暂停':'已继续');
    setTimeout(refresh,250);
  }catch(e){toast(e.message,true)}
}

async function sendChat(id){
  var ta=$('[data-input="'+id+'"]'); if(!ta) return;
  var text=ta.value.trim(); if(!text) return;
  var btn=$('[data-send="'+id+'"]'); if(btn) btn.disabled=true;
  try{
    await api('/api/instances/'+encodeURIComponent(id)+'/message',{method:'POST',body:{text:text}});
    ta.value=''; toast('已进入指令缓冲，下一个决策周期生效');
  }catch(e){toast(e.message,true)}
  finally{if(btn) btn.disabled=false}
}

function appendText(el,text){
  if(!el) return;
  el.value=(el.value?el.value+' ':'')+text;
  el.focus();
}

function startVoice(id){
  S.voiceTarget=id;
  if(window.GoPlayHost&&window.GoPlayHost.startVoice){
    try{window.GoPlayHost.startVoice();toast('请说话…');return}catch(e){}
  }
  var SR=window.SpeechRecognition||window.webkitSpeechRecognition;
  if(!SR){toast('当前环境不支持语音输入，请用键盘输入',true);return}
  try{
    var rec=new SR(); rec.lang='zh-CN'; rec.interimResults=false; rec.maxAlternatives=1;
    rec.onresult=function(ev){
      var text='';
      for(var i=ev.resultIndex;i<ev.results.length;i++) text+=ev.results[i][0].transcript;
      var ta=$('[data-input="'+S.voiceTarget+'"]');
      appendText(ta,text.trim()); toast('已识别');
    };
    rec.onerror=function(){toast('语音识别失败，请用键盘输入',true)};
    rec.start(); toast('请说话…');
  }catch(e){toast('语音启动失败：'+e.message,true)}
}

window.goplayVoice=function(text){
  if(!S.voiceTarget) return;
  var ta=$('[data-input="'+S.voiceTarget+'"]');
  appendText(ta,String(text||'').trim());
  toast('已识别语音');
};

async function saveAPI(ev){
  ev.preventDefault();
  var base=$('#apiBase').value.trim();
  var model=$('#apiModel').value.trim();
  if(!base||!model){toast('接口地址和模型名不能为空',true);return}
  var body={mode:'api',base_url:base,model:model,temperature:Number($('#apiTemp').value||'0.2'),max_tokens:parseInt($('#apiTokens').value||'512',10),vision:$('#apiVision').checked};
  var key=$('#apiKey').value.trim(); if(key) body.api_key=key;
  try{
    var res=await api('/api/llm',{method:'POST',body:body});
    S.llm=res; $('#apiKey').value=''; S.visionDirty=false;
    toast('API 已保存并启用'); renderAI();
  }catch(e){toast('保存失败：'+e.message,true)}
}

async function testAPI(){
  var b=$('#apiTest'); b.disabled=true; b.textContent='测试中…';
  try{
    var r=await api('/api/llm/test',{method:'POST',body:{}});
    toast('连接正常 · '+r.model+' · '+fmtMS(r.latency_ms)+' ms');
  }catch(e){toast('连接失败：'+e.message,true)}
  finally{b.disabled=false;b.textContent='测试连接'}
}

async function clearAPI(){
  if(!confirm('清除保存在本机的 API Key？本机模型不受影响。')) return;
  try{
    var mode=(S.llm&&S.llm.mode)||'api';
    var res=await api('/api/llm',{method:'POST',body:{mode:mode==='auto'?'auto':mode,clear_key:true}});
    S.llm=res; toast('已清除 API Key'); renderAI();
  }catch(e){toast(e.message,true)}
}

async function enableLocal(){
  try{
    var res=await api('/api/llm',{method:'POST',body:{mode:'local'}});
    S.llm=res; toast('已切换本机 AI，正在下载/启动…'); renderAI();
    setTimeout(refresh,1200); setTimeout(refresh,4000);
  }catch(e){toast(e.message,true)}
}
async function stopLocal(){
  try{await api('/api/local/stop',{method:'POST'});toast('本机模型已停止');setTimeout(refresh,300)}catch(e){toast(e.message,true)}
}
async function downloadModel(){
  try{await api('/api/local/download',{method:'POST'});toast('开始下载策略模型');setTimeout(refresh,800)}catch(e){toast(e.message,true)}
}
async function cancelDownload(){
  try{await api('/api/local/cancel',{method:'POST'});toast('已请求取消下载');setTimeout(refresh,500)}catch(e){toast(e.message,true)}
}

async function addPref(ev){
  ev.preventDefault();
  var text=$('#prefText').value.trim(); if(!text){toast('写一句偏好再提交',true);return}
  var w=$('#prefWeight').value;
  var body={text:text,weights:{}}; body.weights[w]=1;
  try{
    await api('/api/memory/global/preferences',{method:'POST',body:body});
    $('#prefText').value=''; toast('已写入跨游戏偏好'); refresh();
  }catch(e){toast(e.message,true)}
}
async function deletePref(id){
  try{await api('/api/memory/global/preferences/'+encodeURIComponent(id),{method:'DELETE'});toast('已删除偏好');refresh()}catch(e){toast(e.message,true)}
}

function hostSetActive(id){
  if(!window.GoPlayHost||!window.GoPlayHost.setActiveInstance) return true;
  try{
    var res=JSON.parse(window.GoPlayHost.setActiveInstance(id)||'{}');
    if(res.ok===false){toast('切换当前游戏失败',true);return false}
    return true;
  }catch(e){return false}
}

function hostStatus(){
  if(!window.GoPlayHost||!window.GoPlayHost.statusJson) return null;
  try{return JSON.parse(window.GoPlayHost.statusJson()||'{}')}catch(e){return null}
}

function refreshHostStatus(){
  var panel=$('#androidPermPanel'); if(!panel) return;
  var s=hostStatus();
  panel.classList.toggle('hidden',!s);
  if(!s) return;
  $('#andCore').textContent=s.coreReady?('运行中 · 127.0.0.1:'+s.corePort):(s.coreError?'启动失败':'启动中');
  $('#andCapture').textContent=s.captureRunning?((s.capturePaused?'已暂停':'运行中')+(s.captureWidth?' · '+s.captureWidth+'×'+s.captureHeight:'')):'未开始';
  $('#andAccess').textContent=s.accessibilityReady?'已开启':'未开启（AI 无法点击/滑动）';
  $('#andOverlay').textContent=s.overlayPermission?'已授权':'未授权（没有悬浮球）';
  $('#andNotif').textContent=s.notificationPermission?'已授权':'未授权';
  $('#andInstance').textContent=s.activeInstance||'—';
}

function hostCall(method,okMessage){
  if(!window.GoPlayHost||typeof window.GoPlayHost[method]!=='function'){
    toast('当前环境没有这个原生功能',true);return;
  }
  try{window.GoPlayHost[method]();if(okMessage)toast(okMessage)}catch(e){toast('原生调用失败：'+e.message,true)}
}

function openOverlay(){
  if(window.GoPlayHost&&window.GoPlayHost.openOverlay){try{window.GoPlayHost.openOverlay();return}catch(e){}}
  if(window.goplayDesktop){window.goplayDesktop('overlay');return}
  window.open('/overlay','goplay-overlay','width=430,height=660');
}

function bind(){
  document.addEventListener('click',function(ev){
    var page=ev.target.closest('[data-page]');
    if(page){ev.preventDefault();go(page.getAttribute('data-page'));return}
    var ng=ev.target.closest('[data-new-game]');
    if(ng){openModal();return}
    var filt=ev.target.closest('[data-filter]');
    if(filt){S.filter=filt.getAttribute('data-filter');$$('.chip[data-filter]').forEach(function(b){b.classList.toggle('active',b===filt)});renderGames();return}
    var actBtn=ev.target.closest('[data-act]');
    if(actBtn){act(actBtn.getAttribute('data-id'),actBtn.getAttribute('data-act'));return}
    var got=ev.target.closest('[data-goto]');
    if(got){go('games');var id=got.getAttribute('data-goto');setTimeout(function(){var c=document.querySelector('[data-card="'+id+'"]');if(c)c.scrollIntoView({behavior:'smooth',block:'center'})},100);return}
    var send=ev.target.closest('[data-send]');
    if(send){sendChat(send.getAttribute('data-send'));return}
    var mic=ev.target.closest('[data-mic]');
    if(mic){startVoice(mic.getAttribute('data-mic'));return}
    var prof=ev.target.closest('[data-profile]');
    if(prof){openModal(prof.getAttribute('data-profile'));return}
    var pref=ev.target.closest('[data-pref]');
    if(pref){deletePref(pref.getAttribute('data-pref'));return}
    var mem=ev.target.closest('[data-mem]');
    if(mem){go('memory');return}
    if(ev.target.closest('#overlayBtn,#overlayBtn2')){openOverlay();return}
    if(ev.target.closest('#newBtn')){openModal();return}
    if(ev.target.closest('#closeModal,#cancelCreate')){closeModal();return}
    if(ev.target.id==='modal'){closeModal();return}
    if(ev.target.closest('#refreshGames')){loadAndroidGames();return}
    if(ev.target.closest('#apiClear')){clearAPI();return}
    if(ev.target.closest('#apiTest')){testAPI();return}
    if(ev.target.closest('#localStart')){enableLocal();return}
    if(ev.target.closest('#localStop')){stopLocal();return}
    if(ev.target.closest('#localRefresh')){refresh();return}
    var dl=ev.target.closest('[data-dl]'); if(dl){downloadModel();return}
    if(ev.target.closest('[data-cancel]')){cancelDownload();return}
    if(ev.target.closest('#apiVision')){S.visionDirty=true;return}
    if(ev.target.closest('#desktopData')){try{window.goplayDesktop('data')}catch(e){}return}
    if(ev.target.closest('#desktopQuit')){try{window.goplayDesktop('quit')}catch(e){}return}
    if(ev.target.closest('#reconnectBtn')){loadStatic();refresh();toast('正在重新连接');return}
    if(ev.target.closest('#androidCapture')){hostCall('startCapture','如果弹出录屏授权，请点「立即开始」');return}
    if(ev.target.closest('#androidStopCapture')){hostCall('stopCapture','已请求停止截屏');return}
    if(ev.target.closest('#androidAccessibility')){hostCall('openAccessibility');return}
    if(ev.target.closest('#androidOverlayPerm')){hostCall('openOverlaySettings');return}
    if(ev.target.closest('#androidNotification')){hostCall('requestNotification');return}
    if(ev.target.closest('#androidAppSettings')){hostCall('openAppSettings');return}
  });
  document.addEventListener('focusin',function(ev){if(ev.target.closest('#gameList')){S.suspendGames=true}});
  document.addEventListener('focusout',function(ev){
    if(!ev.target.closest('#gameList')) return;
    setTimeout(function(){
      var a=document.activeElement;
      if(!a||!a.closest||!a.closest('#gameList')){S.suspendGames=false;renderGames()}
    },160);
  });
  document.addEventListener('keydown',function(ev){
    if(ev.key==='Escape'){closeModal();return}
    if(ev.target.matches&&ev.target.matches('[data-input]')){
      if(ev.key==='Enter'&&!ev.shiftKey){ev.preventDefault();sendChat(ev.target.getAttribute('data-input'))}
    }
  });
  $('#fSource').addEventListener('change',function(){updateSourceUI();if(this.value==='android')loadAndroidGames()});
  $('#fAndroidGame').addEventListener('change',function(){
    var opt=this.selectedOptions[0]; if(!opt||!this.value) return;
    var label=opt.getAttribute('data-label')||this.value;
    $('#fName').value=label;
    if(!$('#fId').value||$('#fId').value.indexOf('game-')===0) $('#fId').value=nextID();
  });
  $('#createForm').addEventListener('submit',createGame);
  $('#apiForm').addEventListener('submit',saveAPI);
  $('#prefForm').addEventListener('submit',addPref);
}

function connectEvents(){
  if(!window.WebSocket) return;
  var proto=location.protocol==='https:'?'wss':'ws';
  try{
    var ws=new WebSocket(proto+'://'+location.host+'/ws/events');
    S.ws=ws;
    ws.onopen=function(){};
    ws.onmessage=function(ev){
      try{
        var e=JSON.parse(ev.data);
        S.events.unshift(e); if(S.events.length>30) S.events.length=30;
        renderEvents();
      }catch(err){}
    };
    ws.onclose=function(){setTimeout(connectEvents,2500)};
    ws.onerror=function(){try{ws.close()}catch(e){}};
  }catch(e){}
}

async function init(){
  S.isAndroid=/Android/i.test(navigator.userAgent||'');
  bind();
  await loadStatic();
  await refresh();
  connectEvents();
  S.poll=setInterval(refresh,2500);
}

init();
})();