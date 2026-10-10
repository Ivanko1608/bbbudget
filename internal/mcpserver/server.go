// Package mcpserver exposes budgets to AI assistants over the Model Context
// Protocol. Every tool is a thin wrapper over app.App.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

const instructions = `bbbudget: multi-currency household budgets with exact money.

Model: a budget has a fiat base currency, other currencies including crypto (BTC, ETH, USDT, ...) with a per_base rate, transfer_fee_pct lost on every payment in that currency and cash_out_fee_pct lost when income in it is converted to base, categories (income|expense), items (amount in any configured currency, with a schedule), debts (balance, APR, minimum payment; a share of each month's leftover, extra_to_debt_pct, goes to debts by strategy avalanche|snowball|listed), and savings goals.
Amounts are exact decimal STRINGS ("1150", "12.50"); never send floats. Months are "YYYY-MM".
Rounding is pessimistic: costs round up, income/receipts round down, to the minor unit.
Rates: a currency's source is "manual" (typed) or a provider (auto = official ECB rate, falling back to currency-api for crypto and other currencies; ecb; currency-api; coingecko). refresh_rates fetches current rates; set_currency fetches automatically when adding a currency without per_base. Never invent a rate: fetch it.

Workflow: call list_budgets, then get_budget before answering about numbers. Use what_if for hypotheticals (nothing is saved). Only use mutating tools when the user clearly asks for a change, then state what changed and that it can be undone.
Every write is recorded in the budget's history with who made it (mcp = you, web = the person in the browser, cli, auto-refresh). history lists recent changes; undo reverts one. If the person says "undo that", undo your own last change; if someone else changed the budget since, undo refuses and lists their changes, so ask before forcing it. Use save_scenario + compare_scenarios to explore alternatives side by side. Items/debts/goals can be referenced by id, exact name, or an unambiguous part of a name.`

// Options configures the server.
type Options struct {
	// DefaultBudget is used when a tool call omits "budget".
	DefaultBudget string
	Version       string
}

// New builds an MCP server over the app.
func New(a *app.App, opts Options) *mcp.Server {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "bbbudget", Title: "Budget planner", Version: opts.Version},
		&mcp.ServerOptions{Instructions: instructions})
	h := &handlers{app: a, def: opts.DefaultBudget}
	h.register(s)
	return s
}

type handlers struct {
	app *app.App
	def string
}

var schemaOpts = &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
	reflect.TypeFor[money.Decimal]():   {Type: "string", Pattern: `^[+-]?[0-9]+(\.[0-9]+)?$`, Description: "exact decimal as a string"},
	reflect.TypeFor[json.RawMessage](): {Type: "object"},
}}

// tool registers a handler whose input schema is inferred from In with the
// money-aware type mapping above. Handlers return any JSON-able value.
func tool[In any](s *mcp.Server, name, desc string, fn func(context.Context, In) (any, error)) {
	schema, err := jsonschema.For[In](schemaOpts)
	if err != nil {
		panic(fmt.Sprintf("tool %s: %v", name, err))
	}
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc, InputSchema: schema},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
			out, err := fn(store.WithChange(ctx, store.Change{Source: "mcp"}), in)
			if err != nil {
				return nil, nil, err // reported to the model as a tool error
			}
			raw, err := json.MarshalIndent(out, "", " ")
			if err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil, nil
		})
}

func (h *handlers) id(ref string) (string, error) {
	if ref = strings.TrimSpace(ref); ref != "" {
		return ref, nil
	}
	if h.def != "" {
		return h.def, nil
	}
	return "", fmt.Errorf(`"budget" is required (no default budget configured); call list_budgets`)
}

// --- inputs ---

type Ref struct {
	Budget string `json:"budget,omitempty" jsonschema:"budget id; omit to use the default budget"`
}

type CreateIn struct {
	ID       string `json:"id" jsonschema:"new budget id: lowercase letters, digits, '-'"`
	Name     string `json:"name,omitempty" jsonschema:"display name"`
	Template string `json:"template,omitempty" jsonschema:"template name from list_templates; default blank"`
}

type ScheduleIn struct {
	Every  *int   `json:"every,omitempty" jsonschema:"period in months: 1 monthly (default), 3 quarterly, 12 yearly, 0 one-off"`
	Starts string `json:"starts,omitempty" jsonschema:"first month YYYY-MM; default is the budget's start month"`
	Ends   string `json:"ends,omitempty" jsonschema:"last month YYYY-MM (inclusive); omit for no end"`
}

type AddItemIn struct {
	Ref
	Name     string        `json:"name"`
	Amount   money.Decimal `json:"amount"`
	Currency string        `json:"currency,omitempty" jsonschema:"defaults to the base currency"`
	Category string        `json:"category" jsonschema:"category id; must exist (see get_budget)"`
	ScheduleIn
}

type UpdateItemIn struct {
	Ref
	Item     string         `json:"item" jsonschema:"item id or name"`
	Name     *string        `json:"name,omitempty"`
	Amount   *money.Decimal `json:"amount,omitempty"`
	Currency *string        `json:"currency,omitempty"`
	Category *string        `json:"category,omitempty"`
	ScheduleIn
}

type ItemRef struct {
	Ref
	Item string `json:"item" jsonschema:"item id or name"`
}

type CurrencyIn struct {
	Ref
	Code           string         `json:"code" jsonschema:"ISO code or crypto ticker, e.g. USD, BTC, USDT"`
	PerBase        *money.Decimal `json:"per_base,omitempty" jsonschema:"units of this currency per 1 base unit; setting it makes the rate manual. Omit to fetch the current rate"`
	Source         *string        `json:"source,omitempty" jsonschema:"where the rate comes from: manual, auto, ecb, currency-api or coingecko"`
	TransferFeePct *money.Decimal `json:"transfer_fee_pct,omitempty" jsonschema:"percent lost on every payment in this currency"`
	CashOutFeePct  *money.Decimal `json:"cash_out_fee_pct,omitempty" jsonschema:"percent lost when income in this currency is converted to base (exchange fee + spread)"`
}

type RatesIn struct {
	Ref
	Codes []string `json:"codes,omitempty" jsonschema:"currencies to refresh; omit for every currency whose source isn't manual. Naming a manual one switches it to auto"`
}

type SettingsIn struct {
	Ref
	budget.SettingsPatch
}

type DebtIn struct {
	Ref
	Debt       string         `json:"debt" jsonschema:"debt id or name; created if no debt matches"`
	Currency   *string        `json:"currency,omitempty"`
	Balance    *money.Decimal `json:"balance,omitempty"`
	APRPct     *money.Decimal `json:"apr_pct,omitempty" jsonschema:"yearly interest in percent"`
	MinPayment *money.Decimal `json:"min_payment,omitempty" jsonschema:"monthly minimum payment in the debt's currency"`
}

type OpsIn struct {
	Ref
	Ops []budget.Op `json:"ops" jsonschema:"edits applied in order, atomically"`
	// ExpectedRevision guards against concurrent edits; omit to apply on top of the latest.
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
}

type ScenarioIn struct {
	Ref
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Ops         []budget.Op `json:"ops"`
}

type CompareIn struct {
	Ref
	Scenarios []string `json:"scenarios,omitempty" jsonschema:"scenario ids or names; omit for all"`
}

type HistoryIn struct {
	Ref
	Limit int `json:"limit,omitempty" jsonschema:"how many recent changes to list; default 20"`
}

type UndoIn struct {
	Ref
	Change int64 `json:"change,omitempty" jsonschema:"revision of the change to undo (see history); omit for the latest change"`
	Force  bool  `json:"force,omitempty" jsonschema:"also undo every change made after it. Only with the person's explicit go-ahead"`
}

type ScenarioRef struct {
	Ref
	Scenario string `json:"scenario" jsonschema:"scenario id or name"`
}

func opsHelp() string {
	keys := make([]string, 0, len(budget.OpKinds))
	for k := range budget.OpKinds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "\n- %s: %s", k, budget.OpKinds[k])
	}
	return b.String() + `
Value fields: Item{name, amount, currency, category, schedule{start, every, end}} (schedule uses 0-based month indexes from start_month), Debt{name, currency, balance, apr_pct, min_payment}, Goal{name, target}, Category{id, label, kind}, SettingsPatch{name, description, start_month, months, savings_start, extra_to_debt_pct, debt_strategy}, CurrencyPatch{per_base, transfer_fee_pct, cash_out_fee_pct, source}. Amounts are decimal strings.`
}

func (h *handlers) register(s *mcp.Server) {
	tool(s, "list_budgets", "List stored budgets.", func(ctx context.Context, _ struct{}) (any, error) {
		return h.app.List(ctx)
	})
	tool(s, "list_templates", "List templates a new budget can start from.", func(ctx context.Context, _ struct{}) (any, error) {
		return h.app.Templates.List(), nil
	})
	tool(s, "create_budget", "Create a budget from a template (see list_templates).", func(ctx context.Context, in CreateIn) (any, error) {
		v, err := h.app.Create(ctx, in.ID, in.Name, in.Template)
		return sum(v, err)
	})
	tool(s, "get_budget", "The full budget: settings, every item with its base-currency cost, debts, goals, scenarios, the month-by-month projection, totals and plain-language insights. Call this first.",
		func(ctx context.Context, in Ref) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			return sum(h.app.Get(ctx, id))
		})

	tool(s, "add_item", "Add an income or cost item. Returns the updated budget.", func(ctx context.Context, in AddItemIn) (any, error) {
		return h.withSchedule(ctx, in.Budget, in.ScheduleIn, func(sch *budget.Schedule) []budget.Op {
			l := budget.Item{Name: in.Name, Amount: in.Amount, Currency: in.Currency, Category: in.Category, Schedule: budget.Monthly()}
			if sch != nil {
				l.Schedule = *sch
			}
			return []budget.Op{budget.NewOp("add_item", "", l)}
		})
	})
	tool(s, "update_item", "Change fields of one item; omitted fields stay as they are. Returns the updated budget.", func(ctx context.Context, in UpdateItemIn) (any, error) {
		return h.withSchedule(ctx, in.Budget, in.ScheduleIn, func(sch *budget.Schedule) []budget.Op {
			return []budget.Op{budget.NewOp("set_item", in.Item, budget.ItemPatch{Name: in.Name, Amount: in.Amount, Currency: in.Currency, Category: in.Category, Schedule: sch})}
		})
	})
	tool(s, "remove_item", "Delete one item. Returns the updated budget.", func(ctx context.Context, in ItemRef) (any, error) {
		return h.apply(ctx, in.Budget, store.AnyRevision, budget.NewOp("remove_item", in.Item, nil))
	})
	tool(s, "set_currency", "Add a currency (fiat or crypto) or change its rate, rate source, transfer loss or cash-out loss. A new currency without per_base gets the current market rate fetched (source auto). Returns the updated budget.",
		func(ctx context.Context, in CurrencyIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			return sum(h.app.SetCurrency(ctx, id, in.Code, budget.CurrencyPatch{PerBase: in.PerBase, Source: in.Source, TransferFeePct: in.TransferFeePct, CashOutFeePct: in.CashOutFeePct}))
		})
	tool(s, "refresh_rates", "Fetch current exchange rates (official ECB rates, crypto prices) and save them. Returns old and new rate per currency, plus the updated budget.",
		func(ctx context.Context, in RatesIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			v, updates, err := h.app.RefreshRates(ctx, id, in.Codes)
			if err != nil {
				if len(updates) > 0 {
					return nil, fmt.Errorf("%w; per currency: %+v", err, updates)
				}
				return nil, err
			}
			return map[string]any{"rate_updates": updates, "budget": summarize(v)}, nil
		})
	tool(s, "update_settings", "Change budget settings (name, start_month, months, savings_start, extra_to_debt_pct, debt_strategy). Returns the updated budget.",
		func(ctx context.Context, in SettingsIn) (any, error) {
			return h.apply(ctx, in.Budget, store.AnyRevision, budget.NewOp("set_settings", "", in.SettingsPatch))
		})
	tool(s, "set_debt", "Create or update a debt (credit card, loan). Creating needs currency (default base), balance, apr_pct and min_payment. Returns the updated budget.",
		func(ctx context.Context, in DebtIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			return sum(h.app.SetDebt(ctx, id, in.Debt, budget.DebtPatch{Currency: in.Currency, Balance: in.Balance, APRPct: in.APRPct, MinPayment: in.MinPayment}))
		})
	tool(s, "apply_ops", "Apply any edits atomically (all or nothing) and save. Covers everything the specific tools do plus categories, goals and removals. Op kinds:"+opsHelp(),
		func(ctx context.Context, in OpsIn) (any, error) {
			rev := store.AnyRevision
			if in.ExpectedRevision != nil {
				rev = *in.ExpectedRevision
			}
			return h.apply(ctx, in.Budget, rev, in.Ops...)
		})
	tool(s, "what_if", "Preview edits WITHOUT saving: returns the projection and insights as if the ops were applied. Same op format as apply_ops.",
		func(ctx context.Context, in OpsIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			nb, r, err := h.app.WhatIf(ctx, id, in.Ops)
			if err != nil {
				return nil, err
			}
			out := summarizeReport(nb, r)
			out.ID = id + " (what-if, not saved)"
			return out, nil
		})

	tool(s, "save_scenario", "Save a named set of ops as a scenario to compare later. The real budget is not changed.", func(ctx context.Context, in ScenarioIn) (any, error) {
		id, err := h.id(in.Budget)
		if err != nil {
			return nil, err
		}
		return h.app.SaveScenario(ctx, id, in.Name, in.Description, in.Ops)
	})
	tool(s, "compare_scenarios", "Compare the current budget against saved scenarios: savings, goal dates, debt payoff, interest.", func(ctx context.Context, in CompareIn) (any, error) {
		id, err := h.id(in.Budget)
		if err != nil {
			return nil, err
		}
		out, err := h.app.Compare(ctx, id, in.Scenarios)
		if err != nil {
			return nil, err
		}
		return outcomeRows(out), nil
	})
	tool(s, "adopt_scenario", "Apply a saved scenario to the real budget and remove it from the scenario list. Returns the updated budget.", func(ctx context.Context, in ScenarioRef) (any, error) {
		id, err := h.id(in.Budget)
		if err != nil {
			return nil, err
		}
		return sum(h.app.AdoptScenario(ctx, id, in.Scenario))
	})
	tool(s, "delete_scenario", "Delete a saved scenario.", func(ctx context.Context, in ScenarioRef) (any, error) {
		id, err := h.id(in.Budget)
		if err != nil {
			return nil, err
		}
		if err := h.app.DeleteScenario(ctx, id, in.Scenario); err != nil {
			return nil, err
		}
		return map[string]string{"deleted": in.Scenario}, nil
	})

	tool(s, "history", "Recent changes to the budget, newest first: revision, time, who made it (mcp, web, cli, auto-refresh) and what changed.",
		func(ctx context.Context, in HistoryIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			if in.Limit <= 0 {
				in.Limit = 20
			}
			return h.app.History(ctx, id, in.Limit)
		})
	tool(s, "undo", "Undo a change by restoring the version before it, saved as a new revision (so an undo can be undone too). Refuses if later changes exist unless force. Returns the updated budget.",
		func(ctx context.Context, in UndoIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			return sum(h.app.Undo(ctx, id, in.Change, in.Force))
		})

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name: "budget", URITemplate: "budget://{id}", MIMEType: "application/json",
		Description: "The raw stored budget document (exact decimal strings).",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		id := strings.TrimPrefix(req.Params.URI, "budget://")
		r, err := h.app.Store.Get(ctx, id)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		raw, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "application/json", Text: string(raw)}}}, nil
	})
}

func (h *handlers) apply(ctx context.Context, ref string, rev int64, ops ...budget.Op) (any, error) {
	id, err := h.id(ref)
	if err != nil {
		return nil, err
	}
	return sum(h.app.Apply(ctx, id, rev, ops))
}

// withSchedule resolves YYYY-MM schedule fields against the budget's start
// month, then applies the ops built by mk (sch is nil when no schedule field was given).
func (h *handlers) withSchedule(ctx context.Context, ref string, in ScheduleIn, mk func(*budget.Schedule) []budget.Op) (any, error) {
	id, err := h.id(ref)
	if err != nil {
		return nil, err
	}
	var sch *budget.Schedule
	if in.Every != nil || in.Starts != "" || in.Ends != "" {
		v, err := h.app.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		s := budget.Monthly()
		if in.Every != nil {
			s.Every = *in.Every
		}
		if in.Starts != "" {
			if s.Start, err = monthIndex(v.Budget.StartMonth, in.Starts); err != nil {
				return nil, err
			}
		}
		if in.Ends != "" {
			e, err := monthIndex(v.Budget.StartMonth, in.Ends)
			if err != nil {
				return nil, err
			}
			s.End = &e
		}
		sch = &s
	}
	return h.apply(ctx, id, store.AnyRevision, mk(sch)...)
}

func monthIndex(start, ym string) (int, error) {
	s, err := time.Parse("2006-01", start)
	if err != nil {
		return 0, err
	}
	t, err := time.Parse("2006-01", ym)
	if err != nil {
		return 0, fmt.Errorf("month must be YYYY-MM, got %q", ym)
	}
	i := (t.Year()-s.Year())*12 + int(t.Month()-s.Month())
	if i < 0 {
		return 0, fmt.Errorf("%s is before the budget starts (%s)", ym, start)
	}
	return i, nil
}

func sum(v *app.View, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return summarize(v), nil
}
