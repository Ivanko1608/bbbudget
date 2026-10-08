package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"bbbudget/internal/app"
	"bbbudget/internal/store"
)

type client struct {
	t *testing.T
	h http.Handler
}

func setup(t *testing.T) *client {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	static := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>ui</title>")}}
	mcpStub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	c := &client{t, New(app.New(st, nil), Options{Static: static, MCP: mcpStub})}
	c.do("POST", "/api/budgets", `{"id":"home","template":"example"}`, 201)
	return c
}

func (c *client) req(method, path, body string, mut func(*http.Request)) *httptest.ResponseRecorder {
	c.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://localhost:8080"+path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Requested-With", "test")
	if mut != nil {
		mut(req)
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, req)
	return w
}

func (c *client) do(method, path, body string, want int) map[string]any {
	c.t.Helper()
	w := c.req(method, path, body, nil)
	if w.Code != want {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, w.Code, want, w.Body)
	}
	var out map[string]any
	if strings.HasPrefix(strings.TrimSpace(w.Body.String()), "{") {
		json.Unmarshal(w.Body.Bytes(), &out)
	}
	return out
}

func endSavings(v map[string]any) string {
	return v["report"].(map[string]any)["totals"].(map[string]any)["end_savings"].(map[string]any)["amount"].(string)
}

func TestGetAndReplace(t *testing.T) {
	c := setup(t)
	v := c.do("GET", "/api/budgets/home", "", 200)
	if endSavings(v) != "10409.88" {
		t.Fatalf("end savings %s", endSavings(v))
	}
	b, _ := json.Marshal(v["budget"])
	body := strings.Replace(string(b), `"amount":"1150"`, `"amount":"1200"`, 1)

	v = c.do("PUT", "/api/budgets/home", `{"revision":1,"budget":`+body+`}`, 200)
	if v["revision"].(float64) != 2 {
		t.Fatal("revision not bumped")
	}
	// Same stale revision again: conflict, with the current revision in the body.
	v = c.do("PUT", "/api/budgets/home", `{"revision":1,"budget":`+body+`}`, 409)
	if v["revision"].(float64) != 2 {
		t.Fatalf("conflict body: %v", v)
	}
	c.do("PUT", "/api/budgets/home", `{"budget":`+body+`}`, 400) // revision required
}

func TestValidationIssuesAreReturned(t *testing.T) {
	c := setup(t)
	v := c.do("GET", "/api/budgets/home", "", 200)
	b, _ := json.Marshal(v["budget"])
	bad := strings.Replace(string(b), `"amount":"1150"`, `"amount":"1150.001"`, 1)
	out := c.do("PUT", "/api/budgets/home", `{"revision":1,"budget":`+bad+`}`, 422)
	issues := out["issues"].([]any)
	if len(issues) != 1 || issues[0].(map[string]any)["path"] != "lines[1].amount" {
		t.Fatalf("issues: %v", issues)
	}
	// A float-y or malformed amount never reaches the domain.
	c.do("PUT", "/api/budgets/home", `{"revision":1,"budget":`+strings.Replace(string(b), `"amount":"1150"`, `"amount":1.15e3`, 1)+`}`, 400)
	c.do("PUT", "/api/budgets/home", `{"revision":1,"budget":{},"extra":1}`, 400)
}

func TestOpsWhatIfScenarios(t *testing.T) {
	c := setup(t)
	v := c.do("POST", "/api/budgets/home/ops", `{"ops":[{"op":"set_line","target":"tuition","value":{"amount":"450"}}]}`, 200)
	if endSavings(v) != "15509.88" { // +5100.00
		t.Fatalf("ops: %s", endSavings(v))
	}
	c.do("POST", "/api/budgets/home/ops", `{"ops":[{"op":"set_line","target":"school","value":{"amount":"1"}}]}`, 400) // ambiguous
	c.do("POST", "/api/budgets/home/ops", `{"ops":[{"op":"set_line","target":"nope","value":{"amount":"1"}}]}`, 400)   // no match inside an op
	c.do("POST", "/api/budgets/home/ops", `{"revision":1,"ops":[{"op":"remove_line","target":"rent"}]}`, 409)

	wi := c.do("POST", "/api/budgets/home/what-if", `{"ops":[{"op":"set_currency","target":"USD","value":{"per_base":"1"}}]}`, 200)
	if wi["report"] == nil {
		t.Fatal("what-if report missing")
	}
	if c.do("GET", "/api/budgets/home", "", 200)["revision"].(float64) != 2 {
		t.Fatal("what-if saved something")
	}

	c.do("POST", "/api/budgets/home/scenarios", `{"name":"Dollar parity","ops":[{"op":"set_currency","target":"USD","value":{"per_base":"1"}}]}`, 201)
	c.do("POST", "/api/budgets/home/scenarios", `{"name":"","ops":[]}`, 422)
	w := c.req("GET", "/api/budgets/home/compare", "", nil)
	var rows []map[string]any
	json.Unmarshal(w.Body.Bytes(), &rows)
	if len(rows) != 2 || rows[1]["id"] != "dollar-parity" {
		t.Fatalf("compare: %s", w.Body)
	}
	c.do("GET", "/api/budgets/home/scenarios/dollar-parity/report", "", 200)
	c.do("POST", "/api/budgets/home/scenarios/dollar-parity/adopt", "{}", 200)
	c.do("DELETE", "/api/budgets/home/scenarios/dollar-parity", "", 404)
}

func TestCreateListDelete(t *testing.T) {
	c := setup(t)
	c.do("POST", "/api/budgets", `{"id":"home"}`, 409)
	c.do("POST", "/api/budgets", `{"id":"Bad Id"}`, 400)
	c.do("POST", "/api/budgets", `{"id":"x","template":"nope"}`, 422)
	c.do("DELETE", "/api/budgets/home", "", 204)
	c.do("GET", "/api/budgets/home", "", 404)
	w := c.req("GET", "/api/budgets", "", nil)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty list should be [], got %s", w.Body)
	}
}

func TestGuards(t *testing.T) {
	c := setup(t)
	// DNS rebinding: a foreign Host is refused even for reads.
	if w := c.req("GET", "/api/budgets", "", func(r *http.Request) { r.Host = "evil.example" }); w.Code != 403 {
		t.Errorf("foreign host: %d", w.Code)
	}
	if w := c.req("GET", "/api/budgets", "", func(r *http.Request) { r.Host = "[::1]:8080" }); w.Code != 200 {
		t.Errorf("ipv6 loopback: %d", w.Code)
	}
	// Cross-site form post (text/plain needs no CORS preflight): refused.
	w := c.req("POST", "/api/budgets/home/ops", `{"ops":[]}`, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") })
	if w.Code != 415 {
		t.Errorf("text/plain post: %d", w.Code)
	}
	w = c.req("DELETE", "/api/budgets/home", "", func(r *http.Request) { r.Header.Del("X-Requested-With") })
	if w.Code != 403 {
		t.Errorf("bare DELETE: %d", w.Code)
	}
	big := `{"id":"x","name":"` + strings.Repeat("a", 2<<20) + `"}`
	if w := c.req("POST", "/api/budgets", big, nil); w.Code != 413 {
		t.Errorf("huge body: %d", w.Code)
	}
}

func TestStaticAndMeta(t *testing.T) {
	c := setup(t)
	w := c.req("GET", "/", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>ui</title>") {
		t.Fatalf("index: %d %s", w.Code, w.Body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "connect-src 'self'") {
		t.Fatalf("UI without a same-origin CSP: %q", csp)
	}
	if w := c.req("POST", "/mcp", `{}`, nil); w.Code != 299 {
		t.Fatalf("mcp not mounted alongside the UI: %d", w.Code)
	}
	if w := c.req("POST", "/index.html", `{}`, nil); w.Code != 405 {
		t.Fatalf("POST to a static file: %d", w.Code)
	}
	m := c.do("GET", "/api/meta", "", 200)
	if len(m["currencies"].([]any)) < 10 || m["op_kinds"] == nil {
		t.Fatalf("meta: %v", m)
	}
}
