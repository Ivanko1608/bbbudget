package app

import (
	"context"

	"bbbudget/internal/i18n"
	"bbbudget/internal/settings"
)

// Locale is the language to render text in for this call: the one the
// transport put on the context, else what ResolveLocale picks with no
// browser preference.
func (a *App) Locale(ctx context.Context) string {
	if l := i18n.FromContext(ctx); l != "" {
		return i18n.Match(l)
	}
	return a.ResolveLocale("")
}

// ResolveLocale picks the language for a reader: the app setting if one is
// chosen, else the browser's Accept-Language header (empty outside HTTP),
// else the environment (LANG), else English.
func (a *App) ResolveLocale(acceptLanguage string) string {
	if s, err := a.Settings(); err == nil && s.Locale != "" && s.Locale != settings.LocaleAuto {
		return i18n.Match(s.Locale)
	}
	if l := i18n.MatchAcceptLanguage(acceptLanguage); l != "" {
		return l
	}
	return i18n.FromEnv()
}

// Localizer renders text for this call (see Locale).
func (a *App) Localizer(ctx context.Context) *i18n.Localizer { return i18n.For(a.Locale(ctx)) }
