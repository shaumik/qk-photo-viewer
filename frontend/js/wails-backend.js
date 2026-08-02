/* Real backend: active only inside the Wails app, where the Go bridge
   (window.go) exists. Photo metadata flows over the bridge; image bytes come
   from the asset server's /api routes so the browser decodes them off the
   main thread and the Go side can prefetch. Same contract as the mock. */
'use strict';

(function () {
  const bridge = window.go && window.go.main && window.go.main.App;
  if (!bridge) return; // plain browser: app.js falls back to the mock

  window.QKWailsBackend = {
    isMock: false,
    label: '',
    readOnly: false,
    metas: [],

    _apply(res) {
      this.label = res.dir;
      this.readOnly = !!res.readOnly;
      this.metas = (res.photos || []).map(p => ({
        id: p.id, name: p.name, pair: p.pair, burstStart: false,
      }));
      return this.metas;
    },

    async open() {
      const dir = await bridge.PickFolder();
      if (!dir) { this.label = 'no folder chosen'; return []; }
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

    async commit(indices) {
      const ids = indices.map(i => this.metas[i].id);
      const res = await bridge.CommitRejects(ids);
      const moved = new Set(res.movedIds || []);
      for (let k = this.metas.length - 1; k >= 0; k--) {
        if (moved.has(this.metas[k].id)) this.metas.splice(k, 1);
      }
      return res;
    },
  };
})();
