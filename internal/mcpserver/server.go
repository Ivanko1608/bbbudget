// Package mcpserver exposes budgets to AI assistants over the Model Context
// Protocol. Every tool is a thin wrapper over app.App.
package mcpserver

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

const instructions = `bbbudget: multi-currency household budgets with exact money.

Model: a budget has a fiat base currency, other currencies including crypto (BTC, ETH, USDT, ...)
with a per_base rate, transfer_fee_pct lost on every payment in that currency and cash_out_fee_pct
lost when income in it is converted to base, categories (income|expense), items (amount in any
configured currency, with a schedule), debts (balance, APR, minimum payment; a share of each
month's leftover, extra_to_debt_pct, goes to debts by strategy avalanche|snowball|listed), savings
pots and planned withdrawals.

Savings: savings live in pots {name, kind general|sinking|emergency, balance, target?, monthly?}.
Savings are balances, never income. Each month's leftover first repays any overdraft, then pays
fixed monthly contributions, then the debt share, then fills pots that have a target (in order),
then goes to the first general pot. A month that falls short is NOT covered from savings
automatically: it is owed (overdraft) and listed under shortfalls with the pots that could cover
it. To use savings, add a withdrawal {name, pot, amount, schedule} (base currency) only when the
user agrees. An item can also have a pot: income on it (a sale, a bonus) goes straight into that
pot, not into the month's leftover, so no debt share applies; a cost on it is paid from that pot
as far as it holds. Use this for windfalls the user wants saved and for costs that sinking funds
were saved for. A debt can be paid off in full in one month: payoff_month (0-based) and
payoff_from (a pot, or "" for that month's own money). The whole balance, that month's interest
included, is paid after income routed to pots has landed, so a sale routed to a pot can pay off a
debt in the same month; a pot that holds less pays what it can and the rest stays owed. set_debt
with payoff_month -1 removes it. Offer general savings first, then sinking funds (say which
planned cost it delays), and the emergency fund only as a last resort.

Amounts are exact decimal STRINGS ("1150", "12.50"); never send floats. Months are "YYYY-MM".

Language: text fields (insights, schedules, month labels) are in the user's configured language
(app setting "locale"). Always answer in the language the user writes in. Each insight also has a
key and typed args with the exact amounts and months; quote numbers from args, not from re-reading
the text.

Rounding is pessimistic: costs round up, income/receipts round down, to the minor unit.

Rates: a currency's source is "manual" (typed) or a provider (auto = official ECB rate, falling
back to currency-api for crypto and other currencies; ecb; currency-api; coingecko). refresh_rates
fetches current rates; set_currency fetches automatically when adding a currency without per_base.
Never invent a rate: fetch it.

Workflow: call list_budgets, then get_budget before answering about numbers. Writes and what_if
return what changed and its effect (totals, first month, debt payoffs, goal dates, insights that
appeared, went away or changed), not the whole budget: answer from that, and call get_budget (with
a view) when you need more. Use what_if for hypotheticals (nothing is saved). Only use mutating
tools when the user clearly asks for a change, then state what changed and that it can be undone.

Actual money: the budget is a plan; log_transactions records what was really spent or received ("I
spent 40 on groceries", or a pasted bank statement: one call with every row, each with an
external_id so a re-import is skipped). month_status compares the month with the plan. Choose
categories from the budget; if one is unclear, ask. Correct mistakes with update_transaction or
delete_transactions (a whole batch for a bad import).

When you come back to a budget you have worked on, start with whats_new: it says what others
changed since your last edit and what that did.

Every write is recorded in the budget's history with who made it (mcp = you, web = the person in
the browser, cli, auto-refresh). history lists recent changes; undo reverts one. If the person
says "undo that", undo your own last change; if someone else changed the budget since, undo
refuses and lists their changes, so ask before forcing it. Use save_scenario + compare_scenarios
to explore alternatives side by side. Items/debts/pots/withdrawals can be referenced by id, exact
name, or an unambiguous part of a name.`

// Options configures the server.
type Options struct {
	// DefaultBudget is used when a tool call omits "budget".
	DefaultBudget string
	Version       string
	// Source is who the history names for every write; default
	// store.SourceMCP, an outside MCP client.
	Source store.Source
}

// New builds an MCP server over the app.
func New(a *app.App, opts Options) *mcp.Server {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	implementation := &mcp.Implementation{
		Name:    "bbbudget",
		Title:   "Budget planner",
		Version: opts.Version,
	}
	s := mcp.NewServer(implementation, &mcp.ServerOptions{Instructions: instructions})
	source := cmp.Or(opts.Source, store.SourceMCP)
	h := &handlers{
		app:           a,
		defaultBudget: opts.DefaultBudget,
		origin:        store.Origin{Source: source},
	}
	h.register(s)
	return s
}

type handlers struct {
	app           *app.App
	defaultBudget string
	origin        store.Origin
}

var schemaOpts = &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
	reflect.TypeFor[money.Decimal](): {
		Type:        "string",
		Pattern:     `^[+-]?[0-9]+(\.[0-9]+)?$`,
		Description: "exact decimal as a string",
	},
	reflect.TypeFor[json.RawMessage](): {Type: "object"},
}}

// toolFunc handles a tool call and returns any JSON-able value.
type toolFunc[In any] func(context.Context, In) (any, error)

// addWriteTool registers a handler whose input schema is inferred from In with the
// money-aware type mapping above.
func addWriteTool[In any](s *mcp.Server, name, desc string, fn toolFunc[In]) {
	addTool(s, &mcp.Tool{Name: name, Description: desc}, fn)
}

// addReadTool registers a tool that never changes anything.
func addReadTool[In any](s *mcp.Server, name, desc string, fn toolFunc[In]) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	addTool(s, &mcp.Tool{Name: name, Description: desc, Annotations: readOnly}, fn)
}

func addTool[In any](s *mcp.Server, t *mcp.Tool, fn toolFunc[In]) {
	schema, err := jsonschema.For[In](schemaOpts)
	if err != nil {
		panic(fmt.Sprintf("tool %s: %v", t.Name, err))
	}
	t.InputSchema = schema
	mcp.AddTool(s, t, toMCPHandler(fn))
}

func toMCPHandler[In any](fn toolFunc[In]) mcp.ToolHandlerFor[In, any] {
	return func(
		ctx context.Context, _ *mcp.CallToolRequest, in In,
	) (*mcp.CallToolResult, any, error) {
		out, err := fn(ctx, in)
		if err != nil {
			return nil, nil, err // reported to the model as a tool error
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return nil, nil, err
		}
		text := &mcp.TextContent{Text: string(raw)}
		return &mcp.CallToolResult{Content: []mcp.Content{text}}, nil, nil
	}
}

func (h *handlers) id(ref string) (string, error) {
	if ref = strings.TrimSpace(ref); ref != "" {
		return ref, nil
	}
	if h.defaultBudget != "" {
		return h.defaultBudget, nil
	}
	return "", fmt.Errorf(`"budget" is required (no default budget configured); call list_budgets`)
}

type Ref struct {
	Budget string `json:"budget,omitempty" jsonschema:"budget id; omit to use the default budget"`
}

type CreateIn struct {
	ID       string `json:"id" jsonschema:"new budget id: lowercase letters, digits, '-'"`
	Name     string `json:"name,omitempty" jsonschema:"display name"`
	Template string `json:"template,omitempty" jsonschema:"template name from list_templates; default blank"`
}

type ScheduleIn struct {
	Every  *int    `json:"every,omitempty" jsonschema:"period in months: 1 monthly (default), 3 quarterly, 12 yearly, 0 one-off"`
	Starts string  `json:"starts,omitempty" jsonschema:"first month YYYY-MM; default is the budget's start month (add_item) or unchanged (update_item)"`
	Ends   *string `json:"ends,omitempty" jsonschema:"last month YYYY-MM (inclusive). In update_item omit to keep the current end, or send an empty string to remove it"`
}

type AddItemIn struct {
	WriteRef
	Name     string        `json:"name"`
	Amount   money.Decimal `json:"amount"`
	Currency string        `json:"currency,omitempty" jsonschema:"defaults to the base currency"`
	Category string        `json:"category" jsonschema:"category id; must exist (see get_budget)"`
	ScheduleIn
}

type UpdateItemIn struct {
	WriteRef
	Item     string         `json:"item" jsonschema:"item id or name"`
	Name     *string        `json:"name,omitempty"`
	Amount   *money.Decimal `json:"amount,omitempty"`
	Currency *string        `json:"currency,omitempty"`
	Category *string        `json:"category,omitempty"`
	ScheduleIn
}

type ItemRef struct {
	WriteRef
	Item string `json:"item" jsonschema:"item id or name"`
}

type CurrencyIn struct {
	WriteRef
	Code           string             `json:"code" jsonschema:"ISO code or crypto ticker, e.g. USD, BTC, USDT"`
	PerBase        *money.Decimal     `json:"per_base,omitempty" jsonschema:"units of this currency per 1 base unit; setting it makes the rate manual. Omit to fetch the current rate"`
	Source         *budget.RateSource `json:"source,omitempty" jsonschema:"where the rate comes from: manual, auto, ecb, currency-api or coingecko"`
	TransferFeePct *money.Decimal     `json:"transfer_fee_pct,omitempty" jsonschema:"percent lost on every payment in this currency"`
	CashOutFeePct  *money.Decimal     `json:"cash_out_fee_pct,omitempty" jsonschema:"percent lost when income in this currency is converted to base (exchange fee + spread)"`
}

type RatesIn struct {
	WriteRef
	Codes []string `json:"codes,omitempty" jsonschema:"currencies to refresh; omit for every currency whose source isn't manual. Naming a manual one switches it to auto"`
}

type SettingsIn struct {
	WriteRef
	budget.SettingsPatch
}

type DebtIn struct {
	WriteRef
	Debt        string         `json:"debt" jsonschema:"debt id or name; created if no debt matches"`
	Currency    *string        `json:"currency,omitempty"`
	Balance     *money.Decimal `json:"balance,omitempty"`
	APRPct      *money.Decimal `json:"apr_pct,omitempty" jsonschema:"yearly interest in percent"`
	MinPayment  *money.Decimal `json:"min_payment,omitempty" jsonschema:"monthly minimum payment in the debt's currency"`
	PayoffMonth *int           `json:"payoff_month,omitempty" jsonschema:"pay the whole balance off in this month (0-based from start_month); -1 removes the payoff"`
	PayoffFrom  *string        `json:"payoff_from,omitempty" jsonschema:"pot (id or name) that pays it off; empty string pays it from that month's own money"`
}

type OpsIn struct {
	WriteRef
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
	WriteRef
	Revision int64 `json:"revision,omitempty" jsonschema:"revision of the change to undo (see history); omit for the latest change"`
	Force    bool  `json:"force,omitempty" jsonschema:"also undo every change made after it. Only with the person's explicit go-ahead"`
	Seen     int64 `json:"seen,omitempty" jsonschema:"with force: the newest revision in the refusal the person agreed to. If more changes landed since, undo refuses again and lists them"`
}

type ScenarioRef struct {
	Ref
	Scenario string `json:"scenario" jsonschema:"scenario id or name"`
}

type AdoptIn struct {
	WriteRef
	Scenario string `json:"scenario" jsonschema:"scenario id or name"`
}

const opsValueFieldsHelp = "\nValue fields: " +
	"Item{name, amount, currency, category, schedule, pot}, " +
	"Debt{name, currency, balance, apr_pct, min_payment, payoff_month, payoff_from}, " +
	"Pot{name, kind, balance, target, monthly}, " +
	"Withdrawal{name, pot, amount, schedule}, " +
	"Category{id, label, kind}, " +
	"SettingsPatch{name, description, start_month, months, overdraft_start, " +
	"extra_to_debt_pct, debt_strategy}, " +
	"CurrencyPatch{per_base, transfer_fee_pct, cash_out_fee_pct, source}. " +
	"Amounts are decimal strings."

const opsScheduleHelp = "\nItem schedules: give calendar months, " +
	`{"starts":"YYYY-MM","ends":"YYYY-MM","every":N} ` +
	"(every: 1 monthly, the default; 3 quarterly; 12 yearly; 0 one-off in starts). " +
	`In set_item only the given schedule fields change, and "ends":"" removes the end. ` +
	"The stored form uses 0-based month indexes {start, every, end}; " +
	"don't mix the two in one schedule. " +
	"Withdrawal schedules and payoff_month still take 0-based month indexes from start_month."

func opsHelp() string {
	var b strings.Builder
	for _, kind := range slices.Sorted(maps.Keys(budget.OpKinds)) {
		fmt.Fprintf(&b, "\n- %s: %s", kind, budget.OpKinds[kind])
	}
	return b.String() + opsValueFieldsHelp + opsScheduleHelp
}

type toolKind int

const (
	readTool toolKind = iota
	writeTool
	writeToolWithEffect
)

const effectDescriptionSuffix = " Returns what changed and its effect; " +
	"set detail to full for the whole budget."

type toolDefinition func(s *mcp.Server)

func newTool[In any](name, description string, kind toolKind, fn toolFunc[In]) toolDefinition {
	return func(s *mcp.Server) {
		switch kind {
		case readTool:
			addReadTool(s, name, description, fn)
		case writeToolWithEffect:
			addWriteTool(s, name, description+effectDescriptionSuffix, fn)
		case writeTool:
			addWriteTool(s, name, description, fn)
		}
	}
}

const (
	listBudgetsDescription   = "List stored budgets."
	listTemplatesDescription = "List templates a new budget can start from."
	createBudgetDescription  = "Create a budget from a template (see list_templates)."
	getBudgetDescription     = "The budget: settings, every item with its base-currency cost, " +
		"debts, savings pots, withdrawals, shortfalls, goals, scenarios, " +
		"the month-by-month projection, totals and plain-language insights. " +
		"Call this first. The default overview shows the first months and the last; " +
		"use view and from/to for more, or for less."
	addItemDescription    = "Add an income or cost item."
	updateItemDescription = "Change fields of one item; " +
		"omitted fields (including schedule fields) stay as they are."
	removeItemDescription  = "Delete one item."
	setCurrencyDescription = "Add a currency (fiat or crypto) or change its rate, " +
		"rate source, transfer loss or cash-out loss. " +
		"A new currency without per_base gets the current market rate fetched (source auto)."
	refreshRatesDescription = "Fetch current exchange rates (official ECB rates, crypto prices) " +
		"and save them. Returns old and new rate per currency, plus the updated budget."
	updateSettingsDescription = "Change budget settings (name, start_month, months, " +
		"overdraft_start, extra_to_debt_pct, debt_strategy). " +
		"Savings balances live in pots: change them with apply_ops set_pot."
	setDebtDescription = "Create or update a debt (credit card, loan). " +
		"Creating needs currency (default base), balance, apr_pct and min_payment. " +
		"payoff_month and payoff_from pay it off in full in one month, " +
		"e.g. from a pot a sale goes into."
	applyOpsDescription = "Apply any edits atomically (all or nothing) and save. " +
		"Covers everything the specific tools do plus categories, pots, withdrawals " +
		"and removals. Op kinds:"
	whatIfDescription = "Preview edits WITHOUT saving: returns the projection and insights " +
		"as if the ops were applied. Same op format as apply_ops."
	saveScenarioDescription = "Save a named set of ops as a scenario to compare later. " +
		"The real budget is not changed."
	compareScenariosDescription = "Compare the current budget against saved scenarios: " +
		"savings, goal dates, debt payoff, interest."
	adoptScenarioDescription = "Apply a saved scenario to the real budget " +
		"and remove it from the scenario list."
	deleteScenarioDescription = "Delete a saved scenario."
	historyDescription        = "Recent changes to the budget, newest first: revision, time, " +
		"who made it (mcp, assistant, web, cli, auto-refresh) and what changed."
	undoDescription = "Undo a change by restoring the version before it, " +
		"saved as a new revision (so an undo can be undone too). " +
		"Refuses if later changes exist unless force."
)

func (h *handlers) tools() []toolDefinition {
	return []toolDefinition{
		newTool("list_budgets", listBudgetsDescription, readTool, h.listBudgets),
		newTool("list_templates", listTemplatesDescription, readTool, h.listTemplates),
		newTool("create_budget", createBudgetDescription, writeTool, h.createBudget),
		newTool("get_budget", getBudgetDescription, readTool, h.getBudget),
		newTool("add_item", addItemDescription, writeToolWithEffect, h.addItem),
		newTool("update_item", updateItemDescription, writeToolWithEffect, h.updateItem),
		newTool("remove_item", removeItemDescription, writeToolWithEffect, h.removeItem),
		newTool("set_currency", setCurrencyDescription, writeToolWithEffect, h.setCurrency),
		newTool("refresh_rates", refreshRatesDescription, writeTool, h.refreshRates),
		newTool(
			"update_settings", updateSettingsDescription, writeToolWithEffect, h.updateSettings,
		),
		newTool("set_debt", setDebtDescription, writeToolWithEffect, h.setDebt),
		newTool("apply_ops", applyOpsDescription+opsHelp(), writeTool, h.applyOps),
		newTool("what_if", whatIfDescription, readTool, h.whatIf),
		newTool("save_scenario", saveScenarioDescription, writeTool, h.saveScenario),
		newTool("compare_scenarios", compareScenariosDescription, readTool, h.compareScenarios),
		newTool("adopt_scenario", adoptScenarioDescription, writeToolWithEffect, h.adoptScenario),
		newTool("delete_scenario", deleteScenarioDescription, writeTool, h.deleteScenario),
		newTool("history", historyDescription, readTool, h.listHistory),
		newTool("undo", undoDescription, writeToolWithEffect, h.undoChange),
	}
}

func (h *handlers) register(s *mcp.Server) {
	for _, registerTool := range h.tools() {
		registerTool(s)
	}
	h.registerLedger(s)
	h.registerWhatsNew(s)
	registerPrompts(s)
	h.registerBudgetResource(s)
}

func (h *handlers) registerBudgetResource(s *mcp.Server) {
	template := &mcp.ResourceTemplate{
		Name:        "budget",
		URITemplate: "budget://{id}",
		MIMEType:    "application/json",
		Description: "The raw stored budget document (exact decimal strings).",
	}
	s.AddResourceTemplate(template, h.readBudgetResource)
}

func (h *handlers) readBudgetResource(
	ctx context.Context, req *mcp.ReadResourceRequest,
) (*mcp.ReadResourceResult, error) {
	id := strings.TrimPrefix(req.Params.URI, "budget://")
	r, err := h.app.Store.Get(ctx, id)
	if err != nil {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	contents := &mcp.ResourceContents{
		URI:      req.Params.URI,
		MIMEType: "application/json",
		Text:     string(raw),
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{contents}}, nil
}

func (h *handlers) listBudgets(ctx context.Context, _ struct{}) (any, error) {
	return h.app.List(ctx)
}

func (h *handlers) listTemplates(_ context.Context, _ struct{}) (any, error) {
	return h.app.Templates.List(), nil
}

func (h *handlers) createBudget(ctx context.Context, in CreateIn) (any, error) {
	v, err := h.app.Create(ctx, h.origin, h.localizer(), in.ID, in.Name, in.Template)
	return h.summarized(v, err)
}

func (h *handlers) getBudget(ctx context.Context, in GetIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	v, err := h.app.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	v.Localize(h.localizer())
	return part(summarize(v), in)
}

func (h *handlers) addItem(ctx context.Context, in AddItemIn) (any, error) {
	input := budget.ItemInput{
		Name:     in.Name,
		Amount:   in.Amount,
		Currency: in.Currency,
		Category: in.Category,
		Schedule: in.ScheduleIn.spec(),
	}
	return h.apply(ctx, in.WriteRef, store.AnyRevision, budget.NewOp(budget.OpAddItem, "", input))
}

func (h *handlers) updateItem(ctx context.Context, in UpdateItemIn) (any, error) {
	patch := budget.ItemPatch{
		Name:     in.Name,
		Amount:   in.Amount,
		Currency: in.Currency,
		Category: in.Category,
		Schedule: in.ScheduleIn.spec(),
	}
	op := budget.NewOp(budget.OpSetItem, in.Item, patch)
	return h.apply(ctx, in.WriteRef, store.AnyRevision, op)
}

func (h *handlers) removeItem(ctx context.Context, in ItemRef) (any, error) {
	op := budget.NewOp(budget.OpRemoveItem, in.Item, nil)
	return h.apply(ctx, in.WriteRef, store.AnyRevision, op)
}

func (h *handlers) setCurrency(ctx context.Context, in CurrencyIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	patch := budget.CurrencyPatch{
		PerBase:        in.PerBase,
		Source:         in.Source,
		TransferFeePct: in.TransferFeePct,
		CashOutFeePct:  in.CashOutFeePct,
	}
	v, err := h.app.SetCurrency(ctx, h.origin, id, in.Code, patch)
	return h.written(in.WriteRef, v, err)
}

func (h *handlers) refreshRates(ctx context.Context, in RatesIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	v, updates, err := h.app.RefreshRates(ctx, h.origin, id, in.Codes)
	if err != nil {
		if len(updates) > 0 {
			return nil, fmt.Errorf("%w; per currency: %+v", err, updates)
		}
		return nil, err
	}
	result, err := h.written(in.WriteRef, v, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"rate_updates": updates, "result": result}, nil
}

func (h *handlers) updateSettings(ctx context.Context, in SettingsIn) (any, error) {
	op := budget.NewOp(budget.OpSetSettings, "", in.SettingsPatch)
	return h.apply(ctx, in.WriteRef, store.AnyRevision, op)
}

func (h *handlers) setDebt(ctx context.Context, in DebtIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	patch := budget.DebtPatch{
		Currency:    in.Currency,
		Balance:     in.Balance,
		APRPct:      in.APRPct,
		MinPayment:  in.MinPayment,
		PayoffMonth: in.PayoffMonth,
		PayoffFrom:  in.PayoffFrom,
	}
	v, err := h.app.SetDebt(ctx, h.origin, id, in.Debt, patch)
	return h.written(in.WriteRef, v, err)
}

func (h *handlers) applyOps(ctx context.Context, in OpsIn) (any, error) {
	rev := store.AnyRevision
	if in.ExpectedRevision != nil {
		rev = *in.ExpectedRevision
	}
	return h.apply(ctx, in.WriteRef, rev, in.Ops...)
}

func (h *handlers) whatIf(ctx context.Context, in OpsIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	v, err := h.app.WhatIf(ctx, id, in.Ops)
	if err != nil {
		return nil, err
	}
	return h.previewed(in.WriteRef, v)
}

func (h *handlers) saveScenario(ctx context.Context, in ScenarioIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	return h.app.SaveScenario(ctx, h.origin, id, in.Name, in.Description, in.Ops)
}

func (h *handlers) compareScenarios(ctx context.Context, in CompareIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	out, err := h.app.Compare(ctx, h.localizer(), id, in.Scenarios)
	if err != nil {
		return nil, err
	}
	return outcomeRows(out), nil
}

func (h *handlers) adoptScenario(ctx context.Context, in AdoptIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	v, err := h.app.AdoptScenario(ctx, h.origin, id, in.Scenario)
	return h.written(in.WriteRef, v, err)
}

func (h *handlers) deleteScenario(ctx context.Context, in ScenarioRef) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	if err := h.app.DeleteScenario(ctx, h.origin, id, in.Scenario); err != nil {
		return nil, err
	}
	return map[string]string{"deleted": in.Scenario}, nil
}

func (h *handlers) listHistory(ctx context.Context, in HistoryIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	if in.Limit <= 0 {
		in.Limit = store.DefaultHistoryPage
	}
	history, err := h.app.History(ctx, id, in.Limit)
	if err != nil {
		return nil, err
	}
	app.LocalizeHistory(history, h.localizer())
	return history, nil
}

func (h *handlers) undoChange(ctx context.Context, in UndoIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	req := app.UndoRequest{Revision: in.Revision, Force: in.Force, Seen: in.Seen}
	v, err := h.app.Undo(ctx, h.origin, id, req)
	return h.written(in.WriteRef, v, err)
}

func (h *handlers) apply(
	ctx context.Context, w WriteRef, rev int64, ops ...budget.Op,
) (any, error) {
	id, err := h.id(w.Budget)
	if err != nil {
		return nil, err
	}
	v, err := h.app.Apply(ctx, h.origin, id, rev, ops)
	return h.written(w, v, err)
}

// spec turns the flat schedule fields of add_item/update_item into a
// ScheduleSpec, or nil when none was given. Months are resolved inside the
// store transaction (see budget.ScheduleSpec), never against a stale read.
func (in ScheduleIn) spec() *budget.ScheduleSpec {
	if in.Every == nil && in.Starts == "" && in.Ends == nil {
		return nil
	}
	return &budget.ScheduleSpec{Every: in.Every, Starts: in.Starts, Ends: in.Ends}
}

// summarized summarizes a budget the tool call returns, in the user's language.
func (h *handlers) summarized(v *app.View, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	v.Localize(h.localizer())
	return summarize(v), nil
}

// localizer renders text in the language the app settings choose.
func (h *handlers) localizer() *i18n.Localizer { return h.app.SettingsLocalizer() }
