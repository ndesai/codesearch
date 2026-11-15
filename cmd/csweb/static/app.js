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
  if (opts.q) u += '?q=' + encodeURIComponent(opts.q);
  if (opts.line) u += '#L' + opts.line;
  return u;
}
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
    renderFile(repo, path, q);
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
    el.innerHTML = `<span class="spinner"></span>${what}`;
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
      <div class="path" title="${esc(r.root)}">${esc(r.root)}</div>
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
      if (f.all) return `<li class="searchall${i === sel ? ' sel' : ''}" data-i="${i}">${icons.search.replace('<svg', '<svg style="width:16px;height:16px;fill:currentColor;vertical-align:-3px"')} Search for <b>${esc(input.value)}</b></li>`;
      const j = f.path.lastIndexOf('/');
      return `<li data-i="${i}" class="${i === sel ? 'sel' : ''}"><span class="repo-chip">${esc(f.repo)}</span>${f.dir ? icons.dir : icons.file}<span class="base">${esc(f.path.slice(j + 1))}${f.dir ? '/' : ''}</span><span class="dir">${esc(f.path.slice(0, j + 1))}</span></li>`;
    }).join('');
    list.hidden = false;
  };
  const choose = i => {
    const f = items[i];
    list.hidden = true;
    if (!f || f.all) { submit(); return; }
    input.blur();
    navigate(fileURL(f.repo, f.path));
  };
  const submit = () => {
    const q = input.value.trim();
    list.hidden = true;
    if (!q) return;
    input.blur();
    navigate(searchURL(q));
  };
  input.addEventListener('input', () => {
    clearTimeout(timer);
    const q = input.value.trim();
    if (q.length < 2) { items = []; render(); return; }
    timer = setTimeout(async () => {
      try {
        const r = await api('suggest', {q}, 'suggest');
        if (input.value.trim() !== q || document.activeElement !== input) return;
        items = (r.files || []);
        if (items.length) items.push({all: true});
        sel = -1;
        render();
      } catch (e) { if (!isAbort(e)) console.warn(e); }
    }, 120);
  });
  input.addEventListener('keydown', e => {
    if (e.key === 'ArrowDown' && items.length) { sel = (sel + 1) % items.length; render(); e.preventDefault(); }
    else if (e.key === 'ArrowUp' && items.length) { sel = (sel - 1 + items.length) % items.length; render(); e.preventDefault(); }
    else if (e.key === 'Escape') { if (!list.hidden) list.hidden = true; else input.blur(); }
    else if (e.key === 'Enter') { e.preventDefault(); sel >= 0 ? choose(sel) : submit(); }
  });
  input.addEventListener('blur', () => setTimeout(() => list.hidden = true, 150));
  list.addEventListener('mousedown', e => {
    const li = e.target.closest('li');
    if (li) { e.preventDefault(); choose(+li.dataset.i); }
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
  const parts = [];
  let summary = r.numFiles === 0 ? 'No results' :
    `<b>${fmtNum(r.numMatches)}</b> match${r.numMatches === 1 ? '' : 'es'} in <b>${fmtNum(r.numFiles)}</b> file${r.numFiles === 1 ? '' : 's'}`;
  summary += ` <span class="muted">(${r.elapsed.toFixed(3)} s)</span>`;
  if (r.timedOut) summary += ' · <span class="warn">search timed out; results are partial</span>';
  else if (r.truncated) summary += ' · <span class="warn">showing the first results only; refine your query</span>';
  parts.push(`<div class="summary">${summary}</div>`);
  if (r.nameMatches && r.nameMatches.length) {
    parts.push(`<div class="namehits"><div class="section-title">Files and folders${r.numNameMatches > r.nameMatches.length ? ` <span class="muted" style="text-transform:none;letter-spacing:0">(${fmtNum(r.numNameMatches)}; <a href="${searchURL('file:' + q)}">see all</a>)</span>` : ''}</div><ul>` +
      r.nameMatches.map(f => `<li><a href="${fileURL(f.repo, f.path)}"><span class="repo-chip">${esc(f.repo)}</span>${f.dir ? icons.dir : icons.file}${pathHTML(f.path + (f.dir ? '/' : ''))}</a></li>`).join('') +
      '</ul></div>');
    if (r.files.length) parts.push('<div class="section-title">Code</div>');
  }
  parts.push('<div id="filelist"></div>');
  if (r.numFiles === 0 && !(r.nameMatches || []).length) {
    parts.push(`<div class="empty">No matches for <code>${esc(q)}</code>.<br><br><span style="font-size:13px">Tips: terms are regular expressions and are case-insensitive unless they contain upper-case letters. Use quotes for literal text.</span></div>`);
  }
  app.innerHTML = `<div class="results">${parts.join('')}</div>`;
  bindSnippets($('#filelist'), q);
  appendResults(r, q);
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
      <span class="meta">${esc(f.lang)}${f.numMatches ? ` · ${fmtNum(f.numMatches)} match${f.numMatches === 1 ? '' : 'es'}` : ''}</span></div>
    ${f.lines.length ? `<table class="snip">${snippetRows(f, shown, q)}</table>` : ''}
    ${rest > 0 ? `<button class="more" data-more="1">Show ${rest} more match${rest === 1 ? '' : 'es'}</button>` :
      unshown > 0 ? `<a class="more" href="${fileURL(f.repo, f.path, {q})}">View all ${fmtNum(f.numMatches)} matches in file</a>` : ''}
  </div>`;
}

function bindSnippets(root, q) {
  root.addEventListener('click', e => {
    const more = e.target.closest('button[data-more]');
    if (more) {
      const fr = more.closest('.fr');
      const f = fr._file;
      $('table.snip', fr).innerHTML = snippetRows(f, f.lines, q);
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

async function renderFile(repo, path, q) {
  // Reuse the layout (and tree) when moving within the same repo.
  let fv = $('.fv', app);
  const sameRepo = view && view.kind === 'file' && view.repo === repo && fv;
  const prev = view;
  view = {kind: 'file', repo, path, q, xref: prev && prev.kind === 'file' ? prev.xref : null};
  const v = view;
  document.title = (path ? path.split('/').pop() : repo) + ' - ND Code Search';
  if (!sameRepo) {
    app.innerHTML = `<div class="fv${treeHidden ? ' notree' : ''}"><nav class="tree" aria-label="Files"></nav>
      <section class="fmain"><div class="fhead"></div><div class="fbody"><div class="codewrap"><div class="loading"><span class="spinner"></span>Loading…</div></div></div></section></div>`;
    fv = $('.fv', app);
    buildTree(repo);
  }
  revealInTree(repo, path);
  const head = $('.fhead', fv), wrap = $('.codewrap', fv);
  head.innerHTML = crumbsHTML(repo, path);

  let r;
  try {
    r = await api('file', {repo, path, q, readme: 1}, 'file');
  } catch (e) {
    if (isAbort(e)) return;
    if (view === v) wrap.innerHTML = `<div class="notice">${esc(e.message)}</div>`;
    return;
  }
  if (view !== v) return;
  v.data = r;
  wrap.scrollTop = 0;
  if (r.kind === 'dir') {
    head.innerHTML = crumbsHTML(repo, path) + `<span class="finfo">${fmtNum(r.entries.length)} items</span>`;
    treeCache.set(repo + '\0' + path, Promise.resolve(r.entries));
    const up = path ? `<tr><td colspan="2"><a href="${fileURL(repo, path.split('/').slice(0, -1).join('/'))}">${icons.dir}..</a></td></tr>` : '';
    const rows = r.entries.map(e => `<tr class="${e.excluded ? 'excluded' : ''}"><td><a href="${fileURL(repo, (path ? path + '/' : '') + e.name)}">${e.dir ? icons.dir : icons.file}${esc(e.name)}</a></td><td class="size">${e.dir ? '' : fmtBytes(e.size)}</td></tr>`).join('');
    const readme = r.readme ? `<div class="readme"><h3>${esc(r.readme.name)}</h3>${codeHTML(r.readme.lines)}</div>` : '';
    wrap.innerHTML = `<div class="dirlist"><table>${up}${rows}</table></div>${readme}`;
    restoreXref();
    return;
  }

  const nlines = r.lines ? r.lines.length : 0;
  head.innerHTML = crumbsHTML(repo, path) +
    `<span class="finfo">${esc(r.lang)}${nlines ? ' · ' + fmtNum(nlines) + ' lines' : ''} · ${fmtBytes(r.size)}</span>` +
    `<span class="findnav" hidden></span>` +
    `<span class="factions">
      <button class="iconbtn" data-act="tree" title="Toggle file tree (t)">${icons.tree}</button>
      <button class="iconbtn" data-act="copy" title="Copy path">${icons.copy}</button>
      <a class="iconbtn" href="/raw/${encodeURIComponent(repo)}/${encPath(path)}" target="_blank" title="Raw file">${icons.raw}</a>
    </span>`;
  head.onclick = e => {
    const b = e.target.closest('[data-act]');
    if (!b) return;
    if (b.dataset.act === 'copy') { navigator.clipboard?.writeText(path).then(() => toast('Path copied')); }
    if (b.dataset.act === 'tree') toggleTree();
  };

  if (r.tooLarge) wrap.innerHTML = `<div class="notice">This file is too large to display (${fmtBytes(r.size)}). <a href="/raw/${encodeURIComponent(repo)}/${encPath(path)}" target="_blank">View raw</a></div>`;
  else if (r.image) wrap.innerHTML = `<div class="notice"><img src="/raw/${encodeURIComponent(repo)}/${encPath(path)}" alt="${esc(path)}"></div>`;
  else if (r.binary) wrap.innerHTML = `<div class="notice">Binary file not shown (${fmtBytes(r.size)}). <a href="/raw/${encodeURIComponent(repo)}/${encPath(path)}?download=1">Download</a></div>`;
  else {
    wrap.innerHTML = codeHTML(r.lines, true);
    setupFind(head, wrap, q);
  }
  onHash(true);
  restoreXref();
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
  const nav = $('.findnav', head);
  if (!q || !marks.length) { view.marks = null; return; }
  view.marks = marks; view.markIdx = -1;
  nav.hidden = false;
  const update = () => {
    nav.innerHTML = `<span>${view.markIdx + 1 || '–'} of ${fmtNum(marks.length)}</span>
      <button class="iconbtn" data-dir="-1" title="Previous match (N)">↑</button><button class="iconbtn" data-dir="1" title="Next match (n)">↓</button>`;
  };
  view.findUpdate = update;
  update();
  nav.onclick = e => { const b = e.target.closest('button'); if (b) gotoMark(+b.dataset.dir); };
  // Start at the linked line, or the first match.
  if (!location.hash) gotoMark(1);
  else {
    const line = /^#L(\d+)/.exec(location.hash);
    if (line) view.markIdx = Math.max(0, marks.findIndex(m => +m.closest('.line').id.slice(1) >= +line[1])) - 1;
    gotoMark(1, true);
  }
}

function gotoMark(dir, noScroll) {
  const marks = view.marks;
  if (!marks) return;
  marks[view.markIdx]?.classList.remove('cur');
  view.markIdx = (view.markIdx + dir + marks.length) % marks.length;
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
  return identRE.test(w) ? {word: w, el: node.parentElement} : null;
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

async function showXref(sym, repo, path, line, jump) {
  const fmain = $('.fmain', app);
  if (!fmain) return;
  let panel = $('.xref', fmain);
  if (!panel) {
    panel = document.createElement('div');
    panel.className = 'xref';
    panel.innerHTML = '<div class="drag"></div><div class="xref-head"></div><div class="xref-body"></div>';
    $('.fbody', fmain).append(panel);
    setupDrag(panel);
  }
  const savedH = sessionStorage.getItem('cs-xref-h');
  if (savedH) panel.style.height = savedH;
  const head = $('.xref-head', panel), body = $('.xref-body', panel);
  head.innerHTML = `<span class="sym">${esc(sym)}</span><span class="counts"><span class="spinner" style="display:inline-block;vertical-align:-2px"></span></span><button class="iconbtn" title="Close (Esc)">✕</button>`;
  $('button', head).onclick = closeXref;
  let r;
  try {
    r = await api('xref', {sym, repo, path}, 'xref');
  } catch (e) {
    if (isAbort(e)) return;
    body.innerHTML = `<div class="error">${esc(e.message)}</div>`;
    $('.counts', head).textContent = '';
    return;
  }
  view.xref = {sym, data: r, from: {repo, path, line}};
  if (jump) {
    const d = (r.defs || []).find(d => !(d.repo === repo && d.path === path && d.line === line)) || (r.defs || [])[0];
    if (d) {
      navigate(fileURL(d.repo, d.path, {line: d.line}));
      return;
    }
    toast('No definition found for ' + sym);
  }
  renderXref(panel, view.xref);
}

function renderXref(panel, x) {
  const r = x.data, head = $('.xref-head', panel), body = $('.xref-body', panel);
  const nd = (r.defs || []).length;
  head.innerHTML = `<span class="sym">${esc(r.symbol)}</span>
    <span class="counts">${nd} definition${nd === 1 ? '' : 's'} · ${fmtNum(r.numRefs)} reference${r.numRefs === 1 ? '' : 's'} in ${fmtNum(r.numFiles)} file${r.numFiles === 1 ? '' : 's'}${r.truncated ? ' (partial)' : ''} · ${r.elapsed.toFixed(2)} s</span>
    <a href="${searchURL('\\b' + r.symbol + '\\b case:yes')}" style="font-size:12px;margin-left:8px">Search</a>
    <button class="iconbtn" title="Close (Esc)">✕</button>`;
  $('button', head).onclick = closeXref;
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

function restoreXref() {
  const x = view.xref;
  if (!x) return;
  const fmain = $('.fmain', app);
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
  renderXref(panel, x);
  highlightSym(x.sym);
}

function closeXref() {
  $('.xref', app)?.remove();
  if (view) view.xref = null;
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

// ---- keyboard ----

document.addEventListener('keydown', e => {
  const t = e.target;
  if (t.matches('input, textarea') || e.ctrlKey || e.metaKey || e.altKey || $('#help').open) return;
  const input = document.body.classList.contains('home') ? $('#app input') : qbox;
  if (e.key === '/') { e.preventDefault(); input.focus(); input.select(); }
  else if (e.key === '?') { $('#help').showModal(); }
  else if (e.key === 'Escape') { closeXref(); }
  else if (e.key === 't' && view && view.kind === 'file') { toggleTree(); }
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
