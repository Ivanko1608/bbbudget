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

const casesDir = "../../evals"

func loadCases(t *testing.T) []Case {
	t.Helper()
	cs, err := Load(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) < 5 {
		t.Fatalf("only %d cases", len(cs))
	}
	return cs
}

// Every case's reference solution passes its own checks, through the real
// loop and tools. Runs in make check: no model, no cost.
func TestReferenceSolutionsPass(t *testing.T) {
	for _, c := range loadCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			if c.Reference == nil {
				t.Fatal("every case needs a reference solution")
			}
			r := Run(context.Background(), c, NewReference(c), t.TempDir())
			if !r.Passed() {
				t.Fatalf("reference fails: %v %v (tools %v, reply %q)", r.Err, r.Failures, r.Tools, r.Reply)
			}
		})
	}
}

type idle struct{}

func (idle) Name() string                                          { return "idle" }
func (idle) Model() string                                         { return "idle" }
func (idle) Start(string, []assistant.Tool) assistant.Conversation { return idle{} }
func (idle) Send(context.Context, string, []assistant.ToolResult) (assistant.Turn, error) {
	return assistant.Turn{Text: "Sure!"}, nil
}

// The checks bite: an assistant that only says "Sure!" fails every case
// that asks for something to happen.
func TestChecksCatchADoNothingAssistant(t *testing.T) {
	failed := 0
	for _, c := range loadCases(t) {
		if r := Run(context.Background(), c, idle{}, t.TempDir()); !r.Passed() {
			failed++
		}
	}
	if failed < 5 {
		t.Fatalf("a do-nothing assistant failed only %d cases", failed)
	}
}

func TestDigitsAndScripts(t *testing.T) {
	for _, s := range []string{"5,100", "5 100", "5 100", "5100"} {
		if digitsOnly(s) != "5100" {
			t.Errorf("digitsOnly(%q) = %q", s, digitsOnly(s))
		}
	}
	if !inScript("В октябре около 317 €.", "cyrillic") || inScript("In October about 317.", "cyrillic") {
		t.Error("inScript")
	}
}

// TestEvals runs every case against a real model: make eval. It is skipped
// unless BBBUDGET_EVAL=1. The model is the one the settings choose
// (BBBUDGET_SETTINGS or the default settings file), else Claude with
// ANTHROPIC_API_KEY. BBBUDGET_EVAL_RUNS repeats each case (default 1).
func TestEvals(t *testing.T) {
	if os.Getenv("BBBUDGET_EVAL") != "1" {
		t.Skip("set BBBUDGET_EVAL=1 (make eval) to run the assistant against a real model")
	}
	st, err := settings.Load(settings.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.AssistantProvider == "" || st.AssistantProvider == settings.AssistantOff {
		st.AssistantProvider = settings.AssistantAnthropic
	}
	runs := 1
	if n, err := strconv.Atoi(os.Getenv("BBBUDGET_EVAL_RUNS")); err == nil && n > 0 {
		runs = n
	}
	var report strings.Builder
	passed, total := 0, 0
	for _, c := range loadCases(t) {
		ok := 0
		var why []string
		for i := 0; i < runs; i++ {
			p, err := assistant.FromSettings(st) // a fresh provider per run
			if err != nil {
				t.Fatal(err)
			}
			r := Run(context.Background(), c, p, t.TempDir())
			total++
			if r.Passed() {
				ok++
				passed++
				continue
			}
			if r.Err != nil {
				why = append(why, r.Err.Error())
			}
			why = append(why, r.Failures...)
			why = append(why, fmt.Sprintf("tools used: %v", r.Tools))
		}
		fmt.Fprintf(&report, "%-28s %d/%d\n", c.Name, ok, runs)
		for _, w := range why {
			fmt.Fprintf(&report, "    %s\n", w)
		}
		if ok < runs {
			t.Errorf("%s passed %d of %d runs", c.Name, ok, runs)
		}
	}
	p, _ := assistant.FromSettings(st)
	fmt.Fprintf(&report, "\n%s %s: %d/%d passed (%.0f%%)\n", p.Name(), p.Model(), passed, total, 100*float64(passed)/float64(total))
	t.Log("\n" + report.String())
}
