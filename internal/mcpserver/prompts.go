package mcpserver

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// promptTemplate is a common job, offered to the user as a slash command. Its
// text is a short procedure over the tools the server already has.
type promptTemplate struct {
	name        string
	title       string
	description string
	arguments   []*mcp.PromptArgument
}

// promptArgs are the arguments a prompt was asked for with.
type promptArgs map[string]string

func (args promptArgs) get(name string) string { return strings.TrimSpace(args[name]) }

func (args promptArgs) getOrDefault(name, fallback string) string {
	if value := args.get(name); value != "" {
		return value
	}
	return fallback
}

func requiredArgument(name, description string) *mcp.PromptArgument {
	return &mcp.PromptArgument{Name: name, Description: description, Required: true}
}

func optionalArgument(name, description string) *mcp.PromptArgument {
	return &mcp.PromptArgument{Name: name, Description: description}
}

var (
	budgetArg   = optionalArgument("budget", "budget id; omit for the default")
	languageArg = optionalArgument("language", "language to answer in; default: the one the user writes in")
	monthArg    = optionalArgument("month", "YYYY-MM; default this month")
)

var promptTemplates = []promptTemplate{
	{
		name:        "onboard",
		title:       "Set up a budget by talking",
		description: "Build a new budget from a short interview instead of filling in forms.",
		arguments: []*mcp.PromptArgument{
			optionalArgument("id", "id for the new budget, e.g. home"),
			languageArg,
		},
	},
	{
		name:        "monthly_review",
		title:       "Review this month",
		description: "Where the month stands against the plan, what changed, and what to do about it.",
		arguments:   []*mcp.PromptArgument{budgetArg, monthArg, languageArg},
	},
	{
		name:        "can_i_afford",
		title:       "Can I afford it?",
		description: "Check a purchase against the plan before making it.",
		arguments: []*mcp.PromptArgument{
			requiredArgument("item", "what you want to buy"),
			requiredArgument("amount", "price, e.g. 1200 or 1200 USD"),
			optionalArgument("when", "YYYY-MM; default this month"),
			budgetArg,
			languageArg,
		},
	},
	{
		name:        "debt_plan",
		title:       "Plan paying off debt",
		description: "Compare payoff strategies and how much extra to put towards debt.",
		arguments:   []*mcp.PromptArgument{budgetArg, languageArg},
	},
	{
		name:        "import_statement",
		title:       "Import a bank statement",
		description: "Turn a pasted bank or card statement into logged transactions.",
		arguments: []*mcp.PromptArgument{
			optionalArgument("statement", "the statement text (CSV or copied rows)"),
			budgetArg,
			languageArg,
		},
	},
}

//go:embed prompts/*.tmpl
var promptFiles embed.FS

// promptTexts holds one template per prompt, named like the prompt, plus the
// shared parts they include.
var promptTexts = template.Must(parsePromptFiles(promptFiles))

func parsePromptFiles(files fs.FS) (*template.Template, error) {
	paths, err := fs.Glob(files, "prompts/*.tmpl")
	if err != nil {
		return nil, err
	}
	texts := template.New("prompts")
	for _, path := range paths {
		if err := parsePromptFile(texts, files, path); err != nil {
			return nil, err
		}
	}
	return texts, nil
}

func parsePromptFile(texts *template.Template, files fs.FS, path string) error {
	source, err := fs.ReadFile(files, path)
	if err != nil {
		return err
	}
	name := strings.TrimSuffix(filepath.Base(path), ".tmpl")
	body := strings.TrimSuffix(string(source), "\n")
	if _, err := texts.New(name).Parse(body); err != nil {
		return fmt.Errorf("parse prompt %s: %w", name, err)
	}
	return nil
}

// promptFields are the values the prompt templates are filled with.
type promptFields struct {
	ID        string
	Budget    string
	Language  string
	Month     string
	When      string
	Item      string
	Amount    string
	Statement string
}

func promptFieldsFromArgs(args promptArgs) promptFields {
	return promptFields{
		ID:        args.get("id"),
		Budget:    args.get("budget"),
		Language:  args.get("language"),
		Month:     args.getOrDefault("month", "this month"),
		When:      args.getOrDefault("when", "this month"),
		Item:      args.get("item"),
		Amount:    args.get("amount"),
		Statement: args.getOrDefault("statement", "(ask me to paste it)"),
	}
}

func (t promptTemplate) renderText(args promptArgs) (string, error) {
	var text strings.Builder
	err := promptTexts.ExecuteTemplate(&text, t.name, promptFieldsFromArgs(args))
	return text.String(), err
}

func registerPrompts(s *mcp.Server) {
	for _, t := range promptTemplates {
		prompt := &mcp.Prompt{
			Name:        t.name,
			Title:       t.title,
			Description: t.description,
			Arguments:   t.arguments,
		}
		s.AddPrompt(prompt, t.render)
	}
}

func (t promptTemplate) render(
	ctx context.Context, req *mcp.GetPromptRequest,
) (*mcp.GetPromptResult, error) {
	args := promptArgs(req.Params.Arguments)
	if missing := t.missingArgument(args); missing != "" {
		return nil, fmt.Errorf("%s needs %q", t.name, missing)
	}
	text, err := t.renderText(args)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", t.name, err)
	}
	message := &mcp.PromptMessage{Role: "user", Content: &mcp.TextContent{Text: text}}
	return &mcp.GetPromptResult{
		Description: t.description,
		Messages:    []*mcp.PromptMessage{message},
	}, nil
}

// missingArgument is the first required argument args lacks, or "".
func (t promptTemplate) missingArgument(args promptArgs) string {
	for _, argument := range t.arguments {
		if argument.Required && args.get(argument.Name) == "" {
			return argument.Name
		}
	}
	return ""
}
