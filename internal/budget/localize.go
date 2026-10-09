package budget

import (
	"time"

	"bbbudget/internal/i18n"
)

// Localize renders every human-readable field of the report in l's
// language: month labels, schedule descriptions, payoff and goal dates and
// insight text. Numbers and keys are untouched. Analyze localizes to
// English; transports re-localize for their reader.
func (r *Report) Localize(l *i18n.Localizer) {
	for i := range r.Months {
		r.Months[i].Label = l.Month(r.Months[i].Key)
	}
	for i := range r.Lines {
		r.Lines[i].ScheduleText = ScheduleText(l, r.StartMonth, r.Lines[i].Schedule)
	}
	for i, d := range r.Debts {
		if d.PaidOffMonth != nil {
			r.Debts[i].PaidOffLabel = r.Months[*d.PaidOffMonth].Label
		}
	}
	for i, g := range r.Goals {
		if g.ReachedMonth != nil {
			r.Goals[i].ReachedLabel = r.Months[*g.ReachedMonth].Label
		}
	}
	for i := range r.Insights {
		r.Insights[i].Text = l.T(r.Insights[i].Msg)
	}
}

// ScheduleText describes a schedule: "monthly", "quarterly from Dec 2026",
// "once, Mar 2027".
func ScheduleText(l *i18n.Localizer, startMonth string, s Schedule) string {
	month := func(i int) i18n.Arg { return i18n.Month(MonthKey(startMonth, i)) }
	if s.Every == 0 {
		return l.Text("schedule.once", args{"month": month(s.Start)})
	}
	var txt string
	switch s.Every {
	case 1:
		txt = l.Text("schedule.monthly", nil)
	case 3:
		txt = l.Text("schedule.quarterly", nil)
	case 12:
		txt = l.Text("schedule.yearly", nil)
	default:
		txt = l.Text("schedule.every", args{"count": i18n.Int(int64(s.Every))})
	}
	if s.Start > 0 {
		txt = l.Text("schedule.from", args{"schedule": i18n.Text(txt), "month": month(s.Start)})
	}
	if s.End != nil {
		txt = l.Text("schedule.until", args{"schedule": i18n.Text(txt), "month": month(*s.End)})
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
