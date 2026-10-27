import 'dart:convert';
import 'dart:io';

import 'package:bbbudget/i18n/catalog.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

const _serverLocalesDir = '../internal/i18n/locales';
const _webCatalogsDir = '../web/static/i18n';
const _catalogsDir = 'assets/i18n';
const _infoPlist = 'ios/Runner/Info.plist';

List<String> listServerLanguageCodes() {
  final codes = [
    for (final file in Directory(_serverLocalesDir).listSync())
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

List<String> listBundleLocalizations() {
  final plist = File(_infoPlist).readAsStringSync();
  final block = RegExp(
    r'<key>CFBundleLocalizations</key>\s*<array>(.*?)</array>',
    dotAll: true,
  ).firstMatch(plist);

  return [
    for (final match in RegExp(
      r'<string>(.*?)</string>',
    ).allMatches(block!.group(1)!))
      match.group(1)!,
  ]..sort();
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  test('the app has a catalog for every locale the server has', () async {
    final appCodes = await Catalog.listLanguageCodes(rootBundle);

    expect(appCodes, listServerLanguageCodes());
  });

  test('the iOS app declares every locale the server has', () {
    expect(listBundleLocalizations(), listServerLanguageCodes());
  });

  test('the app catalogs are the web catalogs, not a copy', () {
    expect(FileSystemEntity.isLinkSync(_catalogsDir), isTrue);
    expect(
      Directory(_catalogsDir).resolveSymbolicLinksSync(),
      Directory(_webCatalogsDir).resolveSymbolicLinksSync(),
    );
  });

  for (final code in listServerLanguageCodes()) {
    test('the $code catalog has every key the app shows', () {
      final catalog = readCatalog(code);

      for (final key in CatalogKey.all) {
        expect(catalog, contains(key));
      }
    });
  }
}
