// ND Code Search web UI. Plain JavaScript, no build step.
'use strict';

const $ = (sel, el = document) => el.querySelector(sel);
const $$ = (sel, el = document) => Array.from(el.querySelectorAll(sel));
const app = $('#app');
const qbox = $('#q');

const esc = s => String(s).replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
const encPath = p => p.split('/').map(encodeURIComponent).join('/');
const fmtNum = n => n.toLocaleString();
function fmtBytes(n) {
  if (n < 1024) return n + ' B';
  const u = ['KB', 'MB', 'GB', 'TB']; let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return n.toFixed(n < 10 ? 1 : 0) + ' ' + u[i];
}
function fmtAgo(t) {
  const s = (Date.now() - new Date(t).getTime()) / 1000;
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + ' min ago';
  if (s < 86400) return Math.floor(s / 3600) + ' h ago';
  return new Date(t).toLocaleDateString();
}

function fileURL(repo, path, opts = {}) {
  let u = '/f/' + encodeURIComponent(repo) + (path ? '/' + encPath(path) : '');
  const p = new URLSearchParams();
  if (opts.q) p.set('q', opts.q);
  if (opts.rev) p.set('rev', opts.rev);
  if (opts.at && opts.at !== path) p.set('at', opts.at);
  if (p.toString()) u += '?' + p.toString().replace(/\+/g, '%20');
  if (opts.line) u += '#L' + opts.line;
  return u;
}
const fmtDate = t => new Date(t * 1000).toLocaleDateString(undefined, {year: 'numeric', month: 'short', day: 'numeric'});
const searchURL = q => '/search?q=' + encodeURIComponent(q);

// ---- API ----

const inflight = {};
async function api(name, params, key) {
  // Requests with the same key cancel each other (newest wins).
  if (key && inflight[key]) inflight[key].abort();
  const ctl = new AbortController();
  if (key) inflight[key] = ctl;
  const url = '/api/' + name + (params ? '?' + new URLSearchParams(params) : '');
  try {
    const r = await fetch(url, {signal: ctl.signal});
    const data = await r.json().catch(() => ({error: r.statusText}));
    if (!r.ok) throw new Error(data.error || r.statusText);
    return data;
  } finally {
    if (key && inflight[key] === ctl) delete inflight[key];
  }
}
const isAbort = e => e && e.name === 'AbortError';

function toast(msg) {
  const t = $('#toast');
  t.textContent = msg; t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => t.hidden = true, 2200);
}

const icons = {
  dir: '<svg class="ico dir" viewBox="0 0 24 24"><path d="M10 4H4a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-8l-2-2z"/></svg>',
  file: '<svg class="ico" viewBox="0 0 24 24"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8l-6-6zm0 2.5L17.5 8H14V4.5z"/></svg>',
  chevron: '<svg viewBox="0 0 24 24"><path d="M9 6l6 6-6 6z"/></svg>',
  raw: '<svg viewBox="0 0 24 24"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8zm-1 7V3.5L18.5 9zM8 13h8v2H8zm0 4h8v2H8z"/></svg>',
  copy: '<svg viewBox="0 0 24 24"><path d="M16 1H4a2 2 0 0 0-2 2v14h2V3h12zm3 4H8a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h11a2 2 0 0 0 2-2V7a2 2 0 0 0-2-2zm0 16H8V7h11z"/></svg>',
  link: '<svg viewBox="0 0 24 24"><path d="M3.9 12a3.1 3.1 0 0 1 3.1-3.1h4V7H7a5 5 0 0 0 0 10h4v-1.9H7A3.1 3.1 0 0 1 3.9 12zM8 13h8v-2H8v2zm9-6h-4v1.9h4a3.1 3.1 0 0 1 0 6.2h-4V17h4a5 5 0 0 0 0-10z"/></svg>',
  history: '<svg class="ico" viewBox="0 0 24 24"><path d="M13 3a9 9 0 0 0-9 9H1l3.9 3.9.1.1L9 12H6a7 7 0 1 1 2.1 5l-1.4 1.4A9 9 0 1 0 13 3zm-1 5v5l4.3 2.5.7-1.2-3.5-2.1V8z"/></svg>',
  popout: '<svg viewBox="0 0 24 24"><path d="M19 19H5V5h7V3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7h-2v7zM14 3v2h3.59l-9.83 9.83 1.41 1.41L19 6.41V10h2V3h-7z"/></svg>',
  tree: '<svg viewBox="0 0 24 24"><path d="M3 3h8v2H3zm4 4h8v2H7zm0 4h8v2H7zm-4 4h8v2H3zm4 4h8v2H7z"/></svg>',
  search: '<svg viewBox="0 0 24 24"><path d="M15.5 14h-.8l-.3-.3A6.5 6.5 0 1 0 9.5 16a6.5 6.5 0 0 0 4.2-1.6l.3.3v.8l5 5 1.5-1.5zm-6 0A4.5 4.5 0 1 1 14 9.5 4.5 4.5 0 0 1 9.5 14z"/></svg>',
};

function pathHTML(path) {
  const i = path.lastIndexOf('/');
  return `<span class="fpath"><span class="dir">${esc(path.slice(0, i + 1))}</span><span class="base">${esc(path.slice(i + 1))}</span></span>`;
}

// ---- routing ----

function navigate(url, replace) {
  if (url === location.pathname + location.search + location.hash) return;
  history[replace ? 'replaceState' : 'pushState'](null, '', url);
  route();
}

// Intercept same-origin link clicks for client-side navigation.
document.addEventListener('click', e => {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  const a = e.target.closest('a[href]');
  if (!a || a.target || a.hasAttribute('download')) return;
  const u = new URL(a.href, location.href);
  if (u.origin !== location.origin || u.pathname.startsWith('/raw/') || u.pathname.startsWith('/api/')) return;
  e.preventDefault();
  if (u.pathname === location.pathname && u.search === location.search && u.hash) {
    if (u.hash !== location.hash) history.pushState(null, '', u.hash);
    onHash(true);
    return;
  }
  navigate(u.pathname + u.search + u.hash);
});
window.addEventListener('popstate', route);

let view = null; // current page state

function route() {
  hideHover();
  const p = location.pathname;
  const params = new URLSearchParams(location.search);
  document.body.classList.toggle('home', p === '/');
  hideSuggest();
  if (p === '/search') {
    const q = params.get('q') || '';
    qbox.value = q;
    renderSearch(q);
  } else if (p.startsWith('/f/')) {
    const parts = p.slice(3).split('/').map(decodeURIComponent);
    const repo = parts.shift();
    const path = parts.join('/').replace(/\/+$/, '');
    const q = params.get('q') || '';
    if (q) qbox.value = q;
    renderFile(repo, path, q, params.get('rev') || '', params.get('at') || '');
  } else {
    qbox.value = '';
    renderHome();
  }
}

// ---- status / indexing indicator ----

let status = null;
async function pollStatus() {
  try {
    status = await api('status');
  } catch (e) {
    setTimeout(pollStatus, 5000);
    return;
  }
  const el = $('#indexing');
  const ix = status.indexing;
  if (ix.running) {
    let what = ix.phase === 'indexing' ? `Indexing ${esc(ix.repo)} · ${fmtNum(ix.indexed)}/${fmtNum(ix.toIndex)}`
      : `Scanning ${esc(ix.repo || '')}${ix.walked ? ' · ' + fmtNum(ix.walked) + ' files' : ''}`;
    el.innerHTML = `<span class="spinner"></span><span class="itext">${what}</span>`;
    el.title = el.textContent;
    el.hidden = false;
  } else {
    el.hidden = true;
  }
  if (view && view.kind === 'home') updateHomeRepos();
  if (status.indexing.running !== pollStatus.lastRunning && pollStatus.lastRunning && view && view.kind === 'home') renderHome();
  pollStatus.lastRunning = status.indexing.running;
  setTimeout(pollStatus, ix.running ? 1500 : 10000);
}

// ---- home ----

function renderHome() {
  view = {kind: 'home'};
  document.title = 'ND Code Search';
  app.innerHTML = `
    <div class="home-wrap">
      <h1 class="home-title"><svg viewBox="0 0 24 24"><circle cx="10" cy="10" r="6.5"/><path d="M15 15l6 6"/></svg>ND Code Search</h1>
      <form class="searchbox big" role="search" autocomplete="off">
        <svg class="sicon" viewBox="0 0 24 24"><circle cx="10" cy="10" r="6.5"/><path d="M15 15l6 6"/></svg>
        <input type="text" name="q" spellcheck="false" placeholder="Search code, files and symbols" aria-label="Search" autofocus>
        <ul class="suggest" hidden></ul>
      </form>
      <div class="examples">Try
        <a href="${searchURL('sym:main')}">sym:main</a>
        <a href="${searchURL('TODO lang:go')}">TODO lang:go</a>
        <a href="${searchURL('"func main(" -file:test')}">"func main(" -file:test</a>
        <a href="${searchURL('file:README')}">file:README</a>
        <a href="#" id="showhelp">more…</a>
      </div>
      <div id="homelists"></div>
      <div class="section-title"><span>Repositories</span><span class="grow"></span>
        <button class="btn" id="reindex">Update index</button></div>
      <div class="repos" id="repos"><div class="loading"><span class="spinner"></span>Loading…</div></div>
      <p class="muted" id="cfginfo" style="font-size:12px;margin-top:20px"></p>
    </div>`;
  const form = $('form', app);
  attachSearchBox(form, $('input', form));
  $('#showhelp').onclick = e => { e.preventDefault(); $('#help').showModal(); };
  $('#reindex').onclick = async () => {
    const r = await fetch('/api/reindex', {method: 'POST', headers: {'X-Requested-With': 'cs'}}).then(r => r.json());
    toast(r.started ? 'Index update started' : 'An index update is already running');
    clearTimeout(pollStatus.timer);
    setTimeout(pollStatus, 300);
  };
  setTimeout(() => $('input', form).focus(), 0);
  if (status) updateHomeRepos();
  renderHomeLists();
}

function updateHomeRepos() {
  const el = $('#repos');
  if (!el || !status) return;
  const ix = status.indexing;
  el.innerHTML = status.repos.map(r => {
    const busy = ix.running && ix.repo === r.name;
    let stats = r.indexed ? `${fmtNum(r.files)} files · ${fmtBytes(r.bytes)} · updated ${fmtAgo(r.updated)}` : (busy ? 'Indexing…' : 'Not indexed yet');
    return `<a class="repo-card" href="${fileURL(r.name, '')}">
      <div class="name">${busy ? '<span class="spinner"></span>' : `<span class="dot ${r.indexed ? '' : 'off'}"></span>`}${esc(r.name)}</div>
      <div class="path" title="${esc(r.root)}">&lrm;${esc(r.root)}&lrm;</div>
      <div class="stats">${stats}</div>
      ${r.error ? `<div class="err">${esc(r.error)}</div>` : ''}
    </a>`;
  }).join('') || '<div class="muted">No repositories configured.</div>';
  $('#reindex').disabled = ix.running;
  $('#cfginfo').innerHTML = `Configuration: <code>${esc(status.config)}</code> · Index: <code>${esc(status.indexDir)}</code>`;
}

// ---- search box & suggestions ----

function attachSearchBox(form, input) {
  const list = $('.suggest', form);
  let items = [], sel = -1, timer = 0;
  const render = () => {
    if (!items.length) { list.hidden = true; return; }
    list.innerHTML = items.map((f, i) => {
      if (f.hdr) return `<li class="hdr">${esc(f.hdr)}</li>`;
      if (f.q) return `<li data-i="${i}" class="qitem${i === sel ? ' sel' : ''}">${f.saved ? '<span class="star">★</span>' : icons.history}<span class="qtext">${esc(f.name || f.q)}</span>${f.name && f.name !== f.q ? `<span class="dir">${esc(f.q)}</span>` : ''}</li>`;
      if (f.scope) return `<li class="searchall scope${i === sel ? ' sel' : ''}" data-i="${i}">${icons.dir} Search in <b>${esc(f.label)}</b></li>`;
      if (f.all) return `<li class="searchall${i === sel ? ' sel' : ''}" data-i="${i}">${icons.search.replace('<svg', '<svg style="width:16px;height:16px;fill:currentColor;vertical-align:-3px"')} Search for <b>${esc(input.value)}</b></li>`;
      const j = f.path.lastIndexOf('/');
      return `<li data-i="${i}" class="${i === sel ? 'sel' : ''}"><span class="repo-chip">${esc(f.repo)}</span>${f.dir ? icons.dir : icons.file}<span class="base">${esc(f.path.slice(j + 1))}${f.dir ? '/' : ''}</span><span class="dir">${esc(f.path.slice(0, j + 1))}&lrm;</span></li>`;
    }).join('');
    list.hidden = false;
  };
  const choose = i => {
    const f = items[i];
    list.hidden = true;
    if (f && f.scope) { input.value = scopedQuery(input.value.trim().replace(/^\.\s*/, '')); submit(); return; }
    if (f && f.q) { input.value = f.q; submit(); return; }
    if (!f || f.all) { submit(); return; }
    input.blur();
    navigate(fileURL(f.repo, f.path));
  };
  const submit = () => {
    let q = input.value.trim();
    list.hidden = true;
    if (!q) return;
    // A leading "." searches the folder being viewed (".ServeMux"), unless
    // it starts a regular expression such as ".*foo".
    if (/^\.(?=[\w\s"'$-])/.test(q) && currentScope()) q = scopedQuery(q.slice(1).trim());
    input.value = q;
    input.blur();
    navigate(searchURL(q));
  };
  // With an empty box, offer saved searches, recent searches and files.
  const showRecent = () => {
    items = [];
    const saved = savedList.slice(0, 5), qs = recent.get('q').slice(0, 6), fs = recent.get('f').slice(0, 6);
    if (saved.length) items.push({hdr: 'Saved searches'}, ...saved.map(x => ({q: x.query, name: x.name, saved: true})));
    if (qs.length) items.push({hdr: 'Recent searches'}, ...qs.map(q => ({q})));
    if (fs.length) items.push({hdr: 'Recently viewed'}, ...fs);
    sel = -1;
    render();
  };
  const selectable = i => items[i] && !items[i].hdr;
  const step = d => {
    if (!items.some((_, i) => selectable(i))) return;
    do { sel = (sel + d + items.length) % items.length; } while (!selectable(sel));
    render();
  };
  input.addEventListener('focus', () => { if (!input.value.trim()) showRecent(); });
  input.addEventListener('input', () => {
    clearTimeout(timer);
    const q = input.value.trim();
    if (!q) { showRecent(); return; }
    if (q.length < 2) { items = []; render(); return; }
    timer = setTimeout(async () => {
      try {
        const r = await api('suggest', {q}, 'suggest');
        if (input.value.trim() !== q || document.activeElement !== input) return;
        items = (r.files || []);
        if (items.length) items.push({all: true});
        const sc = currentScope();
        if (sc && !/^(repo|r):/.test(q)) items.unshift({scope: true, label: (sc.dir ? sc.dir + '/' : sc.repo)});
        sel = -1;
        render();
      } catch (e) { if (!isAbort(e)) console.warn(e); }
    }, 120);
  });
  input.addEventListener('keydown', e => {
    if (e.key === 'ArrowDown' && items.length) { step(1); e.preventDefault(); }
    else if (e.key === 'ArrowUp' && items.length) { step(-1); e.preventDefault(); }
    else if (e.key === 'Escape') { if (!list.hidden) list.hidden = true; else input.blur(); }
    else if (e.key === 'Enter') { e.preventDefault(); sel >= 0 ? choose(sel) : submit(); }
  });
  input.addEventListener('blur', () => setTimeout(() => list.hidden = true, 150));
  list.addEventListener('mousedown', e => {
    const li = e.target.closest('li');
    if (li && li.dataset.i) { e.preventDefault(); choose(+li.dataset.i); }
  });
  form.addEventListener('submit', e => { e.preventDefault(); submit(); });
}
function hideSuggest() { $$('.suggest').forEach(l => l.hidden = true); }

// ---- search results ----

const SNIP_LINES = 5;

async function renderSearch(q) {
  view = {kind: 'search', q, page: 0};
  const v = view;
  document.title = q + ' - ND Code Search';
  if (!q) { app.innerHTML = '<div class="empty">Enter a query.</div>'; return; }
  app.innerHTML = '<div class="loading"><span class="spinner"></span>Searching…</div>';
  let r;
  try {
    r = await api('search', {q, page: 0}, 'search');
  } catch (e) {
    if (isAbort(e)) return;
    app.innerHTML = `<div class="results"><div class="error">${esc(e.message)}</div></div>`;
    return;
  }
  if (view !== v) return;
  app.scrollTop = 0;
  recent.add('q', q);
  const parts = [];
  let summary = r.numFiles === 0 ? 'No results' :
    `<b>${fmtNum(r.numMatches)}</b> match${r.numMatches === 1 ? '' : 'es'} in <b>${fmtNum(r.numFiles)}</b> file${r.numFiles === 1 ? '' : 's'}`;
  summary += ` <span class="muted">(${r.elapsed.toFixed(3)} s)</span>`;
  if (r.timedOut) summary += ' · <span class="warn">search timed out; results are partial</span>';
  else if (r.truncated) summary += ' · <span class="warn">showing the first results only; refine your query</span>';
  parts.push(`<div class="summary"><span class="stext">${summary}</span><button class="tbtn savebtn" title="Save this search">☆ Save search</button></div>`);
  if (r.nameMatches && r.nameMatches.length) {
    parts.push(`<div class="namehits"><div class="section-title">Files and folders${r.numNameMatches > r.nameMatches.length ? ` <span class="muted" style="text-transform:none;letter-spacing:0">(${fmtNum(r.numNameMatches)}; <a href="${searchURL('file:' + q)}">see all</a>)</span>` : ''}</div><ul>` +
      r.nameMatches.map(f => `<li><a href="${fileURL(f.repo, f.path)}"><span class="repo-chip">${esc(f.repo)}</span>${f.dir ? icons.dir : icons.file}${pathHTML(f.path + (f.dir ? '/' : ''))}</a></li>`).join('') +
      '</ul></div>');
    if (r.files.length) parts.push('<div class="section-title">Code</div>');
  }
  parts.push('<div id="filelist"></div>');
  if (r.numFiles === 0 && !(r.nameMatches || []).length) {
    parts.push(`<div class="empty">No matches for <code>${esc(q)}</code>.<br><br><span style="font-size:13px">Tips: terms are regular expressions and are case-insensitive (add <code>case:yes</code> to match case). Use quotes for literal text.</span></div>`);
  }
  const side = facetsHTML(r.facets, q);
  app.innerHTML = `<div class="sres${side ? '' : ' nofacets'}">${side ? `<aside class="facets">${side}</aside>` : ''}<div class="results">${parts.join('')}</div></div>`;
  bindSnippets($('#filelist'), q);
  appendResults(r, q);
  setupSaveButton(q);
}

// ---- filter sidebar ----

function hasFilter(q, f) { return (' ' + q + ' ').includes(' ' + f + ' '); }
function toggleFilter(q, f) {
  if (hasFilter(q, f)) return (' ' + q + ' ').replace(' ' + f + ' ', ' ').trim().replace(/\s+/g, ' ');
  return q + ' ' + f;
}

function facetsHTML(fs, q) {
  if (!fs) return '';
  const section = (title, list, fmt) => {
    list = list || [];
    const active = list.some(f => hasFilter(q, f.filter));
    if (list.length < 2 && !active) return '';
    return `<div class="fsec"><div class="ftitle">${title}</div>` + list.map(f => {
      const on = hasFilter(q, f.filter);
      return `<a class="facet${on ? ' on' : ''}" href="${searchURL(toggleFilter(q, f.filter))}" title="${esc(on ? 'Remove ' : 'Add ') + esc(f.filter)}"><span class="fl">${on ? '✓ ' : ''}${fmt(f.label)}</span><span class="fc">${fmtNum(f.count)}</span></a>`;
    }).join('') + '</div>';
  };
  // Categories are hidden with -is:..., and shown as long as they are present
  // (or hidden by the query).
  const kinds = (fs.kinds || []).slice();
  for (const [label, filter] of [['Tests', '-is:test'], ['Generated', '-is:generated'], ['Third-party', '-is:vendored']]) {
    if (hasFilter(q, filter) && !kinds.some(k => k.filter === filter)) kinds.push({label, filter, count: 0});
  }
  const hide = kinds.length ? `<div class="fsec"><div class="ftitle">Hide</div>` + kinds.map(k => {
    const on = hasFilter(q, k.filter);
    return `<a class="facet${on ? ' on' : ''}" href="${searchURL(toggleFilter(q, k.filter))}" title="${on ? 'Show' : 'Hide'} ${esc(k.label.toLowerCase())} (${esc(k.filter)})"><span class="fl">${on ? '✓ ' : ''}${esc(k.label)}</span><span class="fc">${on ? '' : fmtNum(k.count)}</span></a>`;
  }).join('') + '</div>' : '';
  return section('Repositories', fs.repos, esc) + section('Languages', fs.langs, esc) +
    section('Directories', fs.dirs, l => `<span class="mono">${esc(l)}</span>`) + hide;
}

function appendResults(r, q) {
  const list = $('#filelist');
  if (!list) return;
  $('.loadmore', app)?.remove();
  const frag = document.createElement('div');
  frag.innerHTML = r.files.map(f => fileResultHTML(f, q, SNIP_LINES)).join('');
  Array.from(frag.children).forEach((el, i) => el._file = r.files[i]);
  list.append(...frag.children);
  if (r.more) {
    const b = document.createElement('button');
    b.className = 'btn loadmore';
    b.textContent = 'Load more results';
    b.onclick = async () => {
      b.disabled = true; b.textContent = 'Loading…';
      const v = view;
      try {
        const nr = await api('search', {q, page: ++v.page}, 'search');
        if (view === v) appendResults(nr, q);
      } catch (e) { if (!isAbort(e)) { b.disabled = false; b.textContent = 'Load more results'; toast(e.message); } }
    };
    list.after(b);
  }
}

function snippetRows(f, lines, q) {
  let prev = -1, out = '';
  for (const l of lines) {
    if (prev >= 0 && l.line !== prev + 1) out += '<tr class="gap"><td colspan="2"></td></tr>';
    const href = fileURL(f.repo, f.path, {q, line: l.line});
    out += `<tr class="row${l.def ? ' def' : ''}" data-href="${esc(href)}"><td class="ln"><a href="${esc(href)}">${l.line}</a></td><td class="code">${l.html}</td></tr>`;
    prev = l.line;
  }
  return out;
}

function fileResultHTML(f, q, max) {
  // Definitions come first; show the rest in line order.
  const shown = f.lines.slice(0, max);
  const rest = f.lines.length - shown.length;
  const unshown = f.numMatches - f.lines.length;
  return `<div class="fr" data-repo="${esc(f.repo)}" data-path="${esc(f.path)}">
    <div class="fr-head"><span class="repo-chip">${esc(f.repo)}</span><a href="${fileURL(f.repo, f.path, {q})}">${pathHTML(f.path)}</a>
      <span class="meta">${esc(f.lang)}${f.numMatches ? ` · ${fmtNum(f.numMatches)} match${f.numMatches === 1 ? '' : 'es'}` : ''}</span>${f.lines.length && q ? '<button class="ctxbtn" title="Show the lines around each match">± context</button>' : ''}</div>
    ${f.lines.length ? `<table class="snip">${snippetRows(f, shown, q)}</table>` : ''}
    ${rest > 0 ? `<button class="more" data-more="1">Show ${rest} more match${rest === 1 ? '' : 'es'}</button>` :
      unshown > 0 ? `<a class="more" href="${fileURL(f.repo, f.path, {q})}">View all ${fmtNum(f.numMatches)} matches in file</a>` : ''}
  </div>`;
}

function bindSnippets(root, q) {
  root.addEventListener('click', e => {
    const ctx = e.target.closest('button.ctxbtn');
    if (ctx) {
      const fr = ctx.closest('.fr');
      fr._ctx = !fr._ctx;
      ctx.classList.toggle('on', fr._ctx);
      renderSnippet(fr, q);
      return;
    }
    const more = e.target.closest('button[data-more]');
    if (more) {
      const fr = more.closest('.fr');
      const f = fr._file;
      fr._all = true;
      renderSnippet(fr, q);
      const unshown = f.numMatches - f.lines.length;
      if (unshown > 0) more.outerHTML = `<a class="more" href="${fileURL(f.repo, f.path, {q})}">View all ${fmtNum(f.numMatches)} matches in file</a>`;
      else more.remove();
      return;
    }
    const row = e.target.closest('tr.row');
    if (row && !e.target.closest('a') && !window.getSelection().toString()) navigate(row.dataset.href);
  });
}

// ---- file view ----

const treeCache = new Map(); // "repo\0path" -> entries
let treeHidden = (() => { try { return localStorage.getItem('cs-tree') === 'hidden'; } catch (e) { return false; } })();

async function listDir(repo, path) {
  const key = repo + '\0' + path;
  if (!treeCache.has(key)) treeCache.set(key, api('file', {repo, path}).then(r => r.entries || []));
  try { return await treeCache.get(key); } catch (e) { treeCache.delete(key); throw e; }
}

async function renderFile(repo, path, q, rev, at) {
  // Reuse the layout (and tree) when moving within the same repo.
  let fv = $('.fv', app);
  const sameRepo = view && view.kind === 'file' && view.repo === repo && fv;
  const prev = view;
  view = {kind: 'file', repo, path, q, rev, at, panel: prev && prev.kind === 'file' ? prev.panel : null};
  const v = view;
  document.title = (path ? path.split('/').pop() : repo) + ' - ND Code Search';
  if (!sameRepo) {
    app.innerHTML = `<div class="fv${treeHidden ? ' notree' : ''}"><aside class="side">
        <div class="sidetabs"><button data-tab="files">Files</button><button data-tab="outline" title="Outline (o)">Outline</button></div>
        <nav class="tree" aria-label="Files"></nav><div class="outline" hidden></div></aside>
      <section class="fmain"><div class="fhead"></div><div class="fbody"><div class="codewrap"><div class="loading"><span class="spinner"></span>Loading…</div></div></div></section></div>`;
    fv = $('.fv', app);
    buildTree(repo);
    $('.sidetabs', fv).onclick = e => { const b = e.target.closest('button'); if (b) setSideTab(b.dataset.tab); };
    setSideTab(sideTab);
  }
  revealInTree(repo, path);
  const head = $('.fhead', fv), wrap = $('.codewrap', fv);
  head.innerHTML = crumbsHTML(repo, path);

  let r;
  try {
    const params = {repo, path, q, readme: 1};
    if (rev) Object.assign(params, {rev, at});
    r = await api('file', params, 'file');
  } catch (e) {
    if (isAbort(e)) return;
    if (view === v) wrap.innerHTML = `<div class="notice">${esc(e.message)}</div>`;
    return;
  }
  if (view !== v) return;
  v.data = r;
  if (r.kind === 'file' && !rev) recent.add('f', {repo, path});
  wrap.scrollTop = 0;
  if (r.kind === 'dir') {
    head.innerHTML = crumbsHTML(repo, path) + searchHereButton() + `<span class="finfo">${fmtNum(r.entries.length)} items</span>`;
    head.onclick = e => { if (e.target.closest('[data-act="searchhere"]')) searchHere(); };
    treeCache.set(repo + '\0' + path, Promise.resolve(r.entries));
    const up = path ? `<tr><td colspan="2"><a href="${fileURL(repo, path.split('/').slice(0, -1).join('/'))}">${icons.dir}..</a></td></tr>` : '';
    const rows = r.entries.map(e => `<tr class="${e.excluded ? 'excluded' : ''}"><td><a href="${fileURL(repo, (path ? path + '/' : '') + e.name)}">${e.dir ? icons.dir : icons.file}${esc(e.name)}</a></td><td class="size">${e.dir ? '' : fmtBytes(e.size)}</td></tr>`).join('');
    const readme = r.readme ? `<div class="readme"><h3>${esc(r.readme.name)}</h3>${codeHTML(r.readme.lines)}</div>` : '';
    wrap.innerHTML = `<div class="dirlist"><table>${up}${rows}</table></div>${readme}`;
    renderOutline(null);
    restorePanel();
    return;
  }

  const nlines = r.lines ? r.lines.length : 0;
  const canPreview = isHTMLFile(path) && !rev && r.lines;
  head.innerHTML = crumbsHTML(repo, path) + searchHereButton() +
    (canPreview ? `<button class="tbtn" data-act="preview" title="Show the rendered page">Preview</button>
      <a class="iconbtn popout" href="${previewURL(repo, path)}" target="_blank" rel="noopener" title="Open the preview in a new tab" hidden>${icons.popout}</a>` : '') +
    `<span class="finfo">${esc(r.lang)}${nlines ? ' · ' + fmtNum(nlines) + ' lines' : ''} · ${fmtBytes(r.size)}</span>` +
    (rev ? `<span class="revbanner">Version <code>${esc(rev.slice(0, 10))}</code> · <a href="${fileURL(repo, path)}">Current version</a></span>` : '') +
    `<span class="findnav" hidden></span>` +
    `<span class="factions">
      ${r.git ? `<button class="tbtn" data-act="blame" title="Show who last changed each line (b)">Blame</button>
      <button class="tbtn" data-act="history" title="Show the file's history (h)">History</button>` : ''}
      <button class="iconbtn" data-act="tree" title="Toggle sidebar (t)">${icons.tree}</button>
      <button class="iconbtn" data-act="find" title="Find in file (f)">${icons.search}</button>
      <span class="menuwrap"><button class="iconbtn" data-act="links" title="Copy path or link (y copies a permalink)">${icons.link}</button>
        <span class="menu" hidden>
          <button data-copy="path">Copy path</button>
          <button data-copy="link">Copy link${location.hash.startsWith('#L') ? ' to selected lines' : ''}</button>
          ${r.head || rev ? '<button data-copy="permalink">Copy permalink (this commit)</button>' : ''}
          <button data-copy="raw">Copy raw file URL</button>
        </span></span>
      <a class="iconbtn" href="/raw/${encodeURIComponent(repo)}/${encPath(path)}" target="_blank" title="Raw file">${icons.raw}</a>
    </span>`;
  head.onclick = e => {
    const c = e.target.closest('[data-copy]');
    if (c) { copyLink(c.dataset.copy); closeMenus(); return; }
    const b = e.target.closest('[data-act]');
    if (!b) return;
    if (b.dataset.act === 'find') openFind();
    if (b.dataset.act === 'links') { e.stopPropagation(); toggleLinksMenu(); }
    if (b.dataset.act === 'tree') toggleTree();
    if (b.dataset.act === 'blame') toggleBlame();
    if (b.dataset.act === 'history') showHistory();
    if (b.dataset.act === 'preview') togglePreview();
    if (b.dataset.act === 'searchhere') searchHere();
  };

  if (r.tooLarge) wrap.innerHTML = `<div class="notice">This file is too large to display (${fmtBytes(r.size)}). <a href="/raw/${encodeURIComponent(repo)}/${encPath(path)}" target="_blank">View raw</a></div>`;
  else if (r.image) wrap.innerHTML = `<div class="notice"><img src="/raw/${encodeURIComponent(repo)}/${encPath(path)}" alt="${esc(path)}"></div>`;
  else if (r.binary) wrap.innerHTML = `<div class="notice">Binary file not shown (${fmtBytes(r.size)}). <a href="/raw/${encodeURIComponent(repo)}/${encPath(path)}?download=1">Download</a></div>`;
  else {
    wrap.innerHTML = codeHTML(r.lines, true);
    setupFind(head, wrap, q);
    if (blameOn && r.git) toggleBlame(true);
  }
  renderOutline(r.outline);
  onHash(true);
  restorePanel();
}

// ---- searching the current folder ----

// currentScope returns the repository and folder being viewed, if any.
function currentScope() {
  if (!view || view.kind !== 'file') return null;
  const isDir = view.data && view.data.kind === 'dir';
  const dir = isDir ? view.path : view.path.split('/').slice(0, -1).join('/');
  return {repo: view.repo, dir};
}

const reEscape = s => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

// scopedQuery adds filters limiting q to the folder being viewed.
function scopedQuery(q) {
  const sc = currentScope();
  if (!sc) return q;
  const quote = v => /[\s"]/.test(v) ? '"' + v.replace(/["\\]/g, '\\$&') + '"' : v;
  let f = 'repo:' + quote('^' + reEscape(sc.repo) + '$');
  if (sc.dir) f += ' file:' + quote('^' + reEscape(sc.dir) + '/');
  return (f + ' ' + q).trim();
}

function searchHereButton() {
  const sc = currentScope() || {};
  return `<button class="tbtn" data-act="searchhere" title="Search in ${esc(sc.dir ? sc.dir + '/' : 'this repository')} (or start a query with .)">${icons.search.replace('<svg', '<svg class="tico"')}Search here</button>`;
}

function searchHere() {
  qbox.value = scopedQuery('') + ' ';
  qbox.focus();
  qbox.setSelectionRange(qbox.value.length, qbox.value.length);
}

// ---- HTML preview ----

const isHTMLFile = p => /\.(html?|xhtml)$/i.test(p);
const previewURL = (repo, path) => '/preview/' + encodeURIComponent(repo) + '/' + encPath(path);

// togglePreview switches an HTML file between its source and the
// rendered page. The page runs in a sandboxed frame with an opaque
// origin (see handlePreview), so its scripts can't reach the app.
function togglePreview() {
  const v = view, wrap = $('.codewrap', app), head = $('.fhead', app);
  if (!v || v.kind !== 'file' || !v.data || !wrap) return;
  v.preview = !v.preview;
  const btn = $('[data-act="preview"]', head);
  btn.classList.toggle('on', v.preview);
  btn.textContent = v.preview ? 'Code' : 'Preview';
  btn.title = v.preview ? 'Show the source' : 'Show the rendered page';
  $('.popout', head).hidden = !v.preview;
  $('[data-act="blame"]', head)?.toggleAttribute('disabled', v.preview);
  $('.findnav', head).style.visibility = v.preview ? 'hidden' : '';
  if (v.preview) {
    wrap.innerHTML = `<iframe class="preview" title="Preview of ${esc(v.path)}" sandbox="allow-scripts allow-forms allow-popups allow-modals" src="${previewURL(v.repo, v.path)}"></iframe>`;
    return;
  }
  wrap.innerHTML = codeHTML(v.data.lines, true);
  setupFind(head, wrap, v.q);
  if (blameOn && v.data.git) toggleBlame(true);
  onHash(true);
}

function codeHTML(lines, clickable) {
  const gw = String(lines.length).length;
  let out = `<div class="code${clickable ? ' clickable' : ''}" style="--gw:${gw}ch">`;
  for (let i = 0; i < lines.length; i++) {
    out += `<div class="line" id="L${i + 1}"><span class="lnum" data-n="${i + 1}"></span><span class="src">${lines[i] || ' '}</span></div>`;
  }
  return out + '</div>';
}

function crumbsHTML(repo, path) {
  let out = `<div class="crumbs"><span class="repo-chip">${esc(repo)}</span><a href="${fileURL(repo, '')}">${esc(repo)}</a>`;
  if (path) {
    const parts = path.split('/');
    parts.forEach((p, i) => {
      const sub = parts.slice(0, i + 1).join('/');
      out += '<span class="sep">/</span>' + (i === parts.length - 1 ? `<span class="last">${esc(p)}</span>` : `<a href="${fileURL(repo, sub)}">${esc(p)}</a>`);
    });
  }
  return out + '</div>';
}

function toggleTree() {
  const fv = $('.fv', app);
  if (!fv) return;
  treeHidden = !treeHidden;
  fv.classList.toggle('notree', treeHidden);
  try { localStorage.setItem('cs-tree', treeHidden ? 'hidden' : 'shown'); } catch (e) {}
}

// ---- file tree ----

async function buildTree(repo) {
  const nav = $('.tree', app);
  nav.innerHTML = `<ul><li class="open" data-path=""><a class="node rootnode" href="${fileURL(repo, '')}" data-dir="1"><span class="tw">${icons.chevron}</span>${icons.dir}<span>${esc(repo)}</span></a><ul></ul></li></ul>`;
  nav.onclick = async e => {
    const a = e.target.closest('a.node');
    if (!a || !a.dataset.dir) return;
    const li = a.parentElement;
    // Clicking the arrow (or an open directory) only toggles it.
    if (e.target.closest('.tw') || (li.classList.contains('open') && a.classList.contains('cur'))) {
      e.preventDefault(); e.stopPropagation();
      if (li.classList.contains('open')) li.classList.remove('open');
      else await expand(repo, li);
      return;
    }
    if (!li.classList.contains('open')) expand(repo, li);
  };
  await expand(repo, $('li', nav));
}

function expand(repo, li) {
  li.classList.add('open');
  // Concurrent callers share one load.
  if (!li._loading) li._loading = loadChildren(repo, li);
  return li._loading;
}

async function loadChildren(repo, li) {
  const path = li.dataset.path;
  const ul = li.querySelector(':scope > ul');
  {
    let ents;
    try { ents = await listDir(repo, path); } catch (e) { ul.innerHTML = `<li class="muted" style="padding-left:24px">${esc(e.message)}</li>`; li._loading = null; return ul; }
    const depth = path ? path.split('/').length : 0;
    ul.innerHTML = ents.map(e => {
      const p = (path ? path + '/' : '') + e.name;
      const pad = `style="padding-left:${8 + (depth + 1) * 14}px"`;
      return e.dir
        ? `<li data-path="${esc(p)}"><a class="node${e.excluded ? ' excluded' : ''}" ${pad} href="${fileURL(repo, p)}" data-dir="1"><span class="tw">${icons.chevron}</span>${icons.dir}<span>${esc(e.name)}</span></a><ul></ul></li>`
        : `<li data-path="${esc(p)}"><a class="node${e.excluded ? ' excluded' : ''}" ${pad} href="${fileURL(repo, p)}"><span class="tw"></span>${icons.file}<span>${esc(e.name)}</span></a></li>`;
    }).join('');
  }
  return ul;
}

async function revealInTree(repo, path) {
  const nav = $('.tree', app);
  if (!nav) return;
  const parts = path ? path.split('/') : [];
  let li = $('li[data-path=""]', nav);
  for (let i = 0; i < parts.length - 1 && li; i++) {
    await expand(repo, li);
    const sub = parts.slice(0, i + 1).join('/');
    li = $$(':scope > ul > li', li).find(x => x.dataset.path === sub);
  }
  if (li && parts.length > 1) await expand(repo, li); // the file's folder
  $$('.node.cur', nav).forEach(n => n.classList.remove('cur'));
  const target = $$('li', nav).find(x => x.dataset.path === path);
  if (target) {
    const node = $(':scope > a.node', target);
    node.classList.add('cur');
    const r = node.getBoundingClientRect(), nr = nav.getBoundingClientRect();
    if (r.top < nr.top || r.bottom > nr.bottom) node.scrollIntoView({block: 'center'});
  }
}

// ---- line selection ----

let selAnchor = 0;
function onHash(scroll) {
  const code = $('.codewrap .code', app);
  if (!code) return;
  $$('.line.sel', code).forEach(l => l.classList.remove('sel'));
  const m = /^#L(\d+)(?:-L?(\d+))?$/.exec(location.hash);
  if (!m) return;
  let a = +m[1], b = +(m[2] || m[1]);
  if (a > b) [a, b] = [b, a];
  for (let i = a; i <= b; i++) document.getElementById('L' + i)?.classList.add('sel');
  const first = document.getElementById('L' + a);
  if (first && scroll !== false) {
    const wrap = $('.codewrap', app);
    wrap.scrollTop = first.offsetTop - wrap.clientHeight / 3;
  }
}
window.addEventListener('hashchange', () => onHash(true));

document.addEventListener('click', e => {
  const n = e.target.closest('.codewrap .code .lnum');
  if (!n) return;
  const line = +n.dataset.n;
  let hash = '#L' + line;
  if (e.shiftKey && selAnchor) hash = '#L' + Math.min(selAnchor, line) + '-L' + Math.max(selAnchor, line);
  else selAnchor = line;
  history.replaceState(null, '', location.pathname + location.search + hash);
  onHash(false);
});

// ---- find in file (matches from the search query) ----

function setupFind(head, wrap, q) {
  const marks = $$('mark', wrap);
  view.qmarks = q && marks.length ? marks : null;
  view.find = null;
  renderFindNav();
  if (!view.qmarks) return;
  // Start at the linked line, or the first match.
  if (!location.hash) gotoMark(1);
  else {
    const line = /^#L(\d+)/.exec(location.hash);
    if (line) view.markIdx = Math.max(0, marks.findIndex(m => +m.closest('.line').id.slice(1) >= +line[1])) - 1;
    gotoMark(1, true);
  }
}

// renderFindNav shows the match counter and arrows: for the find bar's
// text when it is open, otherwise for the search query's matches.
function renderFindNav() {
  const nav = $('.fhead .findnav', app);
  if (!nav) return;
  const find = view.find;
  view.marks = find ? find.marks : view.qmarks;
  view.markIdx = find ? find.idx : (view.qidx ?? -1);
  if (!find && !view.qmarks) { nav.hidden = true; nav.innerHTML = ''; return; }
  nav.hidden = false;
  nav.innerHTML = (find ? `<input class="findinput" type="text" placeholder="Find in file" spellcheck="false" aria-label="Find in file" value="${esc(find.text)}">` : '') +
    '<span class="fcount"></span><button class="iconbtn" data-dir="-1" title="Previous match (N)">↑</button><button class="iconbtn" data-dir="1" title="Next match (n)">↓</button>' +
    (find ? '<button class="iconbtn" data-close="1" title="Close (Esc)">✕</button>' : '');
  view.findUpdate = () => {
    const n = view.marks ? view.marks.length : 0;
    $('.fcount', nav).textContent = find && !find.text ? '' : n ? `${view.markIdx + 1 || '–'} of ${fmtNum(n)}${n >= MAX_FIND ? '+' : ''}` : 'No matches';
  };
  view.findUpdate();
  nav.onclick = e => {
    const b = e.target.closest('button');
    if (!b) return;
    if (b.dataset.close) closeFind();
    else gotoMark(+b.dataset.dir);
  };
  const input = $('.findinput', nav);
  if (!input) return;
  let timer = 0;
  input.oninput = () => {
    clearTimeout(timer);
    timer = setTimeout(() => runFind(input.value), 150);
  };
  input.onkeydown = e => {
    if (e.key === 'Enter') { e.preventDefault(); gotoMark(e.shiftKey ? -1 : 1); }
    else if (e.key === 'Escape') { e.preventDefault(); closeFind(); }
  };
}

const MAX_FIND = 10000;

function openFind() {
  const v = view;
  if (!v || v.kind !== 'file' || v.preview || !$('.codewrap .code', app)) return;
  if (!v.find) {
    v.qidx = v.markIdx;
    v.marks?.[v.markIdx]?.classList.remove('cur');
    v.find = {text: '', marks: [], idx: -1};
    renderFindNav();
  }
  const input = $('.fhead .findinput', app);
  input.focus();
  input.select();
}

function closeFind() {
  clearFindMarks();
  view.find = null;
  renderFindNav();
  view.marks?.[view.markIdx]?.classList.add('cur');
}

function clearFindMarks() {
  const parents = new Set();
  $$('.codewrap mark.ff', app).forEach(m => { parents.add(m.parentNode); m.replaceWith(...m.childNodes); });
  parents.forEach(p => p.normalize());
}

// runFind highlights every case-insensitive occurrence of text in the
// code, wrapping the matched text (which may span several syntax
// highlighting spans) in <mark class="ff"> elements.
function runFind(text) {
  const find = view.find;
  if (!find) return;
  clearFindMarks();
  find.text = text;
  find.marks = [];
  find.idx = -1;
  if (text) {
    const needle = text.toLowerCase();
    for (const src of $$('.codewrap .code .src', app)) {
      const hay = src.textContent.toLowerCase();
      const found = [];
      for (let i = hay.indexOf(needle); i >= 0; i = hay.indexOf(needle, i + needle.length)) found.push(i);
      // Wrap from the end so earlier offsets stay valid.
      for (let k = found.length - 1; k >= 0; k--) wrapText(src, found[k], found[k] + needle.length);
      if (found.length) find.marks.push(...$$('mark.ff', src).filter(m => !m.dataset.part));
      if (find.marks.length >= MAX_FIND) break;
    }
  }
  view.marks = find.marks;
  view.markIdx = -1;
  if (find.marks.length) gotoMark(1);
  else view.findUpdate();
}

// wrapText wraps the characters [a, b) of el's text in mark.ff elements,
// one per text node the range touches. All but the first get data-part
// so that each match is counted once.
function wrapText(el, a, b) {
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
  const nodes = [];
  for (let off = 0, n = walker.nextNode(); n; n = walker.nextNode()) {
    const len = n.textContent.length;
    if (off + len > a && off < b) nodes.push([n, Math.max(0, a - off), Math.min(len, b - off)]);
    off += len;
    if (off >= b) break;
  }
  nodes.forEach(([n, x, y], i) => {
    let mid = n;
    if (x > 0) mid = n.splitText(x);
    if (y - x < mid.textContent.length) mid.splitText(y - x);
    const m = document.createElement('mark');
    m.className = 'ff';
    if (i > 0) m.dataset.part = '1';
    mid.replaceWith(m);
    m.append(mid);
  });
}

function gotoMark(dir, noScroll) {
  const marks = view.marks;
  if (!marks || !marks.length) return;
  marks[view.markIdx]?.classList.remove('cur');
  view.markIdx = (view.markIdx + dir + marks.length) % marks.length;
  if (view.find) view.find.idx = view.markIdx;
  const m = marks[view.markIdx];
  m.classList.add('cur');
  view.findUpdate();
  if (!noScroll) {
    const wrap = $('.codewrap', app);
    const top = m.closest('.line').offsetTop;
    if (top < wrap.scrollTop + 40 || top > wrap.scrollTop + wrap.clientHeight - 60) wrap.scrollTop = top - wrap.clientHeight / 3;
  }
}

// ---- cross references ----

const identRE = /^[A-Za-z_$][\w$]*$/;
const skipClasses = /^(k|kd|kn|kp|kr|kc|kt|m\w*|il|c\w*|o|p|w|err)$/; // keywords, numbers, comments, operators

function wordAt(x, y) {
  let node, off;
  if (document.caretPositionFromPoint) {
    const p = document.caretPositionFromPoint(x, y);
    if (!p) return null;
    node = p.offsetNode; off = p.offset;
  } else if (document.caretRangeFromPoint) {
    const r = document.caretRangeFromPoint(x, y);
    if (!r) return null;
    node = r.startContainer; off = r.startOffset;
  }
  if (!node || node.nodeType !== 3) return null;
  const t = node.textContent;
  let a = off, b = off;
  while (a > 0 && /[\w$]/.test(t[a - 1])) a--;
  while (b < t.length && /[\w$]/.test(t[b])) b++;
  const w = t.slice(a, b);
  return identRE.test(w) ? {word: w, el: node.parentElement, node, a, b} : null;
}

// ---- hover cards ----

const hoverCache = new Map(); // "repo\0path\0sym" -> promise of /api/hover result
let hoverTimer = 0, hoverKey = '', hoverHideTimer = 0;
const canHover = matchMedia('(hover: hover) and (pointer: fine)').matches;

function hoverCard() {
  let c = $('#hovercard');
  if (!c) {
    c = document.createElement('div');
    c.id = 'hovercard';
    c.className = 'hovercard';
    c.hidden = true;
    c.addEventListener('mouseenter', () => clearTimeout(hoverHideTimer));
    c.addEventListener('mouseleave', () => scheduleHideHover());
    document.body.append(c);
  }
  return c;
}

function hideHover() {
  clearTimeout(hoverTimer);
  hoverKey = '';
  const c = $('#hovercard');
  if (c) c.hidden = true;
}

function scheduleHideHover() {
  clearTimeout(hoverHideTimer);
  hoverHideTimer = setTimeout(hideHover, 250);
}

// Show the card for the identifier under the pointer after a short pause.
document.addEventListener('mousemove', e => {
  if (!canHover || e.buttons) return;
  if (e.target.closest('#hovercard')) return;
  const src = e.target.closest('.codewrap .code.clickable .src');
  if (!src || !view || view.kind !== 'file') { if (hoverKey) scheduleHideHover(); return; }
  const hit = wordAt(e.clientX, e.clientY);
  if (hit && hit.el && hit.el !== src && skipClasses.test(hit.el.className) && !hit.el.closest('mark')) { scheduleHideHover(); return; }
  let rect = null;
  if (hit) {
    const range = document.createRange();
    range.setStart(hit.node, hit.a);
    range.setEnd(hit.node, hit.b);
    rect = range.getBoundingClientRect();
    if (e.clientX < rect.left - 1 || e.clientX > rect.right + 1 || e.clientY < rect.top - 1 || e.clientY > rect.bottom + 1) rect = null;
  }
  if (!rect) { scheduleHideHover(); return; }
  const key = hit.word + '@' + Math.round(rect.left) + ',' + Math.round(rect.top);
  if (key === hoverKey) { clearTimeout(hoverHideTimer); return; }
  hoverKey = key;
  clearTimeout(hoverTimer);
  hoverTimer = setTimeout(() => showHover(hit.word, rect, key), 350);
});

document.addEventListener('mousedown', e => { if (!e.target.closest('#hovercard')) hideHover(); });
document.addEventListener('scroll', hideHover, true);

async function showHover(sym, rect, key) {
  const v = view;
  if (!v || v.kind !== 'file') return;
  const ck = v.repo + '\0' + v.path + '\0' + sym;
  if (!hoverCache.has(ck)) {
    if (hoverCache.size > 500) hoverCache.clear();
    hoverCache.set(ck, api('hover', {sym, repo: v.repo, path: v.path}).catch(() => null));
  }
  const r = await hoverCache.get(ck);
  if (key !== hoverKey || view !== v) return;
  if (!r || !r.defs.length) { hideHover(); return; }
  const d = r.defs[0];
  const k = kindLetter[d.kind] || (d.kind ? d.kind[0] : 'd');
  const c = hoverCard();
  c.innerHTML = `<div class="hc-head"><span class="okind k-${esc(d.kind || 'def')}">${esc(k)}</span><b>${esc(r.symbol)}</b>
      ${d.kind ? `<span class="muted">${esc(d.kind)}</span>` : ''}
      <a class="hc-loc" href="${fileURL(d.repo, d.path, {line: d.line})}" title="${esc(d.repo + ': ' + d.path)}">${esc(d.path.split('/').pop())}:${d.line}</a></div>
    <pre class="hc-sig">${d.html}</pre>` +
    (d.doc ? `<div class="hc-doc">${esc(d.doc)}</div>` : '') +
    `<div class="hc-foot"><a href="${fileURL(d.repo, d.path, {line: d.line})}">Go to definition</a> · <a href="#" data-refs="1">Find references</a>` +
    (r.total > 1 ? ` · <span class="muted">${r.total - 1} more definition${r.total > 2 ? 's' : ''}</span>` : '') + '</div>';
  $('[data-refs]', c).onclick = e => { e.preventDefault(); hideHover(); highlightSym(sym); showXref(sym, v.repo, v.path, 0, false); };
  $$('a[href^="/"]', c).forEach(a => a.addEventListener('click', hideHover));
  c.hidden = false;
  // Below the word, or above it if there is no room; kept on screen.
  const w = c.offsetWidth, h = c.offsetHeight;
  let top = rect.bottom + 6;
  if (top + h > innerHeight - 8 && rect.top - h - 6 > 8) top = rect.top - h - 6;
  c.style.top = Math.max(8, top) + 'px';
  c.style.left = Math.max(8, Math.min(rect.left, innerWidth - w - 8)) + 'px';
}

// "i" shows the card for the selected identifier (keyboard access).
function hoverSelection() {
  const sel = window.getSelection();
  const w = sel.toString().trim();
  if (!identRE.test(w) || !sel.rangeCount || !sel.anchorNode || !sel.anchorNode.parentElement?.closest('.codewrap .code .src')) return false;
  hoverKey = 'sel:' + w;
  showHover(w, sel.getRangeAt(0).getBoundingClientRect(), hoverKey);
  return true;
}

document.addEventListener('click', async e => {
  const c = e.target.closest('.codewrap .code.clickable .src');
  if (!c || e.button !== 0 || window.getSelection().toString()) return;
  const hit = wordAt(e.clientX, e.clientY);
  if (!hit) return;
  if (hit.el && hit.el !== c && skipClasses.test(hit.el.className) && !hit.el.closest('mark')) return;
  const line = +c.parentElement.id.slice(1);
  highlightSym(hit.word);
  const jump = e.ctrlKey || e.metaKey;
  await showXref(hit.word, view.repo, view.path, line, jump);
});

function highlightSym(word) {
  $$('.codewrap .symhl', app).forEach(s => s.classList.remove('symhl'));
  if (!word) return;
  for (const s of $$('.codewrap .code .src span', app)) if (s.textContent === word) s.classList.add('symhl');
}

// ensurePanel returns the bottom panel of the file view, creating it if
// needed. It shows cross references, file history or a commit.
function ensurePanel() {
  const fmain = $('.fmain', app);
  if (!fmain) return null;
  let panel = $('.xref', fmain);
  if (!panel) {
    panel = document.createElement('div');
    panel.className = 'xref';
    panel.innerHTML = '<div class="drag"></div><div class="xref-head"></div><div class="xref-body"></div>';
    $('.fbody', fmain).append(panel);
    setupDrag(panel);
    const savedH = sessionStorage.getItem('cs-xref-h');
    if (savedH) panel.style.height = savedH;
  }
  return panel;
}

function panelLoading(panel, title) {
  $('.xref-head', panel).innerHTML = `<span class="sym">${title}</span><span class="counts"><span class="spinner" style="display:inline-block;vertical-align:-2px"></span></span><button class="iconbtn" title="Close (Esc)">✕</button>`;
  $('.xref-head button', panel).onclick = closePanel;
}

async function showXref(sym, repo, path, line, jump) {
  const panel = ensurePanel();
  if (!panel) return;
  const body = $('.xref-body', panel);
  panelLoading(panel, esc(sym));
  let r;
  try {
    r = await api('xref', {sym, repo, path}, 'xref');
  } catch (e) {
    if (isAbort(e)) return;
    body.innerHTML = `<div class="error">${esc(e.message)}</div>`;
    $('.xref-head .counts', panel).textContent = '';
    return;
  }
  view.panel = {kind: 'xref', sym, data: r, from: {repo, path, line}};
  if (jump) {
    const d = (r.defs || []).find(d => !(d.repo === repo && d.path === path && d.line === line)) || (r.defs || [])[0];
    if (d) {
      navigate(fileURL(d.repo, d.path, {line: d.line}));
      return;
    }
    toast('No definition found for ' + sym);
  }
  renderXref(panel, view.panel);
}

function renderXref(panel, x) {
  const r = x.data, head = $('.xref-head', panel), body = $('.xref-body', panel);
  const nd = (r.defs || []).length;
  head.innerHTML = `<span class="sym">${esc(r.symbol)}</span>
    <span class="counts">${nd} definition${nd === 1 ? '' : 's'} · ${fmtNum(r.numRefs)} reference${r.numRefs === 1 ? '' : 's'} in ${fmtNum(r.numFiles)} file${r.numFiles === 1 ? '' : 's'}${r.truncated ? ' (partial)' : ''} · ${r.elapsed.toFixed(2)} s</span>
    <a href="${searchURL('\\b' + r.symbol + '\\b case:yes')}" style="font-size:12px;margin-left:8px">Search</a>
    <span class="grow"></span>
    <button class="tbtn" data-calls="callers" title="Functions that call ${esc(r.symbol)}">Callers</button>
    <button class="tbtn" data-calls="callees" title="Functions ${esc(r.symbol)} calls"${nd ? '' : ' disabled'}>Callees</button>
    <button class="iconbtn" title="Close (Esc)">✕</button>`;
  $('.iconbtn', head).onclick = closePanel;
  $$('[data-calls]', head).forEach(b => b.onclick = () => showCalls(r.symbol, b.dataset.calls, x));
  let html = '';
  if (nd) {
    html += '<h4>Definitions</h4><div class="fr defs"><table class="snip">' + r.defs.map(d => {
      const here = d.repo === view.repo && d.path === view.path;
      const href = fileURL(d.repo, d.path, {line: d.line});
      return `<tr class="row${here ? ' here' : ''}" data-href="${esc(href)}"><td class="ln"><a href="${esc(href)}" title="${esc(d.repo + ': ' + d.path)}">${esc(d.path.split('/').pop())}:${d.line}</a></td><td class="code">${d.html}</td></tr>`;
    }).join('') + '</table></div>';
  } else {
    html += '<h4>Definitions</h4><div class="muted" style="padding:2px 16px 6px;font-size:13px">No definition found in the index.</div>';
  }
  if ((r.refs || []).length) {
    html += '<h4>References</h4>' + r.refs.map(f => fileResultHTML(f, '', 1e9)).join('');
  }
  body.innerHTML = html;
  body.onclick = e => {
    const row = e.target.closest('tr.row');
    if (row && !e.target.closest('a') && !window.getSelection().toString()) navigate(row.dataset.href);
  };
}

// restorePanel re-renders the bottom panel after navigating to another
// file. Cross references stay open; history and commits belong to a file.
function restorePanel() {
  const p = view.panel;
  if (!p) return;
  if (p.kind !== 'xref' && p.kind !== 'calls' && (p.repo !== view.repo || p.path !== view.path)) { closePanel(); return; }
  const panel = ensurePanel();
  if (!panel) return;
  if (p.kind === 'xref') { renderXref(panel, p); highlightSym(p.sym); }
  else if (p.kind === 'history') renderHistory(panel, p);
  else if (p.kind === 'commit') renderCommit(panel, p);
  else if (p.kind === 'calls') renderCalls(panel, p);
}

function closePanel() {
  $('.xref', app)?.remove();
  if (view) view.panel = null;
  highlightSym(null);
}

function setupDrag(panel) {
  const drag = $('.drag', panel);
  drag.addEventListener('pointerdown', e => {
    e.preventDefault();
    drag.setPointerCapture(e.pointerId);
    const body = panel.parentElement.getBoundingClientRect();
    const move = ev => {
      const h = Math.max(80, Math.min(body.bottom - ev.clientY, body.height * 0.85));
      panel.style.height = h + 'px';
    };
    const up = () => {
      drag.removeEventListener('pointermove', move);
      drag.removeEventListener('pointerup', up);
      sessionStorage.setItem('cs-xref-h', panel.style.height);
    };
    drag.addEventListener('pointermove', move);
    drag.addEventListener('pointerup', up);
  });
}

// ---- keyboard result navigation (j / k) ----

function moveCursor(d) {
  const rows = $$('#filelist tr.row:not(.ctx)');
  if (!rows.length) return;
  rows[view.cursor]?.classList.remove('kcur');
  view.cursor = Math.max(0, Math.min(rows.length - 1, (view.cursor ?? -1) + d));
  const row = rows[view.cursor];
  row.classList.add('kcur');
  row.scrollIntoView({block: 'nearest'});
}

function openCursor() {
  const row = $$('#filelist tr.row:not(.ctx)')[view.cursor];
  if (row) navigate(row.dataset.href);
}

// ---- sidebar: files / outline ----

let sideTab = (() => { try { return localStorage.getItem('cs-side') || 'files'; } catch (e) { return 'files'; } })();

function setSideTab(tab, focus) {
  sideTab = tab;
  try { localStorage.setItem('cs-side', tab); } catch (e) {}
  const fv = $('.fv', app);
  if (!fv) return;
  if (focus && treeHidden) toggleTree();
  $$('.sidetabs button', fv).forEach(b => b.classList.toggle('on', b.dataset.tab === tab));
  $('.tree', fv).hidden = tab !== 'files';
  $('.outline', fv).hidden = tab !== 'outline';
  if (focus && tab === 'outline') $('.outline input', fv)?.focus();
}

const kindLetter = {class: 'C', struct: 'S', interface: 'I', enum: 'E', trait: 'T', protocol: 'P', type: 'T', typealias: 'T',
  func: 'f', method: 'm', module: 'M', namespace: 'N', object: 'O', message: 'M', service: 'S', impl: 'i', name: 'n', target: 't'};

function renderOutline(syms) {
  const el = $('.fv .outline', app);
  if (!el) return;
  view.outline = syms || [];
  if (!syms || !syms.length) {
    el.innerHTML = `<div class="muted" style="padding:12px 16px;font-size:13px">${syms ? 'No symbols found in this file.' : 'Open a file to see its outline.'}</div>`;
    return;
  }
  el.innerHTML = `<input type="text" placeholder="Filter symbols" spellcheck="false" aria-label="Filter symbols"><ul>` +
    syms.map((s, i) => {
      const k = s.kind.startsWith('h') && s.kind.length === 2 ? '#' : (kindLetter[s.kind] || s.kind[0]);
      return `<li><a class="osym k-${esc(s.kind)}" href="#L${s.line}" data-i="${i}" style="padding-left:${10 + s.depth * 14}px" title="${esc(s.kind)} · line ${s.line}"><span class="okind">${esc(k)}</span><span class="oname">${esc(s.name)}</span></a></li>`;
    }).join('') + '</ul>';
  const input = $('input', el);
  input.oninput = () => {
    const f = input.value.toLowerCase();
    $$('li', el).forEach((li, i) => li.hidden = f && !syms[i].name.toLowerCase().includes(f));
  };
  input.onkeydown = e => {
    if (e.key === 'Enter') { const a = $$('li:not([hidden]) a', el)[0]; if (a) a.click(); }
    if (e.key === 'Escape') input.blur();
  };
  const wrap = $('.codewrap', app);
  wrap.onscroll = () => {
    if (wrap._raf) return;
    wrap._raf = requestAnimationFrame(() => { wrap._raf = 0; syncOutline(); });
  };
  syncOutline();
}

// syncOutline marks the outline entry for the code at the top of the view.
function syncOutline() {
  const el = $('.fv .outline', app), wrap = $('.codewrap', app), first = $('#L1');
  if (!el || !wrap || !first || !view.outline || !view.outline.length) return;
  const top = Math.floor(wrap.scrollTop / first.offsetHeight) + 2;
  let cur = -1;
  for (let i = 0; i < view.outline.length && view.outline[i].line <= top; i++) cur = i;
  $$('a.cur', el).forEach(a => a.classList.remove('cur'));
  const a = $(`a[data-i="${cur}"]`, el);
  if (a) {
    a.classList.add('cur');
    if (!el.hidden) {
      const r = a.getBoundingClientRect(), er = el.getBoundingClientRect();
      if (r.top < er.top + 40 || r.bottom > er.bottom) a.scrollIntoView({block: 'nearest'});
    }
  }
}

// ---- git: blame ----

let blameOn = false;

async function toggleBlame(restore) {
  const code = $('.codewrap .code', app);
  const btn = $('.fhead [data-act="blame"]', app);
  if (!code) return;
  if (code.classList.contains('blame-on') && !restore) {
    code.classList.remove('blame-on');
    $$('.bl', code).forEach(e => e.remove());
    btn?.classList.remove('on');
    blameOn = false;
    return;
  }
  blameOn = true;
  btn?.classList.add('on', 'busy');
  const v = view;
  let r;
  try {
    const params = {repo: v.repo, path: v.path};
    if (v.rev) params.rev = v.rev;
    r = await api('blame', params, 'blame');
  } catch (e) {
    if (!isAbort(e)) { toast('Blame: ' + e.message); btn?.classList.remove('on'); blameOn = false; }
    return;
  } finally {
    btn?.classList.remove('busy');
  }
  if (view !== v || code.classList.contains('blame-on')) return;
  const times = r.commits.map(c => c.time).filter(Boolean);
  const lo = Math.min(...times), hi = Math.max(...times);
  const lines = $$('.line', code);
  for (let i = 0; i < lines.length && i < r.lines.length; i++) {
    const ci = r.lines[i], c = r.commits[ci] || {};
    const start = i === 0 || r.lines[i - 1] !== ci;
    const span = document.createElement('span');
    span.className = 'bl' + (start ? ' start' : '');
    span.style.setProperty('--age', hi > lo && c.time ? ((c.time - lo) / (hi - lo)).toFixed(2) : 1);
    if (start) {
      span.title = `${c.summary || ''}\n${c.author || ''}, ${c.time ? new Date(c.time * 1000).toLocaleString() : ''}`;
      span.innerHTML = c.hash
        ? `<a class="bh" data-hash="${esc(c.hash)}">${esc(c.hash.slice(0, 7))}</a><span class="ba">${esc(c.author)}</span><span class="bd">${fmtDate(c.time)}</span>`
        : `<span class="ba">${esc(c.author || '')}</span>`;
    }
    lines[i].insertBefore(span, lines[i].firstChild);
  }
  code.classList.add('blame-on');
}

document.addEventListener('click', e => {
  const h = e.target.closest('.code .bl .bh');
  if (h) { e.preventDefault(); showCommit(h.dataset.hash, view.at || view.path); }
});

// ---- git: history and commits ----

async function showHistory() {
  const panel = ensurePanel();
  if (!panel) return;
  const v = view;
  panelLoading(panel, 'History');
  let r;
  try {
    r = await api('history', {repo: v.repo, path: v.path}, 'panel');
  } catch (e) {
    if (!isAbort(e)) $('.xref-body', panel).innerHTML = `<div class="error">${esc(e.message)}</div>`;
    return;
  }
  if (view !== v) return;
  view.panel = {kind: 'history', repo: v.repo, path: v.path, data: r};
  renderHistory(panel, view.panel);
}

function renderHistory(panel, p) {
  const r = p.data, n = r.commits.length;
  $('.xref-head', panel).innerHTML = `<span class="sym">History</span><span class="counts">${esc(p.path.split('/').pop())} · ${n}${n >= 200 ? '+' : ''} commit${n === 1 ? '' : 's'}</span><button class="iconbtn" title="Close (Esc)">✕</button>`;
  $('.xref-head button', panel).onclick = closePanel;
  const body = $('.xref-body', panel);
  if (!n) { body.innerHTML = '<div class="muted" style="padding:12px 16px">No commits found for this file.</div>'; return; }
  body.innerHTML = '<table class="hist">' + r.commits.map((c, i) => {
    const cur = view.rev && c.hash.startsWith(view.rev);
    return `<tr class="hrow${cur ? ' here' : ''}" data-i="${i}">
      <td class="hd">${fmtDate(c.time)}</td><td class="ha" title="${esc(c.email)}">${esc(c.author)}</td>
      <td class="hs">${esc(c.subject)}${c.path !== r.path ? ` <span class="muted">(${esc(c.path)})</span>` : ''}</td>
      <td class="hh"><a href="${fileURL(view.repo, view.path, {rev: c.hash, at: c.path})}" title="View the file at this commit"><code>${esc(c.hash.slice(0, 8))}</code></a></td></tr>`;
  }).join('') + '</table>';
  body.onclick = e => {
    const row = e.target.closest('tr.hrow');
    if (!row || e.target.closest('a')) return;
    const c = r.commits[+row.dataset.i];
    showCommit(c.hash, c.path, p);
  };
}

async function showCommit(hash, at, back) {
  const panel = ensurePanel();
  if (!panel) return;
  const v = view;
  panelLoading(panel, `<code>${esc(hash.slice(0, 8))}</code>`);
  let r;
  try {
    r = await api('commit', {repo: v.repo, path: v.path, hash, at: at || ''}, 'panel');
  } catch (e) {
    if (!isAbort(e)) $('.xref-body', panel).innerHTML = `<div class="error">${esc(e.message)}</div>`;
    return;
  }
  if (view !== v) return;
  view.panel = {kind: 'commit', repo: v.repo, path: v.path, hash, at, data: r, back: back || null};
  renderCommit(panel, view.panel);
}

function renderCommit(panel, p) {
  const r = p.data;
  const subject = r.message.split('\n')[0];
  $('.xref-head', panel).innerHTML = (p.back ? '<button class="tbtn" data-back="1" title="Back to history">← History</button>' : '') +
    `<span class="sym"><code>${esc(r.hash.slice(0, 10))}</code></span><span class="counts">${esc(subject)}</span><button class="iconbtn" title="Close (Esc)">✕</button>`;
  $('.xref-head .iconbtn', panel).onclick = closePanel;
  const backBtn = $('.xref-head [data-back]', panel);
  if (backBtn) backBtn.onclick = () => { view.panel = p.back; renderHistory(panel, p.back); };
  const rest = r.message.slice(subject.length).trim();
  let html = `<div class="cmeta"><b>${esc(r.author)}</b> &lt;${esc(r.email)}&gt; · ${new Date(r.time * 1000).toLocaleString()}
    ${r.parents.length ? ' · parent ' + r.parents.map(h => `<a href="#" data-commit="${esc(h)}"><code>${esc(h.slice(0, 8))}</code></a>`).join(', ') : ''}
    · <a href="${fileURL(view.repo, view.path, {rev: r.hash, at: p.at})}">View file at this commit</a>
    · <a href="#" data-all="1">${p.all ? 'Show only this file' : 'Show all changed files'}</a>
    ${rest ? `<pre class="cmsg">${esc(rest)}</pre>` : ''}</div>`;
  if (!r.diff.length) html += '<div class="muted" style="padding:8px 16px">No changes to this file in this commit.</div>';
  else {
    html += '<table class="diff">' + r.diff.map(d => {
      switch (d.kind) {
        case 'f': return `<tr class="d-file"><td colspan="3">${d.html}</td></tr>`;
        case 'i': return `<tr class="d-info"><td colspan="3">${d.html}</td></tr>`;
        case '@': return `<tr class="d-hunk"><td colspan="3">${d.html}</td></tr>`;
      }
      const cls = d.kind === '+' ? 'd-add' : d.kind === '-' ? 'd-del' : 'd-ctx';
      return `<tr class="${cls}"><td class="dn">${d.old || ''}</td><td class="dn">${d.new || ''}</td><td class="code"><span class="dk">${d.kind}</span>${d.html}</td></tr>`;
    }).join('') + '</table>';
    if (r.truncated) html += '<div class="muted" style="padding:8px 16px">Diff truncated.</div>';
  }
  const body = $('.xref-body', panel);
  body.innerHTML = html;
  body.scrollTop = 0;
  body.onclick = async e => {
    const c = e.target.closest('[data-commit]');
    if (c) { e.preventDefault(); showCommit(c.dataset.commit, p.at, p.back); return; }
    if (e.target.closest('[data-all]')) {
      e.preventDefault();
      const all = !p.all;
      try {
        const params = {repo: view.repo, path: view.path, hash: r.hash, at: p.at || ''};
        if (all) params.all = 1;
        p.data = await api('commit', params, 'panel');
        p.all = all;
        renderCommit(panel, p);
      } catch (err) { if (!isAbort(err)) toast(err.message); }
    }
  };
}

// ---- clipboard and links ----

// copyText copies s to the clipboard. The async clipboard API only
// exists in secure contexts (https or localhost), so over plain http
// (e.g. a Tailscale address) fall back to a hidden textarea.
async function copyText(s) {
  try {
    if (navigator.clipboard && window.isSecureContext) { await navigator.clipboard.writeText(s); return true; }
  } catch (e) {}
  const ta = document.createElement('textarea');
  ta.value = s;
  ta.setAttribute('readonly', '');
  ta.style.cssText = 'position:fixed;top:0;left:0;opacity:0';
  document.body.append(ta);
  ta.select();
  ta.setSelectionRange(0, s.length);
  let ok = false;
  try { ok = document.execCommand('copy'); } catch (e) {}
  ta.remove();
  return ok;
}

function toggleLinksMenu() {
  const m = $('.fhead .menu', app);
  if (!m) return;
  const open = m.hidden;
  closeMenus();
  m.hidden = !open;
}

function closeMenus() {
  let closed = false;
  $$('.menu', app).forEach(m => { if (!m.hidden) { m.hidden = true; closed = true; } });
  return closed;
}
document.addEventListener('click', e => { if (!e.target.closest('.menuwrap')) closeMenus(); });

// copyLink copies a link to the current file. A permalink pins the
// current commit, so it keeps pointing at the same code after the file
// changes. With pin, the address bar is also switched to the permalink.
async function copyLink(kind, pin) {
  const v = view, r = v.data;
  if (!r) return;
  const hash = location.hash.startsWith('#L') ? location.hash : '';
  let text, label;
  switch (kind) {
    case 'path': text = v.path; label = 'Path copied'; break;
    case 'raw': text = location.origin + '/raw/' + encodeURIComponent(v.repo) + '/' + encPath(v.path); label = 'Raw file URL copied'; break;
    case 'permalink': {
      const rev = v.rev || r.head;
      if (!rev) { kind = 'link'; break; }
      const u = fileURL(v.repo, v.path, {rev, at: v.at || r.gitPath}) + hash;
      text = location.origin + u; label = 'Permalink copied';
      if (pin) history.replaceState(null, '', u);
      break;
    }
  }
  if (kind === 'link') { text = location.origin + fileURL(v.repo, v.path) + hash; label = 'Link copied'; }
  toast(await copyText(text) ? label : 'Could not copy to the clipboard');
}

// ---- recent searches and files (per browser) ----

const recent = {
  max: {q: 50, f: 30},
  get(kind) {
    try { return JSON.parse(localStorage.getItem('cs-recent-' + kind)) || []; } catch (e) { return []; }
  },
  add(kind, item) {
    const key = x => kind === 'q' ? x : x.repo + '\0' + x.path;
    const list = [item, ...this.get(kind).filter(x => key(x) !== key(item))].slice(0, this.max[kind]);
    try { localStorage.setItem('cs-recent-' + kind, JSON.stringify(list)); } catch (e) {}
  },
  clear(kind) {
    try { localStorage.removeItem('cs-recent-' + kind); } catch (e) {}
  },
};

// ---- saved searches (shared, stored by the server) ----

let savedList = [];
async function loadSaved() {
  try { savedList = (await api('saved')).saved || []; } catch (e) {}
  if (view && view.kind === 'home') renderHomeLists();
  if (view && view.kind === 'search') setupSaveButton(view.q);
}

async function setSaved(query, add, name) {
  try {
    const r = await fetch('/api/saved', {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'X-Requested-With': 'cs'},
      body: JSON.stringify({action: add ? 'add' : 'remove', query, name: name || ''}),
    });
    const data = await r.json();
    if (!r.ok) throw new Error(data.error || r.statusText);
    savedList = data.saved;
    toast(add ? 'Search saved' : 'Saved search removed');
  } catch (e) { toast(e.message); }
}

function setupSaveButton(q) {
  const b = $('.savebtn', app);
  if (!b) return;
  const saved = savedList.some(x => x.query === q);
  b.textContent = saved ? '★ Saved' : '☆ Save search';
  b.classList.toggle('on', saved);
  b.title = saved ? 'Remove from saved searches' : 'Save this search';
  b.onclick = async () => {
    if (saved) await setSaved(q, false);
    else {
      const name = prompt('Name for this saved search:', q);
      if (name === null) return;
      await setSaved(q, true, name);
    }
    setupSaveButton(q);
  };
}

function renderHomeLists() {
  const el = $('#homelists');
  if (!el) return;
  const qs = recent.get('q').slice(0, 8), fs = recent.get('f').slice(0, 8);
  let html = '';
  if (savedList.length) {
    html += '<div class="section-title"><span>Saved searches</span></div><div class="hlist">' +
      savedList.map(x => `<span class="hchip saved"><a href="${searchURL(x.query)}" title="${esc(x.query)}"><span class="star">★</span>${esc(x.name)}</a><button data-unsave="${esc(x.query)}" title="Remove">×</button></span>`).join('') + '</div>';
  }
  if (qs.length) {
    html += '<div class="section-title"><span>Recent searches</span><span class="grow"></span><button class="btn" data-clear="q">Clear</button></div><div class="hlist">' +
      qs.map(q => `<span class="hchip"><a href="${searchURL(q)}">${esc(q)}</a></span>`).join('') + '</div>';
  }
  if (fs.length) {
    html += '<div class="section-title"><span>Recently viewed</span><span class="grow"></span><button class="btn" data-clear="f">Clear</button></div><ul class="rfiles">' +
      fs.map(f => `<li><a href="${fileURL(f.repo, f.path)}"><span class="repo-chip">${esc(f.repo)}</span>${icons.file}${pathHTML(f.path)}</a></li>`).join('') + '</ul>';
  }
  el.innerHTML = html;
  el.onclick = async e => {
    const un = e.target.closest('[data-unsave]');
    if (un) { await setSaved(un.dataset.unsave, false); renderHomeLists(); }
    const cl = e.target.closest('[data-clear]');
    if (cl) { recent.clear(cl.dataset.clear); renderHomeLists(); }
  };
}

// ---- context lines in search results ----

const CONTEXT = 3;

// renderSnippet redraws a file's result: the matching lines (all of them
// once expanded) and, with context on, the lines around them.
async function renderSnippet(fr, q) {
  const f = fr._file;
  const lines = fr._all ? f.lines : f.lines.slice(0, SNIP_LINES);
  const table = $('table.snip', fr);
  if (!fr._ctx) { table.innerHTML = snippetRows(f, lines, q); return; }
  let r;
  try {
    r = await api('context', {repo: f.repo, path: f.path, q, n: CONTEXT, lines: lines.map(l => l.line).join(',')});
  } catch (e) { if (!isAbort(e)) toast(e.message); return; }
  if (!fr._ctx) return;
  const match = new Map(lines.map(l => [l.line, l]));
  let prev = -1, out = '';
  for (const c of r.lines) {
    if (prev >= 0 && c.line !== prev + 1) out += '<tr class="gap"><td colspan="2"></td></tr>';
    const m = match.get(c.line);
    const href = fileURL(f.repo, f.path, {q, line: c.line});
    out += `<tr class="row${m ? (m.def ? ' def' : '') : ' ctx'}" data-href="${esc(href)}"><td class="ln"><a href="${esc(href)}">${c.line}</a></td><td class="code">${m ? m.html : c.html}</td></tr>`;
    prev = c.line;
  }
  table.innerHTML = out;
}

// ---- call hierarchy ----

// showCalls opens the call hierarchy for sym, from the cross reference
// panel x: callers of sym, or the functions its definition calls.
function showCalls(sym, mode, x) {
  const panel = ensurePanel();
  if (!panel) return;
  const from = x.from || {repo: view.repo, path: view.path};
  const def = (x.data.defs || [])[0];
  view.panel = {kind: 'calls', sym, mode, from, def, xref: x};
  renderCalls(panel, view.panel);
}

function renderCalls(panel, p) {
  const head = $('.xref-head', panel), body = $('.xref-body', panel);
  head.innerHTML = `<button class="tbtn" data-back="1" title="Back to definitions and references">← References</button>
    <span class="sym">${p.mode === 'callers' ? 'Callers of' : 'Calls from'} ${esc(p.sym)}</span>
    <span class="counts"></span><span class="grow"></span>
    <button class="tbtn${p.mode === 'callers' ? ' on' : ''}" data-mode="callers">Callers</button>
    <button class="tbtn${p.mode === 'callees' ? ' on' : ''}" data-mode="callees"${p.def ? '' : ' disabled'}>Callees</button>
    <button class="iconbtn" title="Close (Esc)">✕</button>`;
  $('.iconbtn', head).onclick = closePanel;
  $('[data-back]', head).onclick = () => { view.panel = p.xref; renderXref(panel, p.xref); };
  $$('[data-mode]', head).forEach(b => b.onclick = () => { p.mode = b.dataset.mode; renderCalls(panel, p); });
  body.innerHTML = '<ul class="ctree"></ul>';
  const root = $('.ctree', body);
  const node = p.mode === 'callers'
    ? {sym: p.sym, repo: p.from.repo, path: p.from.path}
    : {sym: p.sym, repo: p.def.repo, path: p.def.path, line: p.def.line};
  loadCallLevel(root, p.mode, node, $('.counts', head));
  body.onclick = e => {
    const tw = e.target.closest('.ctw');
    if (tw) { toggleCallNode(tw.closest('li'), p.mode); return; }
    const row = e.target.closest('tr.row');
    if (row && !e.target.closest('a') && !window.getSelection().toString()) navigate(row.dataset.href);
  };
}

const bareName = n => n.slice(Math.max(n.lastIndexOf('.'), n.lastIndexOf(':')) + 1);

// loadCallLevel fills ul with the callers (or callees) of node.
async function loadCallLevel(ul, mode, node, counts) {
  ul.innerHTML = '<li class="muted cload"><span class="spinner"></span> Loading…</li>';
  let r;
  try {
    r = mode === 'callers'
      ? await api('callers', {sym: node.sym, repo: node.repo, path: node.path})
      : await api('callees', {sym: node.sym, repo: node.repo, path: node.path, line: node.line || 0});
  } catch (e) {
    ul.innerHTML = `<li class="error">${esc(e.message)}</li>`;
    return;
  }
  const items = mode === 'callers' ? r.callers : r.callees;
  if (counts) {
    counts.textContent = mode === 'callers'
      ? `${fmtNum(r.numCalls)} call${r.numCalls === 1 ? '' : 's'} in ${fmtNum(items.length)} function${items.length === 1 ? '' : 's'}${r.truncated ? ' (partial)' : ''} · ${r.elapsed.toFixed(2)} s`
      : `${fmtNum(items.length)} function${items.length === 1 ? '' : 's'} called`;
  }
  if (!items.length) {
    ul.innerHTML = `<li class="muted cnone">${mode === 'callers' ? 'No calls found.' : 'No calls found in this function.'}</li>`;
    return;
  }
  ul.innerHTML = items.map((c, i) => mode === 'callers' ? callerHTML(c, i) : calleeHTML(c, node, i)).join('');
  Array.from(ul.children).forEach((li, i) => li._item = items[i]);
}

function callerHTML(c, i) {
  const k = kindLetter[c.kind] || 'f';
  const name = c.name || '(top level)';
  const href = fileURL(c.repo, c.path, {line: c.line || (c.calls[0] && c.calls[0].line)});
  return `<li><div class="cnode"><span class="ctw" title="Show call sites and callers">${icons.chevron}</span>
    <span class="okind k-${esc(c.kind || 'func')}">${esc(k)}</span>
    <a href="${esc(href)}" class="cname">${esc(name)}</a>
    <span class="cloc" title="${esc(c.repo + ': ' + c.path)}">${esc(c.path)}${c.line ? ':' + c.line : ''} · ${c.calls.length} call${c.calls.length === 1 ? '' : 's'}</span></div>
    <div class="ckids" hidden></div></li>`;
}

function calleeHTML(c, node, i) {
  const resolved = !!c.path;
  const href = resolved ? fileURL(c.repo, c.path, {line: c.defLine}) : fileURL(node.repo, node.path, {line: c.line});
  return `<li${resolved ? '' : ' class="unresolved"'}><div class="cnode">${resolved ? `<span class="ctw" title="Show what it calls">${icons.chevron}</span>` : '<span class="ctw none"></span>'}
    <span class="okind k-${esc(c.kind || 'func')}">${esc(kindLetter[c.kind] || 'f')}</span>
    <a href="${esc(href)}" class="cname">${esc(c.name)}</a>
    <span class="cloc">${resolved ? esc(c.path) + ':' + c.defLine : 'definition not indexed'}${c.count > 1 ? ' · ' + c.count + ' calls' : ''}</span></div>
    <div class="ckids" hidden></div></li>`;
}

async function toggleCallNode(li, mode) {
  const kids = $(':scope > .ckids', li);
  li.classList.toggle('open');
  kids.hidden = !li.classList.contains('open');
  if (kids.hidden || kids.dataset.loaded) return;
  kids.dataset.loaded = '1';
  const c = li._item;
  if (mode === 'callers') {
    const f = {repo: c.repo, path: c.path, lines: c.calls.map(s => ({line: s.line, html: s.html}))};
    kids.innerHTML = `<table class="snip csites">${snippetRows(f, f.lines, '')}</table>` + (c.name ? '<ul class="ctree"></ul>' : '');
    if (c.name) loadCallLevel($('ul', kids), mode, {sym: bareName(c.name), repo: c.repo, path: c.path});
  } else {
    kids.innerHTML = '<ul class="ctree"></ul>';
    loadCallLevel($('ul', kids), mode, {sym: c.name, repo: c.repo, path: c.path, line: c.defLine});
  }
}

// ---- keyboard ----

document.addEventListener('keydown', e => {
  const t = e.target;
  if (t.matches('input, textarea') || e.ctrlKey || e.metaKey || e.altKey || $('#help').open) return;
  const input = document.body.classList.contains('home') ? $('#app input') : qbox;
  if (e.key === '/') { e.preventDefault(); input.focus(); input.select(); }
  else if (e.key === '?') { $('#help').showModal(); }
  else if (e.key === 'Escape') { if ($('#hovercard') && !$('#hovercard').hidden) { hideHover(); return; } if (closeMenus()) return; if (view && view.find) closeFind(); else closePanel(); }
  else if (e.key === 't' && view && view.kind === 'file') { toggleTree(); }
  else if ((e.key === 'j' || e.key === 'k') && view && view.kind === 'search') { e.preventDefault(); moveCursor(e.key === 'j' ? 1 : -1); }
  else if ((e.key === 'Enter' || e.key === 'o') && view && view.kind === 'search' && view.cursor >= 0) { openCursor(); }
  else if (e.key === 'b' && view && view.kind === 'file' && view.data && view.data.git && !view.preview) { toggleBlame(); }
  else if (e.key === 'h' && view && view.kind === 'file' && view.data && view.data.git) { showHistory(); }
  else if (e.key === 'o' && view && view.kind === 'file') { e.preventDefault(); setSideTab('outline', true); }
  else if (e.key === 'f' && view && view.kind === 'file') { e.preventDefault(); openFind(); }
  else if (e.key === 'i' && view && view.kind === 'file') { if (hoverSelection()) e.preventDefault(); }
  else if (e.key === 'y' && view && view.kind === 'file' && view.data && view.data.kind === 'file') { copyLink('permalink', true); }
  else if ((e.key === 'n' || e.key === 'N') && view && view.marks) { gotoMark(e.key === 'n' ? 1 : -1); }
});

// ---- theme & help ----

$('#themebtn').onclick = () => {
  const dark = matchMedia('(prefers-color-scheme: dark)').matches;
  const cur = document.documentElement.dataset.theme || (dark ? 'dark' : 'light');
  const next = cur === 'dark' ? 'light' : 'dark';
  document.documentElement.dataset.theme = next;
  try { localStorage.setItem('cs-theme', next); } catch (e) {}
};
$('#helpbtn').onclick = () => $('#help').showModal();

attachSearchBox($('#topsearch'), qbox);
route();
pollStatus();
loadSaved();
