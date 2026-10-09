package budget

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"

	"bbbudget/internal/i18n"
)

// maxChangeParts caps how many changes DescribeChange spells out.
const maxChangeParts = 6

// DescribeChange summarises the difference between two versions of a budget
// in one line, for the change history: "Rent: amount 1150 → 1200; added
// line Gym", in English. ChangeMsg is the same summary as a message, for
// rendering in the reader's language.
func DescribeChange(before, after Budget) string { return english().T(ChangeMsg(before, after)) }

// ChangeMsg summarises a change as a message. Items are matched by id
// (currencies by code), so a rename reads as a change, not a removal plus an
// addition. Equal documents give "no changes".
func ChangeMsg(before, after Budget) i18n.Msg {
	var parts []i18n.Msg
	parts = append(parts, diffFields(settingsOf(before), settingsOf(after))...)
	parts = append(parts, diffItems("currency", before.Currencies, after.Currencies,
		func(c CurrencyRate) (string, string) { return c.Code, c.Code }, "rate_date", "rate_from")...)
	parts = append(parts, diffItems("category", before.Categories, after.Categories,
		func(c Category) (string, string) { return c.ID, c.Label })...)
	parts = append(parts, diffItems("line", before.Lines, after.Lines, func(l Line) (string, string) { return l.ID, l.Name })...)
	parts = append(parts, diffItems("debt", before.Debts, after.Debts, func(d Debt) (string, string) { return d.ID, d.Name })...)
	parts = append(parts, diffItems("goal", before.Goals, after.Goals, func(g Goal) (string, string) { return g.ID, g.Name })...)
	parts = append(parts, diffItems("scenario", before.Scenarios, after.Scenarios,
		func(s Scenario) (string, string) { return s.ID, s.Name })...)
	if len(parts) == 0 {
		return i18n.Msg{Key: "change.none"}
	}
	if len(parts) > maxChangeParts {
		n := len(parts) - maxChangeParts
		parts = append(parts[:maxChangeParts], msg("change.more", args{"count": i18n.Int(int64(n))}))
	}
	return msg("change.list", args{"items": i18n.JoinedList(parts, "; ")})
}

// settingsOf is the budget without its lists, so settings diff field by field.
func settingsOf(b Budget) Budget {
	b.Currencies, b.Categories, b.Lines, b.Debts, b.Goals, b.Scenarios = nil, nil, nil, nil, nil, nil
	return b
}

// diffItems reports added, removed and changed items. key returns an item's
// id and its display name; ignore lists fields that aren't worth reporting.
func diffItems[T any](kind string, before, after []T, key func(T) (string, string), ignore ...string) []i18n.Msg {
	old := map[string]T{}
	for _, it := range before {
		id, _ := key(it)
		old[id] = it
	}
	thing := i18n.List([]i18n.Msg{{Key: "thing." + kind}})
	seen := map[string]bool{}
	var out []i18n.Msg
	for _, it := range after {
		id, name := key(it)
		seen[id] = true
		prev, ok := old[id]
		if !ok {
			out = append(out, msg("change.added", args{"kind": thing, "name": i18n.Text(name)}))
			continue
		}
		_, prevName := key(prev)
		if fields := diffFields(prev, it, ignore...); len(fields) > 0 {
			out = append(out, msg("change.item", args{"name": i18n.Text(prevName), "fields": i18n.JoinedList(fields, ", ")}))
		}
	}
	for _, it := range before {
		if id, name := key(it); !seen[id] {
			out = append(out, msg("change.removed", args{"kind": thing, "name": i18n.Text(name)}))
		}
	}
	return out
}

// diffFields compares two values field by field through their JSON form, so
// it stays correct as fields are added: one "field old → new" per change.
func diffFields(before, after any, ignore ...string) []i18n.Msg {
	a, b := fieldsOf(before), fieldsOf(after)
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

func fieldsOf(v any) map[string]json.RawMessage {
	raw, _ := json.Marshal(v) // budget types always marshal
	var m map[string]json.RawMessage
	json.Unmarshal(raw, &m)
	for k, v := range m {
		if string(v) == "null" || string(v) == `""` || string(v) == "[]" {
			delete(m, k) // absent and empty read the same
		}
	}
	return m
}

func show(raw json.RawMessage) i18n.Arg {
	if raw == nil {
		return i18n.List([]i18n.Msg{{Key: "change.empty"}})
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return i18n.Text(s)
	}
	return i18n.Text(string(raw))
}
