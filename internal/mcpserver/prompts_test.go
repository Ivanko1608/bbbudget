package mcpserver

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (h *harness) prompt(name string, args map[string]string) (string, error) {
	h.t.Helper()
	params := &mcp.GetPromptParams{Name: name, Arguments: args}
	result, err := h.cs.GetPrompt(context.Background(), params)
	if err != nil {
		return "", err
	}
	return result.Messages[0].Content.(*mcp.TextContent).Text, nil
}

func TestEveryWorkflowIsOffered(t *testing.T) {
	h := setup(t)

	listed, err := h.cs.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, p := range listed.Prompts {
		names = append(names, p.Name)
	}
	offered := []string{"onboard", "monthly_review", "can_i_afford", "debt_plan", "import_statement"}
	for _, want := range offered {
		if !slices.Contains(names, want) {
			t.Errorf("%s is not offered: %v", want, names)
		}
	}
}

func TestPromptArgumentsReachTheText(t *testing.T) {
	h := setup(t)
	args := map[string]string{"item": "a bike", "amount": "1200", "language": "Russian"}

	text, err := h.prompt("can_i_afford", args)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"a bike", "1200", "Answer in Russian."} {
		if !strings.Contains(text, want) {
			t.Errorf("can_i_afford lacks %q:\n%s", want, text)
		}
	}
}

func TestWithoutALanguageThePromptAnswersInTheUsersOwn(t *testing.T) {
	h := setup(t)

	text, err := h.prompt("debt_plan", nil)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(text, "Answer in the language the user writes in.") {
		t.Errorf("debt_plan doesn't follow the user's language:\n%s", text)
	}
}

func TestAMissingRequiredArgumentIsAnError(t *testing.T) {
	h := setup(t)

	_, err := h.prompt("can_i_afford", map[string]string{"item": "a bike"})

	if err == nil {
		t.Error("can_i_afford without an amount was accepted")
	}
}

// toolLike matches snake_case words: tool names, and also fields such as
// extra_to_debt_pct.
var toolLike = regexp.MustCompile(`\b[a-z]+(?:_[a-z]+)+\b`)

func TestPromptsNameOnlyToolsThatExist(t *testing.T) {
	h := setup(t)
	tools, err := h.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	isTool := map[string]bool{}
	isToolVerb := map[string]bool{}
	for _, tool := range tools.Tools {
		verb, _, _ := strings.Cut(tool.Name, "_")
		isTool[tool.Name] = true
		isToolVerb[verb] = true
	}
	allArguments := promptArgs{"item": "a bike", "amount": "1200"}

	for _, w := range promptTemplates {
		for _, word := range toolLike.FindAllString(w.text(allArguments), -1) {
			verb, _, _ := strings.Cut(word, "_")
			if isToolVerb[verb] && !isTool[word] {
				t.Errorf("%s names %s, which is not a tool", w.name, word)
			}
		}
	}
}
