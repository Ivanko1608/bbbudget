---
name: ticket
description: Hand a GitHub issue of bbbudget to a Sonnet agent that builds it and opens a pull request, then have an Opus agent review that pull request. Use for "/ticket 12", "work on issue 12", "hand off #12", or "what's next" on the backlog.
---

# Ticket: issue to reviewed pull request

GitHub issues are the backlog. Labels say who can act:

| Label | Meaning |
|---|---|
| `ready` | Goal and acceptance criteria are written. An agent may take it. |
| `in-progress` | An agent is building it. |
| `in-review` | A pull request is open and reviewed. Waiting for the user, who is assigned. |
| `needs-user` | Only the user can do it (merge, `gh pr ready`, resolve threads, force-push, decide). The user is assigned. |
| `blocked` | Waits on something else. The body says what. |

Milestones group issues by goal. `gh issue list --label ready` is the queue.

## No issue number given

List the queue with `gh issue list --label ready` and `gh issue list --label needs-user`. Show both, and suggest the oldest `ready` issue that nothing blocks. Stop there.

## With an issue number

1. **Check the ticket.** `gh issue view <n>`. It needs the `ready` label and an acceptance criteria list. If either is missing, say what is missing and stop. Don't guess acceptance criteria.

2. **Mark it taken.** `gh issue edit <n> --remove-label ready --add-label in-progress`.

3. **Build, on Sonnet.** Spawn one `general-purpose` agent with `model: "sonnet"` and `isolation: "worktree"`. Its prompt holds the full issue text plus these rules:
   - Branch `issue-<n>-<short-slug>` from `origin/main`, unless the issue names another base branch.
   - Follow `CLAUDE.md` and `CONTRIBUTING.md`. Run `make check`, and `CHROME=/usr/bin/chromium make e2e` for UI changes.
   - Aim for under about 1000 added lines. If the work needs more, make one commit per step, each passing `make check`, and list the steps in the PR body.
   - Never change the git identity. No Claude trailers or mentions in commits or the PR. No personal data anywhere.
   - Don't force-push, rewrite history, run `gh pr ready` or resolve review threads.
   - Push the branch and open a pull request whose body starts with `Closes #<n>`, then lists how each acceptance criterion is met.
   - Write every PR body or review body to a uniquely named file inside your own worktree, never to a shared scratchpad, so parallel agents can't overwrite each other's text.
   - Reply with the PR number, the `make check` result and anything left undone.

4. **Review, on Opus.** The user asked for Opus here, so this overrides the Sonnet default. Spawn a `general-purpose` agent with `model: "opus"`. Its prompt holds the PR number, the issue's acceptance criteria and these instructions:
   - Read the diff with `gh pr diff <pr>`. Check out the branch in a fresh worktree and run `make check`.
   - Check each acceptance criterion: met, partly met or not met, with evidence.
   - Check correctness, the rules in `CONTRIBUTING.md` and the readability rules in `CLAUDE.md`.
   - Run `gh pr checks <pr>`. A failing check is a blocking finding.
   - Post the review with `gh pr review <pr> --comment --body-file <file>`, where the file is uniquely named and inside your own worktree. Approving isn't possible, because the same account opened the PR.
   - Reply with a verdict, `merge` or `changes needed`, and the blocking findings.

5. **Fix, at most twice.** On `changes needed`, send the blocking findings back to the Sonnet agent with `SendMessage`, then review again. After two rounds, stop and hand the rest to the user.

6. **Check CI.** Run `gh pr checks <pr> --watch` yourself, and don't take the reviewer's word for it. A local `make check` doesn't count as CI.
   - Every check passes: the verdict stands.
   - A check fails: send the failure to the Sonnet agent. This counts as a fix round.
   - A check never started (no steps, failed within seconds, or an annotation about billing or runners): the verdict is `waiting on CI`. Tell the user why, quoting the annotation.

7. **Hand back.** `gh issue edit <n> --remove-label in-progress --add-label in-review`. Comment on the issue with the PR link, the verdict and the CI result. Tell the user the PR number, the verdict and the CI result. Give `gh pr merge <pr> --squash` only when every check passed.

## Writing a new ticket

When the user describes work, create the issue with the **Task** form's sections: Goal, Context, Acceptance criteria, Out of scope. Size it for one pull request of about 1000 added lines. Split it into several issues only when parts can land on their own. Add `ready` only when the acceptance criteria are checkable, and `needs-user` with `--add-assignee @me` when only the user can do it.
