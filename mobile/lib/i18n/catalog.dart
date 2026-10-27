import 'dart:convert';

import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';

const _catalogDir = 'assets/i18n/';
const _catalogExtension = '.json';

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

  final Map<String, String> _texts;

  static Catalog of(BuildContext context) {
    return Localizations.of<Catalog>(context, Catalog)!;
  }

  /// The locales of the bundled catalogs, which are the web app's.
  static Future<List<String>> listLanguageCodes(AssetBundle bundle) async {
    final manifest = await AssetManifest.loadFromAssetBundle(bundle);
    final codes = [
      for (final asset in manifest.listAssets())
        if (asset.startsWith(_catalogDir) && asset.endsWith(_catalogExtension))
          asset.substring(
            _catalogDir.length,
            asset.length - _catalogExtension.length,
          ),
    ];
    codes.sort();

    return codes;
  }

  static Future<Catalog> load(String languageCode, AssetBundle bundle) async {
    final json = await bundle.loadString(
      '$_catalogDir$languageCode$_catalogExtension',
    );
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

class CatalogDelegate extends LocalizationsDelegate<Catalog> {
  const CatalogDelegate(this.languageCodes);

  final List<String> languageCodes;

  List<Locale> get supportedLocales {
    return [for (final code in languageCodes) Locale(code)];
  }

  @override
  bool isSupported(Locale locale) {
    return languageCodes.contains(locale.languageCode);
  }

  @override
  Future<Catalog> load(Locale locale) {
    return Catalog.load(locale.languageCode, rootBundle);
  }

  @override
  bool shouldReload(CatalogDelegate old) => false;
}
