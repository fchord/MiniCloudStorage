import 'package:flutter/material.dart';

enum ChunkPhase { pending, uploading, success, failed }

/// Byte-weighted segmented bar. One [AnimationController] for all in-flight
/// segments so progress updates do not rebuild tickers.
class ChunkProgressBar extends StatefulWidget {
  const ChunkProgressBar({
    super.key,
    required this.sizes,
    required this.phases,
    required this.merging,
    this.onRetryChunk,
  });

  final List<int> sizes;
  final List<ChunkPhase> phases;
  final bool merging;
  final ValueChanged<int>? onRetryChunk;

  @override
  State<ChunkProgressBar> createState() => _ChunkProgressBarState();
}

class _ChunkProgressBarState extends State<ChunkProgressBar>
    with SingleTickerProviderStateMixin {
  late final AnimationController _pulse;

  @override
  void initState() {
    super.initState();
    _pulse = AnimationController(
      vsync: this,
      duration: const Duration(milliseconds: 1100),
    );
    _syncTicker();
  }

  @override
  void didUpdateWidget(covariant ChunkProgressBar oldWidget) {
    super.didUpdateWidget(oldWidget);
    _syncTicker();
  }

  bool get _needsTick =>
      widget.merging || widget.phases.contains(ChunkPhase.uploading);

  void _syncTicker() {
    if (_needsTick) {
      if (!_pulse.isAnimating) _pulse.repeat();
    } else if (_pulse.isAnimating) {
      _pulse.stop();
      _pulse.reset();
    }
  }

  @override
  void dispose() {
    _pulse.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final n = widget.sizes.length;
    if (n == 0) return const SizedBox.shrink();
    return SizedBox(
      height: 8,
      width: double.infinity,
      child: AnimatedBuilder(
        animation: _pulse,
        builder: (context, _) {
          return Row(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              for (var i = 0; i < n; i++) ...[
                if (i > 0) const SizedBox(width: 2),
                Expanded(
                  flex: widget.sizes[i].clamp(1, 1 << 30),
                  child: _Segment(
                    phase: widget.phases[i],
                    merging: widget.merging,
                    t: _pulse.value,
                    scheme: scheme,
                    onTap: widget.phases[i] == ChunkPhase.failed
                        ? () => widget.onRetryChunk?.call(i)
                        : null,
                  ),
                ),
              ],
            ],
          );
        },
      ),
    );
  }
}

class _Segment extends StatelessWidget {
  const _Segment({
    required this.phase,
    required this.merging,
    required this.t,
    required this.scheme,
    this.onTap,
  });

  final ChunkPhase phase;
  final bool merging;
  final double t;
  final ColorScheme scheme;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final color = _color();
    final child = SizedBox.expand(
      child: DecoratedBox(
        decoration: BoxDecoration(
          color: color,
          borderRadius: BorderRadius.circular(4),
          gradient: _gradient(),
        ),
      ),
    );
    if (onTap == null) return child;
    return MouseRegion(
      cursor: SystemMouseCursors.click,
      child: GestureDetector(onTap: onTap, child: child),
    );
  }

  Color _color() {
    if (merging && phase == ChunkPhase.success) {
      return Color.lerp(const Color(0xFF16A34A), scheme.primary, 0.25 + 0.25 * _tri(t))!;
    }
    switch (phase) {
      case ChunkPhase.pending:
        return const Color(0xFFE5E7EB);
      case ChunkPhase.uploading:
        return Color.lerp(scheme.primary.withValues(alpha: 0.45), scheme.primary, _tri(t))!;
      case ChunkPhase.success:
        return const Color(0xFF16A34A);
      case ChunkPhase.failed:
        return scheme.error;
    }
  }

  Gradient? _gradient() {
    if (phase != ChunkPhase.uploading || merging) return null;
    final x = -1.0 + 2.0 * t;
    return LinearGradient(
      begin: Alignment(x - 0.4, 0),
      end: Alignment(x + 0.4, 0),
      colors: [
        scheme.primary.withValues(alpha: 0.35),
        scheme.primary,
        scheme.primary.withValues(alpha: 0.35),
      ],
    );
  }

  static double _tri(double t) => t < 0.5 ? t * 2 : (1 - t) * 2;
}
