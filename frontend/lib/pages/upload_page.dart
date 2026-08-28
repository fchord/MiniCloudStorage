import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:file_picker/file_picker.dart';
import 'package:minicloudstorage/api.dart';
import 'package:minicloudstorage/widgets/page_shell.dart';

const maxSize = 1 << 30;

class UploadPage extends StatefulWidget {
  const UploadPage({super.key});

  @override
  State<UploadPage> createState() => _UploadPageState();
}

class _UploadPageState extends State<UploadPage> {
  final _api = ApiClient();
  PlatformFile? _file;
  bool _needPassword = false;
  bool _uploading = false;
  double _progress = 0;
  String? _status;
  String? _error;
  CompleteUploadResult? _done;

  Future<void> _pick() async {
    if (_uploading) return;
    final result = await FilePicker.platform.pickFiles(allowMultiple: false);
    if (result == null || result.files.isEmpty) return;
    final file = result.files.single;
    if (file.size <= 0 || file.size > maxSize) {
      setState(() {
        _error = '文件大小必须大于 0 且不超过 1GB';
        _file = null;
        _done = null;
      });
      return;
    }
    setState(() {
      _file = file;
      _error = null;
      _done = null;
      _progress = 0;
      _status = null;
    });
  }

  Future<void> _upload() async {
    final file = _file;
    if (file == null || _uploading) return;
    setState(() {
      _uploading = true;
      _error = null;
      _done = null;
      _progress = 0;
      _status = '正在创建上传任务…';
    });
    try {
      final init = await _api.initUpload(
        filename: file.name,
        size: file.size,
        contentType: file.extension == null
            ? 'application/octet-stream'
            : 'application/octet-stream',
      );
      final xfile = file.xFile;
      for (var i = 0; i < init.totalChunks; i++) {
        final start = i * init.chunkSize;
        final end = (start + init.chunkSize > file.size)
            ? file.size
            : start + init.chunkSize;
        setState(() {
          _status = '正在上传分片 ${i + 1}/${init.totalChunks}';
        });
        Object? lastErr;
        for (var attempt = 0; attempt < 3; attempt++) {
          try {
            final builder = BytesBuilder(copy: false);
            await for (final chunk in xfile.openRead(start, end)) {
              builder.add(chunk);
            }
            await _api.putChunk(
              uploadId: init.uploadId,
              index: i,
              bytes: builder.takeBytes(),
            );
            lastErr = null;
            break;
          } catch (e) {
            lastErr = e;
            await Future<void>.delayed(Duration(milliseconds: 400 * (attempt + 1)));
          }
        }
        if (lastErr != null) {
          throw lastErr;
        }
        setState(() {
          _progress = (i + 1) / init.totalChunks;
        });
      }
      setState(() => _status = '正在完成上传…');
      final done = await _api.complete(
        uploadId: init.uploadId,
        enablePassword: _needPassword,
      );
      setState(() {
        _done = done;
        _status = '上传成功';
        _progress = 1;
      });
    } catch (e) {
      setState(() {
        _error = e is ApiException ? _humanError(e) : '上传失败：$e';
      });
    } finally {
      if (mounted) {
        setState(() => _uploading = false);
      }
    }
  }

  String _humanError(ApiException e) {
    switch (e.code) {
      case 'invalid_size':
        return '文件大小不符合要求（最大 1GB）';
      case 'invalid_filename':
        return '文件名不合法';
      default:
        return '上传失败（${e.code}）';
    }
  }

  @override
  Widget build(BuildContext context) {
    return PageShell(
      title: 'MiniCloudStorage',
      subtitle: '文件保存 7 天，到期自动删除。一次选择一个文件，最大 1GB。',
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          _PickerCard(
            file: _file,
            enabled: !_uploading,
            onTap: _pick,
          ),
          const SizedBox(height: 16),
          SwitchListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('访问该文件需要密码'),
            subtitle: const Text('勾选后自动分配一个四位数字密码'),
            value: _needPassword,
            onChanged: _uploading
                ? null
                : (v) => setState(() => _needPassword = v),
          ),
          const SizedBox(height: 8),
          FilledButton.icon(
            onPressed: _file == null || _uploading ? null : _upload,
            icon: _uploading
                ? const SizedBox(
                    width: 16,
                    height: 16,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  )
                : const Icon(Icons.cloud_upload_outlined),
            label: Text(_uploading ? '上传中…' : '开始上传'),
          ),
          if (_uploading || _progress > 0) ...[
            const SizedBox(height: 16),
            LinearProgressIndicator(value: _uploading ? _progress : 1),
            if (_status != null)
              Padding(
                padding: const EdgeInsets.only(top: 8),
                child: Text(_status!, style: Theme.of(context).textTheme.bodySmall),
              ),
          ],
          if (_error != null) ...[
            const SizedBox(height: 16),
            _Banner(color: Theme.of(context).colorScheme.errorContainer, text: _error!),
          ],
          if (_done != null) ...[
            const SizedBox(height: 20),
            _SuccessCard(result: _done!),
          ],
        ],
      ),
    );
  }
}

class _PickerCard extends StatelessWidget {
  const _PickerCard({
    required this.file,
    required this.enabled,
    required this.onTap,
  });

  final PlatformFile? file;
  final bool enabled;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Material(
      color: scheme.surfaceContainerLowest,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(16),
        side: BorderSide(color: scheme.outlineVariant, width: 1.5),
      ),
      child: InkWell(
        onTap: enabled ? onTap : null,
        borderRadius: BorderRadius.circular(16),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 28),
          child: Column(
            children: [
              Icon(Icons.insert_drive_file_outlined, size: 40, color: scheme.primary),
              const SizedBox(height: 12),
              Text(
                file == null ? '点击选择一个文件' : file!.name,
                textAlign: TextAlign.center,
                style: Theme.of(context).textTheme.titleMedium,
              ),
              const SizedBox(height: 6),
              Text(
                file == null ? '最大 1GB，仅可选一个文件' : _formatBytes(file!.size),
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _SuccessCard extends StatelessWidget {
  const _SuccessCard({required this.result});

  final CompleteUploadResult result;

  @override
  Widget build(BuildContext context) {
    final share = result.urlWithPassword ?? result.url;
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('上传成功', style: Theme.of(context).textTheme.titleMedium),
            const SizedBox(height: 8),
            SelectableText(share),
            if (result.password != null) ...[
              const SizedBox(height: 8),
              Text('访问密码：${result.password}'),
            ],
            const SizedBox(height: 8),
            Text('将于 ${_formatTime(result.expiresAt.toLocal())} 自动删除'),
            const SizedBox(height: 12),
            Wrap(
              spacing: 8,
              children: [
                FilledButton.tonalIcon(
                  onPressed: () => _copy(context, share),
                  icon: const Icon(Icons.copy),
                  label: const Text('复制链接'),
                ),
                if (result.password != null)
                  OutlinedButton(
                    onPressed: () => _copy(context, result.password!),
                    child: const Text('复制密码'),
                  ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

class _Banner extends StatelessWidget {
  const _Banner({required this.color, required this.text});

  final Color color;
  final String text;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: color,
        borderRadius: BorderRadius.circular(12),
      ),
      child: Text(text),
    );
  }
}

Future<void> _copy(BuildContext context, String text) async {
  await Clipboard.setData(ClipboardData(text: text));
  if (context.mounted) {
    ScaffoldMessenger.of(context).showSnackBar(
      const SnackBar(content: Text('已复制')),
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
