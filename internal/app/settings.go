package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"bbbudget/internal/budget"
	"bbbudget/internal/settings"
	"bbbudget/internal/store"
)

// ErrRatesOff means fetching rates is turned off in the app settings.
var ErrRatesOff = errors.New(RatesOffMessage)

// Settings reads the app settings. They are read from disk on every call, so
// a change made by another process (the CLI, a second server) applies at once.
func (a *App) Settings() (settings.Settings, error) { return settings.Load(a.SettingsPath) }

// UpdateSettings applies a patch and saves it.
func (a *App) UpdateSettings(p settings.Patch) (settings.Settings, error) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	s, err := a.Settings()
	if err != nil {
		return s, err
	}
	ns, err := s.Apply(p)
	if err != nil {
		return s, NewClientErrorFrom(ErrBadInput, err)
	}
	return ns, settings.Save(a.SettingsPath, ns)
}

// defaultRateSource is the rate source a currency gets when none is given. A "manual"
// default still fetches when a fetch is explicitly asked for.
func (a *App) defaultRateSource() budget.RateSource {
	s, err := a.Settings()
	if err != nil || s.DefaultRateSource == budget.RateSourceManual {
		return budget.RateSourceAuto
	}
	return s.DefaultRateSource
}

// AutoRefreshStatus says what the last automatic refresh did.
type AutoRefreshStatus struct {
	LastRun  time.Time `json:"last_run,omitzero"`
	Budgets  int       `json:"budgets"`  // budgets with fetched rates
	Updated  int       `json:"updated"`  // currencies updated
	Failures []string  `json:"failures"` // "home: BTC: ..." per problem
}

type autoState struct {
	mu     sync.Mutex
	status AutoRefreshStatus
	// retrySoon is set when the last run fetched nothing but had failures
	// (the network wasn't up yet, a provider was down): try again after
	// retryAfter instead of a whole interval.
	retrySoon bool
}

// retryAfter is how soon an auto-refresh that fetched nothing is retried.
const retryAfter = 10 * time.Minute

// AutoRefresh returns the last automatic refresh.
func (a *App) AutoRefresh() AutoRefreshStatus {
	a.auto.mu.Lock()
	defer a.auto.mu.Unlock()
	s := a.auto.status
	s.Failures = append([]string{}, s.Failures...)
	return s
}

// autoRefresh is the origin of the rate updates RefreshIfDue makes.
var autoRefresh = store.Origin{Source: store.SourceAutoRefresh}

// autoRefreshFailedMessage is what the status says about a budget whose
// refresh failed for a reason a person can't act on. The error is logged.
const autoRefreshFailedMessage = "couldn't refresh the rates; see the server log"

// RefreshIfDue refreshes the rates of every budget when the auto-refresh
// interval has passed since the last run. It reports whether it ran. The
// errors behind the status's failures go to logger.
func (a *App) RefreshIfDue(ctx context.Context, logger *slog.Logger) (bool, error) {
	s, err := a.Settings()
	if err != nil {
		return false, err
	}
	every := s.Interval()
	a.auto.mu.Lock()
	last, wait := a.auto.status.LastRun, every
	if a.auto.retrySoon {
		wait = min(every, retryAfter)
	}
	a.auto.mu.Unlock()
	if every == 0 || !s.FetchRates || (!last.IsZero() && a.Now().Sub(last) < wait) {
		return false, nil
	}
	list, err := a.List(ctx)
	if err != nil {
		return false, err
	}
	st := AutoRefreshStatus{LastRun: a.Now(), Failures: []string{}}
	for _, m := range list {
		_, updates, err := a.RefreshRates(ctx, autoRefresh, m.ID, nil)
		if len(updates) > 0 {
			st.Budgets++
		}
		for _, u := range updates {
			if u.Error != "" {
				st.Failures = append(st.Failures, fmt.Sprintf("%s: %s: %s", m.ID, u.Code, u.Error))
				logRateDetail(logger, m.ID, u)
			} else {
				st.Updated++
			}
		}
		if err != nil && len(updates) == 0 {
			logger.Error("auto-refresh of a budget failed", "budget", m.ID, "err", err)
			st.Failures = append(st.Failures, fmt.Sprintf("%s: %s", m.ID, getFailureMessage(err)))
		}
	}
	a.auto.mu.Lock()
	a.auto.status = st
	a.auto.retrySoon = st.Updated == 0 && len(st.Failures) > 0
	a.auto.mu.Unlock()
	return true, nil
}

func logRateDetail(logger *slog.Logger, budgetID string, u RateUpdate) {
	if u.Detail != "" {
		logger.Error("auto-refresh of a rate failed",
			"budget", budgetID, "currency", u.Code, "detail", u.Detail)
	}
}

// getFailureMessage is the text of err when a client may hear about it, else
// autoRefreshFailedMessage.
func getFailureMessage(err error) string {
	message, isClientError := GetClientMessage(err)
	if !isClientError {
		return autoRefreshFailedMessage
	}

	return message
}

// RunAutoRefresh calls RefreshIfDue every minute until ctx ends. The settings
// are re-read each time, so turning auto-refresh on or off needs no restart.
func (a *App) RunAutoRefresh(ctx context.Context, log *slog.Logger) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		ran, err := a.RefreshIfDue(ctx, log)
		if err != nil {
			log.Warn("auto-refresh of rates", "err", err)
		} else if ran {
			st := a.AutoRefresh()
			log.Info("refreshed exchange rates", "budgets", st.Budgets, "currencies", st.Updated, "failures", len(st.Failures))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
