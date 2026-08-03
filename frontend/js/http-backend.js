/* Phone remote backend: active when the UI is served by the LAN remote
   server (qkserve or the desktop app's phone session) — no Wails bridge,
   everything over the same-origin HTTP API, live sync via SSE. */
'use strict';

window.QKHttpBackend = {
  async detect() {
    if (window.go) return null; // inside the desktop app: the bridge backend wins
    if (location.protocol === 'file:') return null; // opened from disk: mock territory
    try {
      const r = await fetch('/api/photos');
      if (!r.ok) return null;
      const st = await r.json();
      if (!st.dir) return null; // an /api impostor, or nothing open
      return this._make(st);
    } catch (e) {
      return null; // file:// or plain static hosting: fall back to the mock
    }
  },

  _make(initial) {
    return {
      isMock: false,
      isRemote: true,
      label: '',
      readOnly: false,
      serverMarks: [],
      metas: [],

      _apply(st) {
        this.label = st.dir;
        this.readOnly = !!st.readOnly;
        this.serverMarks = st.rejected || [];
        this.metas = (st.photos || []).map(p => ({
          id: p.id, name: p.name, pair: p.pair, burstStart: false,
        }));
        return this.metas;
      },

      async open() { return this._apply(initial); },

      async refresh() {
        const r = await fetch('/api/photos');
        return this._apply(await r.json());
      },

      thumbURL(i) {
        return '/api/thumb/' + encodeURIComponent(this.metas[i].id);
      },

      async full(i) {
        const meta = this.metas[i];
        const im = new Image();
        im.src = '/api/preview/' + encodeURIComponent(meta.id);
        await new Promise((resolve, reject) => {
          im.onload = resolve;
          im.onerror = () => reject(new Error('could not load ' + meta.name));
        });
        try { await im.decode(); } catch (e) { /* decoded on paint instead */ }
        return im;
      },

      async meta(i) {
        const r = await fetch('/api/meta/' + encodeURIComponent(this.metas[i].id));
        return r.ok ? r.json() : {};
      },

      setReject(id, rejected) {
        fetch('/api/reject', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id, rejected }),
        }).catch(() => {});
      },

      async commit(indices) {
        const ids = indices.map(i => this.metas[i].id);
        const r = await fetch('/api/commit', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ ids }),
        });
        if (!r.ok) throw new Error('commit failed: HTTP ' + r.status);
        const res = await r.json();
        const moved = new Set(res.movedIds || []);
        for (let k = this.metas.length - 1; k >= 0; k--) {
          if (moved.has(this.metas[k].id)) this.metas.splice(k, 1);
        }
        return res;
      },

      onEvent(cb) {
        const es = new EventSource('/api/events'); // reconnects by itself
        es.onmessage = e => { try { cb(JSON.parse(e.data)); } catch (_) { } };
      },

      async folderPresent() {
        try { return (await fetch('/api/photos')).ok; } catch (e) { return false; }
      },
    };
  },
};
