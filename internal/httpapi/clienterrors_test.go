package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bbbudget/internal/app"
	"bbbudget/internal/assistant"
	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/rates"
	"bbbudget/internal/settings"
	"bbbudget/internal/store"
)

const wrapperPath = "/secret/path"

type loggedAPI struct {
	*client
	dir  string
	logs *bytes.Buffer
}

func setupLogged(t *testing.T, tune func(*app.App, *Options)) loggedAPI {
	t.Helper()
	dir := t.TempDir()
	st, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(st, nil)
	a.Rates = offlineRates()
	a.SettingsPath = filepath.Join(t.TempDir(), "settings.json")
	logs := &bytes.Buffer{}
	opts := Options{Log: slog.New(slog.NewTextHandler(logs, nil))}
	if tune != nil {
		tune(a, &opts)
	}
	c := &client{t, New(a, opts)}
	c.do("POST", "/api/budgets", `{"id":"home","template":"example"}`, 201)
	return loggedAPI{client: c, dir: dir, logs: logs}
}

func newBareServer() *server {
	return &server{app: app.New(nil, nil), opts: Options{Log: slog.New(slog.DiscardHandler)}}
}

func issues(path, message string) []budget.Issue {
	return []budget.Issue{{Path: path, Message: message}}
}

func TestFailNeverSendsTextWrappedAroundARecognisedError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"op error", &budget.OpError{Index: 1, Op: "set_item", Err: errors.New("x")}, 400},
		{"validation", &budget.ValidationError{Issues: issues("a", "b")}, 422},
		{"undo conflict", &app.UndoConflictError{Revision: 2}, 409},
		{"nothing to undo", app.ErrNothingToUndo, 409},
		{"too old", app.ErrTooOld, 409},
		{"revision conflict", &store.ConflictError{Expected: 1, Actual: 2}, 409},
		{"budget not found", store.ErrNotFound, 404},
		{"no match", &budget.NoMatchError{Kind: "item", Ref: "x"}, 404},
		{"already exists", store.ErrExists, 409},
		{"rates off", app.ErrRatesOff, 409},
		{"rate provider", &app.RateError{Err: errors.New("lookup " + wrapperPath)}, 502},
		{"body too large", &http.MaxBytesError{Limit: 1}, 413},
		{"undecodable body", &badRequestError{cause: errors.New("unexpected end")}, 400},
		{"bad id", store.ErrBadID, 400},
		{"bad input", app.NewClientError(app.ErrBadInput, "unknown source"), 400},
		{"bad input, plain", app.ErrBadInput, 400},
		{"arithmetic", &money.ArithmeticError{Msg: "overflow"}, 400},
		{"note not found", store.ErrNoteNotFound, 404},
		{"too many notes", store.ErrTooManyNotes, 409},
		{"empty note", store.ErrNoteEmpty, 400},
		{"long note", store.ErrNoteTooLong, 400},
		{"bad note source", store.ErrBadNoteSource, 400},
		{"bad note id", store.ErrBadNoteID, 400},
		{"unrecognised", errors.New("open " + wrapperPath), 500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newBareServer()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/", nil)

			s.fail(w, r, fmt.Errorf("at %s: %w", wrapperPath, c.err))

			if w.Code != c.want {
				t.Errorf("status %d, want %d", w.Code, c.want)
			}
			if strings.Contains(w.Body.String(), "/secret") {
				t.Errorf("body leaks the wrapper: %s", w.Body)
			}
		})
	}
}

func TestFailKeepsTheClientWrittenTextOfAnError(t *testing.T) {
	s := newBareServer()
	w := httptest.NewRecorder()
	clientErr := app.NewClientError(store.ErrNotFound, "no transaction %q", "t9")
	err := fmt.Errorf("at %s: %w", wrapperPath, clientErr)

	s.fail(w, httptest.NewRequest("GET", "/", nil), err)

	want := `no transaction \"t9\"`
	if !strings.Contains(w.Body.String(), want) {
		t.Errorf("body %s lacks %s", w.Body, want)
	}
}

func TestARateProviderFailureSendsAFixedMessageAndLogsTheDetail(t *testing.T) {
	api := setupLogged(t, func(a *app.App, _ *Options) {
		down := rates.Fixed{ID: budget.RateSourceAuto, Err: errors.New("lookup " + wrapperPath)}
		a.Rates = map[budget.RateSource]rates.Provider{budget.RateSourceAuto: down}
	})
	api.do("PUT", "/api/settings", `{"fetch_rates":true}`, 200)

	response := api.req("POST", "/api/budgets/home/rates/refresh", `{"codes":["USD"]}`, nil)

	eq(t, "status", response.Code, http.StatusBadGateway)
	if strings.Contains(response.Body.String(), "/secret") {
		t.Errorf("body leaks the provider error: %s", response.Body)
	}
	if !strings.Contains(response.Body.String(), app.RateFetchFailedMessage) {
		t.Errorf("body lacks the fixed message: %s", response.Body)
	}
	if !strings.Contains(api.logs.String(), wrapperPath) {
		t.Errorf("the full error was not logged: %s", api.logs)
	}
}

func TestAQuoteFailureSendsAFixedMessage(t *testing.T) {
	api := setupLogged(t, func(a *app.App, _ *Options) {
		down := rates.Fixed{ID: budget.RateSourceAuto, Err: errors.New("lookup " + wrapperPath)}
		a.Rates = map[budget.RateSource]rates.Provider{budget.RateSourceAuto: down}
	})
	api.do("PUT", "/api/settings", `{"fetch_rates":true}`, 200)

	response := api.req("GET", "/api/rates?base=EUR&codes=USD", "", nil)

	eq(t, "status", response.Code, http.StatusBadGateway)
	if strings.Contains(response.Body.String(), "/secret") {
		t.Errorf("body leaks the provider error: %s", response.Body)
	}
}

type failingModel struct{}

func (failingModel) Name() settings.AssistantProvider { return "failing" }
func (failingModel) Model() string                    { return "test" }

func (failingModel) Start(string, []assistant.Tool) assistant.Conversation {
	return failingConversation{}
}

type failingConversation struct{}

func (failingConversation) SendText(context.Context, string) (assistant.Turn, error) {
	return assistant.Turn{}, fmt.Errorf("open %s: permission denied", wrapperPath)
}

func (failingConversation) SendResults(
	context.Context, []assistant.ToolResult,
) (assistant.Turn, error) {
	return assistant.Turn{}, nil
}

func TestAChatFailureSendsAFixedMessageAndLogsTheDetail(t *testing.T) {
	api := setupLogged(t, func(a *app.App, opts *Options) {
		opts.Assistant = assistant.New(
			a,
			func() (assistant.Provider, error) { return failingModel{}, nil },
			opts.Log,
		)
	})

	response := api.req("POST", "/api/budgets/home/chat", `{"message":"hi"}`, nil)

	events := eventsOf(t, response.Body.String())
	eq(t, "event type", events[0].Type, assistant.EventError)
	eq(t, "message", events[0].Error, assistantFailedMessage)
	if strings.Contains(response.Body.String(), "/secret") {
		t.Errorf("stream leaks the assistant error: %s", response.Body)
	}
	if !strings.Contains(api.logs.String(), wrapperPath) {
		t.Errorf("the full error was not logged: %s", api.logs)
	}
}

func TestACorruptStoredBudgetIsAnInternalError(t *testing.T) {
	api := setupLogged(t, nil)
	corrupt := filepath.Join(api.dir, "home.json")
	if err := os.WriteFile(corrupt, []byte(`{"id": nope`), 0o600); err != nil {
		t.Fatal(err)
	}

	response := api.req("GET", "/api/budgets/home", "", nil)

	eq(t, "status", response.Code, http.StatusInternalServerError)
	eq(t, "body", strings.TrimSpace(response.Body.String()), `{"error":"internal error"}`)
	if !strings.Contains(api.logs.String(), "is corrupt") {
		t.Errorf("the corruption was not logged: %s", api.logs)
	}
}
