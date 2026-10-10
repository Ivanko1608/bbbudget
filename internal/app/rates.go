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
	var partial *rates.PartialError
	if err != nil && !errors.As(err, &partial) {
		return nil, &RateError{err}
	}
	out := make([]RateUpdate, len(codes))
	for i, c := range codes {
		out[i] = RateUpdate{Code: c, Source: source}
		if v, ok := q[c]; ok {
			out[i].New, out[i].From, out[i].Date = money.RoundSignificant(v.PerBase, rateDigits), v.From, v.Date
		} else if partial != nil {
			out[i].Error = fmt.Sprintf("could not get a %s rate right now (%v); try again later", c, partial.Err)
		} else {
			out[i].Error = fmt.Sprintf("%s has no %s rate for %s", source, c, base)
		}
	}
	return out, nil
}

// RefreshRates fetches fresh rates for a budget's currencies and saves them.
// With no codes it refreshes every currency whose source isn't manual. Naming
// a manual currency refreshes it anyway and switches it to the default source.
// Currencies that fail keep their old rate and are reported with Error set; if
// all fail nothing is saved and a *RateError is returned. A currency edited
// while its rate was being fetched (say, a rate typed by hand) is left as the
// edit made it.
func (a *App) RefreshRates(
	ctx context.Context, origin store.Origin, id string, codes []string,
) (*View, []RateUpdate, error) {
	r, targets, updates, err := a.fetchRates(ctx, id, codes)
	if err != nil || len(targets) == 0 {
		if err != nil {
			return nil, updates, err
		}
		v, err := view(r)
		return v, updates, err
	}
	fetched := map[string]budget.CurrencyRate{} // each currency as it was when fetching started
	for _, c := range r.Budget.Currencies {
		fetched[c.Code] = c
	}
	var skipped map[string]bool
	updateRates := func(cur budget.Budget) (budget.Budget, error) {
		if cur.BaseCurrency != r.Budget.BaseCurrency {
			return budget.Budget{}, fmt.Errorf("the base currency changed to %s while rates were fetched; refresh again", cur.BaseCurrency)
		}
		skipped = map[string]bool{}
		var ops []budget.Op
		for i, u := range updates {
			if u.Error != "" {
				continue
			}
			j := slices.IndexFunc(cur.Currencies, func(c budget.CurrencyRate) bool { return c.Code == u.Code })
			if j < 0 || changedSince(fetched[u.Code], cur.Currencies[j]) {
				skipped[u.Code] = true
				continue
			}
			src := targets[i].Source
			ops = append(ops, budget.NewOp("set_currency", u.Code, budget.CurrencyPatch{PerBase: &updates[i].New, Source: &src, RateDate: &updates[i].Date, RateFrom: &updates[i].From}))
		}
		return budget.Apply(cur, ops...)
	}
	saved, err := a.Store.Update(ctx, origin, id, store.AnyRevision, updateRates)
	if err != nil {
		return nil, updates, err
	}
	for i, u := range updates {
		if skipped[u.Code] {
			updates[i].Error = "changed or removed while the rate was being fetched; left as it is"
		}
	}
	v, err := view(saved)
	return v, updates, err
}

// PreviewRates is RefreshRates without saving: the same currencies, sources
// and providers, so a dry run shows exactly what a refresh would do.
func (a *App) PreviewRates(ctx context.Context, id string, codes []string) ([]RateUpdate, error) {
	_, _, updates, err := a.fetchRates(ctx, id, codes)
	return updates, err
}

// changedSince reports whether a currency's rate or source was edited.
func changedSince(was, now budget.CurrencyRate) bool {
	return was.Source != now.Source || was.PerBase.Cmp(now.PerBase) != 0
}

// fetchRates picks the currencies to refresh and fetches them, outside any
// store lock (the network can be slow), one request per source. targets[i]
// is the currency (with the source it is fetched from) behind updates[i].
// It fails only if nothing at all could be fetched.
func (a *App) fetchRates(ctx context.Context, id string, codes []string) (store.Record, []budget.CurrencyRate, []RateUpdate, error) {
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return r, nil, nil, err
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
				return r, nil, nil, &budget.NoMatchError{Kind: "currency", Ref: code}
			}
			c := b.Currencies[i]
			if c.IsManual() {
				c.Source = a.defaultSource()
			}
			targets = append(targets, c)
		}
	}
	if len(targets) == 0 {
		return r, nil, []RateUpdate{}, nil
	}
	if s, err := a.Settings(); err != nil {
		return r, nil, nil, err
	} else if !s.FetchRates {
		return r, nil, nil, ErrRatesOff
	}

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
	updates := make([]RateUpdate, len(targets))
	ok := false
	for i, c := range targets {
		u := got[c.Code]
		u.Code, u.Source, u.Old = c.Code, c.Source, c.PerBase
		updates[i] = u
		ok = ok || u.Error == ""
	}
	if !ok {
		if len(fetchErrs) > 0 {
			return r, nil, updates, &RateError{errors.Join(fetchErrs...)}
		}
		return r, nil, updates, &RateError{errors.New(updates[0].Error)}
	}
	return r, targets, updates, nil
}

// SetCurrency adds or edits a currency. A new currency without a per_base,
// or one switched to a non-manual source without one, gets its rate fetched
// (a new currency gets the default source from the settings).
func (a *App) SetCurrency(
	ctx context.Context, origin store.Origin, id, code string, patch budget.CurrencyPatch,
) (*View, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if patch.Source != nil && *patch.Source == "" {
		patch.Source = nil // "" is "not given", not a silent "manual" on a fetched rate
	}
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
	setCurrency := budget.NewOp("set_currency", code, patch)
	return a.Apply(ctx, origin, id, store.AnyRevision, []budget.Op{setCurrency})
}
