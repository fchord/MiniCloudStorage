import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';
import 'package:minicloudstorage/app_fonts.dart';

class PageShell extends StatelessWidget {
  const PageShell({
    super.key,
    required this.title,
    required this.subtitle,
    required this.child,
  });

  final String title;
  final String subtitle;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: InkWell(
          onTap: () => context.go('/'),
          child: const Text('MiniCloudStorage', style: kNotoTextStyle),
        ),
      ),
      body: LayoutBuilder(
        builder: (context, constraints) {
          final wide = constraints.maxWidth >= 600;
          final maxWidth = wide ? 640.0 : double.infinity;
          return Align(
            alignment: Alignment.topCenter,
            child: SingleChildScrollView(
              padding: EdgeInsets.symmetric(
                horizontal: wide ? 24 : 16,
                vertical: 24,
              ),
              child: ConstrainedBox(
                constraints: BoxConstraints(maxWidth: maxWidth),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    CjkText(
                      title,
                      style: Theme.of(context).textTheme.headlineSmall?.merge(kNotoTextStyle),
                    ),
                    const SizedBox(height: 8),
                    CjkText(
                      subtitle,
                      style: Theme.of(context).textTheme.bodyMedium?.merge(kNotoTextStyle),
                    ),
                    const SizedBox(height: 24),
                    child,
                  ],
                ),
              ),
            ),
          );
        },
      ),
    );
  }
}
