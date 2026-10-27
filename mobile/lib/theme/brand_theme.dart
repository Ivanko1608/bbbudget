import 'package:flutter/cupertino.dart';
import 'package:flutter/material.dart';

import 'brand_colors.dart';

const _hairlineWidth = 0.5;

const _pageTransitionsTheme = PageTransitionsTheme(
  builders: {
    TargetPlatform.iOS: CupertinoPageTransitionsBuilder(),
    TargetPlatform.android: FadeForwardsPageTransitionsBuilder(),
  },
);

ThemeData createBrandTheme({
  required Brightness brightness,
  required TargetPlatform platform,
}) {
  final colors = brightness == Brightness.dark
      ? BrandColors.dark
      : BrandColors.light;

  return ThemeData(
    brightness: brightness,
    platform: platform,
    colorScheme: createColorScheme(brightness, colors),
    scaffoldBackgroundColor: colors.page,
    canvasColor: colors.page,
    dividerColor: colors.border,
    splashFactory: createSplashFactory(platform),
    pageTransitionsTheme: _pageTransitionsTheme,
    textTheme: createTextTheme(brightness, colors),
    cardTheme: createCardTheme(colors),
    navigationBarTheme: createNavigationBarTheme(colors),
    extensions: [colors],
  );
}

ColorScheme createColorScheme(Brightness brightness, BrandColors colors) {
  return ColorScheme.fromSeed(
    seedColor: colors.accent,
    brightness: brightness,
  ).copyWith(
    surface: colors.page,
    onSurface: colors.ink,
    primary: colors.ink,
    onPrimary: colors.page,
    secondary: colors.accent,
    onSecondary: colors.ink,
    outline: colors.border,
    outlineVariant: colors.border,
  );
}

InteractiveInkFeatureFactory createSplashFactory(TargetPlatform platform) {
  if (platform == TargetPlatform.iOS) {
    return NoSplash.splashFactory;
  }

  return InkSparkle.splashFactory;
}

TextTheme createTextTheme(Brightness brightness, BrandColors colors) {
  final base = ThemeData(brightness: brightness).textTheme;
  final inked = base.apply(bodyColor: colors.ink, displayColor: colors.ink);

  return inked.copyWith(
    headlineLarge: inked.headlineLarge?.copyWith(fontWeight: FontWeight.w800),
  );
}

CardThemeData createCardTheme(BrandColors colors) {
  return CardThemeData(
    color: colors.card,
    elevation: 0,
    margin: EdgeInsets.zero,
    shape: RoundedRectangleBorder(
      borderRadius: BorderRadius.circular(12),
      side: BorderSide(color: colors.border, width: _hairlineWidth),
    ),
  );
}

NavigationBarThemeData createNavigationBarTheme(BrandColors colors) {
  return NavigationBarThemeData(
    backgroundColor: colors.card,
    indicatorColor: colors.accent,
    surfaceTintColor: Colors.transparent,
    elevation: 0,
  );
}
