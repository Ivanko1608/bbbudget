package evals

import (
	"context"
	"testing"

	"bbbudget/internal/assistant"
	"bbbudget/internal/settings"
)

const casesDir = "../../evals"

func loadCases(t *testing.T) []Case {
	t.Helper()
	cases, err := Load(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	return cases
}

// idleProvider is a model that only says "Sure!" and never calls a tool.
type idleProvider struct{}

func (idleProvider) Name() settings.AssistantProvider { return "idle" }
func (idleProvider) Model() string                    { return "idle" }
func (idleProvider) Start(string, []assistant.Tool) assistant.Conversation {
	return idleProvider{}
}
func (idleProvider) SendText(context.Context, string) (assistant.Turn, error) {
	return assistant.Turn{Text: "Sure!"}, nil
}
func (idleProvider) SendResults(context.Context, []assistant.ToolResult) (assistant.Turn, error) {
	return assistant.Turn{Text: "Sure!"}, nil
}

func TestAnAssistantThatDoesNothingFailsEveryCase(t *testing.T) {
	for _, c := range loadCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			result := Run(context.Background(), c, idleProvider{}, t.TempDir())

			if result.Err != nil {
				t.Fatal(result.Err)
			}
			if result.Passed() {
				t.Fatalf("an assistant that only says %q passed", result.Reply)
			}
		})
	}
}

func TestLoadFillsInTheNameAndTemplate(t *testing.T) {
	cases := loadCases(t)

	for _, c := range cases {
		if c.Name == "" {
			t.Errorf("a case has no name: %+v", c)
		}
		if c.Template == "" {
			t.Errorf("%s has no template", c.Name)
		}
	}
}

func TestRemoveDigitGroupingJoinsEveryWayToWriteAThousand(t *testing.T) {
	for _, written := range []string{"5,100", "5 100", "5 100", "5 100", "5100"} {
		if got := removeDigitGrouping(written); got != "5100" {
			t.Errorf("removeDigitGrouping(%q) = %q", written, got)
		}
	}
}

func TestIsInScript(t *testing.T) {
	tests := []struct {
		text   string
		script Script
		want   bool
	}{
		{"В октябре около 317 €.", ScriptCyrillic, true},
		{"In October about 317.", ScriptCyrillic, false},
		{"In October about 317.", ScriptLatin, true},
		{"317", ScriptLatin, false},
		{"anything", "klingon", false},
	}

	for _, test := range tests {
		if got := isInScript(test.text, test.script); got != test.want {
			t.Errorf("isInScript(%q, %s) = %v, want %v", test.text, test.script, got, test.want)
		}
	}
}
