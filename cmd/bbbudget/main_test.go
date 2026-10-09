package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runCLI runs a command in-process. Tests never read the user's own settings.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	// Keep commands that omit -dir, -templates or -settings out of the real home
	// (which is unwritable in sandboxed builds such as Nix).
	if os.Getenv("BBBUDGET_SETTINGS") == "" {
		t.Setenv("BBBUDGET_SETTINGS", filepath.Join(t.TempDir(), "settings.json"))
	}
	if os.Getenv("BBBUDGET_DIR") == "" {
		t.Setenv("BBBUDGET_DIR", t.TempDir())
	}
	if os.Getenv("BBBUDGET_TEMPLATES") == "" {
		t.Setenv("BBBUDGET_TEMPLATES", t.TempDir())
	}
	var out, errb bytes.Buffer
	err := run(context.Background(), args, &out, &errb)
	return out.String() + errb.String(), err
}

func TestCLI(t *testing.T) {
	dir := t.TempDir()
	if out, err := runCLI(t, "init", "home", "-template", "example", "-dir", dir); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := runCLI(t, "init", "home", "-dir", dir); err == nil {
		t.Fatal("duplicate init succeeded")
	}
	out, err := runCLI(t, "list", "-dir", dir)
	if err != nil || !strings.Contains(out, "home") {
		t.Fatalf("list: %v %s", err, out)
	}
	out, err = runCLI(t, "report", "-dir", dir, "home")
	if err != nil || !strings.Contains(out, "€10,409.88") {
		t.Fatalf("report: %v %s", err, out)
	}
	// Flags may follow positionals; YAML imports keep exact amounts.
	file := filepath.Join(t.TempDir(), "mine.yaml")
	os.WriteFile(file, []byte("name: Mine\nbase_currency: GBP\ncategories: [{label: Pay, kind: income}, {label: Living}]\nlines:\n  - {name: Pay, amount: 2000.10, category: pay}\n  - {name: Rent, amount: 950, category: living}\n"), 0o600)
	if out, err := runCLI(t, "import", "mine", file, "-dir", dir); err != nil {
		t.Fatalf("import: %v %s", err, out)
	}
	out, err = runCLI(t, "report", "mine", "-dir", dir)
	if err != nil || !strings.Contains(out, "£1,050.10") {
		t.Fatalf("report after import: %v %s", err, out)
	}
	tplDir := t.TempDir()
	os.WriteFile(filepath.Join(tplDir, "starter.yaml"), []byte("description: My starter\nbase_currency: EUR\ncategories: [{label: Living}]\n"), 0o600)
	out, err = runCLI(t, "templates", "-templates", tplDir)
	if err != nil || !strings.Contains(out, "starter") || !strings.Contains(out, "example") {
		t.Fatalf("templates: %v %s", err, out)
	}
	if out, err := runCLI(t, "init", "s", "-template", "starter", "-templates", tplDir, "-dir", dir); err != nil {
		t.Fatalf("init from user template: %v %s", err, out)
	}

	if _, err := runCLI(t, "frobnicate"); err == nil {
		t.Fatal("unknown command accepted")
	}
	if _, err := runCLI(t, "report", "-dir", dir); err == nil {
		t.Fatal("report without id accepted")
	}
}

func TestSettingsAndRatesCLI(t *testing.T) {
	dir, conf := t.TempDir(), filepath.Join(t.TempDir(), "settings.json")
	if out, err := runCLI(t, "init", "home", "-template", "example", "-dir", dir, "-settings", conf); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	out, err := runCLI(t, "settings", "-settings", conf)
	if err != nil || !strings.Contains(out, "default_rate_source  auto") || !strings.Contains(out, "coingecko_api_key    not set") {
		t.Fatalf("defaults: %v\n%s", err, out)
	}
	out, err = runCLI(t, "settings", "-settings", conf, "-set", "auto_refresh=daily", "-set", "fetch_rates=false", "-set", "coingecko_api_key=CG-x")
	if err != nil || !strings.Contains(out, "auto_refresh         daily") || !strings.Contains(out, "fetch_rates          false") {
		t.Fatalf("set: %v\n%s", err, out)
	}
	if strings.Contains(out, "CG-x") || !strings.Contains(out, "coingecko_api_key    set") {
		t.Fatalf("the key must not be printed:\n%s", out)
	}
	raw, _ := os.ReadFile(conf)
	if !strings.Contains(string(raw), `"auto_refresh": "daily"`) {
		t.Fatalf("not saved:\n%s", raw)
	}
	for _, bad := range [][]string{{"-set", "auto_refresh=weekly"}, {"-set", "colour=blue"}, {"-set", "fetch_rates=maybe"}, {"-set", "novalue"}, {"extra"}} {
		if _, err := runCLI(t, append([]string{"settings", "-settings", conf}, bad...)...); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}

	// Rates: nothing to do while every rate is manual; offline mode refuses a fetch
	// without touching the network.
	out, err = runCLI(t, "rates", "home", "-dir", dir, "-settings", conf)
	if err != nil || !strings.Contains(out, "every rate is manual") {
		t.Fatalf("all manual: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "rates", "home", "-only", "usd", "-dir", dir, "-settings", conf); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("offline: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "rates", "home", "-only", "usd", "-dry-run", "-dir", dir, "-settings", conf); err == nil {
		t.Fatalf("offline dry run: %s", out)
	}
	// Dry runs check currencies exactly like real runs.
	if out, err := runCLI(t, "rates", "home", "-only", "XYZ", "-dry-run", "-dir", dir, "-settings", conf); err == nil {
		t.Fatalf("dry run accepted an unknown currency: %s", out)
	}

	// A broken settings file is reported, not silently replaced by defaults.
	os.WriteFile(conf, []byte(`{"auto_refresh": "weekly"}`), 0o600)
	if _, err := runCLI(t, "list", "-dir", dir, "-settings", conf); err == nil || !strings.Contains(err.Error(), conf) {
		t.Fatalf("broken settings file: %v", err)
	}
}

// TestStdioMCP builds the real binary and talks to it the way Claude does.
func TestStdioMCP(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	t.Setenv("BBBUDGET_SETTINGS", filepath.Join(t.TempDir(), "settings.json")) // inherited by the binary
	bin := filepath.Join(t.TempDir(), "bbbudget")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir := t.TempDir()
	if out, err := exec.Command(bin, "init", "home", "-template", "example", "-dir", dir).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	ctx := context.Background()
	cmd := exec.Command(bin, "mcp", "-dir", dir, "-budget", "home")
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "e2e"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	if cs.InitializeResult().Instructions == "" {
		t.Error("server instructions missing")
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "update_line", Arguments: map[string]any{"line": "rent", "amount": "1200"}})
	if err != nil || res.IsError {
		t.Fatalf("update_line: %v %+v", err, res)
	}
	if !strings.Contains(res.Content[0].(*mcp.TextContent).Text, `"amount": "1200.00"`) {
		t.Fatal("update not reflected")
	}
	// The change is on disk for every other process (e.g. the web server).
	out, _ := exec.Command(bin, "report", "home", "-dir", dir).CombinedOutput()
	// rent 1200 CHF = 1276.60/month (was 1223.41): 12 × 53.19 = 638.28 less saved
	if !strings.Contains(string(out), "€9,771.60") {
		t.Fatalf("report after MCP edit:\n%s", out)
	}
}
