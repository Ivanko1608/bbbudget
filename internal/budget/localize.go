package budget

import (
	"time"

	"bbbudget/internal/i18n"
)

// Localize renders every human-readable field of the report in l's
// language: month labels, schedule descriptions, payoff, goal and shortfall
// dates and insight text. Numbers and keys are untouched. Analyze localizes to
// English; transports re-localize for their reader.
func (r *Report) Localize(l *i18n.Localizer) {
	for i := range r.Months {
		r.Months[i].Label = l.Month(r.Months[i].Key)
	}
	for i := range r.Items {
		r.Items[i].ScheduleText = ScheduleText(l, r.StartMonth, r.Items[i].Schedule)
	}
	for i, d := range r.Debts {
		if d.PaidOffMonth != nil {
			r.Debts[i].PaidOffLabel = r.Months[*d.PaidOffMonth].Label
		}
	}
	for i := range r.Goals {
		r.localizeGoal(&r.Goals[i])
	}
	for i := range r.Pots {
		if r.Pots[i].Goal != nil {
			r.localizeGoal(r.Pots[i].Goal)
		}
	}
	for i, sf := range r.Shortfalls {
		r.Shortfalls[i].Label = r.Months[sf.Month].Label
	}
	for i := range r.Insights {
		r.Insights[i].Text = l.Render(r.Insights[i].Msg)
	}
}

func (r *Report) localizeGoal(g *GoalStatus) {
	if g.ReachedMonth != nil {
		g.ReachedLabel = r.Months[*g.ReachedMonth].Label
	}
}

// ScheduleText describes a schedule: "monthly", "quarterly from Dec 2026",
// "once, Mar 2027".
func ScheduleText(l *i18n.Localizer, startMonth string, s Schedule) string {
	month := func(i int) i18n.Arg { return i18n.Month(MonthKey(startMonth, i)) }
	if s.Every == 0 {
		return l.Render(msg("schedule.once", args{"month": month(s.Start)}))
	}
	var txt string
	switch s.Every {
	case 1:
		txt = l.Render(msg("schedule.monthly", nil))
	case 3:
		txt = l.Render(msg("schedule.quarterly", nil))
	case 12:
		txt = l.Render(msg("schedule.yearly", nil))
	default:
		txt = l.Render(msg("schedule.every", args{"count": i18n.Int(int64(s.Every))}))
	}
	if s.Start > 0 {
		txt = l.Render(msg("schedule.from", args{"schedule": i18n.Text(txt), "month": month(s.Start)}))
	}
	if s.End != nil {
		txt = l.Render(msg("schedule.until", args{"schedule": i18n.Text(txt), "month": month(*s.End)}))
	}
	return txt
}

// MonthKey is "YYYY-MM" for month index i of a budget starting in startMonth.
func MonthKey(startMonth string, i int) string {
	t, err := time.Parse("2006-01", startMonth)
	if err != nil {
		return startMonth
	}
	return t.AddDate(0, i, 0).Format("2006-01")
}
