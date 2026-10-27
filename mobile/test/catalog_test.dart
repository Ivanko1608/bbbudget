import 'dart:convert';
import 'dart:io';

import 'package:bbbudget/i18n/catalog.dart';
import 'package:flutter_test/flutter_test.dart';

const _serverLocalesDir = '../internal/i18n/locales';
const _catalogsDir = 'assets/i18n';

List<String> listLanguageCodes(String dir) {
  final codes = [
    for (final file in Directory(dir).listSync())
      if (file.path.endsWith('.json'))
        file.uri.pathSegments.last.replaceAll('.json', ''),
  ];
  codes.sort();

  return codes;
}

Map<String, dynamic> readCatalog(String languageCode) {
  final file = File('$_catalogsDir/$languageCode.json');

  return jsonDecode(file.readAsStringSync()) as Map<String, dynamic>;
}

void main() {
  test('the app supports every locale the server has', () {
    final serverCodes = listLanguageCodes(_serverLocalesDir);

    expect(Catalog.languageCodes, serverCodes);
  });

  test('the app reads the web catalogs, not its own copies', () {
    final webCodes = listLanguageCodes('../web/static/i18n');
    final appCodes = listLanguageCodes(_catalogsDir);

    expect(appCodes, webCodes);
  });

  for (final code in Catalog.languageCodes) {
    test('the $code catalog has every key the app shows', () {
      final catalog = readCatalog(code);

      for (final key in CatalogKey.all) {
        expect(catalog, contains(key));
      }
    });
  }
}
