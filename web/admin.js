'use strict';
const $ = id => document.getElementById(id);

const ICON_LOCK = '<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"></rect><path d="M7 11V7a5 5 0 0 1 10 0v4"></path></svg>';
const ICON_EXT = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path><polyline points="15 3 21 3 21 9"></polyline><line x1="10" y1="14" x2="21" y2="3"></line></svg>';
const ICON_TRASH = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"></path><path d="M10 11v6"></path><path d="M14 11v6"></path><path d="M9 6V4a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2"></path></svg>';

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

  $('summary').textContent = '共 ' + data.spaces.length + ' 个空间';
  $('kpiSpaces').textContent = data.spaces.length;
  $('kpiTabsSize').textContent = fmtSize(data.total_tabs_size);
  $('kpiFilesSize').textContent = fmtSize(data.total_files_size);

  const box = $('spaceList'); box.innerHTML = '';
  if(!data.spaces.length){
    box.innerHTML = '<div class="admin-card admin-empty">暂无便签数据</div>';
    return;
  }
  for(const sp of data.spaces){
    const card = document.createElement('section');
    card.className = 'admin-card';

    let tabsRows = sp.tabs.map(t =>
      '<tr>' +
        '<td>' + (t.title ? esc(t.title) : '<span class="muted">（无标题，取首行）</span>') + '</td>' +
        '<td class="col-num">' + fmtSize(t.size) + '</td>' +
        '<td class="col-act"><button type="button" class="danger sm-btn" data-act="deltab" data-space="' + esc(sp.name) + '" data-id="' + esc(t.id) + '">删除</button></td>' +
      '</tr>'
    ).join('');
    if(!sp.tabs.length) tabsRows = '<tr><td colspan="3" class="muted">无标签</td></tr>';

    let fileRows = sp.files.map(f =>
      '<tr>' +
        '<td class="col-break">' + esc(f.filename) + '</td>' +
        '<td class="col-num">' + fmtSize(f.size) + '</td>' +
        '<td class="col-time">' + fmtTime(f.expires_at) + '</td>' +
        '<td class="col-act"><button type="button" class="danger sm-btn" data-act="delfile" data-space="' + esc(sp.name) + '" data-id="' + esc(f.id) + '">删除</button></td>' +
      '</tr>'
    ).join('');
    if(!sp.files.length) fileRows = '<tr><td colspan="4" class="muted">无文件</td></tr>';

    card.innerHTML =
      '<div class="admin-card-head">' +
        '<div class="admin-card-title-row">' +
          '<h3><a href="/' + encodeURIComponent(sp.name) + '" target="_blank" rel="noopener">' + esc(sp.name) + ICON_EXT + '</a></h3>' +
          (sp.has_password ? '<span class="pill pill-green">' + ICON_LOCK + '已保护</span>' : '') +
          (sp.readonly ? '<span class="pill pill-amber">只读</span>' : '') +
        '</div>' +
        '<button type="button" class="danger sm-btn icon-btn-sm" data-act="delspace" data-space="' + esc(sp.name) + '">' + ICON_TRASH + '<span>删除整个便签</span></button>' +
      '</div>' +
      '<div class="admin-card-meta">' +
        '<span>创建于 ' + fmtTime(sp.created_at) + '</span>' +
        '<span class="meta-dot">·</span>' +
        '<span>最近访问 ' + fmtTime(sp.last_access) + '</span>' +
        '<span class="meta-dot">·</span>' +
        '<span>' + sp.tabs.length + ' 个标签（' + fmtSize(sp.tabs_size) + '）</span>' +
        '<span class="meta-dot">·</span>' +
        '<span>' + sp.files.length + ' 个文件（' + fmtSize(sp.files_size) + '）</span>' +
      '</div>' +
      '<div class="admin-table-wrap">' +
        '<table class="admin-table"><thead><tr><th>标签标题</th><th class="col-num">占用</th><th class="col-act">操作</th></tr></thead><tbody>' + tabsRows + '</tbody></table>' +
      '</div>' +
      '<div class="admin-table-wrap">' +
        '<table class="admin-table"><thead><tr><th>暂存文件</th><th class="col-num">大小</th><th class="col-time">到期时间</th><th class="col-act">操作</th></tr></thead><tbody>' + fileRows + '</tbody></table>' +
      '</div>';
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
