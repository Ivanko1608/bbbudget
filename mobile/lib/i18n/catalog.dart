import 'dart:convert';

import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';

/// Keys of the shared catalogs in web/static/i18n that the app shows.
abstract final class CatalogKey {
  static const appTitle = 'app.title';
  static const tabOverview = 'tab.overview';
  static const tabAsk = 'chat.open';
  static const tabPlan = 'tab.plan';

  static const all = [appTitle, tabOverview, tabAsk, tabPlan];
}

/// The texts of one locale, read from the catalog the web app also uses.
class Catalog {
  Catalog(this._texts);

  static const delegate = _CatalogDelegate();

  static const languageCodes = ['en', 'ru'];

  static final supportedLocales = [
    for (final code in languageCodes) Locale(code),
  ];

  final Map<String, String> _texts;

  static Catalog of(BuildContext context) {
    return Localizations.of<Catalog>(context, Catalog)!;
  }

  static Future<Catalog> load(String languageCode, AssetBundle bundle) async {
    final json = await bundle.loadString('assets/i18n/$languageCode.json');
    final decoded = jsonDecode(json) as Map<String, dynamic>;

    return Catalog(decoded.cast<String, String>());
  }

  String text(String key) {
    final text = _texts[key];
    if (text == null) {
      throw StateError('Missing catalog key: $key');
    }

    return text;
  }
}

class _CatalogDelegate extends LocalizationsDelegate<Catalog> {
  const _CatalogDelegate();

  @override
  bool isSupported(Locale locale) {
    return Catalog.languageCodes.contains(locale.languageCode);
  }

  @override
  Future<Catalog> load(Locale locale) {
    return Catalog.load(locale.languageCode, rootBundle);
  }

  @override
  bool shouldReload(_CatalogDelegate old) => false;
}
