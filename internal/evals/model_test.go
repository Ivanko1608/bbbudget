package evals

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"bbbudget/internal/assistant"
	"bbbudget/internal/settings"
)

const (
	enableEnv = "BBBUDGET_EVAL"
	runsEnv   = "BBBUDGET_EVAL_RUNS"
)

// TestEvals runs every case against a real model: make eval. It is skipped
// unless BBBUDGET_EVAL=1. The model is the one the settings choose
// (BBBUDGET_SETTINGS or the default settings file), else Claude with
// ANTHROPIC_API_KEY. BBBUDGET_EVAL_RUNS repeats each case (default 1).
func TestEvals(t *testing.T) {
	if os.Getenv(enableEnv) != "1" {
		t.Skip("set BBBUDGET_EVAL=1 (make eval) to run the assistant against a real model")
	}
	chosen := loadModelSettings(t)
	runs := runsPerCase()
	var report strings.Builder
	passedRuns, totalRuns := 0, 0
	for _, c := range loadCases(t) {
		outcome := runCase(t, c, chosen, runs)
		report.WriteString(outcome.describe(c.Name, runs))
		passedRuns += outcome.passed
		totalRuns += runs
		if outcome.passed < runs {
			t.Errorf("%s passed %d of %d runs", c.Name, outcome.passed, runs)
		}
	}
	provider := newProvider(t, chosen)
	fmt.Fprintf(&report, "\n%s %s: %d/%d passed (%.0f%%)\n",
		provider.Name(), provider.Model(), passedRuns, totalRuns,
		100*float64(passedRuns)/float64(totalRuns))
	t.Log("\n" + report.String())
}

func loadModelSettings(t *testing.T) settings.Settings {
	t.Helper()
	chosen, err := settings.Load(settings.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if chosen.AssistantProvider == settings.AssistantOff {
		chosen.AssistantProvider = settings.AssistantAnthropic
	}
	return chosen
}

func runsPerCase() int {
	runs, err := strconv.Atoi(os.Getenv(runsEnv))
	if err != nil || runs < 1 {
		return 1
	}
	return runs
}

func newProvider(t *testing.T, chosen settings.Settings) assistant.Provider {
	t.Helper()
	provider, err := assistant.NewProvider(chosen)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

// caseOutcome is how many runs of a case passed, and why the others did not.
type caseOutcome struct {
	passed  int
	reasons []string
}

func runCase(t *testing.T, c Case, chosen settings.Settings, runs int) caseOutcome {
	var outcome caseOutcome
	for range runs {
		result := Run(context.Background(), c, newProvider(t, chosen), t.TempDir())
		if result.Passed() {
			outcome.passed++
			continue
		}
		if result.Err != nil {
			outcome.reasons = append(outcome.reasons, result.Err.Error())
		}
		outcome.reasons = append(outcome.reasons, result.Failures...)
		outcome.reasons = append(outcome.reasons, fmt.Sprintf("tools used: %v", result.Tools))
	}
	return outcome
}

func (o caseOutcome) describe(name string, runs int) string {
	var text strings.Builder
	fmt.Fprintf(&text, "%-28s %d/%d\n", name, o.passed, runs)
	for _, reason := range o.reasons {
		fmt.Fprintf(&text, "    %s\n", reason)
	}
	return text.String()
}
