'use strict';
const spaceName = decodeURIComponent(location.pathname.slice(1));
const $ = id => document.getElementById(id);
const apiBase = '/api/spaces/' + encodeURIComponent(spaceName);

let S = null;           // server state
let activeId = null;
let dirty = false;
let saveTimer = null;

const ICON_CLOSE = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>';
const ICON_DL = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="7 10 12 15 17 10"></polyline><line x1="12" y1="15" x2="12" y2="3"></line></svg>';
const ICON_TRASH = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"></path><path d="M10 11v6"></path><path d="M14 11v6"></path><path d="M9 6V4a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2"></path></svg>';

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
  return line ? (line.length > 40 ? line.slice(0,40) + '...' : line) : '未命名';
}
async function api(path, opts){
  const res = await fetch(apiBase + path, opts);
  let data = null;
  try { data = await res.json(); } catch(e) {}
  return {res, data};
}
function activeTab(){ return S ? S.tabs.find(t=>t.id===activeId) : null; }

function updateStats(){
  const text = $('editor').value || '';
  const chars = text.length;
  const lines = chars === 0 ? 0 : text.split('\n').length;
  const tabs = S ? S.tabs.length : 0;
  $('statTabs').textContent = tabs;
  $('statLines').textContent = lines;
  $('statChars').textContent = chars;
  $('editorStats').textContent = lines + ' 行 · ' + chars + ' 字符';
}

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
  if(!S.readonly) setStatus('✓ 读取成功', 'ok');
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
  $('saveBtn').disabled = ro;
  $('clearEditorBtn').classList.toggle('hidden', ro);
  if(ro) setStatus('只读模式', 'amber');
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
      x.type = 'button';
      x.className = 'x';
      x.innerHTML = ICON_CLOSE;
      x.title = '删除标签';
      x.setAttribute('aria-label', '删除标签');
      x.addEventListener('click', ev => { ev.stopPropagation(); deleteTab(t.id); });
      chip.appendChild(x);
    }
    attachDrag(chip, t.id);
    chip.addEventListener('dblclick', () => renameChip(chip, t));
    box.appendChild(chip);
  }
  updateStats();
}
function renderEditor(){
  const t = activeTab();
  $('editor').value = t ? t.content : '';
  $('titleInput').value = t ? t.title : '';
  $('editor').disabled = !t;
  $('titleInput').disabled = !t;
  $('saveBtn').disabled = !t || (S && S.readonly);
  $('clearEditorBtn').disabled = !t || (S && S.readonly);
  dirty = false;
  updateStats();
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
      if(t.id === activeId) $('titleInput').value = nt;
    }
    renderChips();
  };
  input.addEventListener('blur', commit);
  input.addEventListener('keydown', e => { if(e.key === 'Enter') input.blur(); if(e.key === 'Escape'){ done = true; renderChips(); } });
  input.addEventListener('pointerdown', e => e.stopPropagation());
  input.addEventListener('click', e => e.stopPropagation());
}

/* generic single-tab save (used by rename, autosave, and manual save) */
async function saveTab(t, patch){
  const {res, data} = await api('/tabs/' + t.id, {method:'PUT', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({title: patch.title !== undefined ? patch.title : t.title, content: patch.content !== undefined ? patch.content : t.content, rev: t.rev})});
  if(res.ok){ t.rev = data.rev; if(patch.title !== undefined) t.title = patch.title; if(patch.content !== undefined) t.content = patch.content; return true; }
  if(res.status === 409){ toast('内容已在其他设备更新，正在刷新'); await load(); return false; }
  toast((data && data.error) || '保存失败');
  return false;
}

/* autosave & manual save */
function setStatus(s, tone){
  const el = $('saveStatus');
  el.textContent = s;
  el.className = 'pill ' + (tone === 'ok' ? 'pill-green' : tone === 'amber' ? 'pill-amber' : 'pill-neutral');
}
function scheduleSave(){
  updateStats();
  if(S.readonly) return;
  dirty = true;
  setStatus('未保存...', 'amber');
  clearTimeout(saveTimer);
  saveTimer = setTimeout(() => saveNow(false), 700);
}
async function saveNow(manual){
  const t = activeTab();
  if(!t || (S && S.readonly)) return;
  if(!dirty){
    if(manual){
      setStatus('✓ 已保存 ' + new Date().toTimeString().slice(0,8), 'ok');
      toast('内容已保存');
    }
    return;
  }
  clearTimeout(saveTimer);
  setStatus('保存中...', 'neutral');
  const ok = await saveTab(t, {title: $('titleInput').value.trim(), content: $('editor').value});
  if(ok){
    dirty = false;
    setStatus('✓ 已保存 ' + new Date().toTimeString().slice(0,8), 'ok');
    renderChips();
    if(manual) toast('已保存');
  }
}
$('editor').addEventListener('input', scheduleSave);
$('titleInput').addEventListener('input', scheduleSave);
$('newTabBtn').addEventListener('click', newTab);
$('saveBtn').addEventListener('click', () => saveNow(true));

$('clearEditorBtn').addEventListener('click', () => {
  const t = activeTab();
  if(!t || S.readonly) return;
  if(!$('editor').value) return;
  if(!confirm('确定清空当前便签正文？')) return;
  $('editor').value = '';
  scheduleSave();
  $('editor').focus();
});

/* ---------- sidebar quick actions & collapse ---------- */
$('copyNoteBtn').addEventListener('click', async () => {
  const text = $('editor').value || '';
  if(!text){ toast('当前便签为空'); return; }
  try {
    await navigator.clipboard.writeText(text);
    toast('已复制到剪贴板');
  } catch(err){
    $('editor').select();
    document.execCommand('copy');
    toast('已复制到剪贴板');
  }
});

$('downloadNoteBtn').addEventListener('click', () => {
  const t = activeTab();
  if(!t){ toast('没有可下载的便签'); return; }
  const text = $('editor').value || '';
  const rawName = (displayTitle(t) || 'note').replace(/[\\/:*?"<>|]/g, '_');
  const blob = new Blob([text], {type: 'text/plain;charset=utf-8'});
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = rawName + '.txt';
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
});

$('sidebarToggleBtn').addEventListener('click', () => {
  const collapsed = $('sidebar').classList.toggle('collapsed');
  $('sidebarToggleBtn').classList.toggle('is-collapsed', collapsed);
});

function setMobileSidebar(open){
  $('sidebar').classList.toggle('mobile-open', open);
  $('sidebarBackdrop').classList.toggle('hidden', !open);
}
$('mobileSidebarBtn').addEventListener('click', () => setMobileSidebar(!$('sidebar').classList.contains('mobile-open')));
$('sidebarBackdrop').addEventListener('click', () => setMobileSidebar(false));

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
    const GAP = 6;
    const snap = chips.map(c => ({el: c, left: c.offsetLeft, width: c.offsetWidth, center: c.offsetLeft + c.offsetWidth / 2}));
    const me = snap[myIdx];
    const minDx = snap[0].left - me.left;
    const maxDx = (snap[snap.length - 1].left + snap[snap.length - 1].width) - (me.left + me.width);
    const grabPoint = me.left + (e.clientX - chip.getBoundingClientRect().left);
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
      const rawDx = ev.clientX - startX;
      if(!moved && Math.abs(rawDx) > 6){ moved = true; chip.classList.add('dragging'); }
      if(!moved) return;
      const dx = Math.max(minDx, Math.min(maxDx, rawDx));
      chip.style.transform = 'translateX(' + dx + 'px)';
      const pointX = grabPoint + rawDx;
      target = myIdx;
      for(let i = 0; i < snap.length; i++){
        if(i === myIdx) continue;
        if(i < myIdx && pointX < snap[i].center) target = Math.min(target, i);
        if(i > myIdx && pointX > snap[i].center) target = Math.max(target, i);
      }
      for(let i = 0; i < snap.length; i++){
        if(i !== myIdx) snap[i].el.style.transform = shiftFor(i);
      }
    };
    const cleanup = () => {
      for(const s of snap){ s.el.classList.add('noanim'); s.el.style.transform = ''; }
      chip.classList.remove('dragging');
      void box.offsetWidth;
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
  $('fileCountBadge').textContent = S.files.length;
  if(!S.files.length){
    const d = document.createElement('div'); d.className = 'file-empty'; d.textContent = '暂无暂存文件';
    list.appendChild(d); return;
  }
  for(const f of S.files){
    const row = document.createElement('div'); row.className = 'file-row';
    const info = document.createElement('div'); info.className = 'file-info';
    const name = document.createElement('span'); name.className = 'fname'; name.textContent = f.filename; name.title = f.filename;
    const meta = document.createElement('div'); meta.className = 'file-meta'; meta.textContent = fmtSize(f.size) + ' · ' + fmtRemain(f.expires_at);
    info.append(name, meta);

    const acts = document.createElement('div'); acts.className = 'file-actions';
    const dl = document.createElement('a');
    dl.className = 'file-icon-btn';
    dl.href = apiBase + '/files/' + f.id + '/download';
    dl.innerHTML = ICON_DL;
    dl.title = '下载文件';
    dl.setAttribute('aria-label', '下载文件');
    acts.appendChild(dl);

    if(!S.readonly){
      const del = document.createElement('button');
      del.type = 'button';
      del.className = 'file-icon-btn danger';
      del.innerHTML = ICON_TRASH;
      del.title = '删除文件';
      del.setAttribute('aria-label', '删除文件');
      del.addEventListener('click', async () => {
        if(!confirm('删除文件「' + f.filename + '」？')) return;
        const {res, data} = await api('/files/' + f.id, {method:'DELETE'});
        if(res.ok){ S.files = S.files.filter(x=>x.id!==f.id); renderFiles(); toast('已删除'); }
        else toast((data && data.error) || '删除失败');
      });
      acts.appendChild(del);
    }
    row.append(info, acts);
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
    const row = document.createElement('div'); row.className = 'file-row upload-row';
    const top = document.createElement('div'); top.className = 'upload-top';
    const nm = document.createElement('span'); nm.className = 'fname'; nm.textContent = file.name; nm.title = file.name;
    const st = document.createElement('span'); st.className = 'ustatus muted tiny'; st.textContent = '上传中...';
    top.append(nm, st);
    const prog = document.createElement('div'); prog.className = 'progress'; prog.innerHTML = '<i></i>';
    row.append(top, prog);
    $('uploadList').appendChild(row);
    await uploadFile(file, days, row);
    setTimeout(()=>row.remove(), 4000);
  }
}
$('pickBtn').addEventListener('click', () => $('fileInput').click());
$('fileInput').addEventListener('change', e => { uploadFiles(e.target.files); e.target.value = ''; });
const dz = $('dropZone');
dz.addEventListener('dragover', e => { e.preventDefault(); dz.classList.add('over'); });
dz.addEventListener('dragleave', () => dz.classList.remove('over'));
dz.addEventListener('drop', e => { e.preventDefault(); dz.classList.remove('over'); uploadFiles(e.dataTransfer.files); });

/* ---------- settings modal ---------- */
function openSettings(){ $('settingsPanel').classList.remove('hidden'); }
function closeSettings(){ $('settingsPanel').classList.add('hidden'); }
$('settingsBtn').addEventListener('click', openSettings);
$('settingsCloseBtn').addEventListener('click', closeSettings);
$('settingsPanel').addEventListener('click', e => { if(e.target === $('settingsPanel')) closeSettings(); });

$('setPwBtn').addEventListener('click', async () => {
  const pw = $('pwInput').value;
  if(!pw){ toast('请输入新密码'); return; }
  const {res, data} = await api('/settings', {method:'PUT', headers:{'Content-Type':'application/json'}, body: JSON.stringify({password: pw})});
  if(res.ok){ $('pwInput').value=''; toast('密码已设置'); load(); }
  else toast((data && data.error) || '设置失败');
});
$('clearPwBtn').addEventListener('click', async () => {
  if(!confirm('确定清除密码？任何人知道空间名就能直接打开。')) return;
  const {res, data} = await api('/settings', {method:'PUT', headers:{'Content-Type':'application/json'}, body: JSON.stringify({clear_password: true})});
  if(res.ok){ toast('密码已清除'); load(); }
  else toast((data && data.error) || '操作失败');
});
$('roChk').addEventListener('change', async e => {
  const {res, data} = await api('/settings', {method:'PUT', headers:{'Content-Type':'application/json'}, body: JSON.stringify({readonly: e.target.checked})});
  if(res.ok){ S.readonly = e.target.checked; applyReadonly(); renderChips(); $('roBadge').classList.toggle('hidden', !S.readonly); toast(S.readonly ? '已开启只读' : '已关闭只读'); }
  else { e.target.checked = !e.target.checked; toast((data && data.error) || '设置失败'); }
});

/* ---------- shortcuts & lifecycle ---------- */
window.addEventListener('keydown', e => {
  if((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's'){ e.preventDefault(); saveNow(true); }
  if(e.key === 'Escape' && !$('settingsPanel').classList.contains('hidden')) closeSettings();
});
window.addEventListener('beforeunload', e => {
  if(dirty){ e.preventDefault(); e.returnValue = ''; }
});

load();