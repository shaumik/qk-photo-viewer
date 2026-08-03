/* QK viewer: keyboard-first culling on desktop, touch on remote (phone) sessions.
   All photo access goes through a backend object, picked at boot:
   Wails bridge (inside the app) > HTTP (phone remote / qkserve) > mock. */
'use strict';

let backend = null; // assigned in init before anything runs
const coarse = matchMedia('(pointer:coarse)').matches;

const $ = id => document.getElementById(id);
const stage = $('stage'), main = $('main'), strip = $('strip');
const gwrap = document.querySelector('#grid .gwrap');
const gridEl = $('grid'), helpEl = $('help'), modalEl = $('modal');

let photos = [];          // [{name, pair, burstStart, rej, el, gel}]
let cur = 0, zoomed = false, showSeq = 0;

/* ---------- full-size cache + prefetch ring ----------
   Mirrors the real pipeline: an LRU of decoded frames plus a ring of
   neighbors loaded in the background while the current frame is on screen. */
const lru = new Map(); // name -> element
async function fullFor(i) {
  const key = photos[i].name;
  if (lru.has(key)) { const el = lru.get(key); lru.delete(key); lru.set(key, el); return el; }
  const el = await backend.full(i);
  lru.set(key, el);
  if (lru.size > 14) lru.delete(lru.keys().next().value);
  return el;
}
let prefetchTimer = 0;
function prefetch(i) {
  clearTimeout(prefetchTimer);
  const ring = [1, -1, 2, 3, -2, 4, 5].map(d => i + d)
    .filter(j => j >= 0 && j < photos.length && !lru.has(photos[j].name));
  const step = () => {
    if (!ring.length) return;
    fullFor(ring.shift()).then(() => { prefetchTimer = setTimeout(step, 30); });
  };
  prefetchTimer = setTimeout(step, 60);
}

/* ---------- viewer ---------- */
async function show(i) {
  if (!photos.length) return;
  cur = Math.max(0, Math.min(photos.length - 1, i));
  const p = photos[cur], my = ++showSeq;
  $('pos').textContent = cur + 1;
  $('fname').textContent = p.name;
  $('pairChip').textContent = p.pair;
  main.classList.toggle('rejected', !!p.rej); stage.classList.toggle('rejected', !!p.rej);
  document.querySelectorAll('.thumb.cur,.gcell.cur').forEach(e => e.classList.remove('cur'));
  p.el.classList.add('cur'); p.gel.classList.add('cur');
  p.el.scrollIntoView({ inline: 'center', block: 'nearest', behavior: 'instant' });
  // Slow readers: admit the frame isn't here yet instead of freezing.
  const slow = setTimeout(() => { if (my === showSeq) stage.classList.add('loading'); }, 180);
  let el;
  try {
    el = await fullFor(cur);
  } catch (e) {
    clearTimeout(slow); stage.classList.remove('loading');
    if (my === showSeq) handleLoadError(p, e);
    return;
  }
  clearTimeout(slow); stage.classList.remove('loading');
  if (my !== showSeq) return;               // a newer frame won the race
  stage.replaceChildren(el);
  setZoom(zoomed);                          // zoom persists across frames: flip a burst at 1:1 to compare focus
  prefetch(cur);
}

async function handleLoadError(p, err) {
  if (backend.folderPresent && !(await backend.folderPresent())) {
    $('gone').classList.remove('hidden');   // card ejected mid-cull
    return;
  }
  toast(`⚠ ${err && err.message ? err.message : 'could not load ' + p.name}`);
}

function refreshRejUI() {
  const n = photos.filter(p => p.rej).length, chip = $('rejChip');
  chip.textContent = n + ' rejected'; chip.classList.toggle('empty', !n);
  $('commitBtn').disabled = !n;
  $('mCommitBtn').disabled = !n; $('mCommitBtn').textContent = n ? `Commit (${n})` : 'Commit';
  const p = photos[cur];
  main.classList.toggle('rejected', !!p.rej); stage.classList.toggle('rejected', !!p.rej);
  $('mRejBtn').classList.toggle('on', !!p.rej);
}
function toggleReject() {
  const p = photos[cur]; p.rej = !p.rej;
  p.el.classList.toggle('rej', p.rej); p.gel.classList.toggle('rej', p.rej);
  refreshRejUI();
  backend.setReject?.(p.id, p.rej); // other screens hear about it via events
}

/* Sync events from other screens (phone ↔ desktop). Everything is
   idempotent: our own actions echo back and are recognized as no-ops. */
function onSyncEvent(e) {
  if (e.type === 'reject') {
    const p = photos.find(q => q.id === e.id);
    if (!p || !!p.rej === !!e.rejected) return;
    p.rej = !!e.rejected;
    p.el.classList.toggle('rej', p.rej); p.gel.classList.toggle('rej', p.rej);
    refreshRejUI();
  } else if (e.type === 'commit') {
    const moved = new Set(e.movedIds || []);
    let removed = 0;
    for (let i = photos.length - 1; i >= 0; i--) if (moved.has(photos[i].id)) {
      photos[i].el.remove(); photos[i].gel.remove(); photos.splice(i, 1); removed++;
    }
    if (!removed) return; // we were the initiator; already applied
    lru.clear();
    $('total').textContent = photos.length;
    show(Math.min(cur, photos.length - 1));
    refreshRejUI();
    toast(`<b>✓</b> ${removed} committed from another screen — ${photos.length} keepers`);
  } else if (e.type === 'open' && backend.refresh) {
    backend.refresh().then(metas => {
      buildPhotos(metas, new Set(backend.serverMarks || []));
      show(0); refreshRejUI();
    });
  }
}

/* ---------- zoom + pan ---------- */
let lastMX = innerWidth / 2, lastMY = innerHeight / 2;
function setZoom(on) {
  zoomed = on; stage.classList.toggle('zoomed', on);
  const c = stage.firstChild;
  if (c && !on) c.style.transform = '';
  if (on) pan(lastMX, lastMY);
}
function pan(mx, my) {
  const c = stage.firstChild; if (!c || !zoomed) return;
  const iw = c.naturalWidth || c.width, ih = c.naturalHeight || c.height;
  const r = stage.getBoundingClientRect();
  const rx = Math.min(1, Math.max(0, (mx - r.left) / r.width));
  const ry = Math.min(1, Math.max(0, (my - r.top) / r.height));
  c.style.transform =
    `translate(${-rx * Math.max(0, iw - r.width)}px,${-ry * Math.max(0, ih - r.height)}px)`;
}
stage.addEventListener('mousemove', e => { lastMX = e.clientX; lastMY = e.clientY; pan(e.clientX, e.clientY); });
stage.addEventListener('click', () => { if (!coarse) setZoom(!zoomed); });

/* ---------- touch: swipe ⟷ navigate, swipe ↑ reject / ↓ un-reject,
               double-tap zoom, drag pans when zoomed ---------- */
let tsX = 0, tsY = 0, tMoved = false, lastTap = 0;
stage.addEventListener('touchstart', e => {
  const t = e.touches[0]; tsX = t.clientX; tsY = t.clientY; tMoved = false;
}, { passive: true });
stage.addEventListener('touchmove', e => {
  e.preventDefault(); const t = e.touches[0];
  if (Math.abs(t.clientX - tsX) > 8 || Math.abs(t.clientY - tsY) > 8) tMoved = true;
  if (zoomed) { lastMX = t.clientX; lastMY = t.clientY; pan(t.clientX, t.clientY); }
}, { passive: false });
stage.addEventListener('touchend', e => {
  const t = e.changedTouches[0], dx = t.clientX - tsX, dy = t.clientY - tsY;
  if (!zoomed && Math.abs(dx) > 56 && Math.abs(dx) > Math.abs(dy) * 1.3) { show(cur + (dx < 0 ? 1 : -1)); return; }
  if (!zoomed && Math.abs(dy) > 56 && Math.abs(dy) > Math.abs(dx) * 1.3) {
    if (dy < 0 && !photos[cur].rej) toggleReject();
    else if (dy > 0 && photos[cur].rej) toggleReject();
    return;
  }
  if (!tMoved) {
    const now = Date.now();
    if (now - lastTap < 320) { setZoom(!zoomed); lastTap = 0; } else lastTap = now;
  }
});

/* ---------- overlays ---------- */
function toggleGrid(on) {
  gridEl.classList.toggle('hidden', !on);
  if (on) photos[cur].gel.scrollIntoView({ block: 'center', behavior: 'instant' });
}
function openCommit() {
  const rej = photos.filter(p => p.rej); if (!rej.length) return;
  if (backend.readOnly) {
    toast('⚠ Card is locked (read-only) — flip the little switch on the card, then reopen');
    return;
  }
  $('modalTitle').textContent = `Move ${rej.length} photo${rej.length > 1 ? 's' : ''} to Trash`;
  $('modalStrip').replaceChildren(...rej.map(p => {
    const im = new Image(); im.src = p.el.querySelector('img').src; return im;
  }));
  modalEl.classList.remove('hidden');
}
async function doCommit() {
  const indices = photos.map((p, i) => p.rej ? i : -1).filter(i => i >= 0);
  if (!indices.length) return;
  let res;
  try {
    res = await backend.commit(indices);
  } catch (e) {
    modalEl.classList.add('hidden');
    handleLoadError(photos[cur], new Error('commit failed — nothing was lost, files are still on the card'));
    return;
  }
  // Remove exactly what the backend says moved; anything that failed
  // stays in the session, still marked, so no photo silently vanishes.
  const moved = new Set(res.movedIds || []);
  for (let i = photos.length - 1; i >= 0; i--) if (moved.has(photos[i].id)) {
    photos[i].el.remove(); photos[i].gel.remove(); photos.splice(i, 1);
  }
  lru.clear(); modalEl.classList.add('hidden');
  $('total').textContent = photos.length;
  await show(Math.min(cur, photos.length - 1));
  refreshRejUI();
  if (res.errors && res.errors.length) {
    toast(`⚠ ${moved.size} moved to ${res.dest || 'Trash'}, ${res.errors.length} file${res.errors.length > 1 ? 's' : ''} failed — still on the card, still marked`);
  } else {
    toast(`<b>✓</b> ${moved.size} pair${moved.size > 1 ? 's' : ''} moved to ${res.dest || 'Trash'} — ${photos.length} keepers`);
  }
}
let toastT = 0;
function toast(html) {
  const t = $('toast'); t.innerHTML = html; t.classList.add('on');
  clearTimeout(toastT); toastT = setTimeout(() => t.classList.remove('on'), 3200);
}

/* ---------- wiring ---------- */
$('prevBtn').onclick = () => show(cur - 1);
$('nextBtn').onclick = () => show(cur + 1);
$('commitBtn').onclick = openCommit;
$('confirmCommit').onclick = doCommit;
$('cancelCommit').onclick = () => modalEl.classList.add('hidden');
$('helpBtn').onclick = () => helpEl.classList.remove('hidden');
$('helpClose').onclick = () => helpEl.classList.add('hidden');
$('mRejBtn').onclick = toggleReject;
$('mCommitBtn').onclick = openCommit;

let keyCount = 0;
document.addEventListener('keydown', e => {
  if (e.key === 'Escape') {
    gridEl.classList.add('hidden'); helpEl.classList.add('hidden');
    modalEl.classList.add('hidden'); $('remoteSheet').classList.add('hidden');
    setZoom(false); return;
  }
  if (!modalEl.classList.contains('hidden')) {
    if (e.key === 'Enter') { doCommit(); e.preventDefault(); } return;
  }
  const k = e.key.toLowerCase();
  if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') { openCommit(); e.preventDefault(); return; }
  if ((e.metaKey || e.ctrlKey) && k === 'o' && backend.canPick) { pickAndOpen(); e.preventDefault(); return; }
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  if (k === 'arrowright' || k === 'arrowdown') show(cur + 1);
  else if (k === 'arrowleft' || k === 'arrowup') show(cur - 1);
  else if (k === 'x') toggleReject();
  else if (k === 'z') setZoom(!zoomed);
  else if (k === 'g') toggleGrid(gridEl.classList.contains('hidden'));
  else if (k === '?') helpEl.classList.toggle('hidden');
  else return;
  e.preventDefault();
  if (++keyCount === 6) $('hintBar').classList.add('gone');
});

/* ---------- building the session ---------- */
function buildPhotos(metas, marked) {
  strip.replaceChildren(); gwrap.replaceChildren(); lru.clear();
  photos = metas.map((m, i) => {
    const t = document.createElement('div'); t.className = 'thumb';
    t.innerHTML = `${m.burstStart && i ? '<span class="bstart"></span>' : ''}` +
      `<img src="${backend.thumbURL(i)}" alt=""><span class="x">✕</span>`;
    t.onclick = () => show(photos.indexOf(p)); strip.appendChild(t);
    const g = document.createElement('div'); g.className = 'gcell';
    g.innerHTML = `<img src="${backend.thumbURL(i)}" alt=""><span class="x">✕</span>` +
      `<span class="nm">${m.name}</span>`;
    g.onclick = () => { show(photos.indexOf(p)); toggleGrid(false); };
    const p = { ...m, rej: !!(marked && marked.has(m.id)), el: t, gel: g };
    if (p.rej) { t.classList.add('rej'); g.classList.add('rej'); }
    gwrap.appendChild(g);
    return p;
  });
  $('pathLabel').textContent = backend.label;
  $('roChip').classList.toggle('hidden', !backend.readOnly);
  $('total').textContent = photos.length;
  $('emptyState').classList.toggle('hidden', photos.length > 0 || !backend.canPick);
  if (!photos.length) {
    $('fname').textContent = 'no photos found';
    $('pairChip').classList.add('hidden');
  } else {
    $('pairChip').classList.remove('hidden');
  }
}

/* Open (or switch to) a card folder — from the header button, the empty
   state, or ⌘O. Cancelling the picker leaves the current session alone. */
async function pickAndOpen() {
  let metas;
  try {
    metas = await backend.open();
  } catch (e) {
    toast('⚠ ' + (e.message || e));
    return;
  }
  if (metas === null) return; // picker cancelled
  buildPhotos(metas, new Set(backend.serverMarks || []));
  if (photos.length) { await show(0); }
  refreshRejUI();
}

$('rescanBtn').onclick = async () => {
  // Card came back: rescan the same folder, keep the user's reject marks.
  const marked = new Set(photos.filter(p => p.rej).map(p => p.id));
  let metas;
  try {
    metas = await (backend.rescan ? backend.rescan() : backend.open());
  } catch (e) {
    toast('⚠ Still can’t reach the folder — is the card mounted?');
    return;
  }
  $('gone').classList.add('hidden');
  buildPhotos(metas, marked);
  marked.forEach(id => backend.setReject?.(id, true)); // re-seed other screens
  await show(Math.min(cur, photos.length - 1));
  refreshRejUI();
};

/* ---------- phone remote session (QR sheet) ---------- */
$('remoteBtn').onclick = async () => {
  try {
    const info = await backend.startRemote();
    $('qrImg').src = info.qr;
    $('remoteUrl').textContent = info.url;
    $('remoteSheet').classList.remove('hidden');
  } catch (e) {
    toast('⚠ Could not start the remote session: ' + (e.message || e));
  }
};
$('remoteClose').onclick = () => $('remoteSheet').classList.add('hidden');
$('remoteStop').onclick = async () => {
  await backend.stopRemote?.();
  $('remoteSheet').classList.add('hidden');
  toast('Remote session stopped');
};

/* ---------- boot ---------- */
(async function init() {
  backend = window.QKWailsBackend
    || (window.QKHttpBackend ? await window.QKHttpBackend.detect() : null)
    || window.QKMockBackend;

  $('pathLabel').textContent = backend.label;
  if (backend.isMock) $('mockChip').classList.remove('hidden');
  if (backend.startRemote) $('remoteBtn').classList.remove('hidden');
  if (backend.canPick) {
    $('openBtn').classList.remove('hidden');
    $('openBtn').onclick = pickAndOpen;
    $('emptyOpen').onclick = pickAndOpen;
  }
  if (backend.isRemote) $('remoteChip').textContent = 'REMOTE · ' + location.hostname;
  if (coarse) $('hintBar').innerHTML =
    '<b>swipe ⟷</b> flip&nbsp;&nbsp;<b>swipe ↑</b> reject&nbsp;&nbsp;<b>double-tap</b> zoom';

  const metas = (await backend.open()) || [];
  buildPhotos(metas, new Set(backend.serverMarks || []));
  backend.onEvent?.(onSyncEvent);
  if (!photos.length) return;
  await show(0);
  refreshRejUI();
  setTimeout(() => $('hintBar').classList.add('gone'), 12000);
})();
