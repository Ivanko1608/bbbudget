import 'package:flutter/cupertino.dart';
import 'package:flutter/material.dart';

import '../i18n/catalog.dart';
import '../screens/home_tab.dart';
import '../theme/brand_colors.dart';

/// The Cupertino tab bar on iOS and the Material navigation bar elsewhere.
class AdaptiveTabBar extends StatelessWidget {
  const AdaptiveTabBar({
    required this.selected,
    required this.onSelected,
    super.key,
  });

  final HomeTab selected;
  final ValueChanged<HomeTab> onSelected;

  @override
  Widget build(BuildContext context) {
    final isIOS = Theme.of(context).platform == TargetPlatform.iOS;
    if (isIOS) {
      return buildCupertinoBar(context);
    }

    return buildMaterialBar(context);
  }

  Widget buildCupertinoBar(BuildContext context) {
    final catalog = Catalog.of(context);
    final colors = BrandColors.of(context);

    return CupertinoTabBar(
      currentIndex: selected.index,
      onTap: (index) => onSelected(HomeTab.values[index]),
      backgroundColor: colors.card,
      activeColor: colors.ink,
      inactiveColor: colors.ink.withValues(alpha: 0.45),
      border: Border(top: BorderSide(color: colors.border, width: 0.5)),
      items: [
        for (final tab in HomeTab.values)
          BottomNavigationBarItem(
            icon: Icon(tab.cupertinoIcon),
            label: catalog.text(tab.labelKey),
          ),
      ],
    );
  }

  Widget buildMaterialBar(BuildContext context) {
    final catalog = Catalog.of(context);

    return NavigationBar(
      selectedIndex: selected.index,
      onDestinationSelected: (index) => onSelected(HomeTab.values[index]),
      destinations: [
        for (final tab in HomeTab.values)
          NavigationDestination(
            icon: Icon(tab.materialIcon),
            label: catalog.text(tab.labelKey),
          ),
      ],
    );
  }
}
