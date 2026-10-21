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
	Store store.Store
	// Notes keeps the person's notes next to the budgets.
	Notes     store.NoteStore
	Templates *templates.Registry
	// Rates are the exchange-rate providers by source name (see budget.RateSources).
	Rates map[budget.RateSource]rates.Provider
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
	// Before is the budget as it was just before this write; nil for reads.
	Before *budget.Budget `json:"-"`
}

// view analyzes a stored budget, with its text in English.
func view(r store.Record) (*View, error) {
	rep, err := budget.Analyze(r.Budget)
	if err != nil {
		return nil, err
	}
	return &View{ID: r.ID, Revision: r.Revision, UpdatedAt: r.UpdatedAt, Budget: r.Budget, Report: rep}, nil
}

// update saves edit and returns the new view together with the budget it
// replaced. The old budget is copied inside the store transaction, so a
// concurrent write can't be mistaken for part of this one.
func (a *App) update(
	ctx context.Context, origin store.Origin, id string, revision int64, edit store.EditFunc,
) (*View, error) {
	var before budget.Budget
	keepBefore := func(current budget.Budget) (budget.Budget, error) {
		copied, err := budget.Clone(current)
		if err != nil {
			return budget.Budget{}, err
		}
		before = copied
		return edit(current)
	}
	r, err := a.Store.Update(ctx, origin, id, revision, keepBefore)
	if err != nil {
		return nil, err
	}
	v, err := view(r)
	if err != nil {
		return nil, err
	}
	v.Before = &before
	return v, nil
}

func (a *App) List(ctx context.Context) ([]store.Meta, error) { return a.Store.List(ctx) }

// Create makes a new budget from a template ("" means blank), with the
// template's category and pot names in lang. Name defaults to the id.
func (a *App) Create(
	ctx context.Context, origin store.Origin, lang *i18n.Localizer, id, name, template string,
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
	localizeTemplate(&b, lang)
	r, err := a.Store.Create(ctx, origin, id, b)
	if err != nil {
		return nil, err
	}
	return view(r)
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
	return view(r)
}

func (a *App) Get(ctx context.Context, id string) (*View, error) {
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return view(r)
}

func (a *App) Delete(ctx context.Context, id string) error { return a.Store.Delete(ctx, id) }

// Replace overwrites the whole document (the web editor's save), guarded by revision.
func (a *App) Replace(
	ctx context.Context, origin store.Origin, id string, revision int64, b budget.Budget,
) (*View, error) {
	return a.update(ctx, origin, id, revision, replaceWith(b))
}

// Apply runs ops against the latest budget and saves the result atomically.
// Pass store.AnyRevision to apply on top of whatever is current.
func (a *App) Apply(
	ctx context.Context, origin store.Origin, id string, revision int64, ops []budget.Op,
) (*View, error) {
	return a.update(ctx, origin, id, revision, applyOps(ops))
}

// WhatIf previews ops without saving anything. It returns the hypothetical
// budget and its report, in English, with Before set to the saved budget.
func (a *App) WhatIf(ctx context.Context, id string, ops []budget.Op) (*View, error) {
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	hypothetical, err := budget.Apply(r.Budget, ops...)
	if err != nil {
		return nil, err
	}
	preview := r
	preview.Budget = hypothetical
	v, err := view(preview)
	if err != nil {
		return nil, err
	}
	v.Before = &r.Budget
	return v, nil
}

// SetDebt updates the debt matching ref, or creates one named ref if none
// matches. Both happen inside one store transaction.
func (a *App) SetDebt(
	ctx context.Context, origin store.Origin, id, ref string, patch budget.DebtPatch,
) (*View, error) {
	return a.update(ctx, origin, id, store.AnyRevision, setOrAddDebt(ref, patch))
}

// setOrAddDebt updates the debt matching ref, or adds one named ref.
func setOrAddDebt(ref string, patch budget.DebtPatch) store.EditFunc {
	return func(b budget.Budget) (budget.Budget, error) {
		_, err := budget.FindDebt(b, ref)
		var nm *budget.NoMatchError
		if !errors.As(err, &nm) {
			return budget.Apply(b, budget.NewOp(budget.OpSetDebt, ref, patch))
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
		ops := []budget.Op{budget.NewOp(budget.OpAddDebt, "", d)}
		if patch.PayoffMonth != nil || patch.PayoffFrom != nil {
			payoff := budget.DebtPatch{PayoffMonth: patch.PayoffMonth, PayoffFrom: patch.PayoffFrom}
			ops = append(ops, budget.NewOp(budget.OpSetDebt, ref, payoff))
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
	return budget.Analyze(nb)
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

// Compare projects the baseline and scenarios side by side, with labels in lang.
func (a *App) Compare(
	ctx context.Context, lang *i18n.Localizer, id string, scenarios []string,
) ([]budget.Outcome, error) {
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return budget.Compare(r.Budget, scenarios, lang)
}

func (a *App) AdoptScenario(
	ctx context.Context, origin store.Origin, id, scenario string,
) (*View, error) {
	return a.update(ctx, origin, id, store.AnyRevision, adoptScenario(scenario))
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
