# Working on bbbudget

CONTRIBUTING.md has the hard rules (exact money, maths only in `internal/budget`, dependencies point inward). Run `make check` before calling anything done, and `make e2e` for UI changes.

This file is about one thing: **code that a stranger understands on the first read, without comments to lean on.** If code needs a comment to be understood, fix the code first.

## Structure

- **A function reads as a list of steps.** The top-level function says *what* happens, and named helpers say *how*. If you want to put a comment above a block to say what it does, extract the block into a function named after that comment.
- **One job, one level of detail.** Don't mix "decide what to do" with byte-level work in the same function. Roughly 40 lines is the point to ask what should come out.
- **Shape data the way it's read.** If readers have to work out what `xs[i+1]` or `xs[:i]` means, give the pieces names, or store the data in the order it's used.
- **Explicit inputs.** Pass what a function needs as parameters. Use `context` for cancellation and deadlines, not for business data that changes the result.

## Expressions

- **Name conditions.** A condition with more than two terms becomes a named variable or a predicate function:

  ```go
  // hard to read
  if last.Autosave && last.Source == c.Source && last.Revision == next.Revision-1 && next.UpdatedAt.Sub(last.At) < coalesceWindow {
  // reads itself
  if continuesAutosave(last, next) {
  ```

- **No meaningless arguments at the call site.** `Undo(ctx, id, 2, true)` says nothing about what `true` means. Use an options struct or a named type. Special values like "0 means latest" get a named constant (`LatestRevision`).
- **No cleverness.** Write the obvious version: `filepath.Join(dir, id+".jsonl")`, not `path(id) + "l"`. No JSON round trips as a form of reflection, no rune arithmetic to build strings, and no multi-assignments of unrelated values on one line.
- **Use the standard library** (`slices.Equal`, `slices.Sort`, `maps.Keys`, `strings.Cut`) instead of hand-rolled loops that do the same thing.
- **Keep lines under about 100 characters.** Split long calls one argument per line, and pull long closures out into named functions. Lines that cannot be split sensibly are exempt, such as struct tags and long string literals.
- **Don't shadow.** In particular, don't declare `err` inside a closure when the outer function has a named `err` result.
- **Keyed struct literals.** A literal with more than three fields, or any bool field, names its fields. `{"EUR", 2, "€", true, false, false}` tells the reader nothing.
- **Don't name variables after packages.** No `fs`, `log`, `errors` or `time` as locals.

## Names

- **Length follows scope.** One letter is fine for a loop index or a three-line closure. Anything that lives longer gets a word: `newBudget`, not `nb`; `entries`, not `h`.
- **Use domain words, consistently.** Don't invent metaphors ("doors" for transports). One concept gets one name everywhere: Go, JSON, UI and docs.
- **Fixed sets of strings are typed constants**, defined once in the lowest package that owns them, never repeated as literals. Every other layer consumes them, JS included: a literal `"set_item"` outside `internal/budget` is a bug.
- **Unnamed results of the same type** (`func(T) (string, string)`) need names or a small struct.

## Comments

Few and short.

- Exported identifiers get one sentence that states the contract.
- Inside a function, comment only a *why* the code can't show: a non-obvious ordering, a lock, a deliberate trade-off.
- Never: restate the code, label blocks, justify why a function exists, or refer to branches, PRs or history. That belongs in the commit message.
- Leave existing comments alone unless your change makes them wrong.

## Tests

- **A test reads like a spec.** Arrange, act and assert, with blank lines between them. Helpers say what they do in domain terms (`setRent("1200")`).
- **One assertion per fact.** Don't put five checks in a single `if a || b || c` with one message. When it fails, nobody can tell which check broke.
- **Table-driven tests** when cases share a shape.
- **Every error in setup code is checked.**

## JavaScript (`web/static`)

- Small render functions.
- Build strings in named variables before you interpolate them.
- No ternaries nested inside template literals.
- No money arithmetic. Floats are for pixels only. Every number shown as money arrives from the server already computed: totals, remainders, percentages.
- No copies of server lists. Rate sources, strategies and other option lists come from `/api/meta`, never from a literal array in JS.

## Tooling

- **Reuse before building.** Before writing a script, hook, CI step or helper, check the standard library, `go.mod`, installed skills and plugins, and well-known tools (gitleaks, pre-commit hooks, GitHub Actions). The leak scan is tinfoil's `leakscan.py`: extend tinfoil, don't copy it here.
- Write your own only when nothing fits, and say in the PR what you checked and why it didn't fit.

## Pull requests

- **Aim for under about 1000 added lines.** It's a target, not a limit. When a PR is bigger, its description says why.
- **A big PR is reviewed commit by commit.** Each commit is one step that passes `make check` on its own, and the description lists the steps in order.
- **Mechanical changes don't count** toward the size: renames, moved files, goldens, fixtures and generated files.
- **Stack PRs only when part of the work should land first**, not to stay under a line count.

## Before you finish

Reread your diff as someone who has never seen it.

- For each comment, ask whether the code could make it unnecessary.
- For each long line or long condition, ask what name it's missing.
