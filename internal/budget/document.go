package budget

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

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
// ids derived from names, the base currency on items and debts, the default
// debt strategy and a 12-month window. It never changes a value that was set.
func Normalize(b *Budget) {
	if b.Months == 0 {
		b.Months = 12
	}
	if b.DebtStrategy == "" {
		b.DebtStrategy = StrategyAvalanche
	}
	if b.Currencies == nil {
		b.Currencies = []CurrencyRate{}
	}
	if b.Categories == nil {
		b.Categories = []Category{}
	}
	if b.Items == nil {
		b.Items = []Item{}
	}
	if b.Debts == nil {
		b.Debts = []Debt{}
	}
	if b.Goals == nil {
		b.Goals = []Goal{}
	}
	if b.Scenarios == nil {
		b.Scenarios = []Scenario{}
	}
	for i := range b.Categories {
		c := &b.Categories[i]
		if c.ID == "" {
			c.ID = uniqueID(c.Label, ids(b.Categories, func(c Category) (string, string) { return c.ID, c.Label }))
		}
		if c.Kind == "" {
			c.Kind = KindExpense
		}
	}
	for i := range b.Items {
		l := &b.Items[i]
		if l.ID == "" {
			l.ID = uniqueID(l.Name, ids(b.Items, itemKey))
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
	for i := range b.Goals {
		if b.Goals[i].ID == "" {
			b.Goals[i].ID = uniqueID(b.Goals[i].Name, ids(b.Goals, goalKey))
		}
	}
}

// UnmarshalJSON reads a budget strictly (unknown fields are errors) and
// accepts "lines", the name "items" had before it was renamed, so budgets
// saved by older versions still load. They are written back as "items".
func (b *Budget) UnmarshalJSON(data []byte) error {
	type plain Budget // no methods: avoids recursion
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if old, ok := fields["lines"]; ok {
		if _, both := fields["items"]; both {
			return errors.New(`a budget has "items" and its old name "lines"; keep one`)
		}
		delete(fields, "lines")
		fields["items"] = old
		var err error
		if data, err = json.Marshal(fields); err != nil {
			return err
		}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode((*plain)(b))
}

// UnmarshalJSON makes an omitted schedule mean "monthly" (the common case)
// rather than the zero value, which would be a one-off in the first month.
func (l *Item) UnmarshalJSON(data []byte) error {
	type plain Item // no methods: avoids recursion
	var in struct {
		plain
		Schedule *Schedule `json:"schedule"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return err
	}
	*l = Item(in.plain)
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
