# Contributing to bbbudget

Thanks for wanting to help! Bug reports, fixes, new currencies, insights and UI polish are all welcome.

## Before you start

- **Security issues** go through private reporting, not public issues. See [SECURITY.md](SECURITY.md).
- **Keep your real finances out of it.** Issues, PRs, test fixtures and screenshots are public forever. Reproduce with the fictional `example` template, or swap in made-up numbers, names and currencies before you share a budget.
- **For bigger changes** (a new storage backend, a new transport, model changes), please open an issue first so we can agree on the shape before you write the code.

## Setup

You need Go 1.25+ and nothing else: no Node, no database. The UI is plain JS with no build step.

```
git clone https://github.com/Ivanko1608/bbbudget
cd bbbudget
make hooks     # optional: leak checks on commit and push, see below
make run       # builds and serves http://127.0.0.1:8080
```

Use a throwaway data directory while developing, so you don't touch your real budgets:

```
./bin/bbbudget init home -template example -dir /tmp/bb
./bin/bbbudget serve -dir /tmp/bb
```

## Checks

`make check` has to pass before a PR is merged. It runs `gofmt`, `go vet`, `golangci-lint` (configured in `.golangci.yml`) and `go test -race ./...`. UI changes should also pass `make e2e` (needs Node 22+ and Chromium; CI runs it too). For changes to money parsing, also run the fuzzer for a bit:

```
go test ./internal/money -fuzz FuzzParseDecimal -fuzztime 30s
```

`make hooks` installs the [pre-commit](https://pre-commit.com) hooks: tinfoil and gitleaks on every commit, and tinfoil's check of commit authors and committers on every push (see [Development](README.md#development)). CI runs the same scans, so a skipped hook still gets caught before merge. If a hook flags a false positive, either commit with `--no-verify` or add its fingerprint to `.leakscanignore`.

## Ground rules for the code

These are what keep the numbers trustworthy, so PRs that break them won't be merged:

- **No floating point in money.** Amounts are `money.Money` (int64 minor units), and rates and percentages are exact rationals. Parse numbers from their text, never through `float64`.
- **Round pessimistically, once per conversion.** Money you pay rounds up and money you receive rounds down.
- **All calculations live in `internal/budget`.** Transports (HTTP, MCP, CLI, UI) call `app.App` and never re-implement maths.
- **Dependencies point inward:** `money` ← `i18n` ← `budget` ← `store`/`app` ← transports.
- **No user-facing text in code.** The domain emits message keys with typed arguments; the UI calls `t("key")`. Add every new key to all catalogs (`internal/i18n/locales/`, `web/static/i18n/`). If you can't translate it, copy the English text, and the tests will tell you if you missed a key, a placeholder or a plural form. Never put user data (item or category names) through the catalogs.
- **Nothing phones home.** No telemetry, analytics or CDNs, and no third-party requests from the UI (its CSP enforces this; don't loosen it). The only outbound requests are the rate providers in `internal/rates`, which are off until the user turns on `fetch_rates`, then run only when they ask or have turned on auto-refresh, and carry currency codes, never budget data. Keep it opt-in.
- **Tests come with behaviour changes.** Projection changes need a hand-checked golden or an invariant, not just "the output looks right".
- **Built-in templates are generic and fictional.**
- **Never leak internal errors** (stack traces, file paths, storage errors) to API clients. Log them server-side and return a generic message.
- **Validate all external input at the boundary:** query params, path values, JSON bodies. Reject bad input with 400, and enforce the limits before it reaches storage: the budget id pattern and the 1 MB body cap (see [SECURITY.md](SECURITY.md)).
- **Cap request body size** (`http.MaxBytesReader`) with a limit derived from the payload, and document how it was derived in a comment next to the constant.
- **Use `json.Decoder.DisallowUnknownFields()`** for write endpoints.
- **Parameterized queries only.** Never build SQL by concatenating input.
- **NULLable DB columns map to nullable Go types** (`sql.Null*`, pointers).
- **Write a response only after it is fully built:** marshal first, then set headers and status, then write.
- **Graceful shutdown uses a fresh timeout context,** not the already-cancelled signal context.

The README's [Extending](README.md#extending) section shows where currencies, insights, edit types and storage backends plug in.

## Pull requests

- Keep each PR to one topic. A small focused PR gets reviewed much faster than a big one.
- Say what changed and why, and how you tested it. For UI changes, a screenshot using the example budget helps.
- Update the README when you change user-visible behaviour, CLI flags, the API or MCP tools.

## License

bbbudget is licensed under the [GNU AGPL v3.0 or later](LICENSE). By contributing, you agree that your contribution is licensed under the same terms. You keep your copyright; there's no CLA.

Contributions to `mobile/` also carry the additional permission under AGPL section 7 in [mobile/LICENSE-EXCEPTION.md](mobile/LICENSE-EXCEPTION.md), which allows distribution through app stores whose terms add restrictions, as long as the complete source is offered under the same terms.
