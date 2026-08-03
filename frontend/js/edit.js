/* QK develop mode.
   Culling answers "keep or kill". This answers "now make it look right",
   for someone who has never opened Photoshop and does not want to.

   So there is one button that does the whole job, and the sliders exist to
   argue with it — not to be learned first. Every control is named for what
   it does to the picture rather than what it does to the numbers.

   Rendering happens in Go, because that is where the sensor data is. The
   panel keeps the edit locally, tells the backend, and asks for a new
   frame; mid-drag it asks for a smaller one so the picture keeps up with
   your hand, and settles at full size when you let go. */
'use strict';

(function () {
  const SLIDERS = [
    { key: 'exposure', label: 'Brightness', min: -5, max: 5, step: 0.05, unit: ' EV' },
    { key: 'highlights', label: 'Highlights', min: -100, max: 100, step: 1, hint: 'pull back a blown sky' },
    { key: 'shadows', label: 'Shadows', min: -100, max: 100, step: 1, hint: 'open up the dark parts' },
    { key: 'blacks', label: 'Depth', min: -100, max: 100, step: 1, hint: 'how black the blacks go' },
    { key: 'contrast', label: 'Contrast', min: -100, max: 100, step: 1 },
    { key: 'temp', label: 'Warmth', min: -100, max: 100, step: 1, hint: 'orange ⟷ blue' },
    { key: 'tint', label: 'Tint', min: -100, max: 100, step: 1, hint: 'green ⟷ magenta' },
    { key: 'vibrance', label: 'Colour', min: -100, max: 100, step: 1 },
    { key: 'clarity', label: 'Punch', min: -100, max: 100, step: 1, hint: 'local contrast' },
    { key: 'sharpen', label: 'Sharpness', min: 0, max: 100, step: 1 },
    { key: 'rotate', label: 'Straighten', min: -45, max: 45, step: 0.1, unit: '\u00b0',
      group: 'Lens and framing' },
    { key: 'distortion', label: 'Lens bulge', min: -100, max: 100, step: 1,
      hint: 'bowed edges from the lens' },
    { key: 'vignette', label: 'Corner light', min: -100, max: 100, step: 1,
      hint: 'dark corners from the lens' },
  ];

  const ZERO = Object.fromEntries(SLIDERS.map(s => [s.key, 0]));
  const DRAG_W = 1024; // reduced render while a slider is moving

  // Auto works in fractions of a stop; nobody wants to read them. Show the
  // slider's own resolution and no more.
  function fmt(s, v) {
    const n = s.step < 1 ? v.toFixed(2) : String(Math.round(v));
    return (v > 0 ? '+' : '') + n + (s.unit || '');
  }

  let on = false;          // develop mode active
  let edit = { ...ZERO };  // the edit for the photo on screen
  let info = null;         // what the backend knows about it
  let showingBefore = false;
  let seq = 0;             // guards against an older render landing last
  let holdTimer = 0;
  let panel, sliderEls = {}, valueEls = {};
  // Photos the user has deliberately put back to as-shot, so the automatic
  // first develop below does not undo their decision the moment they
  // arrow away and come back.
  const leftAlone = new Set();

  /* ---------- the backend calls, all over the same /api the viewer uses ---------- */

  const api = {
    async info(id) {
      const r = await fetch('/api/developinfo/' + encodeURIComponent(id));
      if (!r.ok) throw new Error('could not read this photo');
      return r.json();
    },
    async set(id, e, hold) {
      const r = await fetch('/api/edit', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id, edit: e, hold: !!hold }),
      });
      if (!r.ok) throw new Error((await r.text()) || 'could not save the edit');
      return r.json();
    },
    async action(id, action) {
      const r = await fetch('/api/edit', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id, action }),
      });
      if (!r.ok) throw new Error((await r.text()) || action + ' failed');
      return r.json();
    },
    async export(body) {
      const r = await fetch('/api/export', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) throw new Error((await r.text()) || 'export failed');
      return r.json();
    },
  };

  const mock = () => backend && backend.isMock;

  /* ---------- panel ---------- */

  function build() {
    panel = $('editPanel');
    const box = $('epSliders');
    box.replaceChildren();
    SLIDERS.forEach(s => {
      if (s.group) {
        const h = document.createElement('div');
        h.className = 'ep-group';
        h.textContent = s.group;
        box.appendChild(h);
      }
      const row = document.createElement('div');
      row.className = 'ep-row';
      row.innerHTML =
        `<label for="ep-${s.key}">${esc(s.label)}` +
        (s.hint ? `<i>${esc(s.hint)}</i>` : '') +
        `<b id="epv-${s.key}">0</b></label>` +
        `<input type="range" id="ep-${s.key}" min="${s.min}" max="${s.max}" step="${s.step}" value="0">`;
      box.appendChild(row);
    });
    for (const s of SLIDERS) {
      const el = $('ep-' + s.key);
      sliderEls[s.key] = el;
      valueEls[s.key] = $('epv-' + s.key);
      el.addEventListener('input', () => onSlider(s, +el.value, true));
      el.addEventListener('change', () => onSlider(s, +el.value, false));
      // Double-click a slider to put just that one back where it started.
      el.addEventListener('dblclick', () => { el.value = 0; onSlider(s, 0, false); });
    }

    $('epAuto').onclick = auto;
    $('epReset').onclick = reset;
    $('epClose').onclick = () => toggle(false);
    $('epExport').onclick = () => exportOne();
    $('epExportAll').onclick = () => exportAll();
    $('epCopy').onclick = copy;

    // Before/after is a press-and-hold, so a comparison costs one gesture.
    const before = $('epBefore');
    const down = ev => { ev.preventDefault(); setBefore(true); };
    const up = () => setBefore(false);
    before.addEventListener('mousedown', down);
    before.addEventListener('touchstart', down, { passive: false });
    ['mouseup', 'mouseleave', 'touchend', 'touchcancel'].forEach(t =>
      before.addEventListener(t, up));

    if (backend.canCopy) $('epCopy').classList.remove('hidden');

    $('cropBtn').onclick = () => QKCrop.toggle();
    // The crop tool owns the rectangle while you drag it and hands it back
    // when you let go; reframing is the only edit that does not come from
    // a slider.
    QKCrop.build((r, modeChanged) => {
      if (!photos.length) return;
      if (!modeChanged) {
        const whole = r.w >= 0.999 && r.h >= 0.999;
        edit.cropX = whole ? 0 : r.x;
        edit.cropY = whole ? 0 : r.y;
        edit.cropW = whole ? 0 : r.w;
        edit.cropH = whole ? 0 : r.h;
      }
      pushEdit(photos[cur], false);
    });
  }

  // pushEdit saves the current edit and repaints. Everything that changes
  // the picture goes through here, so there is one place that knows the
  // difference between a value still moving and one that has settled.
  async function pushEdit(p, dragging) {
    if (mock()) { repaint(); return; }
    try {
      info = await api.set(p.id, edit, dragging);
    } catch (err) {
      toast('\u26a0 ' + err.message);
      return;
    }
    if (!dragging) markEdited(p, info.edited);
    await repaint(dragging ? DRAG_W : 0);
  }

  function syncSliders() {
    for (const s of SLIDERS) {
      const v = +edit[s.key] || 0;
      sliderEls[s.key].value = v;
      valueEls[s.key].textContent = fmt(s, v);
      sliderEls[s.key].parentElement.classList.toggle('set', v !== 0);
    }
    $('epReset').disabled = SLIDERS.every(s => !edit[s.key]);
  }

  function describe() {
    const tag = $('epSource'), note = $('epNote');
    if (mock()) {
      tag.textContent = 'MOCK';
      tag.className = 'ep-source approx';
      note.textContent = 'The mock shoot fakes developing with a screen filter. ' +
        'Open a real card to edit sensor data.';
      return;
    }
    if (!info) { tag.textContent = ''; note.textContent = ''; return; }
    if (info.source === 'raw') {
      tag.textContent = 'RAW';
      tag.className = 'ep-source raw';
      const stops = info.headroom >= 0.15
        ? `${info.headroom.toFixed(1)} stops of highlight still recoverable`
        : 'highlights already at the sensor\u2019s limit';
      note.textContent = `${info.camera || 'Sensor data'} · ${stops}.`;
      if (info.edited) {
        note.textContent += ' Developed — hold compare to see it as shot.';
      }
      if (info.approxColor) {
        note.textContent += ' Colour profile is a generic one for this make — ' +
          'colours are close, not exact.';
      }
      if (info.lens) {
        note.textContent += ` ${info.lens}` +
          (info.lensLearned ? ', corrections remembered from last time.' : '.');
      }
    } else {
      tag.textContent = 'JPEG';
      tag.className = 'ep-source approx';
      note.textContent = 'Editing the camera’s own JPEG: no highlight recovery, ' +
        'and big colour shifts will show. QK could not decode this file’s RAW.';
    }
  }

  /* ---------- rendering ---------- */

  // frame returns the picture for photo i as develop mode wants it: the
  // developed version, or — in the mock shoot — the plain one with a
  // screen filter standing in.
  async function frame(i) {
    const p = photos[i];
    if (mock()) {
      const im = await backend.full(i);
      im.style.filter = cssApprox(edit);
      return im;
    }
    info = await api.info(p.id);
    // Sensor data with nothing but a transfer curve on it looks flat and
    // dark — technically honest, and not what anyone opened an editor to
    // see. So a photo that has never been developed gets developed now.
    // Every slider shows what happened, Reset undoes all of it, and
    // holding compare shows what it looked like before.
    if (!info.edited && !leftAlone.has(p.id)) {
      try { info = await api.action(p.id, 'auto'); } catch (err) { /* as shot, then */ }
      markEdited(p, info.edited);
    }
    edit = { ...ZERO, ...info.edit };
    syncSliders();
    describe();
    QKCrop.adopt(edit, info.width, info.height);
    return loadFrame(p.id, 0);
  }

  function loadFrame(id, maxDim) {
    const q = new URLSearchParams();
    if (maxDim) q.set('w', maxDim);
    if (showingBefore) q.set('original', '1');
    if (QKCrop.active()) q.set('uncropped', '1');
    // Named 'v', not 't': the LAN remote server reserves ?t= for its
    // session token and turns away anything else that uses it.
    q.set('v', (info && info.tag) || '0');
    const im = new Image();
    im.src = '/api/develop/' + encodeURIComponent(id) + '?' + q.toString();
    return new Promise((resolve, reject) => {
      im.onload = () => resolve(im);
      im.onerror = () => reject(new Error('could not develop this photo'));
    });
  }

  // repaint swaps the stage image without disturbing anything else, so a
  // slider drag does not flash or lose the zoom.
  async function repaint(maxDim) {
    if (!on || !photos.length) return;
    const p = photos[cur], my = ++seq;
    if (mock()) {
      const c = stage.firstChild;
      if (c) c.style.filter = cssApprox(showingBefore ? ZERO : edit);
      return;
    }
    let im;
    try {
      im = await loadFrame(p.id, maxDim);
    } catch (err) {
      if (my === seq) toast('⚠ ' + err.message);
      return;
    }
    if (my !== seq || !on || photos[cur] !== p) return;
    stage.replaceChildren(im);
    setZoom(zoomed);
    QKCrop.relayout();
  }

  // cssApprox is the mock shoot's stand-in: roughly the right direction,
  // definitely not the real pipeline.
  function cssApprox(e) {
    return `brightness(${Math.pow(2, e.exposure || 0).toFixed(3)}) ` +
      `contrast(${(1 + (e.contrast || 0) / 220).toFixed(3)}) ` +
      `saturate(${(1 + (e.vibrance || 0) / 140).toFixed(3)}) ` +
      `sepia(${Math.max(0, (e.temp || 0) / 260).toFixed(3)})`;
  }

  /* ---------- actions ---------- */

  function onSlider(s, v, dragging) {
    edit[s.key] = v;
    valueEls[s.key].textContent = fmt(s, v);
    sliderEls[s.key].parentElement.classList.toggle('set', v !== 0);
    $('epReset').disabled = SLIDERS.every(x => !edit[x.key]);
    showingBefore = false;
    if (mock()) { repaint(); return; }

    const p = photos[cur];
    clearTimeout(holdTimer);
    // Mid-drag, coalesce: no point rendering a value your hand has
    // already left behind.
    if (dragging) holdTimer = setTimeout(() => pushEdit(p, true), 45);
    else pushEdit(p, false);
  }

  async function auto() {
    if (!photos.length) return;
    leftAlone.delete(photos[cur].id);
    if (mock()) {
      edit = { ...ZERO, exposure: 0.4, contrast: 18, vibrance: 22 };
      syncSliders(); repaint(); return;
    }
    const p = photos[cur], btn = $('epAuto');
    btn.disabled = true; btn.classList.add('busy');
    try {
      info = await api.action(p.id, 'auto');
      edit = { ...ZERO, ...info.edit };
      syncSliders();
      markEdited(p, info.edited);
      showingBefore = false;
      await repaint(0);
      toast('<b>✨</b> Auto-developed — nudge anything you disagree with');
    } catch (err) {
      toast('⚠ ' + err.message);
    } finally {
      btn.disabled = false; btn.classList.remove('busy');
    }
  }

  async function reset() {
    if (!photos.length) return;
    leftAlone.add(photos[cur].id);
    edit = { ...ZERO };
    syncSliders();
    QKCrop.adopt(edit, 0, 0);
    showingBefore = false;
    if (mock()) { repaint(); return; }
    const p = photos[cur];
    try {
      info = await api.action(p.id, 'reset');
      markEdited(p, false);
    } catch (err) {
      toast('⚠ ' + err.message);
      return;
    }
    repaint(0);
  }

  function setBefore(want) {
    if (showingBefore === want) return;
    showingBefore = want;
    $('epBefore').classList.toggle('on', want);
    repaint(0);
  }

  async function exportOne() {
    if (!photos.length) return;
    const p = photos[cur];
    toast(`Developing ${esc(p.name)} at full size…`);
    try {
      const dest = backend.pickExportFolder ? (await backend.pickExportFolder()) || '' : '';
      const res = await api.export({ id: p.id, dest });
      offerReveal(`<b>✓</b> Exported ${esc(p.name)}`, res.path || res.dir);
    } catch (err) {
      toast('⚠ ' + err.message);
    }
  }

  async function exportAll() {
    const keepers = photos.filter(p => !p.rej);
    if (!keepers.length) { toast('Nothing to export — every photo is marked as a reject'); return; }
    try {
      const dest = backend.pickExportFolder ? (await backend.pickExportFolder()) || '' : '';
      const res = await api.export({ ids: keepers.map(p => p.id), dest });
      toast(`Developing ${res.count} photo${res.count > 1 ? 's' : ''}…`);
    } catch (err) {
      toast('⚠ ' + err.message);
    }
  }

  async function copy() {
    if (!photos.length || !backend.copyPhoto) return;
    toast('Developing at full size…');
    try {
      await backend.copyPhoto(photos[cur].id);
      toast('<b>✓</b> Copied — paste it anywhere');
    } catch (err) {
      toast('⚠ Could not copy: ' + (err.message || err));
    }
  }

  function offerReveal(msg, path) {
    if (backend.reveal && path) {
      toast(`${msg} — <a href="#" id="revealLink">show me</a>`);
      const link = document.getElementById('revealLink');
      if (link) link.onclick = ev => { ev.preventDefault(); backend.reveal(path); };
    } else {
      toast(`${msg} → ${esc(path || '')}`);
    }
  }

  /* ---------- edited badges ---------- */

  function markEdited(p, yes) {
    p.edited = !!yes;
    p.el.classList.toggle('edited', !!yes);
    p.gel.classList.toggle('edited', !!yes);
  }

  /* ---------- mode ---------- */

  async function toggle(want) {
    const next = want === undefined ? !on : !!want;
    if (next === on) return;
    on = next;
    if (!on) QKCrop.toggle(false);
    panel.classList.toggle('hidden', !on);
    document.body.classList.toggle('editing', on);
    showingBefore = false;
    $('epBefore').classList.remove('on');
    if (!photos.length) return;
    // Coming back out, the stage goes back to the camera preview — which
    // is instant, and is what culling wants to see.
    await show(cur);
  }

  function key(e) {
    if (e.metaKey || e.ctrlKey) {
      const k = e.key.toLowerCase();
      if (k === 'e') { on ? (e.shiftKey ? exportAll() : exportOne()) : toggle(true); return true; }
      if (k === 'c' && on && backend.canCopy) { copy(); return true; }
      return false;
    }
    if (e.altKey) return false;
    if (e.key === 'Escape' && on) {
      if (QKCrop.active()) { QKCrop.toggle(false); return true; }
      toggle(false);
      return true;
    }
    const k = e.key.toLowerCase();
    if (k === 'e') { toggle(); return true; }
    if (!on) return false;
    if (k === 'a') { auto(); return true; }
    if (k === 'c') { QKCrop.toggle(); return true; }
    if (k === 'r') { reset(); return true; }
    if (e.key === '\\') { setBefore(!showingBefore); return true; }
    return false;
  }

  window.QKEdit = {
    active: () => on,
    frame,
    toggle,
    key,
    markEdited,
    boot() {
      build();
      const marks = new Set(backend.serverEdits || []);
      photos.forEach(p => { if (marks.has(p.id)) markEdited(p, true); });
      if (mock()) describe();
    },
    // A sync event from another screen: adopt the edit if we are looking
    // at that photo, and mark the thumbnail either way.
    onRemoteEdit(ev) {
      const p = photos.find(q => q.id === ev.id);
      if (!p) return;
      const zero = !ev.edit || Object.values(ev.edit).every(v => !v);
      markEdited(p, !zero);
      if (!on || photos[cur] !== p) return;
      edit = { ...ZERO, ...(ev.edit || {}) };
      if (info) info.tag = ev.tag;
      syncSliders();
      repaint(0);
    },
    onExportProgress(ev) {
      if (ev.finished) {
        const failed = ev.failed
          ? `, ${ev.failed} failed`
          : '';
        offerReveal(`<b>✓</b> Exported ${ev.done - (ev.failed || 0)} photo${ev.done > 1 ? 's' : ''}${failed}`, ev.dest);
      } else if (ev.total > 1) {
        toast(`Developing ${ev.done + 1} of ${ev.total} — ${esc(ev.name || '')}`);
      }
    },
  };
})();
