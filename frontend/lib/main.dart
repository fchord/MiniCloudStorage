import 'package:flutter/material.dart';
import 'package:flutter_web_plugins/url_strategy.dart';
import 'package:go_router/go_router.dart';
import 'package:minicloudstorage/pages/file_page.dart';
import 'package:minicloudstorage/pages/upload_page.dart';

void main() {
  usePathUrlStrategy();
  runApp(const MiniCloudApp());
}

final _router = GoRouter(
  routes: [
    GoRoute(
      path: '/',
      builder: (context, state) => const UploadPage(),
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

class MiniCloudApp extends StatelessWidget {
  const MiniCloudApp({super.key});

  @override
  Widget build(BuildContext context) {
    const seed = Color(0xFF0F766E);
    return MaterialApp.router(
      title: 'MiniCloudStorage',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: seed),
        useMaterial3: true,
        visualDensity: VisualDensity.standard,
      ),
      routerConfig: _router,
    );
  }
}
