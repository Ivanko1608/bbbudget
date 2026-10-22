package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bbbudget/internal/budget"
)

var start = time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)

func TestBuiltinsAreValid(t *testing.T) {
	r := Builtin()
	if got := strings.Join(r.Names(), ","); got != "blank,example" {
		t.Fatalf("builtins: %s", got)
	}
	for _, name := range r.Names() {
		b, err := r.New(name, "Mine", start)
		if err != nil {
			t.Fatal(err)
		}
		if b.Name != "Mine" || b.StartMonth != "2026-10" {
			t.Errorf("%s: %s %s", name, b.Name, b.StartMonth)
		}
		if _, err := budget.Analyze(b); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if b, _ := r.New("example", "", start); b.Name != "Example household" {
		t.Errorf("template name not kept: %q", b.Name)
	}
	if b, _ := r.New("blank", "", start); b.Name != "blank" {
		t.Errorf("unnamed template should fall back to its name: %q", b.Name)
	}
	if _, err := r.New("nope", "", start); err == nil {
		t.Error("unknown template accepted")
	}
}

func TestBuiltinSourceIsItsEmbeddedFile(t *testing.T) {
	r := Builtin()

	sources := map[string]string{}
	for _, tpl := range r.List() {
		sources[tpl.Name] = tpl.Source
	}

	if got := sources["example"]; got != filepath.Join("builtin", "example.yaml") {
		t.Errorf("example source: %q", got)
	}
}

func TestUserDirectory(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("mine.yaml", "description: My own\nbase_currency: GBP\ncategories: [{label: Pay, kind: income}, {label: Living}]\nitems: [{name: Pay, amount: 2500, category: pay}]\n")
	write("blank.json", `{"description":"my blank","base_currency":"USD","categories":[{"label":"Stuff"}]}`)
	write("notes.txt", "ignored")

	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.Names(), ","); got != "blank,example,mine" {
		t.Fatalf("names: %s", got)
	}
	b, _ := r.New("blank", "", start)
	if b.BaseCurrency != "USD" {
		t.Error("user template should override the built-in")
	}
	for _, tpl := range r.List() {
		if tpl.Name == "mine" && tpl.Source != filepath.Join(dir, "mine.yaml") {
			t.Errorf("source: %s", tpl.Source)
		}
	}

	// A broken file is reported by name, not skipped silently.
	write("broken.yaml", "base_currency: EUR\nitems: [{name: x, amount: 1.001, category: nope}]\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "broken.yaml") {
		t.Fatalf("broken template: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "broken.yaml")); err != nil {
		t.Fatal(err)
	}
	write("Bad Name.yaml", "base_currency: EUR\ncategories: [{label: A}]\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("bad file name accepted")
	}

	if _, err := Load(filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("a missing directory is fine: %v", err)
	}
}
