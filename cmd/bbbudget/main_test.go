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

	"bbbudget/internal/assistant"
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

func requireContains(t *testing.T, what, out, want string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Fatalf("%s: output lacks %q:\n%s", what, want, out)
	}
}

func requireRunning(t *testing.T, what string, err error, out string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v\n%s", what, err, out)
	}
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
	requireRunning(t, "list", err, out)
	requireContains(t, "list", out, "home")
	out, err = runCLI(t, "report", "-dir", dir, "home")
	requireRunning(t, "report", err, out)
	requireContains(t, "report", out, "€10,409.88")
	// Flags may follow positionals; YAML imports keep exact amounts.
	file := filepath.Join(t.TempDir(), "mine.yaml")
	mine := "name: Mine\nbase_currency: GBP\n" +
		"categories: [{label: Pay, kind: income}, {label: Living}]\n" +
		"items:\n" +
		"  - {name: Pay, amount: 2000.10, category: pay}\n" +
		"  - {name: Rent, amount: 950, category: living}\n"
	os.WriteFile(file, []byte(mine), 0o600)
	if out, err := runCLI(t, "import", "mine", file, "-dir", dir); err != nil {
		t.Fatalf("import: %v %s", err, out)
	}
	out, err = runCLI(t, "report", "mine", "-dir", dir)
	requireRunning(t, "report after import", err, out)
	requireContains(t, "report after import", out, "£1,050.10")
	tplDir := t.TempDir()
	starter := "description: My starter\nbase_currency: EUR\ncategories: [{label: Living}]\n"
	os.WriteFile(filepath.Join(tplDir, "starter.yaml"), []byte(starter), 0o600)
	out, err = runCLI(t, "templates", "-templates", tplDir)
	requireRunning(t, "templates", err, out)
	requireContains(t, "templates", out, "starter")
	requireContains(t, "templates", out, "example")
	initStarter := []string{"init", "s", "-template", "starter", "-templates", tplDir, "-dir", dir}
	if out, err := runCLI(t, initStarter...); err != nil {
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
	initHome := []string{"init", "home", "-template", "example", "-dir", dir, "-settings", conf}
	if out, err := runCLI(t, initHome...); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	out, err := runCLI(t, "settings", "-settings", conf)
	requireRunning(t, "defaults", err, out)
	requireContains(t, "defaults", out, "default_rate_source  auto")
	requireContains(t, "defaults", out, "coingecko_api_key    not set")
	out, err = runCLI(t, "settings", "-settings", conf,
		"-set", "auto_refresh=daily",
		"-set", "fetch_rates=false",
		"-set", "coingecko_api_key=CG-x")
	requireRunning(t, "set", err, out)
	requireContains(t, "set", out, "auto_refresh         daily")
	requireContains(t, "set", out, "fetch_rates          false")
	requireContains(t, "set", out, "coingecko_api_key    set")
	if strings.Contains(out, "CG-x") {
		t.Fatalf("the key must not be printed:\n%s", out)
	}
	raw, _ := os.ReadFile(conf)
	if !strings.Contains(string(raw), `"auto_refresh": "daily"`) {
		t.Fatalf("not saved:\n%s", raw)
	}
	badSettings := [][]string{
		{"-set", "auto_refresh=weekly"},
		{"-set", "colour=blue"},
		{"-set", "fetch_rates=maybe"},
		{"-set", "novalue"},
		{"extra"},
	}
	for _, bad := range badSettings {
		args := append([]string{"settings", "-settings", conf}, bad...)
		if _, err := runCLI(t, args...); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}

	// Rates: nothing to do while every rate is manual; offline mode refuses a fetch
	// without touching the network.
	out, err = runCLI(t, "rates", "home", "-dir", dir, "-settings", conf)
	requireRunning(t, "all manual", err, out)
	requireContains(t, "all manual", out, "every rate is manual")
	out, err = runCLI(t, "rates", "home", "-only", "usd", "-dir", dir, "-settings", conf)
	if err == nil {
		t.Fatalf("offline refresh succeeded: %s", out)
	}
	if !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("offline: %v\n%s", err, out)
	}
	offlineDryRun := []string{
		"rates", "home", "-only", "usd", "-dry-run", "-dir", dir, "-settings", conf,
	}
	if out, err := runCLI(t, offlineDryRun...); err == nil {
		t.Fatalf("offline dry run: %s", out)
	}
	// Dry runs check currencies exactly like real runs.
	unknownDryRun := []string{
		"rates", "home", "-only", "XYZ", "-dry-run", "-dir", dir, "-settings", conf,
	}
	if out, err := runCLI(t, unknownDryRun...); err == nil {
		t.Fatalf("dry run accepted an unknown currency: %s", out)
	}

	// A broken settings file is reported, not silently replaced by defaults.
	os.WriteFile(conf, []byte(`{"auto_refresh": "weekly"}`), 0o600)
	_, err = runCLI(t, "list", "-dir", dir, "-settings", conf)
	if err == nil {
		t.Fatal("broken settings file accepted")
	}
	if !strings.Contains(err.Error(), conf) {
		t.Fatalf("error does not name the settings file: %v", err)
	}
}

// TestStdioMCP builds the real binary and talks to it the way Claude does.
func TestStdioMCP(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	// inherited by the binary
	t.Setenv("BBBUDGET_SETTINGS", filepath.Join(t.TempDir(), "settings.json"))
	t.Setenv("BBBUDGET_TEMPLATES", t.TempDir())
	bin := filepath.Join(t.TempDir(), "bbbudget")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir := t.TempDir()
	initHome := exec.Command(bin, "init", "home", "-template", "example", "-dir", dir)
	if out, err := initHome.CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	ctx := context.Background()
	cmd := exec.Command(bin, "mcp", "-dir", dir, "-budget", "home")
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e"}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	if cs.InitializeResult().Instructions == "" {
		t.Error("server instructions missing")
	}
	rentTo1200 := &mcp.CallToolParams{
		Name:      "update_item",
		Arguments: map[string]any{"item": "rent", "amount": "1200"},
	}
	res, err := cs.CallTool(ctx, rentTo1200)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("update_item failed: %+v", res)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "amount 1150 → 1200") {
		t.Errorf("the change is missing: %s", text)
	}
	if !strings.Contains(text, `"end_savings"`) {
		t.Errorf("the effect on end savings is missing: %s", text)
	}
	// The change is on disk for every other process (e.g. the web server).
	out, _ := exec.Command(bin, "report", "home", "-dir", dir).CombinedOutput()
	// rent 1200 CHF = 1276.60/month (was 1223.41): 12 × 53.19 = 638.28 less saved
	if !strings.Contains(string(out), "€9,771.60") {
		t.Fatalf("report after MCP edit:\n%s", out)
	}
}

func TestTheAssistantIsSetUpFromTheCLI(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "settings.json")

	out, err := runCLI(t, "settings", "-settings", conf,
		"-set", "assistant_provider=anthropic",
		"-set", "assistant_api_key=sk-secret")

	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !strings.Contains(out, "assistant_provider   anthropic  ready") {
		t.Errorf("provider not shown as ready:\n%s", out)
	}
	if !strings.Contains(out, "assistant_model      "+assistant.DefaultAnthropicModel) {
		t.Errorf("default model not shown:\n%s", out)
	}
	if strings.Contains(out, "sk-secret") {
		t.Errorf("the key was printed:\n%s", out)
	}
}
