// Command bbbudget plans multi-currency household budgets.
//
//	bbbudget serve            web UI + JSON API (+ MCP at /mcp) on localhost
//	bbbudget mcp              MCP server on stdio, for Claude Desktop / Claude Code
//	bbbudget init <id>        create a budget from a template
//	bbbudget list             list budgets
//	bbbudget report <id>      print a budget's projection and insights
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
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
	"bbbudget/internal/httpapi"
	"bbbudget/internal/mcpserver"
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
  version  print the version

Data lives in $BBBUDGET_DIR (default ~/.local/share/bbbudget); every command takes -dir.
Your templates live in $BBBUDGET_TEMPLATES (default ~/.config/bbbudget/templates); -templates overrides.
`

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

	openApp := func() (*app.App, error) {
		st, err := store.NewFileStore(*dir)
		if err != nil {
			return nil, err
		}
		reg, err := templates.Load(*tplDir)
		if err != nil {
			return nil, err
		}
		return app.New(st, reg), nil
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
		opts := httpapi.Options{Static: web.FS(), Log: log}
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
		id, err := parseWithID(fs, args, "init <id> [-template name] [-name display-name]")
		if err != nil {
			return err
		}
		a, err := openApp()
		if err != nil {
			return err
		}
		v, err := a.Create(ctx, id, *name, *template)
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
		v, err := a.Import(ctx, id, b)
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
		id, err := parseWithID(fs, args, "report <id> [-scenario name]")
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
		printReport(stdout, rep)
		return nil

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

func parseWithID(fs *flag.FlagSet, args []string, usage string) (string, error) {
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

func printReport(w io.Writer, r *budget.Report) {
	fmt.Fprintf(w, "%s (%s)\n\n", r.Name, r.BaseCurrency)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "MONTH\tINCOME\tSPENT\tTO DEBTS\tLEFT\tSAVED\tSAVINGS\t")
	for _, m := range r.Months {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n", m.Label, m.Income.Format(), m.Expenses.Format(), m.DebtPayments.Format(), m.Left.Format(), m.Saved.Format(), m.Savings.Format())
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
