'use strict';

// PUT file slices on a worker thread so the Flutter UI isolate / CanvasKit
// spinner is not blocked while the browser reads an 8MB Blob onto the wire.
// Overlapping async fetches are intentional: the UI posts up to 4 chunks at
// once and this worker must not queue them strictly one-by-one.
// Abort after 90s: Cloudflare/origin can hang a PUT with no response, which
// previously deadlocked the 4-wide Dart pool (progress frozen at N/93).
const DEFAULT_TIMEOUT_MS = 90000;

self.onmessage = async (event) => {
  const { id, url, blob, timeoutMs } = event.data;
  const ms = typeof timeoutMs === 'number' && timeoutMs > 0
    ? timeoutMs
    : DEFAULT_TIMEOUT_MS;
  const ac = new AbortController();
  const timer = setTimeout(function () { ac.abort(); }, ms);
  try {
    const res = await fetch(url, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/octet-stream' },
      body: blob,
      signal: ac.signal,
    });
    let body = '';
    if (res.status !== 204) {
      try {
        body = await res.text();
      } catch (_) {}
    }
    self.postMessage({ id: id, status: res.status, body: body });
  } catch (err) {
    const aborted = err && (err.name === 'AbortError' || String(err).indexOf('abort') !== -1);
    self.postMessage({
      id: id,
      status: aborted ? 408 : 0,
      body: String(err),
    });
  } finally {
    clearTimeout(timer);
  }
};
