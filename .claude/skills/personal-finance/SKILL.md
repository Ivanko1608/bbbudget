---
name: personal-finance
description: Household budgeting rules for bbbudget — cash flow vs. balances, savings pots (emergency fund, sinking funds, general savings), what to do when a month is short, debt vs. savings trade-offs, and how to word money advice. Use when changing the projection engine, savings/pots/goals, shortfall handling, insights, or any user-facing money wording in this repo.
---

# Personal finance rules for bbbudget

The engine plans a household's money month by month. These rules decide what
the numbers mean. When code and these rules disagree, the code is the bug.

## 1. Flows are not balances

Keep two separate things, as accountants keep an income statement apart from a
balance sheet:

- **Flows** happen in a month: income, costs, debt payments, contributions to
  savings, withdrawals from savings.
- **Balances** are what you hold at a point in time: each savings pot, each
  debt, the current account.

Consequences:

- **Savings are never income.** Money taken from a pot is a *transfer from a
  balance*, shown on its own line ("From savings"), never added to income and
  never used to make a month look affordable.
- **A month's result is income minus spending.** A short month stays short
  even if savings could cover it. The plan says so plainly.
- Totals per month must reconcile:
  `income + from savings − costs − debt payments − to savings = change in cash`.

## 2. Savings live in pots

Savings are split into named pots with a purpose. Common kinds:

| Kind | Purpose | Use to cover a short month? |
|---|---|---|
| Emergency fund | Job loss, illness, urgent repairs. Usually 3–6 months of essential costs. | Only as a last resort, and the plan must say it is an emergency use. |
| Sinking fund | Saving ahead for a known cost: holiday, car, school fees, insurance. | Only for the cost it was saved for, otherwise flag that it delays that cost. |
| General savings | Unassigned money. | Yes, first choice. |

Rules:

- One euro sits in exactly one pot. Two goals must not both count the same
  money (no double counting).
- A pot may have a target and a monthly contribution. Leftover money that no
  rule assigns goes to the default pot (general savings), not into thin air.
- Pots never go below zero. A withdrawal larger than the pot is an error.

## 3. When a month is short

Never move money out of savings silently. Instead:

1. Report the shortfall: "October is €350 short."
2. List what could cover it, in this order, with what is available:
   general savings → sinking funds (saying which planned cost it delays) →
   emergency fund (marked as a last resort). Pots kept out of shortfalls are
   not offered.
3. Let the user choose: a planned withdrawal from a pot is a real, visible
   transfer that they add (or accept from the suggestion).
4. If nothing covers it, the shortfall carries into the next month as money
   owed on the current account (an overdraft), and next month's surplus pays
   it back before anything is saved. Say that this usually costs interest or
   fees in real life.

## 4. Order for a surplus

A typical order, which the plan follows unless the user changes it:

1. Repay any overdraft from earlier months.
2. Minimum debt payments (already in spending).
3. Fixed monthly contributions to pots.
4. The chosen share of what is left to extra debt payments.
5. The rest to the default pot.

Debt vs. saving: paying off a debt at APR *x*% is a guaranteed *x*% return.
An emergency fund comes before aggressive extra payments on low-interest debt;
high-interest debt (credit cards) usually beats saving beyond a small buffer.
State these as trade-offs, not orders.

## 5. Wording

- Plain, specific and calm: "You're €350 short in October. You could take it
  from General savings (€1,200 there)." Not "Warning! Budget exceeded!".
- Name the consequence of a choice: "Taking it from the Holiday fund means the
  holiday is covered in March instead of February."
- Never present a guess as a fact, never promise returns, and don't give tax
  or investment advice. Say "you could", not "you should", for choices that
  depend on the person's situation.
- Amounts are exact, in the base currency, with the month named.
