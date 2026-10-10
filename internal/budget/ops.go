package budget

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bbbudget/internal/money"
)

// Op is one edit to a budget. Every change (UI, MCP, scenarios) is expressed
// as Ops so it can be previewed (what-if), saved (scenario) or applied.
//
//	{"op":"set_item","target":"rent","value":{"amount":"800"}}
type Op struct {
	Op string `json:"op"`
	// Target names the thing to change: an id, an exact name, or an
	// unambiguous part of a name (case-insensitive). Currency code for *_currency.
	Target string          `json:"target,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
}

// Patches: nil fields are left unchanged.

type ItemPatch struct {
	Name     *string        `json:"name,omitempty"`
	Amount   *money.Decimal `json:"amount,omitempty"`
	Currency *string        `json:"currency,omitempty"`
	Category *string        `json:"category,omitempty"`
	Schedule *ScheduleSpec  `json:"schedule,omitempty"`
	// Pot routes the item through a pot; "" puts it back in the month's budget.
	Pot *string `json:"pot,omitempty"`
}

// ItemInput is the value of add_item: an Item whose schedule is a ScheduleSpec.
// An omitted schedule means monthly from the first month.
type ItemInput struct {
	ID       string        `json:"id,omitempty"`
	Name     string        `json:"name"`
	Amount   money.Decimal `json:"amount"`
	Currency string        `json:"currency,omitempty"`
	Category string        `json:"category"`
	Schedule *ScheduleSpec `json:"schedule,omitempty"`
	Pot      string        `json:"pot,omitempty"`
}

// ScheduleSpec is a schedule as ops describe it, in one of two forms:
//
//   - by calendar month (preferred): {"starts":"2026-03","ends":"2026-12","every":1}.
//     Months are resolved against the budget's start_month when the op is
//     applied, so a stored scenario keeps pointing at the same calendar months.
//     Omitted fields keep the item's current values (monthly from the first
//     month for a new item), and "ends": "" removes the end. A spec with only
//     "every" is in this form too.
//   - by month index from start_month, whenever start or end is given:
//     {"start":2,"every":0,"end":5}. This is the stored form; omitted fields
//     are 0 (so no every means one-off) and the whole schedule is replaced.
//
// Mixing the two forms is an error, never a guess.
type ScheduleSpec struct {
	Start  *int    `json:"start,omitempty"`
	Every  *int    `json:"every,omitempty"`
	End    *int    `json:"end,omitempty"`
	Starts string  `json:"starts,omitempty"`
	Ends   *string `json:"ends,omitempty"`
}

// resolve turns the spec into a Schedule for a budget starting at startMonth.
// cur is the schedule being edited (set_item), nil for a new item.
func (s ScheduleSpec) resolve(startMonth string, cur *Schedule) (Schedule, error) {
	byIndex := s.Start != nil || s.End != nil
	if byIndex && (s.Starts != "" || s.Ends != nil) {
		return Schedule{}, fmt.Errorf("schedule: use either starts/ends (YYYY-MM) or start/end (month indexes), not both")
	}
	if byIndex {
		out := Schedule{End: s.End}
		setIf(&out.Start, s.Start)
		setIf(&out.Every, s.Every)
		return out, nil
	}
	out := Monthly()
	if cur != nil {
		out = *cur
	}
	setIf(&out.Every, s.Every)
	if s.Starts != "" {
		i, err := MonthIndex(startMonth, s.Starts)
		if err != nil {
			return Schedule{}, fmt.Errorf("schedule.starts: %w", err)
		}
		out.Start = i
	}
	if s.Ends != nil {
		out.End = nil
		if *s.Ends != "" {
			i, err := MonthIndex(startMonth, *s.Ends)
			if err != nil {
				return Schedule{}, fmt.Errorf("schedule.ends: %w", err)
			}
			out.End = &i
		}
	}
	return out, nil
}

// MonthIndex is the 0-based index of month ym ("YYYY-MM") in a budget that
// starts in startMonth. Months before the start are an error.
func MonthIndex(startMonth, ym string) (int, error) {
	s, err := time.Parse("2006-01", startMonth)
	if err != nil {
		return 0, fmt.Errorf("budget start_month %q is not YYYY-MM", startMonth)
	}
	t, err := time.Parse("2006-01", strings.TrimSpace(ym))
	if err != nil {
		return 0, fmt.Errorf("month must be YYYY-MM, got %q", ym)
	}
	i := (t.Year()-s.Year())*12 + int(t.Month()-s.Month())
	if i < 0 {
		return 0, fmt.Errorf("%s is before the budget starts (%s)", ym, startMonth)
	}
	return i, nil
}

// CurrencyPatch edits a currency. Setting per_base without a source makes the
// rate manual (a typed rate would otherwise be overwritten by the next refresh).
// A manual rate carries no fetch date or provider.
type CurrencyPatch struct {
	PerBase        *money.Decimal `json:"per_base,omitempty"`
	TransferFeePct *money.Decimal `json:"transfer_fee_pct,omitempty"`
	CashOutFeePct  *money.Decimal `json:"cash_out_fee_pct,omitempty"`
	Source         *string        `json:"source,omitempty"`
	RateDate       *string        `json:"rate_date,omitempty"`
	RateFrom       *string        `json:"rate_from,omitempty"`
}

func (v CurrencyPatch) apply(c *CurrencyRate) {
	if v.PerBase != nil && v.Source == nil {
		c.Source = SourceManual
	}
	setIf(&c.PerBase, v.PerBase)
	setIf(&c.TransferFeePct, v.TransferFeePct)
	setIf(&c.CashOutFeePct, v.CashOutFeePct)
	setIf(&c.Source, v.Source)
	setIf(&c.RateDate, v.RateDate)
	setIf(&c.RateFrom, v.RateFrom)
	if c.IsManual() {
		c.RateDate, c.RateFrom = "", ""
	}
}

type SettingsPatch struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	StartMonth  *string `json:"start_month,omitempty"`
	Months      *int    `json:"months,omitempty"`
	// SavingsStart is the old single savings balance: it sets the balance of
	// the default pot (the first general one).
	SavingsStart   *money.Decimal `json:"savings_start,omitempty"`
	OverdraftStart *money.Decimal `json:"overdraft_start,omitempty"`
	ExtraToDebtPct *money.Decimal `json:"extra_to_debt_pct,omitempty"`
	DebtStrategy   *string        `json:"debt_strategy,omitempty"`
}

type DebtPatch struct {
	Name       *string        `json:"name,omitempty"`
	Currency   *string        `json:"currency,omitempty"`
	Balance    *money.Decimal `json:"balance,omitempty"`
	APRPct     *money.Decimal `json:"apr_pct,omitempty"`
	MinPayment *money.Decimal `json:"min_payment,omitempty"`
	// PayoffMonth sets the month to pay the debt off in full; -1 removes the payoff.
	PayoffMonth *int `json:"payoff_month,omitempty"`
	// PayoffFrom names the pot that pays it off; "" pays it from the month's own money.
	PayoffFrom *string `json:"payoff_from,omitempty"`
}

type GoalPatch struct {
	Name   *string        `json:"name,omitempty"`
	Target *money.Decimal `json:"target,omitempty"`
}

type PotPatch struct {
	Name    *string        `json:"name,omitempty"`
	Kind    *string        `json:"kind,omitempty"`
	Balance *money.Decimal `json:"balance,omitempty"`
	Target  *money.Decimal `json:"target,omitempty"`
	Monthly *money.Decimal `json:"monthly,omitempty"`
}

type WithdrawalPatch struct {
	Name     *string        `json:"name,omitempty"`
	Pot      *string        `json:"pot,omitempty"`
	Amount   *money.Decimal `json:"amount,omitempty"`
	Schedule *Schedule      `json:"schedule,omitempty"`
}

type CategoryPatch struct {
	Label *string `json:"label,omitempty"`
	Kind  *string `json:"kind,omitempty"`
	Color *string `json:"color,omitempty"`
}

// OpKinds documents every supported op and its value type.
var OpKinds = map[string]string{
	"set_settings":      "value: SettingsPatch (no target)",
	"set_currency":      "target: code; value: CurrencyPatch (adds the currency if missing; per_base required then; a per_base without source makes the rate manual)",
	"remove_currency":   "target: code (must be unused)",
	"add_category":      "value: Category",
	"set_category":      "target: category; value: CategoryPatch",
	"remove_category":   "target: category (must be unused)",
	"add_item":          "value: Item (id optional; schedule optional, default monthly)",
	"set_item":          "target: item; value: ItemPatch (schedule: only the given fields change when using starts/ends)",
	"remove_item":       "target: item",
	"add_debt":          "value: Debt (id optional; payoff_month + payoff_from pot pay it off in full that month)",
	"set_debt":          "target: debt; value: DebtPatch (payoff_month -1 removes the payoff; payoff_from \"\" pays it from the month's own money)",
	"remove_debt":       "target: debt",
	"add_pot":           "value: Pot (id optional; kind general|sinking|emergency, default general)",
	"set_pot":           "target: pot; value: PotPatch",
	"remove_pot":        "target: pot (must have no withdrawals; keep at least one pot)",
	"add_withdrawal":    "value: Withdrawal{name, pot, amount, schedule} (id optional; base currency; schedule defaults to once in month 0)",
	"set_withdrawal":    "target: withdrawal; value: WithdrawalPatch",
	"remove_withdrawal": "target: withdrawal",
	"add_goal":          "value: Goal{name, target}: same as add_pot with kind sinking (kept for older clients)",
	"set_goal":          "target: pot; value: GoalPatch (kept for older clients)",
	"remove_goal":       "target: pot (kept for older clients)",
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
		setIf(&b.OverdraftStart, v.OverdraftStart)
		if v.SavingsStart != nil {
			i := defaultPot(b)
			if i < 0 {
				return fmt.Errorf("savings_start needs a savings pot; add one with add_pot")
			}
			b.Pots[i].Balance = *v.SavingsStart
		}
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
				v.apply(&b.Currencies[i])
				return nil
			}
		}
		if v.PerBase == nil {
			return fmt.Errorf("currency %s is new: per_base is required", code)
		}
		c := CurrencyRate{Code: code}
		v.apply(&c)
		b.Currencies = append(b.Currencies, c)

	case "remove_currency":
		code := strings.ToUpper(strings.TrimSpace(op.Target))
		for _, l := range b.Items {
			if l.Currency == code {
				return fmt.Errorf("currency %s is used by item %q", code, l.Name)
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
		for _, l := range b.Items {
			if l.Category == b.Categories[i].ID {
				return fmt.Errorf("category %q is used by item %q", b.Categories[i].Label, l.Name)
			}
		}
		b.Categories = remove(b.Categories, i)

	case "add_item":
		var in ItemInput
		if err := decodeStrict(op.Value, &in); err != nil {
			return err
		}
		v := Item{ID: in.ID, Name: in.Name, Amount: in.Amount, Currency: in.Currency, Category: in.Category, Schedule: Monthly(), Pot: in.Pot}
		if in.Schedule != nil {
			s, err := in.Schedule.resolve(b.StartMonth, nil)
			if err != nil {
				return err
			}
			v.Schedule = s
		}
		if v.ID == "" {
			v.ID = uniqueID(v.Name, ids(b.Items, itemKey))
		}
		if v.Currency == "" {
			v.Currency = b.BaseCurrency
		}
		if v.Category == "" {
			return fmt.Errorf("category is required")
		}
		if v.Pot != "" {
			id, err := potRef(b, v.Pot)
			if err != nil {
				return err
			}
			v.Pot = id
		}
		b.Items = append(b.Items, v)

	case "set_item":
		var v ItemPatch
		if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		i, err := find("item", op.Target, ids(b.Items, itemKey))
		if err != nil {
			return err
		}
		l := &b.Items[i]
		setIf(&l.Name, v.Name)
		setIf(&l.Amount, v.Amount)
		setIf(&l.Currency, v.Currency)
		setIf(&l.Category, v.Category)
		if v.Schedule != nil {
			s, err := v.Schedule.resolve(b.StartMonth, &l.Schedule)
			if err != nil {
				return err
			}
			l.Schedule = s
		}
		if v.Pot != nil {
			id := ""
			if *v.Pot != "" {
				if id, err = potRef(b, *v.Pot); err != nil {
					return err
				}
			}
			l.Pot = id
		}

	case "remove_item":
		i, err := find("item", op.Target, ids(b.Items, itemKey))
		if err != nil {
			return err
		}
		b.Items = remove(b.Items, i)

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
		if v.PayoffFrom != "" {
			id, err := potRef(b, v.PayoffFrom)
			if err != nil {
				return err
			}
			v.PayoffFrom = id
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
		if v.PayoffFrom != nil {
			id := ""
			if *v.PayoffFrom != "" {
				if id, err = potRef(b, *v.PayoffFrom); err != nil {
					return err
				}
			}
			d.PayoffFrom = id
		}
		if v.PayoffMonth != nil {
			if *v.PayoffMonth < 0 {
				d.PayoffMonth, d.PayoffFrom = nil, ""
			} else {
				d.PayoffMonth = new(int)
				*d.PayoffMonth = *v.PayoffMonth
			}
		}

	case "remove_debt":
		i, err := find("debt", op.Target, ids(b.Debts, debtKey))
		if err != nil {
			return err
		}
		b.Debts = remove(b.Debts, i)

	case "add_pot", "add_goal":
		v := Pot{Kind: PotGeneral}
		if op.Op == "add_goal" {
			var g Goal
			if err := decodeStrict(op.Value, &g); err != nil {
				return err
			}
			v = Pot{ID: g.ID, Name: g.Name, Kind: PotSinking, Target: g.Target}
		} else if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		if v.ID == "" {
			v.ID = uniqueID(v.Name, ids(b.Pots, potKey))
		}
		if v.Kind == "" {
			v.Kind = PotGeneral
		}
		b.Pots = append(b.Pots, v)

	case "set_pot", "set_goal":
		var v PotPatch
		if op.Op == "set_goal" {
			var g GoalPatch
			if err := decodeStrict(op.Value, &g); err != nil {
				return err
			}
			v = PotPatch{Name: g.Name, Target: g.Target}
		} else if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		i, err := find("pot", op.Target, ids(b.Pots, potKey))
		if err != nil {
			return err
		}
		pt := &b.Pots[i]
		setIf(&pt.Name, v.Name)
		setIf(&pt.Kind, v.Kind)
		setIf(&pt.Balance, v.Balance)
		setIf(&pt.Target, v.Target)
		setIf(&pt.Monthly, v.Monthly)

	case "remove_pot", "remove_goal":
		i, err := find("pot", op.Target, ids(b.Pots, potKey))
		if err != nil {
			return err
		}
		for _, w := range b.Withdrawals {
			if w.Pot == b.Pots[i].ID {
				return fmt.Errorf("pot %q is used by the transfer %q; remove that first", b.Pots[i].Name, w.Name)
			}
		}
		for _, d := range b.Debts {
			if d.PayoffFrom == b.Pots[i].ID {
				return fmt.Errorf("pot %q pays off the debt %q; change that first", b.Pots[i].Name, d.Name)
			}
		}
		for _, l := range b.Items {
			if l.Pot == b.Pots[i].ID {
				return fmt.Errorf("pot %q is used by the item %q; route that item elsewhere first", b.Pots[i].Name, l.Name)
			}
		}
		b.Pots = remove(b.Pots, i)

	case "add_withdrawal":
		var v Withdrawal
		if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		i, err := find("pot", v.Pot, ids(b.Pots, potKey))
		if err != nil {
			return err
		}
		v.Pot = b.Pots[i].ID
		if v.ID == "" {
			v.ID = uniqueID(v.Name, ids(b.Withdrawals, withdrawalKey))
		}
		b.Withdrawals = append(b.Withdrawals, v)

	case "set_withdrawal":
		var v WithdrawalPatch
		if err := decodeStrict(op.Value, &v); err != nil {
			return err
		}
		i, err := find("withdrawal", op.Target, ids(b.Withdrawals, withdrawalKey))
		if err != nil {
			return err
		}
		w := &b.Withdrawals[i]
		if v.Pot != nil {
			k, err := find("pot", *v.Pot, ids(b.Pots, potKey))
			if err != nil {
				return err
			}
			w.Pot = b.Pots[k].ID
		}
		setIf(&w.Name, v.Name)
		setIf(&w.Amount, v.Amount)
		setIf(&w.Schedule, v.Schedule)

	case "remove_withdrawal":
		i, err := find("withdrawal", op.Target, ids(b.Withdrawals, withdrawalKey))
		if err != nil {
			return err
		}
		b.Withdrawals = remove(b.Withdrawals, i)

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

func itemKey(l Item) (id, name string)             { return l.ID, l.Name }
func debtKey(d Debt) (id, name string)             { return d.ID, d.Name }
func potKey(p Pot) (id, name string)               { return p.ID, p.Name }
func withdrawalKey(w Withdrawal) (id, name string) { return w.ID, w.Name }

// potRef resolves a pot reference (id or name) to its id.
func potRef(b *Budget, ref string) (string, error) {
	i, err := find("pot", ref, ids(b.Pots, potKey))
	if err != nil {
		return "", err
	}
	return b.Pots[i].ID, nil
}

// defaultPot is the index of the pot that receives unassigned leftover: the
// first general pot, else the last pot, or -1 when there are none.
func defaultPot(b *Budget) int {
	for i, p := range b.Pots {
		if p.Kind == PotGeneral {
			return i
		}
	}
	return len(b.Pots) - 1
}

// find resolves a reference: exact id, then exact name, then a unique
// substring of a name (all case-insensitive). Ambiguity is an error rather
// than a guess, so "school" never silently edits the wrong item.
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
