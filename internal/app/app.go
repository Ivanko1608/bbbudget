// Package app is the use-case layer shared by the HTTP API, the MCP server
// and the CLI. Transports stay thin: they decode input, call App, encode output.
package app

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"bbbudget/internal/budget"
	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
	"bbbudget/internal/rates"
	"bbbudget/internal/store"
	"bbbudget/internal/templates"
)

type App struct {
	Store     store.Store
	Templates *templates.Registry
	// Rates are the exchange-rate providers by source name (see budget.RateSources).
	Rates map[string]rates.Provider
	// SettingsPath is the app settings file; "" means defaults, read-only.
	SettingsPath string
	Now          func() time.Time

	settingsMu sync.Mutex
	auto       autoState
}

// New returns an App. A nil registry means built-in templates only. Rates
// come from the public providers in package rates. Set SettingsPath to use
// a settings file.
func New(s store.Store, t *templates.Registry) *App {
	if t == nil {
		t = templates.Builtin()
	}
	a := &App{Store: s, Templates: t, Now: time.Now}
	a.Rates = rates.Registry(&http.Client{Timeout: 20 * time.Second}, func() string {
		st, _ := a.Settings()
		return st.CoinGeckoAPIKey
	})
	return a
}

// View is a stored budget together with its computed report.
type View struct {
	ID        string         `json:"id"`
	Revision  int64          `json:"revision"`
	UpdatedAt time.Time      `json:"updated_at"`
	Budget    budget.Budget  `json:"budget"`
	Report    *budget.Report `json:"report"`
}

// view analyzes a stored budget, with its text in the caller's language.
func (a *App) view(ctx context.Context, r store.Record) (*View, error) {
	rep, err := a.analyze(ctx, r.Budget)
	if err != nil {
		return nil, err
	}
	return &View{ID: r.ID, Revision: r.Revision, UpdatedAt: r.UpdatedAt, Budget: r.Budget, Report: rep}, nil
}

func (a *App) List(ctx context.Context) ([]store.Meta, error) { return a.Store.List(ctx) }

// Create makes a new budget from a template ("" means blank). Name defaults to the id.
func (a *App) Create(
	ctx context.Context, origin store.Origin, id, name, template string,
) (*View, error) {
	if template == "" {
		template = "blank"
	}
	if name == "" {
		name = id
	}
	b, err := a.Templates.New(template, name, a.Now())
	if err != nil {
		return nil, err
	}
	localizeTemplate(&b, a.Localizer(ctx))
	r, err := a.Store.Create(ctx, origin, id, b)
	if err != nil {
		return nil, err
	}
	return a.view(ctx, r)
}

// localizeTemplate translates the category labels and pot names a template
// left as they are in English ("Income" for category income, "General
// savings" for pot savings) into the reader's language. A name anyone
// changed is theirs and stays as it is.
func localizeTemplate(b *budget.Budget, l *i18n.Localizer) {
	en := i18n.For(i18n.Default)
	translate := func(key, name string) string {
		m := i18n.Msg{Key: key}
		if en.Has(key) && en.Render(m) == name {
			return l.Render(m)
		}
		return name
	}
	for i, c := range b.Categories {
		b.Categories[i].Label = translate("category."+c.ID, c.Label)
	}
	for i, p := range b.Pots {
		b.Pots[i].Name = translate("pot."+p.ID, p.Name)
	}
}

// Import stores a full budget document as a new budget.
func (a *App) Import(
	ctx context.Context, origin store.Origin, id string, b budget.Budget,
) (*View, error) {
	r, err := a.Store.Create(ctx, origin, id, b)
	if err != nil {
		return nil, err
	}
	return a.view(ctx, r)
}

func (a *App) Get(ctx context.Context, id string) (*View, error) {
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return a.view(ctx, r)
}

func (a *App) Delete(ctx context.Context, id string) error { return a.Store.Delete(ctx, id) }

// Replace overwrites the whole document (the web editor's save), guarded by revision.
func (a *App) Replace(
	ctx context.Context, origin store.Origin, id string, revision int64, b budget.Budget,
) (*View, error) {
	r, err := a.Store.Update(ctx, origin, id, revision, replaceWith(b))
	if err != nil {
		return nil, err
	}
	return a.view(ctx, r)
}

// Apply runs ops against the latest budget and saves the result atomically.
// Pass store.AnyRevision to apply on top of whatever is current.
func (a *App) Apply(
	ctx context.Context, origin store.Origin, id string, revision int64, ops []budget.Op,
) (*View, error) {
	r, err := a.Store.Update(ctx, origin, id, revision, applyOps(ops))
	if err != nil {
		return nil, err
	}
	return a.view(ctx, r)
}

func (a *App) analyze(ctx context.Context, b budget.Budget) (*budget.Report, error) {
	rep, err := budget.Analyze(b)
	if err != nil {
		return nil, err
	}
	if l := a.Localizer(ctx); l.Locale() != i18n.Default {
		rep.Localize(l)
	}
	return rep, nil
}

// WhatIf previews ops without saving anything. It returns the hypothetical
// budget and its report.
func (a *App) WhatIf(ctx context.Context, id string, ops []budget.Op) (budget.Budget, *budget.Report, error) {
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return budget.Budget{}, nil, err
	}
	nb, err := budget.Apply(r.Budget, ops...)
	if err != nil {
		return budget.Budget{}, nil, err
	}
	rep, err := a.analyze(ctx, nb)
	return nb, rep, err
}

// SetDebt updates the debt matching ref, or creates one named ref if none
// matches. Both happen inside one store transaction.
func (a *App) SetDebt(
	ctx context.Context, origin store.Origin, id, ref string, patch budget.DebtPatch,
) (*View, error) {
	r, err := a.Store.Update(ctx, origin, id, store.AnyRevision, setOrAddDebt(ref, patch))
	if err != nil {
		return nil, err
	}
	return a.view(ctx, r)
}

// setOrAddDebt updates the debt matching ref, or adds one named ref.
func setOrAddDebt(ref string, patch budget.DebtPatch) store.EditFunc {
	return func(b budget.Budget) (budget.Budget, error) {
		_, err := budget.FindDebt(b, ref)
		var nm *budget.NoMatchError
		if !errors.As(err, &nm) {
			return budget.Apply(b, budget.NewOp("set_debt", ref, patch))
		}
		zero := money.MustDecimal("0")
		d := budget.Debt{Name: ref, Currency: b.BaseCurrency, Balance: zero, APRPct: zero, MinPayment: zero}
		if patch.Balance != nil {
			d.Balance = *patch.Balance
		}
		if patch.APRPct != nil {
			d.APRPct = *patch.APRPct
		}
		if patch.MinPayment != nil {
			d.MinPayment = *patch.MinPayment
		}
		if patch.Currency != nil {
			d.Currency = *patch.Currency
		}
		ops := []budget.Op{budget.NewOp("add_debt", "", d)}
		if patch.PayoffMonth != nil || patch.PayoffFrom != nil {
			payoff := budget.DebtPatch{PayoffMonth: patch.PayoffMonth, PayoffFrom: patch.PayoffFrom}
			ops = append(ops, budget.NewOp("set_debt", ref, payoff))
		}
		return budget.Apply(b, ops...)
	}
}

// ScenarioReport projects a saved scenario without saving.
func (a *App) ScenarioReport(ctx context.Context, id, scenario string) (*budget.Report, error) {
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s, _, err := budget.FindScenario(r.Budget, scenario)
	if err != nil {
		return nil, err
	}
	nb, err := budget.Apply(r.Budget, s.Ops...)
	if err != nil {
		return nil, err
	}
	return a.analyze(ctx, nb)
}

func (a *App) SaveScenario(
	ctx context.Context, origin store.Origin, id, name, description string, ops []budget.Op,
) (budget.Scenario, error) {
	var saved budget.Scenario
	addScenario := func(b budget.Budget) (budget.Budget, error) {
		nb, s, err := budget.AddScenario(b, name, description, ops)
		saved = s
		return nb, err
	}
	_, err := a.Store.Update(ctx, origin, id, store.AnyRevision, addScenario)
	return saved, err
}

func (a *App) Compare(ctx context.Context, id string, scenarios []string) ([]budget.Outcome, error) {
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return budget.Compare(r.Budget, scenarios, a.Localizer(ctx))
}

func (a *App) AdoptScenario(
	ctx context.Context, origin store.Origin, id, scenario string,
) (*View, error) {
	r, err := a.Store.Update(ctx, origin, id, store.AnyRevision, adoptScenario(scenario))
	if err != nil {
		return nil, err
	}
	return a.view(ctx, r)
}

func (a *App) DeleteScenario(ctx context.Context, origin store.Origin, id, scenario string) error {
	_, err := a.Store.Update(ctx, origin, id, store.AnyRevision, removeScenario(scenario))
	return err
}

func replaceWith(b budget.Budget) store.EditFunc {
	return func(budget.Budget) (budget.Budget, error) { return b, nil }
}

func applyOps(ops []budget.Op) store.EditFunc {
	return func(b budget.Budget) (budget.Budget, error) { return budget.Apply(b, ops...) }
}

func adoptScenario(scenario string) store.EditFunc {
	return func(b budget.Budget) (budget.Budget, error) { return budget.AdoptScenario(b, scenario) }
}

func removeScenario(scenario string) store.EditFunc {
	return func(b budget.Budget) (budget.Budget, error) { return budget.RemoveScenario(b, scenario) }
}
