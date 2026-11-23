'use strict';
const spaceName = decodeURIComponent(location.pathname.slice(1));
const $ = id => document.getElementById(id);
const apiBase = '/api/spaces/' + encodeURIComponent(spaceName);

let S = null;            // server state
let activeId = null;
let dirty = false;
let saveTimer = null;

function toast(msg){
  const t = $('toast'); t.textContent = msg; t.classList.add('show');
  clearTimeout(t._h); t._h = setTimeout(()=>t.classList.remove('show'), 2600);
}
function fmtSize(n){
  if(n < 1024) return n + ' B';
  if(n < 1024*1024) return (n/1024).toFixed(1) + ' KB';
  if(n < 1024*1024*1024) return (n/1024/1024).toFixed(1) + ' MB';
  return (n/1024/1024/1024).toFixed(2) + ' GB';
}
function fmtRemain(exp){
  let ms = new Date(exp) - Date.now();
  if(ms <= 0) return '已过期';
  const h = Math.floor(ms/3600000), d = Math.floor(h/24);
  if(d >= 1) return '剩余 ' + d + ' 天 ' + (h%24) + ' 小时';
  if(h >= 1) return '剩余 ' + h + ' 小时';
  return '剩余 ' + Math.max(1, Math.floor(ms/60000)) + ' 分钟';
}
function displayTitle(t){
  if(t.title) return t.title;
  const line = (t.content || '').split('\n').map(s=>s.trim()).find(s=>s.length>0);
  return line ? (line.length > 40 ? line.slice(0,40) + '…' : line) : '未命名';
}
async function api(path, opts){
  const res = await fetch(apiBase + path, opts);
  let data = null;
  try { data = await res.json(); } catch(e) {}
  return {res, data};
}
function activeTab(){ return S ? S.tabs.find(t=>t.id===activeId) : null; }

/* ---------- load & unlock ---------- */
async function load(){
  const {res, data} = await api('/state');
  if(res.status === 401){
    $('unlockName').textContent = spaceName;
    $('unlockView').classList.remove('hidden');
    $('mainView').classList.add('hidden');
    return;
  }
  if(!res.ok){ toast((data && data.error) || '加载失败'); return; }
  S = data;
  $('unlockView').classList.add('hidden');
  $('mainView').classList.remove('hidden');
  $('spaceName').textContent = S.name;
  $('roBadge').classList.toggle('hidden', !S.readonly);
  $('pwBadge').classList.toggle('hidden', !S.has_password);
  $('roChk').checked = S.readonly;
  $('fileMaxHint').textContent = fmtSize(S.file_max);
  $('directLink').textContent = location.origin + '/' + S.name + '@你的密码';
  if(!S.tabs.some(t=>t.id===activeId)) activeId = S.tabs.length ? S.tabs[0].id : null;
  renderChips(); renderEditor(); renderFiles();
  applyReadonly();
}
$('unlockForm').addEventListener('submit', async e => {
  e.preventDefault();
  const {res, data} = await api('/unlock', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({password: $('unlockPw').value})});
  if(res.ok){ $('unlockErr').textContent=''; load(); }
  else $('unlockErr').textContent = (data && data.error) || '解锁失败';
});

function applyReadonly(){
  const ro = S.readonly;
  $('editor').readOnly = ro;
  $('titleInput').readOnly = ro;
  $('newTabBtn').disabled = ro;
  $('pickBtn').disabled = ro;
  if(ro) setStatus('只读模式');
}

/* ---------- tabs ---------- */
function renderChips(){
  const box = $('chips'); box.innerHTML = '';
  for(const t of S.tabs){
    const chip = document.createElement('div');
    chip.className = 'chip' + (t.id===activeId ? ' active' : '');
    chip.dataset.id = t.id;
    const label = document.createElement('span');
    label.className = 'label'; label.textContent = displayTitle(t);
    chip.appendChild(label);
    if(!S.readonly){
      const x = document.createElement('button');
      x.className = 'x'; x.textContent = '✕'; x.title = '删除标签';
      x.addEventListener('click', ev => { ev.stopPropagation(); deleteTab(t.id); });
      chip.appendChild(x);
    }
    attachDrag(chip, t.id);
    chip.addEventListener('dblclick', () => renameChip(chip, t));
    box.appendChild(chip);
  }
}
function renderEditor(){
  const t = activeTab();
  $('editor').value = t ? t.content : '';
  $('titleInput').value = t ? t.title : '';
  $('editor').disabled = !t;
  $('titleInput').disabled = !t;
  dirty = false;
}
function selectTab(id){
  if(id === activeId) return;
  if(dirty) saveNow();
  activeId = id;
  renderChips(); renderEditor();
}
async function newTab(){
  const {res, data} = await api('/tabs', {method:'POST', headers:{'Content-Type':'application/json'}, body:'{}'});
  if(!res.ok){ toast((data && data.error) || '新建失败'); return; }
  S.tabs.push(data); activeId = data.id;
  renderChips(); renderEditor(); $('editor').focus();
}
async function deleteTab(id){
  const t = S.tabs.find(x=>x.id===id);
  if(!confirm('删除标签「' + displayTitle(t) + '」？此操作不可恢复。')) return;
  const {res, data} = await api('/tabs/' + id, {method:'DELETE'});
  if(!res.ok){ toast((data && data.error) || '删除失败'); return; }
  S.tabs = S.tabs.filter(x=>x.id!==id);
  if(activeId === id) activeId = S.tabs.length ? S.tabs[0].id : null;
  renderChips(); renderEditor(); toast('已删除');
}
function renameChip(chip, t){
  if(S.readonly) return;
  const label = chip.querySelector('.label');
  const input = document.createElement('input');
  input.value = t.title || '';
  input.placeholder = '输入标题';
  label.replaceWith(input);
  input.focus(); input.select();
  let done = false;
  const commit = async () => {
    if(done) return; done = true;
    const nt = input.value.trim();
    if(nt !== t.title){
      const ok = await saveTab(t, {title: nt});
      if(!ok){ renderChips(); return; }
    }
    renderChips();
  };
  input.addEventListener('blur', commit);
  input.addEventListener('keydown', e => { if(e.key === 'Enter') input.blur(); if(e.key === 'Escape'){ done = true; renderChips(); } });
  input.addEventListener('pointerdown', e => e.stopPropagation());
  input.addEventListener('click', e => e.stopPropagation());
}

/* generic single-tab save (used by rename and autosave) */
async function saveTab(t, patch){
  const {res, data} = await api('/tabs/' + t.id, {method:'PUT', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({title: patch.title !== undefined ? patch.title : t.title, content: patch.content !== undefined ? patch.content : t.content, rev: t.rev})});
  if(res.ok){ t.rev = data.rev; if(patch.title !== undefined) t.title = patch.title; if(patch.content !== undefined) t.content = patch.content; return true; }
  if(res.status === 409){ toast('内容已在其他设备更新，正在刷新'); await load(); return false; }
  toast((data && data.error) || '保存失败');
  return false;
}

/* autosave */
function setStatus(s){ $('saveStatus').textContent = s; }
function scheduleSave(){
  if(S.readonly) return;
  dirty = true; setStatus('未保存…');
  clearTimeout(saveTimer);
  saveTimer = setTimeout(saveNow, 700);
}
async function saveNow(){
  const t = activeTab(); if(!t || !dirty) return;
  clearTimeout(saveTimer);
  setStatus('保存中…');
  const ok = await saveTab(t, {title: $('titleInput').value.trim(), content: $('editor').value});
  if(ok){ dirty = false; setStatus('已保存 ' + new Date().toTimeString().slice(0,8)); renderChips(); }
}
$('editor').addEventListener('input', scheduleSave);
$('titleInput').addEventListener('input', scheduleSave);
$('newTabBtn').addEventListener('click', newTab);

/* ---------- drag to reorder ---------- */
function attachDrag(chip, id){
  chip.addEventListener('pointerdown', e => {
    if(S.readonly) return;
    if(e.button !== undefined && e.button !== 0) return;
    if(e.target.closest('.x') || e.target.tagName === 'INPUT') return;
    const box = $('chips');
    const chips = [...box.querySelectorAll('.chip')];
    const myIdx = chips.indexOf(chip);
    if(myIdx < 0) return;
    // Layout snapshot. During the gesture the dragged node is NEVER moved in
    // the DOM (siblings only shift via transforms), so pointer capture stays
    // stable; the real DOM reorder happens once, on release.
    const GAP = 6;
    const snap = chips.map(c => ({el: c, left: c.offsetLeft, width: c.offsetWidth, center: c.offsetLeft + c.offsetWidth / 2}));
    const me = snap[myIdx];
    const minDx = snap[0].left - me.left;
    const maxDx = (snap[snap.length - 1].left + snap[snap.length - 1].width) - (me.left + me.width);
    const startX = e.clientX;
    let moved = false, target = myIdx, finished = false;
    try { chip.setPointerCapture(e.pointerId); } catch(err) {}
    const shiftFor = i => {
      if(i === myIdx) return '';
      const shift = me.width + GAP;
      if(target < myIdx && i >= target && i < myIdx) return 'translateX(' + shift + 'px)';
      if(target > myIdx && i > myIdx && i <= target) return 'translateX(' + (-shift) + 'px)';
      return '';
    };
    const onMove = ev => {
      let dx = ev.clientX - startX;
      if(!moved && Math.abs(dx) > 6){ moved = true; chip.classList.add('dragging'); }
      if(!moved) return;
      dx = Math.max(minDx, Math.min(maxDx, dx));
      chip.style.transform = 'translateX(' + dx + 'px)';
      const myCenter = me.center + dx;
      target = myIdx;
      for(let i = 0; i < snap.length; i++){
        if(i === myIdx) continue;
        if(i < myIdx && myCenter < snap[i].center) target = Math.min(target, i);
        if(i > myIdx && myCenter > snap[i].center) target = Math.max(target, i);
      }
      for(let i = 0; i < snap.length; i++){
        if(i !== myIdx) snap[i].el.style.transform = shiftFor(i);
      }
    };
    const cleanup = () => {
      for(const s of snap){ s.el.classList.add('noanim'); s.el.style.transform = ''; }
      chip.classList.remove('dragging');
      void box.offsetWidth; // reflow so clearing transforms is not animated
      for(const s of snap) s.el.classList.remove('noanim');
    };
    const detach = () => {
      chip.removeEventListener('pointermove', onMove);
      chip.removeEventListener('pointerup', onUp);
      chip.removeEventListener('pointercancel', onCancel);
    };
    const onUp = async () => {
      if(finished) return; finished = true;
      detach();
      if(!moved){ cleanup(); selectTab(id); return; }
      const from = myIdx, to = target;
      cleanup();
      if(to !== from){
        const ref = to < from ? chips[to] : chips[to].nextSibling;
        box.insertBefore(chip, ref);
        const order = [...box.querySelectorAll('.chip')].map(c => c.dataset.id);
        S.tabs.sort((a, b) => order.indexOf(a.id) - order.indexOf(b.id));
        const {res, data} = await api('/tabs/order', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({order})});
        if(!res.ok){ toast((data && data.error) || '排序保存失败'); renderChips(); }
      }
    };
    const onCancel = () => {
      if(finished) return; finished = true;
      detach();
      cleanup();
    };
    chip.addEventListener('pointermove', onMove);
    chip.addEventListener('pointerup', onUp);
    chip.addEventListener('pointercancel', onCancel);
  });
}

/* ---------- files ---------- */
function renderFiles(){
  const list = $('fileList'); list.innerHTML = '';
  if(!S.files.length){
    const d = document.createElement('div'); d.className = 'muted small'; d.textContent = '暂无文件';
    list.appendChild(d); return;
  }
  for(const f of S.files){
    const row = document.createElement('div'); row.className = 'file-row';
    const name = document.createElement('span'); name.className = 'fname'; name.textContent = f.filename;
    const meta = document.createElement('span'); meta.className = 'muted small'; meta.textContent = fmtSize(f.size) + ' · ' + fmtRemain(f.expires_at);
    const dl = document.createElement('a'); dl.href = apiBase + '/files/' + f.id + '/download'; dl.textContent = '下载';
    row.append(name, meta, dl);
    if(!S.readonly){
      const del = document.createElement('button'); del.className = 'danger'; del.textContent = '删除';
      del.addEventListener('click', async () => {
        if(!confirm('删除文件「' + f.filename + '」？')) return;
        const {res, data} = await api('/files/' + f.id, {method:'DELETE'});
        if(res.ok){ S.files = S.files.filter(x=>x.id!==f.id); renderFiles(); toast('已删除'); }
        else toast((data && data.error) || '删除失败');
      });
      row.appendChild(del);
    }
    list.appendChild(row);
  }
}
async function uploadFile(file, days, row){
  const bar = row.querySelector('.progress > i');
  const status = row.querySelector('.ustatus');
  const init = await api('/files/init', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({filename: file.name, size: file.size, days})});
  if(!init.res.ok){ status.textContent = (init.data && init.data.error) || '无法开始上传'; return; }
  const uploadId = init.data.upload_id, chunk = init.data.chunk_size;
  let offset = 0;
  try {
    while(offset < file.size){
      const slice = file.slice(offset, offset + chunk);
      const res = await fetch(apiBase + '/files/' + uploadId + '/chunk?offset=' + offset, {method:'PUT', body: slice});
      const data = await res.json().catch(()=>null);
      if(!res.ok){ throw new Error((data && data.error) || '上传失败'); }
      offset = data.received;
      bar.style.width = Math.round(offset / file.size * 100) + '%';
    }
    const done = await api('/files/' + uploadId + '/complete', {method:'POST'});
    if(!done.res.ok) throw new Error((done.data && done.data.error) || '上传收尾失败');
    status.textContent = '完成';
    S.files.push(done.data); renderFiles();
  } catch(err){
    status.textContent = err.message;
    await api('/uploads/' + uploadId, {method:'DELETE'}).catch(()=>{});
  }
}
async function uploadFiles(files){
  const days = parseInt($('daysSel').value, 10) || 1;
  for(const file of files){
    if(file.size > S.file_max){ toast('「' + file.name + '」超过单文件上限 ' + fmtSize(S.file_max)); continue; }
    const row = document.createElement('div'); row.className = 'file-row';
    const nm = document.createElement('span'); nm.className = 'fname'; nm.textContent = file.name;
    const prog = document.createElement('div'); prog.className = 'progress'; prog.innerHTML = '<i></i>';
    const st = document.createElement('span'); st.className = 'ustatus muted small'; st.textContent = '上传中…';
    row.append(nm, prog, st);
    $('uploadList').appendChild(row);
    await uploadFile(file, days, row);
    setTimeout(()=>row.remove(), 4000);
  }
}
$('pickBtn').addEventListener('click', () => $('fileInput').click());
$('fileInput').addEventListener('change', e => { uploadFiles([...e.target.files]); e.target.value = ''; });
const dz = $('dropZone');
dz.addEventListener('dragover', e => { e.preventDefault(); dz.classList.add('over'); });
dz.addEventListener('dragleave', () => dz.classList.remove('over'));
dz.addEventListener('drop', e => { e.preventDefault(); dz.classList.remove('over'); if(e.dataTransfer.files.length) uploadFiles([...e.dataTransfer.files]); });

/* ---------- panels & settings ---------- */
$('filesBtn').addEventListener('click', () => { $('filesPanel').classList.toggle('hidden'); $('settingsPanel').classList.add('hidden'); });
$('settingsBtn').addEventListener('click', () => { $('settingsPanel').classList.toggle('hidden'); $('filesPanel').classList.add('hidden'); });
$('setPwBtn').addEventListener('click', async () => {
  const pw = $('pwInput').value;
  if(!pw){ toast('请先输入密码'); return; }
  const {res, data} = await api('/password', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({password: pw})});
  if(res.ok){ $('pwInput').value=''; toast('访问密码已设置'); load(); }
  else toast((data && data.error) || '设置失败');
});
$('clearPwBtn').addEventListener('click', async () => {
  if(!confirm('清除访问密码后，任何人打开链接都能查看这个便签。继续？')) return;
  const {res, data} = await api('/password', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({password: ''})});
  if(res.ok){ toast('访问密码已清除'); load(); }
  else toast((data && data.error) || '清除失败');
});
$('roChk').addEventListener('change', async e => {
  const {res, data} = await api('/readonly', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({readonly: e.target.checked})});
  if(res.ok){ toast(e.target.checked ? '已开启只读模式' : '已关闭只读模式'); load(); }
  else { e.target.checked = !e.target.checked; toast((data && data.error) || '设置失败'); }
});

/* ---------- polling for cross-device updates ---------- */
setInterval(async () => {
  if(document.hidden || !S) return;
  const {res, data} = await api('/state');
  if(!res.ok) return;
  const newFiles = JSON.stringify(data.files) !== JSON.stringify(S.files);
  const newTabsMeta = data.tabs.map(t=>t.id+':'+t.rev).join() !== S.tabs.map(t=>t.id+':'+t.rev).join();
  if(newFiles){ S.files = data.files; renderFiles(); }
  if(newTabsMeta && !dirty){
    S.tabs = data.tabs; S.readonly = data.readonly;
    renderChips(); renderEditor(); applyReadonly();
  }
}, 5000);

load();
