package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"bbbudget/internal/budget"
)

func ptr[T any](v T) *T { return &v }

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func TestMissingFileGivesDefaults(t *testing.T) {
	for _, path := range []string{"", filepath.Join(t.TempDir(), "nope", "settings.json")} {
		s, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%q): %v", path, err)
		}
		eq(t, "Load("+path+")", s, Defaults())
	}
	defaults := Defaults()
	eq(t, "default FetchRates", defaults.FetchRates, false)
	eq(t, "default DefaultRateSource", defaults.DefaultRateSource, "auto")
	eq(t, "default AutoRefresh", defaults.AutoRefresh, RefreshOff)
	if err := defaults.Validate(); err != nil {
		t.Fatalf("defaults do not validate: %v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conf", "settings.json") // directory is created
	want := Settings{
		FetchRates:        false,
		DefaultRateSource: "ecb",
		AutoRefresh:       RefreshDaily,
		CoinGeckoAPIKey:   "CG-abc123",
		AssistantProvider: AssistantOpenAICompatible,
		AssistantModel:    "llama3.1",
		AssistantURL:      "http://127.0.0.1:11434/v1",
		AssistantAPIKey:   "sk-test",
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "loaded settings", got, want)
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("settings file mode %v: it can hold an API key", fi.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestPartialFileKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"auto_refresh": "hourly"}`), 0o600)
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "FetchRates", s.FetchRates, false)
	eq(t, "DefaultRateSource", s.DefaultRateSource, "auto")
	eq(t, "AutoRefresh", s.AutoRefresh, RefreshHourly)
}

func TestBadFilesAreErrors(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": `{"fetch_rate": true}`,
		"bad source":    `{"default_rate_source": "google"}`,
		"bad interval":  `{"auto_refresh": "weekly"}`,
		"not json":      `fetch_rates = true`,
	} {
		path := filepath.Join(t.TempDir(), "settings.json")
		os.WriteFile(path, []byte(body), 0o600)
		s, err := Load(path)
		if err == nil {
			t.Errorf("%s: want an error", name)
		} else if !strings.Contains(err.Error(), path) {
			t.Errorf("%s: want an error naming the file, got %v", name, err)
		}
		if s != Defaults() {
			t.Errorf("%s: a broken file must not yield half-read settings: %+v", name, s)
		}
	}
}

func TestApply(t *testing.T) {
	s, err := Defaults().Apply(Patch{DefaultRateSource: ptr(budget.RateSource(" coingecko ")), CoinGeckoAPIKey: ptr("k1"), FetchRates: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "DefaultRateSource", s.DefaultRateSource, "coingecko")
	eq(t, "CoinGeckoAPIKey", s.CoinGeckoAPIKey, "k1")
	eq(t, "FetchRates", s.FetchRates, false)
	eq(t, "AutoRefresh", s.AutoRefresh, RefreshOff)
	if s, _ = s.Apply(Patch{CoinGeckoAPIKey: ptr("")}); s.CoinGeckoAPIKey != "" {
		t.Fatal("an empty key removes it")
	}
	for _, p := range []Patch{{DefaultRateSource: ptr(budget.RateSource("yahoo"))}, {AutoRefresh: ptr("sometimes")}, {CoinGeckoAPIKey: ptr("two words")}} {
		if _, err := Defaults().Apply(p); err == nil {
			t.Errorf("accepted %+v", p)
		}
	}
	if err := Save(filepath.Join(t.TempDir(), "s.json"), Settings{DefaultRateSource: "nope"}); err == nil {
		t.Fatal("Save must refuse invalid settings")
	}
}

func TestInterval(t *testing.T) {
	for v, want := range map[string]time.Duration{RefreshOff: 0, RefreshHourly: time.Hour, RefreshDaily: 24 * time.Hour} {
		if got := (Settings{AutoRefresh: v}).Interval(); got != want {
			t.Errorf("%s: %v", v, got)
		}
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("BBBUDGET_SETTINGS", "/x/s.json")
	if DefaultPath() != "/x/s.json" {
		t.Fatal("BBBUDGET_SETTINGS")
	}
	t.Setenv("BBBUDGET_SETTINGS", "")
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	if DefaultPath() != filepath.Join("/cfg", "bbbudget", "settings.json") {
		t.Fatal(DefaultPath())
	}
}

func TestTheAssistantIsOffByDefault(t *testing.T) {
	defaults := Defaults()

	if defaults.AssistantProvider != AssistantOff {
		t.Errorf("provider: %s", defaults.AssistantProvider)
	}
}

func TestAssistantPatch(t *testing.T) {
	patch := Patch{
		AssistantProvider: ptr(AssistantAnthropic),
		AssistantModel:    ptr(" claude-opus-5-5 "),
		AssistantAPIKey:   ptr("sk-ant-test"),
	}

	patched, err := Defaults().Apply(patch)

	if err != nil {
		t.Fatal(err)
	}
	if patched.AssistantProvider != AssistantAnthropic {
		t.Errorf("provider: %s", patched.AssistantProvider)
	}
	if patched.AssistantModel != "claude-opus-5-5" {
		t.Errorf("model: %q", patched.AssistantModel)
	}
	if patched.AssistantAPIKey != "sk-ant-test" {
		t.Errorf("key: %q", patched.AssistantAPIKey)
	}
}

func TestBadAssistantSettingsAreRefused(t *testing.T) {
	cases := map[string]Patch{
		"unknown provider": {AssistantProvider: ptr(AssistantProvider("gemini"))},
		"model with space": {AssistantModel: ptr("llama 3")},
		"not a web URL":    {AssistantURL: ptr("file:///etc/passwd")},
		"URL without host": {AssistantURL: ptr("http://")},
		"key with space":   {AssistantAPIKey: ptr("two words")},
	}
	for name, patch := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Defaults().Apply(patch)

			if err == nil {
				t.Errorf("accepted %+v", patch)
			}
		})
	}
}
