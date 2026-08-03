/* Real backend: active only inside the Wails app, where the Go bridge
   (window.go) exists. Photo metadata and actions flow over the bridge;
   image bytes come from the asset server's /api routes so the browser
   decodes them off the main thread and the Go side can prefetch. State
   changes arrive as Wails runtime events. Same contract as the mock. */
'use strict';

(function () {
  const bridge = window.go && window.go.main && window.go.main.App;
  if (!bridge) return; // plain browser: app.js falls back to http or mock

  window.QKWailsBackend = {
    isMock: false,
    canPick: true, // native folder dialog available — Open… button shows
    label: '',
    readOnly: false,
    serverMarks: [],
    metas: [],

    _apply(res) {
      this.label = res.dir;
      this.readOnly = !!res.readOnly;
      this.serverMarks = res.rejected || [];
      this.metas = (res.photos || []).map(p => ({
        id: p.id, name: p.name, pair: p.pair, burstStart: false,
      }));
      return this.metas;
    },

    // Returns null when the user cancels the picker — distinct from an
    // empty folder, so a cancel never wipes an already-open session.
    async open() {
      const dir = await bridge.PickFolder();
      if (!dir) {
        if (!this.label) this.label = 'no folder chosen';
        return null;
      }
      return this._apply(await bridge.OpenFolder(dir));
    },

    // Recovery path after the card was pulled: same folder, fresh scan.
    async rescan() {
      return this._apply(await bridge.Rescan());
    },

    async folderPresent() {
      return bridge.FolderPresent();
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

    setReject(id, rejected) {
      bridge.SetReject(id, rejected).catch(() => {});
    },

    async meta(i) {
      const r = await fetch('/api/meta/' + encodeURIComponent(this.metas[i].id));
      return r.ok ? r.json() : {};
    },

    openURL(url) {
      bridge.OpenMapURL(url);
    },

    async commit(indices) {
      const ids = indices.map(i => this.metas[i].id);
      const res = await bridge.CommitRejects(ids);
      const moved = new Set(res.movedIds || []);
      for (let k = this.metas.length - 1; k >= 0; k--) {
        if (moved.has(this.metas[k].id)) this.metas.splice(k, 1);
      }
      return res;
    },

    onEvent(cb) {
      if (window.runtime && window.runtime.EventsOn) {
        window.runtime.EventsOn('qk', cb);
      }
    },

    // Phone remote session controls (the QR sheet).
    async startRemote() { return bridge.StartRemote(); },
    async stopRemote() { return bridge.StopRemote(); },
  };
})();
