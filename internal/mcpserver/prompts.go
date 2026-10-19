package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// promptTemplate is a common job, offered to the user as a slash command. Its
// text is a short procedure over the tools the server already has.
type promptTemplate struct {
	name        string
	title       string
	description string
	arguments   []*mcp.PromptArgument
	text        func(promptArgs) string
}

// promptArgs are the arguments a prompt was asked for with.
type promptArgs map[string]string

func (args promptArgs) get(name string) string { return strings.TrimSpace(args[name]) }

func (args promptArgs) getOrDefault(name, fallback string) string {
	if value := args.get(name); value != "" {
		return value
	}
	return fallback
}

func requiredArgument(name, description string) *mcp.PromptArgument {
	return &mcp.PromptArgument{Name: name, Description: description, Required: true}
}

func optionalArgument(name, description string) *mcp.PromptArgument {
	return &mcp.PromptArgument{Name: name, Description: description}
}

var (
	budgetArg   = optionalArgument("budget", "budget id; omit for the default")
	languageArg = optionalArgument(
		"language", "language to answer in; default: the one the user writes in",
	)
	monthArg = optionalArgument("month", "YYYY-MM; default this month")
)

var promptTemplates = []promptTemplate{
	{
		name:        "onboard",
		title:       "Set up a budget by talking",
		description: "Build a new budget from a short interview instead of filling in forms.",
		arguments: []*mcp.PromptArgument{
			optionalArgument("id", "id for the new budget, e.g. home"),
			languageArg,
		},
		text: onboardText,
	},
	{
		name:        "monthly_review",
		title:       "Review this month",
		description: "Where the month stands against the plan, what changed, and what to do about it.",
		arguments:   []*mcp.PromptArgument{budgetArg, monthArg, languageArg},
		text:        monthlyReviewText,
	},
	{
		name:        "can_i_afford",
		title:       "Can I afford it?",
		description: "Check a purchase against the plan before making it.",
		arguments: []*mcp.PromptArgument{
			requiredArgument("item", "what you want to buy"),
			requiredArgument("amount", "price, e.g. 1200 or 1200 USD"),
			optionalArgument("when", "YYYY-MM; default this month"),
			budgetArg,
			languageArg,
		},
		text: canIAffordText,
	},
	{
		name:        "debt_plan",
		title:       "Plan paying off debt",
		description: "Compare payoff strategies and how much extra to put towards debt.",
		arguments:   []*mcp.PromptArgument{budgetArg, languageArg},
		text:        debtPlanText,
	},
	{
		name:        "import_statement",
		title:       "Import a bank statement",
		description: "Turn a pasted bank or card statement into logged transactions.",
		arguments: []*mcp.PromptArgument{
			optionalArgument("statement", "the statement text (CSV or copied rows)"),
			budgetArg,
			languageArg,
		},
		text: importStatementText,
	},
}

func languageInstruction(args promptArgs) string {
	if language := args.get("language"); language != "" {
		return "Answer in " + language + "."
	}
	return "Answer in the language the user writes in."
}

func budgetPhrase(args promptArgs) string {
	if id := args.get("budget"); id != "" {
		return fmt.Sprintf("the budget %q", id)
	}
	return "the budget (the default one, or ask which if there are several: list_budgets)"
}

func onboardText(args promptArgs) string {
	newBudget := "a new budget"
	if id := args.get("id"); id != "" {
		newBudget = fmt.Sprintf("a new budget with id %q", id)
	}
	return `Help me set up a budget in bbbudget by interviewing me. ` + languageInstruction(args) + `
Ask a few questions at a time, in this order, and keep it conversational:
1. The currency I think in (the base currency), and the other currencies I get paid or pay
   in (crypto counts).
2. Income: each source, amount, currency, how often.
3. Fixed costs: rent or mortgage, utilities, school, insurance, subscriptions; amount,
   currency, how often, and when they start or end.
4. Variable spending I want to plan for (food, transport, fun), as monthly amounts.
5. Debts: balance, interest rate (APR), minimum payment. Savings I already have.
6. What I am saving for, and how much.
Don't invent numbers; if I don't know one, suggest a placeholder and say so.
Then create_budget for ` + newBudget + ` from the blank template, and add everything in one
apply_ops call: categories first, then items, debts and savings pots, with schedules as
"starts"/"ends" in YYYY-MM. Add currencies with set_currency so their rates are fetched.
Finish with a short summary from get_budget: what is left each month, when debts are paid
off, when savings targets are reached, and the one or two insights that matter most.
Mention that every change can be undone.`
}

func monthlyReviewText(args promptArgs) string {
	month := args.getOrDefault("month", "this month")
	return `Review ` + month + ` for ` + budgetPhrase(args) + `. ` + languageInstruction(args) + `
1. Call whats_new to see what changed since we last spoke, and month_status for ` + month + `.
2. Tell me in a few sentences where the month stands: spent versus planned, which
   categories are over or on course to go over, and anything spent that wasn't planned.
3. Mention changes someone else made (in the browser, rate refreshes) only if they matter.
4. Suggest at most two concrete adjustments, quantified with what_if. Never apply them
   without asking.
Keep it short. Numbers come from the tool results, never estimated.`
}

func canIAffordText(args promptArgs) string {
	purchase := args.get("item") + " for " + args.get("amount")
	when := args.getOrDefault("when", "this month")
	return `Can I afford ` + purchase + ` in ` + when + `? Use ` + budgetPhrase(args) + `. ` +
		languageInstruction(args) + `
Model it with what_if as a one-off item (schedule {"starts": the month, "every": 0}) in a
fitting category. Don't save anything.
Answer with yes, no, or "yes, but" first, then why, from the effect: what that month is
left with, whether savings go negative, and which savings targets or debt payoffs move and
by how much.
If it doesn't fit, say what would make it fit (a later month, spreading it, a cut
elsewhere), quantified with one more what_if.`
}

func debtPlanText(args promptArgs) string {
	return `Help me plan paying off the debts in ` + budgetPhrase(args) + `. ` +
		languageInstruction(args) + `
1. get_budget with view "debts": balances, rates, minimums, and payoff dates as things stand.
2. save_scenario for avalanche versus snowball (debt_strategy), and for two or three levels
   of extra_to_debt_pct that the monthly leftover can carry. Then compare_scenarios.
3. Show me a small table: payoff date per debt, total interest, savings at the end. Point
   out any minimum payment that doesn't even cover the interest.
4. Recommend one in a sentence, and adopt_scenario only if I agree.`
}

func importStatementText(args promptArgs) string {
	statement := args.getOrDefault("statement", "(ask me to paste it)")
	return `Log my bank statement into ` + budgetPhrase(args) + `. ` + languageInstruction(args) + `
1. get_budget with view "items" for the categories and items.
2. Read every row: date (YYYY-MM-DD), amount, currency, description. Skip transfers
   between my own accounts and card repayments, and tell me you did.
3. Pick a category for each row from the budget's categories, and the planned item it
   pays when that is obvious. If rows are ambiguous, ask me about them together before
   logging anything. Don't guess.
4. Give each row an external_id: the bank's own id if there is one, else
   date|amount|description. Then log every row in one log_transactions call.
5. Report what was logged, what was skipped as already imported, and the month's flags.
   Say that the whole import can be taken back with delete_transactions and its batch.

Statement:
` + statement
}

func registerPrompts(s *mcp.Server) {
	for _, w := range promptTemplates {
		prompt := &mcp.Prompt{
			Name:        w.name,
			Title:       w.title,
			Description: w.description,
			Arguments:   w.arguments,
		}
		s.AddPrompt(prompt, w.render)
	}
}

func (t promptTemplate) render(
	ctx context.Context, req *mcp.GetPromptRequest,
) (*mcp.GetPromptResult, error) {
	args := promptArgs(req.Params.Arguments)
	if missing := t.missingArgument(args); missing != "" {
		return nil, fmt.Errorf("%s needs %q", t.name, missing)
	}
	message := &mcp.PromptMessage{Role: "user", Content: &mcp.TextContent{Text: t.text(args)}}
	return &mcp.GetPromptResult{
		Description: t.description,
		Messages:    []*mcp.PromptMessage{message},
	}, nil
}

// missingArgument is the first required argument args lacks, or "".
func (t promptTemplate) missingArgument(args promptArgs) string {
	for _, argument := range t.arguments {
		if argument.Required && args.get(argument.Name) == "" {
			return argument.Name
		}
	}
	return ""
}
