import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:minicloudstorage/admin_api.dart';
import 'package:minicloudstorage/api.dart';
import 'package:minicloudstorage/app_fonts.dart';
import 'package:qr_flutter/qr_flutter.dart';

class AdminSetupPage extends StatefulWidget {
  const AdminSetupPage({super.key});

  @override
  State<AdminSetupPage> createState() => _AdminSetupPageState();
}

class _AdminSetupPageState extends State<AdminSetupPage> {
  final _api = AdminApi();
  final _password = TextEditingController();
  final _totp = TextEditingController();
  final _currentPassword = TextEditingController();
  final _newPassword = TextEditingController();
  final _confirmPassword = TextEditingController();
  final _enrollCode = TextEditingController();

  bool _loading = true;
  bool _intranetBlocked = false;
  bool _busy = false;
  bool _authed = false;
  bool _totpEnrolled = false;
  bool _statusFailed = false;
  String? _error;
  String? _notice;
  String? _pendingSecret;
  String? _pendingOtpauth;

  @override
  void initState() {
    super.initState();
    _bootstrap();
  }

  @override
  void dispose() {
    _password.dispose();
    _totp.dispose();
    _currentPassword.dispose();
    _newPassword.dispose();
    _confirmPassword.dispose();
    _enrollCode.dispose();
    super.dispose();
  }

  Future<void> _bootstrap() async {
    setState(() {
      _loading = true;
      _intranetBlocked = false;
      _statusFailed = false;
      _error = null;
    });
    try {
      final st = await _api.setupStatus();
      if (!mounted) return;
      setState(() {
        _totpEnrolled = st.totpEnrolled;
        _authed = false;
        _loading = false;
      });
    } on ApiException catch (e) {
      if (!mounted) return;
      setState(() {
        _loading = false;
        _intranetBlocked = e.status == 404;
        _statusFailed = e.status != 404;
        _error = _setupError(e);
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _loading = false;
        _statusFailed = true;
        _error = '无法连接维护接口：$e';
      });
    }
  }

  Future<void> _login() async {
    if (_busy) return;
    setState(() {
      _busy = true;
      _error = null;
      _notice = null;
    });
    try {
      final result = await _api.setupLogin(_password.text, totp: _totp.text.trim());
      _password.clear();
      _totp.clear();
      if (!mounted) return;
      setState(() {
        _authed = true;
        _totpEnrolled = result.totpEnrolled;
      });
    } on ApiException catch (e) {
      if (!mounted) return;
      if (e.status == 404) {
        setState(() {
          _intranetBlocked = true;
          _authed = false;
        });
      }
      setState(() => _error = _setupError(e));
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = '登录失败：$e');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _changePassword() async {
    if (_busy) return;
    if (_newPassword.text != _confirmPassword.text) {
      setState(() => _error = '两次输入的新口令不一致');
      return;
    }
    if (_newPassword.text.trim().isEmpty) {
      setState(() => _error = '新口令不能为空');
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
      _notice = null;
    });
    try {
      await _api.setupChangePassword(
        currentPassword: _currentPassword.text,
        newPassword: _newPassword.text,
      );
      _currentPassword.clear();
      _newPassword.clear();
      _confirmPassword.clear();
      if (!mounted) return;
      setState(() => _notice = '口令已更新');
    } on ApiException catch (e) {
      if (!mounted) return;
      _handleAuthLoss(e);
      setState(() => _error = _setupError(e));
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = '改口令失败：$e');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _beginTotp() async {
    if (_busy) return;
    setState(() {
      _busy = true;
      _error = null;
      _notice = null;
    });
    try {
      final began = await _api.setupTotpBegin();
      _enrollCode.clear();
      if (!mounted) return;
      setState(() {
        _pendingSecret = began.secret;
        _pendingOtpauth = began.otpauthUrl;
        _totpEnrolled = began.totpEnrolled;
      });
    } on ApiException catch (e) {
      if (!mounted) return;
      _handleAuthLoss(e);
      setState(() => _error = _setupError(e));
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = '无法生成动态码：$e');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _confirmTotp() async {
    if (_busy) return;
    setState(() {
      _busy = true;
      _error = null;
      _notice = null;
    });
    try {
      await _api.setupTotpConfirm(_enrollCode.text.trim());
      _enrollCode.clear();
      if (!mounted) return;
      setState(() {
        _pendingSecret = null;
        _pendingOtpauth = null;
        _totpEnrolled = true;
        _notice = '动态码已绑定。公网 /admin 现在可以用口令加动态码登录。';
      });
    } on ApiException catch (e) {
      if (!mounted) return;
      _handleAuthLoss(e);
      setState(() => _error = _setupError(e));
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = '绑定失败：$e');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  void _handleAuthLoss(ApiException e) {
    if (e.status == 404) {
      _intranetBlocked = true;
      _authed = false;
      return;
    }
    if (e.status == 401 && (e.code == 'need_auth' || e.code == 'admin_disabled')) {
      _authed = false;
    }
  }

  String _setupError(ApiException e) {
    if (e.status == 404 || e.code == 'not_found') {
      return '维护页仅可从内网访问，公网无法使用这些接口。请打开 http://192.168.43.111:30987/admin/setup';
    }
    if (e.code == 'bad_password' || e.code == 'bad_totp') return '口令或动态码错误';
    if (e.code == 'need_auth') return '请重新登录维护页';
    if (e.code == 'no_pending') return '请先生成新的动态码再确认';
    if (e.code == 'empty_password') return '新口令不能为空';
    if (e.code == 'locked') return '尝试过多，请稍后再试';
    return '请求失败（${e.code}）';
  }

  String _groupedSecret(String secret) {
    final buf = StringBuffer();
    for (var i = 0; i < secret.length; i++) {
      if (i > 0 && i % 4 == 0) buf.write(' ');
      buf.write(secret[i]);
    }
    return buf.toString();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('维护设定', style: kNotoTextStyle),
        actions: [
          if (_authed)
            TextButton(
              onPressed: () {
                AdminApi.clearSetupToken();
                setState(() {
                  _authed = false;
                  _pendingSecret = null;
                  _pendingOtpauth = null;
                  _notice = null;
                  _error = null;
                });
              },
              child: const Text('退出'),
            ),
        ],
      ),
      body: _body(),
    );
  }

  Widget _body() {
    if (_loading) {
      return const Center(child: CircularProgressIndicator());
    }
    if (_intranetBlocked) {
      return _messagePane(
        '维护页仅可从内网访问。\n请在局域网打开 http://192.168.43.111:30987/admin/setup',
      );
    }
    if (_statusFailed) {
      return Center(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Text(_error ?? '无法读取维护接口'),
              const SizedBox(height: 16),
              FilledButton(onPressed: _bootstrap, child: const Text('重试')),
            ],
          ),
        ),
      );
    }
    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 480),
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(24),
          child: _authed ? _settingsBody() : _loginBody(),
        ),
      ),
    );
  }

  Widget _messagePane(String text) {
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(24),
        child: Text(text, textAlign: TextAlign.center),
      ),
    );
  }

  Widget _loginBody() {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text('用现有管理员口令进入。若已绑定动态码，还需填写一枚 6 位码。', style: Theme.of(context).textTheme.bodyMedium),
        const SizedBox(height: 16),
        TextField(
          controller: _password,
          obscureText: true,
          enabled: !_busy,
          onSubmitted: (_) {
            if (!_busy) _login();
          },
          decoration: const InputDecoration(
            labelText: '口令',
            border: OutlineInputBorder(),
          ),
        ),
        if (_totpEnrolled) ...[
          const SizedBox(height: 12),
          TextField(
            controller: _totp,
            enabled: !_busy,
            keyboardType: TextInputType.number,
            inputFormatters: [
              FilteringTextInputFormatter.digitsOnly,
              LengthLimitingTextInputFormatter(6),
            ],
            onSubmitted: (_) {
              if (!_busy) _login();
            },
            decoration: const InputDecoration(
              labelText: '动态码',
              border: OutlineInputBorder(),
              counterText: '',
            ),
          ),
        ],
        if (_error != null) ...[
          const SizedBox(height: 12),
          Text(_error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
        ],
        const SizedBox(height: 16),
        FilledButton(
          onPressed: _busy ? null : _login,
          child: Text(_busy ? '验证中…' : '进入'),
        ),
      ],
    );
  }

  Widget _settingsBody() {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (_notice != null) ...[
          Text(_notice!, style: TextStyle(color: Theme.of(context).colorScheme.primary)),
          const SizedBox(height: 16),
        ],
        if (_error != null) ...[
          Text(_error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
          const SizedBox(height: 16),
        ],
        Text('修改口令', style: Theme.of(context).textTheme.titleMedium),
        const SizedBox(height: 12),
        TextField(
          controller: _currentPassword,
          obscureText: true,
          enabled: !_busy,
          decoration: const InputDecoration(
            labelText: '当前口令',
            border: OutlineInputBorder(),
          ),
        ),
        const SizedBox(height: 12),
        TextField(
          controller: _newPassword,
          obscureText: true,
          enabled: !_busy,
          decoration: const InputDecoration(
            labelText: '新口令',
            border: OutlineInputBorder(),
          ),
        ),
        const SizedBox(height: 12),
        TextField(
          controller: _confirmPassword,
          obscureText: true,
          enabled: !_busy,
          decoration: const InputDecoration(
            labelText: '确认新口令',
            border: OutlineInputBorder(),
          ),
        ),
        const SizedBox(height: 12),
        FilledButton(
          onPressed: _busy ? null : _changePassword,
          child: const Text('更新口令'),
        ),
        const SizedBox(height: 32),
        Text(
          _totpEnrolled ? '重新绑定动态码' : '绑定动态码',
          style: Theme.of(context).textTheme.titleMedium,
        ),
        const SizedBox(height: 8),
        Text(
          _totpEnrolled
              ? '用 Microsoft Authenticator 扫新二维码，提交新动态码后旧密钥立即作废。'
              : '首次绑定后，公网 /admin 才允许登录。用 Microsoft Authenticator 扫描下方二维码。',
        ),
        const SizedBox(height: 12),
        OutlinedButton(
          onPressed: _busy ? null : _beginTotp,
          child: Text(_pendingOtpauth == null ? '生成绑定二维码' : '重新生成二维码'),
        ),
        if (_pendingOtpauth != null && _pendingSecret != null) ...[
          const SizedBox(height: 16),
          Center(
            child: ColoredBox(
              color: Colors.white,
              child: Padding(
                padding: const EdgeInsets.all(12),
                child: QrImageView(
                  data: _pendingOtpauth!,
                  size: 220,
                  backgroundColor: Colors.white,
                ),
              ),
            ),
          ),
          const SizedBox(height: 12),
          const Text('无法扫码时，在 Authenticator 中手动输入密钥：'),
          const SizedBox(height: 8),
          SelectableText(
            _groupedSecret(_pendingSecret!),
            style: const TextStyle(fontFamily: 'monospace', letterSpacing: 1.2),
          ),
          const SizedBox(height: 12),
          TextField(
            controller: _enrollCode,
            enabled: !_busy,
            keyboardType: TextInputType.number,
            inputFormatters: [
              FilteringTextInputFormatter.digitsOnly,
              LengthLimitingTextInputFormatter(6),
            ],
            decoration: const InputDecoration(
              labelText: 'Authenticator 中的 6 位动态码',
              border: OutlineInputBorder(),
              counterText: '',
            ),
          ),
          const SizedBox(height: 12),
          FilledButton(
            onPressed: _busy ? null : _confirmTotp,
            child: const Text('确认绑定'),
          ),
        ],
      ],
    );
  }
}
