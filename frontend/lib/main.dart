import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_web_plugins/url_strategy.dart';
import 'package:go_router/go_router.dart';
import 'package:minicloudstorage/app_fonts.dart';
import 'package:minicloudstorage/pages/admin_page.dart';
import 'package:minicloudstorage/pages/admin_setup_page.dart';
import 'package:minicloudstorage/pages/file_page.dart';
import 'package:minicloudstorage/pages/upload_page.dart';
import 'package:web/web.dart' as web;

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  usePathUrlStrategy();
  // Deep-link FilePage must not paint filename with a missing face. Wait for
  // FontLoader (not document.fonts.ready) with a hard cap so boot-mask cannot
  // hang forever. If this times out, keep loading in the background and rebuild.
  final fontJob = _registerCjkFont();
  try {
    await fontJob.timeout(const Duration(milliseconds: 3000));
    markCjkFontReady();
  } catch (_) {
    fontJob.then((_) {
      markCjkFontReady();
    }, onError: (Object _, StackTrace __) {});
  }
  runApp(const MiniCloudApp());
}

Future<void> _registerCjkFont() async {
  final data = await rootBundle.load('assets/fonts/NotoSansSC-Regular.otf');
  // Register under both the OT family name and the FontManifest alias so
  // Theme, AppBar, and CanvasKit fallback lookups all hit the bundled face
  // instead of painting .notdef while gstatic Noto downloads.
  final named = FontLoader(kNotoSansSC)..addFont(Future.value(data));
  final aliased = FontLoader(kNotoSansSCAlias)..addFont(Future.value(data));
  await Future.wait([named.load(), aliased.load()]);
}

void hideBootMask() {
  try {
    web.document.getElementById('boot-mask')?.remove();
  } catch (_) {}
}

final _router = GoRouter(
  routes: [
    GoRoute(
      path: '/',
      builder: (context, state) => const UploadPage(),
    ),
    GoRoute(
      path: '/admin/setup',
      builder: (context, state) => const AdminSetupPage(),
    ),
    GoRoute(
      path: '/admin',
      builder: (context, state) => const AdminPage(),
    ),
    GoRoute(
      path: '/:code',
      builder: (context, state) {
        final code = state.pathParameters['code'] ?? '';
        final password = state.uri.queryParameters['p'] ??
            state.uri.queryParameters['password'];
        return FilePage(code: code, initialPassword: password);
      },
    ),
  ],
);

class MiniCloudApp extends StatefulWidget {
  const MiniCloudApp({super.key});

  @override
  State<MiniCloudApp> createState() => _MiniCloudAppState();
}

class _MiniCloudAppState extends State<MiniCloudApp> {
  @override
  void initState() {
    super.initState();
    cjkFontReady.addListener(_onFontReady);
    WidgetsBinding.instance.addPostFrameCallback((_) => _syncMask());
    // Safety net: never keep the mask forever (HTML also uncovers at 5s).
    Future<void>.delayed(const Duration(milliseconds: 5000), hideBootMask);
  }

  @override
  void dispose() {
    cjkFontReady.removeListener(_onFontReady);
    super.dispose();
  }

  void _onFontReady() {
    _syncMask();
    if (mounted) setState(() {});
  }

  void _syncMask() {
    if (cjkFontReady.value) hideBootMask();
  }

  @override
  Widget build(BuildContext context) {
    const seed = Color(0xFF0F766E);
    final colorScheme = ColorScheme.fromSeed(seedColor: seed);
    final material = ThemeData(useMaterial3: true, colorScheme: colorScheme);
    final textTheme = material.textTheme.apply(
      fontFamily: kNotoSansSC,
      fontFamilyFallback: kNotoFallback,
    );
    final primaryTextTheme = material.primaryTextTheme.apply(
      fontFamily: kNotoSansSC,
      fontFamilyFallback: kNotoFallback,
    );
    return MaterialApp.router(
      title: 'MiniCloudStorage',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        colorScheme: colorScheme,
        useMaterial3: true,
        visualDensity: VisualDensity.standard,
        fontFamily: kNotoSansSC,
        textTheme: textTheme,
        primaryTextTheme: primaryTextTheme,
        appBarTheme: AppBarTheme(
          titleTextStyle: textTheme.titleLarge?.merge(kNotoTextStyle).copyWith(
                color: colorScheme.onSurface,
              ),
          toolbarTextStyle: textTheme.bodyMedium?.merge(kNotoTextStyle),
        ),
        snackBarTheme: SnackBarThemeData(
          contentTextStyle: textTheme.bodyMedium?.merge(kNotoTextStyle),
        ),
      ),
      builder: (context, child) {
        return DefaultTextStyle.merge(
          style: kNotoTextStyle,
          child: child ?? const SizedBox.shrink(),
        );
      },
      routerConfig: _router,
    );
  }
}
