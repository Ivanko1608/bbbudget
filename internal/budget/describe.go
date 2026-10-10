package budget

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"

	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
)

// maxChangeParts caps how many changes DescribeChange spells out.
const maxChangeParts = 6

// DescribeChange summarises the difference between two versions of a budget
// in one line of English, for the change history: "Rent: amount 1150 →
// 1200; added item Gym". ChangeMsg is the same summary as a message, for
// rendering in the reader's language.
func DescribeChange(before, after Budget) string { return english().Render(ChangeMsg(before, after)) }

// ChangeMsg summarises a change as a message. Things are matched by id
// (currencies by code), so a rename reads as a change, not a removal plus an
// addition. Equal documents give "no changes".
func ChangeMsg(before, after Budget) i18n.Msg {
	parts := diffJSONFields(settingsOf(before), settingsOf(after))
	parts = append(parts, diffList("currency", before.Currencies, after.Currencies, currencyKey, "rate_date", "rate_from")...)
	parts = append(parts, diffList("category", before.Categories, after.Categories, categoryKey)...)
	parts = append(parts, diffList("item", before.Items, after.Items, itemKey)...)
	parts = append(parts, diffList("debt", before.Debts, after.Debts, debtKey)...)
	parts = append(parts, diffList("pot", before.Pots, after.Pots, potKey)...)
	parts = append(parts, diffList("withdrawal", before.Withdrawals, after.Withdrawals, withdrawalKey)...)
	parts = append(parts, diffList("scenario", before.Scenarios, after.Scenarios, scenarioKey)...)
	if len(parts) == 0 {
		return msg("change.none", nil)
	}
	if len(parts) > maxChangeParts {
		n := len(parts) - maxChangeParts
		parts = append(parts[:maxChangeParts], msg("change.more", args{"count": i18n.Int(int64(n))}))
	}
	return msg("change.list", args{"items": i18n.JoinedList(parts, "; ")})
}

func currencyKey(c CurrencyRate) (string, string) { return c.Code, c.Code }
func categoryKey(c Category) (string, string)     { return c.ID, c.Label }

// budgetSettings is the part of a budget that isn't a list. A test checks
// it covers every such field of Budget, so a new setting can't be missed.
type budgetSettings struct {
	Name           string        `json:"name"`
	Description    string        `json:"description,omitempty"`
	BaseCurrency   string        `json:"base_currency"`
	StartMonth     string        `json:"start_month"`
	Months         int           `json:"months"`
	OverdraftStart money.Decimal `json:"overdraft_start,omitzero"`
	ExtraToDebtPct money.Decimal `json:"extra_to_debt_pct"`
	DebtStrategy   string        `json:"debt_strategy"`
}

func settingsOf(b Budget) budgetSettings {
	return budgetSettings{
		Name:           b.Name,
		Description:    b.Description,
		BaseCurrency:   b.BaseCurrency,
		StartMonth:     b.StartMonth,
		Months:         b.Months,
		OverdraftStart: b.OverdraftStart,
		ExtraToDebtPct: b.ExtraToDebtPct,
		DebtStrategy:   b.DebtStrategy,
	}
}

// diffList reports what was added to, removed from and changed in a list.
// key gives each element's id (how versions are matched) and the name to
// show; ignore lists fields that aren't worth reporting.
func diffList[T any](kind string, before, after []T, key func(T) (id, name string), ignore ...string) []i18n.Msg {
	old := map[string]T{}
	for _, el := range before {
		id, _ := key(el)
		old[id] = el
	}
	thing := i18n.Nested(msg("thing."+kind, nil))
	kept := map[string]bool{}
	var out []i18n.Msg
	for _, el := range after {
		id, name := key(el)
		kept[id] = true
		prev, ok := old[id]
		if !ok {
			out = append(out, msg("change.added", args{"kind": thing, "name": i18n.Text(name)}))
			continue
		}
		_, prevName := key(prev) // a rename is described under the old name
		if fields := diffJSONFields(prev, el, ignore...); len(fields) > 0 {
			out = append(out, msg("change.fields", args{"name": i18n.Text(prevName), "fields": i18n.JoinedList(fields, ", ")}))
		}
	}
	for _, el := range before {
		if id, name := key(el); !kept[id] {
			out = append(out, msg("change.removed", args{"kind": thing, "name": i18n.Text(name)}))
		}
	}
	return out
}

// diffJSONFields compares two values field by field through their JSON
// form, so a field added to a type is covered without changing this code.
// Each change reads "field old → new".
func diffJSONFields(before, after any, ignore ...string) []i18n.Msg {
	a, errA := jsonFields(before)
	b, errB := jsonFields(after)
	if errA != nil || errB != nil {
		return []i18n.Msg{msg("change.changed", nil)} // can't happen for budget types
	}
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var changes []i18n.Msg
	for _, k := range keys {
		if slices.Contains(ignore, k) || bytes.Equal(a[k], b[k]) {
			continue
		}
		changes = append(changes, msg("change.field", args{"field": i18n.Text(k), "old": show(a[k]), "new": show(b[k])}))
	}
	return changes
}

// jsonFields is v's JSON object as field name → raw value. Empty values are
// left out, so "" or [] and a missing field read as the same thing.
func jsonFields(v any) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for k, v := range fields {
		if isEmptyJSON(v) {
			delete(fields, k)
		}
	}
	return fields, nil
}

func isEmptyJSON(v json.RawMessage) bool {
	switch string(v) {
	case "null", `""`, "[]", "{}":
		return true
	}
	return false
}

func show(raw json.RawMessage) i18n.Arg {
	if raw == nil {
		return i18n.Nested(msg("change.empty", nil))
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return i18n.Text(s)
	}
	return i18n.Text(string(raw))
}
