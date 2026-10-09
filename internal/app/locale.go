package app

import (
	"context"

	"bbbudget/internal/i18n"
)

// Locale is the language to render text in for this call: one set on the
// context by the transport (from the request), else the app setting, else
// the environment (LANG), else English.
func (a *App) Locale(ctx context.Context) string {
	if l := i18n.FromContext(ctx); l != "" {
		return i18n.Match(l)
	}
	if s, err := a.Settings(); err == nil && s.Locale != "" && s.Locale != "auto" {
		return i18n.Match(s.Locale)
	}
	return i18n.FromEnv()
}

// Localizer renders text for this call (see Locale).
func (a *App) Localizer(ctx context.Context) *i18n.Localizer { return i18n.For(a.Locale(ctx)) }
