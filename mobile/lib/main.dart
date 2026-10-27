import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';

import 'app.dart';
import 'i18n/catalog.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  final languageCodes = await Catalog.listLanguageCodes(rootBundle);

  runApp(BbbudgetApp(languageCodes: languageCodes));
}
