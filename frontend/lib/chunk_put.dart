import 'dart:async';
import 'dart:js_interop';
import 'dart:js_interop_unsafe';

import 'package:web/web.dart' as web;

/// Worker abort + Dart backup. Stay under Cloudflare's ~100s proxy timeout so
/// a hanging PUT fails into retry instead of freezing the 4-wide pool.
const kChunkPutTimeout = Duration(seconds: 90);
const kChunkPutTimeoutMs = 90000;

class ChunkPutResult {
  ChunkPutResult({required this.status, required this.body});
  final int status;
  final String body;
}

/// Uploads a [web.Blob] via a Web Worker so the 8MB read/send does not run on
/// the Dart UI isolate (which is also CanvasKit's raster thread).
class ChunkPutClient {
  ChunkPutClient._();
  static final ChunkPutClient instance = ChunkPutClient._();

  web.Worker? _worker;
  bool _workerBroken = false;
  int _nextId = 0;
  final Map<int, Completer<ChunkPutResult>> _pending = {};

  bool get isAvailable {
    _ensureWorker();
    return _worker != null;
  }

  Future<ChunkPutResult> put({
    required String url,
    required web.Blob blob,
  }) async {
    _ensureWorker();
    final worker = _worker;
    if (worker == null) {
      throw StateError('upload worker unavailable');
    }
    final id = _nextId++;
    final completer = Completer<ChunkPutResult>();
    _pending[id] = completer;
    final msg = JSObject();
    msg.setProperty('id'.toJS, id.toJS);
    msg.setProperty('url'.toJS, url.toJS);
    msg.setProperty('blob'.toJS, blob);
    msg.setProperty('timeoutMs'.toJS, kChunkPutTimeoutMs.toJS);
    worker.postMessage(msg);
    return completer.future.timeout(
      kChunkPutTimeout + const Duration(seconds: 5),
      onTimeout: () {
        _pending.remove(id);
        throw TimeoutException('chunk put timeout', kChunkPutTimeout);
      },
    );
  }

  void _ensureWorker() {
    if (_worker != null || _workerBroken) return;
    try {
      final script = web.URL(
        'chunk_upload_worker.js?v=2',
        web.document.baseURI,
      );
      final worker = web.Worker(script.href.toJS);
      worker.addEventListener(
        'message',
        (web.Event event) {
          final data = (event as web.MessageEvent).data?.dartify();
          if (data is! Map) return;
          final id = _asInt(data['id']);
          if (id == null) return;
          final pending = _pending.remove(id);
          if (pending == null || pending.isCompleted) return;
          pending.complete(
            ChunkPutResult(
              status: _asInt(data['status']) ?? 0,
              body: _asString(data['body']) ?? '',
            ),
          );
        }.toJS,
      );
      worker.addEventListener(
        'error',
        (web.Event _) {
          _failAll();
        }.toJS,
      );
      _worker = worker;
    } catch (_) {
      _workerBroken = true;
    }
  }

  void abortAll() {
    final worker = _worker;
    _worker = null;
    for (final pending in _pending.values) {
      if (!pending.isCompleted) {
        pending.completeError(StateError('upload cancelled'));
      }
    }
    _pending.clear();
    worker?.terminate();
  }

  static int? _asInt(dynamic value) {
    if (value is num) return value.toInt();
    if (value is String) return int.tryParse(value);
    return null;
  }

  static String? _asString(dynamic value) => value is String ? value : null;

  void _failAll() {
    _workerBroken = true;
    final err = StateError('upload worker failed');
    for (final pending in _pending.values) {
      if (!pending.isCompleted) pending.completeError(err);
    }
    _pending.clear();
  }
}
