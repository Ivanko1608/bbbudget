# Assistant evals

Each `*.yaml` file here is a conversation with the in-app assistant and checks on what it should have done.

`make eval` runs every case against a real model (the one your settings choose, or `ANTHROPIC_API_KEY`) and prints a pass rate. It costs money and isn't deterministic, so it is never part of `make check` or CI. `EVAL_RUNS=5 make eval` repeats each case five times.

Every case starts from a fresh budget made from a built-in template, on a fixed date (8 October 2026).

```yaml
name: raise-rent                     # defaults to the file name
template: example                    # the budget to start from (fictional)
turns:                               # what the person says, in order
  - Raise rent to 1200
expect:
  saved: true                        # did anything get written?
  writes_by: assistant               # every write attributed to the assistant
  items:                             # by id: fields the item must have
    rent: {amount: "1200"}
  no_item_named: [gym]               # items that must not exist
  transactions: 1                    # how many logged in total
  transaction: {category: home, amount: "45.2"}  # one logged transaction must match
  reply_script: cyrillic             # the last reply is written in this script
  reply_contains: ["5100"]           # the last reply mentions each of these (any number format)
```
