/* The crop rectangle.
   Composition is the part of a photograph no slider reaches, and the part
   that most separates a snapshot from something worth selling. So this is
   direct: you drag the frame you want, on the picture, at full size.

   While cropping, the stage shows the *uncropped* frame — you cannot
   choose where to cut if you can only see what survived the last cut. The
   rectangle lives entirely in the browser during a drag and is only sent
   to the backend when you let go, so dragging costs nothing. */
'use strict';

(function () {
  const HANDLES = ['nw', 'n', 'ne', 'e', 'se', 's', 'sw', 'w'];
  const MIN = 0.04; // never crop to a sliver by accident

  // Ratios worth having as one click. These are delivery formats — where
  // the photo is going, rather than what it is.
  const ASPECTS = [
    { label: 'Free', v: 0 },
    { label: 'Original', v: -1 },
    { label: '1:1', v: 1, hint: 'square' },
    { label: '4:5', v: 4 / 5, hint: 'portrait feed' },
    { label: '3:2', v: 3 / 2, hint: 'print' },
    { label: '16:9', v: 16 / 9, hint: 'wide' },
  ];

  let on = false;
  let rect = { x: 0, y: 0, w: 1, h: 1 };
  let frameW = 0, frameH = 0; // the corrected frame, in pixels
  let aspect = 0;             // 0 free, else width over height in pixels
  let aspectLabel = '';       // what the user actually clicked
  let wrap, box, drag = null;
  let commit = () => {};

  /* ---------- geometry ---------- */

  // imageRect is where the photo actually sits inside the stage: the image
  // is letterboxed, so the overlay cannot just cover the stage.
  function imageRect() {
    const img = stage.firstChild;
    if (!img) return null;
    const ir = img.getBoundingClientRect();
    const sr = stage.getBoundingClientRect();
    if (!ir.width || !ir.height) return null;
    return { left: ir.left - sr.left, top: ir.top - sr.top, width: ir.width, height: ir.height };
  }

  function layout() {
    if (!wrap) return;
    const r = imageRect();
    if (!on || !r) { wrap.classList.add('hidden'); return; }
    wrap.classList.remove('hidden');
    // The overlay covers the stage and nothing else: its shading spreads
    // far enough to darken the whole window otherwise, panel included.
    const sr = stage.getBoundingClientRect(), mr = main.getBoundingClientRect();
    wrap.style.left = (sr.left - mr.left) + 'px';
    wrap.style.top = (sr.top - mr.top) + 'px';
    wrap.style.width = sr.width + 'px';
    wrap.style.height = sr.height + 'px';
    box.style.left = (r.left + rect.x * r.width) + 'px';
    box.style.top = (r.top + rect.y * r.height) + 'px';
    box.style.width = (rect.w * r.width) + 'px';
    box.style.height = (rect.h * r.height) + 'px';
    if (frameW && frameH) {
      // When a ratio is locked, say the one that was chosen: on a 3:2
      // sensor, "3:2" and "original" are the same shape and only one of
      // them is the answer to what you just clicked.
      box.dataset.size = aspect
        ? aspectLabel
        : ratioName(Math.round(rect.w * frameW), Math.round(rect.h * frameH));
    }
  }

  // ratioName reduces the crop to the simplest ratio people recognise, and
  // falls back to the raw numbers when it is not a familiar one.
  function ratioName(w, h) {
    if (!w || !h) return '';
    for (const a of ASPECTS) {
      const v = a.v === -1 ? frameW / frameH : a.v;
      if (v > 0 && Math.abs(w / h - v) < 0.01) return a.label === 'Original' ? 'original' : a.label;
    }
    return `${(w / h).toFixed(2)}:1`;
  }

  // withAspect forces a rectangle to the locked ratio, growing or shrinking
  // whichever side the handle being dragged is not controlling.
  function withAspect(r, anchor) {
    const target = aspect === -1 ? frameW / frameH : aspect;
    if (!target || !frameW || !frameH) return r;
    // Ratios are in pixels; the rectangle is normalised, so the frame's own
    // shape has to come out of the conversion.
    const norm = target * frameH / frameW;
    const drivenByHeight = anchor === 'n' || anchor === 's';
    if (drivenByHeight) {
      r.w = r.h * norm;
    } else {
      r.h = r.w / norm;
    }
    return r;
  }

  function clampRect(r, anchor) {
    r.w = Math.max(MIN, Math.min(1, r.w));
    r.h = Math.max(MIN, Math.min(1, r.h));
    if (aspect) withAspect(r, anchor);
    // Keep it on the picture. Whichever side had to be clipped is now
    // fixed, so the *other* one is the one that has to follow the ratio —
    // recomputing the side just clamped would undo the clamp.
    if (r.w > 1) { r.w = 1; if (aspect) withAspect(r, 'e'); }
    if (r.h > 1) { r.h = 1; if (aspect) withAspect(r, 'n'); }
    r.x = Math.max(0, Math.min(1 - r.w, r.x));
    r.y = Math.max(0, Math.min(1 - r.h, r.y));
    return r;
  }

  /* ---------- dragging ---------- */

  function onDown(ev) {
    const r = imageRect();
    if (!on || !r) return;
    const handle = ev.target.dataset ? ev.target.dataset.h : null;
    if (!handle && ev.target !== box) return;
    ev.preventDefault();
    ev.stopPropagation();
    drag = { handle, x: ev.clientX, y: ev.clientY, start: { ...rect }, r };
    wrap.setPointerCapture(ev.pointerId);
  }

  function onMove(ev) {
    if (!drag) return;
    ev.preventDefault();
    const dx = (ev.clientX - drag.x) / drag.r.width;
    const dy = (ev.clientY - drag.y) / drag.r.height;
    const s = drag.start;
    let r;
    if (!drag.handle) {
      r = { x: s.x + dx, y: s.y + dy, w: s.w, h: s.h };
      r.x = Math.max(0, Math.min(1 - r.w, r.x));
      r.y = Math.max(0, Math.min(1 - r.h, r.y));
      rect = r;
      layout();
      return;
    }
    // Each handle moves the edges it names and leaves the others alone.
    let x0 = s.x, y0 = s.y, x1 = s.x + s.w, y1 = s.y + s.h;
    if (drag.handle.includes('w')) x0 = Math.min(x1 - MIN, s.x + dx);
    if (drag.handle.includes('e')) x1 = Math.max(x0 + MIN, s.x + s.w + dx);
    if (drag.handle.includes('n')) y0 = Math.min(y1 - MIN, s.y + dy);
    if (drag.handle.includes('s')) y1 = Math.max(y0 + MIN, s.y + s.h + dy);
    r = clampRect({ x: x0, y: y0, w: x1 - x0, h: y1 - y0 }, drag.handle);
    rect = r;
    layout();
  }

  function onUp(ev) {
    if (!drag) return;
    try { wrap.releasePointerCapture(ev.pointerId); } catch (e) { /* already gone */ }
    drag = null;
    commit(rect);
  }

  /* ---------- panel controls ---------- */

  function buildAspectRow() {
    const row = $('cropAspects');
    row.replaceChildren(...ASPECTS.map(a => {
      const b = document.createElement('button');
      b.textContent = a.label;
      if (a.hint) b.title = a.hint;
      b.onclick = () => setAspect(a.v, b);
      return b;
    }));
    row.firstChild.classList.add('on');
  }

  function setAspect(v, btn) {
    aspect = v;
    aspectLabel = btn.textContent === 'Original' ? 'original' : btn.textContent;
    for (const b of $('cropAspects').children) b.classList.toggle('on', b === btn);
    if (!v) return;
    // Grow the rectangle back out to the largest one of this shape that
    // still fits, keeping it where the eye already is.
    const cx = rect.x + rect.w / 2, cy = rect.y + rect.h / 2;
    let r = clampRect({ x: rect.x, y: rect.y, w: 1, h: 1 }, 'e');
    r.x = Math.max(0, Math.min(1 - r.w, cx - r.w / 2));
    r.y = Math.max(0, Math.min(1 - r.h, cy - r.h / 2));
    rect = r;
    layout();
    commit(rect);
  }

  function reset() {
    rect = { x: 0, y: 0, w: 1, h: 1 };
    aspect = 0;
    aspectLabel = '';
    for (const b of $('cropAspects').children) b.classList.remove('on');
    $('cropAspects').firstChild.classList.add('on');
    layout();
    commit(rect);
  }

  /* ---------- wiring ---------- */

  window.QKCrop = {
    active: () => on,

    build(onCommit) {
      commit = onCommit;
      wrap = document.createElement('div');
      wrap.id = 'cropOverlay';
      wrap.className = 'hidden';
      box = document.createElement('div');
      box.className = 'crop-box';
      box.innerHTML = HANDLES.map(h =>
        `<span class="crop-h crop-${h}" data-h="${h}"></span>`).join('');
      wrap.appendChild(box);
      // The overlay lives beside the stage rather than inside it: the
      // stage's children are replaced on every render.
      main.appendChild(wrap);
      wrap.addEventListener('pointerdown', onDown);
      wrap.addEventListener('pointermove', onMove);
      wrap.addEventListener('pointerup', onUp);
      wrap.addEventListener('pointercancel', onUp);
      addEventListener('resize', layout);
      buildAspectRow();
      $('cropReset').onclick = reset;
      $('cropDone').onclick = () => this.toggle(false);
    },

    // adopt takes the rectangle from an edit, and the shape of the frame it
    // is measured against.
    adopt(edit, w, h) {
      frameW = w || frameW;
      frameH = h || frameH;
      rect = (edit && edit.cropW > 0 && edit.cropH > 0)
        ? { x: edit.cropX, y: edit.cropY, w: edit.cropW, h: edit.cropH }
        : { x: 0, y: 0, w: 1, h: 1 };
      layout();
    },

    rect: () => ({ ...rect }),
    relayout: layout,

    toggle(want) {
      const next = want === undefined ? !on : !!want;
      if (next === on) return;
      on = next;
      document.body.classList.toggle('cropping', on);
      $('cropTools').classList.toggle('hidden', !on);
      $('cropBtn').classList.toggle('on', on);
      layout();
      // The frame behind the rectangle changes: uncropped while choosing,
      // cropped once you are done.
      commit(rect, true);
    },
  };
})();
