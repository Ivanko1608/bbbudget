package budget

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// maxChangeParts caps how many changes DescribeChange spells out.
const maxChangeParts = 6

// DescribeChange summarises the difference between two versions of a budget
// in one line, for the change history: "Rent: amount 1150 → 1200; added
// line Gym". Items are matched by id (currencies by code), so a rename reads
// as a change, not a removal plus an addition. It is "no changes" when the
// documents are equal.
func DescribeChange(before, after Budget) string {
	var parts []string
	parts = append(parts, diffFields("", settingsOf(before), settingsOf(after))...)
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

// settingsOf is the budget without its lists, so settings diff field by field.
func settingsOf(b Budget) Budget {
	b.Currencies, b.Categories, b.Lines, b.Debts, b.Goals, b.Scenarios = nil, nil, nil, nil, nil, nil
	return b
}

// diffItems reports added, removed and changed items. key returns an item's
// id and its display name; ignore lists fields that aren't worth reporting.
func diffItems[T any](kind string, before, after []T, key func(T) (string, string), ignore ...string) []string {
	old := map[string]T{}
	for _, it := range before {
		id, _ := key(it)
		old[id] = it
	}
	seen := map[string]bool{}
	var out []string
	for _, it := range after {
		id, name := key(it)
		seen[id] = true
		prev, ok := old[id]
		if !ok {
			out = append(out, fmt.Sprintf("added %s %s", kind, name))
			continue
		}
		_, prevName := key(prev)
		label := name
		if prevName != name {
			label = prevName
		}
		out = append(out, diffFields(label+": ", prev, it, ignore...)...)
	}
	for _, it := range before {
		if id, name := key(it); !seen[id] {
			out = append(out, fmt.Sprintf("removed %s %s", kind, name))
		}
	}
	return out
}

// diffFields compares two values field by field through their JSON form, so
// it stays correct as fields are added. Each change reads "field old → new";
// all changes to one item share the prefix once.
func diffFields(prefix string, before, after any, ignore ...string) []string {
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
