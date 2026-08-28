#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

mkdir -p dist/static

echo "==> build go api"
(cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o "$ROOT/dist/minicloudstorage" ./cmd/server)
chmod +x dist/minicloudstorage

echo "==> build flutter web"
if command -v flutter >/dev/null 2>&1; then
  (cd frontend && flutter pub get && flutter build web --release --base-href /)
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

echo "build artifacts in dist/"
ls -lh dist/minicloudstorage
du -sh dist/static
