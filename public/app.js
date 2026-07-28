// qk frontend — the whole point is that → feels instant.

const $ = (s) => document.querySelector(s);
const photoEl = $('#photo');
const videoEl = $('#video');
const filmstrip = $('#filmstrip');

let items = [];
let cur = 0;
const PREFETCH_AHEAD = 8;
const PREFETCH_BEHIND = 2;
const prefetched = new Map(); // id -> Image (keeps decoded copies alive)
let exifVisible = false;
let zoomed = false;

// ---------- data ----------
async function api(path, body) {
  const res = await fetch(path, body
    ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
    : undefined);
  return res.json();
}

async function load() {
  const data = await api('/api/list');
  items = data.items;
  document.title = `qk — ${items.length} items`;
  buildFilmstrip();
  // Start on the first non-rejected item.
  cur = Math.max(0, items.findIndex(it => !it.rejected));
  show(cur);
}

// ---------- display ----------
function previewUrl(it, size) {
  return `/preview/${it.id}${size ? `?size=${size}` : ''}`;
}

function show(i) {
  if (!items.length) { $('#filename').textContent = 'no photos found'; return; }
  cur = Math.max(0, Math.min(i, items.length - 1));
  const it = items[cur];
  resetZoom();

  if (it.kind === 'video') {
    photoEl.style.display = 'none';
    videoEl.style.display = '';
    videoEl.src = `/file/${it.id}`;
    videoEl.play().catch(() => {});
  } else {
    videoEl.pause();
    videoEl.removeAttribute('src');
    videoEl.style.display = 'none';
    photoEl.style.display = '';
    const pre = prefetched.get(it.id);
    photoEl.src = pre ? pre.src : previewUrl(it, 'fit');
  }

  $('#counter').textContent = `${cur + 1} / ${items.length}`;
  $('#filename').textContent = it.name + (it.hasRaw && it.kind !== 'raw' ? '  (RAW+JPG)' : it.kind === 'raw' ? '  (RAW)' : '');
  $('#rejected-overlay').classList.toggle('hidden', !it.rejected);
  updateStats();
  updateFilmstrip();
  if (exifVisible) showExif();
  prefetch();
}

function updateStats() {
  const rejected = items.filter(it => it.rejected);
  const bytes = rejected.reduce((a, it) => a + it.size, 0);
  $('#stats').innerHTML = rejected.length
    ? `<span class="rejected-count">${rejected.length} rejected</span> · ${fmtBytes(bytes)} to free`
    : `${items.length} items`;
}

function fmtBytes(b) {
  if (b > 1e9) return (b / 1e9).toFixed(1) + ' GB';
  if (b > 1e6) return (b / 1e6).toFixed(1) + ' MB';
  return Math.round(b / 1e3) + ' KB';
}

function prefetch() {
  for (let d = -PREFETCH_BEHIND; d <= PREFETCH_AHEAD; d++) {
    const it = items[cur + d];
    if (!it || it.kind === 'video' || prefetched.has(it.id)) continue;
    const img = new Image();
    img.src = previewUrl(it, 'fit');
    prefetched.set(it.id, img);
  }
  // Bound memory: drop entries far from the cursor.
  if (prefetched.size > 40) {
    const keep = new Set();
    for (let d = -10; d <= 20; d++) if (items[cur + d]) keep.add(items[cur + d].id);
    for (const id of prefetched.keys()) if (!keep.has(id)) prefetched.delete(id);
  }
}

// ---------- filmstrip ----------
function buildFilmstrip() {
  filmstrip.innerHTML = '';
  items.forEach((it, i) => {
    const div = document.createElement('div');
    div.className = 'thumb' + (it.rejected ? ' rejected' : '');
    div.dataset.i = i;
    if (it.kind === 'video') {
      div.classList.add('video-thumb');
      div.textContent = '🎬';
    } else {
      const img = document.createElement('img');
      img.loading = 'lazy';
      img.src = previewUrl(it, 'thumb');
      div.appendChild(img);
    }
    if (it.hasRaw) {
      const b = document.createElement('span');
      b.className = 'badge';
      b.textContent = 'RAW';
      div.appendChild(b);
    }
    div.onclick = () => show(i);
    filmstrip.appendChild(div);
  });
}

function updateFilmstrip() {
  filmstrip.querySelectorAll('.thumb').forEach((el) => {
    const i = +el.dataset.i;
    el.classList.toggle('current', i === cur);
    el.classList.toggle('rejected', items[i].rejected);
  });
  const el = filmstrip.children[cur];
  if (el) el.scrollIntoView({ block: 'nearest', inline: 'center', behavior: 'auto' });
}

// ---------- actions ----------
function flash(kind) {
  const f = $('#flash');
  f.className = '';
  void f.offsetWidth; // restart animation
  f.className = kind;
}

async function reject() {
  const it = items[cur];
  if (!it || it.rejected) { next(); return; }
  it.rejected = true; // optimistic — UI never waits on disk
  flash('reject');
  updateStats();
  updateFilmstrip();
  api('/api/reject', { id: it.id });
  next();
}

async function undo() {
  const r = await api('/api/undo', {});
  if (!r.ok) return;
  const i = items.findIndex(it => it.id === r.id);
  if (i >= 0) { items[i].rejected = false; show(i); }
}

async function restoreCurrent() {
  const it = items[cur];
  if (!it.rejected) return;
  it.rejected = false;
  api('/api/restore', { id: it.id });
  show(cur);
}

function keep() { flash('keep'); next(); }
function next() { if (cur < items.length - 1) show(cur + 1); else updateFilmstrip(); }
function prev() { if (cur > 0) show(cur - 1); }

function jumpUnreviewed(dir) {
  for (let i = cur + dir; i >= 0 && i < items.length; i += dir) {
    if (!items[i].rejected) { show(i); return; }
  }
}

// ---------- zoom ----------
function resetZoom() {
  zoomed = false;
  photoEl.classList.remove('zoomed');
  photoEl.style.transform = '';
  photoEl.style.left = '';
  photoEl.style.top = '';
}

function toggleZoom(ev) {
  const it = items[cur];
  if (!it || it.kind === 'video') return;
  if (zoomed) { resetZoom(); return; }
  zoomed = true;
  photoEl.classList.add('zoomed');
  panTo(ev);
}

function panTo(ev) {
  if (!zoomed) return;
  const stage = $('#stage').getBoundingClientRect();
  const fx = ev ? (ev.clientX - stage.left) / stage.width : 0.5;
  const fy = ev ? (ev.clientY - stage.top) / stage.height : 0.5;
  const ox = Math.max(0, photoEl.naturalWidth - stage.width);
  const oy = Math.max(0, photoEl.naturalHeight - stage.height);
  photoEl.style.left = `${-ox * fx}px`;
  photoEl.style.top = `${-oy * fy}px`;
}

photoEl.addEventListener('click', toggleZoom);
$('#stage').addEventListener('mousemove', (ev) => panTo(ev));

// ---------- EXIF ----------
async function showExif() {
  const it = items[cur];
  const panel = $('#exif-panel');
  panel.classList.remove('hidden');
  panel.innerHTML = '<span class="dim">loading…</span>';
  const id = it.id;
  const t = await api(`/api/exif/${id}`);
  if (items[cur].id !== id) return; // moved on already
  panel.innerHTML = [
    t.camera && `<b>${t.camera}</b>`,
    t.lens && `<span class="dim">${t.lens}</span>`,
    [t.shutter, t.aperture, t.iso, t.focal].filter(Boolean).join(' · '),
    [t.dims, t.date].filter(Boolean).map(s => `<span class="dim">${s}</span>`).join(' · '),
  ].filter(Boolean).join('<br>');
}

// ---------- commit ----------
$('#commit-btn').onclick = openCommit;
function openCommit() {
  const rejected = items.filter(it => it.rejected);
  const bytes = rejected.reduce((a, it) => a + it.size, 0);
  const keepCount = items.length - rejected.length;
  $('#commit-summary').textContent = rejected.length
    ? `Keeping ${keepCount}, rejecting ${rejected.length} (${fmtBytes(bytes)}). Rejected files are sitting in _rejected/ on the card — delete them permanently?`
    : `Nothing rejected yet. The _rejected/ folder is empty.`;
  $('#commit-modal').classList.remove('hidden');
}
$('#commit-cancel').onclick = () => $('#commit-modal').classList.add('hidden');
$('#commit-restore').onclick = async () => {
  for (const it of items) {
    if (it.rejected) { await api('/api/restore', { id: it.id }); it.rejected = false; }
  }
  $('#commit-modal').classList.add('hidden');
  show(cur);
};
$('#commit-delete').onclick = async () => {
  const r = await api('/api/empty-rejected', {});
  $('#commit-modal').classList.add('hidden');
  alert(`Deleted ${r.deleted} files, freed ${fmtBytes(r.freed)}.`);
  load();
};

// ---------- keys ----------
document.addEventListener('keydown', (e) => {
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  if (!$('#commit-modal').classList.contains('hidden')) {
    if (e.key === 'Escape') $('#commit-modal').classList.add('hidden');
    return;
  }
  switch (e.key) {
    case 'ArrowRight': next(); break;
    case 'ArrowLeft': prev(); break;
    case ' ': e.preventDefault(); keep(); break;
    case 'x': case 'X': case 'd': case 'D': case 'Delete': case 'Backspace': e.preventDefault(); reject(); break;
    case 'u': case 'U': items[cur]?.rejected ? restoreCurrent() : undo(); break;
    case 'z': case 'Z': toggleZoom(); break;
    case 'i': case 'I':
      exifVisible = !exifVisible;
      exifVisible ? showExif() : $('#exif-panel').classList.add('hidden');
      break;
    case 'j': case 'J': jumpUnreviewed(1); break;
    case 'k': case 'K': jumpUnreviewed(-1); break;
    case 'Home': show(0); break;
    case 'End': show(items.length - 1); break;
    case '?': $('#help').classList.toggle('hidden'); break;
    case 'Escape': $('#help').classList.add('hidden'); resetZoom(); break;
  }
});

load();
