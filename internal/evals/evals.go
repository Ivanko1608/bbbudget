// Package evals runs the assistant against the cases in evals/ and checks
// what it did. See evals/README.md.
package evals

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"bbbudget/internal/store"
)

// Script is a writing system a reply can be checked against.
type Script string

const (
	ScriptCyrillic Script = "cyrillic"
	ScriptLatin    Script = "latin"
)

// ItemField is a field of a budget item that a case can check.
type ItemField string

const (
	ItemAmount   ItemField = "amount"
	ItemCurrency ItemField = "currency"
	ItemCategory ItemField = "category"
	ItemName     ItemField = "name"
	ItemEvery    ItemField = "every"
	ItemStarts   ItemField = "starts"
)

// TransactionField is a field of a logged transaction that a case can check.
type TransactionField string

const (
	TransactionDate     TransactionField = "date"
	TransactionAmount   TransactionField = "amount"
	TransactionCurrency TransactionField = "currency"
	TransactionCategory TransactionField = "category"
	TransactionBase     TransactionField = "base"
)

const (
	defaultTemplate = "example"
	budgetID        = "home"
	budgetName      = "Home"
)

// Today is the fixed date every case runs on, so "this month" and dates in
// cases mean the same thing every time. The example budget starts that month.
var Today = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// Case is one eval: a conversation and what should be true after it.
type Case struct {
	Name     string   `yaml:"name"`
	Template string   `yaml:"template"`
	Turns    []string `yaml:"turns"`
	// Reference is, per turn, the tool calls a correct assistant could make.
	Reference [][]ReferenceCall `yaml:"reference"`
	// ReferenceReply is what the reference assistant says after its calls.
	ReferenceReply string `yaml:"reference_reply"`
	Expect         Expect `yaml:"expect"`
}

// ReferenceCall is one tool call in a reference solution.
type ReferenceCall struct {
	Tool  string         `yaml:"tool"`
	Input map[string]any `yaml:"input"`
}

// Expect are the checks; unset ones are skipped.
type Expect struct {
	Saved         *bool                           `yaml:"saved"`
	WritesBy      store.Source                    `yaml:"writes_by"`
	Items         map[string]map[ItemField]string `yaml:"items"`
	NoItemNamed   []string                        `yaml:"no_item_named"`
	Transactions  *int                            `yaml:"transactions"`
	Transaction   map[TransactionField]string     `yaml:"transaction"`
	ReplyScript   Script                          `yaml:"reply_script"`
	ReplyContains []string                        `yaml:"reply_contains"`
	ReplyOmits    []string                        `yaml:"reply_omits"`
}

// Load reads every case in dir, sorted by file name.
func Load(dir string) ([]Case, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	slices.Sort(files)
	cases := make([]Case, 0, len(files))
	for _, file := range files {
		loaded, err := loadCase(file)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		cases = append(cases, loaded)
	}
	return cases, nil
}

func loadCase(file string) (Case, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return Case{}, err
	}
	var loaded Case
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&loaded); err != nil {
		return Case{}, err
	}
	if len(loaded.Turns) == 0 {
		return Case{}, errors.New("no turns")
	}
	if len(loaded.Reference) != len(loaded.Turns) {
		return Case{}, fmt.Errorf("reference needs one list of calls per turn (%d turns, %d lists)",
			len(loaded.Turns), len(loaded.Reference))
	}
	if loaded.Name == "" {
		loaded.Name = strings.TrimSuffix(filepath.Base(file), ".yaml")
	}
	if loaded.Template == "" {
		loaded.Template = defaultTemplate
	}
	return loaded, nil
}
