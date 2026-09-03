{{flutter_js}}
{{flutter_build_config}}

// HTML renderer was removed in Flutter 3.32; this app is CanvasKit.
// Never block _flutter.loader.load on document.fonts.ready / FontFace.load:
// a missing or slow OTF leaves fonts.ready pending forever, so Flutter never
// starts and #boot-mask stays up. Mask hide lives in Dart (font bound) and
// index.html (5s + click), not here — uncovering at engine-start paints
// deep-link filenames with .notdef.
(function () {
  function hideBootMask() {
    if (typeof window.hideBootMask === 'function') {
      window.hideBootMask();
      return;
    }
    const el = document.getElementById('boot-mask');
    if (el) el.remove();
  }

  function withTimeout(promise, ms) {
    return Promise.race([
      promise.catch(function () {}),
      new Promise(function (resolve) { setTimeout(resolve, ms); }),
    ]);
  }

  async function cleanupServiceWorkers() {
    if ('serviceWorker' in navigator) {
      const regs = await navigator.serviceWorker.getRegistrations();
      await Promise.all(regs.map(function (r) { return r.unregister(); }));
    }
    if (window.caches && caches.keys) {
      const keys = await caches.keys();
      await Promise.all(keys.map(function (k) { return caches.delete(k); }));
    }
  }

  (async function () {
    try {
      await withTimeout(cleanupServiceWorkers(), 400);
    } catch (e) {
      console.warn('service worker cleanup failed', e);
    }

    // Do not register a new Flutter service worker: LAN deploys must not get
    // stuck on a cached main.dart.js after the next dist update.
    _flutter.loader.load({
      onEntrypointLoaded: async function (engineInitializer) {
        const appRunner = await engineInitializer.initializeEngine();
        await appRunner.runApp();
        // Dart main() waits for FontLoader (capped) then runApp, then hides
        // the mask when Noto is bound. Do not uncover here: a deep-link
        // FilePage would paint filename with .notdef. HTML uncovers at 5s.
      },
    });
  })();
})();
