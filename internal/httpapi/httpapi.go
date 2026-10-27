// Package httpapi serves the JSON API, the embedded web UI and (optionally)
// the MCP server over streamable HTTP, all from one process.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"bbbudget/internal/app"
	"bbbudget/internal/assistant"
	"bbbudget/internal/budget"
	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
	"bbbudget/internal/settings"
	"bbbudget/internal/store"
)

const maxBody = 1 << 20

const errInternal = "internal error"

var internalErrorBody = []byte(`{"error":"` + errInternal + `"}` + "\n")

// The codes go on to the outside rate providers, so the cap bounds what one
// request can ask of them. The UI asks for one code at a time.
const maxCurrenciesPerRatesRequest = 50

// uiCSP keeps the UI to its own origin: no third-party scripts, fonts, images
// or requests. Inline styles stay allowed because the UI renders them.
const uiCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Options configures the handler.
type Options struct {
	// Static is the web UI file system (served at /). Nil disables the UI.
	Static fs.FS
	// MCP, if set, is mounted at /mcp.
	MCP http.Handler
	// AllowedHosts are accepted Host header names besides localhost/127.0.0.1/::1.
	AllowedHosts []string
	Log          *slog.Logger
	// Paths are shown on the settings page (e.g. "data", "templates", "settings").
	Paths map[string]string
	// Assistant, if set, answers chat messages.
	Assistant *assistant.Assistant
}

type server struct {
	app  *app.App
	opts Options
}

// New returns the root handler.
func New(a *app.App, opts Options) http.Handler {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	s := &server{a, opts}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/meta", s.meta)
	mux.HandleFunc("GET /api/rates", s.quote)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("GET /api/budgets", s.list)
	mux.HandleFunc("POST /api/budgets", s.create)
	mux.HandleFunc("GET /api/budgets/{id}", s.get)
	mux.HandleFunc("PUT /api/budgets/{id}", s.replace)
	mux.HandleFunc("DELETE /api/budgets/{id}", s.delete)
	mux.HandleFunc("POST /api/budgets/{id}/ops", s.ops)
	mux.HandleFunc("GET /api/budgets/{id}/history", s.history)
	mux.HandleFunc("GET /api/budgets/{id}/transactions", s.listTransactions)
	mux.HandleFunc("POST /api/budgets/{id}/transactions", s.logTransactions)
	mux.HandleFunc("PUT /api/budgets/{id}/transactions/{tid}", s.updateTransaction)
	mux.HandleFunc("DELETE /api/budgets/{id}/transactions/{tid}", s.deleteTransactions)
	mux.HandleFunc("DELETE /api/budgets/{id}/transactions", s.deleteBatch)
	mux.HandleFunc("GET /api/budgets/{id}/status", s.status)
	mux.HandleFunc("POST /api/budgets/{id}/undo", s.undo)
	mux.HandleFunc("POST /api/budgets/{id}/what-if", s.whatIf)
	mux.HandleFunc("POST /api/budgets/{id}/rates/refresh", s.refreshRates)
	mux.HandleFunc("GET /api/budgets/{id}/compare", s.compare)
	mux.HandleFunc("POST /api/budgets/{id}/scenarios", s.saveScenario)
	mux.HandleFunc("GET /api/budgets/{id}/scenarios/{sid}/report", s.scenarioReport)
	mux.HandleFunc("POST /api/budgets/{id}/scenarios/{sid}/adopt", s.adoptScenario)
	mux.HandleFunc("DELETE /api/budgets/{id}/scenarios/{sid}", s.deleteScenario)
	mux.HandleFunc("POST /api/budgets/{id}/chat", s.chat)
	mux.HandleFunc("GET /api/notes", s.listNotes)
	mux.HandleFunc("DELETE /api/notes/{id}", s.deleteNote)
	if opts.MCP != nil {
		mux.Handle("/mcp", opts.MCP)
	}
	if opts.Static != nil {
		// "/" without a method: a "GET /" pattern would conflict with "/mcp".
		files := http.FileServerFS(opts.Static)
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				s.writeErr(w, http.StatusMethodNotAllowed, "method not allowed", nil)
				return
			}
			w.Header().Set("Content-Security-Policy", uiCSP)
			w.Header().Set("Referrer-Policy", "no-referrer")
			files.ServeHTTP(w, r)
		})
	}
	return s.guard(mux)
}

// guard blocks DNS-rebinding (unknown Host) and cross-site form posts
// (mutations must be JSON, which browsers cannot send cross-origin without a
// CORS preflight this server never approves).
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			s.writeErr(w, http.StatusForbidden, "host not allowed", nil)
			return
		}
		if r.URL.Path != "/mcp" && r.Method != http.MethodGet && r.Method != http.MethodHead {
			if ct := r.Header.Get("Content-Type"); r.Method != http.MethodDelete && !strings.HasPrefix(ct, "application/json") {
				s.writeErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json", nil)
				return
			}
			if r.Method == http.MethodDelete && r.Header.Get("X-Requested-With") == "" {
				s.writeErr(w, http.StatusForbidden, "DELETE requires an X-Requested-With header", nil)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *server) hostAllowed(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	for _, h := range s.opts.AllowedHosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// --- helpers ---

type errBody struct {
	Error  string         `json:"error"`
	Issues []budget.Issue `json:"issues,omitempty"`
}

// writeJSON sends v with status. The body is encoded before anything is
// written, so an encoding failure becomes a logged 500 instead of a success
// status with a broken body.
func (s *server) writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := encodeJSON(v)
	if err != nil {
		s.opts.Log.Error("encoding response failed", "status", status, "err", err)
		status = http.StatusInternalServerError
		body = internalErrorBody
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if _, err := w.Write(body); err != nil {
		s.opts.Log.Error("writing response failed", "status", status, "err", err)
	}
}

// encodeJSON encodes v the way json.Encoder does, including the trailing newline.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// reader renders text in the language r asks for.
func (s *server) reader(r *http.Request) *i18n.Localizer {
	return s.app.Localizer(r.Header.Get("Accept-Language"))
}

// writeView writes v with its text in the language r asks for.
func (s *server) writeView(w http.ResponseWriter, r *http.Request, status int, v *app.View) {
	v.Localize(s.reader(r))
	s.writeJSON(w, status, v)
}

func (s *server) writeErr(w http.ResponseWriter, status int, msg string, issues []budget.Issue) {
	s.writeJSON(w, status, errBody{Error: msg, Issues: issues})
}

// fail maps domain errors to HTTP statuses. Anything unrecognised is a 500
// and is logged, with a generic message to the client.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var (
		invalid    *budget.ValidationError
		conflict   *store.ConflictError
		badOp      *budget.OpError
		noMatch    *budget.NoMatchError
		tooLarge   *http.MaxBytesError
		rateFailed *app.RateError
		undoLater  *app.UndoConflictError
	)
	switch {
	case errors.As(err, &badOp): // checked first: a bad target inside an op is the caller's mistake
		s.writeErr(w, http.StatusBadRequest, err.Error(), nil)
	case errors.As(err, &invalid):
		reader := s.reader(r)
		issues := make([]budget.Issue, len(invalid.Issues))
		for i, is := range invalid.Issues {
			issues[i] = is.Localized(reader)
		}
		s.writeErr(w, http.StatusUnprocessableEntity, err.Error(), issues)
	case errors.As(err, &undoLater):
		app.LocalizeHistory(undoLater.Later, s.reader(r))
		body := map[string]any{"error": err.Error(), "later": undoLater.Later}
		s.writeJSON(w, http.StatusConflict, body)
	case errors.Is(err, app.ErrNothingToUndo), errors.Is(err, app.ErrTooOld):
		s.writeErr(w, http.StatusConflict, err.Error(), nil)
	case errors.As(err, &conflict):
		body := map[string]any{"error": err.Error(), "revision": conflict.Actual}
		s.writeJSON(w, http.StatusConflict, body)
	case errors.Is(err, store.ErrNotFound), errors.As(err, &noMatch):
		s.writeErr(w, http.StatusNotFound, err.Error(), nil)
	case errors.Is(err, store.ErrExists):
		s.writeErr(w, http.StatusConflict, err.Error(), nil)
	case errors.Is(err, app.ErrRatesOff):
		s.writeErr(w, http.StatusConflict, app.ErrRatesOff.Error(), nil)
	case errors.As(err, &rateFailed): // the rate provider failed, not us
		s.writeErr(w, http.StatusBadGateway, err.Error(), nil)
	case errors.As(err, &tooLarge):
		s.writeErr(w, http.StatusRequestEntityTooLarge, "request body too large", nil)
	case isBadRequest(err):
		s.writeErr(w, http.StatusBadRequest, err.Error(), nil)
	default:
		s.opts.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		s.writeErr(w, http.StatusInternalServerError, errInternal, nil)
	}
}

var errBadRequest = errors.New("bad request")

func isBadRequest(err error) bool {
	var (
		overflow  *money.ArithmeticError
		badJSON   *json.SyntaxError
		wrongType *json.UnmarshalTypeError
	)
	return errors.Is(err, store.ErrBadID) ||
		errors.Is(err, errBadRequest) ||
		errors.Is(err, app.ErrBadInput) ||
		errors.As(err, &overflow) ||
		errors.As(err, &badJSON) ||
		errors.As(err, &wrongType)
}

func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		var me *http.MaxBytesError
		if errors.As(err, &me) {
			return err
		}
		return errors.Join(errBadRequest, err)
	}
	return nil
}

// --- handlers ---

// Writes through the API are made in the browser. The editor saves as you
// type, and those saves merge into one history entry.
var (
	fromBrowser = store.Origin{Source: store.SourceWeb}
	editorSave  = store.Origin{Source: store.SourceWeb, Autosave: true}
)

type localeInfo struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func locales() []localeInfo {
	var out []localeInfo
	for _, code := range i18n.Locales() {
		out = append(out, localeInfo{Code: code, Name: i18n.Catalogs()[code].Meta.Name})
	}
	return out
}

func (s *server) meta(w http.ResponseWriter, r *http.Request) {
	digits := map[string]int{}
	for _, c := range money.Codes() {
		digits[c] = money.MustLookup(c).Digits
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"currencies": money.Codes(), "crypto": money.CryptoCodes(), "rate_sources": budget.RateSources, "digits": digits,
		"templates": s.app.Templates.List(), "op_kinds": budget.OpKinds,
		"strategies": budget.Strategies,
		"locale":     s.reader(r).Locale(),
		"locales":    locales(),
	})
}

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	l, err := s.app.List(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if l == nil {
		l = []store.Meta{}
	}
	s.writeJSON(w, http.StatusOK, l)
}

func (s *server) create(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID       string         `json:"id"`
		Name     string         `json:"name"`
		Template string         `json:"template"`
		Budget   *budget.Budget `json:"budget"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	var (
		v   *app.View
		err error
	)
	if in.Budget != nil {
		v, err = s.app.Import(r.Context(), fromBrowser, in.ID, *in.Budget)
	} else {
		v, err = s.app.Create(r.Context(), fromBrowser, s.reader(r), in.ID, in.Name, in.Template)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeView(w, r, http.StatusCreated, v)
}

func (s *server) get(w http.ResponseWriter, r *http.Request) {
	v, err := s.app.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeView(w, r, http.StatusOK, v)
}

func (s *server) replace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision *int64        `json:"revision"`
		Budget   budget.Budget `json:"budget"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Revision == nil {
		s.writeErr(w, http.StatusBadRequest, "revision is required (the revision you loaded)", nil)
		return
	}
	v, err := s.app.Replace(r.Context(), editorSave, r.PathValue("id"), *in.Revision, in.Budget)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeView(w, r, http.StatusOK, v)
}

func (s *server) delete(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) history(w http.ResponseWriter, r *http.Request) {
	limit := store.DefaultHistoryPage
	if q := r.URL.Query().Get("limit"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 0 {
			s.writeErr(w, http.StatusBadRequest, "limit must be a whole number", nil)
			return
		}
		limit = n
	}
	h, err := s.app.History(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if h == nil {
		h = []store.Entry{}
	}
	app.LocalizeHistory(h, s.reader(r))
	s.writeJSON(w, http.StatusOK, h)
}

func (s *server) listTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := app.TransactionFilter{
		From:     q.Get("from"),
		To:       q.Get("to"),
		Category: q.Get("category"),
		Batch:    q.Get("batch"),
		Limit:    100,
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			s.writeErr(w, http.StatusBadRequest, "limit must be a whole number", nil)
			return
		}
		f.Limit = n
	}
	txs, err := s.app.ListTransactions(r.Context(), r.PathValue("id"), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, txs)
}

func (s *server) logTransactions(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Transactions []budget.TransactionInput `json:"transactions"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := s.app.LogTransactions(r.Context(), fromBrowser, r.PathValue("id"), in.Transactions)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, res)
}

func (s *server) updateTransaction(w http.ResponseWriter, r *http.Request) {
	var p app.TransactionPatch
	if err := decode(r, &p); err != nil {
		s.fail(w, r, err)
		return
	}
	t, err := s.app.UpdateTransaction(r.Context(), r.PathValue("id"), r.PathValue("tid"), p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, t)
}

func (s *server) deleteTransactions(w http.ResponseWriter, r *http.Request) {
	ids := []string{r.PathValue("tid")}
	removed, err := s.app.DeleteTransactions(r.Context(), r.PathValue("id"), ids)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, deletedResult{Deleted: removed})
}

// deleteBatch removes everything one log call added (/transactions?batch=b3).
func (s *server) deleteBatch(w http.ResponseWriter, r *http.Request) {
	batch := r.URL.Query().Get("batch")
	removed, err := s.app.DeleteBatch(r.Context(), r.PathValue("id"), batch)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, deletedResult{Deleted: removed})
}

// deletedResult is what deleting transactions answers.
type deletedResult struct {
	Deleted int `json:"deleted"`
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	st, err := s.app.MonthStatus(r.Context(), r.PathValue("id"), r.URL.Query().Get("month"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	st.Localize(s.reader(r))
	s.writeJSON(w, http.StatusOK, st)
}

func (s *server) undo(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision int64 `json:"revision"`
		Force    bool  `json:"force"`
		Seen     int64 `json:"seen"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	req := app.UndoRequest{Revision: in.Revision, Force: in.Force, Seen: in.Seen}
	v, err := s.app.Undo(r.Context(), fromBrowser, r.PathValue("id"), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeView(w, r, http.StatusOK, v)
}

func (s *server) ops(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision *int64      `json:"revision"`
		Ops      []budget.Op `json:"ops"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	rev := store.AnyRevision
	if in.Revision != nil {
		rev = *in.Revision
	}
	v, err := s.app.Apply(r.Context(), fromBrowser, r.PathValue("id"), rev, in.Ops)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeView(w, r, http.StatusOK, v)
}

func (s *server) whatIf(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ops []budget.Op `json:"ops"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	v, err := s.app.WhatIf(r.Context(), r.PathValue("id"), in.Ops)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v.Localize(s.reader(r))
	s.writeJSON(w, http.StatusOK, map[string]any{"budget": v.Budget, "report": v.Report})
}

// settingsView is what the settings page sees. The API key itself never
// leaves the server; only whether one is set.
type settingsView struct {
	FetchRates        bool                  `json:"fetch_rates"`
	DefaultRateSource budget.RateSource     `json:"default_rate_source"`
	AutoRefresh       string                `json:"auto_refresh"`
	CoinGeckoKeySet   bool                  `json:"coingecko_api_key_set"`
	Locale            string                `json:"locale"`
	Status            app.AutoRefreshStatus `json:"auto_refresh_status"`
	Paths             map[string]string     `json:"paths"`

	AssistantProvider settings.AssistantProvider `json:"assistant_provider"`
	AssistantModel    string                     `json:"assistant_model"`
	AssistantURL      string                     `json:"assistant_url"`
	AssistantKeySet   bool                       `json:"assistant_api_key_set"`
	AssistantStatus   assistant.Status           `json:"assistant_status"`
}

func (s *server) settingsView(st settings.Settings) settingsView {
	paths := s.opts.Paths
	if paths == nil {
		paths = map[string]string{}
	}
	locale := st.Locale
	if locale == "" {
		locale = settings.LocaleAuto
	}
	return settingsView{
		FetchRates:        st.FetchRates,
		DefaultRateSource: st.DefaultRateSource,
		AutoRefresh:       st.AutoRefresh,
		CoinGeckoKeySet:   st.CoinGeckoAPIKey != "",
		Locale:            locale,
		Status:            s.app.AutoRefresh(),
		Paths:             paths,
		AssistantProvider: st.AssistantProvider,
		AssistantModel:    st.AssistantModel,
		AssistantURL:      st.AssistantURL,
		AssistantKeySet:   st.AssistantAPIKey != "",
		AssistantStatus:   assistant.StatusFromSettings(st),
	}
}

func (s *server) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.app.Settings()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.settingsView(st))
}

func (s *server) putSettings(w http.ResponseWriter, r *http.Request) {
	var p settings.Patch
	if err := decode(r, &p); err != nil {
		s.fail(w, r, err)
		return
	}
	st, err := s.app.UpdateSettings(p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.settingsView(st))
}

// quote returns current rates without saving: ?base=EUR&codes=USD,BTC[&source=ecb].
func (s *server) quote(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	codes := strings.Split(q.Get("codes"), ",")
	missingInput := q.Get("base") == "" || q.Get("codes") == ""
	if missingInput || len(codes) > maxCurrenciesPerRatesRequest {
		msg := fmt.Sprintf(
			"base and codes (comma-separated, at most %d) are required", maxCurrenciesPerRatesRequest)
		s.writeErr(w, http.StatusBadRequest, msg, nil)
		return
	}
	out, err := s.app.Quote(
		r.Context(),
		strings.ToUpper(q.Get("base")),
		budget.RateSource(q.Get("source")),
		codes,
	)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// refreshRates fetches and saves fresh rates: {codes?: [...]}; omitted means
// every currency whose source isn't manual.
func (s *server) refreshRates(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Codes []string `json:"codes"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	v, updates, err := s.app.RefreshRates(r.Context(), fromBrowser, r.PathValue("id"), in.Codes)
	if err != nil {
		var re *app.RateError
		if errors.As(err, &re) && updates != nil {
			s.writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "rate_updates": updates})
			return
		}
		s.fail(w, r, err)
		return
	}
	v.Localize(s.reader(r))
	s.writeJSON(w, http.StatusOK, struct {
		*app.View
		Updates []app.RateUpdate `json:"rate_updates"`
	}{v, updates})
}

func (s *server) compare(w http.ResponseWriter, r *http.Request) {
	out, err := s.app.Compare(r.Context(), s.reader(r), r.PathValue("id"), r.URL.Query()["scenario"])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *server) saveScenario(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		Ops         []budget.Op `json:"ops"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	sc, err := s.app.SaveScenario(
		r.Context(), fromBrowser, r.PathValue("id"), in.Name, in.Description, in.Ops,
	)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, sc)
}

func (s *server) scenarioReport(w http.ResponseWriter, r *http.Request) {
	rep, err := s.app.ScenarioReport(r.Context(), r.PathValue("id"), r.PathValue("sid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rep.Localize(s.reader(r))
	s.writeJSON(w, http.StatusOK, rep)
}

func (s *server) adoptScenario(w http.ResponseWriter, r *http.Request) {
	v, err := s.app.AdoptScenario(r.Context(), fromBrowser, r.PathValue("id"), r.PathValue("sid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeView(w, r, http.StatusOK, v)
}

func (s *server) deleteScenario(w http.ResponseWriter, r *http.Request) {
	err := s.app.DeleteScenario(r.Context(), fromBrowser, r.PathValue("id"), r.PathValue("sid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
