// Package settings holds app-wide preferences: the ones that belong to the
// person running bbbudget rather than to any one budget. They live in one
// JSON file outside the repository (it can hold an API key).
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"bbbudget/internal/budget"
)

// Auto-refresh intervals.
const (
	RefreshOff    = "off"
	RefreshHourly = "hourly"
	RefreshDaily  = "daily"
)

// AssistantProvider is the model provider behind the in-app assistant.
type AssistantProvider string

const (
	// AssistantOff hides the assistant.
	AssistantOff AssistantProvider = "off"
	// AssistantAnthropic is Claude, through the Anthropic API.
	AssistantAnthropic AssistantProvider = "anthropic"
	// AssistantOpenAICompatible is any server with an OpenAI-compatible chat
	// completions API, such as Ollama.
	AssistantOpenAICompatible AssistantProvider = "openai-compatible"
)

// AssistantProviders is the set AssistantProvider must come from.
var AssistantProviders = []AssistantProvider{
	AssistantOff,
	AssistantAnthropic,
	AssistantOpenAICompatible,
}

// Longest values accepted for the free-text fields.
const (
	maxAPIKey    = 200
	maxModelName = 200
)

// Settings are the app-wide preferences. The zero value is not valid; start
// from Defaults.
type Settings struct {
	// FetchRates allows contacting exchange-rate providers at all. Off means
	// fully offline: every rate is typed by hand.
	FetchRates bool `json:"fetch_rates"`
	// DefaultRateSource is the source a new currency gets ("auto", "ecb",
	// "currency-api", "coingecko" or "manual").
	DefaultRateSource budget.RateSource `json:"default_rate_source"`
	// AutoRefresh refreshes every budget's non-manual rates while `bbbudget
	// serve` runs: "off", "hourly" or "daily".
	AutoRefresh string `json:"auto_refresh"`
	// CoinGeckoAPIKey is an optional free demo key that raises CoinGecko's
	// rate limit. Never sent back to the UI; only whether it is set.
	CoinGeckoAPIKey string `json:"coingecko_api_key,omitempty"`
	// Locale is the language for text: "auto" (the browser's language in the
	// UI, $LANG elsewhere) or a language tag such as "en" or "de". Empty
	// means auto.
	Locale string `json:"locale"`
	// AssistantProvider is where the in-app assistant's model runs.
	AssistantProvider AssistantProvider `json:"assistant_provider"`
	// AssistantModel names the model; empty means the provider's default.
	AssistantModel string `json:"assistant_model,omitempty"`
	// AssistantURL is the API root of an OpenAI-compatible server; empty
	// means Ollama on this machine.
	AssistantURL string `json:"assistant_url,omitempty"`
	// AssistantAPIKey is the key for the assistant's provider. Never sent
	// back to the UI; only whether it is set.
	AssistantAPIKey string `json:"assistant_api_key,omitempty"`
}

// Defaults are used when there is no settings file.
func Defaults() Settings {
	return Settings{
		FetchRates:        false,
		DefaultRateSource: budget.RateSourceAuto,
		AutoRefresh:       RefreshOff,
		Locale:            LocaleAuto,
		AssistantProvider: AssistantOff,
	}
}

// LocaleAuto picks the language from the reader's browser or environment.
const LocaleAuto = "auto"

// localeRe is the shape of a language tag. Which languages exist is up to
// the i18n package; a tag it lacks falls back to English.
var localeRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

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
	if !slices.Contains(budget.RateSources, s.DefaultRateSource) {
		sources := budget.FormatRateSources()
		errs = append(errs, fmt.Errorf("default_rate_source must be one of %s", sources))
	}
	if !slices.Contains([]string{RefreshOff, RefreshHourly, RefreshDaily}, s.AutoRefresh) {
		errs = append(errs, errors.New("auto_refresh must be off, hourly or daily"))
	}
	if s.Locale != LocaleAuto && s.Locale != "" && !localeRe.MatchString(s.Locale) {
		errs = append(errs, errors.New(`locale must be "auto" or a language tag such as "en" or "de"`))
	}
	if !isValidToken(s.CoinGeckoAPIKey, maxAPIKey) {
		errs = append(errs, errors.New("coingecko_api_key must be a single token without spaces"))
	}
	return errors.Join(append(errs, s.validateAssistant()...)...)
}

func (s Settings) validateAssistant() []error {
	var errs []error
	if !slices.Contains(AssistantProviders, s.AssistantProvider) {
		errs = append(errs,
			errors.New("assistant_provider must be off, anthropic or openai-compatible"))
	}
	if !isValidToken(s.AssistantModel, maxModelName) {
		errs = append(errs, errors.New("assistant_model must be a single name without spaces"))
	}
	if s.AssistantURL != "" && !isWebURL(s.AssistantURL) {
		errs = append(errs, errors.New("assistant_url must be an http or https URL"))
	}
	if !isValidToken(s.AssistantAPIKey, maxAPIKey) {
		errs = append(errs, errors.New("assistant_api_key must be a single token without spaces"))
	}
	return errs
}

// isValidToken reports whether text is empty or one word of at most limit bytes.
func isValidToken(text string, limit int) bool {
	return len(text) <= limit && !strings.ContainsAny(text, " \t\r\n")
}

func isWebURL(text string) bool {
	parsed, err := url.Parse(text)
	isWeb := parsed != nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
	return err == nil && isWeb && parsed.Host != ""
}

// Patch changes some settings; nil fields stay as they are. An empty key
// removes it.
type Patch struct {
	FetchRates        *bool              `json:"fetch_rates,omitempty"`
	DefaultRateSource *budget.RateSource `json:"default_rate_source,omitempty"`
	AutoRefresh       *string            `json:"auto_refresh,omitempty"`
	CoinGeckoAPIKey   *string            `json:"coingecko_api_key,omitempty"`
	Locale            *string            `json:"locale,omitempty"`

	AssistantProvider *AssistantProvider `json:"assistant_provider,omitempty"`
	AssistantModel    *string            `json:"assistant_model,omitempty"`
	AssistantURL      *string            `json:"assistant_url,omitempty"`
	AssistantAPIKey   *string            `json:"assistant_api_key,omitempty"`
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
	if p.DefaultRateSource != nil {
		s.DefaultRateSource = budget.RateSource(strings.TrimSpace(string(*p.DefaultRateSource)))
	}
	set(&s.AutoRefresh, p.AutoRefresh)
	set(&s.CoinGeckoAPIKey, p.CoinGeckoAPIKey)
	set(&s.Locale, p.Locale)
	if p.AssistantProvider != nil {
		s.AssistantProvider = *p.AssistantProvider
	}
	set(&s.AssistantModel, p.AssistantModel)
	set(&s.AssistantURL, p.AssistantURL)
	set(&s.AssistantAPIKey, p.AssistantAPIKey)
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
