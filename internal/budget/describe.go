package budget

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"bbbudget/internal/money"
)

// maxChangeParts caps how many changes DescribeChange spells out.
const maxChangeParts = 6

// DescribeChange summarises the difference between two versions of a budget
// in one line, for the change history: "Rent: amount 1150 → 1200; added
// item Gym". Things are matched by id (currencies by code), so a rename reads
// as a change, not a removal plus an addition. It is "no changes" when the
// documents are equal.
func DescribeChange(before, after Budget) string {
	parts := diffJSONFields("", settingsOf(before), settingsOf(after))
	parts = append(parts, diffList("currency", before.Currencies, after.Currencies, currencyKey, "rate_date", "rate_from")...)
	parts = append(parts, diffList("category", before.Categories, after.Categories, categoryKey)...)
	parts = append(parts, diffList("item", before.Items, after.Items, itemKey)...)
	parts = append(parts, diffList("debt", before.Debts, after.Debts, debtKey)...)
	parts = append(parts, diffList("pot", before.Pots, after.Pots, potKey)...)
	parts = append(parts, diffList("withdrawal", before.Withdrawals, after.Withdrawals, withdrawalKey)...)
	parts = append(parts, diffList("scenario", before.Scenarios, after.Scenarios, scenarioKey)...)
	if len(parts) == 0 {
		return "no changes"
	}
	if len(parts) > maxChangeParts {
		n := len(parts) - maxChangeParts
		parts = append(parts[:maxChangeParts], fmt.Sprintf("and %d more change%s", n, plural(n)))
	}
	return strings.Join(parts, "; ")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
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
func diffList[T any](kind string, before, after []T, key func(T) (id, name string), ignore ...string) []string {
	old := map[string]T{}
	for _, el := range before {
		id, _ := key(el)
		old[id] = el
	}
	kept := map[string]bool{}
	var out []string
	for _, el := range after {
		id, name := key(el)
		kept[id] = true
		prev, ok := old[id]
		if !ok {
			out = append(out, fmt.Sprintf("added %s %s", kind, name))
			continue
		}
		_, prevName := key(prev) // a rename is described under the old name
		out = append(out, diffJSONFields(prevName+": ", prev, el, ignore...)...)
	}
	for _, el := range before {
		if id, name := key(el); !kept[id] {
			out = append(out, fmt.Sprintf("removed %s %s", kind, name))
		}
	}
	return out
}

// diffJSONFields compares two values field by field through their JSON
// form, so a field added to a type is covered without changing this code.
// Each change reads "field old → new", and all changes to one thing share
// the prefix once.
func diffJSONFields(prefix string, before, after any, ignore ...string) []string {
	a, errA := jsonFields(before)
	b, errB := jsonFields(after)
	if errA != nil || errB != nil {
		return []string{prefix + "changed"} // can't happen for budget types
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
	var changes []string
	for _, k := range keys {
		if slices.Contains(ignore, k) || bytes.Equal(a[k], b[k]) {
			continue
		}
		changes = append(changes, fmt.Sprintf("%s %s → %s", k, show(a[k]), show(b[k])))
	}
	if len(changes) == 0 {
		return nil
	}
	if prefix == "" {
		return changes
	}
	return []string{prefix + strings.Join(changes, ", ")}
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

func show(raw json.RawMessage) string {
	if raw == nil {
		return "(none)"
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}
