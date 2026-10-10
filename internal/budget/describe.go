package budget

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"

	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
)

// maxChangeParts caps how many changes Describe spells out.
const maxChangeParts = 6

// Difference kinds.
const (
	Added   = "added"
	Removed = "removed"
	Changed = "changed" // one field of a thing, or a budget setting
)

// Difference is one way two versions of a budget differ: a thing added or
// removed, or one field changed. The history stores these, and each reader
// turns them into text (Describe for English).
type Difference struct {
	Kind string `json:"kind"`
	// Thing is what kind of thing it is ("item", "pot", ...); empty for a
	// budget setting.
	Thing string `json:"thing,omitempty"`
	// Name is the thing's name, as it was before a rename.
	Name string `json:"name,omitempty"`
	// Field, Old and New say what changed, as JSON names and values; an
	// empty value means unset.
	Field string `json:"field,omitempty"`
	Old   string `json:"old,omitempty"`
	New   string `json:"new,omitempty"`
}

// Differences lists what changed between two versions of a budget. Things
// are matched by id (currencies by code), so a rename is a changed name,
// not a removal plus an addition.
func Differences(before, after Budget) []Difference {
	diffs := diffJSONFields("", "", settingsOf(before), settingsOf(after))
	diffs = append(diffs, diffList("currency", before.Currencies, after.Currencies, currencyKey, "rate_date", "rate_from")...)
	diffs = append(diffs, diffList("category", before.Categories, after.Categories, categoryKey)...)
	diffs = append(diffs, diffList("item", before.Items, after.Items, itemKey)...)
	diffs = append(diffs, diffList("debt", before.Debts, after.Debts, debtKey)...)
	diffs = append(diffs, diffList("pot", before.Pots, after.Pots, potKey)...)
	diffs = append(diffs, diffList("withdrawal", before.Withdrawals, after.Withdrawals, withdrawalKey)...)
	diffs = append(diffs, diffList("scenario", before.Scenarios, after.Scenarios, scenarioKey)...)
	return diffs
}

// Describe summarises differences in one line of English, for the change
// history: "Rent: amount 1150 → 1200; added item Gym". DifferencesMsg is the
// same summary as a message, for rendering in the reader's language.
func Describe(diffs []Difference) string { return english().Render(DifferencesMsg(diffs)) }

// DifferencesMsg summarises differences as a message. Changed fields of one
// thing share its name; it is "no changes" when there are none.
func DifferencesMsg(diffs []Difference) i18n.Msg {
	var parts []i18n.Msg
	for i := 0; i < len(diffs); i++ {
		d := diffs[i]
		switch {
		case d.Kind != Changed:
			parts = append(parts, msg("change."+d.Kind, args{"kind": i18n.Nested(msg("thing."+d.Thing, nil)), "name": i18n.Text(d.Name)}))
		case d.Thing == "":
			parts = append(parts, fieldMsg(d))
		default:
			fields := []i18n.Msg{fieldMsg(d)}
			for i+1 < len(diffs) && sameThing(diffs[i+1], d) {
				i++
				fields = append(fields, fieldMsg(diffs[i]))
			}
			parts = append(parts, msg("change.fields", args{"name": i18n.Text(d.Name), "fields": i18n.JoinedList(fields, ", ")}))
		}
	}
	if len(parts) == 0 {
		return msg("change.none", nil)
	}
	if len(parts) > maxChangeParts {
		n := len(parts) - maxChangeParts
		parts = append(parts[:maxChangeParts], msg("change.more", args{"count": i18n.Int(int64(n))}))
	}
	return msg("change.list", args{"items": i18n.JoinedList(parts, "; ")})
}

func sameThing(a, b Difference) bool {
	return a.Kind == Changed && a.Thing == b.Thing && a.Name == b.Name
}

func fieldMsg(d Difference) i18n.Msg {
	field := i18n.Text(d.Field) // a field the catalog doesn't name yet shows as is
	if key := "field." + d.Field; english().Has(key) {
		field = i18n.Nested(msg(key, nil))
	}
	return msg("change.field", args{"field": field, "old": orNone(d.Old), "new": orNone(d.New)})
}

func orNone(v string) i18n.Arg {
	if v == "" {
		return i18n.Nested(msg("change.empty", nil))
	}
	return i18n.Text(v)
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
func diffList[T any](kind string, before, after []T, key func(T) (id, name string), ignore ...string) []Difference {
	old := map[string]T{}
	for _, el := range before {
		id, _ := key(el)
		old[id] = el
	}
	kept := map[string]bool{}
	var out []Difference
	for _, el := range after {
		id, name := key(el)
		kept[id] = true
		prev, ok := old[id]
		if !ok {
			out = append(out, Difference{Kind: Added, Thing: kind, Name: name})
			continue
		}
		_, prevName := key(prev) // a rename is described under the old name
		out = append(out, diffJSONFields(kind, prevName, prev, el, ignore...)...)
	}
	for _, el := range before {
		if id, name := key(el); !kept[id] {
			out = append(out, Difference{Kind: Removed, Thing: kind, Name: name})
		}
	}
	return out
}

// diffJSONFields compares two versions of a thing field by field through
// their JSON form, so a field added to a type is covered without changing
// this code. thing and name say which thing it is ("" for the settings).
func diffJSONFields(thing, name string, before, after any, ignore ...string) []Difference {
	a, errA := jsonFields(before)
	b, errB := jsonFields(after)
	if errA != nil || errB != nil { // can't happen for budget types
		return []Difference{{Kind: Changed, Thing: thing, Name: name}}
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
	var diffs []Difference
	for _, k := range keys {
		if slices.Contains(ignore, k) || bytes.Equal(a[k], b[k]) {
			continue
		}
		diffs = append(diffs, Difference{Kind: Changed, Thing: thing, Name: name, Field: k, Old: show(a[k]), New: show(b[k])})
	}
	return diffs
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

// show is a JSON value as text: strings without their quotes, "" when unset.
func show(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}
