package budget

import (
	"fmt"
	"strconv"
	"strings"

	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
)

// Action is what happened to a part of a budget between two versions.
type Action string

const (
	Added   Action = "added"
	Removed Action = "removed"
	Changed Action = "changed"
)

// Part is which part of a budget a difference is in.
type Part string

const (
	PartSettings   Part = "settings"
	PartCurrency   Part = "currency"
	PartCategory   Part = "category"
	PartItem       Part = "item"
	PartDebt       Part = "debt"
	PartPot        Part = "pot"
	PartWithdrawal Part = "withdrawal"
	PartScenario   Part = "scenario"
)

// Difference is one way two versions of a budget differ: something added
// or removed, or one field of it changed. Name is its name before any
// rename, and empty for the settings. Old and New are empty when unset.
type Difference struct {
	Action Action `json:"action"`
	Part   Part   `json:"part"`
	Name   string `json:"name,omitempty"`
	Field  string `json:"field,omitempty"`
	Old    string `json:"old,omitempty"`
	New    string `json:"new,omitempty"`
}

// Differences lists what changed between two versions of a budget. Things
// are matched by id (currencies by code), so a rename is a changed name,
// not a removal plus an addition.
func Differences(before, after Budget) []Difference {
	diffs := diffFields(PartSettings, "", settingsFields(before), settingsFields(after))
	diffs = append(diffs, currencyList.diff(before.Currencies, after.Currencies)...)
	diffs = append(diffs, categoryList.diff(before.Categories, after.Categories)...)
	diffs = append(diffs, itemList.diff(before.Items, after.Items)...)
	diffs = append(diffs, debtList.diff(before.Debts, after.Debts)...)
	diffs = append(diffs, potList.diff(before.Pots, after.Pots)...)
	diffs = append(diffs, withdrawalList.diff(before.Withdrawals, after.Withdrawals)...)
	diffs = append(diffs, scenarioList.diff(before.Scenarios, after.Scenarios)...)
	return diffs
}

// list knows how to compare one kind of list in a budget.
type list[T any] struct {
	part   Part
	key    func(T) (id, name string)
	fields func(T) []field
}

var (
	currencyList   = list[CurrencyRate]{PartCurrency, currencyKey, currencyFields}
	categoryList   = list[Category]{PartCategory, categoryKey, categoryFields}
	itemList       = list[Item]{PartItem, itemKey, itemFields}
	debtList       = list[Debt]{PartDebt, debtKey, debtFields}
	potList        = list[Pot]{PartPot, potKey, potFields}
	withdrawalList = list[Withdrawal]{PartWithdrawal, withdrawalKey, withdrawalFields}
	scenarioList   = list[Scenario]{PartScenario, scenarioKey, scenarioFields}
)

// diff reports what was added and changed, in the order of after, then what
// was removed.
func (l list[T]) diff(before, after []T) []Difference {
	old := map[string]T{}
	for _, b := range before {
		id, _ := l.key(b)
		old[id] = b
	}
	var diffs []Difference
	kept := map[string]bool{}
	for _, a := range after {
		id, name := l.key(a)
		kept[id] = true
		previous, existed := old[id]
		if !existed {
			diffs = append(diffs, Difference{Action: Added, Part: l.part, Name: name})
			continue
		}
		_, oldName := l.key(previous)
		diffs = append(diffs, diffFields(l.part, oldName, l.fields(previous), l.fields(a))...)
	}
	for _, b := range before {
		if id, name := l.key(b); !kept[id] {
			diffs = append(diffs, Difference{Action: Removed, Part: l.part, Name: name})
		}
	}
	return diffs
}

// field is one named value of a part of a budget, as the history shows it.
type field struct{ name, value string }

// diffFields compares two versions of the same thing. Both lists come from
// the same fields function, so they have the same fields in the same order.
func diffFields(part Part, name string, before, after []field) []Difference {
	var diffs []Difference
	for i := range after {
		if before[i].value == after[i].value {
			continue
		}
		diff := Difference{
			Action: Changed,
			Part:   part,
			Name:   name,
			Field:  after[i].name,
			Old:    before[i].value,
			New:    after[i].value,
		}
		diffs = append(diffs, diff)
	}
	return diffs
}

func settingsFields(b Budget) []field {
	return []field{
		{"name", b.Name},
		{"description", b.Description},
		{"base_currency", b.BaseCurrency},
		{"start_month", b.StartMonth},
		{"months", strconv.Itoa(b.Months)},
		{"overdraft_start", optionalDecimal(b.OverdraftStart)},
		{"extra_to_debt_pct", b.ExtraToDebtPct.String()},
		{"debt_strategy", b.DebtStrategy},
	}
}

// currencyFields leaves out rate_date and rate_from: they change with every
// refresh, and the rate itself is already listed.
func currencyFields(c CurrencyRate) []field {
	return []field{
		{"code", c.Code},
		{"per_base", c.PerBase.String()},
		{"transfer_fee_pct", c.TransferFeePct.String()},
		{"cash_out_fee_pct", optionalDecimal(c.CashOutFeePct)},
		{"source", string(c.Source)},
	}
}

func categoryFields(c Category) []field {
	return []field{
		{"id", c.ID},
		{"label", c.Label},
		{"kind", c.Kind},
		{"color", c.Color},
	}
}

func itemFields(i Item) []field {
	return []field{
		{"id", i.ID},
		{"name", i.Name},
		{"amount", i.Amount.String()},
		{"currency", i.Currency},
		{"category", i.Category},
		{"schedule", scheduleValue(i.Schedule)},
		{"pot", i.Pot},
	}
}

func debtFields(d Debt) []field {
	return []field{
		{"id", d.ID},
		{"name", d.Name},
		{"currency", d.Currency},
		{"balance", d.Balance.String()},
		{"apr_pct", d.APRPct.String()},
		{"min_payment", d.MinPayment.String()},
		{"payoff_month", optionalInt(d.PayoffMonth)},
		{"payoff_from", d.PayoffFrom},
	}
}

func potFields(p Pot) []field {
	return []field{
		{"id", p.ID},
		{"name", p.Name},
		{"kind", p.Kind},
		{"balance", p.Balance.String()},
		{"target", optionalDecimal(p.Target)},
		{"monthly", optionalDecimal(p.Monthly)},
	}
}

func withdrawalFields(w Withdrawal) []field {
	return []field{
		{"id", w.ID},
		{"name", w.Name},
		{"pot", w.Pot},
		{"amount", w.Amount.String()},
		{"schedule", scheduleValue(w.Schedule)},
	}
}

func scenarioFields(s Scenario) []field {
	return []field{
		{"id", s.ID},
		{"name", s.Name},
		{"description", s.Description},
		{"ops", opsValue(s.Ops)},
	}
}

func currencyKey(c CurrencyRate) (id, name string) { return c.Code, c.Code }
func categoryKey(c Category) (id, name string)     { return c.ID, c.Label }

// optionalDecimal is a decimal field that counts as unset when zero.
func optionalDecimal(d money.Decimal) string {
	if d.IsZero() {
		return ""
	}
	return d.String()
}

func optionalInt(n *int) string {
	if n == nil {
		return ""
	}
	return strconv.Itoa(*n)
}

func scheduleValue(s Schedule) string {
	value := fmt.Sprintf("start %d, every %d", s.Start, s.Every)
	if s.End != nil {
		value += fmt.Sprintf(", end %d", *s.End)
	}
	return value
}

func opsValue(ops []Op) string {
	parts := make([]string, len(ops))
	for i, op := range ops {
		parts[i] = strings.TrimSpace(fmt.Sprintf("%s %s %s", op.Op, op.Target, op.Value))
	}
	return strings.Join(parts, "; ")
}

// maxDescribed caps how many things Describe spells out.
const maxDescribed = 6

// Describe summarises differences in one line of English, for the change
// history: "Rent: amount 1150 → 1200; added item Gym".
func Describe(diffs []Difference) string { return english().Render(DifferencesMsg(diffs)) }

// DifferencesMsg is the same summary as a message, for rendering in the
// reader's language. It is "no changes" when there are none.
func DifferencesMsg(diffs []Difference) i18n.Msg {
	subjects := groupBySubject(diffs)
	if len(subjects) == 0 {
		return msg("change.none", nil)
	}
	shown := subjects[:min(len(subjects), maxDescribed)]
	parts := make([]i18n.Msg, len(shown))
	for i, s := range shown {
		parts[i] = s.message()
	}
	if hidden := len(subjects) - len(shown); hidden > 0 {
		parts = append(parts, msg("change.more", args{"count": i18n.Int(int64(hidden))}))
	}
	return msg("change.list", args{"items": i18n.JoinedList(parts, "; ")})
}

// subject is one thing that differs, with all of its changed fields.
type subject struct {
	action Action
	part   Part
	name   string
	fields []Difference
}

func groupBySubject(diffs []Difference) []subject {
	var subjects []subject
	for _, d := range diffs {
		last := len(subjects) - 1
		if last >= 0 && subjects[last].takes(d) {
			subjects[last].fields = append(subjects[last].fields, d)
			continue
		}
		subjects = append(subjects, newSubject(d))
	}
	return subjects
}

func newSubject(d Difference) subject {
	return subject{action: d.Action, part: d.Part, name: d.Name, fields: []Difference{d}}
}

// takes reports whether d is another changed field of the same thing.
func (s subject) takes(d Difference) bool {
	bothChanged := s.action == Changed && d.Action == Changed
	return bothChanged && s.part == d.Part && s.name == d.Name
}

func (s subject) message() i18n.Msg {
	if s.action != Changed {
		part := i18n.Nested(msg("part."+string(s.part), nil))
		return msg("change."+string(s.action), args{"part": part, "name": i18n.Text(s.name)})
	}
	fields := make([]i18n.Msg, len(s.fields))
	for i, d := range s.fields {
		fields[i] = fieldMsg(d)
	}
	changes := i18n.JoinedList(fields, ", ")
	if s.part == PartSettings {
		return msg("change.settings", args{"fields": changes})
	}
	return msg("change.fields", args{"name": i18n.Text(s.name), "fields": changes})
}

func fieldMsg(d Difference) i18n.Msg {
	field := i18n.Text(d.Field)
	if key := "field." + d.Field; english().Has(key) {
		field = i18n.Nested(msg(key, nil))
	}
	return msg("change.field", args{"field": field, "old": orNone(d.Old), "new": orNone(d.New)})
}

func orNone(value string) i18n.Arg {
	if value == "" {
		return i18n.Nested(msg("change.empty", nil))
	}
	return i18n.Text(value)
}
