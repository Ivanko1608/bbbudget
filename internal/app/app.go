// Package app is the use-case layer shared by the HTTP API, the MCP server
// and the CLI. Transports stay thin: they decode input, call App, encode output.
package app

import (
	"context"
	"errors"
	"time"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
	"bbbudget/internal/templates"
)

type App struct {
	Store     store.Store
	Templates *templates.Registry
	Now       func() time.Time
}

// New returns an App. A nil registry means built-in templates only.
func New(s store.Store, t *templates.Registry) *App {
	if t == nil {
		t = templates.Builtin()
	}
	return &App{Store: s, Templates: t, Now: time.Now}
}

// View is a stored budget together with its computed report.
type View struct {
	ID        string         `json:"id"`
	Revision  int64          `json:"revision"`
	UpdatedAt time.Time      `json:"updated_at"`
	Budget    budget.Budget  `json:"budget"`
	Report    *budget.Report `json:"report"`
}

func view(r store.Record) (*View, error) {
	rep, err := budget.Analyze(r.Budget)
	if err != nil {
		return nil, err
	}
	return &View{ID: r.ID, Revision: r.Revision, UpdatedAt: r.UpdatedAt, Budget: r.Budget, Report: rep}, nil
}

func (a *App) List(ctx context.Context) ([]store.Meta, error) { return a.Store.List(ctx) }

// Create makes a new budget from a template ("" means blank). Name defaults to the id.
func (a *App) Create(ctx context.Context, id, name, template string) (*View, error) {
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
	r, err := a.Store.Create(ctx, id, b)
	if err != nil {
		return nil, err
	}
	return view(r)
}

// Import stores a full budget document as a new budget.
func (a *App) Import(ctx context.Context, id string, b budget.Budget) (*View, error) {
	r, err := a.Store.Create(ctx, id, b)
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
func (a *App) Replace(ctx context.Context, id string, revision int64, b budget.Budget) (*View, error) {
	r, err := a.Store.Update(ctx, id, revision, func(budget.Budget) (budget.Budget, error) { return b, nil })
	if err != nil {
		return nil, err
	}
	return view(r)
}

// Apply runs ops against the latest budget and saves the result atomically.
// Pass store.AnyRevision to apply on top of whatever is current.
func (a *App) Apply(ctx context.Context, id string, revision int64, ops []budget.Op) (*View, error) {
	r, err := a.Store.Update(ctx, id, revision, func(b budget.Budget) (budget.Budget, error) { return budget.Apply(b, ops...) })
	if err != nil {
		return nil, err
	}
	return view(r)
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
	rep, err := budget.Analyze(nb)
	return nb, rep, err
}

// SetDebt updates the debt matching ref, or creates one named ref if none
// matches. Both happen inside one store transaction.
func (a *App) SetDebt(ctx context.Context, id, ref string, patch budget.DebtPatch) (*View, error) {
	r, err := a.Store.Update(ctx, id, store.AnyRevision, func(b budget.Budget) (budget.Budget, error) {
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
		return budget.Apply(b, budget.NewOp("add_debt", "", d))
	})
	if err != nil {
		return nil, err
	}
	return view(r)
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

func (a *App) SaveScenario(ctx context.Context, id, name, description string, ops []budget.Op) (budget.Scenario, error) {
	var saved budget.Scenario
	_, err := a.Store.Update(ctx, id, store.AnyRevision, func(b budget.Budget) (budget.Budget, error) {
		nb, s, err := budget.AddScenario(b, name, description, ops)
		saved = s
		return nb, err
	})
	return saved, err
}

func (a *App) Compare(ctx context.Context, id string, scenarios []string) ([]budget.Outcome, error) {
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return budget.Compare(r.Budget, scenarios)
}

func (a *App) AdoptScenario(ctx context.Context, id, scenario string) (*View, error) {
	r, err := a.Store.Update(ctx, id, store.AnyRevision, func(b budget.Budget) (budget.Budget, error) {
		return budget.AdoptScenario(b, scenario)
	})
	if err != nil {
		return nil, err
	}
	return view(r)
}

func (a *App) DeleteScenario(ctx context.Context, id, scenario string) error {
	_, err := a.Store.Update(ctx, id, store.AnyRevision, func(b budget.Budget) (budget.Budget, error) {
		return budget.RemoveScenario(b, scenario)
	})
	return err
}
