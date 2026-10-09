package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"bbbudget/internal/budget"
)

func ptr[T any](v T) *T { return &v }

func TestMissingFileGivesDefaults(t *testing.T) {
	for _, path := range []string{"", filepath.Join(t.TempDir(), "nope", "settings.json")} {
		s, err := Load(path)
		if err != nil || s != Defaults() {
			t.Fatalf("Load(%q) = %+v, %v", path, s, err)
		}
	}
	if d := Defaults(); d.FetchRates || d.DefaultRateSource != "auto" || d.AutoRefresh != RefreshOff || d.Validate() != nil {
		t.Fatalf("defaults: %+v", d)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conf", "settings.json") // directory is created
	want := Settings{FetchRates: false, DefaultRateSource: "ecb", AutoRefresh: RefreshDaily, CoinGeckoAPIKey: "CG-abc123"}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
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
	if err != nil || s.FetchRates || s.DefaultRateSource != "auto" || s.AutoRefresh != RefreshHourly {
		t.Fatalf("got %+v, %v", s, err)
	}
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
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: want an error naming the file, got %v", name, err)
		}
		if s != Defaults() {
			t.Errorf("%s: a broken file must not yield half-read settings: %+v", name, s)
		}
	}
}

func TestApply(t *testing.T) {
	s, err := Defaults().Apply(Patch{DefaultRateSource: ptr(" coingecko "), CoinGeckoAPIKey: ptr("k1"), FetchRates: ptr(false)})
	if err != nil || s.DefaultRateSource != "coingecko" || s.CoinGeckoAPIKey != "k1" || s.FetchRates || s.AutoRefresh != RefreshOff {
		t.Fatalf("got %+v, %v", s, err)
	}
	if s, _ = s.Apply(Patch{CoinGeckoAPIKey: ptr("")}); s.CoinGeckoAPIKey != "" {
		t.Fatal("an empty key removes it")
	}
	for _, p := range []Patch{{DefaultRateSource: ptr("yahoo")}, {AutoRefresh: ptr("sometimes")}, {CoinGeckoAPIKey: ptr("two words")}} {
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

// The settings package can't import budget (it sits below it), so its copy of
// the source names is checked here.
func TestRateSourcesMatchBudget(t *testing.T) {
	if !slices.Equal(slices.Sorted(slices.Values(RateSources)), slices.Sorted(slices.Values(budget.RateSources))) {
		t.Fatalf("settings %v vs budget %v", RateSources, budget.RateSources)
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
