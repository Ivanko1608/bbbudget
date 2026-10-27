package app

import (
	"cmp"
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
	Source budget.RateSource `json:"source"`
	From   budget.RateSource `json:"from,omitempty"`
	Date   string            `json:"date,omitempty"`
	Old    money.Decimal     `json:"old,omitzero"`
	New    money.Decimal     `json:"new,omitzero"`
	// Error is set when this currency could not be priced; the rest still update.
	Error string `json:"error,omitempty"`
	// Detail is the full error behind Error, for logs. It never goes to clients.
	Detail string `json:"-"`
}

// RatesOffMessage is what clients are told when fetching rates is turned off.
const RatesOffMessage = "fetching exchange rates is turned off in Settings; " +
	"type rates by hand or turn it on"

// RateFetchFailedMessage is what clients are told when a rate provider fails.
const RateFetchFailedMessage = "couldn't reach the exchange rate provider"

// RateError means no rate could be fetched at all.
type RateError struct{ Err error }

func (e *RateError) Error() string { return "fetching exchange rates: " + e.Err.Error() }
func (e *RateError) Unwrap() error { return e.Err }

// ErrBadInput marks a request the caller got wrong (unknown currency or source).
var ErrBadInput = errors.New("bad input")

func (a *App) requireFetching() error {
	s, err := a.Settings()
	if err != nil {
		return err
	}
	if !s.FetchRates {
		return ErrRatesOff
	}
	return nil
}

func (a *App) provider(source budget.RateSource) (rates.Provider, error) {
	if err := a.requireFetching(); err != nil {
		return nil, err
	}
	if source == "" || source == budget.RateSourceManual {
		source = budget.RateSourceAuto
	}
	p, ok := a.Rates[source]
	if !ok {
		sources := budget.FormatRateSources()
		return nil, NewClientError(
			ErrBadInput, "unknown rate source %q (use one of %s)", source, sources)
	}
	return p, nil
}

// Quote fetches current rates without touching any budget: codes per 1 base,
// from source ("" means auto). Codes no provider could price come back with Error set.
func (a *App) Quote(
	ctx context.Context,
	base string,
	source budget.RateSource,
	codes []string,
) ([]RateUpdate, error) {
	if _, err := money.Lookup(base); err != nil {
		return nil, NewClientErrorFrom(ErrBadInput, err)
	}
	p, err := a.provider(source)
	if err != nil {
		return nil, err
	}
	if source == "" {
		source = budget.RateSourceAuto
	}
	codes = slices.Clone(codes)
	for i, c := range codes {
		codes[i] = strings.ToUpper(strings.TrimSpace(c))
		if _, err := money.Lookup(codes[i]); err != nil {
			return nil, NewClientErrorFrom(ErrBadInput, err)
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
			out[i].Error = fmt.Sprintf("could not get a %s rate right now; try again later", c)
			out[i].Detail = partial.Err.Error()
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
	original, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	updates, err := a.fetchRates(ctx, original.Budget, codes)
	if err != nil {
		return nil, updates, err
	}
	var v *View
	if len(updates) == 0 {
		v, err = view(original)
		return v, updates, err
	}
	var skipped map[string]bool
	updateRates := func(current budget.Budget) (budget.Budget, error) {
		if current.BaseCurrency != original.Budget.BaseCurrency {
			return budget.Budget{}, fmt.Errorf("the base currency changed to %s while rates were fetched; refresh again", current.BaseCurrency)
		}
		skipped = map[string]bool{}
		var ops []budget.Op
		for i, update := range updates {
			if update.Error != "" {
				continue
			}
			if isCurrencyChangedOrRemoved(update.Code, original.Budget, current) {
				skipped[update.Code] = true
				continue
			}
			patch := budget.CurrencyPatch{
				PerBase:  &updates[i].New,
				Source:   &updates[i].Source,
				RateDate: &updates[i].Date,
				RateFrom: &updates[i].From,
			}
			ops = append(ops, budget.NewOp(budget.OpSetCurrency, update.Code, patch))
		}
		return budget.Apply(current, ops...)
	}
	v, err = a.update(ctx, origin, id, store.AnyRevision, updateRates)
	if err != nil {
		return nil, updates, err
	}
	for i, update := range updates {
		if skipped[update.Code] {
			updates[i].Error = "changed or removed while the rate was being fetched; left as it is"
		}
	}
	return v, updates, nil
}

// PreviewRates is RefreshRates without saving: the same currencies, sources
// and providers, so a dry run shows exactly what a refresh would do.
func (a *App) PreviewRates(ctx context.Context, id string, codes []string) ([]RateUpdate, error) {
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return a.fetchRates(ctx, r.Budget, codes)
}

// isCurrencyChangedOrRemoved reports whether currentBudget edited or dropped the
// currency since originalBudget was read.
func isCurrencyChangedOrRemoved(
	code string, originalBudget, currentBudget budget.Budget,
) bool {
	originalRate, _ := originalBudget.GetCurrencyRate(code)
	currentRate, found := currentBudget.GetCurrencyRate(code)
	if !found {
		return true
	}
	sourceChanged := originalRate.Source != currentRate.Source
	rateChanged := originalRate.PerBase.Cmp(currentRate.PerBase) != 0
	return sourceChanged || rateChanged
}

// fetchRates fetches fresh rates for the budget's currencies named in codes, or for every
// non-manual currency when codes is empty, one request per source. It runs
// outside any store lock because the network can be slow. It fails only if
// nothing at all could be fetched.
func (a *App) fetchRates(
	ctx context.Context, original budget.Budget, codes []string,
) ([]RateUpdate, error) {
	var targets []budget.CurrencyRate
	if len(codes) == 0 {
		for _, c := range original.Currencies {
			if !c.IsManual() {
				targets = append(targets, c)
			}
		}
	} else {
		for _, code := range codes {
			code = strings.ToUpper(strings.TrimSpace(code))
			currency, found := original.GetCurrencyRate(code)
			if !found {
				return nil, &budget.NoMatchError{Kind: "currency", Ref: code}
			}
			if currency.IsManual() {
				currency.Source = a.defaultRateSource()
			}
			targets = append(targets, currency)
		}
	}
	if len(targets) == 0 {
		return []RateUpdate{}, nil
	}
	if err := a.requireFetching(); err != nil {
		return nil, err
	}

	byRateSource := map[budget.RateSource][]string{}
	for _, c := range targets {
		byRateSource[c.Source] = append(byRateSource[c.Source], c.Code)
	}
	got := map[string]RateUpdate{}
	var fetchErrs []error
	for src, cs := range byRateSource {
		q, err := a.Quote(ctx, original.BaseCurrency, src, cs)
		if err != nil {
			fetchErrs = append(fetchErrs, err)
			for _, c := range cs {
				got[c] = failedUpdate(err)
			}
			continue
		}
		for _, u := range q {
			got[u.Code] = u
		}
	}
	updates := make([]RateUpdate, len(targets))
	anyFetched := false
	for i, c := range targets {
		u := got[c.Code]
		u.Code, u.Source, u.Old = c.Code, c.Source, c.PerBase
		updates[i] = u
		anyFetched = anyFetched || u.Error == ""
	}
	if !anyFetched {
		if len(fetchErrs) > 0 {
			return updates, &RateError{errors.Join(fetchErrs...)}
		}
		return updates, &RateError{errors.New(cmp.Or(updates[0].Detail, updates[0].Error))}
	}
	return updates, nil
}

// failedUpdate is the outcome of a currency whose quote failed with err. Only
// errors written for the client keep their text.
func failedUpdate(err error) RateUpdate {
	var clientErr *ClientError
	switch {
	case errors.As(err, &clientErr):
		return RateUpdate{Error: clientErr.Message}
	case errors.Is(err, ErrRatesOff):
		return RateUpdate{Error: RatesOffMessage}
	default:
		return RateUpdate{Error: RateFetchFailedMessage, Detail: err.Error()}
	}
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
	_, exists := r.Budget.GetCurrencyRate(code)
	if patch.PerBase == nil && (!exists || patch.Source != nil) {
		src := a.defaultRateSource()
		if patch.Source != nil {
			src = *patch.Source
		}
		if src != budget.RateSourceManual {
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
	setCurrency := budget.NewOp(budget.OpSetCurrency, code, patch)
	return a.Apply(ctx, origin, id, store.AnyRevision, []budget.Op{setCurrency})
}
