package budget

import (
	"strings"
	"testing"

	"bbbudget/internal/i18n"
)

func compareWithStaleScenario(t *testing.T, lang string) Outcome {
	t.Helper()
	b := fixture(t)
	cheaperSchool := []Op{NewOp("set_item", "tuition", ItemPatch{Amount: decp("450")})}
	b, _, err := AddScenario(b, "Cheaper school", "", cheaperSchool)
	if err != nil {
		t.Fatal(err)
	}
	b, err = Apply(b, NewOp("remove_item", "tuition", nil))
	if err != nil {
		t.Fatal(err)
	}

	outcomes, err := Compare(b, nil, i18n.For(lang))
	if err != nil {
		t.Fatal(err)
	}

	return outcomes[1]
}

func TestStaleScenarioOutcomeHasAFixedMessageAndTheFailedOp(t *testing.T) {
	stale := compareWithStaleScenario(t, "en")

	eq(t, "error", stale.Error, "This scenario no longer applies to the budget.")
	eq(t, "op index", stale.OpIndex, 1)
	eq(t, "op", stale.Op, OpKind("set_item"))
}

func TestStaleScenarioMessageIsLocalised(t *testing.T) {
	stale := compareWithStaleScenario(t, "ru")

	eq(t, "error", stale.Error, "Этот сценарий больше не подходит к бюджету.")
}

func TestStaleScenarioMessageHoldsNoErrorText(t *testing.T) {
	stale := compareWithStaleScenario(t, "en")

	eq(t, "mentions the item ref", strings.Contains(stale.Error, "tuition"), false)
}
