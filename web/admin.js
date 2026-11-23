'use strict';
const $ = id => document.getElementById(id);
function esc(s){
  return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}
function fmtSize(n){
  if(n < 1024) return n + ' B';
  if(n < 1024*1024) return (n/1024).toFixed(1) + ' KB';
  if(n < 1024*1024*1024) return (n/1024/1024).toFixed(1) + ' MB';
  return (n/1024/1024/1024).toFixed(2) + ' GB';
}
function fmtTime(s){
  const d = new Date(s);
  return d.getFullYear() + '-' + String(d.getMonth()+1).padStart(2,'0') + '-' + String(d.getDate()).padStart(2,'0') + ' ' + String(d.getHours()).padStart(2,'0') + ':' + String(d.getMinutes()).padStart(2,'0');
}
function toast(msg){
  const t = $('toast'); t.textContent = msg; t.classList.add('show');
  clearTimeout(t._h); t._h = setTimeout(()=>t.classList.remove('show'), 2600);
}

async function loadOverview(){
  const res = await fetch('/api/admin/overview');
  if(res.status === 401){ $('loginBox').classList.remove('hidden'); $('mainBox').classList.add('hidden'); return; }
  const data = await res.json().catch(()=>null);
  if(!res.ok){ toast((data && data.error) || '加载失败'); $('loginBox').classList.remove('hidden'); return; }
  $('loginBox').classList.add('hidden');
  $('mainBox').classList.remove('hidden');
  $('summary').textContent = '共 ' + data.spaces.length + ' 个便签 · 标签共 ' + fmtSize(data.total_tabs_size) + ' · 文件共 ' + fmtSize(data.total_files_size);
  const box = $('spaceList'); box.innerHTML = '';
  if(!data.spaces.length){
    box.innerHTML = '<div class="admin-card muted">暂无便签数据</div>'; return;
  }
  for(const sp of data.spaces){
    const card = document.createElement('div'); card.className = 'admin-card';
    let tabsRows = sp.tabs.map(t =>
      '<tr><td>' + esc(t.title || '<span class="muted">（无标题，取首行）</span>') + '</td><td>' + fmtSize(t.size) + '</td>' +
      '<td><button class="danger" data-act="deltab" data-space="' + esc(sp.name) + '" data-id="' + esc(t.id) + '">删除</button></td></tr>').join('');
    if(!sp.tabs.length) tabsRows = '<tr><td colspan="3" class="muted">无标签</td></tr>';
    let fileRows = sp.files.map(f =>
      '<tr><td>' + esc(f.filename) + '</td><td>' + fmtSize(f.size) + '</td><td>' + fmtTime(f.expires_at) + '</td>' +
      '<td><button class="danger" data-act="delfile" data-space="' + esc(sp.name) + '" data-id="' + esc(f.id) + '">删除</button></td></tr>').join('');
    if(!sp.files.length) fileRows = '<tr><td colspan="4" class="muted">无文件</td></tr>';
    card.innerHTML =
      '<div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">' +
        '<h3><a href="/' + encodeURIComponent(sp.name) + '" target="_blank">' + esc(sp.name) + '</a></h3>' +
        (sp.has_password ? '<span class="badge">已设密码</span>' : '') +
        (sp.readonly ? '<span class="badge ro">只读</span>' : '') +
        '<span style="flex:1"></span>' +
        '<button class="danger" data-act="delspace" data-space="' + esc(sp.name) + '">删除整个便签</button>' +
      '</div>' +
      '<div class="muted small">创建 ' + fmtTime(sp.created_at) + ' · 最近访问 ' + fmtTime(sp.last_access) +
      ' · ' + sp.tabs.length + ' 个标签（' + fmtSize(sp.tabs_size) + '）· ' + sp.files.length + ' 个文件（' + fmtSize(sp.files_size) + '）</div>' +
      '<table style="margin-top:10px"><tr><th>标签标题</th><th>占用</th><th></th></tr>' + tabsRows + '</table>' +
      '<table style="margin-top:8px"><tr><th>文件</th><th>大小</th><th>到期</th><th></th></tr>' + fileRows + '</table>';
    box.appendChild(card);
  }
}

document.addEventListener('click', async e => {
  const btn = e.target.closest('button[data-act]');
  if(!btn) return;
  const act = btn.dataset.act, space = btn.dataset.space, id = btn.dataset.id;
  let url, msg;
  if(act === 'delspace'){ url = '/api/admin/spaces/' + encodeURIComponent(space); msg = '删除便签「' + space + '」及其全部标签和文件？不可恢复。'; }
  if(act === 'deltab'){ url = '/api/admin/spaces/' + encodeURIComponent(space) + '/tabs/' + id; msg = '删除这个标签？不可恢复。'; }
  if(act === 'delfile'){ url = '/api/admin/spaces/' + encodeURIComponent(space) + '/files/' + id; msg = '删除这个文件？不可恢复。'; }
  if(!confirm(msg)) return;
  const res = await fetch(url, {method:'DELETE'});
  const data = await res.json().catch(()=>null);
  if(res.ok){ toast('已删除'); loadOverview(); }
  else toast((data && data.error) || '删除失败');
});

$('loginForm').addEventListener('submit', async e => {
  e.preventDefault();
  const res = await fetch('/api/admin/login', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({password: $('adminPw').value})});
  const data = await res.json().catch(()=>null);
  if(res.ok){ $('loginErr').textContent = ''; $('adminPw').value = ''; loadOverview(); }
  else $('loginErr').textContent = (data && data.error) || '登录失败';
});
$('refreshBtn').addEventListener('click', loadOverview);
$('logoutBtn').addEventListener('click', async () => { await fetch('/api/admin/logout', {method:'POST'}); loadOverview(); });

loadOverview();
