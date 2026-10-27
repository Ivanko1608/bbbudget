import 'package:bbbudget/app.dart';
import 'package:bbbudget/screens/home_tab.dart';
import 'package:bbbudget/theme/brand_theme.dart';
import 'package:flutter/cupertino.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

final _iOS = TargetPlatformVariant.only(TargetPlatform.iOS);
final _android = TargetPlatformVariant.only(TargetPlatform.android);

// The catalog comes from the asset bundle, which is real I/O that the test
// clock cannot wait for.
Future<void> waitForCatalog(WidgetTester tester) async {
  const attempts = 100;
  const pause = Duration(milliseconds: 20);

  for (var attempt = 0; attempt < attempts; attempt++) {
    await tester.runAsync(() => Future<void>.delayed(pause));
    await tester.pump();
    if (find.byType(Scaffold).evaluate().isNotEmpty) {
      return;
    }
  }
}

Future<void> pumpApp(WidgetTester tester) async {
  await tester.pumpWidget(const BbbudgetApp());
  await waitForCatalog(tester);
  await tester.pumpAndSettle();
}

ScrollPhysics getPagePhysics(WidgetTester tester) {
  final context = tester.element(find.byType(ListView).first);

  return ScrollConfiguration.of(context).getScrollPhysics(context);
}

Future<void> pumpPushedPage(WidgetTester tester) async {
  final theme = createBrandTheme(
    brightness: Brightness.light,
    platform: defaultTargetPlatform,
  );
  await tester.pumpWidget(
    MaterialApp(
      theme: theme,
      home: Builder(
        builder: (context) => TextButton(
          onPressed: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => const Scaffold(body: Text('pushed')),
            ),
          ),
          child: const Text('open'),
        ),
      ),
    ),
  );
  await tester.tap(find.text('open'));
  await tester.pumpAndSettle();
}

void main() {
  setUp(rootBundle.clear);

  group('on iOS', () {
    testWidgets('shows the Cupertino tab bar', (tester) async {
      await pumpApp(tester);

      expect(find.byType(CupertinoTabBar), findsOneWidget);
      expect(find.byType(NavigationBar), findsNothing);
    }, variant: _iOS);

    testWidgets('uses iOS icons', (tester) async {
      await pumpApp(tester);

      expect(find.byIcon(CupertinoIcons.chat_bubble), findsOneWidget);
    }, variant: _iOS);

    testWidgets('switches page when a tab is tapped', (tester) async {
      await pumpApp(tester);

      await tester.tap(find.text('Plan'));
      await tester.pumpAndSettle();

      final bar = tester.widget<CupertinoTabBar>(find.byType(CupertinoTabBar));

      expect(bar.currentIndex, HomeTab.plan.index);
    }, variant: _iOS);

    testWidgets('has no ink ripple', (tester) async {
      await pumpApp(tester);

      final theme = Theme.of(tester.element(find.byType(Scaffold)));

      expect(theme.splashFactory, NoSplash.splashFactory);
    }, variant: _iOS);

    testWidgets('scrolls with a bounce', (tester) async {
      await pumpApp(tester);

      expect(getPagePhysics(tester), isA<BouncingScrollPhysics>());
    }, variant: _iOS);

    testWidgets('swipes back from the left edge', (tester) async {
      await pumpPushedPage(tester);

      await tester.dragFrom(const Offset(2, 300), const Offset(500, 0));
      await tester.pumpAndSettle();

      expect(find.text('pushed'), findsNothing);
    }, variant: _iOS);
  });

  group('on Android', () {
    testWidgets('shows the Material navigation bar', (tester) async {
      await pumpApp(tester);

      expect(find.byType(NavigationBar), findsOneWidget);
      expect(find.byType(CupertinoTabBar), findsNothing);
    }, variant: _android);

    testWidgets('scrolls without a bounce', (tester) async {
      await pumpApp(tester);

      expect(getPagePhysics(tester), isA<ClampingScrollPhysics>());
    }, variant: _android);
  });
}
