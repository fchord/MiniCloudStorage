import 'package:flutter/material.dart';
import 'package:minicloudstorage/api.dart';
import 'package:minicloudstorage/widgets/page_shell.dart';
import 'package:web/web.dart' as web;

class FilePage extends StatefulWidget {
  const FilePage({
    super.key,
    required this.code,
    this.initialPassword,
  });

  final String code;
  final String? initialPassword;

  @override
  State<FilePage> createState() => _FilePageState();
}

class _FilePageState extends State<FilePage> {
  final _api = ApiClient();
  final _passwordController = TextEditingController();
  FileMeta? _meta;
  String? _error;
  String? _banner;
  bool _loading = true;
  String? _acceptedPassword;

  @override
  void initState() {
    super.initState();
    if (widget.initialPassword != null) {
      _passwordController.text = widget.initialPassword!;
    }
    _load(widget.initialPassword);
  }

  @override
  void dispose() {
    _passwordController.dispose();
    super.dispose();
  }

  Future<void> _load(String? password) async {
    setState(() {
      _loading = true;
      _error = null;
      _banner = null;
    });
    try {
      final meta = await _api.getFile(widget.code, password: password);
      setState(() {
        _meta = meta;
        _acceptedPassword = password;
        _loading = false;
      });
    } on ApiException catch (e) {
      setState(() {
        _loading = false;
        _meta = null;
        if (e.code == 'need_password') {
          _error = null;
          _banner = '该文件需要密码才能查看和下载';
        } else if (e.code == 'bad_password') {
          _error = '密码错误';
          _banner = '打开页面时密码不正确，请重新输入后再下载';
          _acceptedPassword = null;
        } else if (e.code == 'expired' || e.code == 'not_found') {
          _error = '文件不存在或已过期删除';
        } else {
          _error = '加载失败（${e.code}）';
        }
      });
    } catch (e) {
      setState(() {
        _loading = false;
        _error = '加载失败：$e';
      });
    }
  }

  void _download() {
    if (_meta == null) {
      final typed = _passwordController.text.trim();
      if (typed.isEmpty) {
        setState(() => _banner = '请先输入密码');
        return;
      }
      _load(typed).then((_) {
        if (_meta != null) {
          _openDownload(_acceptedPassword);
        }
      });
      return;
    }
    _openDownload(_acceptedPassword);
  }

  void _openDownload(String? password) {
    final url = _api.downloadUrl(widget.code, password: password);
    web.window.open(url, '_self');
  }

  @override
  Widget build(BuildContext context) {
    final needPasswordForm = _meta == null &&
        (_banner != null || _error == '密码错误') &&
        _error != '文件不存在或已过期删除';

    return PageShell(
      title: '文件详情',
      subtitle: '短码 ${widget.code}',
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (_loading) const Center(child: CircularProgressIndicator()),
          if (!_loading && _banner != null)
            _InfoBanner(
              text: _banner!,
              error: _error == '密码错误',
            ),
          if (!_loading && _error != null && _error != '密码错误') ...[
            const SizedBox(height: 12),
            Text(_error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
          ],
          if (!_loading && needPasswordForm) ...[
            const SizedBox(height: 16),
            TextField(
              controller: _passwordController,
              keyboardType: TextInputType.number,
              maxLength: 4,
              obscureText: true,
              decoration: const InputDecoration(
                labelText: '四位数字密码',
                border: OutlineInputBorder(),
                counterText: '',
              ),
              onSubmitted: (v) => _load(v.trim()),
            ),
            const SizedBox(height: 12),
            FilledButton(
              onPressed: () => _load(_passwordController.text.trim()),
              child: const Text('确认密码'),
            ),
          ],
          if (_meta != null) ...[
            const SizedBox(height: 8),
            _MetaList(meta: _meta!),
            const SizedBox(height: 20),
            FilledButton.icon(
              onPressed: _download,
              icon: const Icon(Icons.download),
              label: const Text('下载文件'),
            ),
          ],
        ],
      ),
    );
  }
}

class _MetaList extends StatelessWidget {
  const _MetaList({required this.meta});

  final FileMeta meta;

  @override
  Widget build(BuildContext context) {
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          children: [
            _row('文件名', meta.filename),
            _row('大小', _formatBytes(meta.sizeBytes)),
            _row('上传时间', _formatTime(meta.createdAt.toLocal())),
            _row('自动删除', '${meta.daysRemaining} 天后（${_formatTime(meta.expiresAt.toLocal())}）'),
          ],
        ),
      ),
    );
  }

  Widget _row(String k, String v) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(width: 88, child: Text(k, style: const TextStyle(color: Colors.black54))),
          Expanded(child: SelectableText(v)),
        ],
      ),
    );
  }
}

class _InfoBanner extends StatelessWidget {
  const _InfoBanner({required this.text, required this.error});

  final String text;
  final bool error;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: error ? scheme.errorContainer : scheme.secondaryContainer,
        borderRadius: BorderRadius.circular(12),
      ),
      child: Text(text),
    );
  }
}

String _formatBytes(int n) {
  if (n < 1024) return '$n B';
  if (n < 1024 * 1024) return '${(n / 1024).toStringAsFixed(1)} KB';
  if (n < 1024 * 1024 * 1024) {
    return '${(n / (1024 * 1024)).toStringAsFixed(1)} MB';
  }
  return '${(n / (1024 * 1024 * 1024)).toStringAsFixed(2)} GB';
}

String _formatTime(DateTime t) {
  String two(int v) => v.toString().padLeft(2, '0');
  return '${t.year}-${two(t.month)}-${two(t.day)} ${two(t.hour)}:${two(t.minute)}';
}
