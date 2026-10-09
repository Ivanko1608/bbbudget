package httpapi

import (
	"bbbudget/internal/assistant"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"bbbudget/internal/app"
	"bbbudget/internal/rates"
	"bbbudget/internal/store"
	"bbbudget/web"
)

func offlineRates() map[string]rates.Provider {
	f := rates.Fixed{ID: rates.CurrencyAPI, Base: "EUR", Date: "2026-10-07", Rates: map[string]string{"USD": "1.1186", "BTC": "0.00001352"}}
	down := rates.Fixed{ID: rates.ECB, Err: errors.New("HTTP 503")}
	return map[string]rates.Provider{rates.Auto: f, rates.CurrencyAPI: f, rates.ECB: down, rates.CoinGecko: down}
}

func TestRates(t *testing.T) {
	c := setup(t)
	var q []map[string]any
	w := c.req("GET", "/api/rates?base=EUR&codes=usd,BTC", "", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &q) != nil || len(q) != 2 || q[1]["new"] != "0.00001352" {
		t.Fatalf("quote: %d %s", w.Code, w.Body)
	}
	c.do("GET", "/api/rates?base=EUR&codes=USD&source=google", "", 400)
	c.do("GET", "/api/rates?base=EUR&codes=USD&source=ecb", "", 502)

	// Refreshing nothing (all manual) succeeds; naming USD fetches it.
	if v := c.do("POST", "/api/budgets/home/rates/refresh", `{}`, 200); len(v["rate_updates"].([]any)) != 0 {
		t.Fatal(v["rate_updates"])
	}
	v := c.do("POST", "/api/budgets/home/rates/refresh", `{"codes":["USD"]}`, 200)
	usd := v["budget"].(map[string]any)["currencies"].([]any)[1].(map[string]any)
	if usd["per_base"] != "1.1186" || usd["source"] != "auto" || usd["rate_date"] != "2026-10-07" || v["report"] == nil {
		t.Fatalf("refresh: %v", usd)
	}
	c.do("POST", "/api/budgets/home/rates/refresh", `{"codes":["JPY"]}`, 404)
	c.do("POST", "/api/budgets/home/ops", `{"ops":[{"op":"set_currency","target":"USD","value":{"source":"ecb"}}]}`, 200)
	if v := c.do("POST", "/api/budgets/home/rates/refresh", `{}`, 502); v["rate_updates"] == nil {
		t.Fatal("a failed refresh should still say what failed")
	}
}

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
	a := app.New(st, nil)
	a.Rates = offlineRates() // tests never touch the network
	a.SettingsPath = filepath.Join(t.TempDir(), "settings.json")
	c := &client{t, New(a, Options{Static: static, MCP: mcpStub, Paths: map[string]string{"data": "/d"}})}
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

func TestSettings(t *testing.T) {
	c := setup(t)
	v := c.do("GET", "/api/settings", "", 200)
	if v["fetch_rates"] != true || v["default_rate_source"] != "auto" || v["auto_refresh"] != "off" || v["coingecko_api_key_set"] != false {
		t.Fatalf("defaults: %v", v)
	}
	if v["paths"].(map[string]any)["data"] != "/d" || v["auto_refresh_status"] == nil {
		t.Fatalf("paths/status: %v", v)
	}

	w := c.req("PUT", "/api/settings", `{"auto_refresh":"daily","coingecko_api_key":"CG-secret"}`, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "CG-secret") {
		t.Fatalf("the key must never be sent back: %d %s", w.Code, w.Body)
	}
	if v := c.do("GET", "/api/settings", "", 200); v["auto_refresh"] != "daily" || v["coingecko_api_key_set"] != true {
		t.Fatalf("after PUT: %v", v)
	}
	c.do("PUT", "/api/settings", `{"auto_refresh":"weekly"}`, 400)
	c.do("PUT", "/api/settings", `{"colour":"blue"}`, 400)
	if w := c.req("PUT", "/api/settings", `{"fetch_rates":false}`, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }); w.Code != 415 {
		t.Fatalf("settings writes need JSON like every mutation: %d", w.Code)
	}

	// Offline: quotes and refreshes are refused with a clear conflict, not a 5xx.
	c.do("PUT", "/api/settings", `{"fetch_rates":false}`, 200)
	if v := c.do("GET", "/api/rates?base=EUR&codes=USD", "", 409); !strings.Contains(v["error"].(string), "turned off") {
		t.Fatal(v)
	}
	c.do("POST", "/api/budgets/home/rates/refresh", `{"codes":["USD"]}`, 409)
}

func TestSettingsPageIsServed(t *testing.T) {
	st, _ := store.NewFileStore(t.TempDir())
	h := New(app.New(st, nil), Options{Static: web.FS()})
	for _, path := range []string{"/settings.html", "/settings.js", "/"} {
		req := httptest.NewRequest("GET", "http://localhost"+path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Errorf("%s: %d", path, w.Code)
		}
		if path == "/" && !strings.Contains(w.Body.String(), `href="settings.html"`) {
			t.Error("the budget page must link to the settings page")
		}
	}
}

func TestMetaListsCrypto(t *testing.T) {
	v := setup(t).do("GET", "/api/meta", "", 200)
	crypto, sources := v["crypto"].([]any), v["rate_sources"].([]any)
	if len(crypto) == 0 || crypto[0] != "ADA" || len(sources) != 5 {
		t.Fatalf("crypto %v, sources %v", crypto, sources)
	}
}

func TestHistoryAndUndo(t *testing.T) {
	c := setup(t)
	c.do("POST", "/api/budgets/home/ops", `{"ops":[{"op":"set_line","target":"rent","value":{"amount":"1200"}}]}`, 200)
	var h []map[string]any
	w := c.req("GET", "/api/budgets/home/history", "", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &h) != nil || len(h) != 2 {
		t.Fatalf("history: %d %s", w.Code, w.Body)
	}
	if h[0]["source"] != "web" || !strings.Contains(h[0]["summary"].(string), "1150 → 1200") || h[0]["budget"] != nil {
		t.Fatalf("latest entry: %v", h[0])
	}
	c.do("GET", "/api/budgets/home/history?limit=x", "", 400)

	// Undoing r2 while r3 exists needs force; the 409 lists what would be lost.
	c.do("POST", "/api/budgets/home/ops", `{"ops":[{"op":"set_line","target":"rent","value":{"amount":"1300"}}]}`, 200)
	if v := c.do("POST", "/api/budgets/home/undo", `{"change":2}`, 409); len(v["later"].([]any)) != 1 {
		t.Fatalf("conflict: %v", v)
	}
	v := c.do("POST", "/api/budgets/home/undo", `{"change":2,"force":true}`, 200)
	if v["revision"].(float64) != 4 {
		t.Fatalf("undo: %v", v["revision"])
	}
	c.do("POST", "/api/budgets/home/undo", `{"change":1}`, 409) // the creation
	c.do("POST", "/api/budgets/home/undo", `{"change":99}`, 400)
}

func TestLocaleNegotiation(t *testing.T) {
	c := setup(t)
	ru := func(r *http.Request) { r.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8") }
	get := func(path string, mut func(*http.Request)) map[string]any {
		t.Helper()
		w := c.req("GET", path, "", mut)
		var out map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
		return out
	}
	if m := get("/api/meta", ru); m["locale"] != "ru" || len(m["locales"].([]any)) < 2 {
		t.Fatalf("meta: %v %v", m["locale"], m["locales"])
	}
	month := func(v map[string]any) string {
		return v["report"].(map[string]any)["months"].([]any)[0].(map[string]any)["label"].(string)
	}
	ruView, enView := get("/api/budgets/home", ru), get("/api/budgets/home", nil)
	if !strings.ContainsAny(month(ruView), "абвгдеёжзийклмнопрстуфхцчшщъыьэюя") || strings.ContainsAny(month(enView), "абвгдеёжзийклмнопрстуфхцчшщъыьэюя") {
		t.Fatalf("month labels: ru %q, en %q", month(ruView), month(enView))
	}

	// Validation issues come back in the reader's language, with their key.
	w := c.req("PUT", "/api/budgets/home", `{"revision":1,"budget":{"name":"","base_currency":"EUR","start_month":"2026-10","months":12}}`, ru)
	var bad struct {
		Issues []struct{ Path, Message, Key string } `json:"issues"`
	}
	if w.Code != 422 || json.Unmarshal(w.Body.Bytes(), &bad) != nil || bad.Issues[0].Message != "обязательное поле" || bad.Issues[0].Key != "issue.required" {
		t.Fatalf("issues: %d %s", w.Code, w.Body)
	}

	// An explicit language setting wins over the browser's.
	c.do("PUT", "/api/settings", `{"locale":"en"}`, 200)
	if m := get("/api/meta", ru); m["locale"] != "en" {
		t.Fatalf("setting should win: %v", m["locale"])
	}
	c.do("PUT", "/api/settings", `{"locale":"not a tag!"}`, 400)
}

func TestTransactionsAPI(t *testing.T) {
	c := setup(t)
	m := time.Now().Format("2006-01") // the example budget starts this month
	v := c.do("POST", "/api/budgets/home/transactions", `{"transactions":[{"date":"`+m+`-02","amount":"45.20","category":"home","external_id":"r1"},{"date":"`+m+`-03","amount":"12","currency":"USD","category":"education"}]}`, 201)
	if v["batch"] != "b1" || len(v["logged"].([]any)) != 2 {
		t.Fatalf("log: %v", v)
	}
	c.do("POST", "/api/budgets/home/transactions", `{"transactions":[{"date":"`+m+`-02","amount":"1","category":"nope"}]}`, 422)
	var list []map[string]any
	w := c.req("GET", "/api/budgets/home/transactions?from="+m+"&to="+m, "", nil)
	if json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 2 || list[0]["date"] != m+"-03" {
		t.Fatalf("list: %s", w.Body)
	}
	if u := c.do("PUT", "/api/budgets/home/transactions/t1", `{"note":"Lidl"}`, 200); u["note"] != "Lidl" || u["base"] != "45.2" {
		t.Fatalf("update: %v", u)
	}
	c.do("PUT", "/api/budgets/home/transactions/t9", `{"note":"x"}`, 404)
	if st := c.do("GET", "/api/budgets/home/status?month="+m, "", 200); st["transactions"].(float64) != 2 {
		t.Fatalf("status: %v", st)
	}
	c.do("GET", "/api/budgets/home/status?month=1999-01", "", 400)
	c.do("DELETE", "/api/budgets/home/transactions/t1", "", 200)
	if d := c.do("DELETE", "/api/budgets/home/transactions?batch=b1", "", 200); d["removed"].(float64) != 1 {
		t.Fatalf("delete batch: %v", d)
	}
}

type fakeModel struct{ turns []assistant.Turn }

func (f *fakeModel) Name() string                                          { return "fake" }
func (f *fakeModel) Model() string                                         { return "fake-1" }
func (f *fakeModel) Start(string, []assistant.Tool) assistant.Conversation { return f }
func (f *fakeModel) Send(context.Context, string, []assistant.ToolResult) (assistant.Turn, error) {
	t := f.turns[0]
	f.turns = f.turns[1:]
	return t, nil
}

func TestAssistantChat(t *testing.T) {
	st, _ := store.NewFileStore(t.TempDir())
	a := app.New(st, nil)
	a.Rates = offlineRates()
	a.SettingsPath = filepath.Join(t.TempDir(), "settings.json")
	model := &fakeModel{turns: []assistant.Turn{
		{Calls: []assistant.ToolCall{{ID: "1", Name: "update_line", Input: json.RawMessage(`{"line":"rent","amount":"1200"}`)}}},
		{Text: "Rent is 1200 now."},
	}}
	as := &assistant.Assistant{App: a, Provider: func() (assistant.Provider, error) { return model, nil }}
	c := &client{t, New(a, Options{Assistant: as, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})}
	c.do("POST", "/api/budgets", `{"id":"home","template":"example"}`, 201)

	// Off by default: the page shows no chat, and chatting is refused.
	if s := c.do("GET", "/api/assistant", "", 200); s["enabled"] != false {
		t.Fatalf("status: %v", s)
	}
	c.do("POST", "/api/budgets/home/chat", `{"message":"hi"}`, 409)

	c.do("PUT", "/api/settings", `{"assistant_provider":"anthropic","assistant_api_key":"sk-secret-123"}`, 200)
	if s := c.do("GET", "/api/assistant", "", 200); s["enabled"] != true || s["model"] != assistant.DefaultAnthropicModel {
		t.Fatalf("status: %v", s)
	}
	// The key is never sent back.
	for _, path := range []string{"/api/settings", "/api/assistant", "/api/meta"} {
		if w := c.req("GET", path, "", nil); strings.Contains(w.Body.String(), "sk-secret") {
			t.Fatalf("%s leaks the key", path)
		}
	}
	if s := c.do("GET", "/api/settings", "", 200)["assistant"].(map[string]any); s["api_key_set"] != true {
		t.Fatalf("settings: %v", s)
	}

	w := c.req("POST", "/api/budgets/home/chat", `{"message":"raise rent to 1200"}`, nil)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("chat: %d %s", w.Code, w.Header())
	}
	var kinds []string
	for _, line := range strings.Split(w.Body.String(), "\n\n") {
		if line = strings.TrimPrefix(strings.TrimSpace(line), "data: "); line == "" {
			continue
		}
		var e assistant.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad event %q: %v", line, err)
		}
		kinds = append(kinds, e.Type)
	}
	if got := strings.Join(kinds, ","); got != "tool,changed,text,session,done" {
		t.Fatalf("events: %s\n%s", got, w.Body)
	}
	var h []map[string]any
	json.Unmarshal(c.req("GET", "/api/budgets/home/history?limit=1", "", nil).Body.Bytes(), &h)
	if h[0]["source"] != "assistant" {
		t.Fatalf("history: %v", h[0])
	}
}
