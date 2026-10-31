package evals

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"bbbudget/internal/app"
	"bbbudget/internal/assistant"
	"bbbudget/internal/budget"
	"bbbudget/internal/rates"
	"bbbudget/internal/store"
)

// Result is how one run of a case went.
type Result struct {
	Case     string
	Failures []string
	// Reply is the assistant's text in answer to the last turn.
	Reply string
	// Tools are the names of the tools it called, in order.
	Tools []string
	// Err is set when the run itself broke, before the checks could run.
	Err error
}

// Passed reports whether the run finished and every check held.
func (r Result) Passed() bool { return r.Err == nil && len(r.Failures) == 0 }

// Run plays a case's turns through the assistant with the provider, on a
// fresh budget in dir, then checks the outcome.
func Run(ctx context.Context, c Case, provider assistant.Provider, dir string) Result {
	result := Result{Case: c.Name}
	budgetApp, err := createApp(ctx, dir, c.Template)
	if err != nil {
		result.Err = err
		return result
	}
	budgetAssistant := newAssistant(budgetApp, provider)
	err = playTurns(ctx, budgetAssistant, c.Turns, &result)
	if err != nil {
		result.Err = err
		return result
	}
	result.Failures = check(ctx, budgetApp, c.Expect, result.Reply)
	return result
}

// createApp opens an app over dir with the budget "home" made from the
// template. Rates are never fetched.
func createApp(ctx context.Context, dir, template string) (*app.App, error) {
	fileStore, err := store.NewFileStore(dir)
	if err != nil {
		return nil, err
	}
	budgetApp := app.New(fileStore, nil)
	budgetApp.Rates = map[budget.RateSource]rates.Provider{}
	budgetApp.Now = func() time.Time { return Today }
	budgetApp.SettingsPath = filepath.Join(dir, "settings.json")
	fromCLI := store.Origin{Source: store.SourceCLI}
	localizer := budgetApp.SettingsLocalizer()
	_, err = budgetApp.Create(ctx, fromCLI, localizer, budgetID, budgetName, template)
	return budgetApp, err
}

func newAssistant(budgetApp *app.App, provider assistant.Provider) *assistant.Assistant {
	budgetAssistant := assistant.New(
		budgetApp,
		func() (assistant.Provider, error) { return provider, nil },
		slog.New(slog.DiscardHandler),
	)
	budgetAssistant.Now = func() time.Time { return Today }
	return budgetAssistant
}

// playTurns sends each turn in the same conversation and records the tools
// called and the reply to the last turn in result.
func playTurns(
	ctx context.Context, budgetAssistant *assistant.Assistant, turns []string, result *Result,
) error {
	conversation := ""
	for _, turn := range turns {
		var texts []string
		record := func(event assistant.Event) {
			switch event.Type {
			case assistant.EventText:
				texts = append(texts, event.Text)
			case assistant.EventTool:
				result.Tools = append(result.Tools, event.Tool)
			}
		}
		request := assistant.Request{Conversation: conversation, Budget: budgetID, Message: turn}
		var err error
		conversation, err = budgetAssistant.Reply(ctx, request, record)
		if err != nil {
			return err
		}
		result.Reply = strings.Join(texts, "\n")
	}
	return nil
}
