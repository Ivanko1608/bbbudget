package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

// createdRevision is the revision of a budget nobody has changed yet.
const createdRevision = 1

// check returns one message per expectation that did not hold.
func check(ctx context.Context, budgetApp *app.App, expect Expect, reply string) []string {
	view, err := budgetApp.Get(ctx, budgetID)
	if err != nil {
		return []string{err.Error()}
	}
	history, err := budgetApp.History(ctx, budgetID, store.AllEntries)
	if err != nil {
		return []string{err.Error()}
	}
	transactions, err := budgetApp.ListTransactions(ctx, budgetID, app.TransactionFilter{})
	if err != nil {
		return []string{err.Error()}
	}
	return slices.Concat(
		checkSaved(view, expect.Saved),
		checkWriters(history, expect.WritesBy),
		checkItems(view, expect),
		checkTransactions(transactions, expect),
		checkReply(reply, expect),
	)
}

func checkSaved(view *app.View, wantSaved *bool) []string {
	if wantSaved == nil {
		return nil
	}
	saved := view.Revision > createdRevision
	if saved == *wantSaved {
		return nil
	}
	return []string{
		fmt.Sprintf("saved: want %v, the budget is at revision %d", *wantSaved, view.Revision),
	}
}

func checkWriters(history []store.Entry, want store.Source) []string {
	if want == "" {
		return nil
	}
	var failures []string
	for _, entry := range history {
		isWrite := entry.Revision > createdRevision
		if isWrite && entry.Source != want {
			failures = append(failures,
				fmt.Sprintf("r%d was written by %q, not %q", entry.Revision, entry.Source, want))
		}
	}
	return failures
}

func checkItems(view *app.View, expect Expect) []string {
	var failures []string
	for id, wantFields := range expect.Items {
		item, found := findItem(view.Budget.Items, id)
		if !found {
			failures = append(failures, fmt.Sprintf("no item %q", id))
			continue
		}
		failures = append(failures, compareItem(view.Budget.StartMonth, item, wantFields)...)
	}
	for _, name := range expect.NoItemNamed {
		for _, item := range view.Budget.Items {
			if strings.Contains(strings.ToLower(item.Name), strings.ToLower(name)) {
				failures = append(failures, fmt.Sprintf("item %q should not exist", item.Name))
			}
		}
	}
	return failures
}

func findItem(items []budget.Item, id string) (budget.Item, bool) {
	index := slices.IndexFunc(items, func(item budget.Item) bool { return item.ID == id })
	if index < 0 {
		return budget.Item{}, false
	}
	return items[index], true
}

func compareItem(startMonth string, item budget.Item, want map[ItemField]string) []string {
	actual := map[ItemField]string{
		ItemAmount:   item.Amount.String(),
		ItemCurrency: item.Currency,
		ItemCategory: item.Category,
		ItemName:     item.Name,
		ItemEvery:    fmt.Sprint(item.Schedule.Every),
		ItemStarts:   budget.MonthKey(startMonth, item.Schedule.Start),
	}
	var failures []string
	for field, wantValue := range want {
		got, known := actual[field]
		switch {
		case !known:
			failures = append(failures, fmt.Sprintf("item %s: unknown field %q", item.ID, field))
		case !sameValue(got, wantValue):
			failures = append(failures,
				fmt.Sprintf("item %s: %s is %q, want %q", item.ID, field, got, wantValue))
		}
	}
	return failures
}

func checkTransactions(transactions []budget.Transaction, expect Expect) []string {
	var failures []string
	if expect.Transactions != nil && len(transactions) != *expect.Transactions {
		failures = append(failures,
			fmt.Sprintf("transactions: %d logged, want %d", len(transactions), *expect.Transactions))
	}
	if expect.Transaction != nil && !slices.ContainsFunc(transactions, matches(expect.Transaction)) {
		logged, _ := json.Marshal(transactions)
		failures = append(failures,
			fmt.Sprintf("no logged transaction matches %v; have %s", expect.Transaction, logged))
	}
	return failures
}

func matches(want map[TransactionField]string) func(budget.Transaction) bool {
	return func(transaction budget.Transaction) bool {
		actual := map[TransactionField]string{
			TransactionDate:     transaction.Date,
			TransactionAmount:   transaction.Amount.String(),
			TransactionCurrency: transaction.Currency,
			TransactionCategory: transaction.Category,
			TransactionBase:     transaction.Base.String(),
		}
		for field, wantValue := range want {
			if !sameValue(actual[field], wantValue) {
				return false
			}
		}
		return true
	}
}

func checkReply(reply string, expect Expect) []string {
	var failures []string
	if expect.ReplyScript != "" && !isInScript(reply, expect.ReplyScript) {
		failures = append(failures,
			fmt.Sprintf("the reply is not in %s script: %q", expect.ReplyScript, reply))
	}
	for _, want := range expect.ReplyContains {
		if !strings.Contains(removeDigitGrouping(reply), removeDigitGrouping(want)) {
			failures = append(failures, fmt.Sprintf("the reply doesn't mention %q: %q", want, reply))
		}
	}
	return failures
}

// sameValue compares decimals by value ("1200" = "1200.00") and the rest as text.
func sameValue(got, want string) bool {
	if got == want {
		return true
	}
	gotDecimal, gotErr := money.ParseDecimal(got)
	wantDecimal, wantErr := money.ParseDecimal(want)
	return gotErr == nil && wantErr == nil && gotDecimal.Cmp(wantDecimal) == 0
}

var digitGroup = regexp.MustCompile(`(\d)[,\s\x{00a0}\x{202f}'](\d{3})`)

// removeDigitGrouping makes "5,100", "5 100" and "5100" the same text.
func removeDigitGrouping(text string) string {
	for {
		ungrouped := digitGroup.ReplaceAllString(text, "$1$2")
		if ungrouped == text {
			return text
		}
		text = ungrouped
	}
}

// isInScript reports whether most letters of text are in the script.
func isInScript(text string, script Script) bool {
	table := map[Script]*unicode.RangeTable{
		ScriptCyrillic: unicode.Cyrillic,
		ScriptLatin:    unicode.Latin,
	}[script]
	if table == nil {
		return false
	}
	inScript, letters := 0, 0
	for _, r := range text {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.Is(table, r) {
			inScript++
		}
	}
	return letters > 0 && inScript*2 > letters
}
