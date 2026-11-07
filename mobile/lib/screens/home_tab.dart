import 'package:flutter/cupertino.dart';
import 'package:flutter/material.dart';

import '../i18n/catalog.dart';

enum HomeTab {
  overview(
    labelKey: CatalogKey.tabOverview,
    materialIcon: Icons.dashboard_outlined,
    cupertinoIcon: CupertinoIcons.square_grid_2x2,
  ),
  ask(
    labelKey: CatalogKey.tabAsk,
    materialIcon: Icons.chat_bubble_outline,
    cupertinoIcon: CupertinoIcons.chat_bubble,
  ),
  plan(
    labelKey: CatalogKey.tabPlan,
    materialIcon: Icons.event_note_outlined,
    cupertinoIcon: CupertinoIcons.calendar,
  );

  const HomeTab({
    required this.labelKey,
    required this.materialIcon,
    required this.cupertinoIcon,
  });

  final String labelKey;
  final IconData materialIcon;
  final IconData cupertinoIcon;
}
