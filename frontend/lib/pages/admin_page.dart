import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:minicloudstorage/admin_api.dart';
import 'package:minicloudstorage/api.dart';
import 'package:minicloudstorage/app_fonts.dart';

class AdminPage extends StatefulWidget {
  const AdminPage({super.key});

  @override
  State<AdminPage> createState() => _AdminPageState();
}

class _AdminPageState extends State<AdminPage> with SingleTickerProviderStateMixin {
  static const _tabs = [
    ('in_progress', '进行中'),
    ('completed', '已完成'),
    ('cancelled', '已取消'),
    ('other_failed', '失败'),
    ('expired', '过期'),
  ];

  final _api = AdminApi();
  final _password = TextEditingController();
  final _totp = TextEditingController();
  late final TabController _tab;
  bool _authed = false;
  bool _busy = false;
  bool _checkingLock = true;
  bool _statusFailed = false;
  bool _totpEnrolled = false;
  int _retryAfter = 0;
  Timer? _lockTimer;
  String? _error;
  List<AdminRecord> _items = [];

  bool get _locked => _retryAfter > 0;
  bool get _loginDisabled => _busy || _locked || _checkingLock;

  @override
  void initState() {
    super.initState();
    _tab = TabController(length: _tabs.length, vsync: this);
    _tab.addListener(() {
      if (_tab.indexIsChanging || !_authed) return;
      _load();
    });
    _authed = (AdminApi.readToken() ?? '').isNotEmpty;
    if (_authed) {
      _checkingLock = false;
      _load();
    } else {
      _refreshLockStatus();
    }
  }

  @override
  void dispose() {
    _lockTimer?.cancel();
    _tab.dispose();
    _password.dispose();
    _totp.dispose();
    super.dispose();
  }

  Future<void> _refreshLockStatus() async {
    try {
      final st = await _api.loginLockStatus();
      if (!mounted) return;
      setState(() {
        _totpEnrolled = st.totpEnrolled;
        _statusFailed = false;
      });
      if (st.totpEnrolled && st.locked && st.retryAfter > 0) {
        _startLockCountdown(st.retryAfter);
      }
    } catch (_) {
      if (!mounted) return;
      setState(() {
        _statusFailed = true;
        _totpEnrolled = false;
      });
    } finally {
      if (mounted) setState(() => _checkingLock = false);
    }
  }

  void _startLockCountdown(int seconds) {
    _lockTimer?.cancel();
    if (seconds <= 0) {
      setState(() {
        _retryAfter = 0;
        _error = null;
      });
      return;
    }
    setState(() {
      _retryAfter = seconds;
      _error = '请于 $_retryAfter 秒后再试';
    });
    _lockTimer = Timer.periodic(const Duration(seconds: 1), (t) {
      if (!mounted) {
        t.cancel();
        return;
      }
      setState(() {
        _retryAfter--;
        if (_retryAfter <= 0) {
          t.cancel();
          _lockTimer = null;
          _retryAfter = 0;
          _error = null;
        } else {
          _error = '请于 $_retryAfter 秒后再试';
        }
      });
    });
  }

  Future<void> _login() async {
    if (_loginDisabled) return;
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await _api.login(_password.text, _totp.text.trim());
      _password.clear();
      _totp.clear();
      _lockTimer?.cancel();
      _lockTimer = null;
      setState(() {
        _authed = true;
        _retryAfter = 0;
      });
      await _load();
    } on ApiException catch (e) {
      final locked = e.status == 429 || e.code == 'locked';
      if (locked) {
        final n = e.retryAfter ?? 0;
        if (n > 0) {
          _startLockCountdown(n);
        } else if (mounted) {
          setState(() => _error = '请稍后再试');
        }
      } else if (mounted) {
        setState(() => _error = _loginError(e));
        await _refreshLockStatus();
      }
    } catch (e) {
      if (mounted) setState(() => _error = '登录失败：$e');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  String _loginError(ApiException e) {
    if (e.code == 'totp_not_enrolled') return '请完善登录安全设定再登录。';
    if (e.code == 'bad_password' || e.code == 'bad_totp') return '口令或动态码错误';
    return '登录失败（${e.code}）';
  }

  Future<void> _load() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final items = await _api.list(_tabs[_tab.index].$1);
      setState(() => _items = items);
    } on ApiException catch (e) {
      if (e.status == 401) {
        setState(() {
          _authed = false;
          _items = [];
          _error = '请重新登录';
          _checkingLock = true;
        });
        await _refreshLockStatus();
      } else {
        setState(() => _error = '加载失败（${e.code}）');
      }
    } catch (e) {
      setState(() => _error = '加载失败：$e');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _delete(AdminRecord row) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('删除这条记录？'),
        content: Text('将删除「${row.filename}」的文件（如仍在）和数据库行，无法恢复。'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('返回')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('删除')),
        ],
      ),
    );
    if (ok != true) return;
    try {
      await _api.deleteRecord(row.id);
      await _load();
    } on ApiException catch (e) {
      if (!mounted) return;
      setState(() => _error = '删除失败（${e.code}）');
    }
  }

  Future<void> _clearPassword(AdminRecord row) async {
    try {
      await _api.clearPassword(row.id);
      await _load();
    } on ApiException catch (e) {
      if (!mounted) return;
      setState(() => _error = '取消密码失败（${e.code}）');
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('管理后台', style: kNotoTextStyle),
        bottom: _authed
            ? TabBar(
                controller: _tab,
                isScrollable: true,
                tabs: [for (final t in _tabs) Tab(text: t.$2)],
              )
            : null,
        actions: [
          if (_authed)
            TextButton(
              onPressed: () {
                AdminApi.clearToken();
                setState(() {
                  _authed = false;
                  _items = [];
                  _checkingLock = true;
                });
                _refreshLockStatus();
              },
              child: const Text('退出'),
            ),
        ],
      ),
      body: _authed ? _listBody() : _loginBody(),
    );
  }

  Widget _loginBody() {
    if (_checkingLock) {
      return const Center(child: CircularProgressIndicator());
    }
    if (_statusFailed) {
      return Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 360),
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                const Text('无法读取登录状态，请刷新。'),
                const SizedBox(height: 16),
                FilledButton(
                  onPressed: () {
                    setState(() => _checkingLock = true);
                    _refreshLockStatus();
                  },
                  child: const Text('重试'),
                ),
              ],
            ),
          ),
        ),
      );
    }
    if (!_totpEnrolled) {
      return const Center(
        child: Padding(
          padding: EdgeInsets.all(24),
          child: Text('请完善登录安全设定再登录。'),
        ),
      );
    }
    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 360),
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text('管理员登录', style: Theme.of(context).textTheme.titleLarge),
              const SizedBox(height: 16),
              TextField(
                controller: _password,
                obscureText: true,
                enabled: !_loginDisabled,
                onSubmitted: (_) {
                  if (_loginDisabled) return;
                  _login();
                },
                decoration: const InputDecoration(
                  labelText: '口令',
                  border: OutlineInputBorder(),
                ),
              ),
              const SizedBox(height: 12),
              TextField(
                controller: _totp,
                enabled: !_loginDisabled,
                keyboardType: TextInputType.number,
                maxLength: 6,
                inputFormatters: [
                  FilteringTextInputFormatter.digitsOnly,
                  LengthLimitingTextInputFormatter(6),
                ],
                onSubmitted: (_) {
                  if (_loginDisabled) return;
                  _login();
                },
                decoration: const InputDecoration(
                  labelText: '动态码',
                  border: OutlineInputBorder(),
                  counterText: '',
                ),
              ),
              if (_error != null) ...[
                const SizedBox(height: 12),
                Text(_error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
              ],
              const SizedBox(height: 16),
              FilledButton(
                onPressed: _loginDisabled ? null : _login,
                child: Text(_busy ? '登录中…' : '登录'),
              ),
            ],
          ),
        ),
      ),
    );
  }

  Widget _listBody() {
    return Column(
      children: [
        if (_error != null)
          Material(
            color: Theme.of(context).colorScheme.errorContainer,
            child: Padding(
              padding: const EdgeInsets.all(12),
              child: Row(
                children: [
                  Expanded(child: Text(_error!)),
                  IconButton(onPressed: _load, icon: const Icon(Icons.refresh)),
                ],
              ),
            ),
          ),
        if (_busy) const LinearProgressIndicator(minHeight: 2),
        Expanded(
          child: _items.isEmpty && !_busy
              ? const Center(child: Text('没有记录'))
              : _AdminTable(
                  tab: _tabs[_tab.index].$1,
                  items: _items,
                  onDelete: _delete,
                  onClearPassword: _clearPassword,
                ),
        ),
      ],
    );
  }
}

class _ColSpec {
  const _ColSpec(this.label, {this.minWidth = 120, this.flex = 0, this.numeric = false});

  final String label;
  final double minWidth;
  final int flex;
  final bool numeric;
}

class _AdminTable extends StatefulWidget {
  const _AdminTable({
    required this.tab,
    required this.items,
    required this.onDelete,
    required this.onClearPassword,
  });

  final String tab;
  final List<AdminRecord> items;
  final void Function(AdminRecord row) onDelete;
  final void Function(AdminRecord row) onClearPassword;

  @override
  State<_AdminTable> createState() => _AdminTableState();
}

class _AdminTableState extends State<_AdminTable> {
  final _hScroll = ScrollController();
  final _vScroll = ScrollController();

  static const _hPad = 16.0;

  @override
  void dispose() {
    _hScroll.dispose();
    _vScroll.dispose();
    super.dispose();
  }

  List<_ColSpec> _columns() {
    switch (widget.tab) {
      case 'in_progress':
        return const [
          _ColSpec('文件名', minWidth: 180, flex: 2),
          _ColSpec('大小', minWidth: 88, numeric: true),
          _ColSpec('存储路径', minWidth: 280, flex: 3),
          _ColSpec('上传时间', minWidth: 148),
          _ColSpec('下载密码', minWidth: 100),
          _ColSpec('删除', minWidth: 72),
        ];
      case 'completed':
        return const [
          _ColSpec('文件名', minWidth: 180, flex: 2),
          _ColSpec('大小', minWidth: 88, numeric: true),
          _ColSpec('存储路径', minWidth: 280, flex: 3),
          _ColSpec('上传时间', minWidth: 148),
          _ColSpec('预定删除', minWidth: 148),
          _ColSpec('下载密码', minWidth: 100),
          _ColSpec('取消密码', minWidth: 96),
          _ColSpec('删除', minWidth: 72),
        ];
      case 'expired':
        return const [
          _ColSpec('文件名', minWidth: 180, flex: 2),
          _ColSpec('大小', minWidth: 88, numeric: true),
          _ColSpec('路径', minWidth: 200, flex: 2),
          _ColSpec('上传时间', minWidth: 148),
          _ColSpec('预定删除', minWidth: 148),
          _ColSpec('密码', minWidth: 88),
          _ColSpec('删除', minWidth: 72),
        ];
      case 'other_failed':
        return const [
          _ColSpec('文件名', minWidth: 180, flex: 2),
          _ColSpec('大小', minWidth: 88, numeric: true),
          _ColSpec('路径', minWidth: 200, flex: 2),
          _ColSpec('上传时间', minWidth: 148),
          _ColSpec('密码', minWidth: 88),
          _ColSpec('原因', minWidth: 120, flex: 1),
          _ColSpec('删除', minWidth: 72),
        ];
      case 'cancelled':
      default:
        return const [
          _ColSpec('文件名', minWidth: 180, flex: 2),
          _ColSpec('大小', minWidth: 88, numeric: true),
          _ColSpec('路径', minWidth: 200, flex: 2),
          _ColSpec('上传时间', minWidth: 148),
          _ColSpec('密码', minWidth: 88),
          _ColSpec('删除', minWidth: 72),
        ];
    }
  }

  List<double> _colWidths(List<_ColSpec> cols, double viewport) {
    final minTotal = cols.fold<double>(0, (s, c) => s + c.minWidth);
    final extra = viewport > minTotal ? viewport - minTotal : 0.0;
    final flexTotal = cols.fold<int>(0, (s, c) => s + c.flex);
    return [
      for (final c in cols)
        c.minWidth + (flexTotal == 0 || extra == 0 ? 0.0 : extra * c.flex / flexTotal),
    ];
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final cols = _columns();
    final headerBg = Color.alphaBlend(scheme.primary.withValues(alpha: 0.10), scheme.surface);
    final evenBg = scheme.surface;
    final oddBg = Color.alphaBlend(scheme.primary.withValues(alpha: 0.05), scheme.surface);
    final headingStyle = kNotoTextStyle.copyWith(
      fontWeight: FontWeight.w600,
      color: scheme.onSurface,
    );
    final dataStyle = kNotoTextStyle.copyWith(color: scheme.onSurface);

    return LayoutBuilder(
      builder: (context, constraints) {
        final inner = (constraints.maxWidth - _hPad * 2).clamp(0.0, double.infinity);
        final widths = _colWidths(cols, inner);
        final tableW = widths.fold<double>(0, (s, w) => s + w);
        return Scrollbar(
          controller: _hScroll,
          thumbVisibility: true,
          child: SingleChildScrollView(
            controller: _hScroll,
            scrollDirection: Axis.horizontal,
            child: SizedBox(
              width: tableW + _hPad * 2,
              height: constraints.maxHeight,
              child: Padding(
                padding: const EdgeInsets.symmetric(horizontal: _hPad),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    DecoratedBox(
                      decoration: BoxDecoration(
                        color: headerBg,
                        border: Border(
                          bottom: BorderSide(color: scheme.outlineVariant),
                        ),
                      ),
                      child: _cellsRow(
                        widths: widths,
                        cols: cols,
                        style: headingStyle,
                        cells: [for (final c in cols) Text(c.label)],
                      ),
                    ),
                    Expanded(
                      child: Scrollbar(
                        controller: _vScroll,
                        thumbVisibility: true,
                        child: ListView.builder(
                          controller: _vScroll,
                          padding: const EdgeInsets.only(bottom: 16),
                          itemCount: widget.items.length,
                          itemBuilder: (context, i) {
                            final row = widget.items[i];
                            return ColoredBox(
                              color: i.isEven ? evenBg : oddBg,
                              child: _cellsRow(
                                widths: widths,
                                cols: cols,
                                style: dataStyle,
                                cells: _rowCells(row),
                              ),
                            );
                          },
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            ),
          ),
        );
      },
    );
  }

  Widget _cellsRow({
    required List<double> widths,
    required List<_ColSpec> cols,
    required TextStyle style,
    required List<Widget> cells,
  }) {
    return Row(
      crossAxisAlignment: CrossAxisAlignment.center,
      children: [
        for (var i = 0; i < cells.length; i++)
          SizedBox(
            width: widths[i],
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
              child: DefaultTextStyle.merge(
                style: style,
                child: Align(
                  alignment: cols[i].numeric ? Alignment.centerRight : Alignment.centerLeft,
                  child: cells[i],
                ),
              ),
            ),
          ),
      ],
    );
  }

  List<Widget> _rowCells(AdminRecord row) {
    final path = row.storagePath.isEmpty ? '已删除' : row.storagePath;
    final created = _formatTime(row.createdAt.toLocal());
    final expires = row.expiresAt == null ? '—' : _formatTime(row.expiresAt!.toLocal());
    final password = row.password.isEmpty ? '—' : row.password;
    final size = _formatBytes(row.sizeBytes);
    final reason = row.reasonCode.isEmpty ? '—' : row.reasonCode;

    final cells = <Widget>[
      SelectableText(row.filename),
      Text(size),
      SelectableText(path),
      Text(created),
    ];

    switch (widget.tab) {
      case 'in_progress':
        cells.addAll([
          SelectableText(password),
          _deleteButton(row),
        ]);
      case 'completed':
        cells.addAll([
          Text(expires),
          SelectableText(password),
          row.password.isEmpty
              ? const SizedBox.shrink()
              : TextButton(
                  style: _actionStyle,
                  onPressed: () => widget.onClearPassword(row),
                  child: const Text('取消密码'),
                ),
          _deleteButton(row),
        ]);
      case 'expired':
        cells.addAll([
          Text(expires),
          SelectableText(password),
          _deleteButton(row),
        ]);
      case 'other_failed':
        cells.addAll([
          SelectableText(password),
          SelectableText(reason),
          _deleteButton(row),
        ]);
      default:
        cells.addAll([
          SelectableText(password),
          _deleteButton(row),
        ]);
    }
    return cells;
  }

  static final _actionStyle = TextButton.styleFrom(
    visualDensity: VisualDensity.compact,
    tapTargetSize: MaterialTapTargetSize.shrinkWrap,
    padding: const EdgeInsets.symmetric(horizontal: 8),
  );

  Widget _deleteButton(AdminRecord row) {
    return TextButton(
      style: _actionStyle,
      onPressed: () => widget.onDelete(row),
      child: const Text('删除'),
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
