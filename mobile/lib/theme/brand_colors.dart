import 'package:flutter/material.dart';

/// The brand palette. Every screen reads its colours from here.
@immutable
class BrandColors extends ThemeExtension<BrandColors> {
  const BrandColors({
    required this.page,
    required this.card,
    required this.border,
    required this.ink,
    required this.inkMuted,
    required this.accent,
  });

  static const _accent = Color(0xFFFFD60A);

  static const light = BrandColors(
    page: Color(0xFFF7F6F2),
    card: Color(0xFFFFFFFF),
    border: Color(0xFFE2E0D8),
    ink: Color(0xFF16181D),
    inkMuted: Color(0x7316181D),
    accent: _accent,
  );

  static const dark = BrandColors(
    page: Color(0xFF0E0F12),
    card: Color(0xFF17181C),
    border: Color(0xFF2A2C32),
    ink: Color(0xFFF2F1EC),
    inkMuted: Color(0x73F2F1EC),
    accent: _accent,
  );

  final Color page;
  final Color card;
  final Color border;
  final Color ink;
  final Color inkMuted;
  final Color accent;

  static BrandColors of(BuildContext context) {
    return Theme.of(context).extension<BrandColors>()!;
  }

  @override
  BrandColors copyWith({
    Color? page,
    Color? card,
    Color? border,
    Color? ink,
    Color? inkMuted,
    Color? accent,
  }) {
    return BrandColors(
      page: page ?? this.page,
      card: card ?? this.card,
      border: border ?? this.border,
      ink: ink ?? this.ink,
      inkMuted: inkMuted ?? this.inkMuted,
      accent: accent ?? this.accent,
    );
  }

  @override
  BrandColors lerp(BrandColors? other, double t) {
    if (other == null) {
      return this;
    }

    return BrandColors(
      page: Color.lerp(page, other.page, t)!,
      card: Color.lerp(card, other.card, t)!,
      border: Color.lerp(border, other.border, t)!,
      ink: Color.lerp(ink, other.ink, t)!,
      inkMuted: Color.lerp(inkMuted, other.inkMuted, t)!,
      accent: Color.lerp(accent, other.accent, t)!,
    );
  }
}
