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
)

// ErrRatesOff means fetching rates is turned off in the app settings.
var ErrRatesOff = errors.New("fetching exchange rates is turned off in Settings; type rates by hand or turn it on")

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
		return s, fmt.Errorf("%w: %v", ErrBadInput, err)
	}
	return ns, settings.Save(a.SettingsPath, ns)
}

// defaultSource is the source a currency gets when none is given. A "manual"
// default still fetches when a fetch is explicitly asked for.
func (a *App) defaultSource() string {
	s, err := a.Settings()
	if err != nil || s.DefaultRateSource == budget.SourceManual {
		return budget.SourceAuto
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
}

// AutoRefresh returns the last automatic refresh.
func (a *App) AutoRefresh() AutoRefreshStatus {
	a.auto.mu.Lock()
	defer a.auto.mu.Unlock()
	s := a.auto.status
	s.Failures = append([]string{}, s.Failures...)
	return s
}

// RefreshIfDue refreshes the rates of every budget when the auto-refresh
// interval has passed since the last run. It reports whether it ran.
func (a *App) RefreshIfDue(ctx context.Context) (bool, error) {
	s, err := a.Settings()
	if err != nil {
		return false, err
	}
	every := s.Interval()
	a.auto.mu.Lock()
	last := a.auto.status.LastRun
	a.auto.mu.Unlock()
	if every == 0 || !s.FetchRates || (!last.IsZero() && a.Now().Sub(last) < every) {
		return false, nil
	}
	list, err := a.List(ctx)
	if err != nil {
		return false, err
	}
	st := AutoRefreshStatus{LastRun: a.Now(), Failures: []string{}}
	for _, m := range list {
		_, updates, err := a.RefreshRates(ctx, m.ID, nil)
		if len(updates) > 0 {
			st.Budgets++
		}
		for _, u := range updates {
			if u.Error != "" {
				st.Failures = append(st.Failures, fmt.Sprintf("%s: %s: %s", m.ID, u.Code, u.Error))
			} else {
				st.Updated++
			}
		}
		if err != nil && len(updates) == 0 {
			st.Failures = append(st.Failures, fmt.Sprintf("%s: %v", m.ID, err))
		}
	}
	a.auto.mu.Lock()
	a.auto.status = st
	a.auto.mu.Unlock()
	return true, nil
}

// RunAutoRefresh calls RefreshIfDue every minute until ctx ends. The settings
// are re-read each time, so turning auto-refresh on or off needs no restart.
func (a *App) RunAutoRefresh(ctx context.Context, log *slog.Logger) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		ran, err := a.RefreshIfDue(ctx)
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
