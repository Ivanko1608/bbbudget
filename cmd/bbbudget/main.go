// Command bbbudget plans multi-currency household budgets.
//
//	bbbudget serve            web UI + JSON API (+ MCP at /mcp) on localhost
//	bbbudget mcp              MCP server on stdio, for Claude Desktop / Claude Code
//	bbbudget init <id>        create a budget from a template
//	bbbudget list             list budgets
//	bbbudget report <id>      print a budget's projection and insights
//	bbbudget rates <id>       fetch current exchange rates into a budget
//	bbbudget log <id> <amount> <category>   record money actually spent or received
//	bbbudget status <id>      this month's plan versus actual
//	bbbudget history <id>     list recent changes and who made them
//	bbbudget undo <id>        undo a change
//	bbbudget settings         show or change app settings
//	bbbudget pair             make a code that pairs a phone with `serve -remote`
//	bbbudget devices          list or revoke paired devices
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/assistant"
	"bbbudget/internal/budget"
	"bbbudget/internal/httpapi"
	"bbbudget/internal/i18n"
	"bbbudget/internal/mcpserver"
	"bbbudget/internal/money"
	"bbbudget/internal/settings"
	"bbbudget/internal/store"
	"bbbudget/internal/templates"
	"bbbudget/web"
)

const timeLayout = "2006-01-02 15:04"

var version = "dev" // set with -ldflags "-X main.version=..."

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "bbbudget:", err)
		os.Exit(1)
	}
}

const usage = `usage: bbbudget <command> [flags]

commands:
  serve    web UI and JSON API on localhost (MCP over HTTP at /mcp)
  mcp      MCP server on stdin/stdout
  init     create a budget:  bbbudget init home -template example
  import   create a budget from a file:  bbbudget import home my-budget.yaml
  templates  list templates (built-in and yours)
  list     list budgets
  report   print a projection:  bbbudget report home [-scenario name]
  rates    fetch current exchange rates:  bbbudget rates home [-only USD,BTC] [-dry-run]
  settings show or change app settings:  bbbudget settings [-set auto_refresh=daily ...]
  log      record money spent or received:  bbbudget log home 40 home [-date 2026-10-03] ` +
	`[-currency USD] [-note "Lidl"]
  status   this month's plan versus actual:  bbbudget status home [-month 2026-10]
  history  list recent changes and who made them:  bbbudget history home [-limit 20]
  undo     undo a change:  bbbudget undo home [-revision REV] [-force]
  pair     make a code that pairs a phone or computer with serve -remote
  devices  list paired devices:  bbbudget devices [-revoke ID]
  version  print the version

Data lives in $BBBUDGET_DIR (default ~/.local/share/bbbudget); every command takes -dir.
Your templates live in $BBBUDGET_TEMPLATES (default ~/.config/bbbudget/templates); ` +
	`-templates overrides.
App settings live in $BBBUDGET_SETTINGS (default ~/.config/bbbudget/settings.json); ` +
	`-settings overrides.
Paired devices live in devices.json next to the settings file.
`

// fromCLI is the origin of every write made by a command.
var fromCLI = store.Origin{Source: store.SourceCLI}

type cli struct {
	flags        *flag.FlagSet
	stdout       io.Writer
	logger       *slog.Logger
	dir          *string
	templateDir  *string
	settingsFile *string
}

type command func(ctx context.Context, c *cli, args []string) error

var commands = map[string]command{
	"serve":     runServe,
	"mcp":       runMCP,
	"init":      runInit,
	"templates": runTemplates,
	"import":    runImport,
	"list":      runList,
	"report":    runReport,
	"rates":     runRates,
	"log":       runLog,
	"status":    runStatus,
	"history":   runHistory,
	"undo":      runUndo,
	"settings":  runSettings,
	"pair":      runPair,
	"devices":   runManageDevices,
	"version":   runVersion,
	"help":      runHelp,
	"-h":        runHelp,
	"--help":    runHelp,
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("missing command")
	}
	name, args := args[0], args[1:]
	runCommand, ok := commands[name]
	if !ok {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", name)
	}
	return runCommand(ctx, newCLI(name, stdout, stderr), args)
}

func newCLI(name string, stdout, stderr io.Writer) *cli {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return &cli{
		flags:  flags,
		stdout: stdout,
		logger: slog.New(slog.NewTextHandler(stderr, nil)),
		dir:    flags.String("dir", store.DefaultDir(), "data directory"),
		templateDir: flags.String(
			"templates",
			templates.DefaultDir(),
			"directory of your own templates (*.yaml, *.json)",
		),
		settingsFile: flags.String("settings", settings.DefaultPath(), "app settings file"),
	}
}

func (c *cli) openApp() (*app.App, error) {
	return openApp(*c.dir, *c.templateDir, *c.settingsFile)
}

func openApp(dir, templateDir, settingsFile string) (*app.App, error) {
	st, err := store.NewFileStore(dir)
	if err != nil {
		return nil, err
	}
	registry, err := templates.Load(templateDir)
	if err != nil {
		return nil, err
	}
	a := app.New(st, registry)
	a.SettingsPath = settingsFile
	if _, err := a.Settings(); err != nil {
		return nil, err
	}
	return a, nil
}

func runServe(ctx context.Context, c *cli, args []string) error {
	addr := c.flags.String("addr", "127.0.0.1:8080", "listen address")
	withMCP := c.flags.Bool("mcp", true, "also serve MCP (streamable HTTP) at /mcp")
	defaultBudget := c.flags.String(
		"budget", "", "default budget for MCP tool calls that omit one",
	)
	hosts := c.flags.String(
		"allow-host", "", "extra Host names to accept, comma-separated (e.g. when proxied)",
	)
	remote := c.flags.Bool(
		"remote", false,
		"answer paired devices, accepting any Host header; put HTTPS in front (see bbbudget pair)",
	)
	if err := c.flags.Parse(args); err != nil {
		return err
	}
	a, err := c.openApp()
	if err != nil {
		return err
	}
	opts := httpapi.Options{
		Static: web.FS(),
		Log:    c.logger,
		Paths: map[string]string{
			"data":      *c.dir,
			"templates": *c.templateDir,
			"settings":  *c.settingsFile,
		},
		Assistant: assistant.New(a, assistant.ProviderFromSettings(a)),
	}
	go a.RunAutoRefresh(ctx, c.logger)
	if *hosts != "" {
		opts.AllowedHosts = strings.Split(*hosts, ",")
	}
	if *remote {
		if opts.Devices, err = openDevices(*c.settingsFile); err != nil {
			return err
		}
		fmt.Fprintln(
			c.stdout, "remote mode: only paired devices can use the API; pair one with `bbbudget pair`",
		)
	}
	if *withMCP {
		opts.MCP = newMCPHandler(mcpserver.New(a, mcpOptions(*defaultBudget)))
	}
	return serve(ctx, *addr, httpapi.New(a, opts), c.logger, c.stdout)
}

func mcpOptions(defaultBudget string) mcpserver.Options {
	return mcpserver.Options{DefaultBudget: defaultBudget, Version: version}
}

func newMCPHandler(srv *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
}

func runMCP(ctx context.Context, c *cli, args []string) error {
	defaultBudget := c.flags.String(
		"budget",
		os.Getenv("BBBUDGET_DEFAULT"),
		"default budget for tool calls that omit one",
	)
	if err := c.flags.Parse(args); err != nil {
		return err
	}
	a, err := c.openApp()
	if err != nil {
		return err
	}
	c.logger.Info("mcp server on stdio", "dir", *c.dir, "default_budget", *defaultBudget)
	srv := mcpserver.New(a, mcpOptions(*defaultBudget))
	return srv.Run(ctx, &mcp.StdioTransport{})
}

func runInit(ctx context.Context, c *cli, args []string) error {
	template := c.flags.String("template", "blank", "template name (see: bbbudget templates)")
	name := c.flags.String("name", "", "display name (default: the id)")
	id, err := parseFlagsAndID(c.flags, args, "init <id> [-template name] [-name display-name]")
	if err != nil {
		return err
	}
	a, err := c.openApp()
	if err != nil {
		return err
	}
	v, err := a.Create(ctx, fromCLI, a.SettingsLocalizer(), id, *name, *template)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "created %q (%s) in %s\n", v.ID, v.Budget.Name, *c.dir)
	return nil
}

func runTemplates(_ context.Context, c *cli, args []string) error {
	if err := c.flags.Parse(args); err != nil {
		return err
	}
	registry, err := templates.Load(*c.templateDir)
	if err != nil {
		return err
	}
	table := newTable(c.stdout)
	fmt.Fprintln(table, "NAME\tSOURCE\tDESCRIPTION")
	for _, t := range registry.List() {
		fmt.Fprintf(table, "%s\t%s\t%s\n", t.Name, t.Source, t.Description)
	}
	fmt.Fprintf(table, "\nAdd your own as *.yaml or *.json in %s\n", *c.templateDir)
	return table.Flush()
}

func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
}

func runImport(ctx context.Context, c *cli, args []string) error {
	pos, err := parseArgs(c.flags, args, 2, "import <id> <file.yaml|file.json>")
	if err != nil {
		return err
	}
	id, file := pos[0], pos[1]
	b, err := readBudgetFile(file)
	if err != nil {
		return err
	}
	a, err := c.openApp()
	if err != nil {
		return err
	}
	if b.StartMonth == "" {
		b.StartMonth = a.Now().Format("2006-01")
	}
	if b.Name == "" {
		b.Name = id
	}
	v, err := a.Import(ctx, fromCLI, id, b)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "imported %q (%s)\n", v.ID, v.Budget.Name)
	return nil
}

func readBudgetFile(file string) (budget.Budget, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return budget.Budget{}, err
	}
	b, err := budget.ParseDocument(data, strings.TrimPrefix(filepath.Ext(file), "."))
	if err != nil {
		return budget.Budget{}, fmt.Errorf("%s: %w", file, err)
	}
	return b, nil
}

func runList(ctx context.Context, c *cli, args []string) error {
	if err := c.flags.Parse(args); err != nil {
		return err
	}
	a, err := c.openApp()
	if err != nil {
		return err
	}
	metas, err := a.List(ctx)
	if err != nil {
		return err
	}
	table := newTable(c.stdout)
	fmt.Fprintln(table, "ID\tNAME\tREVISION\tUPDATED")
	for _, m := range metas {
		updated := m.UpdatedAt.Local().Format(timeLayout)
		fmt.Fprintf(table, "%s\t%s\t%d\t%s\n", m.ID, m.Name, m.Revision, updated)
	}
	return table.Flush()
}

func runReport(ctx context.Context, c *cli, args []string) error {
	scenario := c.flags.String("scenario", "", "show a saved scenario instead of the budget")
	id, err := parseFlagsAndID(c.flags, args, "report <id> [-scenario name]")
	if err != nil {
		return err
	}
	a, err := c.openApp()
	if err != nil {
		return err
	}
	report, err := getReport(ctx, a, id, *scenario)
	if err != nil {
		return err
	}
	reader := a.SettingsLocalizer()
	report.Localize(reader)
	printReport(c.stdout, report, reader)
	return nil
}

func getReport(ctx context.Context, a *app.App, id, scenario string) (*budget.Report, error) {
	if scenario != "" {
		return a.ScenarioReport(ctx, id, scenario)
	}
	v, err := a.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return v.Report, nil
}

func runRates(ctx context.Context, c *cli, args []string) error {
	only := c.flags.String(
		"only",
		"",
		"comma-separated currencies to refresh (default: all whose source isn't manual)",
	)
	dryRun := c.flags.Bool("dry-run", false, "show current rates without saving")
	id, err := parseFlagsAndID(c.flags, args, "rates <id> [-only USD,BTC] [-dry-run]")
	if err != nil {
		return err
	}
	a, err := c.openApp()
	if err != nil {
		return err
	}
	var updates []app.RateUpdate
	codes := parseCurrencyCodes(*only)
	if *dryRun {
		updates, err = a.PreviewRates(ctx, id, codes)
	} else {
		_, updates, err = a.RefreshRates(ctx, fromCLI, id, codes)
	}
	printRates(c.stdout, updates)
	if err == nil && len(updates) == 0 {
		fmt.Fprintln(c.stdout, "no currencies to refresh: every rate is manual "+
			"(name some with -only to fetch them)")
	}
	return err
}

func parseCurrencyCodes(list string) []string {
	if list == "" {
		return nil
	}
	return strings.Split(strings.ToUpper(strings.ReplaceAll(list, " ", "")), ",")
}

func runLog(ctx context.Context, c *cli, args []string) error {
	date := c.flags.String("date", "", "day it happened, YYYY-MM-DD (default today)")
	currency := c.flags.String("currency", "", "currency (default the base currency)")
	note := c.flags.String("note", "", "a note")
	item := c.flags.String("item", "", "the planned item it pays (id or name)")
	usage := "log <id> <amount> <category> " +
		"[-date YYYY-MM-DD] [-currency CODE] [-note text] [-item ref]"
	pos, err := parseArgs(c.flags, args, 3, usage)
	if err != nil {
		return err
	}
	amount, err := money.ParseDecimal(pos[1])
	if err != nil {
		return fmt.Errorf("amount: %w", err)
	}
	a, err := c.openApp()
	if err != nil {
		return err
	}
	if *date == "" {
		*date = a.Now().Format(time.DateOnly)
	}
	in := budget.TransactionInput{
		Date:     *date,
		Amount:   amount,
		Currency: *currency,
		Category: pos[2],
		Note:     *note,
		Item:     *item,
	}
	return logTransaction(ctx, c.stdout, a, pos[0], in)
}

func runStatus(ctx context.Context, c *cli, args []string) error {
	month := c.flags.String("month", "", "YYYY-MM (default this month)")
	id, err := parseFlagsAndID(c.flags, args, "status <id> [-month YYYY-MM]")
	if err != nil {
		return err
	}
	a, err := c.openApp()
	if err != nil {
		return err
	}
	status, err := a.MonthStatus(ctx, id, *month)
	if err != nil {
		return err
	}
	printStatus(c.stdout, status, a.SettingsLocalizer())
	return nil
}

func runHistory(ctx context.Context, c *cli, args []string) error {
	limit := c.flags.Int("limit", store.DefaultHistoryPage, "how many changes to show (0 for all)")
	id, err := parseFlagsAndID(c.flags, args, "history <id> [-limit 20]")
	if err != nil {
		return err
	}
	if *limit < 0 {
		return fmt.Errorf("-limit %d: must be a whole number", *limit)
	}
	a, err := c.openApp()
	if err != nil {
		return err
	}
	entries, err := a.History(ctx, id, *limit)
	if err != nil {
		return err
	}
	app.LocalizeHistory(entries, a.SettingsLocalizer())
	table := newTable(c.stdout)
	fmt.Fprintln(table, "REV\tWHEN\tBY\tCHANGE")
	for _, e := range entries {
		when := e.At.Local().Format(timeLayout)
		fmt.Fprintf(table, "%d\t%s\t%s\t%s\n", e.Revision, when, e.Source, e.Summary)
	}
	return table.Flush()
}

func runUndo(ctx context.Context, c *cli, args []string) error {
	revision := c.flags.Int64("revision", 0, "revision of the change to undo (default: the latest)")
	force := c.flags.Bool("force", false, "also undo every change made after it")
	id, err := parseFlagsAndID(c.flags, args, "undo <id> [-revision REV] [-force]")
	if err != nil {
		return err
	}
	a, err := c.openApp()
	if err != nil {
		return err
	}
	v, err := a.Undo(ctx, fromCLI, id, app.UndoRequest{Revision: *revision, Force: *force})
	if err != nil {
		return err
	}
	entries, err := a.History(ctx, id, 1)
	if err != nil {
		return err
	}
	app.LocalizeHistory(entries, a.SettingsLocalizer())
	fmt.Fprintf(c.stdout, "r%d: %s\n", v.Revision, entries[0].Summary)
	return nil
}

func runSettings(_ context.Context, c *cli, args []string) error {
	var sets []string
	setKeys := "fetch_rates, default_rate_source, auto_refresh, coingecko_api_key, locale, " +
		"assistant_provider, assistant_model, assistant_url, assistant_api_key"
	c.flags.Func("set", "key=value to change (repeatable): "+setKeys, func(v string) error {
		sets = append(sets, v)
		return nil
	})
	if _, err := parseArgs(c.flags, args, 0, "settings [-set key=value ...]"); err != nil {
		return err
	}
	a, err := c.openApp()
	if err != nil {
		return err
	}
	if err := updateSettings(a, sets); err != nil {
		return err
	}
	current, err := a.Settings()
	if err != nil {
		return err
	}
	return printSettings(c.stdout, a, current, *c.settingsFile)
}

func updateSettings(a *app.App, sets []string) error {
	if len(sets) == 0 {
		return nil
	}
	patch, err := settingsPatch(sets)
	if err != nil {
		return err
	}
	_, err = a.UpdateSettings(patch)
	return err
}

func printSettings(w io.Writer, a *app.App, s settings.Settings, file string) error {
	locale := cmp.Or(s.Locale, settings.LocaleAuto)
	table := newTable(w)
	fmt.Fprintf(table, "fetch_rates\t%v\n", s.FetchRates)
	fmt.Fprintf(table, "default_rate_source\t%s\n", s.DefaultRateSource)
	fmt.Fprintf(table, "auto_refresh\t%s\tused by bbbudget serve\n", s.AutoRefresh)
	fmt.Fprintf(table, "coingecko_api_key\t%s\n", describeSecret(s.CoinGeckoAPIKey))
	fmt.Fprintf(table, "locale\t%s\tnow %s; available: %s\n",
		locale, a.ResolveLocale(""), strings.Join(i18n.Locales(), ", "))
	printAssistantSettings(table, s)
	fmt.Fprintf(table, "\nfile\t%s\n", file)
	return table.Flush()
}

func runVersion(_ context.Context, c *cli, _ []string) error {
	fmt.Fprintln(c.stdout, version)
	return nil
}

func runHelp(_ context.Context, c *cli, _ []string) error {
	fmt.Fprint(c.stdout, usage)
	return nil
}

// parseArgs parses flags anywhere among the positional arguments
// ("report home -dir x" and "report -dir x home" both work) and checks the
// number of positionals.
func parseArgs(flags *flag.FlagSet, args []string, want int, usage string) ([]string, error) {
	var pos []string
	for {
		if err := flags.Parse(args); err != nil {
			return nil, err
		}
		if flags.NArg() == 0 {
			break
		}
		pos = append(pos, flags.Arg(0))
		args = flags.Args()[1:]
	}
	if len(pos) != want {
		return nil, errors.New("usage: bbbudget " + usage)
	}
	return pos, nil
}

// parseFlagsAndID parses a command's flags and its one positional argument,
// the budget id ("home" in `bbbudget report home`).
func parseFlagsAndID(flags *flag.FlagSet, args []string, usage string) (string, error) {
	pos, err := parseArgs(flags, args, 1, usage)
	if err != nil {
		return "", err
	}
	return pos[0], nil
}

func serve(
	ctx context.Context, addr string, h http.Handler, logger *slog.Logger, stdout io.Writer,
) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	fmt.Fprintf(
		stdout,
		"bbbudget %s on http://%s  (MCP: http://%s/mcp)\n",
		version, ln.Addr(), ln.Addr(),
	)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// settingsPatch turns "key=value" flags into a settings patch.
func settingsPatch(sets []string) (settings.Patch, error) {
	var p settings.Patch
	for _, kv := range sets {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return p, fmt.Errorf("-set %q: want key=value", kv)
		}
		switch k {
		case "fetch_rates":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return p, fmt.Errorf("fetch_rates: want true or false, got %q", v)
			}
			p.FetchRates = &b
		case "default_rate_source":
			rateSource := budget.RateSource(v)
			p.DefaultRateSource = &rateSource
		case "auto_refresh":
			p.AutoRefresh = &v
		case "locale":
			p.Locale = &v
		case "coingecko_api_key":
			p.CoinGeckoAPIKey = &v
		case "assistant_provider":
			provider := settings.AssistantProvider(v)
			p.AssistantProvider = &provider
		case "assistant_model":
			p.AssistantModel = &v
		case "assistant_url":
			p.AssistantURL = &v
		case "assistant_api_key":
			p.AssistantAPIKey = &v
		default:
			return p, fmt.Errorf("unknown setting %q", k)
		}
	}
	return p, nil
}

// printAssistantSettings shows whether a key is set, never the key.
func printAssistantSettings(w io.Writer, s settings.Settings) {
	status := assistant.StatusFromSettings(s)
	ready := cmp.Or(status.Problem, "ready")
	fmt.Fprintf(w, "assistant_provider\t%s\t%s\n", s.AssistantProvider, ready)
	fmt.Fprintf(w, "assistant_model\t%s\n", cmp.Or(status.Model, s.AssistantModel))
	fmt.Fprintf(w, "assistant_url\t%s\n", s.AssistantURL)
	fmt.Fprintf(w, "assistant_api_key\t%s\n", describeSecret(s.AssistantAPIKey))
}

func describeSecret(secret string) string {
	if secret == "" {
		return "not set"
	}
	return "set"
}

func printRates(w io.Writer, updates []app.RateUpdate) {
	if len(updates) == 0 {
		return
	}
	table := newTable(w)
	fmt.Fprintln(table, "CURRENCY\tOLD\tNEW\tFROM\tDATE")
	for _, u := range updates {
		if u.Error != "" {
			fmt.Fprintf(table, "%s\t%s\t-\t%s\t\n", u.Code, u.Old, u.Error)
			continue
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", u.Code, u.Old, u.New, u.From, u.Date)
	}
	table.Flush()
}

func printReport(w io.Writer, r *budget.Report, l *i18n.Localizer) {
	fmt.Fprintf(w, "%s (%s)\n\n", r.Name, r.BaseCurrency)
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(table, l.Render(i18n.Msg{Key: "cli.report.header"}))
	format := l.MoneyExact
	for _, m := range r.Months {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n",
			m.Label,
			format(m.Income),
			format(m.Expenses),
			format(m.DebtPayments),
			format(m.Left),
			format(m.Saved),
			format(m.Savings),
		)
	}
	table.Flush()
	fmt.Fprintln(w)
	for _, in := range r.Insights {
		fmt.Fprintf(w, "%s %s\n", toneMark(in.Tone), i18n.StripMdBold(in.Text))
	}
}

func toneMark(tone budget.Tone) string {
	switch tone {
	case budget.ToneWarn:
		return "!"
	case budget.ToneGood:
		return "+"
	default:
		return "-"
	}
}

// logTransaction logs one transaction from the command line and prints it,
// with the flags of its month.
func logTransaction(
	ctx context.Context, w io.Writer, a *app.App, id string, in budget.TransactionInput,
) error {
	result, err := a.LogTransactions(ctx, fromCLI, id, []budget.TransactionInput{in})
	if err != nil {
		return err
	}
	v, err := a.Get(ctx, id)
	if err != nil {
		return err
	}
	logged := result.Logged[0]
	printTransaction(w, logged, v.Budget.BaseCurrency)
	return printMonthFlags(ctx, w, a, id, logged.Date[:len("YYYY-MM")])
}

// printTransaction prints t on one line, with its value in the base
// currency when it was in another one.
func printTransaction(w io.Writer, t budget.Transaction, base string) {
	line := fmt.Sprintf("%s  %s  %s  %s %s", t.ID, t.Date, t.Category, t.Amount, t.Currency)
	if t.Currency != base {
		line += fmt.Sprintf("  → %s %s", t.Base, base)
	}
	fmt.Fprintln(w, line)
}

func printMonthFlags(ctx context.Context, w io.Writer, a *app.App, id, month string) error {
	status, err := a.MonthStatus(ctx, id, month)
	if errors.Is(err, app.ErrBadInput) {
		return nil // a month outside the budget's window has no status
	}
	if err != nil {
		return err
	}
	localizer := a.SettingsLocalizer()
	for _, f := range status.Flags {
		fmt.Fprintf(w, "! %s\n", i18n.StripMdBold(localizer.Render(f.Msg)))
	}
	return nil
}

func printStatus(w io.Writer, status *budget.MonthStatus, l *i18n.Localizer) {
	fmt.Fprintf(w, "%s · %d/%d\n\n", l.Month(status.Month), status.DaysElapsed, status.DaysInMonth)
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(table, l.Render(i18n.Msg{Key: "cli.status.header"}))
	for _, c := range status.Categories {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t\n",
			c.Label, l.MoneyExact(c.Planned), l.MoneyExact(c.Actual), l.MoneyExact(c.Remaining), c.State)
	}
	table.Flush()
	for _, f := range status.Flags {
		fmt.Fprintf(w, "\n! %s", i18n.StripMdBold(l.Render(f.Msg)))
	}
	if len(status.Flags) > 0 {
		fmt.Fprintln(w)
	}
}
