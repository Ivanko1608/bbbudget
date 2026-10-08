// Package httpapi serves the JSON API, the embedded web UI and (optionally)
// the MCP server over streamable HTTP, all from one process.
package httpapi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

const maxBody = 1 << 20

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
	mux.HandleFunc("GET /api/budgets", s.list)
	mux.HandleFunc("POST /api/budgets", s.create)
	mux.HandleFunc("GET /api/budgets/{id}", s.get)
	mux.HandleFunc("PUT /api/budgets/{id}", s.replace)
	mux.HandleFunc("DELETE /api/budgets/{id}", s.delete)
	mux.HandleFunc("POST /api/budgets/{id}/ops", s.ops)
	mux.HandleFunc("POST /api/budgets/{id}/what-if", s.whatIf)
	mux.HandleFunc("GET /api/budgets/{id}/compare", s.compare)
	mux.HandleFunc("POST /api/budgets/{id}/scenarios", s.saveScenario)
	mux.HandleFunc("GET /api/budgets/{id}/scenarios/{sid}/report", s.scenarioReport)
	mux.HandleFunc("POST /api/budgets/{id}/scenarios/{sid}/adopt", s.adoptScenario)
	mux.HandleFunc("DELETE /api/budgets/{id}/scenarios/{sid}", s.deleteScenario)
	if opts.MCP != nil {
		mux.Handle("/mcp", opts.MCP)
	}
	if opts.Static != nil {
		// "/" without a method: a "GET /" pattern would conflict with "/mcp".
		files := http.FileServerFS(opts.Static)
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeErr(w, http.StatusMethodNotAllowed, "method not allowed", nil)
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
			writeErr(w, http.StatusForbidden, "host not allowed", nil)
			return
		}
		if r.URL.Path != "/mcp" && r.Method != http.MethodGet && r.Method != http.MethodHead {
			if ct := r.Header.Get("Content-Type"); r.Method != http.MethodDelete && !strings.HasPrefix(ct, "application/json") {
				writeErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json", nil)
				return
			}
			if r.Method == http.MethodDelete && r.Header.Get("X-Requested-With") == "" {
				writeErr(w, http.StatusForbidden, "DELETE requires an X-Requested-With header", nil)
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string, issues []budget.Issue) {
	writeJSON(w, status, errBody{msg, issues})
}

// fail maps domain errors to HTTP statuses. Anything unrecognised is a 500
// and is logged, with a generic message to the client.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var (
		ve *budget.ValidationError
		ce *store.ConflictError
		oe *budget.OpError
		nm *budget.NoMatchError
		ae *money.ArithmeticError
		me *http.MaxBytesError
		se *json.SyntaxError
		te *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &oe): // checked first: a bad target inside an op is the caller's mistake
		writeErr(w, http.StatusBadRequest, err.Error(), nil)
	case errors.As(err, &ve):
		writeErr(w, http.StatusUnprocessableEntity, err.Error(), ve.Issues)
	case errors.As(err, &ce):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "revision": ce.Actual})
	case errors.Is(err, store.ErrNotFound), errors.As(err, &nm):
		writeErr(w, http.StatusNotFound, err.Error(), nil)
	case errors.Is(err, store.ErrExists):
		writeErr(w, http.StatusConflict, err.Error(), nil)
	case errors.As(err, &me):
		writeErr(w, http.StatusRequestEntityTooLarge, "request body too large", nil)
	case errors.Is(err, store.ErrBadID), errors.As(err, &ae), errors.As(err, &se), errors.As(err, &te), errors.Is(err, errBadRequest):
		writeErr(w, http.StatusBadRequest, err.Error(), nil)
	default:
		s.opts.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error", nil)
	}
}

var errBadRequest = errors.New("bad request")

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

func (s *server) meta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"currencies": money.Codes(), "templates": s.app.Templates.List(), "op_kinds": budget.OpKinds,
		"strategies": []string{budget.StrategyAvalanche, budget.StrategySnowball, budget.StrategyListed},
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
	writeJSON(w, http.StatusOK, l)
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
		v, err = s.app.Import(r.Context(), in.ID, *in.Budget)
	} else {
		v, err = s.app.Create(r.Context(), in.ID, in.Name, in.Template)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) get(w http.ResponseWriter, r *http.Request) {
	v, err := s.app.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
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
		writeErr(w, http.StatusBadRequest, "revision is required (the revision you loaded)", nil)
		return
	}
	v, err := s.app.Replace(r.Context(), r.PathValue("id"), *in.Revision, in.Budget)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) delete(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	v, err := s.app.Apply(r.Context(), r.PathValue("id"), rev, in.Ops)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) whatIf(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ops []budget.Op `json:"ops"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	b, rep, err := s.app.WhatIf(r.Context(), r.PathValue("id"), in.Ops)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"budget": b, "report": rep})
}

func (s *server) compare(w http.ResponseWriter, r *http.Request) {
	out, err := s.app.Compare(r.Context(), r.PathValue("id"), r.URL.Query()["scenario"])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
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
	sc, err := s.app.SaveScenario(r.Context(), r.PathValue("id"), in.Name, in.Description, in.Ops)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

func (s *server) scenarioReport(w http.ResponseWriter, r *http.Request) {
	rep, err := s.app.ScenarioReport(r.Context(), r.PathValue("id"), r.PathValue("sid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *server) adoptScenario(w http.ResponseWriter, r *http.Request) {
	v, err := s.app.AdoptScenario(r.Context(), r.PathValue("id"), r.PathValue("sid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) deleteScenario(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteScenario(r.Context(), r.PathValue("id"), r.PathValue("sid")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
