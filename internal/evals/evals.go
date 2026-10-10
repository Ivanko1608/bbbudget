// Package evals runs the assistant against the cases in evals/ and checks
// what it did. See evals/README.md.
package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.yaml.in/yaml/v3"

	"bbbudget/internal/app"
	"bbbudget/internal/assistant"
	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/rates"
	"bbbudget/internal/store"
)

// Case is one eval: a conversation and what should be true after it.
type Case struct {
	Name     string   `yaml:"name"`
	Template string   `yaml:"template"`
	Turns    []string `yaml:"turns"`
	// Reference is, per turn, tool calls a correct assistant could make.
	Reference      [][]Call `yaml:"reference"`
	ReferenceReply string   `yaml:"reference_reply"`
	Expect         Expect   `yaml:"expect"`
}

// Call is one tool call in a reference solution.
type Call struct {
	Tool  string         `yaml:"tool"`
	Input map[string]any `yaml:"input"`
}

// Expect are the checks; unset ones are skipped.
type Expect struct {
	Saved         *bool                        `yaml:"saved"`
	WritesBy      string                       `yaml:"writes_by"`
	Items         map[string]map[string]string `yaml:"items"`
	NoItemNamed   []string                     `yaml:"no_item_named"`
	Transactions  *int                         `yaml:"transactions"`
	Transaction   map[string]string            `yaml:"transaction"`
	ReplyScript   string                       `yaml:"reply_script"`
	ReplyContains []string                     `yaml:"reply_contains"`
}

// Load reads every case in dir.
func Load(dir string) ([]Case, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []Case
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var c Case
		d := yaml.NewDecoder(strings.NewReader(string(raw)))
		d.KnownFields(true)
		if err := d.Decode(&c); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if c.Name == "" {
			c.Name = strings.TrimSuffix(filepath.Base(f), ".yaml")
		}
		if c.Template == "" {
			c.Template = "example"
		}
		if len(c.Turns) == 0 {
			return nil, fmt.Errorf("%s: no turns", f)
		}
		if c.Reference != nil && len(c.Reference) != len(c.Turns) {
			return nil, fmt.Errorf("%s: reference needs one list of calls per turn (%d turns, %d lists)", f, len(c.Turns), len(c.Reference))
		}
		out = append(out, c)
	}
	return out, nil
}

// Today is the fixed date every case runs on, so "this month" and dates in
// cases mean the same thing every time. The example budget starts that month.
var Today = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// Result is how one run of a case went.
type Result struct {
	Case     string
	Failures []string
	Reply    string
	Tools    []string
	Err      error
}

// Passed reports whether every check held.
func (r Result) Passed() bool { return r.Err == nil && len(r.Failures) == 0 }

// Run plays a case's turns through the assistant with provider p, on a
// fresh budget in a fresh directory, then checks the outcome.
func Run(ctx context.Context, c Case, p assistant.Provider, dir string) Result {
	res := Result{Case: c.Name}
	st, err := store.NewFileStore(dir)
	if err != nil {
		res.Err = err
		return res
	}
	a := app.New(st, nil)
	a.Rates = map[string]rates.Provider{} // evals never fetch rates
	a.Now = func() time.Time { return Today }
	a.SettingsPath = filepath.Join(dir, "settings.json")
	if _, err := a.Create(ctx, "home", "Home", c.Template); err != nil {
		res.Err = err
		return res
	}
	as := &assistant.Assistant{App: a, Provider: func() (assistant.Provider, error) { return p, nil }, Now: func() time.Time { return Today }}
	session := ""
	for _, turn := range c.Turns {
		var texts []string
		session, err = as.Reply(ctx, session, "home", turn, func(e assistant.Event) {
			switch e.Type {
			case "text":
				texts = append(texts, e.Text)
			case "tool":
				res.Tools = append(res.Tools, e.Tool)
			}
		})
		if err != nil {
			res.Err = err
			return res
		}
		res.Reply = strings.Join(texts, "\n")
	}
	res.Failures = check(ctx, a, c.Expect, res.Reply)
	return res
}

func check(ctx context.Context, a *app.App, e Expect, reply string) []string {
	var fails []string
	fail := func(format string, args ...any) { fails = append(fails, fmt.Sprintf(format, args...)) }
	v, err := a.Get(ctx, "home")
	if err != nil {
		return []string{err.Error()}
	}
	h, _ := a.History(ctx, "home", 0)
	if e.Saved != nil && (v.Revision > 1) != *e.Saved {
		fail("saved: want %v, the budget is at revision %d", *e.Saved, v.Revision)
	}
	if e.WritesBy != "" {
		for _, entry := range h {
			if entry.Revision > 1 && entry.Source != e.WritesBy {
				fail("r%d was written by %q, not %q", entry.Revision, entry.Source, e.WritesBy)
			}
		}
	}
	items := map[string]budget.Item{}
	for _, l := range v.Budget.Items {
		items[l.ID] = l
	}
	for id, want := range e.Items {
		l, ok := items[id]
		if !ok {
			fail("no item %q (have %s)", id, itemIDs(v.Budget.Items))
			continue
		}
		got := map[string]string{
			"amount": l.Amount.String(), "currency": l.Currency, "category": l.Category, "name": l.Name,
			"every": fmt.Sprint(l.Schedule.Every), "starts": budget.MonthKey(v.Budget.StartMonth, l.Schedule.Start),
		}
		for k, w := range want {
			if g, ok := got[k]; !ok {
				fail("item %s: unknown field %q in the case", id, k)
			} else if !sameValue(g, w) {
				fail("item %s: %s is %q, want %q", id, k, g, w)
			}
		}
	}
	for _, name := range e.NoItemNamed {
		for _, l := range v.Budget.Items {
			if strings.Contains(strings.ToLower(l.Name), strings.ToLower(name)) {
				fail("item %q should not exist", l.Name)
			}
		}
	}
	txs, _ := a.ListTransactions(ctx, "home", app.TxFilter{})
	if e.Transactions != nil && len(txs) != *e.Transactions {
		fail("transactions: %d logged, want %d", len(txs), *e.Transactions)
	}
	if e.Transaction != nil && !anyTx(txs, e.Transaction) {
		raw, _ := json.Marshal(txs)
		fail("no logged transaction matches %v; have %s", e.Transaction, raw)
	}
	if e.ReplyScript != "" && !inScript(reply, e.ReplyScript) {
		fail("the reply is not in %s script: %q", e.ReplyScript, reply)
	}
	for _, want := range e.ReplyContains {
		if !strings.Contains(digitsOnly(reply), digitsOnly(want)) {
			fail("the reply doesn't mention %q: %q", want, reply)
		}
	}
	return fails
}

func itemIDs(ls []budget.Item) string {
	var ids []string
	for _, l := range ls {
		ids = append(ids, l.ID)
	}
	return strings.Join(ids, ", ")
}

// sameValue compares decimals by value ("1200" = "1200.00") and the rest as text.
func sameValue(got, want string) bool {
	if got == want {
		return true
	}
	g, err1 := money.ParseDecimal(got)
	w, err2 := money.ParseDecimal(want)
	return err1 == nil && err2 == nil && g.Rat().Cmp(w.Rat()) == 0
}

func anyTx(txs []budget.Transaction, want map[string]string) bool {
	for _, t := range txs {
		got := map[string]string{"date": t.Date, "amount": t.Amount.String(), "currency": t.Currency, "category": t.Category, "base": t.Base.String()}
		ok := true
		for k, w := range want {
			if !sameValue(got[k], w) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

var grouping = regexp.MustCompile(`(\d)[,\s\x{00a0}\x{202f}'](\d{3})`)

// digitsOnly removes digit grouping, so "5,100", "5 100" and "5100" match.
func digitsOnly(s string) string {
	for {
		n := grouping.ReplaceAllString(s, "$1$2")
		if n == s {
			return s
		}
		s = n
	}
}

// inScript reports whether most letters of s are in the named script.
func inScript(s, script string) bool {
	table := map[string]*unicode.RangeTable{"cyrillic": unicode.Cyrillic, "latin": unicode.Latin}[script]
	if table == nil {
		return false
	}
	in, letters := 0, 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
			if unicode.Is(table, r) {
				in++
			}
		}
	}
	return letters > 0 && in*2 > letters
}

// Reference is a scripted model that makes a case's reference tool calls,
// one list per turn, then answers with the reference reply. It shows a case
// can be passed, with no model involved.
type Reference struct {
	c    Case
	turn int
	step int // 0: make the calls, 1: answer
}

// NewReference returns the scripted model for c.
func NewReference(c Case) *Reference { return &Reference{c: c} }

func (r *Reference) Name() string  { return "reference" }
func (r *Reference) Model() string { return "reference" }
func (r *Reference) Start(string, []assistant.Tool) assistant.Conversation {
	return r
}

func (r *Reference) Send(_ context.Context, text string, results []assistant.ToolResult) (assistant.Turn, error) {
	for _, res := range results {
		if res.IsError {
			return assistant.Turn{}, fmt.Errorf("reference call failed: %s", res.Content)
		}
	}
	if text != "" { // a new turn from the person
		if r.turn >= len(r.c.Reference) {
			return assistant.Turn{}, errors.New("more turns than reference lists")
		}
		calls := r.c.Reference[r.turn]
		r.turn++
		if len(calls) > 0 {
			var out []assistant.ToolCall
			for i, c := range calls {
				raw, err := json.Marshal(c.Input)
				if err != nil {
					return assistant.Turn{}, err
				}
				out = append(out, assistant.ToolCall{ID: fmt.Sprintf("r%d-%d", r.turn, i), Name: c.Tool, Input: raw})
			}
			return assistant.Turn{Calls: out}, nil
		}
	}
	reply := r.c.ReferenceReply
	if reply == "" {
		reply = "Done."
	}
	return assistant.Turn{Text: reply}, nil
}
