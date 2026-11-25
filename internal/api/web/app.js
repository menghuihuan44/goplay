(function(){
'use strict';
var S={
  page:'overview', instances:[], metrics:null, prefs:[], profiles:[], llm:null, local:null,
  cfg:null, health:null, events:[], filter:'all', loading:false, suspendGames:false,
  voiceTarget:null, voice:null, eventsAt:0, ws:null, poll:null, staticLoaded:false,
  visionDirty:false, isAndroid:false, host:null, aiRoutes:null, routeDraft:null, diag:{}, diagAt:{},
  diagBusy:{}, starting:{}, pkgs:{}, frameNonce:0, teach:{}, teachId:null
};
var $=function(sel,root){return (root||document).querySelector(sel)};
var $$=function(sel,root){return Array.prototype.slice.call((root||document).querySelectorAll(sel))};
var ROLES=[
  {key:'perception',label:'感知',desc:'看画面、OCR、识别状态'},
  {key:'analysis',label:'分析',desc:'理解局面、归纳发生了什么'},
  {key:'strategy',label:'决策',desc:'在事件边界选择动作'},
  {key:'language',label:'对话',desc:'聊天、解释、语音回复'},
  {key:'memory',label:'记忆',desc:'压缩、摘要、检索长期知识'},
  {key:'validation',label:'校验',desc:'检查动作是否合法安全'}
];
var ROLE_LABEL={perception:'感知',analysis:'分析',strategy:'决策',language:'对话',memory:'记忆',validation:'校验',validator:'校验'};

function esc(v){
  return String(v===undefined||v===null?'':v)
    .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
    .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}
function num(v,d){v=Number(v);return isFinite(v)?v:(d||0)}
function pct(v){
  if(v===undefined||v===null) return null;
  v=Number(v); if(!isFinite(v)) return null;
  return Math.max(0,Math.min(100, v<=1 ? v*100 : v));
}
function fmtPct(v){var p=pct(v);return p===null?'—':Math.round(p)+'%'}
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
function sleep(ms){return new Promise(function(r){setTimeout(r,ms)})}
function toast(msg,isErr){
  var t=$('#toast'); if(!t) return;
  t.textContent=msg; t.className='toast show'+(isErr?' err':'');
  clearTimeout(toast._timer);
  toast._timer=setTimeout(function(){t.className='toast'+(isErr?' err':'')},3200);
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
    var err=new Error(msg); err.status=res.status; throw err;
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
  if(v&&v.waiting&&s==='running') return {key:'running',label:'等待画面'};
  if(s==='human_control') return {key:'human',label:'人工接管'};
  if(s==='running') return {key:'running',label:'AUTO'};
  if(s==='paused') return {key:'paused',label:'已暂停'};
  if(s==='starting') return {key:'running',label:'启动中'};
  if(s==='error') return {key:'error',label:'需处理'};
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
  return ({risk_aversion:'更谨慎',aggression:'更激进',exploration:'偏好探索',resource_aversion:'节省资源'})[k]||k;
}
function vals(v){return (v&&v.state&&v.state.values)||{}}
function stat(v,k,d){var s=(v&&v.stats)||{};return num(s[k],d)}

function displayOf(v){
  var d=(v&&v.display)||{};
  var fields=Array.isArray(d.fields)?d.fields.slice(0):[];
  if(!fields.length){
    var vs=vals(v), tx=(v&&v.state&&v.state.text)||{};
    Object.keys(vs).slice(0,8).forEach(function(k){
      fields.push({key:k,label:k,value:vs[k],kind:'number'});
    });
    Object.keys(tx).slice(0,4).forEach(function(k){
      if(fields.length>=10) return;
      fields.push({key:k,label:k,value:tx[k],kind:'text'});
    });
  }
  var summary=d.summary||(v&&v.state&&v.state.summary)||'';
  var tags=Array.isArray(d.tags)?d.tags:((v&&v.state&&v.state.tags)||[]);
  return {
    summary:summary, fields:fields, tags:tags,
    outcome:(d.outcome===undefined?null:d.outcome),
    progress:(d.progress===undefined?null:d.progress),
    progressLabel:d.progress_label||'进度',
    notes:d.notes||'',
    lastAction:d.last_action||((v&&v.stats&&v.stats.last_action)||''),
    lastReason:d.last_reason||((v&&v.decision&&v.decision.reason)||'')
  };
}
function fieldValue(f){
  var v=f&&f.value;
  if(v===undefined||v===null||v==='') return '—';
  if(typeof v==='number'){
    if(f.kind==='percent') return fmtPct(v);
    return (Math.round(v*10)/10).toString();
  }
  return String(v);
}
function highlightFields(disp,n){
  var list=disp.fields.filter(function(f){return f&&f.highlight});
  var rest=disp.fields.filter(function(f){return f&&!f.highlight});
  return list.concat(rest).slice(0,n);
}

function errorRaw(v){
  return (v&&v.stats&&v.stats.last_error)||(v&&v.last_error)||'';
}
function errorActive(v){
  if(v&&v.waiting&&statusInfo(v).key!=='error') return false;
  var d=S.diag[v.id];return statusInfo(v).key==='error'||!!errorRaw(v)||(d&&d.ok===false&&d.code&&d.code!=='ok')
}
function ensureDiag(id,force){
  if(S.diagBusy[id]) return;
  if(!force&&S.diag[id]&&Date.now()-num(S.diagAt[id],0)<10000) return;
  if(!force&&S.diagAt[id]&&Date.now()-num(S.diagAt[id],0)<25000&&S.diagErr&&S.diagErr[id]) return;
  S.diagBusy[id]=true; S.diagAt[id]=Date.now();
  api('/api/instances/'+encodeURIComponent(id)+'/diagnostics').then(function(d){
    S.diag[id]=d||{}; delete S.diagErr[id];
  }).catch(function(e){
    S.diagErr=S.diagErr||{}; S.diagErr[id]=e.message;
  }).then(function(){
    delete S.diagBusy[id];
    if(S.page==='games') renderGames(); else if(S.page==='overview') renderOverview();
  });
}
function diagText(v){
  var d=S.diag[v.id]||{};
  return ['GoPlay 诊断','实例: '+(v.name||v.id),'状态: '+v.status,'错误: '+(d.detail||d.error||errorRaw(v)),'代码: '+(d.code||''),'日志: '+(d.log_path||''),'时间: '+new Date().toISOString()].join('\n');
}
function copyDiag(v){
  var text=diagText(v);
  if(navigator.clipboard&&navigator.clipboard.writeText){
    navigator.clipboard.writeText(text).then(function(){toast('诊断信息已复制')}).catch(function(){toast('复制失败，请长按选择文字',true)});
  }else{
    var ta=document.createElement('textarea'); ta.value=text; document.body.appendChild(ta); ta.select();
    try{document.execCommand('copy');toast('诊断信息已复制')}catch(e){toast('复制失败',true)}
    ta.remove();
  }
}
function diagActions(v){
  var d=S.diag[v.id]||{};
  var list=Array.isArray(d.actions)?d.actions:[];
  var out=[];
  list.forEach(function(a){
    if(typeof a==='string') out.push({id:a,label:({open_permissions:'去授权',start_capture:'开始截屏',retry_start:'重新开始',open_ai:'AI 设置',release_human:'交还 AI',open_logs:'复制诊断'})[a]||a});
    else if(a&&a.id) out.push({id:a.id,label:a.label||a.id});
  });
  if(!out.length){
    if(d.code==='android_no_client'||d.code==='android_ack_timeout'||d.code==='capture_failed') out=[{id:'open_permissions',label:'去授权'},{id:'start_capture',label:'开始截屏'},{id:'retry_start',label:'重新开始'}];
    else if(d.code==='llm_unavailable'||d.code==='model_missing') out=[{id:'open_ai',label:'AI 设置'}];
    else if(d.code==='external_control') out=[{id:'release_human',label:'交还 AI'}];
    else out=[{id:'retry_start',label:'重新开始'},{id:'open_permissions',label:'检查权限'}];
  }
  return out;
}
function errorCard(v){
  var d=S.diag[v.id]||{};
  var raw=errorRaw(v);
  var title=d.title||'AI 停下来了';
  var detail=d.detail||d.summary||raw||'没有拿到错误详情。可以重新开始，或复制诊断信息排查。';
  var checks=Array.isArray(d.checks)?d.checks:[];
  var suggestions=Array.isArray(d.suggestions)?d.suggestions:[];
  var html='<div class="diag-card">'
    +'<div class="diag-head"><span class="diag-icon">!</span><div><b>'+esc(title)+'</b><small>'+esc(d.code||'unknown')+'</small></div><button class="link-btn" data-copy="'+esc(v.id)+'">复制诊断</button></div>'
    +'<p class="diag-detail">'+esc(detail)+'</p>';
  if(checks.length){
    html+='<div class="diag-checks">'+checks.map(function(c){
      var ok=c.ok===true; var name=c.name||c.label||c.key||'检查项';
      var val=c.detail||c.value||'';
      return '<div class="diag-check '+(ok?'ok':'bad')+'"><span>'+(ok?'✓':'✗')+'</span><div><b>'+esc(name)+'</b>'+(val?'<small>'+esc(val)+'</small>':'')+'</div></div>';
    }).join('')+'</div>';
  }
  if(suggestions.length){
    html+='<ul class="diag-suggest">'+suggestions.slice(0,5).map(function(s){
      var t=typeof s==='string'?s:(s.text||s.label||'');
      return '<li>'+esc(t)+'</li>';
    }).join('')+'</ul>';
  }
  var acts=diagActions(v);
  html+='<div class="diag-actions">'+acts.map(function(a){
    return '<button class="pill-btn" data-diagact="'+esc(a.id)+'" data-id="'+esc(v.id)+'">'+esc(a.label)+'</button>';
  }).join('')+'<button class="link-btn" data-diagload="'+esc(v.id)+'">重新诊断</button></div>';
  if(!S.diag[v.id]&&!S.diagBusy[v.id]) html+='<p class="form-note">正在分析原因…</p>';
  if(raw) html+='<details class="diag-raw"><summary>原始错误</summary><pre>'+esc(raw)+'</pre></details>';
  html+='</div>';
  ensureDiag(v.id,false);
  return html;
}

function fieldChips(disp,n){
  var f=highlightFields(disp,n||3);
  if(!f.length) return '<span class="muted small">还没识别出属性</span>';
  return f.map(function(x){
    return '<span class="field-chip"><span>'+esc(x.label||x.key)+'</span><b>'+esc(fieldValue(x))+'</b></span>';
  }).join('');
}
function compactCard(v){
  var st=statusInfo(v), disp=displayOf(v), progress=pct(disp.progress);
  var action=disp.lastAction||((v.decision&&v.decision.action&&v.decision.action.kind)||'等待决策');
  var outcome=disp.outcome?('<span class="outcome">'+esc(disp.outcome)+'</span>'):'';
  return '<article class="icard" data-card="'+esc(v.id)+'">'
    +'<div class="icard-top"><span class="dot '+(st.key==='running'?'on':'')+'"></span><h3>'+esc(v.name||v.id)+'</h3>'+outcome+'<span class="status '+st.key+'">'+st.label+'</span></div>'
    +'<div class="icard-sub">'+esc(v.profile||'')+' · '+esc(v.id)+'</div>'
    +(errorActive(v)?'<div class="icard-error">'+esc((errorRaw(v)||'需要处理').slice(0,110))+'<button class="link-btn" data-goto="'+esc(v.id)+'">看原因</button></div>':'')
    +'<div class="icard-fields">'+fieldChips(disp,3)+'</div>'
    +(progress!==null?'<div class="progline"><span>'+esc(disp.progressLabel)+'</span><b>'+fmtPct(progress)+'</b></div>':'')
    +'<div class="action-line"><b>'+esc(action)+'</b>'+(disp.lastReason?' · '+esc(disp.lastReason):'')+'</div>'
    +'<div class="icard-actions">'
      +(st.key==='running'?'<button class="pill-btn" data-act="pause" data-id="'+esc(v.id)+'">暂停</button><button class="pill-btn" data-act="stop" data-id="'+esc(v.id)+'">停止</button>':'<button class="pill-btn" data-act="start" data-id="'+esc(v.id)+'">开始</button>')
      +'<button class="pill-btn" data-goto="'+esc(v.id)+'">打开</button>'
    +'</div></article>';
}
function screenPanel(v){
  var url='/api/instances/'+encodeURIComponent(v.id)+'/frame?t='+Math.floor(Date.now()/3000);
  return '<div class="screen-wrap">'
    +'<div class="screen-head"><span>AI 看到的画面</span><span class="muted small">最近一帧 · 低帧率预览</span></div>'
    +'<img class="game-screen" alt="AI 画面预览" src="'+url+'" onload="this.parentNode.classList.add(\'hasimg\')" onerror="this.parentNode.classList.add(\'noimg\')">'
    +'<div class="screen-empty">还没有画面。检查录屏权限，并保持截屏运行。<br><button class="link-btn" data-perm="capture">开始截屏</button></div>'
    +'</div>';
}
function teachPanel(v){
  var t=(v&&v.teaching)||{};
  var steps=num(t.steps,0), demos=num(t.demos,0);
  if(t.recording){
    return '<div class="teach-live"><span class="teach-eye">◉</span><div><b>AI 正在看你怎么玩</b><small>已记录 '+steps+' 步画面与输入；你操作结束后会整理成示范，之后在相似画面复用。</small></div><button class="link-btn" data-teach="'+esc(v.id)+'">查看</button></div>';
  }
  if(steps>0||demos>0){
    var used=num(t.uses,0), hits=num(t.hints,0), sim=num(t.hint_similarity,0);
    var usedText=used>0?('AI 已模仿 '+used+' 次'+(hits>0?'/命中相似示范 '+hits+' 次':'')+(sim>0?'（最近相似度 '+Math.round(sim*100)+'%）':'')+'。'):'';
    return '<div class="teach-live learned"><span>🎓</span><div><b>已学会 '+demos+' 段示范 · '+steps+' 步</b><small>'+esc(usedText+(t.last_note||'在相似画面会优先模仿你的操作，再根据结果调整。'))+'</small></div><button class="link-btn" data-teach="'+esc(v.id)+'">查看</button></div>';
  }
  if(v.status==='human_control'){
    return '<div class="teach-live"><span>👀</span><div><b>AI 正在观察你的操作</b><small>记录画面、按键和点击；你放开键鼠后 AI 继续，并复用刚学到的动作。</small></div></div>';
  }
  return '';
}
function teachButton(v){
  var t=(v&&v.teaching)||{}; var n=num(t.steps,0);
  if(!n&&!num(t.demos,0)&&!t.recording&&v.status!=='human_control') return '';
  return '<button class="pill-btn" data-teach="'+esc(v.id)+'">🎓 示范 '+n+'</button>';
}
function teachImage(v,rel){
  if(!rel) return '';
  return '/api/instances/'+encodeURIComponent(v.id)+'/teach/frame?path='+encodeURIComponent(rel);
}
function demoStepHTML(v,step){
  var before=teachImage(v,step.before_image), after=teachImage(v,step.after_image);
  var action=step.action||{};
  var label=action.kind||'动作';
  if(action.input&&action.input.key) label=action.input.key+' 键';
  if(action.input&&action.input.type==='click') label='点击 ('+Math.round(num(action.input.x,0)*100)+'%, '+Math.round(num(action.input.y,0)*100)+'%)';
  return '<div class="teach-step">'
    +'<div class="teach-shots">'
      +(before?'<figure><img loading="lazy" src="'+before+'" alt="操作前画面"><figcaption>操作前</figcaption></figure>':'')
      +(after?'<figure><img loading="lazy" src="'+after+'" alt="操作后画面"><figcaption>操作后</figcaption></figure>':'')
    +'</div>'
    +'<div class="teach-step-text"><b>'+esc(label)+'</b><p>'+esc(step.note||'')+'</p><small>意图：'+esc(step.intent||'操作')+(step.similarity?' · 相似度 '+Math.round(num(step.similarity,0)*100)+'%':'')+'</small></div>'
  +'</div>';
}
function renderTeaching(id){
  var info=S.teach[id]||{status:{},demos:[]}, t=info.status||{}, demos=Array.isArray(info.demos)?info.demos:[];
  var body=$('#teachBody'); if(!body) return;
  var used=num(t.uses,0), hits=num(t.hints,0), sim=num(t.hint_similarity,0);
  var statusLine=t.recording?('正在记录：已 '+num(t.steps,0)+' 步'):('你接管游戏时，AI 会记录画面、按键和点击。'+(used>0?(' 已模仿 '+used+' 次/命中 '+hits+' 次'+(sim>0?'（相似度 '+Math.round(sim*100)+'%）':'')+'。'):''));
  var html='<div class="teach-status"><div><b>'+(t.enabled!==false?'学习已开启':'学习已关闭')+'</b><small>'+esc(statusLine)+'</small></div>'
    +'<button class="pill-btn" data-teachtoggle="'+esc(id)+'">'+(t.enabled!==false?'暂停学习':'开启学习')+'</button>'
    +'<button class="pill-btn danger-text" data-teachclear="'+esc(id)+'">清空示范</button></div>';
  if(t.last_note) html+='<p class="teach-note">'+esc(t.last_note)+'</p>';
  if(!demos.length){
    html+='<div class="empty">还没有示范。进入游戏后接管一会儿，或让别人替你玩几步；AI 会把画面变化和按键记录下来。</div>';
  }else{
    demos=demos.slice().reverse();
    html+='<div class="teach-demos">'+demos.map(function(d){
      var steps=Array.isArray(d.steps)?d.steps:[];
      var shown=steps.slice(-8);
      return '<section class="teach-demo"><div class="teach-demo-head"><div><b>'+esc(d.summary||'一段人工示范')+'</b><small>'+esc(d.source||'human-takeover')+' · '+steps.length+' 步 · '+esc(ago(d.ended_at||d.started_at))+'</small></div></div>'
        +(shown.length?shown.map(function(st){return demoStepHTML({id:id},st)}).join(''):'<p class="muted small">这段示范没有记录到动作。</p>')
      +'</section>';
    }).join('')+'</div>';
  }
  body.innerHTML=html;
}
async function openTeaching(id){
  S.teachId=id;
  var m=$('#teachModal'); if(!m) return;
  m.classList.remove('hidden');
  $('#teachBody').innerHTML='<div class="muted">正在读取示范…</div>';
  try{
    var info=await api('/api/instances/'+encodeURIComponent(id)+'/teach');
    S.teach[id]=info; renderTeaching(id);
  }catch(e){ $('#teachBody').innerHTML='<div class="empty">读取失败：'+esc(e.message)+'</div>'; }
}
function closeTeaching(){ $('#teachModal').classList.add('hidden'); S.teachId=null; }
async function toggleTeaching(id){
  var info=S.teach[id]||{}, t=info.status||{};
  try{
    var next=await api('/api/instances/'+encodeURIComponent(id)+'/teach',{method:'POST',body:{enabled:t.enabled===false}});
    S.teach[id]=next; renderTeaching(id); toast(next.status&&next.status.enabled!==false?'已开启示范学习':'已暂停示范学习');
  }catch(e){toast(e.message,true)}
}
async function clearTeaching(id){
  if(!window.confirm('清空这个游戏学到的示范？已经沉淀到记忆里的经验不会删除。')) return;
  try{ await api('/api/instances/'+encodeURIComponent(id)+'/teach',{method:'DELETE'}); toast('示范已清空'); await openTeaching(id); }
  catch(e){toast(e.message,true)}
}
function tracePanel(v){
  var tr=(v.trace||[]).slice(-8); if(!tr.length) return '';
  var label={see:'看画面',think:'选动作',directive:'听指令',act:'按下去',result:'看结果',wait:'等待',resume:'恢复',recover:'自动修复'};
  var rows=tr.map(function(e){
    var d=e.data||{}, extra='';
    if(e.phase==='see'){ if(d.frame_delta!==undefined) extra='变化 '+(num(d.frame_delta,0)*100).toFixed(1)+'%'; }
    else if(e.phase==='think'){ extra=(d.source?'来源 '+d.source:'')+(d.reason?' · '+d.reason:''); }
    else if(e.phase==='act'){ var inp=d.input||{}; extra=(inp.type||'')+(inp.key?' '+inp.key:'')+(inp.x!==undefined?' @'+(num(inp.x,0)*100).toFixed(0)+'%,'+(num(inp.y,0)*100).toFixed(0)+'%':''); }
    else if(e.phase==='result'){ extra='奖励 '+(num(d.reward,0)).toFixed(2)+(d.effective===false?' · 无有效变化':' · 有效')+(d.terminal?' · 终局':''); }
    else if(e.phase==='wait'||e.phase==='resume'){ extra=(d.waited_ms?Math.round(d.waited_ms)+' ms':''); }
    return '<div class="trace-row '+e.phase+'"><span class="trace-phase">'+(label[e.phase]||e.phase)+'</span><span class="trace-msg">'+esc(e.message||'')+'</span>'+(extra?'<span class="trace-extra">'+esc(extra)+'</span>':'')+'</div>';
  }).join('');
  return '<details class="trace-box" open><summary>AI 思维链 · 画面 → 选择 → 按键 → 结果 <span class="muted small">（事实记录，不是隐藏推理）</span></summary>'+rows+'</details>';
}
function fullCard(v){
  var st=statusInfo(v), disp=displayOf(v), stats=v.stats||{}, memory=v.memory||{};
  var progress=pct(disp.progress);
  var running=st.key==='running';
  var action=disp.lastAction||((v.decision&&v.decision.action&&v.decision.action.kind)||'等待决策');
  var dec=v.decision||{};
  var chat=(v.chat||[]).slice(-4).map(function(m){
    return '<div class="chat-line '+(m.role==='user'?'user':'ai')+'"><b>'+(m.role==='user'?'你':'AI')+'</b><span>'+esc(m.text)+'</span></div>';
  }).join('');
  var controls='';
  if(running){
    controls='<button class="pill-btn" data-act="pause" data-id="'+esc(v.id)+'">暂停</button><button class="pill-btn" data-act="stop" data-id="'+esc(v.id)+'">停止</button>';
  }else if(st.key==='human'){
    controls='<button class="pill-btn" data-act="release" data-id="'+esc(v.id)+'">交还 AI</button><button class="pill-btn" data-act="resume" data-id="'+esc(v.id)+'">恢复 AI</button><button class="pill-btn" data-act="stop" data-id="'+esc(v.id)+'">停止</button>';
  }else if(st.key==='paused'){
    controls='<button class="pill-btn" data-act="resume" data-id="'+esc(v.id)+'">继续</button><button class="pill-btn" data-act="stop" data-id="'+esc(v.id)+'">停止</button>';
  }else{
    controls='<button class="pill-btn" data-act="start" data-id="'+esc(v.id)+'">'+(S.starting[v.id]?'准备中…':'开始')+'</button>';
  }
  var outcome=disp.outcome?'<span class="outcome">'+esc(disp.outcome)+'</span>':'';
  var tags=(disp.tags||[]).slice(0,6).map(function(t){return '<span class="tag">'+esc(t)+'</span>'}).join('');
  var notes=disp.notes?'<p class="gc-notes">'+esc(disp.notes)+'</p>':'';
  if(st.key==='running'||st.key==='human'||st.key==='error') ensureDiag(v.id,false);
  var fields=disp.fields.slice(0,10).map(function(f){
    return '<div class="dyn-field '+(f.highlight?'hl':'')+'"><span>'+esc(f.label||f.key)+'</span><b>'+esc(fieldValue(f))+'</b></div>';
  }).join('');
  var voice=S.voice&&S.voice.id===v.id;
  return '<article class="game-card" data-card="'+esc(v.id)+'">'
    +'<div class="gc-head"><div style="min-width:0;flex:1"><h3>'+esc(v.name||v.id)+'</h3><div class="gc-meta">'+sourceIcon(v.source_kind)+' '+esc(v.profile||'')+' · seed '+esc(v.seed||0)+'</div></div>'+outcome+'<span class="status '+st.key+'">'+st.label+'</span></div>'
    +(errorActive(v)?errorCard(v):'')
    +(v.waiting?'<p class="gc-notes wait-note">AI 正在等待：'+esc(v.wait_reason||'连接暂时中断')+'。核心保持运行并自动恢复，恢复后会继续操作；这不是动作错误。</p>':'')
    +teachPanel(v)
    +'<div class="gc-summary"><span class="dot '+(running?'on':'')+'"></span><span>'+esc(disp.summary||'等待第一帧…')+'</span></div>'
    +(progress!==null?'<div class="progress"><div class="labels"><span>'+esc(disp.progressLabel)+'</span><b>'+fmtPct(progress)+'</b></div><div class="bar"><i style="width:'+fmtPct(progress)+'%"></i></div></div>':'')
    +'<div class="gc-stats mini-stats"><div><span>动作</span><b>'+Math.round(stat(v,'actions',0))+'</b></div><div><span>有效动作</span><b class="'+(stat(v,'effective_actions',0)?'lime':'')+'">'+Math.round(stat(v,'effective_actions',0))+'</b></div><div><span>决策</span><b>'+Math.round(stat(v,'decisions',0))+'</b></div><div><span>错误</span><b class="'+(stat(v,'errors',0)?'danger-text':'')+'">'+Math.round(stat(v,'errors',0))+'</b></div></div>'
    +(fields?'<div class="dyn-grid">'+fields+'</div>':'<p class="form-note">AI 还没有从这个画面里识别出可展示的属性；它会继续观察和学习。</p>')
    +(tags?'<div class="gc-tags">'+tags+'</div>':'')
    +screenPanel(v)
    +'<div class="now-line"><div><span class="muted small">AI 正在做什么</span><b>'+esc(action)+'</b></div><div class="now-reason">'+esc(disp.lastReason||dec.reason||'等待决策边界')+'</div><div class="now-meta">'+esc(dec.provider||'')+(dec.model?' · '+esc(dec.model):'')+(dec.latency_ms?' · '+fmtMS(dec.latency_ms)+' ms':'')+(dec.fallback?' · 回退':'' )+'</div></div>'
    +tracePanel(v)
    +notes
    +(chat?'<div class="chat-lines">'+chat+'</div>':'')
    +'<div class="gc-chat"><textarea rows="2" maxlength="500" data-input="'+esc(v.id)+'" placeholder="说话或输入约束，例如：先别打那个 Boss"></textarea><button class="mic-btn call-btn '+(voice?'active':'')+'" data-call="'+esc(v.id)+'" title="实时语音通话">🎙 '+(voice?'通话中':'通话')+'</button><button class="send-btn" data-send="'+esc(v.id)+'">发送</button></div>'
    +'<div class="gc-chat-hint">Enter 发送 · Shift+Enter 换行 · 指令在下一个决策周期生效'+(voice?' · <b class="lime">通话中，说完会自动发送</b>':'')+'</div>'
    +'<div class="card-controls">'+controls+teachButton(v)+'<button class="pill-btn" data-mem="'+esc(v.id)+'">记忆 '+Math.round(num(memory.bytes,0)/1024)+' KB</button><button class="pill-btn danger-text" data-act="delete" data-id="'+esc(v.id)+'">删除</button></div>'
    +'</article>';
}

function setupSteps(){
  var llm=S.llm||{}; var local=llm.local||{}; var router=llm.router||{};
  var apiOk=!!(llm.has_api_key&&router.api_available!==false);
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
  renderEvents(); renderAndroidBanner();
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
  var apiState=llm.has_api_key?(router.api_available===false?'已保存 Key':'可用'):'未配置';
  var runningModels=local.running_models||[];
  var runningCount=runningModels.length||(local.servers||[]).filter(function(x){return x&&x.running}).length;
  var localState=runningCount>1?('运行中 ×'+runningCount):(runningCount===1?'运行中':(local.download&&local.download.active?'下载中':(local.enabled?'已就绪':'未启动')));
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
  (local.servers||[]).forEach(function(sv){
    if(sv&&sv.running) chips.push('<span class="bchip">llama-server<b>'+esc(sv.model||local.model||'')+' · :'+esc(sv.port||'')+'</b></span>');
  });
  if(!(local.servers||[]).length&&local.server&&local.server.running) chips.push('<span class="bchip">llama-server<b>127.0.0.1:'+esc(local.server.port||'')+'</b></span>');
  $('#localSummary').innerHTML=chips.join('')||'<span class="muted">正在检测设备…</span>';
  var models=(local.models||[]).slice(0);
  var seen={}; models.forEach(function(m){seen[m.id]=1});
  (local.catalog||[]).forEach(function(m){ if(!seen[m.id]){ var x={}; for(var k in m){x[k]=m[k]} x.extra=true; models.push(x); seen[m.id]=1; } });
  var dl=local.download||{};
  if(!models.length){
    $('#localModels').innerHTML='<div class="muted">没有检测到可用模型档位。'+(local.error?' '+esc(local.error):'')+'</div>';
  }else{
    $('#localModels').innerHTML=models.map(function(m){
      var active=dl.active&&(dl.model_id===m.id||dl.model===m.id||!dl.model_id);
      var selected=!!(m.selected||(local.model===m.id));
      var running=!!m.running||runningModels.indexOf(m.id)>=0;
      var canDl=!m.present&&!active;var canCancel=active;
      var row='<div class="model-row'+(active?' dl':'')+((selected||running)?' selected':'')+'"><div class="mr-head"><span class="dot '+(running||m.present?'on':'')+'"></span><b>'+esc(m.id)+'</b><span class="tag">'+esc(m.extra?'可选':(ROLE_LABEL[m.role]||m.role||''))+'</span><span class="tag '+(running||m.present?'ok':'')+'">'+(running?'运行中':(m.present?(selected?'当前默认':'已下载'):'需下载'))+'</span></div>'
        +'<div class="mr-meta"><span>'+esc(m.license||'')+'</span><span>'+esc(fmtSize(m.size_bytes))+'</span></div>';
      if(active){
        row+='<div class="bar"><i style="width:'+Math.max(0,Math.min(100,num(dl.percent,0)))+'%"></i></div><div class="mr-meta"><span>'+esc(fmtSize(dl.downloaded))+' / '+esc(fmtSize(dl.total))+'</span><span>'+esc(num(dl.bytes_per_second,0).toFixed(0))+' B/s</span><span><button class="link-btn" data-cancel="1">取消</button></span></div>';
      }
      row+='<div class="model-actions">'
        +(canDl?'<button class="pill-btn model-action" data-dl="'+esc(m.id)+'">下载</button>':'')
        +((m.present&&!running&&!active)?'<button class="pill-btn model-action" data-startmodel="'+esc(m.id)+'">启动</button>':'')
        +((m.present&&!selected&&!active)?'<button class="link-btn" data-select="'+esc(m.id)+'">设为默认</button>':'')
        +((m.present&&!running&&!active)?'<button class="link-btn danger-text" data-delmodel="'+esc(m.id)+'">删除</button>':'')
        +(running?'<button class="link-btn" data-stopmodel="'+esc(m.id)+'">停止</button>':'')
        +(running?'<span class="tag ok">运行中</span>':'')
      +'</div></div>';
      return row;
    }).join('');
  }
  $('#localNote').textContent=dl.error?('下载失败：'+dl.error):(local.note||'模型不入安装包。可以同时运行多个模型，每一层可以指定不同模型；未运行的模型会明确标出并回退规则层。');
  var starting=!!(local.download&&local.download.active);
  $('#localStart').disabled=!!((runningCount>0)||starting);
  $('#localStart').textContent=starting?'正在下载模型…':(runningCount>0?'本机已有模型运行中':'启用本机 AI');
  $('#localStop').disabled=!(runningCount>0);
  $('#localRefresh').disabled=false;
  renderRuntimeTruth();
  renderRoutes();
  renderVoice(llm.voice||{});
}
function voiceModeLabel(m){return m==='api'?'API TTS':m==='local'?'本地/自建 TTS':'系统语音'}
function renderVoice(v){
  v=v||{};
  fillIfIdle('voiceASR',v.asr_mode||'system');
  fillIfIdle('voiceTTS',v.tts_mode||'system');
  fillIfIdle('voiceBase',v.base_url||'');
  fillIfIdle('voiceModel',v.model||'tts-1');
  fillIfIdle('voiceName',v.voice||'alloy');
  fillIfIdle('voiceSpeed',v.speed===undefined?'1':v.speed);
  if(document.activeElement!==$('#voiceEnabled')) $('#voiceEnabled').checked=v.enabled!==false;
  var t=$('#voiceTag'); if(t){t.textContent=v.enabled===false?'已静音':voiceModeLabel(v.tts_mode);t.className='tag '+(v.enabled===false?'':'ok')}
  var n=$('#voiceNote'); if(n) n.textContent=v.note||'Key 只保存在本机；本机模式不打包语音模型，失败会自动回退系统语音。';
  renderVoiceStatus();
}
function renderVoiceStatus(){
  var box=$('#voiceStatus'); if(!box) return;
  var host=(S.host&&S.host.voice)||{};
  var rows=[];
  if(S.voice) rows.push(['通话','进行中：'+instanceName(S.voice.id)+' · 说完自动发送']);
  if(host.listening) rows.push(['麦克风','正在听你说话']);
  if(host.speaking) rows.push(['AI','正在说话']);
  if(host.last_heard) rows.push(['听到',String(host.last_heard)]);
  if(host.last_spoken) rows.push(['说了',String(host.last_spoken)]);
  if(host.error) rows.push(['错误',String(host.error)]);
  if(!rows.length){
    box.innerHTML='还没有语音状态。点游戏卡或设置页的「通话」后，这里会显示 AI 是否在听、是否在说、听到了什么、说了什么。';
    return;
  }
  box.innerHTML=rows.map(function(r){return '<div class="vr"><span class="vk">'+esc(r[0])+'</span><span class="vv">'+esc(r[1])+'</span></div>'}).join('');
}
function routeObj(role){
  var r=(S.aiRoutes&&S.aiRoutes.routes)||{};
  var x=r[role];
  if(typeof x==='string') return {mode:x,model:''};
  if(x&&typeof x==='object') return {mode:x.mode||'auto',model:x.model||''};
  return {mode:'auto',model:''};
}
function routeStatus(role){
  var st=(S.aiRoutes&&S.aiRoutes.status)||{};
  return st[role]||(S.aiRoutes&&S.aiRoutes.available&&S.aiRoutes.available[role])||null;
}
function downloadedRouteModels(){
  var local=(S.llm&&S.llm.local)||{};
  var seen={}, out=[];
  (local.models||[]).forEach(function(m){ if(m&&m.present&&!seen[m.id]){seen[m.id]=1;out.push(m)} });
  return out;
}
/* Draft survives the 2.5s refresh: without it every poll rebuilt the form from
   the saved config, which is why a user's selection always snapped back to 自动. */
function ensureRouteDraft(){
  if(S.routeDraft) return S.routeDraft;
  var d={};
  ROLES.forEach(function(r){ d[r.key]=routeObj(r.key) });
  S.routeDraft=d;
  return d;
}
function routeModelControl(role,cur){
  if(cur.mode==='local'){
    var models=downloadedRouteModels(), found=false;
    var opts='<option value="">默认（按设备档位）</option>';
    models.forEach(function(m){
      var sel=cur.model===m.id; if(sel) found=true;
      opts+='<option value="'+esc(m.id)+'"'+(sel?' selected':'')+'>'+esc(m.id)+' · '+esc(fmtSize(m.size_bytes))+'</option>';
    });
    if(cur.model&&!found) opts+='<option value="'+esc(cur.model)+'" selected>'+esc(cur.model)+'（尚未下载）</option>';
    return '<select data-route-model="'+role+'">'+opts+'</select>';
  }
  var ph=cur.mode==='api'?'API 模型名，例如 gpt-4o-mini':'可选：自动模式下优先使用的 API 模型名';
  return '<input data-route-model="'+role+'" value="'+esc(cur.model)+'" placeholder="'+ph+'">';
}
function routeStatusTag(st){
  if(!st) return '<span class="tag">状态未返回</span>';
  if(st.implemented===false) return '<span class="tag warn">未接入</span>';
  if(st.available===false) return '<span class="tag warn">不可用，将回退</span>';
  if(st.state==='working') return '<span class="tag ok">正在调用</span>';
  if(num(st.calls,0)>0) return '<span class="tag ok">已调用 '+esc(st.calls)+' 次</span>';
  return '<span class="tag">尚未调用</span>';
}
function routeExplain(role,cur,st){
  var bits=[];
  var modeText={
    auto:'自动：先看本机模型是否已启动，可用就用本机；否则用已配置的 API；两者都不可用则规则回退。不会静默假装模型在工作。',
    api:'API：这一层只把请求发给上面配置的 API；Key 或接口不可用时明确回退规则。',
    local:'本机：这一层只在本机 llama-server 推理；模型没启动或没下载就回退，不会偷偷改用 API。',
    offline:'规则回退：这一层不调用任何模型，直接由规则处理。'
  }[cur.mode]||'';
  bits.push(modeText);
  if(st&&st.implemented===false) bits.push('这一层当前没有真实调用点：选择会保存，但实际仍由规则层处理。');
  if(st&&num(st.calls,0)>0){
    var last=st.last_at?('，最近 '+ago(st.last_at)):'';
    bits.push('已调用 '+esc(st.calls)+' 次'+(st.last_latency_ms?'，上次 '+esc(Math.round(num(st.last_latency_ms,0)))+' ms':'')+last+(st.last_error?'，最近错误：'+esc(st.last_error):''));
  }else if(st&&st.implemented!==false){
    bits.push('还没有调用记录；开始游玩并走到这一层后，这里会显示实际调用次数和最近错误。');
  }
  return bits.join(' ');
}
function renderRuntimeTruth(){
  var el=$('#runtimeTruth'); if(!el) return;
  var llm=S.llm||{}, local=llm.local||{};
  var st=(S.aiRoutes&&S.aiRoutes.status)||{};
  var wired=[], unwired=[];
  ROLES.forEach(function(r){
    var x=st[r.key];
    if(x&&x.implemented===false) unwired.push(r.label); else wired.push(r.label);
  });
  var running='当前没有大模型在跑，规则引擎在兜底';
  var servers=(local.servers||[]).filter(function(x){return x&&x.running});
  var runningIDs=(local.running_models||[]).filter(function(x){return !!x});
  if(servers.length){
    var names=servers.map(function(x){return x.model||x.base_url||(':'+x.port)});
    running='本机 '+servers.length+' 个模型在跑：'+names.join('、')+'；不同层可以指定不同模型，未运行的层会回退规则';
  }else if(runningIDs.length){
    running='本机模型在跑：'+runningIDs.join('、')+'；不同层可以指定不同模型';
  }else if(local.server&&local.server.running){
    running='本机 llama-server 正在跑'+(local.model?'（'+local.model+'）':'')+'，多个层可以共用它';
  }else if(llm.has_api_key){
    running='API 已配置，按每一层实际可用性调用；不可用时回退规则';
  }
  el.innerHTML='<b>现在到底是什么在跑：</b>'+esc(running)+'。'
    +'<span>已接入真实模型调用的层：'+esc(wired.join('、')||'无')+'。</span>'
    +(unwired.length?'<span class="warn-text">尚未接入调用点（选择会保存，但仍由规则层处理）：'+esc(unwired.join('、'))+'。</span>':'');
}
function renderRoutes(){
  var box=$('#aiRoutes'); if(!box) return;
  var has=!!(S.aiRoutes&&S.aiRoutes.routes);
  if(!has&&!S.routeDraft) return;
  // Keep the caret while the user types an API model name. The draft still
  // holds the value, so a later poll can safely rebuild the row.
  var active=document.activeElement;
  if(active&&active.tagName==='INPUT'&&active.closest&&active.closest('#aiRoutes')) return;
  var draft=ensureRouteDraft();
  box.innerHTML=ROLES.map(function(r){
    var cur=draft[r.key]||{mode:'auto',model:''}, st=routeStatus(r.key);
    return '<div class="route-row" data-role="'+r.key+'"><div class="route-name"><b>'+esc(r.label)+'</b><small>'+esc(r.desc)+'</small></div>'
      +'<select data-route-mode="'+r.key+'">'
        +'<option value="auto"'+(cur.mode==='auto'?' selected':'')+'>自动</option>'
        +'<option value="api"'+(cur.mode==='api'?' selected':'')+'>API</option>'
        +'<option value="local"'+(cur.mode==='local'?' selected':'')+'>本机</option>'
        +'<option value="offline"'+(cur.mode==='offline'?' selected':'')+'>规则回退</option>'
      +'</select>'
      +routeModelControl(r.key,cur)
      +routeStatusTag(st)
      +'<div class="route-help" data-route-help="'+r.key+'">'+esc(routeExplain(r.key,cur,st))+'</div>'
      +'</div>';
  }).join('');
  box.onchange=function(ev){
    var sel=ev.target.closest('[data-route-mode]');
    if(sel){
      var role=sel.getAttribute('data-route-mode');
      draft[role]=draft[role]||{mode:'auto',model:''};
      draft[role].mode=sel.value;
      if(sel.value!=='api'&&sel.value!=='local') draft[role].model='';
      renderRoutes();
      return;
    }
    var m=ev.target.closest('[data-route-model]');
    if(m){ draft[m.getAttribute('data-route-model')].model=String(m.value||'').trim(); }
  };
  box.oninput=function(ev){
    var m=ev.target.closest('[data-route-model]');
    if(m){ draft[m.getAttribute('data-route-model')].model=String(m.value||'').trim(); }
  };
  var tag=$('#routesTag'); if(tag){tag.textContent=has?'已分层':'旧版后端';tag.className='tag '+(has?'ok':'warn')}
  var note=$('#routesNote'); if(note) note.textContent=has?'每一层实际调用次数、最近活动与错误都来自运行时统计；同一时刻只有一个本机模型在跑，不同层可以共用它，未接入的层会标明。':'当前后端没有分层路由接口，只有决策层可切换；升级后这里会逐层生效。';
}
async function saveRoutes(){
  var draft=ensureRouteDraft(), routes={};
  ROLES.forEach(function(r){
    var cur=draft[r.key]||{mode:'auto',model:''};
    routes[r.key]={mode:cur.mode||'auto',model:(cur.model||'').trim()};
  });
  try{
    var r=await api('/api/ai/routes',{method:'POST',body:{routes:routes}});
    S.aiRoutes=r||{routes:routes}; S.routeDraft=null; toast('分层 AI 设置已保存'); renderRoutes();
    setTimeout(refresh,300);
  }catch(e){toast('保存失败：'+e.message,true)}
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
function permValue(s,name){
  var p=(s&&s.permissions)||{};
  if(name==='overlay') return !!(p.overlay||p.overlay_permission||s.overlayPermission);
  if(name==='accessibility') return !!(p.accessibility||p.accessibility_ready||s.accessibilityReady);
  if(name==='notification') return !!(p.notification||p.notifications||s.notificationPermission);
  if(name==='capture') return !!(p.capture||s.captureRunning);
  return false;
}
function permissionRows(s){
  var rows=[
    {key:'overlay',label:'悬浮窗 / 灵动岛',need:'没有它就没有悬浮球，也看不到实时对话入口',btn:'去授权'},
    {key:'accessibility',label:'无障碍服务',need:'AI 通过它点击、滑动、返回；没有它只能看不能动',btn:'去开启'},
    {key:'notification',label:'通知权限',need:'Android 13+ 后台保活与录屏前台服务提示',btn:'去开启'},
    {key:'capture',label:'屏幕录制',need:'AI 只能通过录屏看画面；停止后实例会报错',btn:'开始截屏'}
  ];
  return rows.map(function(r){
    var ok=permValue(s,r.key);
    return '<div class="perm-row '+(ok?'ok':'')+'"><span class="perm-ico">'+(ok?'✓':'!')+'</span><div><b>'+esc(r.label)+'</b><small>'+esc(ok?'已就绪':r.need)+'</small></div>'
      +(ok?'<span class="tag ok">已开启</span>':'<button class="pill-btn" data-perm="'+r.key+'">'+esc(r.btn)+'</button>')+'</div>';
  }).join('');
}
function missingPermissions(s){
  var miss=[];
  if(!permValue(s,'accessibility')) miss.push('无障碍');
  if(!permValue(s,'overlay')) miss.push('悬浮窗');
  if(!permValue(s,'capture')) miss.push('录屏');
  return miss;
}
function renderAndroidBanner(){
  var boxes=[$('#androidBanner'),$('#androidBannerGames')].filter(Boolean);
  var s=S.host;
  if(!boxes.length) return;
  if(!s){boxes.forEach(function(b){b.classList.add('hidden')});return}
  var miss=missingPermissions(s);
  var capErr=s.captureError||s.capture_error||'';
  var html='';
  if(miss.length||capErr){
    html='<div class="ab-main"><b>'+(miss.length?'还不能玩：缺少 '+esc(miss.join('、')):'截屏已停止')+'</b><p>'+esc(capErr||'按顺序授权；缺少无障碍时 AI 只能看画面，不能点击。')+'</p></div>'
      +'<div class="ab-actions">'+miss.map(function(n){
        var key=n==='无障碍'?'accessibility':n==='悬浮窗'?'overlay':'capture';
        return '<button class="pill-btn" data-perm="'+key+'">授权'+esc(n)+'</button>';
      }).join('')+'<button class="primary-btn" data-prepare="1">按顺序准备</button></div>';
  }else if(s.captureRunning&&!s.wsConnected){
    html='<div class="ab-main"><b>正在连接核心…</b><p>录屏已开，等待手机与核心的 WebSocket 握手；连上后才会启动 AI。</p></div>'
      +'<div class="ab-actions"><button class="pill-btn" data-reconnect="1">重新连接</button><button class="primary-btn" data-prepare="1">按顺序准备</button></div>';
  }
  boxes.forEach(function(b){
    if(!html){b.classList.add('hidden');return}
    b.classList.remove('hidden');b.innerHTML=html;
  });
}
function refreshHostStatus(){
  var panel=$('#androidPermPanel'); if(!panel) return;
  var s=hostStatus();
  S.host=s;
  renderVoiceStatus();
  panel.classList.toggle('hidden',!s);
  var tp=$('#takeoverPanel'); if(tp) tp.classList.toggle('hidden',!s);
  if(!s){renderAndroidBanner();return}
  var core=$('#andCore'); if(core) core.textContent=s.coreReady?('运行中 · 127.0.0.1:'+s.corePort):(s.coreError?'启动失败：'+s.coreError:'启动中');
  var cap=$('#andCapture'); if(cap) cap.textContent=s.captureRunning?((s.capturePaused?'已暂停':'运行中')+(s.captureWidth?' · '+s.captureWidth+'×'+s.captureHeight:'')):'未开始';
  var bridge=$('#andBridge');
  if(bridge){
    var wsState=String(s.wsState||'').toLowerCase();
    bridge.textContent=s.wsConnected?'已连接 · 可下发操作':(wsState==='connecting'||wsState==='reconnecting'?'连接中…':(s.wsError?'未连接：'+s.wsError:'未连接'));
  }
  var list=$('#androidPermList'); if(list) list.innerHTML=permissionRows(s);
  var miss=missingPermissions(s);
  var missCount=miss.length+(s.wsConnected?0:1);
  var ready=$('#andReady'); if(ready){ready.textContent=missCount?('还差 '+missCount+' 项'):'已就绪';ready.className='tag '+(missCount?'warn':'ok')}
  var ce=$('#andCaptureError');
  if(ce){var err=s.captureError||s.capture_error||'';ce.textContent=err?('截屏错误：'+err):'';ce.classList.toggle('hidden',!err)}
  var sel=$('#takeoverSens');
  if(sel&&document.activeElement!==sel) sel.value=s.takeoverSensitivity||s.takeover_sensitivity||'medium';
  var vs=$('#androidVoiceState'); if(vs) vs.textContent=S.voice?('通话中：'+instanceName(S.voice.id)+' · 说完自动发送'):'实时通话：直接说话，识别后自动发送给 AI；AI 的回复会通过 TTS 说出来。';
  renderAndroidBanner();
}
function renderSettings(){
  var cfg=S.cfg||{};
  $('#setAddr').textContent=cfg.addr||location.host||'—';
  $('#setData').textContent=cfg.data_dir||'—';
  $('#setVersion').textContent=(cfg.version||(S.health&&S.health.version)||'—');
  $('#setMode').textContent=modeLabel((S.llm&&S.llm.mode)||cfg.llm&&cfg.llm.mode);
  var host=$('#hostInfo');
  if(window.GoPlayHost){host.textContent='Android 原生壳 · 核心与模型都在本机运行'+(S.host&&S.host.device?' · '+S.host.device:'');}
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
    else if(kind==='instance.error') text='出现错误：'+esc(d.error||'').slice(0,80);
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
      api('/api/llm'),
      api('/api/ai/routes').catch(function(){return null})
    ]);
    S.instances=(pack[0]&&pack[0].instances)||[];
    S.metrics=pack[1]||null;
    S.prefs=(pack[2]&&pack[2].preferences)||[];
    S.llm=pack[3]||null;
    S.local=(pack[3]&&pack[3].local)||null;
    if(pack[4]) S.aiRoutes=pack[4];
    S.health=S.health||{addr:(S.cfg&&S.cfg.addr)||location.host};
    S.frameNonce=Math.floor(Date.now()/3000);
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
  $('#windowsPickWrap').classList.toggle('hidden',src!=='windows');
  $('#launchWrap').classList.toggle('hidden',src==='mock');
  $('#createNote').textContent=src==='android'?'Android 会先检查悬浮窗、无障碍、通知和录屏；缺什么就打开系统设置，全部就绪后才开始 AI。':src==='windows'?'电脑端从开始菜单或 Steam 选择游戏，创建后可自动启动。AI 使用全屏截屏 + SendInput，不注入游戏进程。':'Mock 地下城用于跑通流程和评测，不需要真实游戏。';
  var def=src==='mock'?'mock_dungeon':src==='windows'?'generic':'generic_touch';
  if($('#fProfile').querySelector('option[value="'+def+'"]')) $('#fProfile').value=def;
}
function openModal(profileId){
  if(!S.staticLoaded) loadStatic();
  var src=S.isAndroid?'android':(window.goplayDesktop?'windows':'mock');
  $('#fSource').value=src;
  $('#fName').value=''; $('#fTargetWindow').value=''; $('#fId').value=nextID();
  $('#fEpisodes').value='1'; $('#fAuto').checked=true; $('#fLaunch').checked=true;
  $('#fAndroidGame').innerHTML='<option value="">加载已安装游戏…</option>';
  $('#fWindowsGame').innerHTML='<option value="">加载已安装游戏…</option>';
  updateSourceUI();
  if(profileId&&$('#fProfile').querySelector('option[value="'+profileId+'"]')) $('#fProfile').value=profileId;
  $('#modal').classList.remove('hidden');
  if(src==='android') loadAndroidGames();
  if(src==='windows') loadWindowsGames();
  setTimeout(function(){$('#fName').focus()},50);
}
function closeModal(){$('#modal').classList.add('hidden')}
function nextID(){return 'game-'+Date.now().toString(36)+Math.floor(Math.random()*900+100).toString(36)}
async function loadWindowsGames(){
  var sel=$('#fWindowsGame'); if(!sel) return;
  sel.innerHTML='<option value="">读取中…</option>';
  try{
    var d=await api('/api/games/windows');
    var list=(d&&d.games)||[];
    if(!list.length){sel.innerHTML='<option value="">没有找到开始菜单或 Steam 游戏，可手动填写名称</option>';return}
    sel.innerHTML='<option value="">选择已安装游戏（可选）…</option>'+list.map(function(g){
      return '<option value="'+esc(g.id)+'" data-name="'+esc(g.name)+'" data-kind="'+esc(g.kind)+'">'+esc(g.name)+' · '+esc(g.source||g.kind)+'</option>';
    }).join('');
  }catch(e){sel.innerHTML='<option value="">读取失败：'+esc(e.message)+'</option>'}
}
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
  var winGame=$('#fWindowsGame').value;
  var name=$('#fName').value.trim();
  var id=$('#fId').value.trim();
  var profile=$('#fProfile').value;
  if(src==='android'&&!pkg){toast('先选择一个已安装的游戏',true);return}
  if(src==='windows'&&!($('#fTargetWindow').value||'').trim()){toast('Windows 游戏必须填写游戏窗口标题，AI 只在这个窗口前台时才会操作',true);return}
  if(!name&&pkg){var opt=$('#fAndroidGame').selectedOptions[0];name=opt?opt.getAttribute('data-label')||pkg:''}
  if(!name&&winGame){var wopt=$('#fWindowsGame').selectedOptions[0];name=wopt?wopt.getAttribute('data-name')||'':''}
  if(!name){toast('给这个游戏起个名字',true);return}
  if(!/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(id)){toast('实例 ID 只能包含字母、数字、下划线和短横线',true);return}
  var auto=$('#fAuto').checked, launch=$('#fLaunch').checked;
  var targetWindow=($('#fTargetWindow').value||'').trim();
  var body={id:id,name:name,profile_id:profile,source_kind:src,target_window:src==='windows'?targetWindow:'',seed:0,max_episodes:Math.max(1,parseInt($('#fEpisodes').value||'1',10)),auto_start:(src==='android'||src==='windows')?false:auto};
  var btn=$('#createForm button[type="submit"]'); btn.disabled=true; btn.textContent='创建中…';
  try{
    await api('/api/instances',{method:'POST',body:body});
    if(pkg) S.pkgs[id]=pkg;
    if(src==='android') hostSetActive(id);
    closeModal();
    if(src==='android'){
      if(auto){
        toast('已创建，先检查权限与画面');
        prepareAndStart(id,pkg,launch,true);
      }else{
        if(launch&&pkg&&window.GoPlayHost&&window.GoPlayHost.launchGame){try{window.GoPlayHost.launchGame(pkg)}catch(e){}}
        toast('已创建；需要时点「开始」会先检查权限');
        setTimeout(function(){refresh();go('games')},300);
      }
    }else if(src==='windows'){
      if(launch&&winGame){
        try{var lg=await api('/api/games/windows/launch',{method:'POST',body:{id:winGame}});toast('已启动 '+(lg.name||'游戏')+'，等待窗口…')}catch(e){toast('启动游戏失败：'+e.message,true)}
      }
      if(auto){
        toast('已创建，稍后开始自动游玩');
        setTimeout(function(){startInstance(id);refresh();go('games')},launch&&winGame?1500:200);
      }else{toast('已创建');setTimeout(function(){refresh();go('games')},300)}
    }else{
      toast('已创建');
      setTimeout(function(){refresh();go('games')},300);
    }
    setTimeout(refresh,400);
  }catch(e){toast('创建失败：'+e.message,true)}
  finally{btn.disabled=false;btn.textContent='创建并开始'}
}
function hostReady(s){
  return !!(s&&s.coreReady&&s.wsConnected&&(s.captureRunning||permValue(s,'capture'))&&permValue(s,'accessibility'));
}
function missingForStart(s){
  var miss=[];
  if(!s||!s.coreReady) miss.push('本机核心');
  if(!permValue(s,'accessibility')) miss.push('无障碍');
  if(!permValue(s,'capture')) miss.push('录屏');
  if(!permValue(s,'overlay')) miss.push('悬浮窗');
  if(s&&s.coreReady&&!s.wsConnected) miss.push('连接核心');
  return miss;
}
async function prepareAndStart(id,pkg,launch,auto){
  if(!window.GoPlayHost){return startInstance(id)}
  if(S.starting[id]) return;
  S.starting[id]=true; hostSetActive(id); renderGames();
  try{
    var s=hostStatus()||{};
    if(window.GoPlayHost.prepareGame){
      try{
        var raw=window.GoPlayHost.prepareGame(id,pkg||'');
        var res=raw?JSON.parse(raw):{};
        if(res&&res.message) toast(res.message);
      }catch(e){toast('权限准备调用失败：'+e.message,true)}
      if(launch&&pkg&&window.GoPlayHost.launchGame){
        try{window.GoPlayHost.launchGame(pkg)}catch(e){toast('启动游戏失败：'+e.message,true)}
      }
    }else{
      if(launch&&pkg&&window.GoPlayHost.launchGame){
        try{window.GoPlayHost.launchGame(pkg)}catch(e){toast('启动游戏失败：'+e.message,true)}
      }
      if(!permValue(s,'capture')){
        try{window.GoPlayHost.startCapture()}catch(e){}
      }
    }
    var deadline=Date.now()+45000; var warned=false; var last=[];
    while(Date.now()<deadline){
      await sleep(900);
      s=hostStatus()||{};
      last=missingForStart(s);
      if(hostReady(s)) break;
      if(!warned&&Date.now()>deadline-32000){
        warned=true;
        toast('还需要：'+last.join('、')+'。请按提示授权，完成后会自动继续',true);
        if(S.page!=='settings') go('settings');
      }
    }
    if(!hostReady(s)){
      toast('准备未完成：'+(last.join('、')||'未知')+'。授权后再点开始',true);
      go('settings'); refreshHostStatus(); return;
    }
    if(auto) await startInstance(id);
  }finally{
    delete S.starting[id]; renderGames(); refreshHostStatus(); refresh();
  }
}
async function startInstance(id){
  try{
    await api('/api/instances/'+encodeURIComponent(id)+'/start',{method:'POST'});
    toast('AI 开始游玩');
    setTimeout(refresh,300);
  }catch(e){toast('启动失败：'+e.message,true); throw e}
}
async function releaseToAI(id){
  hostSetActive(id);
  var hostMsg='';
  if(window.GoPlayHost&&window.GoPlayHost.releaseHuman){
    try{
      var raw=window.GoPlayHost.releaseHuman(); var hr=raw?JSON.parse(raw):{};
      if(hr&&hr.ok===false) hostMsg=hr.message||hr.error||'';
    }catch(e){hostMsg=e.message||''}
  }
  try{
    await api('/api/instances/'+encodeURIComponent(id)+'/release',{method:'POST'});
  }catch(e){}
  try{
    await api('/api/instances/'+encodeURIComponent(id)+'/resume',{method:'POST'});
    toast(hostMsg?('已恢复实例，但手机端交还失败：'+hostMsg):'已交还 AI',!!hostMsg);
  }catch(e){toast(e.message,true)}
  setTimeout(refresh,250);
}
async function act(id,action){
  if(action==='delete'){
    if(!confirm('删除这个游戏实例？它的专属记忆也会被删除。')) return;
    try{await api('/api/instances/'+encodeURIComponent(id),{method:'DELETE'});toast('已删除');refresh()}catch(e){toast(e.message,true)}
    return;
  }
  if(action==='start'){
    var v=null; for(var i=0;i<S.instances.length;i++){if(S.instances[i].id===id)v=S.instances[i]}
    if(v&&v.source_kind==='android'){
      await prepareAndStart(id,S.pkgs[id]||v.package||v.pkg||'',true,true); return;
    }
    try{await startInstance(id)}catch(e){} return;
  }
  if(action==='release'){await releaseToAI(id);return}
  if((action==='resume')&&window.GoPlayHost) hostSetActive(id);
  var path=action==='stop'?'stop':action==='pause'?'pause':'resume';
  try{
    await api('/api/instances/'+encodeURIComponent(id)+'/'+path,{method:'POST'});
    toast(action==='stop'?'已停止':action==='pause'?'已暂停':'已继续');
    setTimeout(refresh,250);
  }catch(e){toast(e.message,true)}
}
async function sendChat(id,textOverride){
  var ta=$('[data-input="'+id+'"]');
  var text=(textOverride!==undefined?textOverride:(ta?ta.value:'')).trim(); if(!text) return;
  var btn=$('[data-send="'+id+'"]'); if(btn) btn.disabled=true;
  try{
    var r=null;
    try{
      r=await api('/api/instances/'+encodeURIComponent(id)+'/chat',{method:'POST',body:{text:text}});
    }catch(e){
      if(e.status===404) r=await api('/api/instances/'+encodeURIComponent(id)+'/message',{method:'POST',body:{text:text}});
      else throw e;
    }
    if(ta) ta.value=''; toast('已进入指令缓冲，下一个决策周期生效');
    if(r&&r.reply) speakReply(r.reply);
    setTimeout(refresh,450);
  }catch(e){toast(e.message,true)}
  finally{if(btn) btn.disabled=false}
}
function appendText(el,text){
  if(!el) return;
  el.value=(el.value?el.value+' ':'')+text;
  el.focus();
}
function speakNarration(text){
  if(!text) return;
  try{if(localStorage.getItem('goplay_narration')==='off') return}catch(e){}
  speakReply(text);
}
function nativeSpeak(text){
  if(window.GoPlayHost&&window.GoPlayHost.speak){
    try{
      var raw=window.GoPlayHost.speak(text);
      var res=null; try{res=typeof raw==='string'?JSON.parse(raw):raw}catch(e){}
      if(!res||res.ok!==false) return true;
      if(window.console) console.warn('native TTS failed',res.error||res);
    }catch(e){}
  }
  if(window.speechSynthesis){
    try{var u=new SpeechSynthesisUtterance(text);u.lang='zh-CN';window.speechSynthesis.cancel();window.speechSynthesis.speak(u);return true}catch(e){}
  }
  return false;
}
function stopSpeechAudio(){
  if(S.ttsAudio){try{S.ttsAudio.pause()}catch(e){};try{URL.revokeObjectURL(S.ttsAudio.src)}catch(e){};S.ttsAudio=null}
  if(window.speechSynthesis){try{window.speechSynthesis.cancel()}catch(e){}}
}
function serverSpeak(text,onFail){
  stopSpeechAudio();
  fetch('/api/voice/tts',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({text:text})}).then(function(r){
    if(!r.ok) return r.text().then(function(t){throw new Error(t||('HTTP '+r.status))});
    return r.blob();
  }).then(function(blob){
    var url=URL.createObjectURL(blob); var au=new Audio(url); S.ttsAudio=au;
    au.onended=function(){try{URL.revokeObjectURL(url)}catch(e){};if(S.ttsAudio===au)S.ttsAudio=null};
    au.onerror=function(){if(window.console)console.warn('server TTS audio failed');onFail()};
    var p=au.play(); if(p&&p.catch)p.catch(function(e){if(window.console)console.warn('server TTS play',e);onFail()});
  }).catch(function(e){if(window.console)console.warn('server TTS',e&&e.message);onFail()});
}
function speakReply(text){
  if(!text) return;
  var v=(S.llm&&S.llm.voice)||S.voiceCfg||{};
  if(v.enabled===false) return;
  var mode=v.tts_mode||'system';
  if(mode==='api'||mode==='local'){serverSpeak(text,function(){nativeSpeak(text)});return}
  nativeSpeak(text);
}
function updateCallUI(){
  $$('[data-call]').forEach(function(b){
    var on=S.voice&&S.voice.id===b.getAttribute('data-call');
    b.classList.toggle('active',!!on);
    b.innerHTML=on?'🎙 通话中':'🎙 通话';
    b.title=on?'结束实时语音':'实时语音通话';
  });
  var call=$('#androidVoiceCall');
  if(call){call.textContent=S.voice?'🎙 通话进行中':'🎙 开始实时通话';call.classList.toggle('active',!!S.voice)}
  var vs=$('#androidVoiceState');
  if(vs) vs.textContent=S.voice?('通话中：'+instanceName(S.voice.id)+' · 说完自动发送，AI 回复会自动朗读'):'实时通话：直接说话，识别后自动发送给 AI；AI 的回复会通过 TTS 说出来。';
}
function voiceTargetInstance(){
  var id=(S.host&&S.host.activeInstance)||'';
  if(id) return id;
  for(var i=0;i<S.instances.length;i++){
    var k=statusInfo(S.instances[i]).key;
    if(k==='running'||k==='human'||k==='paused') return S.instances[i].id;
  }
  return S.instances.length?S.instances[0].id:'';
}
function startVoiceFromSettings(){
  var id=voiceTargetInstance();
  if(!id){toast('先创建一个游戏，再开始实时通话',true);return}
  startCall(id);
}
function webSpeechStart(){
  var SR=window.SpeechRecognition||window.webkitSpeechRecognition;
  if(!SR){toast('当前环境不支持语音，请用键盘输入',true);return false}
  try{
    if(S.voice.rec){try{S.voice.rec.stop()}catch(e){}}
    var rec=new SR(); rec.lang='zh-CN'; rec.continuous=true; rec.interimResults=true; rec.maxAlternatives=1;
    rec.onresult=function(ev){
      var interim='', final='';
      for(var i=ev.resultIndex;i<ev.results.length;i++){
        var t=ev.results[i][0].transcript;
        if(ev.results[i].isFinal) final+=t; else interim+=t;
      }
      if(interim&&S.voice){var ta=$('[data-input="'+S.voice.id+'"]');if(ta)ta.placeholder='听到：'+interim}
      if(final) window.goplayVoiceFinal(final.trim());
    };
    rec.onend=function(){if(S.voice&&S.voice.active){try{rec.start()}catch(e){}}};
    rec.onerror=function(){};
    S.voice.rec=rec; rec.start();
    return true;
  }catch(e){toast('语音启动失败：'+e.message,true);return false}
}
function startCall(id){
  hostSetActive(id);
  if(S.voice&&S.voice.id!==id) stopCall();
  S.voiceTarget=id; S.voice={id:id,active:true,rec:null};
  if(window.GoPlayHost&&window.GoPlayHost.startConversation){
    var raw=null;
    try{raw=window.GoPlayHost.startConversation()}catch(e){toast('原生语音启动失败：'+e.message,true)}
    var res=null;
    try{res=typeof raw==='string'?JSON.parse(raw):raw}catch(e){res=null}
    if(res&&res.ok===false){
      var msg=res.message||res.error||'语音启动失败';
      if(res.error==='permission_required'){
        toast('请允许麦克风；授权后会自动开始通话');
      }else{
        toast('语音启动失败：'+msg,true);
        S.voice=null; S.voiceTarget=null; updateCallUI(); return;
      }
    }else{
      toast('实时通话中，说完会自动发送；AI 说话时会暂停听音');
    }
  }else{
    if(webSpeechStart()) toast('实时通话中，说完会自动发送');
  }
  updateCallUI();
}
function stopCall(){
  if(window.GoPlayHost&&window.GoPlayHost.stopConversation){try{window.GoPlayHost.stopConversation()}catch(e){}}
  if(window.GoPlayHost&&window.GoPlayHost.stopSpeaking){try{window.GoPlayHost.stopSpeaking()}catch(e){}}
  if(S.voice&&S.voice.rec){try{S.voice.rec.onend=null;S.voice.rec.stop()}catch(e){}}
  stopSpeechAudio();
  if(window.speechSynthesis){try{window.speechSynthesis.cancel()}catch(e){}}
  setHostVoice({speaking:false,listening:false});
  S.voice=null; S.voiceTarget=null; updateCallUI(); toast('已结束通话');
}
function toggleCall(id){if(S.voice&&S.voice.id===id) stopCall(); else startCall(id)}
window.goplayFocus=function(where){
  if(String(where||'')==='permissions'){ go('settings'); setTimeout(refreshHostStatus,80); }
};
function setHostVoice(patch){
  S.host=S.host||{}; var v={};
  for(var k in (S.host.voice||{})) v[k]=S.host.voice[k];
  for(var j in (patch||{})) v[j]=patch[j];
  S.host.voice=v; renderVoiceStatus();
}
window.goplayVoice=function(text){var t=String(text||'').trim();if(t)setHostVoice({last_heard:t,listening:true});if(!S.voiceTarget)return;var ta=$('[data-input="'+S.voiceTarget+'"]');appendText(ta,t);toast('已识别语音')};
window.goplayVoiceFinal=function(text){
  if(!S.voiceTarget) return;
  var t=String(text||'').trim(); if(!t) return;
  setHostVoice({last_heard:t});
  var ta=$('[data-input="'+S.voiceTarget+'"]'); if(ta){ta.value=t;ta.placeholder='说话或输入约束…'}
  sendChat(S.voiceTarget,t);
};
window.goplayVoicePartial=function(text){var t=String(text||'').trim();if(t)setHostVoice({last_heard:t,listening:true});if(!S.voiceTarget)return;var ta=$('[data-input="'+S.voiceTarget+'"]');if(ta)ta.placeholder='听到：'+t};
window.goplayVoiceError=function(msg){setHostVoice({error:String(msg||'未知'),listening:false});toast('语音错误：'+(msg||'未知'),true)};
window.goplayVoiceState=function(raw){
  var v=null; try{v=typeof raw==='string'?JSON.parse(raw):raw}catch(e){}
  if(!v||typeof v!=='object') return;
  setHostVoice({listening:!!v.listening,speaking:!!v.speaking,last_heard:v.last_heard||'',last_spoken:v.last_spoken||'',error:v.error||''});
};
window.goplayVoiceStarted=function(){if(!S.voice)return;S.voice.active=true;setHostVoice({listening:true,error:''});updateCallUI();var vs=$('#androidVoiceState');if(vs)vs.textContent='通话中：麦克风已打开，切到游戏后仍可继续说话'};
window.goplaySpeaking=function(on){setHostVoice({speaking:!!on});var vs=$('#androidVoiceState');if(vs&&S.voice)vs.textContent=on?'AI 正在说话…':'通话中：说完自动发送，AI 回复会自动朗读'};

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
    toast('API 已保存并启用'); renderAI(); refresh();
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
async function saveVoice(ev){
  ev.preventDefault();
  var body={enabled:$('#voiceEnabled').checked,tts_mode:$('#voiceTTS').value,asr_mode:$('#voiceASR').value,base_url:$('#voiceBase').value.trim(),model:$('#voiceModel').value.trim(),voice:$('#voiceName').value.trim(),speed:Number($('#voiceSpeed').value||'1')};
  var key=$('#voiceKey').value.trim(); if(key) body.api_key=key;
  try{
    var res=await api('/api/voice',{method:'POST',body:body});
    S.llm=S.llm||{}; S.llm.voice=res; $('#voiceKey').value='';
    toast('语音设置已保存'); renderVoice(res);
  }catch(e){toast('保存语音设置失败：'+e.message,true)}
}
async function clearVoiceKey(){
  if(!confirm('清除保存在本机的语音 TTS Key？')) return;
  try{var res=await api('/api/voice',{method:'POST',body:{clear_key:true}});S.llm=S.llm||{};S.llm.voice=res;toast('已清除语音 Key');renderVoice(res)}catch(e){toast(e.message,true)}
}
async function clearVoiceBase(){
  try{var res=await api('/api/voice',{method:'POST',body:{clear_base:true}});S.llm=S.llm||{};S.llm.voice=res;toast('已清除语音接口地址，将复用 API 地址');renderVoice(res)}catch(e){toast(e.message,true)}
}
async function enableLocal(){
  try{
    var res=await api('/api/llm',{method:'POST',body:{mode:'local'}});
    S.llm=res; toast('已切换本机 AI，正在下载/启动…'); renderAI();
    setTimeout(refresh,1200); setTimeout(refresh,4000);
  }catch(e){toast(e.message,true)}
}
async function startLocal(){
  try{await api('/api/local/start',{method:'POST'});toast('正在启动本机模型');setTimeout(refresh,700)}catch(e){toast(e.message,true)}
}
async function stopLocal(){
  try{await api('/api/local/stop',{method:'POST'});toast('本机模型已停止');setTimeout(refresh,300)}catch(e){toast(e.message,true)}
}
async function startModel(id){
  try{await api('/api/local/start',{method:'POST',body:{model_id:id}});toast('正在启动 '+id);setTimeout(refresh,700)}catch(e){toast(e.message,true)}
}
async function stopModel(id){
  try{await api('/api/local/stop',{method:'POST',body:{model_id:id}});toast('已停止 '+id);setTimeout(refresh,400)}catch(e){toast(e.message,true)}
}
async function downloadModel(id){
  try{await api('/api/local/download',{method:'POST',body:{model_id:id}});toast('开始下载 '+(id||'推荐模型'));setTimeout(refresh,800)}catch(e){toast(e.message,true)}
}
async function cancelDownload(){
  try{await api('/api/local/cancel',{method:'POST'});toast('已请求取消下载');setTimeout(refresh,500)}catch(e){toast(e.message,true)}
}
async function selectModel(id){
  try{await api('/api/local/select',{method:'POST',body:{model_id:id}});toast('已切换本机模型');setTimeout(refresh,500)}catch(e){toast(e.message,true)}
}
async function deleteModel(id){
  if(!confirm('从手机/电脑删除模型 '+id+'？之后可以重新下载。')) return;
  try{await api('/api/local/models/'+encodeURIComponent(id),{method:'DELETE'});toast('已删除');setTimeout(refresh,400)}catch(e){toast(e.message,true)}
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
function reconnectHost(){
  if(!window.GoPlayHost||!window.GoPlayHost.reconnectCore){
    toast('当前环境没有手机桥接，正在刷新连接');loadStatic();refresh();return;
  }
  try{
    var r=JSON.parse(window.GoPlayHost.reconnectCore()||'{}');
    toast(r.wsConnected?'手机桥接已连接':'正在重新连接手机桥接…',!r.wsConnected);
  }catch(e){toast('重连失败：'+e.message,true)}
  setTimeout(refreshHostStatus,800);
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
function hostCall(method,okMessage){
  if(!window.GoPlayHost||typeof window.GoPlayHost[method]!=='function'){
    toast('当前环境没有这个原生功能',true);return;
  }
  try{window.GoPlayHost[method]();if(okMessage)toast(okMessage);setTimeout(refreshHostStatus,600)}catch(e){toast('原生调用失败：'+e.message,true)}
}
function grantPerm(kind){
  if(kind==='overlay') return hostCall('openOverlaySettings','请在系统页允许「显示在其他应用上层」');
  if(kind==='accessibility') return hostCall('openAccessibility','请在系统页开启 GoPlay 无障碍服务');
  if(kind==='notification') return hostCall('requestNotification','请允许通知');
  if(kind==='capture') return hostCall('startCapture','如果弹出录屏授权，请点「立即开始」');
}
function prepareAll(){
  var s=hostStatus()||{};
  var inst=s.activeInstance;
  if(window.GoPlayHost&&window.GoPlayHost.prepareGame&&inst){
    try{var raw=window.GoPlayHost.prepareGame(inst,S.pkgs[inst]||'');var r=raw?JSON.parse(raw):{};if(r.message)toast(r.message)}catch(e){}
  }else{
    var miss=missingPermissions(s);
    if(miss.indexOf('悬浮窗')>=0) return grantPerm('overlay');
    if(miss.indexOf('无障碍')>=0) return grantPerm('accessibility');
    if(miss.indexOf('录屏')>=0) return grantPerm('capture');
  }
  toast('按提示完成授权；状态会自动刷新'); setTimeout(refreshHostStatus,900);
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
    var call=ev.target.closest('[data-call]');
    if(call){toggleCall(call.getAttribute('data-call'));return}
    var prof=ev.target.closest('[data-profile]');
    if(prof){openModal(prof.getAttribute('data-profile'));return}
    var pref=ev.target.closest('[data-pref]');
    if(pref){deletePref(pref.getAttribute('data-pref'));return}
    var mem=ev.target.closest('[data-mem]');
    if(mem){go('memory');return}
    var teach=ev.target.closest('[data-teach]');
    if(teach){openTeaching(teach.getAttribute('data-teach'));return}
    var tt=ev.target.closest('[data-teachtoggle]');
    if(tt){toggleTeaching(tt.getAttribute('data-teachtoggle'));return}
    var tc=ev.target.closest('[data-teachclear]');
    if(tc){clearTeaching(tc.getAttribute('data-teachclear'));return}
    if(ev.target.closest('#closeTeach')){closeTeaching();return}
    if(ev.target.id==='teachModal'){closeTeaching();return}
    var diagAct=ev.target.closest('[data-diagact]');
    if(diagAct){diagAction(diagAct.getAttribute('data-diagact'),diagAct.getAttribute('data-id'));return}
    var diagLoad=ev.target.closest('[data-diagload]');
    if(diagLoad){ensureDiag(diagLoad.getAttribute('data-diagload'),true);toast('重新诊断中…');return}
    var copy=ev.target.closest('[data-copy]');
    if(copy){for(var ci=0;ci<S.instances.length;ci++){if(S.instances[ci].id===copy.getAttribute('data-copy')){copyDiag(S.instances[ci]);break}}return}
    var perm=ev.target.closest('[data-perm]');
    if(perm){grantPerm(perm.getAttribute('data-perm'));return}
    if(ev.target.closest('[data-prepare]')){prepareAll();return}
    if(ev.target.closest('#overlayBtn,#overlayBtn2')){openOverlay();return}
    if(ev.target.closest('#newBtn')){openModal();return}
    if(ev.target.closest('#closeModal,#cancelCreate')){closeModal();return}
    if(ev.target.id==='modal'){closeModal();return}
    if(ev.target.closest('#refreshGames')){loadAndroidGames();return}
    if(ev.target.closest('#refreshWinGames')){loadWindowsGames();return}
    if(ev.target.closest('#apiClear')){clearAPI();return}
    if(ev.target.closest('#apiTest')){testAPI();return}
    if(ev.target.closest('#localStart')){startLocal();return}
    if(ev.target.closest('#localStop')){stopLocal();return}
    if(ev.target.closest('#localRefresh')){refresh();return}
    if(ev.target.closest('#saveRoutes')){saveRoutes();return}
    var dl=ev.target.closest('[data-dl]'); if(dl){downloadModel(dl.getAttribute('data-dl'));return}
    var selm=ev.target.closest('[data-select]'); if(selm){selectModel(selm.getAttribute('data-select'));return}
    var stm=ev.target.closest('[data-startmodel]'); if(stm){startModel(stm.getAttribute('data-startmodel'));return}
    var spm=ev.target.closest('[data-stopmodel]'); if(spm){stopModel(spm.getAttribute('data-stopmodel'));return}
    var delm=ev.target.closest('[data-delmodel]'); if(delm){deleteModel(delm.getAttribute('data-delmodel'));return}
    if(ev.target.closest('[data-cancel]')){cancelDownload();return}
    if(ev.target.closest('#apiVision')){S.visionDirty=true;return}
    if(ev.target.closest('#desktopData')){try{window.goplayDesktop('data')}catch(e){}return}
    if(ev.target.closest('#desktopQuit')){try{window.goplayDesktop('quit')}catch(e){}return}
    if(ev.target.closest('#reconnectBtn,[data-reconnect]')){reconnectHost();return}
    if(ev.target.closest('#androidPrepareAll')){prepareAll();return}
    if(ev.target.closest('#androidPermissionCenter')){go('settings');return}
    if(ev.target.closest('#androidVoiceCall')){startVoiceFromSettings();return}
    if(ev.target.closest('#androidVoiceStop')){if(S.voice)stopCall();else toast('当前没有通话',true);return}
    if(ev.target.closest('#androidRelease')){var ai=S.host&&S.host.activeInstance;if(ai)releaseToAI(ai);else toast('先选择一个游戏',true);return}
    if(ev.target.closest('#androidCapture')){grantPerm('capture');return}
    if(ev.target.closest('#androidStopCapture')){hostCall('stopCapture','已请求停止截屏');return}
    if(ev.target.closest('#androidAccessibility')){grantPerm('accessibility');return}
    if(ev.target.closest('#androidOverlayPerm')){grantPerm('overlay');return}
    if(ev.target.closest('#androidNotification')){grantPerm('notification');return}
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
    if(ev.key==='Escape'){closeTeaching();closeModal();return}
    if(ev.target.matches&&ev.target.matches('[data-input]')){
      if(ev.key==='Enter'&&!ev.shiftKey){ev.preventDefault();sendChat(ev.target.getAttribute('data-input'))}
    }
  });
  $('#fSource').addEventListener('change',function(){updateSourceUI();if(this.value==='android')loadAndroidGames();if(this.value==='windows')loadWindowsGames()});
  $('#fWindowsGame').addEventListener('change',function(){var o=this.selectedOptions[0];if(o&&o.getAttribute('data-name')){$('#fName').value=o.getAttribute('data-name');$('#fProfile').value='generic';if(!($('#fTargetWindow').value||'').trim())$('#fTargetWindow').value=o.getAttribute('data-name')}});
  $('#fAndroidGame').addEventListener('change',function(){
    var opt=this.selectedOptions[0]; if(!opt||!this.value) return;
    var label=opt.getAttribute('data-label')||this.value;
    $('#fName').value=label;
    if(!$('#fId').value||$('#fId').value.indexOf('game-')===0) $('#fId').value=nextID();
  });
  $('#createForm').addEventListener('submit',createGame);
  $('#apiForm').addEventListener('submit',saveAPI);
  $('#voiceForm').addEventListener('submit',saveVoice);
  $('#voiceTest').addEventListener('click',function(){speakReply('这是一句语音测试。现在你可以听到 AI 的声音了。')});
  $('#voiceClear').addEventListener('click',clearVoiceKey);
  $('#voiceClearBase').addEventListener('click',clearVoiceBase);
  $('#prefForm').addEventListener('submit',addPref);
  var sel=$('#takeoverSens');
  if(sel) sel.addEventListener('change',function(){
    if(window.GoPlayHost&&window.GoPlayHost.setTakeoverSensitivity){
      try{window.GoPlayHost.setTakeoverSensitivity(this.value);toast('接管灵敏度已更新')}catch(e){toast(e.message,true)}
    }else toast('当前环境不能调整接管灵敏度',true);
  });
}
function diagAction(action,id){
  if(action==='open_permissions') {go('settings');setTimeout(function(){var p=$('#androidPermPanel');if(p)p.scrollIntoView({behavior:'smooth'})},120);return}
  if(action==='start_capture'){grantPerm('capture');return}
  if(action==='retry_start'){act(id,'start');return}
  if(action==='open_ai'){go('ai');return}
  if(action==='release_human'){releaseToAI(id);return}
  if(action==='open_logs'){for(var i=0;i<S.instances.length;i++){if(S.instances[i].id===id){copyDiag(S.instances[i]);break}}return}
  if(action==='open_app'){var v=null;for(var j=0;j<S.instances.length;j++){if(S.instances[j].id===id)v=S.instances[j]}
    if(v&&window.GoPlayHost&&window.GoPlayHost.launchGame){try{window.GoPlayHost.launchGame(S.pkgs[id]||v.package||'');toast('正在打开游戏')}catch(e){toast(e.message,true)}}return}
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
        if(e.type==='instance.narration'&&e.data&&e.data.text) speakNarration(e.data.text);
        if(e.type==='instance.error'&&e.instance_id) ensureDiag(e.instance_id,true);
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
  refreshHostStatus();
  connectEvents();
  S.poll=setInterval(refresh,2500);
}
init();
})();
