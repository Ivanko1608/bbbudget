package budget

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"bbbudget/internal/money"
	"go.yaml.in/yaml/v3"
)

// MaxDocumentBytes bounds any budget or template file.
const MaxDocumentBytes = 1 << 20

// ParseDocument decodes a budget document from JSON or YAML (format "json",
// "yaml" or "yml") and normalizes it. Unknown fields are errors, so typos
// never silently drop data. It does not validate: call Compile for that.
//
// YAML numbers keep their literal text: `amount: 45.50` is exactly 45.50,
// never a float64.
func ParseDocument(data []byte, format string) (Budget, error) {
	if len(data) > MaxDocumentBytes {
		return Budget{}, fmt.Errorf("document is larger than %d bytes", MaxDocumentBytes)
	}
	var err error
	switch strings.ToLower(format) {
	case "json":
	case "yaml", "yml":
		if data, err = yamlToJSON(data); err != nil {
			return Budget{}, err
		}
	default:
		return Budget{}, fmt.Errorf("unsupported format %q (use json or yaml)", format)
	}
	var b Budget
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		return Budget{}, err
	}
	Normalize(&b)
	return b, nil
}

// Normalize fills in what a hand-written document may leave out: empty lists,
// ids derived from names, the base currency on lines and debts, the default
// debt strategy and a 12-month window. It never changes a value that was set.
func Normalize(b *Budget) {
	if b.Months == 0 {
		b.Months = 12
	}
	if b.DebtStrategy == "" {
		b.DebtStrategy = StrategyAvalanche
	}
	emptyLists(b)
	for i := range b.Categories {
		c := &b.Categories[i]
		if c.ID == "" {
			c.ID = uniqueID(c.Label, ids(b.Categories, func(c Category) (string, string) { return c.ID, c.Label }))
		}
		if c.Kind == "" {
			c.Kind = KindExpense
		}
	}
	for i := range b.Lines {
		l := &b.Lines[i]
		if l.ID == "" {
			l.ID = uniqueID(l.Name, ids(b.Lines, lineKey))
		}
		if l.Currency == "" {
			l.Currency = b.BaseCurrency
		}
	}
	for i := range b.Debts {
		d := &b.Debts[i]
		if d.ID == "" {
			d.ID = uniqueID(d.Name, ids(b.Debts, debtKey))
		}
		if d.Currency == "" {
			d.Currency = b.BaseCurrency
		}
	}
	for i := range b.Pots {
		p := &b.Pots[i]
		if p.ID == "" {
			p.ID = uniqueID(p.Name, ids(b.Pots, potKey))
		}
		if p.Kind == "" {
			p.Kind = PotGeneral
		}
	}
	for i := range b.Withdrawals {
		w := &b.Withdrawals[i]
		if w.ID == "" {
			w.ID = uniqueID(w.Name, ids(b.Withdrawals, withdrawalKey))
		}
	}
}

// emptyLists makes every list field an empty list rather than nil, so a
// budget always encodes as [] and never as null (clients iterate them).
func emptyLists(b *Budget) {
	if b.Currencies == nil {
		b.Currencies = []CurrencyRate{}
	}
	if b.Categories == nil {
		b.Categories = []Category{}
	}
	if b.Lines == nil {
		b.Lines = []Line{}
	}
	if b.Debts == nil {
		b.Debts = []Debt{}
	}
	if b.Pots == nil {
		b.Pots = []Pot{}
	}
	if b.Withdrawals == nil {
		b.Withdrawals = []Withdrawal{}
	}
	if b.Scenarios == nil {
		b.Scenarios = []Scenario{}
	}
}

// UnmarshalJSON reads a budget, converting the pre-pots form (savings_start
// and goals) into pots, so every stored or sent document gets the same model.
// Unknown fields are errors.
func (b *Budget) UnmarshalJSON(data []byte) error {
	type plain Budget // no methods: avoids recursion
	var in struct {
		plain
		SavingsStart *money.Decimal `json:"savings_start"`
		Goals        []Goal         `json:"goals"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return err
	}
	*b = Budget(in.plain)
	defer emptyLists(b) // stored files from before pots have no "withdrawals"
	legacy := (in.SavingsStart != nil && in.SavingsStart.Sign() != 0) || len(in.Goals) > 0
	if b.Pots != nil {
		if legacy {
			return fmt.Errorf("savings_start and goals are replaced by pots; put the savings and targets in pots")
		}
		return nil
	}
	b.Pots = MigrateSavings(in.SavingsStart, in.Goals, &b.OverdraftStart)
	return nil
}

// MigrateSavings turns the old single savings balance and its goals into
// pots: a "General savings" pot holding the balance, and one pot per goal
// (an emergency fund if the name says so, otherwise a sinking fund). Old
// goals all counted the same money; as pots each fills on its own. A
// negative old balance becomes money owed (overdraft).
func MigrateSavings(start *money.Decimal, goals []Goal, overdraft *money.Decimal) []Pot {
	zero := money.MustDecimal("0")
	general := Pot{ID: "savings", Name: "General savings", Kind: PotGeneral, Balance: zero}
	if start != nil {
		if start.Sign() >= 0 {
			general.Balance = *start
		} else if overdraft.Sign() == 0 {
			*overdraft = money.MustDecimal(strings.TrimPrefix(start.String(), "-"))
		}
	}
	pots := []Pot{general}
	for _, g := range goals {
		kind := PotSinking
		if strings.Contains(strings.ToLower(g.Name), "emergency") {
			kind = PotEmergency
		}
		id := g.ID
		if id == "" || id == general.ID {
			id = uniqueID(g.Name, ids(pots, potKey))
		}
		pots = append(pots, Pot{ID: id, Name: g.Name, Kind: kind, Balance: zero, Target: g.Target})
	}
	return pots
}

// UnmarshalJSON makes an omitted schedule mean "monthly" (the common case)
// rather than the zero value, which would be a one-off in the first month.
func (l *Line) UnmarshalJSON(data []byte) error {
	type plain Line // no methods: avoids recursion
	var in struct {
		plain
		Schedule *Schedule `json:"schedule"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return err
	}
	*l = Line(in.plain)
	l.Schedule = Monthly()
	if in.Schedule != nil {
		l.Schedule = *in.Schedule
	}
	return nil
}

var (
	jsonInt    = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
	jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)
)

// yamlToJSON converts YAML to JSON while preserving scalar text. Numbers that
// are plain decimals stay JSON numbers (decoded exactly by money.Decimal and
// into ints); anything else (1e3, 0x10, .5) becomes a string so the decoder
// rejects it with a clear error instead of reinterpreting it.
func yamlToJSON(data []byte) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return []byte("{}"), nil
	}
	var buf bytes.Buffer
	nodes := 0
	if err := writeNode(&buf, root.Content[0], 0, &nodes); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeNode(buf *bytes.Buffer, n *yaml.Node, depth int, nodes *int) error {
	*nodes++
	if depth > 32 || *nodes > 100_000 {
		return fmt.Errorf("yaml document is too deep or too large")
	}
	switch n.Kind {
	case yaml.AliasNode:
		return writeNode(buf, n.Alias, depth+1, nodes)
	case yaml.MappingNode:
		buf.WriteByte('{')
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: mapping keys must be plain strings", k.Line)
			}
			if i > 0 {
				buf.WriteByte(',')
			}
			key, _ := json.Marshal(k.Value)
			buf.Write(key)
			buf.WriteByte(':')
			if err := writeNode(buf, n.Content[i+1], depth+1, nodes); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case yaml.SequenceNode:
		buf.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeNode(buf, c, depth+1, nodes); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case yaml.ScalarNode:
		switch {
		case n.Tag == "!!null":
			buf.WriteString("null")
		case n.Tag == "!!bool":
			buf.WriteString(strings.ToLower(n.Value))
		case n.Tag == "!!int" && jsonInt.MatchString(n.Value), n.Tag == "!!float" && jsonNumber.MatchString(n.Value):
			buf.WriteString(n.Value)
		default:
			s, _ := json.Marshal(n.Value)
			buf.Write(s)
		}
	default:
		return fmt.Errorf("line %d: unsupported yaml node", n.Line)
	}
	return nil
}
