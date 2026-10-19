package mcpserver

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden prompt texts")

type promptCase struct {
	name   string
	prompt string
	args   map[string]string
}

var promptCases = []promptCase{
	{"onboard_default", "onboard", nil},
	{"onboard_full", "onboard", map[string]string{"id": "home", "language": "German"}},
	{"monthly_review_default", "monthly_review", nil},
	{
		"monthly_review_full",
		"monthly_review",
		map[string]string{"budget": "home", "month": "2026-03", "language": "German"},
	},
	{
		"can_i_afford_default",
		"can_i_afford",
		map[string]string{"item": "a bike", "amount": "1200"},
	},
	{
		"can_i_afford_full",
		"can_i_afford",
		map[string]string{
			"item": "a bike", "amount": "1200 USD", "when": "2026-05",
			"budget": "home", "language": "German",
		},
	},
	{"debt_plan_default", "debt_plan", nil},
	{"debt_plan_full", "debt_plan", map[string]string{"budget": "home", "language": "German"}},
	{"import_statement_default", "import_statement", nil},
	{
		"import_statement_full",
		"import_statement",
		map[string]string{
			"statement": "2026-03-01,-12.50,EUR,Coffee\n2026-03-02,-40,EUR,Fuel",
			"budget":    "home",
			"language":  "German",
		},
	},
}

func TestPromptTextsMatchTheGoldenFiles(t *testing.T) {
	h := setup(t)

	for _, c := range promptCases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join("testdata", "prompts", c.name+".golden")

			got, err := h.prompt(c.prompt, c.args)
			if err != nil {
				t.Fatal(err)
			}

			if *updateGolden {
				writeGolden(t, path, got)
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("%s changed.\nwant:\n%s\ngot:\n%s", c.name, want, got)
			}
		})
	}
}

func writeGolden(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
