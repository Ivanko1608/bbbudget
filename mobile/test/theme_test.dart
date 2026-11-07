import 'package:bbbudget/theme/brand_colors.dart';
import 'package:bbbudget/theme/brand_theme.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

ThemeData createTheme(Brightness brightness) {
  return createBrandTheme(
    brightness: brightness,
    platform: TargetPlatform.android,
  );
}

void main() {
  const yellow = Color(0xFFFFD60A);

  group('light theme', () {
    final theme = createTheme(Brightness.light);

    test('uses the light palette', () {
      expect(theme.extension<BrandColors>(), BrandColors.light);
    });

    test('paints the page in the page colour', () {
      expect(theme.scaffoldBackgroundColor, BrandColors.light.page);
    });

    test('paints cards flat in the card colour with a hairline border', () {
      final shape = theme.cardTheme.shape as RoundedRectangleBorder;

      expect(theme.cardTheme.color, BrandColors.light.card);
      expect(theme.cardTheme.elevation, 0);
      expect(shape.side.color, BrandColors.light.border);
    });

    test('writes text in ink', () {
      expect(theme.textTheme.bodyMedium?.color, BrandColors.light.ink);
    });

    test('marks the selected tab with the accent', () {
      expect(theme.navigationBarTheme.indicatorColor, yellow);
    });
  });

  group('dark theme', () {
    final theme = createTheme(Brightness.dark);

    test('uses the dark palette', () {
      expect(theme.extension<BrandColors>(), BrandColors.dark);
    });

    test('paints the page in the page colour', () {
      expect(theme.scaffoldBackgroundColor, BrandColors.dark.page);
    });

    test('writes text in ink', () {
      expect(theme.textTheme.bodyMedium?.color, BrandColors.dark.ink);
    });
  });

  test('both palettes share the yellow accent', () {
    expect(BrandColors.light.accent, yellow);
    expect(BrandColors.dark.accent, yellow);
  });

  test('page titles are large and bold', () {
    final title = createTheme(Brightness.light).textTheme.headlineLarge;

    expect(title?.fontWeight, FontWeight.w800);
  });
}
