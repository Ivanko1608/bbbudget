package budget

import (
	"strings"

	"bbbudget/internal/money"
)

// AddScenario stores a named set of ops. The ops must apply cleanly now.
func AddScenario(b Budget, name, description string, ops []Op) (Budget, Scenario, error) {
	if strings.TrimSpace(name) == "" {
		return Budget{}, Scenario{}, &ValidationError{[]Issue{{"name", "scenario name is required"}}}
	}
	if len(ops) == 0 {
		return Budget{}, Scenario{}, &ValidationError{[]Issue{{"ops", "a scenario needs at least one op"}}}
	}
	if _, err := Apply(b, ops...); err != nil {
		return Budget{}, Scenario{}, err
	}
	nb, err := Clone(b)
	if err != nil {
		return Budget{}, Scenario{}, err
	}
	s := Scenario{ID: uniqueID(name, ids(nb.Scenarios, scenarioKey)), Name: name, Description: description, Ops: ops}
	nb.Scenarios = append(nb.Scenarios, s)
	return nb, s, nil
}

func scenarioKey(s Scenario) (string, string) { return s.ID, s.Name }

// FindScenario resolves a scenario reference.
func FindScenario(b Budget, ref string) (Scenario, int, error) {
	i, err := find("scenario", ref, ids(b.Scenarios, scenarioKey))
	if err != nil {
		return Scenario{}, -1, err
	}
	return b.Scenarios[i], i, nil
}

// RemoveScenario deletes a scenario.
func RemoveScenario(b Budget, ref string) (Budget, error) {
	_, i, err := FindScenario(b, ref)
	if err != nil {
		return Budget{}, err
	}
	nb, err := Clone(b)
	if err != nil {
		return Budget{}, err
	}
	nb.Scenarios = remove(nb.Scenarios, i)
	return nb, nil
}

// AdoptScenario applies a scenario's ops to the budget for real and removes it.
func AdoptScenario(b Budget, ref string) (Budget, error) {
	s, i, err := FindScenario(b, ref)
	if err != nil {
		return Budget{}, err
	}
	nb, err := Apply(b, s.Ops...)
	if err != nil {
		return Budget{}, err
	}
	nb.Scenarios = remove(nb.Scenarios, i)
	return nb, nil
}

// Outcome is the headline result of one variant of the budget.
type Outcome struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Error is set when the scenario no longer applies (e.g. its item was deleted).
	Error            string        `json:"error,omitempty"`
	FirstMonthSaved  *money.Money  `json:"first_month_saved,omitempty"`
	EndSavings       *money.Money  `json:"end_savings,omitempty"`
	DeltaEndSavings  *money.Money  `json:"delta_end_savings,omitempty"`
	TotalInterest    *money.Money  `json:"total_interest_base,omitempty"`
	Goals            []GoalStatus  `json:"goals,omitempty"`
	Debts            []DebtSummary `json:"debts,omitempty"`
	NegativeSavingIn string        `json:"savings_negative_in,omitempty"`
}

// Compare evaluates the baseline and the given scenarios (all if refs is
// empty). Every outcome is computed from the same baseline independently.
func Compare(b Budget, refs []string) ([]Outcome, error) {
	baseRep, err := Analyze(b)
	if err != nil {
		return nil, err
	}
	baseline := outcome("baseline", "Current budget", baseRep, nil)
	out := []Outcome{baseline}

	var scen []Scenario
	if len(refs) == 0 {
		scen = b.Scenarios
	}
	for _, r := range refs {
		s, _, err := FindScenario(b, r)
		if err != nil {
			return nil, err
		}
		scen = append(scen, s)
	}
	for _, s := range scen {
		variant, err := Apply(b, s.Ops...)
		if err != nil {
			out = append(out, Outcome{ID: s.ID, Name: s.Name, Error: err.Error()})
			continue
		}
		rep, err := Analyze(variant)
		if err != nil {
			out = append(out, Outcome{ID: s.ID, Name: s.Name, Error: err.Error()})
			continue
		}
		out = append(out, outcome(s.ID, s.Name, rep, baseline.EndSavings))
	}
	return out, nil
}

func outcome(id, name string, r *Report, baseEnd *money.Money) Outcome {
	o := Outcome{ID: id, Name: name, Goals: r.Goals, Debts: r.Debts}
	first, end, interest := r.Months[0].Saved, r.Totals.EndSavings, r.Totals.TotalInterest
	o.FirstMonthSaved, o.EndSavings, o.TotalInterest = &first, &end, &interest
	if baseEnd != nil {
		d := end.Sub(*baseEnd)
		o.DeltaEndSavings = &d
	}
	for _, m := range r.Months {
		if m.Savings.IsNegative() {
			o.NegativeSavingIn = m.Label
			break
		}
	}
	return o
}
