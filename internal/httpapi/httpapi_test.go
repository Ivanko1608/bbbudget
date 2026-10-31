package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"bbbudget/internal/app"
	"bbbudget/internal/assistant"
	"bbbudget/internal/budget"
	"bbbudget/internal/rates"
	"bbbudget/internal/store"
	"bbbudget/web"
)

const (
	setUSDParity = `{"op":"set_currency","target":"USD","value":{"per_base":"1"}}`
	cyrillic     = "абвгдеёжзийклмнопрстуфхцчшщъыьэюя"
)

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func setItemAmountOps(target, amount string) string {
	return fmt.Sprintf(
		`{"ops":[{"op":"set_item","target":%q,"value":{"amount":%q}}]}`,
		target,
		amount,
	)
}

func sendAsPlainText(r *http.Request) {
	r.Header.Set("Content-Type", "text/plain")
}

func offlineRates() map[budget.RateSource]rates.Provider {
	f := rates.Fixed{
		ID:    budget.RateSourceCurrencyAPI,
		Base:  "EUR",
		Date:  "2026-10-07",
		Rates: map[string]string{"USD": "1.1186", "BTC": "0.00001352"},
	}
	down := rates.Fixed{ID: budget.RateSourceECB, Err: errors.New("HTTP 503")}
	return map[budget.RateSource]rates.Provider{
		budget.RateSourceAuto:        f,
		budget.RateSourceCurrencyAPI: f,
		budget.RateSourceECB:         down,
		budget.RateSourceCoinGecko:   down,
	}
}

func TestRates(t *testing.T) {
	c := setup(t)
	c.do("PUT", "/api/settings", `{"fetch_rates":true}`, 200) // off by default
	var q []map[string]any
	w := c.req("GET", "/api/rates?base=EUR&codes=usd,BTC", "", nil)
	eq(t, "quote status", w.Code, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &q); err != nil {
		t.Fatalf("quote: %v %s", err, w.Body)
	}
	eq(t, "quotes", len(q), 2)
	eq(t, "BTC new rate", q[1]["new"], "0.00001352")
	c.do("GET", "/api/rates?base=EUR&codes=USD&source=google", "", 400)
	c.do("GET", "/api/rates?base=EUR&codes=USD&source=ecb", "", 502)

	// Refreshing nothing (all manual) succeeds; naming USD fetches it.
	nothingToRefresh := c.do("POST", "/api/budgets/home/rates/refresh", `{}`, 200)
	eq(t, "rate updates", len(nothingToRefresh["rate_updates"].([]any)), 0)
	v := c.do("POST", "/api/budgets/home/rates/refresh", `{"codes":["USD"]}`, 200)
	usd := v["budget"].(map[string]any)["currencies"].([]any)[1].(map[string]any)
	eq(t, "USD per_base", usd["per_base"], "1.1186")
	eq(t, "USD source", usd["source"], "auto")
	eq(t, "USD rate_date", usd["rate_date"], "2026-10-07")
	if v["report"] == nil {
		t.Fatal("refresh returned no report")
	}
	c.do("POST", "/api/budgets/home/rates/refresh", `{"codes":["JPY"]}`, 404)
	usdToECB := `{"ops":[{"op":"set_currency","target":"USD","value":{"source":"ecb"}}]}`
	c.do("POST", "/api/budgets/home/ops", usdToECB, 200)
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
	return setupWith(t, nil)
}

// setupWith serves the API with the assistant newAssistant makes over the
// app, or with none when it is nil.
func setupWith(t *testing.T, newAssistant func(*app.App) *assistant.Assistant) *client {
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
	opts := Options{Static: static, MCP: mcpStub, Paths: map[string]string{"data": "/d"}}
	if newAssistant != nil {
		opts.Assistant = newAssistant(a)
	}
	c := &client{t, New(a, opts)}
	c.do("POST", "/api/budgets", `{"id":"home","template":"example"}`, 201)
	return c
}

func (c *client) req(
	method, path, body string,
	mut func(*http.Request),
) *httptest.ResponseRecorder {
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
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			c.t.Fatal(err)
		}
	}
	return out
}

func endSavings(v map[string]any) string {
	totals := v["report"].(map[string]any)["totals"].(map[string]any)
	return totals["end_savings"].(map[string]any)["amount"].(string)
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
	eq(t, "issues", len(issues), 1)
	eq(t, "issue path", issues[0].(map[string]any)["path"], "items[1].amount")
	// A float-y or malformed amount never reaches the domain.
	floaty := strings.Replace(string(b), `"amount":"1150"`, `"amount":1.15e3`, 1)
	c.do("PUT", "/api/budgets/home", `{"revision":1,"budget":`+floaty+`}`, 400)
	c.do("PUT", "/api/budgets/home", `{"revision":1,"budget":{},"extra":1}`, 400)
}

func TestOpsWhatIfScenarios(t *testing.T) {
	c := setup(t)
	v := c.do("POST", "/api/budgets/home/ops", setItemAmountOps("tuition", "450"), 200)
	if endSavings(v) != "15509.88" { // +5100.00
		t.Fatalf("ops: %s", endSavings(v))
	}
	ambiguousTarget := setItemAmountOps("school", "1")
	c.do("POST", "/api/budgets/home/ops", ambiguousTarget, 400)
	unmatchedTarget := setItemAmountOps("nope", "1")
	c.do("POST", "/api/budgets/home/ops", unmatchedTarget, 400)
	staleRemoval := `{"revision":1,"ops":[{"op":"remove_item","target":"rent"}]}`
	c.do("POST", "/api/budgets/home/ops", staleRemoval, 409)

	wi := c.do("POST", "/api/budgets/home/what-if", `{"ops":[`+setUSDParity+`]}`, 200)
	if wi["report"] == nil {
		t.Fatal("what-if report missing")
	}
	if c.do("GET", "/api/budgets/home", "", 200)["revision"].(float64) != 2 {
		t.Fatal("what-if saved something")
	}

	parityScenario := `{"name":"Dollar parity","ops":[` + setUSDParity + `]}`
	c.do("POST", "/api/budgets/home/scenarios", parityScenario, 201)
	c.do("POST", "/api/budgets/home/scenarios", `{"name":"","ops":[]}`, 422)
	w := c.req("GET", "/api/budgets/home/compare", "", nil)
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	eq(t, "compared scenarios", len(rows), 2)
	eq(t, "second scenario id", rows[1]["id"], "dollar-parity")
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
	foreignHost := func(r *http.Request) { r.Host = "evil.example" }
	w := c.req("GET", "/api/budgets", "", foreignHost)
	if w.Code != 403 {
		t.Errorf("foreign host: %d", w.Code)
	}
	ipv6Loopback := func(r *http.Request) { r.Host = "[::1]:8080" }
	w = c.req("GET", "/api/budgets", "", ipv6Loopback)
	if w.Code != 200 {
		t.Errorf("ipv6 loopback: %d", w.Code)
	}
	// Cross-site form post (text/plain needs no CORS preflight): refused.
	w = c.req("POST", "/api/budgets/home/ops", `{"ops":[]}`, sendAsPlainText)
	if w.Code != 415 {
		t.Errorf("text/plain post: %d", w.Code)
	}
	withoutMarker := func(r *http.Request) { r.Header.Del("X-Requested-With") }
	w = c.req("DELETE", "/api/budgets/home", "", withoutMarker)
	if w.Code != 403 {
		t.Errorf("bare DELETE: %d", w.Code)
	}
	big := `{"id":"x","name":"` + strings.Repeat("a", 2<<20) + `"}`
	w = c.req("POST", "/api/budgets", big, nil)
	if w.Code != 413 {
		t.Errorf("huge body: %d", w.Code)
	}
}

func TestStaticAndMeta(t *testing.T) {
	c := setup(t)
	w := c.req("GET", "/", "", nil)
	eq(t, "index status", w.Code, 200)
	if !strings.Contains(w.Body.String(), "<title>ui</title>") {
		t.Fatalf("index body: %s", w.Body)
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'self'", "connect-src 'self'"} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("UI without a same-origin CSP, lacking %q: %q", directive, csp)
		}
	}
	w = c.req("POST", "/mcp", `{}`, nil)
	eq(t, "mcp status alongside the UI", w.Code, 299)
	w = c.req("POST", "/index.html", `{}`, nil)
	eq(t, "POST to a static file status", w.Code, 405)
	m := c.do("GET", "/api/meta", "", 200)
	if len(m["currencies"].([]any)) < 10 {
		t.Fatalf("meta lists too few currencies: %v", m["currencies"])
	}
	if m["op_kinds"] == nil {
		t.Fatal("meta lacks op_kinds")
	}
}

func TestSettings(t *testing.T) {
	c := setup(t)
	v := c.do("GET", "/api/settings", "", 200)
	eq(t, "fetch_rates", v["fetch_rates"], false)
	eq(t, "default_rate_source", v["default_rate_source"], "auto")
	eq(t, "auto_refresh", v["auto_refresh"], "off")
	eq(t, "coingecko_api_key_set", v["coingecko_api_key_set"], false)
	eq(t, "data path", v["paths"].(map[string]any)["data"], "/d")
	if v["auto_refresh_status"] == nil {
		t.Fatalf("no auto_refresh_status: %v", v)
	}

	withKey := `{"auto_refresh":"daily","coingecko_api_key":"CG-secret"}`
	w := c.req("PUT", "/api/settings", withKey, nil)
	eq(t, "settings PUT status", w.Code, 200)
	if strings.Contains(w.Body.String(), "CG-secret") {
		t.Fatalf("the key must never be sent back: %s", w.Body)
	}
	v = c.do("GET", "/api/settings", "", 200)
	eq(t, "auto_refresh after PUT", v["auto_refresh"], "daily")
	eq(t, "coingecko_api_key_set after PUT", v["coingecko_api_key_set"], true)
	c.do("PUT", "/api/settings", `{"auto_refresh":"weekly"}`, 400)
	c.do("PUT", "/api/settings", `{"colour":"blue"}`, 400)
	w = c.req("PUT", "/api/settings", `{"fetch_rates":false}`, sendAsPlainText)
	if w.Code != 415 {
		t.Fatalf("settings writes need JSON like every mutation: %d", w.Code)
	}

	// Offline: quotes and refreshes are refused with a clear conflict, not a 5xx.
	c.do("PUT", "/api/settings", `{"fetch_rates":false}`, 200)
	offline := c.do("GET", "/api/rates?base=EUR&codes=USD", "", 409)
	if !strings.Contains(offline["error"].(string), "turned off") {
		t.Fatal(offline)
	}
	c.do("POST", "/api/budgets/home/rates/refresh", `{"codes":["USD"]}`, 409)
}

func TestSettingsPageIsServed(t *testing.T) {
	st, _ := store.NewFileStore(t.TempDir())
	h := New(app.New(st, nil), Options{Static: web.FS()})
	pages := []string{
		"/settings.html", "/settings.js", "/", "/manifest.webmanifest", "/icon.svg",
		"/icon-192.png", "/icon-512.png", "/icon-maskable-512.png", "/icon-180.png",
	}
	for _, path := range pages {
		req := httptest.NewRequest("GET", "http://localhost"+path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Errorf("%s: %d", path, w.Code)
		}
		if path == "/" && !strings.Contains(w.Body.String(), `href="settings.html"`) {
			t.Error("the budget page must link to the settings page")
		}
		if path == "/manifest.webmanifest" {
			ct := w.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, "application/manifest+json") {
				t.Errorf("manifest served as %q: browsers won't install the app", ct)
			}
			var m struct {
				Display string `json:"display"`
				Icons   []struct{ Src, Purpose string }
			}
			if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
				t.Errorf("manifest: %v", err)
				continue
			}
			if m.Display != "standalone" {
				t.Errorf("manifest display = %q, want standalone", m.Display)
			}
			if len(m.Icons) < 3 {
				t.Errorf("manifest has %d icons, want at least 3", len(m.Icons))
			}
		}
	}
}

func TestMetaListsCrypto(t *testing.T) {
	v := setup(t).do("GET", "/api/meta", "", 200)
	crypto, sources := v["crypto"].([]any), v["rate_sources"].([]any)
	if len(crypto) == 0 {
		t.Fatal("no crypto currencies")
	}
	eq(t, "first crypto currency", crypto[0], "ADA")
	eq(t, "rate sources", len(sources), 5)
	digits := v["digits"].(map[string]any)
	eq(t, "EUR digits", digits["EUR"], 2.0)
	eq(t, "JPY digits", digits["JPY"], 0.0)
	eq(t, "BTC digits", digits["BTC"], 8.0)
}

func TestMetaReportsAPIVersion(t *testing.T) {
	c := setup(t)

	w := c.req("GET", "/api/meta", "", nil)
	var meta struct {
		APIVersion uint8 `json:"api_version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}

	eq(t, "api version", meta.APIVersion, 1)
}

// Budgets saved before pots existed (no "pots"/"withdrawals") or saved with
// null lists must load with every list as [], never null: the UI iterates them.
func TestOldStoredBudgetsLoadWithoutNullLists(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"id":"old","revision":3,"updated_at":"2026-10-01T00:00:00Z","budget":{
		"name":"Old","base_currency":"EUR","start_month":"2026-10","months":12,
		"savings_start":"250",
		"currencies":[],"categories":[{"id":"income","label":"Income","kind":"income"}],
		"items":[{"id":"pay","name":"Pay","amount":"1000","currency":"EUR","category":"income",
			"schedule":{"start":0,"every":1}}],
		"debts":[],"extra_to_debt_pct":"0","debt_strategy":"avalanche",
		"goals":[{"id":"emergency","name":"Emergency fund","target":"2000"}],"scenarios":[]}}`
	nulls := `{"id":"nulls","revision":1,"updated_at":"2026-10-01T00:00:00Z","budget":{
		"name":"Nulls","base_currency":"EUR","start_month":"2026-10","months":12,
		"currencies":null,"categories":[{"id":"income","label":"Income","kind":"income"}],
		"items":null,"debts":null,"extra_to_debt_pct":"0","debt_strategy":"avalanche",
		"pots":[{"id":"savings","name":"General savings","kind":"general","balance":"0"}],
		"withdrawals":null,"scenarios":null}}`
	for name, raw := range map[string]string{"old.json": legacy, "nulls.json": nulls} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(st, nil)
	a.Rates = offlineRates()
	a.SettingsPath = filepath.Join(t.TempDir(), "settings.json")
	c := &client{t, New(a, Options{Static: fstest.MapFS{}})}
	for _, id := range []string{"old", "nulls"} {
		w := c.req("GET", "/api/budgets/"+id, "", nil)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", id, w.Code, w.Body)
		}
		var v struct {
			Budget map[string]any `json:"budget"`
			Report map[string]any `json:"report"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		budgetLists := []string{
			"currencies", "categories", "items", "debts", "pots", "withdrawals", "scenarios",
		}
		for _, k := range budgetLists {
			if _, ok := v.Budget[k].([]any); !ok {
				t.Errorf("%s: budget.%s = %v, want a list", id, k, v.Budget[k])
			}
		}
		for _, k := range []string{"pots", "goals", "shortfalls", "insights"} {
			if _, ok := v.Report[k].([]any); !ok {
				t.Errorf("%s: report.%s = %v, want a list", id, k, v.Report[k])
			}
		}
	}
}

func TestDebtPayoffOverHTTP(t *testing.T) {
	c := setup(t)
	v := c.do("POST", "/api/budgets/home/ops", `{"ops":[
		{"op":"add_item","value":{"name":"Bike sale","amount":"3000","category":"income",
			"schedule":{"start":2,"every":0},"pot":"General"}},
		{"op":"add_debt","value":{"name":"Store card","balance":"2500","apr_pct":"24",
			"min_payment":"80","payoff_month":2,"payoff_from":"General"}}]}`, 200)
	d := v["budget"].(map[string]any)["debts"].([]any)[0].(map[string]any)
	eq(t, "stored payoff_from", d["payoff_from"], "savings")
	eq(t, "stored payoff_month", d["payoff_month"], 2.0)
	m2 := v["report"].(map[string]any)["months"].([]any)[2].(map[string]any)
	payoff := m2["payoff"].(map[string]any)["amount"]
	payoffFromPots := m2["payoff_from_pots"].(map[string]any)["amount"]
	if payoff == "0" {
		t.Fatalf("month 2 pays nothing off: %v", m2["payoff"])
	}
	eq(t, "month 2 payoff from pots", payoffFromPots, payoff)
	if m2["debts"].([]any)[0].(map[string]any)["balance"].(map[string]any)["amount"] != "0" {
		t.Errorf("not cleared: %v", m2["debts"])
	}

	// A payoff from a pot that doesn't exist is a validation issue with its path.
	b, _ := json.Marshal(v["budget"])
	bad := strings.Replace(string(b), `"payoff_from":"savings"`, `"payoff_from":"nowhere"`, 1)
	replaceBody := fmt.Sprintf(`{"revision":%v,"budget":%s}`, v["revision"], bad)
	out := c.do("PUT", "/api/budgets/home", replaceBody, 422)
	issues := out["issues"].([]any)
	eq(t, "issues", len(issues), 1)
	eq(t, "issue path", issues[0].(map[string]any)["path"], "debts[0].payoff_from")
	// The pot can't be removed while the payoff uses it.
	c.do("POST", "/api/budgets/home/ops", `{"ops":[
		{"op":"set_item","target":"bike","value":{"pot":""}},
		{"op":"remove_pot","target":"savings"}]}`, 400)
}

const setRentTo1200 = `{"ops":[{"op":"set_item","target":"rent","value":{"amount":"1200"}}]}`
const setRentTo1300 = `{"ops":[{"op":"set_item","target":"rent","value":{"amount":"1300"}}]}`

func TestHistoryListsBrowserChanges(t *testing.T) {
	c := setup(t)
	c.do("POST", "/api/budgets/home/ops", setRentTo1200, 200)

	w := c.req("GET", "/api/budgets/home/history", "", nil)

	var entries []map[string]any
	eq(t, "history status", w.Code, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("history: %v %s", err, w.Body)
	}
	if len(entries) != 2 {
		t.Fatalf("want the creation and one change, got %d entries", len(entries))
	}
	if entries[0]["source"] != "web" {
		t.Errorf("source: %v", entries[0]["source"])
	}
	if entries[0]["summary"] != "Rent: amount 1150 → 1200" {
		t.Errorf("summary: %v", entries[0]["summary"])
	}
	if _, listed := entries[0]["budget"]; listed {
		t.Error("history listings must not carry whole budgets")
	}
}

func TestHistoryLimitMustBeAWholeNumber(t *testing.T) {
	c := setup(t)

	c.do("GET", "/api/budgets/home/history?limit=x", "", 400)
}

// With r2 (rent 1200) and r3 (rent 1300) saved after the creation.
func TestUndoAnswers(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"the latest change", `{}`, 200},
		{"a change with a later one on top", `{"revision":2}`, 409},
		{"forced, having seen everything", `{"revision":2,"force":true,"seen":3}`, 200},
		{"forced, without having seen r3", `{"revision":2,"force":true,"seen":2}`, 409},
		{"the creation", `{"revision":1}`, 409},
		{"a revision never written", `{"revision":99}`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := setup(t)
			c.do("POST", "/api/budgets/home/ops", setRentTo1200, 200)
			c.do("POST", "/api/budgets/home/ops", setRentTo1300, 200)

			c.do("POST", "/api/budgets/home/undo", tc.body, tc.want)
		})
	}
}

func TestUndoRefusalListsLaterChanges(t *testing.T) {
	c := setup(t)
	c.do("POST", "/api/budgets/home/ops", setRentTo1200, 200)
	c.do("POST", "/api/budgets/home/ops", setRentTo1300, 200)

	refusal := c.do("POST", "/api/budgets/home/undo", `{"revision":2}`, 409)

	if later := refusal["later"].([]any); len(later) != 1 {
		t.Errorf("later changes: %v", later)
	}
}

func TestLocaleNegotiation(t *testing.T) {
	c := setup(t)
	ru := func(r *http.Request) { r.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8") }
	get := func(path string, mut func(*http.Request)) map[string]any {
		t.Helper()
		w := c.req("GET", path, "", mut)
		var out map[string]any
		if w.Code != 200 {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("GET %s: %v %s", path, err, w.Body)
		}
		return out
	}
	russianMeta := get("/api/meta", ru)
	eq(t, "locale", russianMeta["locale"], "ru")
	if len(russianMeta["locales"].([]any)) < 2 {
		t.Fatalf("meta lists too few locales: %v", russianMeta["locales"])
	}
	month := func(v map[string]any) string {
		return v["report"].(map[string]any)["months"].([]any)[0].(map[string]any)["label"].(string)
	}
	ruView, enView := get("/api/budgets/home", ru), get("/api/budgets/home", nil)
	if !strings.ContainsAny(month(ruView), cyrillic) {
		t.Fatalf("Russian month label %q has no Cyrillic letters", month(ruView))
	}
	if strings.ContainsAny(month(enView), cyrillic) {
		t.Fatalf("English month label %q has Cyrillic letters", month(enView))
	}

	// Validation issues come back in the reader's language, with their key.
	nameless := `{"revision":1,"budget":{"name":"","base_currency":"EUR",` +
		`"start_month":"2026-10","months":12}}`
	w := c.req("PUT", "/api/budgets/home", nameless, ru)
	var bad struct {
		Issues []struct{ Path, Message, Key string } `json:"issues"`
	}
	eq(t, "issues status", w.Code, 422)
	if err := json.Unmarshal(w.Body.Bytes(), &bad); err != nil {
		t.Fatalf("issues: %v %s", err, w.Body)
	}
	eq(t, "issue message", bad.Issues[0].Message, "обязательное поле")
	eq(t, "issue key", bad.Issues[0].Key, "issue.required")

	// An explicit language setting wins over the browser's.
	c.do("PUT", "/api/settings", `{"locale":"en"}`, 200)
	if m := get("/api/meta", ru); m["locale"] != "en" {
		t.Fatalf("setting should win: %v", m["locale"])
	}
	c.do("PUT", "/api/settings", `{"locale":"not a tag!"}`, 400)
}

func TestHistoryInTheReadersLanguage(t *testing.T) {
	c := setup(t)
	russian := func(r *http.Request) { r.Header.Set("Accept-Language", "ru") }

	w := c.req("GET", "/api/budgets/home/history", "", russian)

	var history []struct{ Summary string }
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := history[len(history)-1].Summary; got != "создан" {
		t.Errorf("creation in Russian: %q", got)
	}
}

// thisMonth is the month the example budget starts in: setup creates it
// today.
func thisMonth() string { return time.Now().Format("2006-01") }

func (c *client) logTwo() map[string]any {
	c.t.Helper()
	m := thisMonth()
	body := `{"transactions":[
		{"date":"` + m + `-02","amount":"45.20","category":"home","external_id":"r1"},
		{"date":"` + m + `-03","amount":"12","currency":"USD","category":"education"}]}`
	return c.do("POST", "/api/budgets/home/transactions", body, http.StatusCreated)
}

func TestLogTransactions(t *testing.T) {
	c := setup(t)

	logged := c.logTwo()

	if logged["batch"] != "b1" {
		t.Errorf("batch: %v", logged["batch"])
	}
	if rows := logged["logged"].([]any); len(rows) != 2 {
		t.Errorf("logged: %v", rows)
	}
}

func TestLogTransactionsRejectsAnUnknownCategory(t *testing.T) {
	c := setup(t)
	body := `{"transactions":[{"date":"` + thisMonth() + `-02","amount":"1","category":"nope"}]}`

	c.do("POST", "/api/budgets/home/transactions", body, http.StatusUnprocessableEntity)
}

func TestListTransactionsNewestFirst(t *testing.T) {
	c := setup(t)
	c.logTwo()
	m := thisMonth()

	w := c.req("GET", "/api/budgets/home/transactions?from="+m+"&to="+m, "", nil)

	var list []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(list) != 2 {
		t.Fatalf("list: %v", list)
	}
	if list[0]["date"] != m+"-03" {
		t.Errorf("newest first: %v", list[0]["date"])
	}
}

func TestUpdateTransaction(t *testing.T) {
	c := setup(t)
	c.logTwo()

	updated := c.do("PUT", "/api/budgets/home/transactions/t1", `{"note":"Lidl"}`, http.StatusOK)

	if updated["note"] != "Lidl" {
		t.Errorf("note: %v", updated["note"])
	}
	if updated["base"] != "45.2" {
		t.Errorf("a note leaves the base value: %v", updated["base"])
	}
}

func TestUpdateUnknownTransaction(t *testing.T) {
	c := setup(t)

	c.do("PUT", "/api/budgets/home/transactions/t9", `{"note":"x"}`, http.StatusNotFound)
}

func TestMonthStatus(t *testing.T) {
	c := setup(t)
	c.logTwo()

	status := c.do("GET", "/api/budgets/home/status?month="+thisMonth(), "", http.StatusOK)

	if status["transaction_count"] != 2.0 {
		t.Errorf("transaction_count: %v", status["transaction_count"])
	}
}

func TestMonthStatusOutsideTheBudget(t *testing.T) {
	c := setup(t)

	c.do("GET", "/api/budgets/home/status?month=1999-01", "", http.StatusBadRequest)
}

func TestDeleteTransactionThenItsBatch(t *testing.T) {
	c := setup(t)
	c.logTwo()

	c.do("DELETE", "/api/budgets/home/transactions/t1", "", http.StatusOK)
	deleted := c.do("DELETE", "/api/budgets/home/transactions?batch=b1", "", http.StatusOK)

	if deleted["deleted"] != 1.0 {
		t.Errorf("removed: %v", deleted["deleted"])
	}
}
