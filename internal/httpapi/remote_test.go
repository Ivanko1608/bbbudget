package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"bbbudget/internal/app"
	"bbbudget/internal/devices"
	"bbbudget/internal/store"
)

type remoteServer struct {
	*client
	devices *devices.Registry
}

func setupRemote(t *testing.T) remoteServer {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(st, nil)
	a.Rates = offlineRates()
	a.SettingsPath = filepath.Join(t.TempDir(), "settings.json")
	registry := devices.Open(filepath.Join(t.TempDir(), "devices.json"))
	static := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>ui</title>")}}
	mcpStub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	opts := Options{Static: static, MCP: mcpStub, Devices: registry}
	return remoteServer{&client{t, New(a, opts)}, registry}
}

// pairDevice pairs a device over HTTP, as a phone would, and returns its token.
func (s remoteServer) pairDevice(name string) string {
	s.t.Helper()
	p, err := s.devices.NewPairing()
	if err != nil {
		s.t.Fatal(err)
	}
	body := `{"code":"` + p.Code + `","name":"` + name + `"}`
	w := s.req("POST", "/api/pair", body, nil)
	if w.Code != http.StatusCreated {
		s.t.Fatalf("pair: status %d: %s", w.Code, w.Body)
	}
	var res pairResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		s.t.Fatal(err)
	}
	return res.Token
}

func withToken(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func (s remoteServer) status(method, path, body string, mut func(*http.Request)) int {
	s.t.Helper()
	return s.req(method, path, body, mut).Code
}

func TestRemoteAPINeedsADeviceToken(t *testing.T) {
	cases := []struct{ method, path, body string }{
		{"GET", "/api/budgets", ""},
		{"GET", "/api/meta", ""},
		{"POST", "/api/budgets", `{"id":"home"}`},
		{"GET", "/api/devices", ""},
		{"POST", "/mcp", `{}`},
	}
	s := setupRemote(t)
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			w := s.req(c.method, c.path, c.body, nil)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("status %d, want 401", w.Code)
			}
			if got := w.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want Bearer", got)
			}
		})
	}
}

func TestPairedDeviceCanUseTheAPI(t *testing.T) {
	s := setupRemote(t)
	token := s.pairDevice("Pixel")

	createBody := `{"id":"home","template":"example"}`
	created := s.status("POST", "/api/budgets", createBody, withToken(token))
	listed := s.status("GET", "/api/budgets", "", withToken(token))
	mcp := s.status("POST", "/mcp", `{}`, withToken(token))

	if created != http.StatusCreated {
		t.Errorf("create budget: %d, want 201", created)
	}
	if listed != http.StatusOK {
		t.Errorf("list budgets: %d, want 200", listed)
	}
	if mcp != 299 {
		t.Errorf("mcp: %d, want the MCP handler's 299", mcp)
	}
}

func TestRemoteRejectsUnknownToken(t *testing.T) {
	s := setupRemote(t)
	s.pairDevice("Pixel")

	got := s.status("GET", "/api/budgets", "", withToken("bbd_GUESSED"))

	if got != http.StatusUnauthorized {
		t.Errorf("unknown token: %d, want 401", got)
	}
}

func TestRevokedDeviceIsLockedOut(t *testing.T) {
	s := setupRemote(t)
	token := s.pairDevice("lost phone")
	list, err := s.devices.List()
	if err != nil {
		t.Fatal(err)
	}
	revokePath := "/api/devices/" + list[0].ID

	revoked := s.status("DELETE", revokePath, "", withToken(token))
	after := s.status("GET", "/api/budgets", "", withToken(token))

	if revoked != http.StatusNoContent {
		t.Errorf("revoke: %d, want 204", revoked)
	}
	if after != http.StatusUnauthorized {
		t.Errorf("request after revoke: %d, want 401", after)
	}
}

func TestDeviceListHidesTokenHashes(t *testing.T) {
	s := setupRemote(t)
	token := s.pairDevice("Pixel")

	w := s.req("GET", "/api/devices", "", withToken(token))

	var list []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("%v: %s", err, w.Body)
	}
	if len(list) != 1 {
		t.Fatalf("got %d devices, want 1", len(list))
	}
	if _, ok := list[0]["token_hash"]; ok {
		t.Error("device list exposes token_hash")
	}
	if list[0]["name"] != "Pixel" {
		t.Errorf("name = %v, want Pixel", list[0]["name"])
	}
}

func TestPairingErrors(t *testing.T) {
	s := setupRemote(t)
	token := s.pairDevice("Pixel")
	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{
			name:   "wrong code",
			method: "POST",
			path:   "/api/pair",
			body:   `{"code":"AAAAA-AAAAA","name":"x"}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "malformed body",
			method: "POST",
			path:   "/api/pair",
			body:   `{"code":`,
			want:   http.StatusBadRequest,
		},
		{
			name:   "unknown field",
			method: "POST",
			path:   "/api/pair",
			body:   `{"code":"AAAAA-AAAAA","admin":true}`,
			want:   http.StatusBadRequest,
		},
		{
			name:   "oversized body",
			method: "POST",
			path:   "/api/pair",
			body:   `{"code":"AAAAA-AAAAA","name":"` + strings.Repeat("x", 2<<10) + `"}`,
			want:   http.StatusRequestEntityTooLarge,
		},
		{
			name:   "revoke unknown",
			method: "DELETE",
			path:   "/api/devices/nope",
			want:   http.StatusNotFound,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := s.status(c.method, c.path, c.body, withToken(token))

			if got != c.want {
				t.Errorf("status %d, want %d", got, c.want)
			}
		})
	}
}

func TestRemoteServesTheUIWithoutAToken(t *testing.T) {
	s := setupRemote(t)

	got := s.status("GET", "/", "", nil)

	if got != http.StatusOK {
		t.Errorf("index: %d, want 200", got)
	}
}

func TestRemoteAcceptsAnyHost(t *testing.T) {
	s := setupRemote(t)
	token := s.pairDevice("Pixel")
	onTailnet := func(r *http.Request) {
		withToken(token)(r)
		r.Host = "budget.example.ts.net"
	}

	got := s.status("GET", "/api/budgets", "", onTailnet)

	if got != http.StatusOK {
		t.Errorf("proxied host: %d, want 200", got)
	}
}

func TestDeviceErrorsDoNotLeakTheWrappedChain(t *testing.T) {
	s := &server{opts: Options{Log: slog.New(slog.DiscardHandler)}}
	cases := []struct {
		name string
		err  error
	}{
		{"bad code", devices.ErrBadCode},
		{"device not found", devices.ErrDeviceNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wrapped := fmt.Errorf("reading /secret/devices.json: %w", c.err)
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/", nil)

			s.fail(w, r, wrapped)

			if strings.Contains(w.Body.String(), "/secret") {
				t.Errorf("body leaks the path: %s", w.Body)
			}
			if !strings.Contains(w.Body.String(), c.err.Error()) {
				t.Errorf("body lacks the sentinel text: %s", w.Body)
			}
		})
	}
}

func TestLocalServerHasNoDeviceRoutes(t *testing.T) {
	c := setup(t)

	w := c.req("POST", "/api/pair", `{"code":"AAAAA-AAAAA"}`, nil)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("pair on a local server: %d, want 405", w.Code)
	}
}
