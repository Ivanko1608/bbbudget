// Command bbbudget plans multi-currency household budgets.
//
//	bbbudget serve            web UI + JSON API (+ MCP at /mcp) on localhost
//	bbbudget mcp              MCP server on stdio, for Claude Desktop / Claude Code
//	bbbudget init <id>        create a budget from a template
//	bbbudget list             list budgets
//	bbbudget report <id>      print a budget's projection and insights
//	bbbudget rates <id>       fetch current exchange rates into a budget
//	bbbudget history <id>     list recent changes and who made them
//	bbbudget undo <id>        undo a change
//	bbbudget settings         show or change app settings
package main

import (
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
	"bbbudget/internal/budget"
	"bbbudget/internal/httpapi"
	"bbbudget/internal/i18n"
	"bbbudget/internal/mcpserver"
	"bbbudget/internal/settings"
	"bbbudget/internal/store"
	"bbbudget/internal/templates"
	"bbbudget/web"
)

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
  history  list recent changes and who made them:  bbbudget history home [-limit 20]
  undo     undo a change:  bbbudget undo home [-revision REV] [-force]
  version  print the version

Data lives in $BBBUDGET_DIR (default ~/.local/share/bbbudget); every command takes -dir.
Your templates live in $BBBUDGET_TEMPLATES (default ~/.config/bbbudget/templates); -templates overrides.
App settings live in $BBBUDGET_SETTINGS (default ~/.config/bbbudget/settings.json); -settings overrides.
`

// fromCLI is the origin of every write made by a command.
var fromCLI = store.Origin{Source: store.SourceCLI}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("missing command")
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", store.DefaultDir(), "data directory")
	tplDir := fs.String("templates", templates.DefaultDir(), "directory of your own templates (*.yaml, *.json)")
	settingsFile := fs.String("settings", settings.DefaultPath(), "app settings file")

	openApp := func() (*app.App, error) {
		st, err := store.NewFileStore(*dir)
		if err != nil {
			return nil, err
		}
		reg, err := templates.Load(*tplDir)
		if err != nil {
			return nil, err
		}
		a := app.New(st, reg)
		a.SettingsPath = *settingsFile
		if _, err := a.Settings(); err != nil {
			return nil, err
		}
		return a, nil
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))

	switch cmd {
	case "serve":
		addr := fs.String("addr", "127.0.0.1:8080", "listen address")
		withMCP := fs.Bool("mcp", true, "also serve MCP (streamable HTTP) at /mcp")
		def := fs.String("budget", "", "default budget for MCP tool calls that omit one")
		hosts := fs.String("allow-host", "", "extra Host names to accept, comma-separated (e.g. when proxied)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		a, err := openApp()
		if err != nil {
			return err
		}
		opts := httpapi.Options{Static: web.FS(), Log: log, Paths: map[string]string{"data": *dir, "templates": *tplDir, "settings": *settingsFile}}
		go a.RunAutoRefresh(ctx, log)
		if *hosts != "" {
			opts.AllowedHosts = strings.Split(*hosts, ",")
		}
		if *withMCP {
			srv := mcpserver.New(a, mcpserver.Options{DefaultBudget: *def, Version: version})
			opts.MCP = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
		}
		return serve(ctx, *addr, httpapi.New(a, opts), log, stdout)

	case "mcp":
		def := fs.String("budget", os.Getenv("BBBUDGET_DEFAULT"), "default budget for tool calls that omit one")
		if err := fs.Parse(args); err != nil {
			return err
		}
		a, err := openApp()
		if err != nil {
			return err
		}
		log.Info("mcp server on stdio", "dir", *dir, "default_budget", *def)
		return mcpserver.New(a, mcpserver.Options{DefaultBudget: *def, Version: version}).Run(ctx, &mcp.StdioTransport{})

	case "init":
		template := fs.String("template", "blank", "template name (see: bbbudget templates)")
		name := fs.String("name", "", "display name (default: the id)")
		id, err := parseFlagsAndID(fs, args, "init <id> [-template name] [-name display-name]")
		if err != nil {
			return err
		}
		a, err := openApp()
		if err != nil {
			return err
		}
		v, err := a.Create(ctx, fromCLI, id, *name, *template)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "created %q (%s) in %s\n", v.ID, v.Budget.Name, *dir)
		return nil

	case "templates":
		if err := fs.Parse(args); err != nil {
			return err
		}
		reg, err := templates.Load(*tplDir)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSOURCE\tDESCRIPTION")
		for _, t := range reg.List() {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", t.Name, t.Source, t.Description)
		}
		fmt.Fprintf(tw, "\nAdd your own as *.yaml or *.json in %s\n", *tplDir)
		return tw.Flush()

	case "import":
		pos, err := parseArgs(fs, args, 2, "import <id> <file.yaml|file.json>")
		if err != nil {
			return err
		}
		id, file := pos[0], pos[1]
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		b, err := budget.ParseDocument(data, strings.TrimPrefix(filepath.Ext(file), "."))
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		a, err := openApp()
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
		fmt.Fprintf(stdout, "imported %q (%s)\n", v.ID, v.Budget.Name)
		return nil

	case "list":
		if err := fs.Parse(args); err != nil {
			return err
		}
		a, err := openApp()
		if err != nil {
			return err
		}
		l, err := a.List(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tREVISION\tUPDATED")
		for _, m := range l {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", m.ID, m.Name, m.Revision, m.UpdatedAt.Local().Format("2006-01-02 15:04"))
		}
		return tw.Flush()

	case "report":
		scenario := fs.String("scenario", "", "show a saved scenario instead of the budget")
		id, err := parseFlagsAndID(fs, args, "report <id> [-scenario name]")
		if err != nil {
			return err
		}
		a, err := openApp()
		if err != nil {
			return err
		}
		var rep *budget.Report
		if *scenario != "" {
			rep, err = a.ScenarioReport(ctx, id, *scenario)
		} else {
			var v *app.View
			if v, err = a.Get(ctx, id); err == nil {
				rep = v.Report
			}
		}
		if err != nil {
			return err
		}
		printReport(stdout, rep, a.Localizer(ctx))
		return nil

	case "rates":
		only := fs.String("only", "", "comma-separated currencies to refresh (default: all whose source isn't manual)")
		dry := fs.Bool("dry-run", false, "show current rates without saving")
		id, err := parseFlagsAndID(fs, args, "rates <id> [-only USD,BTC] [-dry-run]")
		if err != nil {
			return err
		}
		a, err := openApp()
		if err != nil {
			return err
		}
		var codes []string
		if *only != "" {
			codes = strings.Split(strings.ToUpper(strings.ReplaceAll(*only, " ", "")), ",")
		}
		var updates []app.RateUpdate
		if *dry {
			updates, err = a.PreviewRates(ctx, id, codes)
		} else {
			_, updates, err = a.RefreshRates(ctx, fromCLI, id, codes)
		}
		printRates(stdout, updates)
		if err == nil && len(updates) == 0 {
			fmt.Fprintln(stdout, "no currencies to refresh: every rate is manual (name some with -only to fetch them)")
		}
		return err

	case "history":
		limit := fs.Int("limit", store.DefaultHistoryPage, "how many changes to show (0 for all)")
		id, err := parseFlagsAndID(fs, args, "history <id> [-limit 20]")
		if err != nil {
			return err
		}
		a, err := openApp()
		if err != nil {
			return err
		}
		h, err := a.History(ctx, id, *limit)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "REV\tWHEN\tBY\tCHANGE")
		for _, e := range h {
			when := e.At.Local().Format("2006-01-02 15:04")
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", e.Revision, when, e.Source, e.Summary)
		}
		return tw.Flush()

	case "undo":
		revision := fs.Int64("revision", 0, "revision of the change to undo (default: the latest)")
		force := fs.Bool("force", false, "also undo every change made after it")
		id, err := parseFlagsAndID(fs, args, "undo <id> [-revision REV] [-force]")
		if err != nil {
			return err
		}
		a, err := openApp()
		if err != nil {
			return err
		}
		v, err := a.Undo(ctx, fromCLI, id, app.UndoRequest{Revision: *revision, Force: *force})
		if err != nil {
			return err
		}
		h, err := a.History(ctx, id, 1)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "r%d: %s\n", v.Revision, h[0].Summary)
		return nil

	case "settings":
		var sets []string
		fs.Func("set", "key=value to change (repeatable): fetch_rates, default_rate_source, auto_refresh, coingecko_api_key, locale", func(v string) error {
			sets = append(sets, v)
			return nil
		})
		if _, err := parseArgs(fs, args, 0, "settings [-set key=value ...]"); err != nil {
			return err
		}
		a, err := openApp()
		if err != nil {
			return err
		}
		if len(sets) > 0 {
			p, err := settingsPatch(sets)
			if err != nil {
				return err
			}
			if _, err := a.UpdateSettings(p); err != nil {
				return err
			}
		}
		s, err := a.Settings()
		if err != nil {
			return err
		}
		key := "not set"
		if s.CoinGeckoAPIKey != "" {
			key = "set"
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		locale := s.Locale
		if locale == "" {
			locale = settings.LocaleAuto
		}
		fmt.Fprintf(tw, "fetch_rates\t%v\ndefault_rate_source\t%s\nauto_refresh\t%s\tused by bbbudget serve\ncoingecko_api_key\t%s\nlocale\t%s\tnow %s; available: %s\n",
			s.FetchRates, s.DefaultRateSource, s.AutoRefresh, key, locale, a.Locale(ctx), strings.Join(i18n.Locales(), ", "))
		fmt.Fprintf(tw, "\nfile\t%s\n", *settingsFile)
		return tw.Flush()

	case "version":
		fmt.Fprintln(stdout, version)
		return nil

	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	}
	fmt.Fprint(stderr, usage)
	return fmt.Errorf("unknown command %q", cmd)
}

// parseArgs parses flags anywhere among the positional arguments
// ("report home -dir x" and "report -dir x home" both work) and checks the
// number of positionals.
func parseArgs(fs *flag.FlagSet, args []string, want int, usage string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != want {
		return nil, errors.New("usage: bbbudget " + usage)
	}
	return pos, nil
}

// parseFlagsAndID parses a command's flags and its one positional argument,
// the budget id ("home" in `bbbudget report home`).
func parseFlagsAndID(fs *flag.FlagSet, args []string, usage string) (string, error) {
	pos, err := parseArgs(fs, args, 1, usage)
	if err != nil {
		return "", err
	}
	return pos[0], nil
}

func serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger, stdout io.Writer) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	fmt.Fprintf(stdout, "bbbudget %s on http://%s  (MCP: http://%s/mcp)\n", version, ln.Addr(), ln.Addr())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
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
			p.DefaultRateSource = &v
		case "auto_refresh":
			p.AutoRefresh = &v
		case "locale":
			p.Locale = &v
		case "coingecko_api_key":
			p.CoinGeckoAPIKey = &v
		default:
			return p, fmt.Errorf("unknown setting %q", k)
		}
	}
	return p, nil
}

func printRates(w io.Writer, updates []app.RateUpdate) {
	if len(updates) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CURRENCY\tOLD\tNEW\tFROM\tDATE")
	for _, u := range updates {
		if u.Error != "" {
			fmt.Fprintf(tw, "%s\t%s\t-\t%s\t\n", u.Code, u.Old, u.Error)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", u.Code, u.Old, u.New, u.From, u.Date)
	}
	tw.Flush()
}

func printReport(w io.Writer, r *budget.Report, l *i18n.Localizer) {
	fmt.Fprintf(w, "%s (%s)\n\n", r.Name, r.BaseCurrency)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, l.Render(i18n.Msg{Key: "cli.report.header"}))
	f := l.MoneyExact
	for _, m := range r.Months {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n", m.Label, f(m.Income), f(m.Expenses), f(m.DebtPayments), f(m.Left), f(m.Saved), f(m.Savings))
	}
	tw.Flush()
	fmt.Fprintln(w)
	for _, in := range r.Insights {
		mark := map[string]string{budget.ToneWarn: "!", budget.ToneGood: "+"}[in.Tone]
		if mark == "" {
			mark = "-"
		}
		fmt.Fprintf(w, "%s %s\n", mark, strings.ReplaceAll(in.Text, "**", ""))
	}
}
