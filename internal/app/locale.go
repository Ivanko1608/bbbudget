package app

import (
	"bbbudget/internal/i18n"
	"bbbudget/internal/settings"
)

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

// Localizer renders text for a reader whose browser asks for acceptLanguage
// ("" outside HTTP); see ResolveLocale.
func (a *App) Localizer(acceptLanguage string) *i18n.Localizer {
	return i18n.For(a.ResolveLocale(acceptLanguage))
}

// Localize renders the view's text with l.
func (v *View) Localize(l *i18n.Localizer) { v.Report.Localize(l) }
