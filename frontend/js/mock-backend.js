/* Mock backend: a deterministic fake shoot rendered on canvas, so the UI can be
   developed and demoed in a plain browser with no Go process behind it.
   The real Wails backend (milestone 2) registers window.QKWailsBackend with the
   same contract; app.js prefers it when present. */
'use strict';

window.QKMockBackend = (function () {
  const W = 1620, H = 1080, TW = 240, TH = 160;

  let s = 20260728;
  function seed() { // mulberry32 — deterministic shoot on every load
    s |= 0; s = s + 0x6D2B79F5 | 0;
    let t = Math.imul(s ^ s >>> 15, 1 | s);
    t = t + Math.imul(t ^ t >>> 7, 61 | t) ^ t;
    return ((t ^ t >>> 14) >>> 0) / 4294967296;
  }

  const PALETTES = [ // dusk skies, one per burst
    { top: '#2a2f52', mid: '#7a5578', low: '#e08a5a', sun: '#ffd9a0' },
    { top: '#1d2b3a', mid: '#3e5a6e', low: '#c9a06a', sun: '#ffe9c4' },
    { top: '#33244d', mid: '#8a4a63', low: '#d4694f', sun: '#ffc890' },
    { top: '#16222e', mid: '#2f4a5c', low: '#88a3ad', sun: '#f4f0e0' },
    { top: '#3a2340', mid: '#94506a', low: '#e8975f', sun: '#ffe2ae' },
  ];

  const shots = [];
  {
    let fileNo = 4810;
    for (let b = 0; b < PALETTES.length; b++) {
      const n = 14 + Math.floor(seed() * 6);          // frames in this burst
      const softAt = 2 + Math.floor(seed() * (n - 5)); // a run of soft frames mid-burst
      const softLen = 2 + Math.floor(seed() * 3);
      for (let i = 0; i < n; i++) {
        shots.push({
          name: 'DSC0' + (fileNo++), burst: b, first: i === 0,
          t: i / (n - 1), flap: i * 1.15 + seed() * .4,
          jx: (seed() - .5) * .03, jy: (seed() - .5) * .04,
          exp: 1 + (seed() - .5) * .12,
          soft: (i >= softAt && i < softAt + softLen) ? (2.5 + seed() * 2.5) : 0,
        });
      }
    }
  }

  /* one shared grain tile so 1:1 zoom looks photographic */
  const grain = (() => {
    const c = document.createElement('canvas'); c.width = c.height = 256;
    const g = c.getContext('2d'), d = g.createImageData(256, 256);
    for (let i = 0; i < d.data.length; i += 4) {
      const v = 110 + seed() * 70;
      d.data[i] = d.data[i + 1] = d.data[i + 2] = v; d.data[i + 3] = 255;
    }
    g.putImageData(d, 0, 0); return c;
  })();

  function drawScene(ctx, w, h, p) {
    const pal = PALETTES[p.burst];
    if (p.soft) ctx.filter = `blur(${p.soft * (w / W)}px)`;
    const sky = ctx.createLinearGradient(0, 0, 0, h);
    sky.addColorStop(0, pal.top); sky.addColorStop(.55, pal.mid); sky.addColorStop(1, pal.low);
    ctx.fillStyle = sky; ctx.fillRect(0, 0, w, h);
    const sx = w * .72, sy = h * .62, sr = w * .3;                 // sun glow
    const sun = ctx.createRadialGradient(sx, sy, 0, sx, sy, sr);
    sun.addColorStop(0, pal.sun); sun.addColorStop(.25, pal.sun + '66'); sun.addColorStop(1, '#0000');
    ctx.fillStyle = sun; ctx.beginPath(); ctx.arc(sx, sy, sr, 0, 7); ctx.fill();
    ctx.fillStyle = 'rgba(12,10,16,.9)';                          // horizon ridge
    ctx.beginPath(); ctx.moveTo(0, h * .86);
    for (let x = 0; x <= w; x += w / 14) ctx.lineTo(x, h * (.84 + .03 * Math.sin(x * .004 + p.burst)));
    ctx.lineTo(w, h); ctx.lineTo(0, h); ctx.fill();
    // the bird — position sweeps across the burst, wings flap frame to frame
    const bx = w * (.16 + .68 * p.t + p.jx), by = h * (.42 - .14 * Math.sin(p.t * Math.PI) + p.jy);
    const sz = w * .052, wing = Math.sin(p.flap) * .9;
    ctx.fillStyle = 'rgba(16,12,18,.96)';
    ctx.beginPath(); ctx.ellipse(bx, by, sz * .62, sz * .16, 0, 0, 7); ctx.fill();      // body
    ctx.beginPath(); ctx.moveTo(bx - sz * .1, by);                                       // far wing
    ctx.quadraticCurveTo(bx - sz * .9, by - sz * (.85 * wing + .15), bx - sz * 1.5, by - sz * (.5 * wing));
    ctx.quadraticCurveTo(bx - sz * .8, by - sz * (.3 * wing) + sz * .12, bx - sz * .1, by + sz * .08); ctx.fill();
    ctx.beginPath(); ctx.moveTo(bx + sz * .1, by);                                       // near wing
    ctx.quadraticCurveTo(bx + sz * .9, by - sz * (1.05 * wing + .15), bx + sz * 1.6, by - sz * (.6 * wing));
    ctx.quadraticCurveTo(bx + sz * .85, by - sz * (.35 * wing) + sz * .12, bx + sz * .1, by + sz * .08); ctx.fill();
    ctx.beginPath(); ctx.moveTo(bx + sz * .55, by - sz * .05); ctx.lineTo(bx + sz * .85, by - sz * .16);
    ctx.lineTo(bx + sz * .58, by + sz * .1); ctx.fill();                                 // head
    ctx.filter = 'none';
    ctx.globalAlpha = .05; ctx.globalCompositeOperation = 'overlay';                     // grain
    ctx.fillStyle = ctx.createPattern(grain, 'repeat'); ctx.fillRect(0, 0, w, h);
    ctx.globalAlpha = 1; ctx.globalCompositeOperation = 'source-over';
    const vin = ctx.createRadialGradient(w / 2, h / 2, h * .45, w / 2, h / 2, w * .72);  // vignette + exposure
    vin.addColorStop(0, '#0000'); vin.addColorStop(1, 'rgba(0,0,0,.32)');
    ctx.fillStyle = vin; ctx.fillRect(0, 0, w, h);
    if (p.exp < 1) { ctx.fillStyle = `rgba(0,0,0,${(1 - p.exp) * .9})`; ctx.fillRect(0, 0, w, h); }
    else if (p.exp > 1) {
      ctx.globalCompositeOperation = 'screen';
      ctx.fillStyle = `rgba(255,244,224,${(p.exp - 1) * .5})`; ctx.fillRect(0, 0, w, h);
      ctx.globalCompositeOperation = 'source-over';
    }
  }

  return {
    isMock: true,
    label: '/Volumes/SONY_A7IV/DCIM/100MSDCF',
    w: W, h: H,

    async open() {
      for (const p of shots) {
        if (p.thumb) continue;
        const c = document.createElement('canvas'); c.width = TW; c.height = TH;
        drawScene(c.getContext('2d'), TW, TH, p);
        p.thumb = c.toDataURL('image/jpeg', .85);
      }
      return shots.map(p => ({ id: p.name, name: p.name + '.ARW', pair: 'ARW +JPG', burstStart: p.first }));
    },

    thumbURL(i) { return shots[i].thumb; },

    async full(i) {
      const c = document.createElement('canvas'); c.width = W; c.height = H;
      drawScene(c.getContext('2d'), W, H, shots[i]);
      return c;
    },

    async meta(i) {
      const p = shots[i];
      if (!p) return {};
      const t = new Date(Date.UTC(2026, 7, 2, 16, 40, 0) + i * 1800 * 1000);
      return {
        camera: 'SONY ILCE-6000',
        taken: t.toISOString().slice(0, 19).replace('T', ' '),
        shutter: p.soft ? '1/60s' : '1/2000s',
        aperture: 'f/5.6',
        iso: p.soft ? 800 : 400,
        focal: '210mm',
        lat: 37.8199 + (i % 7) * 2e-4,
        lng: -122.4783 + (i % 5) * 2e-4,
      };
    },

    async commit(indices) {
      const movedIds = indices.map(i => shots[i].name);
      const gone = new Set(indices);
      for (let i = shots.length - 1; i >= 0; i--) if (gone.has(i)) shots.splice(i, 1);
      return { movedIds, dest: 'Trash', errors: [] };
    },
  };
})();
