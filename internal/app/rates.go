package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/rates"
	"bbbudget/internal/store"
)

// rateDigits is how many significant digits a fetched rate keeps when stored.
// Ten is far below any provider's real precision and far above what changes
// a projection.
const rateDigits = 10

// RateUpdate is the outcome of fetching one currency's rate.
type RateUpdate struct {
	Code string `json:"code"`
	// Source is the configured source; From is the provider that answered.
	Source string        `json:"source"`
	From   string        `json:"from,omitempty"`
	Date   string        `json:"date,omitempty"`
	Old    money.Decimal `json:"old,omitzero"`
	New    money.Decimal `json:"new,omitzero"`
	// Error is set when this currency could not be priced; the rest still update.
	Error string `json:"error,omitempty"`
}

// RateError means no rate could be fetched at all.
type RateError struct{ Err error }

func (e *RateError) Error() string { return "fetching exchange rates: " + e.Err.Error() }
func (e *RateError) Unwrap() error { return e.Err }

// ErrBadInput marks a request the caller got wrong (unknown currency or source).
var ErrBadInput = errors.New("bad input")

func (a *App) provider(source string) (rates.Provider, error) {
	if s, err := a.Settings(); err != nil {
		return nil, err
	} else if !s.FetchRates {
		return nil, ErrRatesOff
	}
	if source == "" || source == budget.SourceManual {
		source = budget.SourceAuto
	}
	p, ok := a.Rates[source]
	if !ok {
		return nil, fmt.Errorf("%w: unknown rate source %q (use one of %s)", ErrBadInput, source, strings.Join(budget.RateSources, ", "))
	}
	return p, nil
}

// Quote fetches current rates without touching any budget: codes per 1 base,
// from source ("" means auto). Codes no provider could price come back with Error set.
func (a *App) Quote(ctx context.Context, base, source string, codes []string) ([]RateUpdate, error) {
	if _, err := money.Lookup(base); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadInput, err)
	}
	p, err := a.provider(source)
	if err != nil {
		return nil, err
	}
	if source == "" {
		source = budget.SourceAuto
	}
	codes = slices.Clone(codes)
	for i, c := range codes {
		codes[i] = strings.ToUpper(strings.TrimSpace(c))
		if _, err := money.Lookup(codes[i]); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadInput, err)
		}
	}
	q, err := p.Quotes(ctx, base, codes)
	if err != nil {
		return nil, &RateError{err}
	}
	out := make([]RateUpdate, len(codes))
	for i, c := range codes {
		out[i] = RateUpdate{Code: c, Source: source}
		if v, ok := q[c]; ok {
			out[i].New, out[i].From, out[i].Date = money.RoundSignificant(v.PerBase, rateDigits), v.From, v.Date
		} else {
			out[i].Error = fmt.Sprintf("%s has no %s rate for %s", source, c, base)
		}
	}
	return out, nil
}

// RefreshRates fetches fresh rates for a budget's currencies and saves them.
// With no codes it refreshes every currency whose source isn't manual. Naming
// a manual currency refreshes it anyway and switches it to the default source. Currencies
// that fail keep their old rate and are reported with Error set; if all fail
// nothing is saved and a *RateError is returned.
func (a *App) RefreshRates(ctx context.Context, id string, codes []string) (*View, []RateUpdate, error) {
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	b := r.Budget
	var targets []budget.CurrencyRate
	if len(codes) == 0 {
		for _, c := range b.Currencies {
			if !c.IsManual() {
				targets = append(targets, c)
			}
		}
	} else {
		for _, code := range codes {
			code = strings.ToUpper(strings.TrimSpace(code))
			i := slices.IndexFunc(b.Currencies, func(c budget.CurrencyRate) bool { return c.Code == code })
			if i < 0 {
				return nil, nil, &budget.NoMatchError{Kind: "currency", Ref: code}
			}
			c := b.Currencies[i]
			if c.IsManual() {
				c.Source = a.defaultSource()
			}
			targets = append(targets, c)
		}
	}
	if len(targets) == 0 {
		v, err := view(r)
		return v, []RateUpdate{}, err
	}
	if s, err := a.Settings(); err != nil {
		return nil, nil, err
	} else if !s.FetchRates {
		return nil, nil, ErrRatesOff
	}

	// Fetch outside the store lock (the network can be slow), one request per source.
	bySource := map[string][]string{}
	for _, c := range targets {
		bySource[c.Source] = append(bySource[c.Source], c.Code)
	}
	got := map[string]RateUpdate{}
	var fetchErrs []error
	for src, cs := range bySource {
		q, err := a.Quote(ctx, b.BaseCurrency, src, cs)
		if err != nil {
			fetchErrs = append(fetchErrs, err)
			for _, c := range cs {
				got[c] = RateUpdate{Error: err.Error()}
			}
			continue
		}
		for _, u := range q {
			got[u.Code] = u
		}
	}
	updates := make([]RateUpdate, 0, len(targets))
	var ops []budget.Op
	for _, c := range targets {
		u := got[c.Code]
		u.Code, u.Source, u.Old = c.Code, c.Source, c.PerBase
		if u.Error == "" {
			src := c.Source
			ops = append(ops, budget.NewOp("set_currency", c.Code, budget.CurrencyPatch{PerBase: &u.New, Source: &src, RateDate: &u.Date, RateFrom: &u.From}))
		}
		updates = append(updates, u)
	}
	if len(ops) == 0 {
		if len(fetchErrs) > 0 {
			return nil, updates, &RateError{errors.Join(fetchErrs...)}
		}
		return nil, updates, &RateError{errors.New(updates[0].Error)}
	}

	saved, err := a.Store.Update(ctx, id, store.AnyRevision, func(cur budget.Budget) (budget.Budget, error) {
		if cur.BaseCurrency != b.BaseCurrency {
			return budget.Budget{}, fmt.Errorf("the base currency changed to %s while rates were fetched; refresh again", cur.BaseCurrency)
		}
		var keep []budget.Op
		for _, op := range ops { // a currency removed meanwhile stays removed
			if slices.ContainsFunc(cur.Currencies, func(c budget.CurrencyRate) bool { return c.Code == op.Target }) {
				keep = append(keep, op)
			}
		}
		return budget.Apply(cur, keep...)
	})
	if err != nil {
		return nil, updates, err
	}
	v, err := view(saved)
	return v, updates, err
}

// SetCurrency adds or edits a currency. A new currency without a per_base,
// or one switched to a non-manual source without one, gets its rate fetched
// (a new currency gets the default source from the settings).
func (a *App) SetCurrency(ctx context.Context, id, code string, patch budget.CurrencyPatch) (*View, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(r.Budget.Currencies, func(c budget.CurrencyRate) bool { return c.Code == code })
	if patch.PerBase == nil && (i < 0 || patch.Source != nil) {
		src := a.defaultSource()
		if patch.Source != nil {
			src = *patch.Source
		}
		if src != budget.SourceManual {
			q, err := a.Quote(ctx, r.Budget.BaseCurrency, src, []string{code})
			if err != nil {
				return nil, err
			}
			if q[0].Error != "" {
				return nil, &RateError{fmt.Errorf("%s; give per_base to set the rate by hand", q[0].Error)}
			}
			patch.PerBase, patch.Source, patch.RateDate, patch.RateFrom = &q[0].New, &src, &q[0].Date, &q[0].From
		}
	}
	return a.Apply(ctx, id, store.AnyRevision, []budget.Op{budget.NewOp("set_currency", code, patch)})
}
