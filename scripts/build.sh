#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

mkdir -p dist/static

echo "==> build go api"
(cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o "$ROOT/dist/minicloudstorage" ./cmd/server)
chmod +x dist/minicloudstorage

echo "==> build flutter web"
# Flutter 3.32 removed the HTML renderer; CanvasKit is the only option.
# --no-web-resources-cdn keeps canvaskit local (no gstatic). CJK tofu is
# handled in web/index.html + FontLoader with timeouts — never block
# flutter_bootstrap.js on document.fonts.ready (that hung #boot-mask).
if command -v flutter >/dev/null 2>&1; then
  (cd frontend && flutter pub get && flutter build web --release --base-href / --no-web-resources-cdn)
  rm -rf dist/static
  cp -a frontend/build/web dist/static
else
  echo "flutter SDK not found, using docker image"
  docker run --rm --network host \
    -e PUB_CACHE=/frontend/.pub-cache \
    -v "$ROOT/frontend":/frontend \
    -w /frontend \
    instrumentisto/flutter:3.32.8 \
    bash -lc "flutter pub get && flutter build web --release --base-href / --no-web-resources-cdn"
  rm -rf dist/static
  cp -a frontend/build/web dist/static
fi

# Flutter always emits flutter_service_worker.js. Replace it with a kill-switch
# so an old registration that updates this URL will drop caches and unregister.
# New loads do not register a service worker (see flutter_bootstrap.js).
cat > dist/static/flutter_service_worker.js <<'EOF'
self.addEventListener('install', function (event) {
  self.skipWaiting();
});
self.addEventListener('activate', function (event) {
  event.waitUntil((async function () {
    try {
      var keys = await caches.keys();
      await Promise.all(keys.map(function (k) { return caches.delete(k); }));
    } catch (e) {}
    await self.registration.unregister();
    var clients = await self.clients.matchAll({ type: 'window' });
    clients.forEach(function (c) { c.navigate(c.url); });
  })());
});
self.addEventListener('fetch', function (event) {
  event.respondWith(fetch(event.request));
});
EOF

# Cache-bust the two files the browser actually loads. Flutter's fileServer
# ignores the query string; a new ?v= forces a new cache key even if an old
# service worker or heuristic cache still has main.dart.js.
BUILD_ID="$(date +%s)"
python3 - "$BUILD_ID" <<'PY'
import sys
from pathlib import Path
bid = sys.argv[1]
root = Path("dist/static")
idx = root / "index.html"
text = idx.read_text(encoding="utf-8")
old = 'src="flutter_bootstrap.js"'
new = f'src="flutter_bootstrap.js?v={bid}"'
if old not in text:
    raise SystemExit("index.html missing flutter_bootstrap.js script src")
idx.write_text(text.replace(old, new, 1), encoding="utf-8")
boot = root / "flutter_bootstrap.js"
b = boot.read_text(encoding="utf-8")
old_js = '"mainJsPath":"main.dart.js"'
new_js = f'"mainJsPath":"main.dart.js?v={bid}"'
if old_js not in b:
    raise SystemExit("flutter_bootstrap.js missing mainJsPath")
boot.write_text(b.replace(old_js, new_js, 1), encoding="utf-8")
(root / ".last_build_id").write_text(bid + "\n", encoding="utf-8")
print(f"cache-bust v={bid}")
PY

echo "build artifacts in dist/"
ls -lh dist/minicloudstorage
ls -l --time-style=long-iso dist/static/main.dart.js frontend/lib/api.dart
du -sh dist/static
