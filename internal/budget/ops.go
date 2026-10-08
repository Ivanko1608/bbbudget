package budget

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"bbbudget/internal/money"
)

// Op is one edit to a budget. Every change (UI, MCP, scenarios) is expressed
// as Ops so it can be previewed (what-if), saved (scenario) or applied.
//
//	{"op":"set_line","target":"rent","value":{"amount":"800"}}
type Op struct {
	Op string `json:"op"`
	// Target names the thing to change: an id, an exact name, or an
	// unambiguous part of a name (case-insensitive). Currency code for *_currency.
	Target string          `json:"target,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
}

// Patches: nil fields are left unchanged.

type LinePatch struct {
	Name     *string        `json:"name,omitempty"`
	Amount   *money.Decimal `json:"amount,omitempty"`
	Currency *string        `json:"currency,omitempty"`
	Category *string        `json:"category,omitempty"`
	Schedule *Schedule      `json:"schedule,omitempty"`
}

type CurrencyPatch struct {
	PerBase        *money.Decimal `json:"per_base,omitempty"`
	TransferFeePct *money.Decimal `json:"transfer_fee_pct,omitempty"`
}

type SettingsPatch struct {
	Name           *string        `json:"name,omitempty"`
	Description    *string        `json:"description,omitempty"`
	StartMonth     *string        `json:"start_month,omitempty"`
	Months         *int           `json:"months,omitempty"`
	SavingsStart   *money.Decimal `json:"savings_start,omitempty"`
	ExtraToDebtPct *money.Decimal `json:"extra_to_debt_pct,omitempty"`
	DebtStrategy   *string        `json:"debt_strategy,omitempty"`
}

type DebtPatch struct {
	Name       *string        `json:"name,omitempty"`
	Currency   *string        `json:"currency,omitempty"`
	Balance    *money.Decimal `json:"balance,omitempty"`
	APRPct     *money.Decimal `json:"apr_pct,omitempty"`
	MinPayment *money.Decimal `json:"min_payment,omitempty"`
}

type GoalPatch struct {
	Name   *string        `json:"name,omitempty"`
	Target *money.Decimal `json:"target,omitempty"`
}

type CategoryPatch struct {
	Label *string `json:"label,omitempty"`
	Kind  *string `json:"kind,omitempty"`
	Color *string `json:"color,omitempty"`
}

// OpKinds documents every supported op and its value type.
var OpKinds = map[string]string{
	"set_settings":    "value: SettingsPatch (no target)",
	"set_currency":    "target: code; value: CurrencyPatch (adds the currency if missing; per_base required then)",
	"remove_currency": "target: code (must be unused)",
	"add_category":    "value: Category",
	"set_category":    "target: category; value: CategoryPatch",
	"remove_category": "target: category (must be unused)",
	"add_line":        "value: Line (id optional)",
	"set_line":        "target: line; value: LinePatch",
	"remove_line":     "target: line",
	"add_debt":        "value: Debt (id optional)",
	"set_debt":        "target: debt; value: DebtPatch",
	"remove_debt":     "target: debt",
	"add_goal":        "value: Goal (id optional)",
	"set_goal":        "target: goal; value: GoalPatch",
	"remove_goal":     "target: goal",
}

// NewOp builds an Op from a typed value.
func NewOp(kind, target string, value any) Op {
	op := Op{Op: kind, Target: target}
	if value != nil {
		b, err := json.Marshal(value)
		if err != nil {
			panic(err) // only typed patch structs are passed here
		}
		op.Value = b
	}
	return op
}

// OpError is a rejected edit: bad input from the caller, never a system fault.
type OpError struct {
	Index int // 1-based position in the ops list
	Op    string
	Err   error
}

func (e *OpError) Error() string { return fmt.Sprintf("op %d (%s): %v", e.Index, e.Op, e.Err) }
func (e *OpError) Unwrap() error { return e.Err }

// Apply returns a new budget with ops applied in order, validated. The input
// is never modified. On error nothing is applied.
func Apply(b Budget, ops ...Op) (Budget, error) {
	nb, err := Clone(b)
	if err != nil {
		return Budget{}, err
	}
	for i, op := range ops {
		if err := applyOne(&nb, op); err != nil {
			return Budget{}, &OpError{Index: i + 1, Op: op.Op, Err: err}
		}
	}
	if _, err := Compile(nb); err != nil {
		return Budget{}, err
	}
	return nb, nil
}

// Clone deep-copies a budget.
func Clone(b Budget) (Budget, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return Budget{}, err
	}
	var out Budget
	return out, json.Unmarshal(raw, &out)
}

func decodeStrict(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return fmt.Errorf("value is required")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

func applyOne(b *Budget, op Op) error {
	switch op.Op {
	case "set_settings":
		var v SettingsPatch
		if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		setIf(&b.Name, v.Name)
		setIf(&b.Description, v.Description)
		setIf(&b.StartMonth, v.StartMonth)
		setIf(&b.Months, v.Months)
		setIf(&b.SavingsStart, v.SavingsStart)
		setIf(&b.ExtraToDebtPct, v.ExtraToDebtPct)
		setIf(&b.DebtStrategy, v.DebtStrategy)

	case "set_currency":
		var v CurrencyPatch
		if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		code := strings.ToUpper(strings.TrimSpace(op.Target))
		for i := range b.Currencies {
			if b.Currencies[i].Code == code {
				setIf(&b.Currencies[i].PerBase, v.PerBase)
				setIf(&b.Currencies[i].TransferFeePct, v.TransferFeePct)
				return nil
			}
		}
		if v.PerBase == nil {
			return fmt.Errorf("currency %s is new: per_base is required", code)
		}
		c := CurrencyRate{Code: code, PerBase: *v.PerBase}
		setIf(&c.TransferFeePct, v.TransferFeePct)
		b.Currencies = append(b.Currencies, c)

	case "remove_currency":
		code := strings.ToUpper(strings.TrimSpace(op.Target))
		for _, l := range b.Lines {
			if l.Currency == code {
				return fmt.Errorf("currency %s is used by line %q", code, l.Name)
			}
		}
		for _, d := range b.Debts {
			if d.Currency == code {
				return fmt.Errorf("currency %s is used by debt %q", code, d.Name)
			}
		}
		i := indexWhere(b.Currencies, func(c CurrencyRate) bool { return c.Code == code })
		if i < 0 {
			return fmt.Errorf("no currency %s", code)
		}
		b.Currencies = remove(b.Currencies, i)

	case "add_category":
		var v Category
		if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		if v.ID == "" {
			v.ID = uniqueID(v.Label, ids(b.Categories, func(c Category) (string, string) { return c.ID, c.Label }))
		}
		if v.Kind == "" {
			v.Kind = KindExpense
		}
		b.Categories = append(b.Categories, v)

	case "set_category":
		var v CategoryPatch
		if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		i, err := find("category", op.Target, ids(b.Categories, func(c Category) (string, string) { return c.ID, c.Label }))
		if err != nil {
			return err
		}
		setIf(&b.Categories[i].Label, v.Label)
		setIf(&b.Categories[i].Kind, v.Kind)
		setIf(&b.Categories[i].Color, v.Color)

	case "remove_category":
		i, err := find("category", op.Target, ids(b.Categories, func(c Category) (string, string) { return c.ID, c.Label }))
		if err != nil {
			return err
		}
		for _, l := range b.Lines {
			if l.Category == b.Categories[i].ID {
				return fmt.Errorf("category %q is used by line %q", b.Categories[i].Label, l.Name)
			}
		}
		b.Categories = remove(b.Categories, i)

	case "add_line":
		var v Line // an omitted schedule decodes as monthly (see Line.UnmarshalJSON)
		if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		if v.ID == "" {
			v.ID = uniqueID(v.Name, ids(b.Lines, lineKey))
		}
		if v.Currency == "" {
			v.Currency = b.BaseCurrency
		}
		if v.Category == "" {
			return fmt.Errorf("category is required")
		}
		b.Lines = append(b.Lines, v)

	case "set_line":
		var v LinePatch
		if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		i, err := find("line", op.Target, ids(b.Lines, lineKey))
		if err != nil {
			return err
		}
		l := &b.Lines[i]
		setIf(&l.Name, v.Name)
		setIf(&l.Amount, v.Amount)
		setIf(&l.Currency, v.Currency)
		setIf(&l.Category, v.Category)
		setIf(&l.Schedule, v.Schedule)

	case "remove_line":
		i, err := find("line", op.Target, ids(b.Lines, lineKey))
		if err != nil {
			return err
		}
		b.Lines = remove(b.Lines, i)

	case "add_debt":
		var v Debt
		if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		if v.ID == "" {
			v.ID = uniqueID(v.Name, ids(b.Debts, debtKey))
		}
		if v.Currency == "" {
			v.Currency = b.BaseCurrency
		}
		b.Debts = append(b.Debts, v)

	case "set_debt":
		var v DebtPatch
		if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		i, err := find("debt", op.Target, ids(b.Debts, debtKey))
		if err != nil {
			return err
		}
		d := &b.Debts[i]
		setIf(&d.Name, v.Name)
		setIf(&d.Currency, v.Currency)
		setIf(&d.Balance, v.Balance)
		setIf(&d.APRPct, v.APRPct)
		setIf(&d.MinPayment, v.MinPayment)

	case "remove_debt":
		i, err := find("debt", op.Target, ids(b.Debts, debtKey))
		if err != nil {
			return err
		}
		b.Debts = remove(b.Debts, i)

	case "add_goal":
		var v Goal
		if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		if v.ID == "" {
			v.ID = uniqueID(v.Name, ids(b.Goals, goalKey))
		}
		b.Goals = append(b.Goals, v)

	case "set_goal":
		var v GoalPatch
		if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		i, err := find("goal", op.Target, ids(b.Goals, goalKey))
		if err != nil {
			return err
		}
		setIf(&b.Goals[i].Name, v.Name)
		setIf(&b.Goals[i].Target, v.Target)

	case "remove_goal":
		i, err := find("goal", op.Target, ids(b.Goals, goalKey))
		if err != nil {
			return err
		}
		b.Goals = remove(b.Goals, i)

	default:
		return fmt.Errorf("unknown op %q", op.Op)
	}
	return nil
}

func setIf[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

func remove[T any](s []T, i int) []T { return append(s[:i:i], s[i+1:]...) }

func indexWhere[T any](s []T, f func(T) bool) int {
	for i, v := range s {
		if f(v) {
			return i
		}
	}
	return -1
}

type idName struct{ id, name string }

func ids[T any](s []T, key func(T) (string, string)) []idName {
	out := make([]idName, len(s))
	for i, v := range s {
		out[i].id, out[i].name = key(v)
	}
	return out
}

func lineKey(l Line) (string, string) { return l.ID, l.Name }
func debtKey(d Debt) (string, string) { return d.ID, d.Name }
func goalKey(g Goal) (string, string) { return g.ID, g.Name }

// find resolves a reference: exact id, then exact name, then a unique
// substring of a name (all case-insensitive). Ambiguity is an error rather
// than a guess, so "school" never silently edits the wrong line.
func find(kind, ref string, items []idName) (int, error) {
	r := strings.ToLower(strings.TrimSpace(ref))
	if r == "" {
		return -1, fmt.Errorf("target %s is required", kind)
	}
	for i, it := range items {
		if strings.ToLower(it.id) == r {
			return i, nil
		}
	}
	for i, it := range items {
		if strings.ToLower(it.name) == r {
			return i, nil
		}
	}
	var hits []int
	for i, it := range items {
		if strings.Contains(strings.ToLower(it.name), r) {
			hits = append(hits, i)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return -1, &NoMatchError{Kind: kind, Ref: ref}
	}
	names := make([]string, len(hits))
	for k, i := range hits {
		names[k] = fmt.Sprintf("%q (id %s)", items[i].name, items[i].id)
	}
	return -1, fmt.Errorf("%q matches several %ss: %s; use the id", ref, kind, strings.Join(names, ", "))
}

// NoMatchError means a reference matched nothing (as opposed to several things).
type NoMatchError struct{ Kind, Ref string }

func (e *NoMatchError) Error() string { return fmt.Sprintf("no %s matches %q", e.Kind, e.Ref) }

// FindDebt resolves a debt reference to its index.
func FindDebt(b Budget, ref string) (int, error) { return find("debt", ref, ids(b.Debts, debtKey)) }

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// uniqueID derives a readable id from a name: "School fees (term 2)" -> "school-fees-term-2".
func uniqueID(name string, existing []idName) string {
	base := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if base == "" {
		base = "item"
	}
	taken := map[string]bool{}
	for _, e := range existing {
		taken[e.id] = true
	}
	id := base
	for n := 2; taken[id]; n++ {
		id = base + "-" + strconv.Itoa(n)
	}
	return id
}
