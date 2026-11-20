'use strict';

const $ = (s) => document.querySelector(s);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};
const fmtTs = (ts) => (ts ? new Date(ts * 1000).toLocaleString(undefined,
  { year: 'numeric', month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit' }) : '');
const money = (n) => `$${(n || 0).toLocaleString()}`;
// The token is minted per server process and arrives only in the HTML of the link the
// command printed, so reads and the write both carry it. A page left open across a restart
// gets told to reload rather than silently failing.
const TOKEN = (document.querySelector('meta[name="brain-token"]') || {}).content || '';
async function api(p) {
  const r = await fetch(p, { headers: { 'x-brain-token': TOKEN } });
  if (!r.ok) throw new Error(`${p} -> HTTP ${r.status}`);
  return r.json();
}
async function post(p, body) {
  const r = await fetch(p, {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'x-brain-token': TOKEN },
    body: JSON.stringify(body),
  });
  const d = await r.json().catch(() => ({ error: `HTTP ${r.status}` }));
  if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
  return d;
}

// chip("won", 12, " · $3,400") without ever touching innerHTML
function chip(label, count, tail) {
  const c = el('div', 'chip');
  c.appendChild(el('span', null, String(label)));
  if (count !== undefined && count !== null) {
    c.appendChild(document.createTextNode(' '));
    c.appendChild(el('b', null, String(count)));
  }
  if (tail) c.appendChild(document.createTextNode(tail));
  return c;
}

function showError(err) {
  const box = el('div', 'empty', `could not load: ${err.message}. Is the viewer still running?`);
  const main = document.querySelector('main');
  main.prepend(box);
}

const TIER_COLOR = { A: 'red', B: 'violet', C: 'cyan', D: 'green', N: '' };
const STATUS_COLOR = {
  won: 'green', negotiating: 'green', demo: 'green', trial: 'green',
  replied: 'honey', engaged: 'honey', contacted: 'cyan', researching: 'cyan',
  new: 'cyan', lost: 'red', skip: '',
};

function tagRow(...tags) {
  const row = el('div', 'row');
  for (const [text, cls] of tags) {
    if (text === '' || text == null) continue;
    row.appendChild(el('span', `tag ${cls || ''}`, text));
  }
  return row;
}

function bodyBlock(text) {
  const wrap = el('div');
  const b = el('div', 'body clamp', text);
  wrap.appendChild(b);
  if (text.length > 220) {
    const more = el('span', 'more', 'show more');
    more.onclick = () => {
      const open = b.classList.toggle('clamp');
      more.textContent = open ? 'show more' : 'show less';
    };
    wrap.appendChild(more);
  }
  return wrap;
}

// --- forget ---------------------------------------------------------------------
//
// The delete lives behind a form that will not submit without a reason, and every refusal
// it can meet — pinned, guardrail, head of a supersession chain — comes back from the CLI
// as the CLI's own text. --force is offered only after one of those has been read, which
// is the point of them: they exist to be read, not to be dismissed in advance.

function forgetControl(m, onDone) {
  const wrap = el('div', 'forget');
  const open = el('button', 'ghost danger', 'forget');
  wrap.appendChild(open);

  open.onclick = () => {
    open.remove();
    const form = el('div', 'forget-form');
    const why = el('input');
    why.type = 'text';
    why.placeholder = `why is #${m.id} going? the tombstone carries this`;
    const go = el('button', 'danger', 'delete');
    const cancel = el('button', 'ghost', 'cancel');
    const msg = el('div', 'forget-msg');
    let force = false;

    const send = async () => {
      const reason = why.value.trim();
      if (!reason) { why.focus(); msg.textContent = 'a reason is required'; return; }
      go.disabled = true;
      try {
        await post('/api/forget', { id: m.id, reason, force });
        onDone();
      } catch (err) {
        go.disabled = false;
        msg.textContent = '';
        // Multi-line refusals lose their shape in textContent alone.
        for (const line of String(err.message).split('\n')) {
          msg.appendChild(el('div', null, line));
        }
        if (!force) {
          const f = el('button', 'ghost danger', 'delete anyway (--force)');
          f.onclick = () => { force = true; send(); };
          msg.appendChild(f);
        }
      }
    };
    go.onclick = send;
    why.onkeydown = (e) => { if (e.key === 'Enter') send(); };
    cancel.onclick = () => { form.remove(); wrap.appendChild(open); };
    form.append(why, go, cancel, msg);
    wrap.appendChild(form);
    why.focus();
  };
  return wrap;
}

function fillSelect(sel, rows, key, keep) {
  const cur = keep ?? sel.value;
  sel.length = 1;
  for (const r of rows) {
    if (!r[key]) continue;
    const o = el('option', null, `${r[key]} (${r.c})`);
    o.value = r[key];
    sel.appendChild(o);
  }
  sel.value = cur;
}

// --- brief ----------------------------------------------------------------------

async function renderBrief() {
  const d = await api('/api/brief');
  const v = $('#view-brief');
  v.innerHTML = '';

  const stats = el('div', 'stats');
  const cards = [
    [d.stats.memories, 'memories'], [d.stats.pinned, 'pinned'],
    [d.stats.superseded || 0, 'superseded'], [d.stats.events, 'events'],
  ];
  if (d.stats.leads) {
    cards.push([d.stats.leads, 'leads'], [money(d.stats.open_pipeline), 'open pipeline'],
      [money(d.stats.won), 'won']);
  }
  for (const [n, l] of cards) {
    const c = el('div', 'stat');
    c.append(el('div', 'n', String(n)), el('div', 'l', l));
    stats.appendChild(c);
  }
  v.appendChild(stats);

  v.appendChild(el('h2', null, 'Pinned — read first'));
  const pins = el('div', 'list');
  for (const m of d.pinned) {
    const c = el('div', 'card pin');
    c.append(
      tagRow([`#${m.id}`, 'mono'], [m.mem_type, 'honey'], [m.scope], [`importance ${m.importance}`]),
      el('h3', null, m.title),
      bodyBlock(m.body),
    );
    pins.appendChild(c);
  }
  v.appendChild(pins.children.length ? pins : el('div', 'empty', 'nothing pinned'));

  if (d.guardrails && d.guardrails.length) {
    v.appendChild(el('h2', null, 'Guardrails — binding, always shown'));
    const gl = el('div', 'list');
    for (const m of d.guardrails) {
      const c = el('div', 'card guard');
      c.append(
        tagRow([`#${m.id}`, 'mono'], [m.mem_type, 'red'], [m.scope]),
        el('h3', null, m.title),
        bodyBlock(m.body),
      );
      gl.appendChild(c);
    }
    v.appendChild(gl);
  }

  if (d.measures && d.measures.length) {
    const breached = d.measures.filter((m) => m.breach).length;
    v.appendChild(el('h2', null, breached
      ? `Measures — ${breached} outside its band` : 'Measures'));
    const ml = el('div', 'list');
    for (const m of d.measures.slice(0, 12)) {
      ml.appendChild(measureCard(m, (k) => {
        switchTab('measures');
        $('#ms-key').value = k;
        loadMeasures().catch(showError);
      }));
    }
    v.appendChild(ml);
  }

  if (d.stats.leads) {
    v.appendChild(el('h2', null, 'Pipeline'));
    const chips = el('div', 'chips');
    for (const p of d.pipeline) {
      const c = chip(p.status, p.c, ` · ${money(p.v)}`);
      c.onclick = () => { switchTab('leads'); $('#lead-status').value = p.status; loadLeads(); };
      chips.appendChild(c);
    }
    v.appendChild(chips);

    if (d.hot.length) {
      v.appendChild(el('h2', null, 'Hot leads'));
      v.appendChild(leadList(d.hot));
    }

    v.appendChild(el('h2', null, 'Next actions'));
    const na = el('div', 'list');
    for (const l of d.next_actions) {
      const c = el('div', 'card');
      const top = el('div', 'row');
      top.append(el('span', 'id', `#${l.id}`), el('span', 'who', `${l.name} @ ${l.company}`));
      top.appendChild(el('span', 'spacer'));
      top.appendChild(el('span', `tag ${STATUS_COLOR[l.status] || ''}`, l.status));
      c.append(top, el('div', 'na', l.next_action + (l.next_date ? ` (${l.next_date})` : '')));
      na.appendChild(c);
    }
    v.appendChild(na.children.length ? na : el('div', 'empty', 'no open next actions'));
  }

  v.appendChild(el('h2', null, 'Recent activity'));
  v.appendChild(eventList(d.recent));
}

// --- memories -------------------------------------------------------------------

let memTimer;
async function loadMemories() {
  const q = new URLSearchParams({
    q: $('#mem-q').value.trim(),
    scope: $('#mem-scope').value,
    type: $('#mem-type').value,
  });
  const d = await api(`/api/memories?${q}`);
  fillSelect($('#mem-scope'), d.scopes, 'scope');
  fillSelect($('#mem-type'), d.types, 'mem_type');

  const list = $('#mem-list');
  list.innerHTML = '';
  if (!d.memories.length) { list.appendChild(el('div', 'empty', 'no memories match')); return; }
  for (const m of d.memories) {
    const c = el('div', `card ${m.pinned ? 'pin' : ''}`);
    c.append(
      tagRow(
        [`#${m.id}`, 'mono'],
        [m.mem_type, 'honey'],
        [`tier ${m.tier}`, TIER_COLOR[m.tier]],
        [m.scope],
        [m.pinned ? 'pinned' : '', 'honey'],
        [m.superseded_by ? `superseded by #${m.superseded_by}` : '', 'red'],
        [m.confidence && m.confidence !== 'reported' ? m.confidence : '',
          m.confidence === 'confirmed' ? 'green' : 'red'],
        [`imp ${m.importance}`],
        [`used ${m.use_count}x`],
        [`score ${m.score.toFixed(3)}`, 'cyan'],
      ),
      el('h3', null, m.title),
      bodyBlock(m.body),
      el('div', 'ts', `updated ${fmtTs(m.updated_at)}`
        + (m.verified_at ? ` · verified ${fmtTs(m.verified_at)}` : '')
        + (m.source && m.source !== 'session' ? ` · ${m.source}` : '')
        + (m.source_date ? ` (${m.source_date})` : '')
        + (m.last_used ? ` · last used ${fmtTs(m.last_used)}` : '')),
    );
    c.lastChild.style.cssText = 'color:var(--dim);font-size:12px;margin-top:8px';
    c.appendChild(forgetControl(m, () => { loadMemories().catch(showError); refreshHeader(); }));
    list.appendChild(c);
  }
}

// --- tree -----------------------------------------------------------------------
//
// Scopes nest on ':' and information flows inward only, which is the shape of the brain
// and the one thing every flat list here threw away. The nesting is derived from the
// scope strings the CLI returns; nothing about visibility or ranking is decided in here.

const collapsed = new Set();
// Every scope path drawn in the last render, so "collapse all" can name them without a
// second walk over the data.
const rendered = new Set();

function scopeTree(memories, byType) {
  const root = { name: '', path: '', kids: new Map(), mems: [] };
  for (const m of memories) {
    let node = root;
    for (const part of String(m.scope || 'global').split(':')) {
      if (!node.kids.has(part)) {
        node.kids.set(part, {
          name: part, path: node.path ? `${node.path}:${part}` : part,
          kids: new Map(), mems: [],
        });
      }
      node = node.kids.get(part);
    }
    node.mems.push(m);
  }
  const total = (n) => {
    n.total = n.mems.length;
    for (const k of n.kids.values()) n.total += total(k);
    return n.total;
  };
  total(root);
  if (byType) {
    const split = (n) => {
      for (const k of n.kids.values()) split(k);
      if (!n.mems.length) return;
      const groups = new Map();
      for (const m of n.mems) {
        if (!groups.has(m.mem_type)) groups.set(m.mem_type, []);
        groups.get(m.mem_type).push(m);
      }
      n.groups = [...groups.entries()].sort((a, b) => b[1].length - a[1].length);
    };
    split(root);
  }
  return root;
}

function memLine(m, reload) {
  const row = el('div', `tree-mem ${m.pinned ? 'pin' : ''}`);
  const head = el('div', 'row');
  head.append(
    el('span', 'tag mono', `#${m.id}`),
    el('span', `tag ${TIER_COLOR[m.tier] || ''}`, m.mem_type),
  );
  if (m.pinned) head.appendChild(el('span', 'tag honey', 'pinned'));
  if (m.superseded_by) head.appendChild(el('span', 'tag red', `→ #${m.superseded_by}`));
  head.appendChild(el('span', 'tree-title', m.title));
  head.appendChild(el('span', 'spacer'));
  head.appendChild(el('span', 'tag cyan', m.score.toFixed(2)));
  row.appendChild(head);
  const detail = el('div', 'tree-detail hidden');
  detail.append(bodyBlock(m.body), forgetControl(m, reload));
  row.appendChild(detail);
  head.onclick = () => detail.classList.toggle('hidden');
  return row;
}

function treeNode(node, declared, reload, depth) {
  rendered.add(node.path);
  const box = el('div', 'tree-node');
  const head = el('div', 'tree-head');
  const caret = el('span', 'caret', collapsed.has(node.path) ? '▸' : '▾');
  head.append(caret, el('span', 'tag cyan', node.name),
    el('span', 'tag mono', String(node.total)));
  if (node.mems.length && node.kids.size) {
    head.appendChild(el('span', 'muted', `${node.mems.length} here`));
  }
  if (declared[node.path]) head.appendChild(el('span', 'muted', declared[node.path]));
  box.appendChild(head);

  const kids = el('div', 'tree-kids');
  if (collapsed.has(node.path)) kids.classList.add('hidden');
  for (const k of [...node.kids.values()].sort((a, b) => b.total - a.total)) {
    kids.appendChild(treeNode(k, declared, reload, depth + 1));
  }
  if (node.groups) {
    for (const [type, rows] of node.groups) {
      const g = el('div', 'tree-group');
      g.appendChild(el('div', 'tree-grouphead', `${type} (${rows.length})`));
      for (const m of rows) g.appendChild(memLine(m, reload));
      kids.appendChild(g);
    }
  } else {
    for (const m of node.mems) kids.appendChild(memLine(m, reload));
  }
  box.appendChild(kids);

  head.onclick = () => {
    const now = kids.classList.toggle('hidden');
    caret.textContent = now ? '▸' : '▾';
    if (now) collapsed.add(node.path); else collapsed.delete(node.path);
  };
  return box;
}

async function loadTree() {
  const d = await api('/api/tree');
  const q = $('#tree-q').value.trim().toLowerCase();
  const rows = q
    ? d.memories.filter((m) => `${m.title} ${m.body}`.toLowerCase().includes(q))
    : d.memories;
  const root = scopeTree(rows, $('#tree-group').value === 'scope-type');
  const v = $('#tree-root');
  v.innerHTML = '';
  rendered.clear();
  const reload = () => { loadTree().catch(showError); refreshHeader(); };
  if (!rows.length) {
    v.appendChild(el('div', 'empty', q ? 'nothing matches' : 'no memories yet'));
    return;
  }
  for (const k of [...root.kids.values()].sort((a, b) => b.total - a.total)) {
    v.appendChild(treeNode(k, d.declared, reload, 0));
  }
  if (d.tombstones && d.tombstones.length) {
    v.appendChild(el('h2', null, 'Forgotten'));
    const note = el('div', 'empty',
      'deleted on purpose — do not relearn without new evidence');
    note.style.cssText = 'text-align:left;padding:4px 0';
    v.appendChild(note);
    const list = el('div', 'list');
    for (const t of d.tombstones) {
      const c = el('div', 'card');
      c.append(tagRow([t.scope, 'cyan'], [fmtTs(t.forgotten_at), 'mono']),
        el('h3', null, t.title));
      if (t.reason) c.appendChild(el('div', 'body', t.reason));
      list.appendChild(c);
    }
    v.appendChild(list);
  }
}

// --- measures -------------------------------------------------------------------
//
// The spark string, the trend and the breach all come from the CLI. Nothing here
// recomputes them: a second implementation of "is this reading outside its band" is
// exactly the drift that took the SQL out of this viewer.

const fmtNum = (v) => (v === null || v === undefined ? ''
  : (Number.isInteger(v) ? String(v) : String(Number(v.toFixed(6)))));

function measureCard(s, onPick) {
  const c = el('div', s.breach ? 'card guard' : 'card');
  const tags = [[s.key, 'mono'], [`${s.n} points`]];
  if (s.unit) tags.push([s.unit]);
  if (s.breach) tags.push([`${s.breach} ${fmtNum(s.breach === 'below' ? s.low : s.high)}`, 'red']);
  else if (s.low !== null || s.high !== null) {
    tags.push([`band ${fmtNum(s.low)}..${fmtNum(s.high)}`, 'cyan']);
  }
  c.appendChild(tagRow(...tags));
  const head = el('div', 'row');
  head.appendChild(el('h3', null, `${s.label} ${fmtNum(s.latest)}${s.unit ? ' ' + s.unit : ''}`));
  if (s.delta !== null && s.delta !== undefined) {
    const sign = s.delta > 0 ? '+' : '';
    head.appendChild(el('span', 'muted',
      `${s.arrow} ${sign}${fmtNum(s.delta)}${s.previous_on ? ' since ' + s.previous_on : ''}`
      + (s.direction ? ` (${s.direction})` : '')));
  }
  c.appendChild(head);
  if (s.spark) c.appendChild(el('div', 'spark', s.spark));
  c.appendChild(el('div', 'muted',
    `${s.first_on || ''} → ${s.latest_on || ''} · min ${fmtNum(s.min)} · mean `
    + `${fmtNum(Number(s.mean.toFixed(3)))} · max ${fmtNum(s.max)}`));
  if (s.why) c.appendChild(el('div', 'muted', s.why));
  if (onPick) c.onclick = () => onPick(s.key);
  return c;
}

async function loadMeasures() {
  const key = $('#ms-key').value;
  // Only send a date the CLI can read. It exits non-zero on anything else -- correctly,
  // since a silently ignored filter is worse -- but half a date mid-keystroke is not a
  // mistake worth an error banner.
  const done = (v) => (/^\d{4}-\d{2}-\d{2}$/.test(v) || /^\d{1,2}[/-]\d{1,2}([/-]\d{4})?$/.test(v)
    ? v : '');
  const from = done($('#ms-from').value.trim());
  const to = done($('#ms-to').value.trim());
  const q = new URLSearchParams();
  if (key) q.set('key', key);
  if (from) q.set('from', from);
  if (to) q.set('to', to);
  const d = await api(`/api/measures?${q}`);

  const sel = $('#ms-key');
  if (sel.options.length !== d.measures.length + 1) {
    sel.innerHTML = '';
    const all = el('option', null, 'every series');
    all.value = '';
    sel.appendChild(all);
    for (const m of d.measures) {
      const o = el('option', null, m.label || m.key);
      o.value = m.key;
      sel.appendChild(o);
    }
    sel.value = key;
  }

  const list = $('#ms-list');
  list.innerHTML = '';
  const points = $('#ms-points');
  points.innerHTML = '';

  if (!d.measures.length) {
    list.appendChild(el('div', 'empty', 'no measures recorded'));
    return;
  }
  const pick = (k) => { $('#ms-key').value = k; loadMeasures().catch(showError); };
  for (const m of d.measures) {
    if (key && m.key !== key) continue;
    list.appendChild(measureCard(m, key ? null : pick));
  }

  if (!key) return;
  if (!d.series || !d.series.matched) {
    points.appendChild(el('div', 'empty', 'no readings in that window'));
    return;
  }
  points.appendChild(el('h2', null, `${d.series.count} readings`));
  for (const p of d.series.points.slice().reverse()) {
    const c = el('div', 'card');
    c.appendChild(tagRow([p.taken_on, 'mono'], ...(p.unit ? [[p.unit]] : []),
      ...(p.scope && p.scope !== 'global' ? [[p.scope]] : [])));
    c.appendChild(el('h3', null, `${fmtNum(p.value)}${p.unit ? ' ' + p.unit : ''}`));
    if (p.note) c.appendChild(el('div', null, p.note));
    if (p.ref) c.appendChild(el('div', 'muted', p.ref));
    points.appendChild(c);
  }
}

// --- leads ----------------------------------------------------------------------

function leadList(leads) {
  const list = el('div', 'list');
  for (const l of leads) {
    const c = el('div', 'card');
    const top = el('div', 'row');
    top.append(el('span', 'id', `#${l.id}`), el('span', 'who', l.name));
    if (l.company) top.appendChild(el('span', 'co', `@ ${l.company}`));
    top.appendChild(el('span', 'spacer'));
    if (l.value_est) top.appendChild(el('span', 'tag honey', money(l.value_est)));
    top.appendChild(el('span', `tag ${STATUS_COLOR[l.status] || ''}`, l.status));
    c.appendChild(top);

    const meta = tagRow([l.role], [l.segment, 'violet'], [l.channel, 'cyan'],
      [l.tier ? `tier ${l.tier}` : '']);
    if (meta.children.length) c.appendChild(meta);
    if (l.url && /^https?:\/\//i.test(l.url)) {
      const a = el('a', null, l.url);
      a.href = l.url; a.target = '_blank'; a.rel = 'noreferrer';
      const w = el('div'); w.style.fontSize = '12px'; w.appendChild(a);
      c.appendChild(w);
    }
    if (l.next_action) c.appendChild(el('div', 'na', `→ ${l.next_action}${l.next_date ? ` (${l.next_date})` : ''}`));
    if (l.notes) c.appendChild(el('div', 'notes', l.notes));
    list.appendChild(c);
  }
  return list;
}

async function loadLeads() {
  const q = new URLSearchParams({
    q: $('#lead-q').value.trim(),
    status: $('#lead-status').value,
    segment: $('#lead-segment').value,
  });
  const d = await api(`/api/leads?${q}`);
  fillSelect($('#lead-status'), d.statuses, 'status');
  fillSelect($('#lead-segment'), d.segments, 'segment');

  const sum = $('#lead-summary');
  sum.innerHTML = '';
  const total = d.leads.reduce((a, l) => a + (l.value_est || 0), 0);
  sum.appendChild(chip('showing', d.leads.length, ` · ${money(total)} est`));
  for (const st of d.statuses) {
    const c = chip(st.status, st.c);
    if ($('#lead-status').value === st.status) c.classList.add('on');
    c.onclick = () => {
      $('#lead-status').value = $('#lead-status').value === st.status ? '' : st.status;
      loadLeads();
    };
    sum.appendChild(c);
  }

  const list = $('#lead-list');
  list.innerHTML = '';
  if (!d.leads.length) { list.appendChild(el('div', 'empty', 'no leads match')); return; }
  list.appendChild(leadList(d.leads));
}

// --- events ---------------------------------------------------------------------

function eventList(events) {
  const tl = el('div', 'timeline');
  events.forEach((e, i) => {
    const n = el('div', `ev ${i === 0 ? 'first' : ''}`);
    const head = el('div', 'row');
    head.append(el('span', 'ts', fmtTs(e.ts)), el('span', 'tag honey', e.kind));
    if (e.scope && e.scope !== 'global') head.appendChild(el('span', 'tag', e.scope));
    if (e.actor) head.appendChild(el('span', 'tag', e.actor));
    n.append(head, el('div', 'body', e.summary));
    if (e.ref) {
      const a = el('a', null, e.ref);
      if (/^https?:/.test(e.ref)) { a.href = e.ref; a.target = '_blank'; a.rel = 'noreferrer'; }
      const w = el('div'); w.appendChild(a);
      n.appendChild(w);
    }
    tl.appendChild(n);
  });
  return tl;
}

async function loadEvents() {
  const q = new URLSearchParams({
    q: $('#ev-q').value.trim(),
    kind: $('#ev-kind').value,
    scope: $('#ev-scope').value,
  });
  const d = await api(`/api/events?${q}`);
  fillSelect($('#ev-kind'), d.kinds, 'kind');
  fillSelect($('#ev-scope'), d.scopes, 'scope');
  const list = $('#ev-list');
  list.innerHTML = '';
  if (!d.events.length) { list.appendChild(el('div', 'empty', 'no activity matches')); return; }
  list.appendChild(eventList(d.events));
}

// --- map ------------------------------------------------------------------------

function matchDeclared(scope, declared) {
  const rx = (p) => new RegExp(`^${p.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/\\\*/g, '.*')}$`);
  return Object.keys(declared).find((p) => rx(p).test(scope));
}

async function loadMap() {
  const d = await api('/api/map');
  const v = $('#view-map');
  v.innerHTML = '';

  const undeclared = [];
  const matched = new Set();
  for (const e of d.scopes) {
    const hit = matchDeclared(e.scope, d.declared);
    if (hit) matched.add(hit);
    else if (Object.keys(d.declared).length) undeclared.push(e.scope);
  }

  const section = (title, note) => {
    v.appendChild(el('h2', null, title));
    if (note) {
      const n = el('div', 'empty', note);
      n.style.cssText = 'text-align:left;padding:4px 0';
      v.appendChild(n);
    }
  };

  const scopeCards = (names) => {
    const list = el('div', 'list');
    for (const e of d.scopes.filter((x) => names.includes(x.scope))) {
      const c = el('div', 'card');
      const t = e.tiers;
      c.append(
        tagRow([e.scope, 'cyan'], [`${e.count} mems`, 'mono'],
          [e.pinned ? `${e.pinned} pinned` : '', 'honey'],
          [`A${t.A} B${t.B} C${t.C} D${t.D} N${t.N}`, 'mono']),
        el('div', 'ts', `last updated ${fmtTs(e.last)}`),
      );
      const desc = d.declared[matchDeclared(e.scope, d.declared)];
      if (desc) c.appendChild(el('div', 'body', desc));
      for (const m of e.top) {
        const line = el('div', 'na');
        line.textContent = `#${m.id} [${m.type}] ${m.title}`;
        line.onclick = () => {
          switchTab('memories');
          $('#mem-scope').value = e.scope;
          $('#mem-q').value = '';
          loadMemories();
        };
        line.style.cursor = 'pointer';
        c.appendChild(line);
      }
      list.appendChild(c);
    }
    return list;
  };

  section('Scopes');
  v.appendChild(scopeCards(d.scopes.map((e) => e.scope).filter((s) => !undeclared.includes(s))));

  if (undeclared.length) {
    section('Undeclared scopes in use', 'declare them in brain.json or refile the memories');
    v.appendChild(scopeCards(undeclared));
  }

  const empty = Object.keys(d.declared).filter((p) => !matched.has(p));
  if (empty.length) {
    section('Declared but empty');
    const chips = el('div', 'chips');
    for (const p of empty) {
      const c = chip(p, 0);
      c.title = d.declared[p] || '';
      chips.appendChild(c);
    }
    v.appendChild(chips);
  }

  section('Types by tier');
  const tchips = el('div', 'chips');
  for (const tier of ['A', 'B', 'C', 'D', 'N']) {
    for (const r of d.types.filter((x) => x.tier === tier)) {
      const c = chip(r.mem_type, r.c);
      c.prepend(el('span', `tag ${TIER_COLOR[tier]}`, tier), document.createTextNode(' '));
      c.onclick = () => { switchTab('memories'); $('#mem-type').value = r.mem_type; loadMemories(); };
      tchips.appendChild(c);
    }
  }
  v.appendChild(tchips);

  if (d.kinds.length) {
    section('Event kinds');
    const kchips = el('div', 'chips');
    for (const r of d.kinds) {
      const c = chip(r.kind, r.c);
      c.title = `last ${fmtTs(r.t)}`;
      c.onclick = () => { switchTab('events'); $('#ev-kind').value = r.kind; loadEvents(); };
      kchips.appendChild(c);
    }
    v.appendChild(kchips);
  }

  if (d.tombstones.length) {
    section('Forgotten', 'deleted on purpose — do not relearn without new evidence');
    const list = el('div', 'list');
    for (const t of d.tombstones) {
      const c = el('div', 'card');
      c.append(tagRow([t.scope, 'cyan'], [fmtTs(t.forgotten_at), 'mono']),
        el('h3', null, t.title));
      if (t.reason) c.appendChild(el('div', 'body', t.reason));
      list.appendChild(c);
    }
    v.appendChild(list);
  }
}

// --- wiring ---------------------------------------------------------------------

const LOADERS = { brief: renderBrief, map: loadMap, tree: loadTree, memories: loadMemories,
  measures: loadMeasures, leads: loadLeads, events: loadEvents };
let current = 'brief';
let WHERE = '';

async function refreshBrand() {
  const m = await api('/api/meta');
  document.title = `${m.project} Brain`;
  $('#brand-name').textContent = m.project;
  // The store may hold other projects. Say whose memories these are, and WHICH store:
  // that is the first question anybody asks of a page like this.
  WHERE = `local file · ${m.db || 'unknown'}`;
  const who = `on ${m.machine || 'this machine'}`;
  const line = $('#dbline');
  if (line) line.title = `${WHERE}\n${who}\nshowing ${m.project} only`;
}

async function refreshHeader() {
  const s = await api('/api/stats');
  $('#dbline').textContent = `${s.memories} memories · ${s.events} events`
    + (s.leads ? ` · ${s.leads} leads` : '') + (WHERE ? ` · ${WHERE}` : '');
  // The leads tab is dead weight in a project with no pipeline.
  const tab = document.querySelector('#tabs button[data-tab="leads"]');
  if (tab) tab.classList.toggle('hidden', !s.leads);
  const mtab = document.querySelector('#tabs button[data-tab="measures"]');
  if (mtab) mtab.classList.toggle('hidden', !s.measures);
}

function switchTab(tab) {
  current = tab;
  refreshHeader().catch(showError);
  for (const b of document.querySelectorAll('#tabs button')) b.classList.toggle('on', b.dataset.tab === tab);
  for (const v of document.querySelectorAll('.view')) v.classList.toggle('hidden', v.id !== `view-${tab}`);
  location.hash = tab;
  LOADERS[tab]().catch(showError);
}

for (const b of document.querySelectorAll('#tabs button')) b.onclick = () => switchTab(b.dataset.tab);
window.onhashchange = () => {
  const tab = location.hash.slice(1);
  if (LOADERS[tab] && tab !== current) switchTab(tab);
};
$('#refresh').onclick = () => {
  document.querySelectorAll('main > .empty').forEach((n) => n.remove());
  refreshHeader().catch(showError);
  LOADERS[current]().catch(showError);
};

const debounce = (fn) => { clearTimeout(memTimer); memTimer = setTimeout(fn, 180); };
$('#mem-q').oninput = () => debounce(loadMemories);
$('#mem-scope').onchange = loadMemories;
$('#mem-type').onchange = loadMemories;
$('#lead-q').oninput = () => debounce(loadLeads);
$('#lead-status').onchange = loadLeads;
$('#lead-segment').onchange = loadLeads;
$('#ms-key').onchange = loadMeasures;
$('#ms-from').oninput = () => debounce(loadMeasures);
$('#ms-to').oninput = () => debounce(loadMeasures);
$('#tree-q').oninput = () => debounce(loadTree);
$('#tree-group').onchange = () => loadTree().catch(showError);
$('#tree-expand').onclick = () => { collapsed.clear(); loadTree().catch(showError); };
$('#tree-collapse').onclick = () => {
  for (const p of rendered) collapsed.add(p);
  loadTree().catch(showError);
};
$('#ev-q').oninput = () => debounce(loadEvents);
$('#ev-kind').onchange = loadEvents;
$('#ev-scope').onchange = loadEvents;

document.onkeydown = (e) => {
  if (e.key === '/' && document.activeElement.tagName !== 'INPUT') {
    const box = { memories: '#mem-q', leads: '#lead-q', events: '#ev-q',
      tree: '#tree-q' }[current];
    if (box) { e.preventDefault(); $(box).focus(); }
  }
};

refreshBrand().catch(showError);
switchTab(LOADERS[location.hash.slice(1)] ? location.hash.slice(1) : 'brief');
