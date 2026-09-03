import 'dart:async';
import 'dart:convert';
import 'dart:js_interop';
import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/scheduler.dart';
import 'package:flutter/services.dart';
import 'package:minicloudstorage/api.dart';
import 'package:minicloudstorage/app_fonts.dart';
import 'package:minicloudstorage/chunk_put.dart';
import 'package:minicloudstorage/picked_file.dart';
import 'package:minicloudstorage/widgets/chunk_progress_bar.dart';
import 'package:minicloudstorage/widgets/page_shell.dart';
import 'package:web/web.dart' as web;

const maxSize = 1 << 30;
const kUploadParallelism = 4;
const kChunkRetries = 3;

class UploadPage extends StatefulWidget {
  const UploadPage({super.key});

  @override
  State<UploadPage> createState() => _UploadPageState();
}

class _UploadProgress extends ChangeNotifier {
  _UploadProgress({
    required this.uploadId,
    required this.fileSize,
    required this.chunkSize,
    required this.totalChunks,
  }) : sizes = List<int>.generate(totalChunks, (i) {
         final start = i * chunkSize;
         final end = math.min(start + chunkSize, fileSize);
         return end - start;
       }),
       phases = List<ChunkPhase>.filled(totalChunks, ChunkPhase.pending);

  final String uploadId;
  final int fileSize;
  final int chunkSize;
  final int totalChunks;
  final List<int> sizes;
  final List<ChunkPhase> phases;
  bool merging = false;
  int serverReceived = 0;

  int get uploadedBytes {
    var n = 0;
    for (var i = 0; i < sizes.length; i++) {
      if (phases[i] == ChunkPhase.success) n += sizes[i];
    }
    return n;
  }

  double get ratio => fileSize == 0 ? 0 : uploadedBytes / fileSize;

  int get successCount =>
      phases.where((p) => p == ChunkPhase.success).length;

  List<int> get failedIndices {
    final out = <int>[];
    for (var i = 0; i < phases.length; i++) {
      if (phases[i] == ChunkPhase.failed) out.add(i);
    }
    return out;
  }

  bool get allSuccess =>
      phases.isNotEmpty && phases.every((p) => p == ChunkPhase.success);

  void setPhase(int i, ChunkPhase p) {
    if (phases[i] == p) return;
    phases[i] = p;
    notifyListeners();
  }

  void setMerging(bool v) {
    if (merging == v) return;
    merging = v;
    notifyListeners();
  }

  void applyServerReceived(int n, List<bool> flags) {
    var changed = n != serverReceived;
    if (changed) serverReceived = n;
    for (var i = 0; i < flags.length && i < phases.length; i++) {
      if (flags[i] && phases[i] != ChunkPhase.success) {
        phases[i] = ChunkPhase.success;
        changed = true;
      }
    }
    if (changed) notifyListeners();
  }
}

class _UploadPageState extends State<UploadPage> {
  final _api = ApiClient();
  final _bar = ValueNotifier<_UploadProgress?>(null);
  final _status = ValueNotifier<String?>(null);
  final _uploading = ValueNotifier<bool>(false);
  final _error = ValueNotifier<String?>(null);
  final _done = ValueNotifier<CompleteUploadResult?>(null);
  final _spinner = GlobalKey(debugLabel: 'upload-spinner');
  PickedLocalFile? _file;
  bool _needPassword = false;
  InitUploadResult? _init;
  bool _completing = false;
  bool _poolRunning = false;
  bool _cancelled = false;
  bool _cancelSent = false;
  web.EventListener? _pageHideListener;
  final Set<int> _inFlight = {};

  @override
  void initState() {
    super.initState();
    _pageHideListener = ((web.Event _) {
      _beaconCancel();
    }).toJS;
    web.window.addEventListener('pagehide', _pageHideListener!);
  }

  @override
  void dispose() {
    if (_pageHideListener != null) {
      web.window.removeEventListener('pagehide', _pageHideListener!);
    }
    _bar.value?.dispose();
    _bar.dispose();
    _status.dispose();
    _uploading.dispose();
    _error.dispose();
    _done.dispose();
    super.dispose();
  }

  Future<void> _pick() async {
    if (_uploading.value) return;
    final file = await pickLocalFile();
    if (file == null) return;
    if (file.size <= 0 || file.size > maxSize) {
      setState(() => _file = null);
      _error.value = '文件大小必须大于 0 且不超过 1GB';
      _done.value = null;
      _resetBar();
      _status.value = null;
      return;
    }
    setState(() => _file = file);
    _error.value = null;
    _done.value = null;
    _resetBar();
    _status.value = null;
  }

  void _resetBar() {
    _bar.value?.dispose();
    _bar.value = null;
  }

  Future<void> _upload() async {
    final file = _file;
    if (file == null || _uploading.value) return;
    _error.value = null;
    _done.value = null;
    _completing = false;
    _cancelled = false;
    _cancelSent = false;
    _inFlight.clear();
    _resetBar();
    _status.value = '正在创建上传任务…';
    _uploading.value = true;
    await SchedulerBinding.instance.endOfFrame;
    try {
      final init = await _api.initUpload(
        filename: file.name,
        size: file.size,
        contentType: 'application/octet-stream',
        enablePassword: _needPassword,
      );
      _init = init;
      final progress = _UploadProgress(
        uploadId: init.uploadId,
        fileSize: file.size,
        chunkSize: init.chunkSize,
        totalChunks: init.totalChunks,
      );
      _resetBar();
      _bar.value = progress;
      _refreshStatus(progress);
      unawaited(_pollServerProgress());
      await _runPool(
        List<int>.generate(init.totalChunks, (i) => i),
      );
      if (_cancelled) return;
      await _finishIfReady();
    } catch (e) {
      _error.value = _fmtErr(e);
      _uploading.value = false;
      if (_status.value != null && _bar.value == null) {
        _status.value = null;
      }
    }
  }

  Future<void> _runPool(List<int> indices) async {
    if (indices.isEmpty) return;
    _poolRunning = true;
    try {
      final n = math.min(kUploadParallelism, indices.length);
      var cursor = 0;
      Future<void> worker() async {
        while (true) {
          if (_cancelled) return;
          final pos = cursor++;
          if (pos >= indices.length) return;
          await _putOne(indices[pos]);
        }
      }
      await Future.wait(List<Future<void>>.generate(n, (_) => worker()));
    } finally {
      _poolRunning = false;
    }
  }

  Future<void> _putOne(int index) async {
    final init = _init;
    final file = _file;
    final progress = _bar.value;
    if (init == null || file == null || progress == null) return;
    if (_cancelled) return;
    if (_inFlight.contains(index)) return;
    if (progress.phases[index] == ChunkPhase.success) return;
    _inFlight.add(index);
    progress.setPhase(index, ChunkPhase.uploading);
    _refreshStatus(progress);
    try {
      final start = index * init.chunkSize;
      final end = math.min(start + init.chunkSize, file.size);
      Object? lastErr;
      for (var attempt = 0; attempt <= kChunkRetries; attempt++) {
        try {
          await Future<void>.delayed(Duration.zero);
          await SchedulerBinding.instance.endOfFrame;
          await _api.putChunkBlob(
            uploadId: init.uploadId,
            index: index,
            blob: file.file.slice(start, end),
          );
          lastErr = null;
          break;
        } catch (e) {
          lastErr = e;
          if (attempt < kChunkRetries) {
            await Future<void>.delayed(
              Duration(milliseconds: 400 * (attempt + 1)),
            );
          }
        }
      }
      if (lastErr != null) {
        if (_cancelled) return;
        progress.setPhase(index, ChunkPhase.failed);
      } else {
        progress.setPhase(index, ChunkPhase.success);
      }
    } finally {
      _inFlight.remove(index);
      _refreshStatus(progress);
    }
  }

  void _refreshStatus(_UploadProgress progress) {
    if (progress.merging) {
      _status.value = '正在合并文件，大文件可能需要几分钟…';
      return;
    }
    final failed = progress.failedIndices.length;
    final up = progress.phases.where((p) => p == ChunkPhase.uploading).length;
    if (failed > 0 && up == 0 && !progress.allSuccess) {
      _status.value = '有 $failed 个分片失败，点击红色分段重试';
      return;
    }
    final n = math.min(kUploadParallelism, progress.totalChunks);
    final shown = math.max(progress.successCount, progress.serverReceived);
    _status.value =
        '正在并行上传（$n 路）… $shown/${progress.totalChunks}';
  }

  Future<void> _pollServerProgress() async {
    while (_uploading.value && !_cancelled && !_completing) {
      await Future<void>.delayed(const Duration(seconds: 2));
      if (!_uploading.value || _cancelled || _completing) return;
      final init = _init;
      final progress = _bar.value;
      if (init == null || progress == null) continue;
      try {
        final st = await _api.getUpload(init.uploadId);
        if (!mounted) return;
        if (st.status == 'merging') {
          progress.setMerging(true);
        }
        progress.applyServerReceived(st.receivedChunks, st.receivedFlags);
        _refreshStatus(progress);
      } catch (_) {}
    }
  }

  Future<void> _retryChunk(int index) async {
    if (_poolRunning || _completing) return;
    final progress = _bar.value;
    if (progress == null) return;
    if (progress.phases[index] != ChunkPhase.failed) return;
    _error.value = null;
    await _putOne(index);
    await _finishIfReady();
  }

  Future<void> _retryFailed() async {
    if (_poolRunning || _completing) return;
    final progress = _bar.value;
    if (progress == null) return;
    final failed = progress.failedIndices;
    if (failed.isEmpty) return;
    _error.value = null;
    await _runPool(failed);
    await _finishIfReady();
  }

  Future<void> _cancel({String reason = 'user_cancel'}) async {
    if (_cancelSent || _done.value != null) return;
    _cancelSent = true;
    final merging = _completing || (_bar.value?.merging ?? false);
    if (!merging) {
      _cancelled = true;
      ChunkPutClient.instance.abortAll();
    }
    final init = _init;
    if (init != null) {
      try {
        final result = await _api.cancelUpload(
          uploadId: init.uploadId,
          reason: reason,
        );
        if (_done.value != null) return;
        if (result.alreadyCompleted) {
          if (result.completed != null) {
            _status.value = '上传成功';
            _done.value = result.completed;
          } else {
            try {
              final st = await _api.getUpload(init.uploadId);
              if (st.isCompleted) {
                _status.value = '上传成功';
                _done.value = st.toCompleteResult();
              }
            } catch (_) {}
          }
          _uploading.value = false;
          _completing = false;
          return;
        }
      } catch (_) {}
    }
    if (_done.value != null) return;
    _cancelled = true;
    _uploading.value = false;
    _completing = false;
    _bar.value?.setMerging(false);
    _status.value = reason == 'page_close' ? '已取消（离开页面）' : '已取消上传';
  }

  void _beaconCancel() {
    if (_completing || (_bar.value?.merging ?? false)) return;
    if (_cancelSent || _done.value != null) return;
    if (!_uploading.value && _bar.value == null) return;
    final init = _init;
    if (init == null) return;
    _cancelled = true;
    _cancelSent = true;
    ChunkPutClient.instance.abortAll();
    final url = '/api/v1/uploads/${init.uploadId}/cancel?reason=page_close';
    final payload = jsonEncode({'reason_code': 'page_close'});
    var sent = false;
    try {
      final blob = web.Blob(
        [payload.toJS].toJS,
        web.BlobPropertyBag(type: 'application/json'),
      );
      sent = web.window.navigator.sendBeacon(url, blob);
    } catch (_) {}
    if (!sent) {
      try {
        final headers = web.Headers();
        headers.set('Content-Type', 'application/json');
        web.window.fetch(
          url.toJS,
          web.RequestInit(
            method: 'POST',
            body: payload.toJS,
            keepalive: true,
            headers: headers,
          ),
        );
      } catch (_) {}
    }
  }

  Future<void> _finishIfReady() async {
    final progress = _bar.value;
    final init = _init;
    if (progress == null || init == null) return;
    if (_cancelled) return;
    if (!progress.allSuccess) return;
    if (_completing) return;
    _completing = true;
    progress.setMerging(true);
    _status.value = '正在合并文件，大文件可能需要几分钟…';
    try {
      final done = await _api.complete(
        uploadId: init.uploadId,
        enablePassword: _needPassword,
        shouldStop: () => _cancelled,
      );
      if (_cancelled && _done.value == null) return;
      _status.value = '上传成功';
      _done.value = done;
      if (mounted) _uploading.value = false;
    } catch (e) {
      await _onCompleteError(progress, init, e);
    } finally {
      _completing = false;
    }
  }

  Future<void> _onCompleteError(
    _UploadProgress progress,
    InitUploadResult init,
    Object e,
  ) async {
    if (_done.value != null) return;
    if (_cancelled || (e is ApiException && _isUserCancel(e))) {
      _status.value = '已取消上传';
      if (mounted) _uploading.value = false;
      return;
    }
    if (e is ApiException && _isConfirmedMergeFail(e)) {
      progress.setMerging(false);
      _error.value = _humanError(e);
      _status.value = '分片已全部上传，合并失败，上传已终止';
      if (mounted) _uploading.value = false;
      return;
    }
    if (e is ApiException && e.code == 'merge_timeout') {
      _error.value = _humanError(e);
      _status.value = '合并仍在进行，请保持本页打开或到管理页确认';
      return;
    }
    // TypeError / parse glitch: GET 仍是 merging 时继续轮询，不要当成合并失败。
    _status.value = '正在合并文件，大文件可能需要几分钟…';
    progress.setMerging(true);
    try {
      final done = await _api.waitUntilComplete(
        init.uploadId,
        shouldStop: () => _cancelled,
      );
      if (_cancelled && _done.value == null) return;
      _status.value = '上传成功';
      _done.value = done;
      if (mounted) _uploading.value = false;
    } catch (e2) {
      if (_done.value != null) return;
      if (_cancelled || (e2 is ApiException && _isUserCancel(e2))) {
        _status.value = '已取消上传';
        if (mounted) _uploading.value = false;
        return;
      }
      if (e2 is ApiException && _isConfirmedMergeFail(e2)) {
        progress.setMerging(false);
        _error.value = _humanError(e2);
        _status.value = '分片已全部上传，合并失败，上传已终止';
        if (mounted) _uploading.value = false;
        return;
      }
      if (e2 is ApiException && e2.code == 'merge_timeout') {
        _error.value = _humanError(e2);
        _status.value = '合并仍在进行，请保持本页打开或到管理页确认';
        return;
      }
      _error.value = null;
      _status.value = '正在合并文件，大文件可能需要几分钟…';
    }
  }

  String _fmtErr(Object e) {
    if (e is ApiException) return _humanError(e);
    return '上传失败：${e.toString()}';
  }

  bool _isUserCancel(ApiException e) =>
      e.code == 'user_cancel' ||
      e.code == 'page_close' ||
      e.code == 'cancelled';

  bool _isConfirmedMergeFail(ApiException e) {
    switch (e.code) {
      case 'complete_failed':
      case 'already_failed':
      case 'storage_error':
      case 'failed':
      case 'merge_failed':
        return true;
      default:
        return false;
    }
  }

  String _humanError(ApiException e) {
    switch (e.code) {
      case 'invalid_size':
        return '文件大小不符合要求（最大 1GB）';
      case 'invalid_filename':
        return '文件名不合法';
      case 'missing_chunks':
        return '还有分片未到齐，请重试失败分段';
      case 'merge_timeout':
        return '合并耗时过长，请到管理页确认是否已成功';
      case 'user_cancel':
      case 'page_close':
        return '已取消上传';
      case 'complete_failed':
      case 'already_failed':
      case 'storage_error':
      case 'failed':
      case 'merge_failed':
        return '合并失败，请稍后重试或到管理页确认';
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
          ValueListenableBuilder<bool>(
            valueListenable: _uploading,
            builder: (context, uploading, _) {
              return _PickerCard(
                file: _file,
                enabled: !uploading,
                onTap: _pick,
              );
            },
          ),
          const SizedBox(height: 16),
          ValueListenableBuilder<bool>(
            valueListenable: _uploading,
            builder: (context, uploading, _) {
              return SwitchListTile(
                contentPadding: EdgeInsets.zero,
                title: const Text('访问该文件需要密码'),
                subtitle: const Text('勾选后自动分配一个四位数字密码'),
                value: _needPassword,
                onChanged: uploading
                    ? null
                    : (v) => setState(() => _needPassword = v),
              );
            },
          ),
          const SizedBox(height: 8),
          ValueListenableBuilder<bool>(
            valueListenable: _uploading,
            builder: (context, uploading, spinner) {
              return Row(
                children: [
                  Expanded(
                    child: _UploadCta(
                      uploading: uploading,
                      enabled: _file != null,
                      onPressed: _upload,
                      spinner: spinner!,
                    ),
                  ),
                  if (uploading) ...[
                    const SizedBox(width: 12),
                    OutlinedButton(
                      onPressed: () => _cancel(),
                      child: const Text('取消'),
                    ),
                  ],
                ],
              );
            },
            child: _PersistentSpinner(key: _spinner),
          ),
          ValueListenableBuilder<bool>(
            valueListenable: _uploading,
            builder: (context, uploading, _) {
              return ValueListenableBuilder<_UploadProgress?>(
                valueListenable: _bar,
                builder: (context, progress, _) {
                  return _ChunkUploadStatus(
                    progress: progress,
                    uploading: uploading,
                    status: _status,
                    onRetryFailed: _retryFailed,
                    onRetryChunk: _retryChunk,
                  );
                },
              );
            },
          ),
          ValueListenableBuilder<String?>(
            valueListenable: _error,
            builder: (context, error, _) {
              if (error == null) return const SizedBox.shrink();
              return Padding(
                padding: const EdgeInsets.only(top: 16),
                child: _Banner(
                  color: Theme.of(context).colorScheme.errorContainer,
                  text: error,
                ),
              );
            },
          ),
          ValueListenableBuilder<CompleteUploadResult?>(
            valueListenable: _done,
            builder: (context, done, _) {
              if (done == null) return const SizedBox.shrink();
              return Padding(
                padding: const EdgeInsets.only(top: 20),
                child: _SuccessCard(result: done),
              );
            },
          ),
        ],
      ),
    );
  }
}

/// Spinner lives for the lifetime of the page. Parent progress updates must
/// not replace this State (new key / new AnimationController looks like a hitch).
class _PersistentSpinner extends StatelessWidget {
  const _PersistentSpinner({super.key});

  @override
  Widget build(BuildContext context) {
    return const SizedBox(
      width: 18,
      height: 18,
      child: RepaintBoundary(
        child: CircularProgressIndicator(strokeWidth: 2),
      ),
    );
  }
}

class _UploadCta extends StatelessWidget {
  const _UploadCta({
    required this.uploading,
    required this.enabled,
    required this.onPressed,
    required this.spinner,
  });

  final bool uploading;
  final bool enabled;
  final VoidCallback onPressed;
  final Widget spinner;

  @override
  Widget build(BuildContext context) {
    return FilledButton(
      onPressed: !enabled || uploading ? null : onPressed,
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          SizedBox(
            width: 18,
            height: 18,
            child: Stack(
              alignment: Alignment.center,
              children: [
                TickerMode(
                  enabled: uploading,
                  child: Opacity(opacity: uploading ? 1 : 0, child: spinner),
                ),
                Opacity(
                  opacity: uploading ? 0 : 1,
                  child: const Icon(Icons.cloud_upload_outlined, size: 18),
                ),
              ],
            ),
          ),
          const SizedBox(width: 8),
          Text(uploading ? '上传中…' : '开始上传'),
        ],
      ),
    );
  }
}

class _ChunkUploadStatus extends StatelessWidget {
  const _ChunkUploadStatus({
    required this.progress,
    required this.uploading,
    required this.status,
    required this.onRetryFailed,
    required this.onRetryChunk,
  });

  final _UploadProgress? progress;
  final bool uploading;
  final ValueNotifier<String?> status;
  final VoidCallback onRetryFailed;
  final ValueChanged<int> onRetryChunk;

  static Widget _barRow({
    required BuildContext context,
    required List<int> sizes,
    required List<ChunkPhase> phases,
    required bool merging,
    required int pct,
    required String barKey,
    ValueChanged<int>? onRetryChunk,
  }) {
    return Row(
      children: [
        Expanded(
          child: ChunkProgressBar(
            key: ValueKey(barKey),
            sizes: sizes,
            phases: phases,
            merging: merging,
            onRetryChunk: onRetryChunk,
          ),
        ),
        const SizedBox(width: 10),
        SizedBox(
          width: 40,
          child: Text(
            '$pct%',
            textAlign: TextAlign.right,
            style: Theme.of(context).textTheme.bodySmall,
          ),
        ),
      ],
    );
  }

  @override
  Widget build(BuildContext context) {
    final p = progress;
    if (p == null) {
      return ValueListenableBuilder<String?>(
        valueListenable: status,
        builder: (context, text, _) {
          if (!uploading && text == null) return const SizedBox.shrink();
          return Padding(
            padding: const EdgeInsets.only(top: 16),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                _barRow(
                  context: context,
                  sizes: const [1],
                  phases: const [ChunkPhase.pending],
                  merging: false,
                  pct: 0,
                  barKey: 'awaiting-init',
                ),
                if (text != null)
                  Padding(
                    padding: const EdgeInsets.only(top: 8),
                    child: Text(
                      text,
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                  ),
              ],
            ),
          );
        },
      );
    }
    return ListenableBuilder(
      listenable: p,
      builder: (context, _) {
        final pct = p.merging ? 100 : (p.ratio * 100).floor();
        final failed = p.failedIndices;
        final busy = p.phases.contains(ChunkPhase.uploading);
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            const SizedBox(height: 16),
            _barRow(
              context: context,
              sizes: p.sizes,
              phases: List<ChunkPhase>.from(p.phases),
              merging: p.merging,
              pct: pct,
              barKey: p.uploadId,
              onRetryChunk: busy ? null : onRetryChunk,
            ),
            ValueListenableBuilder<String?>(
              valueListenable: status,
              builder: (context, text, _) {
                if (text == null) return const SizedBox.shrink();
                return Padding(
                  padding: const EdgeInsets.only(top: 8),
                  child: Text(text, style: Theme.of(context).textTheme.bodySmall),
                );
              },
            ),
            if (failed.isNotEmpty && !p.merging && !busy)
              Align(
                alignment: Alignment.centerLeft,
                child: TextButton(
                  onPressed: onRetryFailed,
                  child: const Text('重试失败分片'),
                ),
              ),
          ],
        );
      },
    );
  }
}

class _PickerCard extends StatelessWidget {
  const _PickerCard({
    required this.file,
    required this.enabled,
    required this.onTap,
  });

  final PickedLocalFile? file;
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
                style: Theme.of(context).textTheme.titleMedium?.merge(kNotoTextStyle),
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
