import 'dart:async';
import 'dart:convert';
import 'dart:js_interop';

import 'package:http/http.dart' as http;
import 'package:minicloudstorage/chunk_put.dart';
import 'package:web/web.dart' as web;

/// dart2js: `json['x'] as String` / `as String?` throws TypeError when the
/// JSON value is null or not a String. `is String` does not.
String? _asString(dynamic value) => value is String ? value : null;

List<bool> _asBoolList(dynamic value) {
  if (value is! List) return const [];
  return value.map((e) => e == true).toList();
}

class ApiException implements Exception {
  ApiException(this.status, this.code, [this.body, this.retryAfter]);

  final int status;
  final String code;
  final String? body;
  final int? retryAfter;

  @override
  String toString() => 'ApiException($status, $code)';
}

class InitUploadResult {
  InitUploadResult({
    required this.uploadId,
    required this.chunkSize,
    required this.totalChunks,
  });

  final String uploadId;
  final int chunkSize;
  final int totalChunks;
}

class CompleteUploadResult {
  CompleteUploadResult({
    required this.code,
    required this.url,
    required this.expiresAt,
    this.password,
    this.urlWithPassword,
  });

  final String code;
  final String url;
  final DateTime expiresAt;
  final String? password;
  final String? urlWithPassword;
}

class CancelUploadResult {
  CancelUploadResult({
    this.alreadyCompleted = false,
    this.completed,
  });

  final bool alreadyCompleted;
  final CompleteUploadResult? completed;
}

class UploadStatus {
  UploadStatus({
    required this.status,
    this.code,
    this.url,
    this.expiresAt,
    this.password,
    this.urlWithPassword,
    this.reasonCode,
    this.filename,
    this.userCancelled = false,
    this.receivedChunks = 0,
    this.totalChunks = 0,
    this.receivedFlags = const [],
  });

  final String status;
  final String? code;
  final String? url;
  final DateTime? expiresAt;
  final String? password;
  final String? urlWithPassword;
  final String? reasonCode;
  final String? filename;
  final bool userCancelled;
  final int receivedChunks;
  final int totalChunks;
  final List<bool> receivedFlags;

  bool get isCompleted => status == 'completed';
  bool get isFailed => status == 'failed' || status == 'cancelled';
  bool get isUserCancelled =>
      userCancelled ||
      reasonCode == 'user_cancel' ||
      reasonCode == 'page_close' ||
      status == 'cancelled';
  bool get isPending => status == 'uploading' || status == 'merging';

  CompleteUploadResult? tryCompleteResult() {
    final code = this.code;
    final url = this.url;
    final expiresAt = this.expiresAt;
    if (code == null || url == null || expiresAt == null) return null;
    return CompleteUploadResult(
      code: code,
      url: url,
      expiresAt: expiresAt,
      password: password,
      urlWithPassword: urlWithPassword,
    );
  }

  CompleteUploadResult toCompleteResult() {
    final done = tryCompleteResult();
    if (done == null) throw ApiException(500, 'incomplete_status');
    return done;
  }
}

class FileMeta {
  FileMeta({
    required this.code,
    required this.filename,
    required this.sizeBytes,
    required this.contentType,
    required this.createdAt,
    required this.expiresAt,
    required this.daysRemaining,
    required this.hasPassword,
    this.status = 'ready',
    this.expiredAt,
  });

  final String code;
  final String filename;
  final int sizeBytes;
  final String contentType;
  final DateTime createdAt;
  final DateTime expiresAt;
  final int daysRemaining;
  final bool hasPassword;
  final String status;
  final DateTime? expiredAt;

  bool get isExpired => status == 'expired';
}

class ApiClient {
  ApiClient({http.Client? client}) : _client = client ?? http.Client();

  final http.Client _client;

  Future<InitUploadResult> initUpload({
    required String filename,
    required int size,
    required String contentType,
    bool enablePassword = false,
  }) async {
    final res = await _client.post(
      Uri.parse('/api/v1/uploads'),
      headers: {'Content-Type': 'application/json'},
      body: jsonEncode({
        'filename': filename,
        'size': size,
        'content_type': contentType,
        'enable_password': enablePassword,
      }),
    );
    final json = _decode(res);
    return InitUploadResult(
      uploadId: _asString(json['upload_id']) ?? '',
      chunkSize: (json['chunk_size'] as num?)?.toInt() ?? 0,
      totalChunks: (json['total_chunks'] as num?)?.toInt() ?? 0,
    );
  }

  /// PUT a browser [web.Blob] without copying bytes into a Dart [Uint8List].
  /// Prefers a Web Worker so the UI thread is not blocked on each 8MB slice.
  Future<void> putChunkBlob({
    required String uploadId,
    required int index,
    required web.Blob blob,
  }) async {
    final url = '/api/v1/uploads/$uploadId/chunks/$index';
    if (ChunkPutClient.instance.isAvailable) {
      final result = await ChunkPutClient.instance.put(url: url, blob: blob);
      if (result.status == 204) return;
      throw _fromWorker(result.status, result.body);
    }
    await _putOnUiThread(url, blob);
  }

  Future<void> _putOnUiThread(String url, web.Blob blob) async {
    final headers = web.Headers();
    headers.set('Content-Type', 'application/octet-stream');
    final ac = web.AbortController();
    final timer = Timer(kChunkPutTimeout, () => ac.abort());
    try {
      final res = await web.window
          .fetch(
            url.toJS,
            web.RequestInit(
              method: 'PUT',
              body: blob,
              headers: headers,
              signal: ac.signal,
            ),
          )
          .toDart;
      if (res.status == 204) return;
      throw await _fromFetch(res);
    } finally {
      timer.cancel();
    }
  }

  Future<CompleteUploadResult> complete({
    required String uploadId,
    required bool enablePassword,
    bool Function()? shouldStop,
  }) async {
    try {
      final res = await _client.post(
        Uri.parse('/api/v1/uploads/$uploadId/complete'),
        headers: {'Content-Type': 'application/json'},
        body: jsonEncode({'enable_password': enablePassword}),
      );
      if (res.statusCode == 202 || _isRecoverableCompleteStatus(res.statusCode)) {
        return waitUntilComplete(uploadId, shouldStop: shouldStop);
      }
      if (res.statusCode == 200) {
        final json = _decode(res);
        final status = _asString(json['status']);
        if (status == 'merging' || status == 'uploading') {
          return waitUntilComplete(uploadId, shouldStop: shouldStop);
        }
        final done = _tryCompleteFromJson(json);
        if (done != null) return done;
        return waitUntilComplete(uploadId, shouldStop: shouldStop);
      }
      throw _fromResponse(res);
    } on ApiException catch (e) {
      if (!_isRecoverableCompleteError(e)) rethrow;
    } catch (e) {
      if (e is ApiException) rethrow;
    }
    return waitUntilComplete(uploadId, shouldStop: shouldStop);
  }

  Future<UploadStatus> getUpload(String uploadId) async {
    final res = await _client.get(Uri.parse('/api/v1/uploads/$uploadId'));
    if (res.statusCode == 404) {
      throw ApiException(404, 'upload_not_found', res.body);
    }
    return _uploadStatusFromJson(_decode(res));
  }

  Future<CompleteUploadResult> waitUntilComplete(
    String uploadId, {
    bool Function()? shouldStop,
  }) async {
    final deadline = DateTime.now().add(const Duration(minutes: 15));
    Object? lastErr;
    while (DateTime.now().isBefore(deadline)) {
      if (shouldStop?.call() == true) {
        throw ApiException(409, 'user_cancel');
      }
      try {
        final st = await getUpload(uploadId);
        if (st.isCompleted) {
          final done = st.tryCompleteResult();
          if (done != null) return done;
        } else if (st.isFailed) {
          throw ApiException(
            409,
            st.reasonCode ?? st.status,
            'upload ${st.status}',
          );
        }
        lastErr = null;
      } on ApiException catch (e) {
        lastErr = e;
        if (e.status == 409) rethrow;
      } catch (_) {
        // TypeError / parse glitch while merging: keep polling.
      }
      await Future<void>.delayed(const Duration(seconds: 2));
    }
    if (lastErr is ApiException) throw lastErr;
    throw ApiException(504, 'merge_timeout');
  }

  UploadStatus _uploadStatusFromJson(Map<String, dynamic> json) {
    final status = _asString(json['status']) ?? 'unknown';
    final completed = status == 'completed' || status == 'already_completed';
    DateTime? expiresAt;
    final raw = json['expires_at'];
    if (completed && raw is String && raw.isNotEmpty) {
      expiresAt = DateTime.tryParse(raw);
    }
    return UploadStatus(
      status: status,
      code: completed ? _asString(json['code']) : null,
      url: completed ? _asString(json['url']) : null,
      expiresAt: expiresAt,
      password: completed ? _asString(json['password']) : null,
      urlWithPassword: completed ? _asString(json['url_with_password']) : null,
      reasonCode: _asString(json['reason_code']),
      filename: _asString(json['filename']),
      userCancelled: json['user_cancelled'] == true,
      receivedChunks: (json['received_chunks'] as num?)?.toInt() ?? 0,
      totalChunks: (json['total_chunks'] as num?)?.toInt() ?? 0,
      receivedFlags: _asBoolList(json['received_flags']),
    );
  }

  CompleteUploadResult? _tryCompleteFromJson(Map<String, dynamic> json) {
    final status = _asString(json['status']);
    if (status == 'merging' ||
        status == 'uploading' ||
        status == 'failed' ||
        status == 'cancelled') {
      return null;
    }
    final code = _asString(json['code']);
    final url = _asString(json['url']);
    final raw = json['expires_at'];
    final expiresAt =
        raw is String && raw.isNotEmpty ? DateTime.tryParse(raw) : null;
    if (code == null || url == null || expiresAt == null) return null;
    return CompleteUploadResult(
      code: code,
      url: url,
      expiresAt: expiresAt,
      password: _asString(json['password']),
      urlWithPassword: _asString(json['url_with_password']),
    );
  }

  bool _isRecoverableCompleteStatus(int status) =>
      status == 502 || status == 504 || status == 524;

  bool _isRecoverableCompleteError(ApiException e) {
    if (_isRecoverableCompleteStatus(e.status)) return true;
    return e.code == 'storage_error' ||
        e.code == 'http_502' ||
        e.code == 'http_504' ||
        e.code == 'http_524';
  }

  Future<CancelUploadResult> cancelUpload({
    required String uploadId,
    required String reason,
  }) async {
    final res = await _client.post(
      Uri.parse('/api/v1/uploads/$uploadId/cancel').replace(
        queryParameters: {'reason': reason},
      ),
      headers: {'Content-Type': 'application/json'},
      body: jsonEncode({'reason_code': reason}),
    );
    if (res.statusCode == 204 || res.statusCode == 404) {
      return CancelUploadResult();
    }
    Map<String, dynamic> json = {};
    if (res.body.isNotEmpty) {
      try {
        json = _asMap(jsonDecode(res.body));
      } catch (_) {}
    }
    final err = _asString(json['error']);
    final status = _asString(json['status']);
    if (err == 'already_completed' || status == 'already_completed') {
      return CancelUploadResult(
        alreadyCompleted: true,
        completed: _tryCompleteFromJson(json),
      );
    }
    if (res.statusCode == 200 || res.statusCode == 409) {
      final completed = _tryCompleteFromJson(json);
      if (completed != null) {
        return CancelUploadResult(
          alreadyCompleted: true,
          completed: completed,
        );
      }
      if (res.statusCode == 200) {
        return CancelUploadResult();
      }
    }
    throw _fromResponse(res);
  }

  Future<FileMeta> getFile(String code, {String? password}) async {
    final uri = Uri.parse('/api/v1/files/$code').replace(
      queryParameters: (password != null && password.isNotEmpty)
          ? {'p': password}
          : null,
    );
    final res = await _client.get(uri);
    if (res.statusCode == 410) {
      return _fileMeta(_asMap(jsonDecode(res.body)), expired: true);
    }
    final json = _decode(res);
    return _fileMeta(json, expired: _asString(json['status']) == 'expired');
  }

  FileMeta _fileMeta(Map<String, dynamic> json, {required bool expired}) {
    DateTime? expiredAt;
    final raw = json['expired_at'];
    if (raw is String && raw.isNotEmpty) {
      expiredAt = DateTime.tryParse(raw);
    }
    return FileMeta(
      code: _asString(json['code']) ?? '',
      filename: _asString(json['filename']) ?? '',
      sizeBytes: (json['size_bytes'] as num?)?.toInt() ?? 0,
      contentType: _asString(json['content_type']) ?? '',
      createdAt: DateTime.parse(_asString(json['created_at']) ?? ''),
      expiresAt: DateTime.parse(_asString(json['expires_at']) ?? ''),
      daysRemaining: (json['days_remaining'] as num?)?.toInt() ?? 0,
      hasPassword: json['has_password'] == true,
      status: expired ? 'expired' : (_asString(json['status']) ?? 'ready'),
      expiredAt: expiredAt,
    );
  }

  String downloadUrl(String code, {String? password}) {
    final uri = Uri.parse('/api/v1/files/$code/download').replace(
      queryParameters: (password != null && password.isNotEmpty)
          ? {'p': password}
          : null,
    );
    return uri.toString();
  }

  Map<String, dynamic> _decode(http.Response res) {
    if (res.statusCode < 200 || res.statusCode >= 300) {
      throw _fromResponse(res);
    }
    if (res.body.isEmpty) {
      return {};
    }
    return _asMap(jsonDecode(res.body));
  }

  Map<String, dynamic> _asMap(dynamic value) {
    if (value is Map<String, dynamic>) return value;
    if (value is Map) return Map<String, dynamic>.from(value);
    return {};
  }

  ApiException _fromResponse(http.Response res) {
    String code = 'http_${res.statusCode}';
    try {
      final json = jsonDecode(res.body);
      if (json is Map) {
        code = _asString(json['error']) ?? code;
      }
    } catch (_) {}
    return ApiException(res.statusCode, code, res.body);
  }

  Future<ApiException> _fromFetch(web.Response res) async {
    String code = 'http_${res.status}';
    String? body;
    try {
      body = (await res.text().toDart).toDart;
      final json = jsonDecode(body);
      if (json is Map) {
        code = _asString(json['error']) ?? code;
      }
    } catch (_) {}
    return ApiException(res.status, code, body);
  }

  ApiException _fromWorker(int status, String body) {
    String code = 'http_$status';
    try {
      final json = jsonDecode(body);
      if (json is Map) {
        code = _asString(json['error']) ?? code;
      }
    } catch (_) {}
    return ApiException(status, code, body);
  }
}
