import 'dart:convert';

import 'package:http/http.dart' as http;
import 'package:minicloudstorage/api.dart';
import 'package:web/web.dart' as web;

const _adminTokenKey = 'mcs_admin_token';
const _setupTokenKey = 'mcs_setup_token';

String? _asString(dynamic value) => value is String ? value : null;

class AdminLoginLockStatus {
  AdminLoginLockStatus({
    required this.locked,
    required this.retryAfter,
    required this.totpEnrolled,
  });

  final bool locked;
  final int retryAfter;
  final bool totpEnrolled;
}

class AdminRecord {
  AdminRecord({
    required this.id,
    required this.kind,
    required this.code,
    required this.filename,
    required this.sizeBytes,
    required this.storagePath,
    required this.createdAt,
    this.expiresAt,
    required this.password,
    required this.reasonCode,
  });

  final String id;
  final String kind;
  final String code;
  final String filename;
  final int sizeBytes;
  final String storagePath;
  final DateTime createdAt;
  final DateTime? expiresAt;
  final String password;
  final String reasonCode;
}

class SetupStatus {
  SetupStatus({required this.totpEnrolled});

  final bool totpEnrolled;
}

class SetupLoginResult {
  SetupLoginResult({required this.token, required this.totpEnrolled});

  final String token;
  final bool totpEnrolled;
}

class SetupTotpBegin {
  SetupTotpBegin({
    required this.secret,
    required this.otpauthUrl,
    required this.totpEnrolled,
  });

  final String secret;
  final String otpauthUrl;
  final bool totpEnrolled;
}

class AdminApi {
  AdminApi({http.Client? client}) : _client = client ?? http.Client();

  final http.Client _client;

  static String? readToken() => web.window.sessionStorage.getItem(_adminTokenKey);

  static void saveToken(String token) {
    web.window.sessionStorage.setItem(_adminTokenKey, token);
  }

  static void clearToken() {
    web.window.sessionStorage.removeItem(_adminTokenKey);
  }

  Future<AdminLoginLockStatus> loginLockStatus() async {
    final res = await _client.get(Uri.parse('/api/v1/admin/login'));
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, _code(res), res.body);
    }
    final json = jsonDecode(res.body);
    if (json is! Map) {
      throw ApiException(500, 'bad_lock_status', res.body);
    }
    final retry = json['retry_after'];
    return AdminLoginLockStatus(
      locked: json['locked'] == true,
      retryAfter: retry is num ? retry.ceil() : 0,
      totpEnrolled: json['totp_enrolled'] == true,
    );
  }

  Future<String> login(String password, String totp) async {
    final res = await _client.post(
      Uri.parse('/api/v1/admin/login'),
      headers: {'Content-Type': 'application/json'},
      body: jsonEncode({'password': password, 'totp': totp}),
    );
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, _code(res), res.body, _retryAfter(res));
    }
    final json = jsonDecode(res.body);
    if (json is! Map) {
      throw ApiException(500, 'bad_token', res.body);
    }
    final token = _asString(json['token']);
    if (token == null || token.isEmpty) {
      throw ApiException(500, 'bad_token', res.body);
    }
    saveToken(token);
    return token;
  }

  Future<List<AdminRecord>> list(String tab) async {
    final res = await _client.get(
      Uri.parse('/api/v1/admin/records').replace(queryParameters: {'tab': tab}),
      headers: _headers(),
    );
    _throwIfAuth(res);
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, _code(res), res.body);
    }
    final decoded = jsonDecode(res.body);
    final json = decoded is Map ? decoded : <String, dynamic>{};
    final rawItems = json['items'];
    final items = rawItems is List ? rawItems : const [];
    return items.map((raw) {
      final m = raw is Map ? raw : const {};
      DateTime? expires;
      final exp = m['expires_at'];
      if (exp is String && exp.isNotEmpty) {
        expires = DateTime.tryParse(exp);
      }
      final created = _asString(m['created_at']);
      return AdminRecord(
        id: _asString(m['id']) ?? '',
        kind: _asString(m['kind']) ?? tab,
        code: _asString(m['code']) ?? '',
        filename: _asString(m['filename']) ?? '',
        sizeBytes: (m['size_bytes'] is num) ? (m['size_bytes'] as num).toInt() : 0,
        storagePath: _asString(m['storage_path']) ?? '',
        createdAt: DateTime.parse(created ?? ''),
        expiresAt: expires,
        password: _asString(m['password']) ?? '',
        reasonCode: _asString(m['reason_code']) ?? '',
      );
    }).toList();
  }

  Future<void> deleteRecord(String id) async {
    final res = await _client.delete(
      Uri.parse('/api/v1/admin/records/$id'),
      headers: _headers(),
    );
    _throwIfAuth(res);
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, _code(res), res.body);
    }
  }

  Future<void> clearPassword(String id) async {
    final res = await _client.post(
      Uri.parse('/api/v1/admin/records/$id/clear-password'),
      headers: _headers(),
    );
    _throwIfAuth(res);
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, _code(res), res.body);
    }
  }

  Future<SetupStatus> setupStatus() async {
    final res = await _client.get(Uri.parse('/api/v1/admin/setup'));
    if (res.statusCode == 404) {
      throw ApiException(404, 'not_found', res.body);
    }
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, _code(res), res.body);
    }
    final json = jsonDecode(res.body);
    if (json is! Map) {
      throw ApiException(500, 'bad_setup_status', res.body);
    }
    return SetupStatus(totpEnrolled: json['totp_enrolled'] == true);
  }

  Future<SetupLoginResult> setupLogin(String password, {String totp = ''}) async {
    final res = await _client.post(
      Uri.parse('/api/v1/admin/setup/login'),
      headers: {'Content-Type': 'application/json'},
      body: jsonEncode({'password': password, 'totp': totp}),
    );
    if (res.statusCode == 404) {
      throw ApiException(404, 'not_found', res.body);
    }
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, _code(res), res.body, _retryAfter(res));
    }
    final json = jsonDecode(res.body);
    if (json is! Map) {
      throw ApiException(500, 'bad_token', res.body);
    }
    final token = _asString(json['token']);
    if (token == null || token.isEmpty) {
      throw ApiException(500, 'bad_token', res.body);
    }
    saveSetupToken(token);
    return SetupLoginResult(token: token, totpEnrolled: json['totp_enrolled'] == true);
  }

  Future<void> setupChangePassword({
    required String currentPassword,
    required String newPassword,
  }) async {
    final res = await _client.post(
      Uri.parse('/api/v1/admin/setup/password'),
      headers: _setupHeaders(),
      body: jsonEncode({
        'current_password': currentPassword,
        'new_password': newPassword,
      }),
    );
    _throwIfSetup(res);
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, _code(res), res.body);
    }
  }

  Future<SetupTotpBegin> setupTotpBegin() async {
    final res = await _client.post(
      Uri.parse('/api/v1/admin/setup/totp/begin'),
      headers: _setupHeaders(),
    );
    _throwIfSetup(res);
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, _code(res), res.body);
    }
    final json = jsonDecode(res.body);
    if (json is! Map) {
      throw ApiException(500, 'bad_totp_begin', res.body);
    }
    final secret = _asString(json['secret']) ?? '';
    final otpauth = _asString(json['otpauth_url']) ?? '';
    if (secret.isEmpty || otpauth.isEmpty) {
      throw ApiException(500, 'bad_totp_begin', res.body);
    }
    return SetupTotpBegin(
      secret: secret,
      otpauthUrl: otpauth,
      totpEnrolled: json['totp_enrolled'] == true,
    );
  }

  Future<void> setupTotpConfirm(String code) async {
    final res = await _client.post(
      Uri.parse('/api/v1/admin/setup/totp/confirm'),
      headers: _setupHeaders(),
      body: jsonEncode({'code': code}),
    );
    _throwIfSetup(res);
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, _code(res), res.body);
    }
  }

  static String? readSetupToken() => web.window.sessionStorage.getItem(_setupTokenKey);

  static void saveSetupToken(String token) {
    web.window.sessionStorage.setItem(_setupTokenKey, token);
  }

  static void clearSetupToken() {
    web.window.sessionStorage.removeItem(_setupTokenKey);
  }

  Map<String, String> _headers() {
    final token = readToken() ?? '';
    return {
      'Authorization': 'Bearer $token',
      'Content-Type': 'application/json',
    };
  }

  Map<String, String> _setupHeaders() {
    final token = readSetupToken() ?? '';
    return {
      'Authorization': 'Bearer $token',
      'Content-Type': 'application/json',
    };
  }

  void _throwIfAuth(http.Response res) {
    if (res.statusCode == 401) {
      clearToken();
      throw ApiException(401, 'need_auth', res.body);
    }
  }

  void _throwIfSetup(http.Response res) {
    if (res.statusCode == 404) {
      clearSetupToken();
      throw ApiException(404, 'not_found', res.body);
    }
    if (res.statusCode == 401) {
      final code = _code(res);
      if (code == 'need_auth' || code == 'admin_disabled') {
        clearSetupToken();
      }
      throw ApiException(401, code, res.body);
    }
  }

  String _code(http.Response res) {
    try {
      final json = jsonDecode(res.body);
      if (json is Map) {
        return _asString(json['error']) ?? 'http_${res.statusCode}';
      }
    } catch (_) {}
    return 'http_${res.statusCode}';
  }

  int? _retryAfter(http.Response res) {
    try {
      final json = jsonDecode(res.body);
      if (json is Map && json['retry_after'] is num) {
        return (json['retry_after'] as num).ceil();
      }
    } catch (_) {}
    return null;
  }
}
