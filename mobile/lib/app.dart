import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_localizations/flutter_localizations.dart';

import 'i18n/catalog.dart';
import 'screens/home_screen.dart';
import 'theme/brand_theme.dart';

class BbbudgetApp extends StatelessWidget {
  const BbbudgetApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      onGenerateTitle: (context) {
        return Catalog.of(context).text(CatalogKey.appTitle);
      },
      theme: createBrandTheme(
        brightness: Brightness.light,
        platform: defaultTargetPlatform,
      ),
      darkTheme: createBrandTheme(
        brightness: Brightness.dark,
        platform: defaultTargetPlatform,
      ),
      themeMode: ThemeMode.system,
      localizationsDelegates: const [
        Catalog.delegate,
        GlobalMaterialLocalizations.delegate,
        GlobalCupertinoLocalizations.delegate,
        GlobalWidgetsLocalizations.delegate,
      ],
      supportedLocales: Catalog.supportedLocales,
      home: const HomeScreen(),
    );
  }
}
