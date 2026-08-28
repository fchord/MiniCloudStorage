import 'dart:convert';
import 'dart:typed_data';

import 'package:http/http.dart' as http;

class ApiException implements Exception {
  ApiException(this.status, this.code, [this.body]);

  final int status;
  final String code;
  final String? body;

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
  });

  final String code;
  final String filename;
  final int sizeBytes;
  final String contentType;
  final DateTime createdAt;
  final DateTime expiresAt;
  final int daysRemaining;
  final bool hasPassword;
}

class ApiClient {
  ApiClient({http.Client? client}) : _client = client ?? http.Client();

  final http.Client _client;

  Future<InitUploadResult> initUpload({
    required String filename,
    required int size,
    required String contentType,
  }) async {
    final res = await _client.post(
      Uri.parse('/api/v1/uploads'),
      headers: {'Content-Type': 'application/json'},
      body: jsonEncode({
        'filename': filename,
        'size': size,
        'content_type': contentType,
      }),
    );
    final json = _decode(res);
    return InitUploadResult(
      uploadId: json['upload_id'] as String,
      chunkSize: json['chunk_size'] as int,
      totalChunks: json['total_chunks'] as int,
    );
  }

  Future<void> putChunk({
    required String uploadId,
    required int index,
    required Uint8List bytes,
  }) async {
    final res = await _client.put(
      Uri.parse('/api/v1/uploads/$uploadId/chunks/$index'),
      headers: {'Content-Type': 'application/octet-stream'},
      body: bytes,
    );
    if (res.statusCode != 204) {
      throw _fromResponse(res);
    }
  }

  Future<CompleteUploadResult> complete({
    required String uploadId,
    required bool enablePassword,
  }) async {
    final res = await _client.post(
      Uri.parse('/api/v1/uploads/$uploadId/complete'),
      headers: {'Content-Type': 'application/json'},
      body: jsonEncode({'enable_password': enablePassword}),
    );
    final json = _decode(res);
    return CompleteUploadResult(
      code: json['code'] as String,
      url: json['url'] as String,
      expiresAt: DateTime.parse(json['expires_at'] as String),
      password: json['password'] as String?,
      urlWithPassword: json['url_with_password'] as String?,
    );
  }

  Future<FileMeta> getFile(String code, {String? password}) async {
    final uri = Uri.parse('/api/v1/files/$code').replace(
      queryParameters: (password != null && password.isNotEmpty)
          ? {'p': password}
          : null,
    );
    final res = await _client.get(uri);
    final json = _decode(res);
    return FileMeta(
      code: json['code'] as String,
      filename: json['filename'] as String,
      sizeBytes: json['size_bytes'] as int,
      contentType: json['content_type'] as String,
      createdAt: DateTime.parse(json['created_at'] as String),
      expiresAt: DateTime.parse(json['expires_at'] as String),
      daysRemaining: json['days_remaining'] as int,
      hasPassword: json['has_password'] as bool,
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
    return jsonDecode(res.body) as Map<String, dynamic>;
  }

  ApiException _fromResponse(http.Response res) {
    String code = 'http_${res.statusCode}';
    try {
      final json = jsonDecode(res.body);
      if (json is Map && json['error'] is String) {
        code = json['error'] as String;
      }
    } catch (_) {}
    return ApiException(res.statusCode, code, res.body);
  }
}
