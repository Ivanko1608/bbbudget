import 'package:flutter/material.dart';

import '../widgets/adaptive_tab_bar.dart';
import 'home_tab.dart';
import 'tab_page.dart';

class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key});

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  HomeTab _selected = HomeTab.overview;

  void _select(HomeTab tab) {
    setState(() => _selected = tab);
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: IndexedStack(
        index: _selected.index,
        children: [for (final tab in HomeTab.values) TabPage(tab: tab)],
      ),
      bottomNavigationBar: AdaptiveTabBar(
        selected: _selected,
        onSelected: _select,
      ),
    );
  }
}
