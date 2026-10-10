package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/store"
)

type WhatsNewIn struct {
	Ref
	Since int64 `json:"since_revision,omitempty" jsonschema:"compare against this revision; default: your own last change"`
}

type changeRow struct {
	Revision int64  `json:"revision"`
	At       string `json:"at"`
	By       string `json:"by"`
	Summary  string `json:"summary"`
}

type whatsNewOut struct {
	Budget   string `json:"budget"`
	Since    int64  `json:"since_revision"`
	Revision int64  `json:"revision"`
	// ByOthers are changes made since then by someone else (the person in
	// the browser, the command line, a scheduled rate refresh).
	ByOthers []changeRow `json:"changes_by_others"`
	// Effect is what everything since then did to the outcome.
	Change   string     `json:"change"`
	Currency string     `json:"currency"`
	Effect   effect     `json:"effect"`
	Month    *statusOut `json:"this_month,omitempty"`
}

func (h *handlers) whatsNew(ctx context.Context, id string, since int64) (any, error) {
	w, err := h.app.WhatsNew(ctx, id, since)
	if err != nil {
		return nil, err
	}
	d, err := h.delta(ctx, id, w.Revision, true, w.Before, w.Now.Budget, w.Now.Report)
	if err != nil {
		return nil, err
	}
	dr := d.(deltaResult)
	out := whatsNewOut{Budget: id, Since: w.Since, Revision: w.Revision, ByOthers: []changeRow{},
		Change: dr.Change, Currency: dr.Currency, Effect: dr.Effect}
	for _, e := range w.Others {
		out.ByOthers = append(out.ByOthers, changeRow{e.Revision, e.At.Format("2006-01-02 15:04"), e.Source, e.Summary})
	}
	if w.Month != nil {
		s := statusResult(w.Month)
		out.Month = &s
	}
	return out, nil
}

func (h *handlers) registerWhatsNew(s *mcp.Server) {
	tool(s, "whats_new", "What happened to the budget since your own last change (or since_revision): changes others made (the person in the browser, the command line, rate refreshes), what all of it did to totals, debt payoff, goal dates and insights, and this month's plan-versus-actual flags. Call it at the start of a conversation about a budget you have worked on before.",
		func(ctx context.Context, in WhatsNewIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			return h.whatsNew(ctx, id, in.Since)
		})
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name: "whats-new", URITemplate: "whats-new://{id}", MIMEType: "application/json",
		Description: "What changed in a budget since the assistant's last change, and this month's flags (same as the whats_new tool).",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		id := strings.TrimPrefix(req.Params.URI, "whats-new://")
		out, err := h.whatsNew(store.WithChange(ctx, store.Change{Source: "mcp"}), id, 0)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "application/json", Text: string(raw)}}}, nil
	})
}

// prompt is one reusable workflow, offered to the user as a slash command.
type prompt struct {
	name, title, description string
	args                     []*mcp.PromptArgument
	text                     func(a map[string]string) string
}

func arg(name, desc string, required bool) *mcp.PromptArgument {
	return &mcp.PromptArgument{Name: name, Description: desc, Required: required}
}

var (
	budgetArg   = arg("budget", "budget id; omit for the default", false)
	languageArg = arg("language", "language to answer in; default: the one the user writes in", false)
)

func lang(a map[string]string) string {
	if l := strings.TrimSpace(a["language"]); l != "" {
		return "Answer in " + l + "."
	}
	return "Answer in the language the user writes in."
}

func which(a map[string]string) string {
	if b := strings.TrimSpace(a["budget"]); b != "" {
		return fmt.Sprintf("the budget %q", b)
	}
	return "the budget (the default one, or ask which if there are several: list_budgets)"
}

var prompts = []prompt{
	{
		name: "onboard", title: "Set up a budget by talking",
		description: "Build a new budget from a short interview instead of filling in forms.",
		args:        []*mcp.PromptArgument{arg("id", "id for the new budget, e.g. home", false), languageArg},
		text: func(a map[string]string) string {
			return `Help me set up a budget in bbbudget by interviewing me. ` + lang(a) + `
Ask a few questions at a time, in this order, and keep it conversational:
1. What currency I think in (the base currency), and which other currencies I get paid or pay in (crypto counts).
2. Income: each source, amount, currency, how often.
3. Fixed costs: rent or mortgage, utilities, school, insurance, subscriptions; amount, currency, how often, and when they start or end.
4. Variable spending I want to plan for (food, transport, fun), as monthly amounts.
5. Debts: balance, interest rate (APR), minimum payment; and savings I already have.
6. Goals: what I am saving for and how much.
Don't invent numbers; if I don't know one, suggest a placeholder and say so.
Then create the budget` + idClause(a) + ` from the blank template (create_budget), add everything in one apply_ops call (categories first, then items, debts, goals; schedules with "starts"/"ends" as YYYY-MM), and set currencies with set_currency so their rates are fetched.
Finish with a short summary of the plan from get_budget: what is left each month, when debts are paid off, when goals are reached, and the one or two insights that matter most. Mention that every change can be undone.`
		},
	},
	{
		name: "monthly_review", title: "Review this month",
		description: "Where the month stands against the plan, what changed, and one thing to do about it.",
		args:        []*mcp.PromptArgument{budgetArg, arg("month", "YYYY-MM; default this month", false), languageArg},
		text: func(a map[string]string) string {
			month := "this month"
			if m := strings.TrimSpace(a["month"]); m != "" {
				month = m
			}
			return `Review ` + month + ` for ` + which(a) + `. ` + lang(a) + `
1. Call whats_new to see what changed since we last spoke, and month_status for ` + month + `.
2. Tell me in a few sentences where the month stands: spent versus planned, which categories are over or on course to go over, and anything spent that wasn't planned.
3. Mention changes someone else made (in the browser, rate refreshes) only if they matter.
4. Suggest at most two concrete adjustments, quantified with what_if (never apply them without asking).
Keep it short. Numbers come from the tool results, never estimated.`
		},
	},
	{
		name: "can_i_afford", title: "Can I afford it?",
		description: "Check a purchase against the plan before making it.",
		args: []*mcp.PromptArgument{arg("item", "what you want to buy", true), arg("amount", "price, e.g. 1200 or 1200 USD", true),
			arg("when", "YYYY-MM; default this month", false), budgetArg, languageArg},
		text: func(a map[string]string) string {
			when := "this month"
			if w := strings.TrimSpace(a["when"]); w != "" {
				when = w
			}
			return fmt.Sprintf(`Can I afford %s for %s in %s? Use %s. %s
Model it with what_if as a one-off item (schedule {"starts": the month, "every": 0}) in a fitting category; don't save anything.
Answer with yes, no, or "yes, but" first, then why, from the effect: what that month is left with, whether savings go negative, which goal dates or debt payoffs move and by how much.
If it doesn't fit, say what would make it fit (a later month, spreading it, a cut elsewhere), quantified with one more what_if.`,
				strings.TrimSpace(a["item"]), strings.TrimSpace(a["amount"]), when, which(a), lang(a))
		},
	},
	{
		name: "debt_plan", title: "Plan paying off debt",
		description: "Compare payoff strategies and how much extra to put towards debt.",
		args:        []*mcp.PromptArgument{budgetArg, languageArg},
		text: func(a map[string]string) string {
			return `Help me plan paying off the debts in ` + which(a) + `. ` + lang(a) + `
1. get_budget (view "debts") to see balances, rates, minimums, and payoff dates as things stand.
2. Save scenarios with save_scenario: avalanche versus snowball (debt_strategy), and two or three levels of extra_to_debt_pct that the monthly leftover can actually carry. Then compare_scenarios.
3. Show me a small table: payoff date per debt, total interest, savings at the end. Point out which minimum payments don't even cover the interest, if any.
4. Recommend one, in a sentence, and offer to adopt it (adopt_scenario) only if I agree.`
		},
	},
	{
		name: "import_statement", title: "Import a bank statement",
		description: "Turn a pasted bank or card statement into logged transactions.",
		args:        []*mcp.PromptArgument{arg("statement", "the statement text (CSV or copied rows)", false), budgetArg, languageArg},
		text: func(a map[string]string) string {
			st := strings.TrimSpace(a["statement"])
			if st == "" {
				st = "(ask me to paste it)"
			}
			return `Log my bank statement into ` + which(a) + `. ` + lang(a) + `
1. get_budget (view "items") for the categories and items.
2. Read every row: date (YYYY-MM-DD), amount, currency, description. Skip transfers between my own accounts and card repayments, and tell me you did.
3. Pick a category for each row from the budget's categories (and the planned item it pays, when obvious). If a row is ambiguous, ask me about those rows together before logging anything; don't guess.
4. Give each row an external_id: the bank's own id if there is one, else date|amount|description. Then log every row in one log_transactions call.
5. Report what was logged, what was skipped as already imported, and the month's flags. Say that the whole import can be taken back with delete_transactions and its batch id.

Statement:
` + st
		},
	},
}

func idClause(a map[string]string) string {
	if id := strings.TrimSpace(a["id"]); id != "" {
		return fmt.Sprintf(" with id %q", id)
	}
	return ""
}

func registerPrompts(s *mcp.Server) {
	for _, p := range prompts {
		p := p
		s.AddPrompt(&mcp.Prompt{Name: p.name, Title: p.title, Description: p.description, Arguments: p.args},
			func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				a := req.Params.Arguments
				for _, want := range p.args {
					if want.Required && strings.TrimSpace(a[want.Name]) == "" {
						return nil, fmt.Errorf("%s needs %q", p.name, want.Name)
					}
				}
				return &mcp.GetPromptResult{Description: p.description, Messages: []*mcp.PromptMessage{
					{Role: "user", Content: &mcp.TextContent{Text: p.text(a)}},
				}}, nil
			})
	}
}
