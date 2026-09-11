import 'dart:async';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';

/// OT name table family is "Noto Sans SC". Pubspec/FontManifest uses NotoSansSC.
/// Theme and Text styles must name a family we actually registered, otherwise
/// CanvasKit paints .notdef (tofu) and later swaps in a downloaded Noto.
const kNotoSansSC = 'Noto Sans SC';
const kNotoSansSCAlias = 'NotoSansSC';
const kNotoFallback = [kNotoSansSCAlias, kNotoSansSC];

const kNotoTextStyle = TextStyle(
  fontFamily: kNotoSansSC,
  fontFamilyFallback: kNotoFallback,
);

/// True after [FontLoader] has registered Noto into Skia.
final cjkFontReady = ValueNotifier<bool>(false);

void markCjkFontReady() {
  if (cjkFontReady.value) return;
  warmupCjkGlyphs();
  cjkFontReady.value = true;
}

void warmupCjkGlyphs() {
  // Do not iterate U+4E00-9FFF: holes in the subset kick off CanvasKit's
  // gstatic Noto download and stall startup. Static UI copy only.
  shapeCjk(
    '文件保存到期自动删除上传中密码下载点击选择最大需要访问勾选分配四位数字开始详情短码加载字体正在分片成功失败合并重试分段并行路名大小时间天后确认取消过期管理登录后台进行中已完成已取消失败存储路径预定明文原因链接',
  );
  shapeCjk(
    '的一是不了人我在有他这中大为上个国们来到时大地为子出就分生会可主发年动同工也能下过己经',
  );
  shapeCjk('，。、；：？！“”‘’（）【】《》—…％．０１２３４５６７８９');
}

/// Layout [text] with Noto off-screen so CanvasKit's first on-screen paragraph
/// does not paint .notdef for glyphs that were not in the startup warmup.
void shapeCjk(String text) {
  if (text.isEmpty) return;
  final builder = ui.ParagraphBuilder(
    ui.ParagraphStyle(fontFamily: kNotoSansSC, fontSize: 16),
  )
    ..pushStyle(
      ui.TextStyle(
        fontFamily: kNotoSansSC,
        fontFamilyFallback: kNotoFallback,
        fontSize: 16,
      ),
    )
    ..addText(text);
  final paragraph = builder.build()
    ..layout(const ui.ParagraphConstraints(width: 10000));
  final recorder = ui.PictureRecorder();
  ui.Canvas(recorder).drawParagraph(paragraph, Offset.zero);
  recorder.endRecording().dispose();
}

/// CJK text that never paints the string until Noto is in Skia and the glyphs
/// have been shaped. A grey bar is shown instead of tofu. After the font
/// arrives later, this rebuilds into real characters.
class CjkText extends StatefulWidget {
  const CjkText(
    this.data, {
    super.key,
    this.style,
    this.selectable = false,
    this.maxLines,
    this.overflow,
    this.textAlign,
  });

  final String data;
  final TextStyle? style;
  final bool selectable;
  final int? maxLines;
  final TextOverflow? overflow;
  final TextAlign? textAlign;

  @override
  State<CjkText> createState() => _CjkTextState();
}

class _CjkTextState extends State<CjkText> {
  String? _shapedFor;
  bool _gaveUp = false;
  Timer? _giveUp;

  @override
  void initState() {
    super.initState();
    cjkFontReady.addListener(_onFont);
    _tryShape();
    // Font failure must not leave a placeholder forever; Noto arriving later
    // still rebuilds via [_onFont].
    _giveUp = Timer(const Duration(milliseconds: 3000), () {
      if (!mounted || _shapedFor == widget.data) return;
      setState(() => _gaveUp = true);
    });
  }

  @override
  void didUpdateWidget(CjkText oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.data != widget.data) {
      _shapedFor = null;
      _gaveUp = false;
      _tryShape();
    }
  }

  @override
  void dispose() {
    _giveUp?.cancel();
    cjkFontReady.removeListener(_onFont);
    super.dispose();
  }

  void _onFont() {
    if (!mounted) return;
    _tryShape();
    setState(() {});
  }

  void _tryShape() {
    if (!cjkFontReady.value) return;
    if (_shapedFor == widget.data) return;
    shapeCjk(widget.data);
    _shapedFor = widget.data;
  }

  @override
  Widget build(BuildContext context) {
    final style = (widget.style ?? const TextStyle()).merge(kNotoTextStyle);
    final showText = _shapedFor == widget.data || _gaveUp;
    if (!showText) {
      return _CjkPlaceholder(height: style.fontSize ?? 16);
    }
    final text = Text(
      widget.data,
      style: style,
      maxLines: widget.maxLines,
      overflow: widget.overflow ?? TextOverflow.visible,
      textAlign: widget.textAlign,
    );
    if (widget.selectable) {
      return SelectionArea(child: text);
    }
    return text;
  }
}

class _CjkPlaceholder extends StatelessWidget {
  const _CjkPlaceholder({required this.height});

  final double height;

  @override
  Widget build(BuildContext context) {
    return Align(
      alignment: Alignment.centerLeft,
      child: Container(
        height: height * 0.7,
        width: 168,
        margin: EdgeInsets.symmetric(vertical: height * 0.2),
        decoration: BoxDecoration(
          color: Colors.black12,
          borderRadius: BorderRadius.circular(4),
        ),
      ),
    );
  }
}
