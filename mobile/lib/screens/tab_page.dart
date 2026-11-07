import 'package:flutter/material.dart';

import '../i18n/catalog.dart';
import 'home_tab.dart';

/// A tab's page: a large bold title, with the content to come.
class TabPage extends StatelessWidget {
  const TabPage({required this.tab, super.key});

  final HomeTab tab;

  @override
  Widget build(BuildContext context) {
    final title = Catalog.of(context).text(tab.labelKey);
    final titleStyle = Theme.of(context).textTheme.headlineLarge;

    return SafeArea(
      child: ListView(
        padding: const EdgeInsets.all(16),
        children: [Text(title, style: titleStyle)],
      ),
    );
  }
}
