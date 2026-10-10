// Package settings holds app-wide preferences: the ones that belong to the
// person running bbbudget rather than to any one budget. They live in one
// JSON file outside the repository (it can hold an API key).
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Auto-refresh intervals.
const (
	RefreshOff    = "off"
	RefreshHourly = "hourly"
	RefreshDaily  = "daily"
)

// Settings are the app-wide preferences. The zero value is not valid; start
// from Defaults.
type Settings struct {
	// FetchRates allows contacting exchange-rate providers at all. Off means
	// fully offline: every rate is typed by hand.
	FetchRates bool `json:"fetch_rates"`
	// DefaultRateSource is the source a new currency gets ("auto", "ecb",
	// "currency-api", "coingecko" or "manual").
	DefaultRateSource string `json:"default_rate_source"`
	// AutoRefresh refreshes every budget's non-manual rates while `bbbudget
	// serve` runs: "off", "hourly" or "daily".
	AutoRefresh string `json:"auto_refresh"`
	// CoinGeckoAPIKey is an optional free demo key that raises CoinGecko's
	// rate limit. Never sent back to the UI; only whether it is set.
	CoinGeckoAPIKey string `json:"coingecko_api_key,omitempty"`
}

// Defaults are used when there is no settings file.
func Defaults() Settings {
	return Settings{FetchRates: false, DefaultRateSource: "auto", AutoRefresh: RefreshOff}
}

// RateSources is the set DefaultRateSource must come from. The app checks it
// against budget.RateSources in a test; this package depends on nothing.
var RateSources = []string{"manual", "auto", "ecb", "currency-api", "coingecko"}

// Interval is AutoRefresh as a duration; zero means off.
func (s Settings) Interval() time.Duration {
	switch s.AutoRefresh {
	case RefreshHourly:
		return time.Hour
	case RefreshDaily:
		return 24 * time.Hour
	}
	return 0
}

// Validate reports every bad field.
func (s Settings) Validate() error {
	var errs []error
	if !slices.Contains(RateSources, s.DefaultRateSource) {
		errs = append(errs, fmt.Errorf("default_rate_source must be one of %s", strings.Join(RateSources, ", ")))
	}
	if !slices.Contains([]string{RefreshOff, RefreshHourly, RefreshDaily}, s.AutoRefresh) {
		errs = append(errs, errors.New("auto_refresh must be off, hourly or daily"))
	}
	if len(s.CoinGeckoAPIKey) > 200 || strings.ContainsAny(s.CoinGeckoAPIKey, " \t\r\n") {
		errs = append(errs, errors.New("coingecko_api_key must be a single token without spaces"))
	}
	return errors.Join(errs...)
}

// Patch changes some settings; nil fields stay as they are. An empty
// CoinGeckoAPIKey removes the key.
type Patch struct {
	FetchRates        *bool   `json:"fetch_rates,omitempty"`
	DefaultRateSource *string `json:"default_rate_source,omitempty"`
	AutoRefresh       *string `json:"auto_refresh,omitempty"`
	CoinGeckoAPIKey   *string `json:"coingecko_api_key,omitempty"`
}

// Apply returns s with p applied, validated.
func (s Settings) Apply(p Patch) (Settings, error) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	if p.FetchRates != nil {
		s.FetchRates = *p.FetchRates
	}
	set(&s.DefaultRateSource, p.DefaultRateSource)
	set(&s.AutoRefresh, p.AutoRefresh)
	set(&s.CoinGeckoAPIKey, p.CoinGeckoAPIKey)
	return s, s.Validate()
}

// DefaultPath is $BBBUDGET_SETTINGS, else settings.json in the user's config
// directory (~/.config/bbbudget/settings.json).
func DefaultPath() string {
	if p := os.Getenv("BBBUDGET_SETTINGS"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "bbbudget", "settings.json")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "bbbudget", "settings.json")
	}
	return ""
}

// Load reads the file at path. A missing file (or an empty path) yields the
// defaults; fields missing from the file keep their defaults.
func Load(path string) (Settings, error) {
	s := Defaults()
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return Defaults(), fmt.Errorf("%s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return Defaults(), fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Save writes s to path atomically, readable only by the owner (it may hold an API key).
func Save(path string, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if path == "" {
		return errors.New("no settings file location (set BBBUDGET_SETTINGS)")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings.*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
