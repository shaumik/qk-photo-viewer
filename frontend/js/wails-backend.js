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
    metas: [],

    async open() {
      const dir = await bridge.PickFolder();
      if (!dir) { this.label = 'no folder chosen'; return []; }
      const photos = await bridge.OpenFolder(dir);
      this.label = dir;
      this.metas = (photos || []).map(p => ({
        id: p.id, name: p.name, pair: p.pair, burstStart: false,
      }));
      return this.metas;
    },

    thumbURL(i) {
      return '/api/thumb/' + encodeURIComponent(this.metas[i].id);
    },

    async full(i) {
      const im = new Image();
      im.src = '/api/preview/' + encodeURIComponent(this.metas[i].id);
      try { await im.decode(); } catch (e) { /* still renders once loaded */ }
      return im;
    },

    async commit(indices) {
      const ids = indices.map(i => this.metas[i].id);
      await bridge.CommitRejects(ids);
      for (let k = indices.length - 1; k >= 0; k--) this.metas.splice(indices[k], 1);
    },
  };
})();
