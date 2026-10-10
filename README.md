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
| **Base currency** | Everything is projected in it (EUR in the example). Must be fiat. |
| **Currencies** | Fiat or crypto (BTC, ETH, SOL, LTC, BNB, XRP, ADA, TRX, DOGE, USDT, USDC, DAI). `per_base` rate (e.g. US$ per €), `transfer_fee_pct` (the share lost on every payment in that currency) and `cash_out_fee_pct` (the share lost when income in it is converted to base, such as fees for selling crypto). |
| **Rate source** | Per currency: `manual` (typed) or fetched from `ecb`, `currency-api`, `coingecko` or `auto`. See [Exchange rates](#exchange-rates). |
| **Categories** | `income` or `expense`, with a label and colour. |
| **Lines** | Amount in any configured currency, with a schedule: monthly, every N months, or one-off. Each schedule has a start month and an optional end month. |
| **Debts** | Balance, APR and minimum payment, in any currency. Interest compounds monthly at APR/12. `extra_to_debt_pct` of each month's leftover is paid on top, ordered by strategy: `avalanche` (highest APR first), `snowball` (smallest balance first) or `listed`. |
| **Savings pots** | Savings split by purpose: general savings, sinking funds (saving ahead for a known cost) and an emergency fund. Each has a balance and optionally a target and a fixed monthly contribution. Each month's leftover pays the contributions, then the share to debts, then fills pots with a target (top to bottom), then goes to the first general pot. |
| **Short months** | Savings are balances, never income. A month that falls short is **not** paid from savings automatically: it's reported with the pots that could cover it (general first, sinking funds next, the emergency fund last), and otherwise owed on the current account and repaid from the next surplus before anything is saved. |
| **Withdrawals** | Planned transfers out of a pot ("take €300 from General savings in October"), shown as *from savings*, never as income. One tap on a short month plans one. |
| **Scenarios** | Named sets of edits, compared side by side against the real budget. You can also adopt one into the budget. |

Insights cover category shares, transfer losses, cash-out losses, how much income is in volatile crypto and what a 30% fall would do, ±10% FX sensitivity, one-offs, slack this month, the biggest line, whether debt minimums cover interest, payoff dates, when each pot reaches its target, short months and how to cover them, withdrawals a pot can't fully pay, and a missing emergency fund (3–6 months of spending).

## Money handling

- **No floating point in any calculation.** Amounts are `int64` minor units (cents, pence; 0 digits for JPY, 3 for KWD). Rates and percentages are exact rationals (`math/big.Rat`). The one exception is chart pixel positions in the browser.
- **Exact on the wire.** Amounts, rates and percentages are JSON *strings* (`"1150"`, `"1.08"`). Bare JSON numbers are accepted on input and parsed from their literal text, never through `float64`. Exponents, `NaN`, thousands separators and more decimal places than the currency allows (`€1.001`) are rejected.
- **Rounding is explicit and pessimistic.** There is one rounding per conversion. Money you pay rounds **up**, money you receive rounds **down**, and interest charged to you rounds up. A plan therefore never looks better than reality, and is off by at most one minor unit per conversion.
- **Identities hold to the cent.** For every month, `left = income − expenses − debt minimums`, `left + from savings + change in overdraft = extra debt payment + into savings`, `saved = into savings − from savings`, `savings = previous + saved = the sum of the pots`, and every pot reconciles (`opening + in − out = closing`, never below zero). Each debt reconciles too: `opening + interest − payments = closing`. Extra payments never exceed their budget. A randomized test checks all of this over 400 budgets with awkward rates, mixed currencies, schedules, pots, withdrawals and overdrafts.
- **Overflow can't wrap.** Arithmetic is checked: overflow and currency mismatches become errors, never wrapped values or crashes. Inputs are bounded (amounts ≤ 10¹² units and ≤ 10¹⁵ minor units, ≤ 600 months).
- **Invalid data is never stored.** Every write is re-validated, and validation reports *all* problems with paths (`lines[3].amount`). The UI highlights the matching fields.

## Exchange rates

Rates can be typed by hand or fetched. Each currency has a `source`:

| Source | What it is | Covers |
|---|---|---|
| `manual` | You type `per_base` (the default for existing budgets). | — |
| `ecb` | European Central Bank euro reference rates: official, published on working days around 16:00 CET. Non-EUR bases use cross rates through the euro. | ~30 major fiat currencies |
| `currency-api` | [fawazahmed0/currency-api](https://github.com/fawazahmed0/exchange-api): free daily rates aggregated from public sources, served by a CDN with a mirror. | ~300 fiat currencies and crypto |
| `coingecko` | CoinGecko live market prices. The keyless API is rate-limited; set `COINGECKO_API_KEY` to a free demo key to raise the limit. | Crypto only |
| `auto` | ECB where it has a rate, otherwise currency-api. | Everything |

Rates are refreshed when you ask, or on a schedule if you turn that on in [Settings](#settings):

- **UI:** choose **Rate from** per currency, then **Update rates now**. Adding a currency or changing its source fetches its rate straight away. Typing a rate yourself switches that currency to manual.
- **CLI:** `bbbudget rates home` refreshes every non-manual currency. `-only USD,BTC` names currencies (a manual one named here switches to `auto`), and `-dry-run` shows the rates without saving. Put it in cron to keep a budget current.
- **Claude:** `refresh_rates`. `set_currency` on a new currency without `per_base` fetches the rate.

Fetched rates stay exact. Provider numbers are parsed from their text (including exponents like `1.35e-05`), never as floats. They are stored to 10 significant digits, and each currency records `rate_date` and `rate_from`. If a provider fails, its currencies keep their old rate and the others still update. A quote is cached for an hour.

### Settings

App-wide preferences live on the **Settings** page (top right of the UI), in `bbbudget settings`, or in `~/.config/bbbudget/settings.json` (`$BBBUDGET_SETTINGS`, `-settings FILE`). Each budget's own settings (base currency, months, debt strategy) stay on the budget page.

| Setting | Default | |
|---|---|---|
| `fetch_rates` | `true` | `false` is fully offline: nothing contacts a rate provider. |
| `default_rate_source` | `auto` | The source a newly added currency gets. |
| `auto_refresh` | `off` | `hourly` or `daily`: while `bbbudget serve` runs, refresh every budget's non-manual rates. The page shows the last run. |
| `coingecko_api_key` | — | Optional CoinGecko demo key. The file is written with mode 0600, and the key is never sent back to the browser or printed. |

```
bbbudget settings                                   # show
bbbudget settings -set auto_refresh=daily -set default_rate_source=ecb
```

A broken settings file is an error naming the file, never silently replaced by defaults. Settings are re-read on every use, so a change from the CLI applies to a running server at once.

### Crypto income

Add the coin as a currency and write the income line in it: `0.015` BTC. Amounts allow the coin's configured decimals (8 for BTC, 6 for USDT). Set `cash_out_fee_pct` to what selling costs you. Income is then valued at the market rate minus that loss, and rounded down like any income. For volatile coins (stablecoins excluded), the insights show the coin's share of income and what a 30% fall would do to the month.

```yaml
currencies:
  - {code: BTC, source: auto, per_base: 0.0000135, cash_out_fee_pct: 1.5}
lines:
  - {name: Freelance (paid in BTC), amount: 0.012, currency: BTC, category: income}
```

## The app

The UI has five views: **Overview** (where the money goes, ranked insights, the projection), **Lines** (income and costs grouped by category, amounts editable in place), **Plan** (debts, savings pots, planned withdrawals), **Scenarios** (try changes, save and compare them) and **Setup** (budget settings, currencies, categories).

- **Wide screens** show the views as tabs next to a summary that stays in view: what's left this month, savings at the end, goal and debt-free dates and the top warning. Every edit's effect shows up there straight away.
- **Phones and small tablets** get an app layout: a bottom tab bar, a pinned bar with the two key numbers, and bottom sheets for editing a line. Touch targets are at least 44px, and safe-area insets clear notches and the home indicator.
- **Installable.** The page ships a web app manifest and icons, so a phone or desktop browser can install it ("Add to Home Screen"); it then opens full screen like an app. The same layout is meant to be wrapped as an Android or iOS app later (for example with Capacitor or a Trusted Web Activity). A native shell needs a reachable server; this one only listens on localhost.
- Deletes can be undone for a few seconds. The current view is in the URL (`#home/lines`), so reloading keeps your place.

## Templates and your own data

Built in are `blank` and `example`, a fictional household that is also the test fixture. **Your own templates live outside the repository**, in `~/.config/bbbudget/templates/` (or `$BBBUDGET_TEMPLATES`, or `-templates DIR`), as YAML or JSON. The file name is the template name, and a file named like a built-in replaces it. Your budgets live in `~/.local/share/bbbudget/` (or `$BBBUDGET_DIR`, or `-dir`). Nothing personal ever needs to be in the repo.

```yaml
# ~/.config/bbbudget/templates/household.yaml
description: Our monthly budget
base_currency: EUR
extra_to_debt_pct: 50
currencies:
  - {code: USD, per_base: 1.08, transfer_fee_pct: 2}   # 2% lost on every USD payment
  - {code: GBP, source: ecb, per_base: 0.85}            # refreshed by `bbbudget rates`
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
pots:                                                    # kind: general | sinking | emergency
  - {name: General savings, balance: 1200}               # kind defaults to general; gets the leftover
  - {name: Emergency fund, kind: emergency, balance: 0, target: 5000}
  - {name: Holiday, kind: sinking, balance: 300, target: 1500, monthly: 100}
withdrawals:                                             # optional: planned use of savings
  - {name: New tyres, pot: general-savings, amount: 400, schedule: {start: 3}}  # once, in month 3
```

Older files with `savings_start` and `goals` still load: the balance becomes a *General savings* pot and each goal a pot with that target (an *emergency* pot if its name says so). Old goals all counted the same money; as pots, each fills on its own.

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

Tools: `list_budgets`, `list_templates`, `create_budget`, `get_budget`, `add_line`, `update_line`, `remove_line`, `set_currency`, `refresh_rates`, `update_settings`, `set_debt`, `apply_ops`, `what_if`, `save_scenario`, `compare_scenarios`, `adopt_scenario`, `delete_scenario`. There is also a resource, `budget://{id}`, with the raw document.

`-budget home` makes `home` the default, so "raise rent to 1200" works without naming the budget. Lines are referenced by id, exact name, or an unambiguous part of a name. An ambiguous reference ("school" matching both "School tuition" and "School supplies") is an error, not a guess. `what_if` never saves.

The web UI and the MCP server can run as separate processes on the same data directory. Writes are serialised with a file lock and guarded by revision numbers. The UI polls and picks up Claude's edits within a few seconds. If you were mid-edit when a change arrived, the UI reloads and tells you, rather than overwriting the other change.

## Layout

```
cmd/bbbudget/        CLI: serve | mcp | init | import | templates | list | report | rates | settings
internal/money/      Money, Decimal, Currency, rounding. No dependencies.
internal/budget/     Document model, validation (Compile → Plan), projection engine,
                     insight rules, ops (edits), scenarios, YAML/JSON documents, report
internal/rates/      Exchange-rate providers (ECB, currency-api, CoinGecko), chain + cache
internal/settings/   App-wide settings file (load, validate, atomic 0600 save)
internal/templates/  Built-in templates (embedded YAML) + the user template directory
internal/store/      Store interface + FileStore (atomic writes, flock, revisions)
internal/app/        Use cases shared by every transport
internal/httpapi/    JSON API + embedded UI + /mcp mount, localhost guards
internal/mcpserver/  MCP tools and resources
web/static/          The UI (vanilla JS, no build step): index.html (budgets), settings.html,
                     manifest.webmanifest and icons (icon.svg is the source of the PNGs)
```

Dependencies only point inward: `money` ← `budget` ← `store`/`app` ← transports. `rates` depends only on the standard library and is used by `app`. Only the transports know about HTTP or MCP.

### HTTP API

| | |
|---|---|
| `GET /api/budgets` · `POST /api/budgets` `{id, name, template}` or `{id, budget}` | list / create / import |
| `GET /api/budgets/{id}` → `{id, revision, budget, report}` | read |
| `PUT /api/budgets/{id}` `{revision, budget}` | replace; `409` if the revision is stale, `422` with `issues` if invalid |
| `POST /api/budgets/{id}/ops` `{ops, revision?}` | apply edits atomically |
| `POST /api/budgets/{id}/what-if` `{ops}` | preview, nothing saved |
| `POST /api/budgets/{id}/rates/refresh` `{codes?}` | fetch and save rates; `502` if no provider answered |
| `GET /api/rates?base=EUR&codes=USD,BTC&source=auto` | quote rates, nothing saved; `409` when fetching is off |
| `GET /api/settings` · `PUT /api/settings` `{fetch_rates?, default_rate_source?, auto_refresh?, coingecko_api_key?}` | app settings (the key is reported only as `coingecko_api_key_set`) |
| `GET /api/budgets/{id}/compare` · `POST …/scenarios` · `POST …/scenarios/{sid}/adopt` · `DELETE …/scenarios/{sid}` | scenarios |

The server binds to 127.0.0.1 and accepts only these requests:

- a localhost `Host` header (blocks DNS rebinding);
- JSON content types for mutations (blocks cross-site form posts);
- DELETE requests carrying an `X-Requested-With` header;
- bodies up to 1 MB.

## Extending

- **A currency.** Add it to the table in `internal/money/currency.go` (code, minor-unit digits, symbol, crypto/stablecoin flags). For a coin, add its id to `coinGeckoIDs` in `internal/rates/providers.go`.
- **A rate provider.** Implement `rates.Provider`, register it in `rates.Registry`, and add its name to `budget.RateSources`.
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
