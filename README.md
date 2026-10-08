# bbbudget

Plan and explore household budgets across currencies: a projection engine with exact money, a web UI, a CLI and an MCP server so Claude (or any MCP client) can read and edit your budgets.

```
make build
./bin/bbbudget init home -template example     # a fictional household to explore
./bin/bbbudget serve            # http://127.0.0.1:8080  (MCP at /mcp)
./bin/bbbudget report home      # projection + insights in the terminal
```

Requires Go 1.25+ (the MCP SDK's minimum; developed on 1.27). One static binary; the UI is embedded.

## What it models

| Concept | Notes |
|---|---|
| **Base currency** | Everything is projected in it (EUR in the example). |
| **Currencies** | `per_base` rate (e.g. US$ per €) and `transfer_fee_pct`, the share lost on every payment in that currency (fees + spread). |
| **Categories** | `income` or `expense`, with a label and colour. |
| **Lines** | Amount in any configured currency, with a schedule: monthly, every N months, or one-off. Each schedule has a start month and an optional end month. |
| **Debts** | Balance, APR and minimum payment, in any currency. Interest compounds monthly at APR/12. `extra_to_debt_pct` of each month's leftover is paid on top, ordered by strategy: `avalanche` (highest APR first), `snowball` (smallest balance first) or `listed`. |
| **Goals** | Savings targets: when they're reached, or the monthly rate needed to reach them. |
| **Scenarios** | Named sets of edits, compared side by side against the real budget. You can also adopt one into the budget. |

Insights cover category shares, transfer losses, ±10% FX sensitivity, one-offs, slack this month, the biggest line, whether debt minimums cover interest, payoff dates, goal ETAs and negative-savings warnings.

## Money handling

- **No floating point in any calculation.** Amounts are `int64` minor units (cents, pence; 0 digits for JPY, 3 for KWD). Rates and percentages are exact rationals (`math/big.Rat`). The one exception is chart pixel positions in the browser.
- **Exact on the wire.** Amounts, rates and percentages are JSON *strings* (`"1150"`, `"1.08"`). Bare JSON numbers are accepted on input and parsed from their literal text, never through `float64`. Exponents, `NaN`, thousands separators and more decimal places than the currency allows (`€1.001`) are rejected.
- **Rounding is explicit and pessimistic.** There is one rounding per conversion. Money you pay rounds **up**, money you receive rounds **down**, and interest charged to you rounds up. A plan therefore never looks better than reality, and is off by at most one minor unit per conversion.
- **Identities hold to the cent.** For every month, `left = income − expenses − debt minimums`, `saved = left − extra debt payment`, and `savings = previous + saved`. Each debt reconciles too: `opening + interest − payments = closing`. Extra payments never exceed their budget. A randomized test checks all of this over 400 budgets with awkward rates, mixed currencies and schedules.
- **Overflow can't wrap.** Arithmetic is checked: overflow and currency mismatches become errors, never wrapped values or crashes. Inputs are bounded (amounts ≤ 10¹² units, ≤ 600 months).
- **Invalid data is never stored.** Every write is re-validated, and validation reports *all* problems with paths (`lines[3].amount`). The UI highlights the matching fields.

## Templates and your own data

Built in are `blank` and `example`, a fictional household that is also the test fixture. **Your own templates live outside the repository**, in `~/.config/bbbudget/templates/` (or `$BBBUDGET_TEMPLATES`, or `-templates DIR`), as YAML or JSON. The file name is the template name, and a file named like a built-in replaces it. Your budgets live in `~/.local/share/bbbudget/` (or `$BBBUDGET_DIR`, or `-dir`). Nothing personal ever needs to be in the repo.

```yaml
# ~/.config/bbbudget/templates/household.yaml
description: Our monthly budget
base_currency: EUR
extra_to_debt_pct: 50
currencies:
  - {code: USD, per_base: 1.08, transfer_fee_pct: 2}   # 2% lost on every USD payment
categories:
  - {label: Income, kind: income}
  - {label: Home}                                        # kind defaults to expense
lines:
  - {name: Salary, amount: 3900, category: income}       # currency defaults to base
  - {name: Rent, amount: 1150, category: home}           # schedule defaults to monthly
  - {name: Insurance, amount: 240, category: home, schedule: {every: 12, start: 2}}
  - {name: Flights, amount: 450.50, currency: USD, category: home, schedule: {every: 0, start: 1}}  # one-off
debts:
  - {name: Credit card, currency: USD, balance: 4000, apr_pct: 24, min_payment: 250}
goals:
  - {name: Emergency fund, target: 5000}
```

```
bbbudget templates                         # list built-in and your templates
bbbudget init home -template household
bbbudget import home ./budget.yaml         # or create a budget straight from a file
```

Ids are derived from names when omitted. YAML numbers keep their exact text: `450.50` is never a float. Unknown fields, exponents and sub-cent amounts are rejected, and a broken template file is reported by file name. The format is the same document the API and **Download JSON** in the UI produce, so you can export a budget and save it as a template.

## Using it with Claude

**Claude Code** (from this directory), add to `.mcp.json`:

```json
{ "mcpServers": { "bbbudget": { "command": "/absolute/path/to/bin/bbbudget", "args": ["mcp", "-budget", "home"] } } }
```

**Claude Desktop**: the same entry goes in `claude_desktop_config.json`.

**Over HTTP**: while `bbbudget serve` runs, the MCP endpoint is `http://127.0.0.1:8080/mcp` (streamable HTTP).

Tools: `list_budgets`, `list_templates`, `create_budget`, `get_budget`, `add_line`, `update_line`, `remove_line`, `set_currency`, `update_settings`, `set_debt`, `apply_ops`, `what_if`, `save_scenario`, `compare_scenarios`, `adopt_scenario`, `delete_scenario`. There is also a resource, `budget://{id}`, with the raw document.

`-budget home` makes `home` the default, so "raise rent to 1200" works without naming the budget. Lines are referenced by id, exact name, or an unambiguous part of a name. An ambiguous reference ("school" matching both "School tuition" and "School supplies") is an error, not a guess. `what_if` never saves.

The web UI and the MCP server can run as separate processes on the same data directory. Writes are serialised with a file lock and guarded by revision numbers. The UI polls and picks up Claude's edits within a few seconds. If you were mid-edit when a change arrived, the UI reloads and tells you, rather than overwriting the other change.

## Layout

```
cmd/bbbudget/        CLI: serve | mcp | init | import | templates | list | report
internal/money/      Money, Decimal, Currency, rounding. No dependencies.
internal/budget/     Document model, validation (Compile → Plan), projection engine,
                     insight rules, ops (edits), scenarios, YAML/JSON documents, report
internal/templates/  Built-in templates (embedded YAML) + the user template directory
internal/store/      Store interface + FileStore (atomic writes, flock, revisions)
internal/app/        Use cases shared by every transport
internal/httpapi/    JSON API + embedded UI + /mcp mount, localhost guards
internal/mcpserver/  MCP tools and resources
web/static/          The UI (vanilla JS, no build step)
```

Dependencies only point inward: `money` ← `budget` ← `store`/`app` ← transports. Only the transports know about HTTP or MCP.

### HTTP API

| | |
|---|---|
| `GET /api/budgets` · `POST /api/budgets` `{id, name, template}` or `{id, budget}` | list / create / import |
| `GET /api/budgets/{id}` → `{id, revision, budget, report}` | read |
| `PUT /api/budgets/{id}` `{revision, budget}` | replace; `409` if the revision is stale, `422` with `issues` if invalid |
| `POST /api/budgets/{id}/ops` `{ops, revision?}` | apply edits atomically |
| `POST /api/budgets/{id}/what-if` `{ops}` | preview, nothing saved |
| `GET /api/budgets/{id}/compare` · `POST …/scenarios` · `POST …/scenarios/{sid}/adopt` · `DELETE …/scenarios/{sid}` | scenarios |

The server binds to 127.0.0.1 and accepts only these requests:

- a localhost `Host` header (blocks DNS rebinding);
- JSON content types for mutations (blocks cross-site form posts);
- DELETE requests carrying an `X-Requested-With` header;
- bodies up to 1 MB.

## Extending

- **A currency.** Add it to the table in `internal/money/currency.go` (code, minor-unit digits, symbol).
- **An insight.** Write `func(*budget.Projection) []budget.Insight` and append it to `DefaultRules` in `insights.go`. Rules are pure functions of the projection.
- **An edit type.** Add a case to `applyOne` in `ops.go` and document it in `OpKinds`. It then works in `apply_ops`, `what_if`, scenarios and the HTTP API.
- **A template.** Drop a YAML or JSON file in your template directory. Only add a built-in (`internal/templates/builtin/`) if it is generic and fictional.
- **A storage backend** (SQLite, a remote API, …). Implement the five-method `store.Store` interface. `Update(id, expectedRevision, fn)` must run `fn` and the write atomically.
- **A new frontend.** Call `app.App`. Never re-implement calculations outside `internal/budget`.

## Development

```
make check     # gofmt, go vet, go test -race
make cover
go test ./internal/money -fuzz FuzzParseDecimal -fuzztime 30s
```

The tests include:

- hand-computed goldens: the example household's numbers, a month-by-month debt payoff, and a foreign-currency extra payment;
- the randomized invariant test;
- the store under concurrent writers from two "processes";
- MCP tools via an in-memory client;
- an end-to-end test that builds the binary and drives `bbbudget mcp` over real stdio.
