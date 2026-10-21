---
name: ticket
description: Hand a GitHub issue of bbbudget to a Sonnet agent that builds it and opens a pull request, then have an Opus agent review that pull request. Records every decision on the issue and the pull request so the user can review cold. Use for "/ticket 12", "work on issue 12", "hand off #12", "what's next" on the backlog, or writing a new ticket.
---

# Ticket: issue to reviewed pull request

GitHub issues are the backlog. Labels say who can act:

| Label | Meaning |
|---|---|
| `ready` | Goal, Why and acceptance criteria are written, and the ticket has a feature. An agent may take it. |
| `in-progress` | An agent is building it. |
| `in-review` | A pull request is open and reviewed. Waiting for the user, who is assigned. |
| `needs-user` | Only the user can do it (merge, `gh pr ready`, resolve threads, force-push, decide). The user is assigned. |
| `blocked` | Waits on something else. The body says what. |
| `feature` | A goal that groups tickets. Never handed to an agent, and never given another label. |

`gh issue list --label ready` is the queue.

## Why this skill writes so much down

The user reviews pull requests days after the session that planned them, and that session's chat is gone by then. Whatever the user decided, and why, must be on GitHub before the session ends: on the issue, in the PR description, and beside the lines it explains. If the reason is only in chat, it is lost.

## Features

A feature is an issue with the `feature` label and three sections: **Why**, **Done when** and **Decisions**. Its tickets are sub-issues of it, so the feature shows progress and every ticket links up to it. List the features with `gh issue list --label feature`.

Every ticket belongs to exactly one feature. A ticket without one isn't ready.

## Decisions

A decision is one line in the issue's **Decisions** section:

```
- 2026-10-20: Struct tags are exempt from the 100-column rule. Moving descriptions out of tags made the code harder to read. (#86, f7be184)
```

It gives the date, what was decided, why, and where it was carried out. When the change doesn't exist yet, link the PR, or the issue if there is no PR yet, and add the commit hash once the change lands.

A decision gets recorded whenever:

- the user changes scope, overrides an acceptance criterion, or changes a rule during a session;
- the build agent makes a call that the acceptance criteria don't cover, such as keeping something or touching a file outside the stated scope;
- a review round changes the approach.

Record it on the issue before passing the change on to an agent. Edit only the Decisions section and leave the rest of the body as it is: `gh issue view <n> --json body --jq .body` to a uniquely named file, edit the file, then `gh issue edit <n> --body-file <file>`. A decision that changes the whole feature goes on the feature issue instead.

## Rule changes

A change to `CLAUDE.md`, `CONTRIBUTING.md`, CI, the Makefile or a skill is a rule change. It goes in its own commit and gets a decision line. In the PR description it is listed first, under **Decision for you**. Never fold a rule change silently into unrelated work.

## The board

Project 2 on `@me`, "bbbudget", shows every ticket grouped by feature. Labels are the source of truth. Whenever you change a ticket's label, also set its Status on the board:

| Label | Status |
|---|---|
| `ready` | Ready |
| `in-progress` | In progress |
| `in-review` | In review |
| `needs-user` | Needs user |
| `blocked` | Blocked |
| closed | Done |

```
gh project item-add 2 --owner @me --url <issue url> --format json --jq .id
gh project field-list 2 --owner @me --format json --jq '.fields[] | select(.name == "Status") | .options'
gh project item-edit --project-id PVT_kwHOAbzc384Bn6pB --field-id PVTSSF_lAHOAbzc384Bn6pBzhmdETM --id <item id> --single-select-option-id <option id>
```

`item-add` returns the existing item when the issue is already on the board.

## No issue number given

List the queue with `gh issue list --label ready` and `gh issue list --label needs-user`. Show both, grouped by feature, and suggest the oldest `ready` issue that nothing blocks. Stop there.

## With an issue number

1. **Check the ticket.** `gh issue view <n>`. It needs the `ready` label, a Why, an acceptance criteria list and a parent feature (`gh issue view <n> --json parent`). If any is missing, say what is missing and stop. Don't guess acceptance criteria.

2. **Mark it taken.** `gh issue edit <n> --remove-label ready --add-label in-progress`, and set the board Status.

3. **Build, on Sonnet.** Spawn one `general-purpose` agent with `model: "sonnet"` and `isolation: "worktree"`. Its prompt holds the full issue text, the feature's title, number and Why, and these rules:
   - Branch `issue-<n>-<short-slug>` from `origin/main`, unless the issue names another base branch.
   - Follow `CLAUDE.md` and `CONTRIBUTING.md`. Run `make check`, and `CHROME=/usr/bin/chromium make e2e` for UI changes.
   - Aim for under about 1000 added lines. If the work needs more, make one commit per step, each passing `make check`, and list the steps in the PR body.
   - A rule change goes in its own commit (see "Rule changes").
   - Never change the git identity. No Claude trailers or mentions in commits, the PR or its comments. No personal data anywhere.
   - Don't force-push, rewrite history, run `gh pr ready` or resolve review threads.
   - Push the branch and open a pull request with the description below.
   - Write every PR body or review body to a uniquely named file inside your own worktree, never to a shared scratchpad, so parallel agents can't overwrite each other's text.
   - Reply with the PR number, the `make check` result, anything left undone, any decision you made that the criteria don't cover, and the list of surprising changes (below).

4. **Annotate the surprises.** Post the build agent's list of surprising changes yourself, as one review with inline comments (see "Surprising changes"). Record its decisions on the issue, and update the PR description to match.

5. **Review, on Opus.** The user asked for Opus here, so this overrides the Sonnet default. Spawn a `general-purpose` agent with `model: "opus"`. Its prompt holds the PR number, the issue's Why, acceptance criteria and Decisions, the feature's Why, and these instructions:
   - Read the diff with `gh pr diff <pr>`. Check out the branch in a fresh worktree and run `make check`.
   - Check each acceptance criterion: met, partly met or not met, with evidence.
   - Check correctness, the rules in `CONTRIBUTING.md` and the readability rules in `CLAUDE.md`.
   - Check that every surprising change has an inline comment on the PR, from any review. A missing one is a blocking finding.
   - Check that the PR description's feature, Why and decisions match the issue. A mismatch is a blocking finding.
   - Check that no rule change is mixed into other work. A mixed one is a blocking finding.
   - Run `gh pr checks <pr>`. A failing check is a blocking finding.
   - Post the review with `gh pr review <pr> --comment --body-file <file>`, where the file is uniquely named and inside your own worktree. Approving isn't possible, because the same account opened the PR.
   - Reply with a verdict, `merge` or `changes needed`, and the blocking findings.

6. **Fix, at most twice.** On `changes needed`, send the blocking findings back to the Sonnet agent with `SendMessage`, then review again. Each fix round that changes the approach adds a decision line, and every new surprising change gets an inline comment, as in step 4. After two rounds, stop and hand the rest to the user.

7. **Check CI.** Run `gh pr checks <pr> --watch` yourself, and don't take the reviewer's word for it. A local `make check` doesn't count as CI.
   - Every check passes: the verdict stands.
   - A check fails: send the failure to the Sonnet agent. This counts as a fix round.
   - A check never started (no steps, failed within seconds, or an annotation about billing or runners): the verdict is `waiting on CI`. Tell the user why, quoting the annotation.

8. **Hand back.** `gh issue edit <n> --remove-label in-progress --add-label in-review`, and set the board Status. Comment on the issue with the PR link, the verdict and the CI result. Tell the user the PR number, the feature, the verdict and the CI result. Give `gh pr merge <pr> --squash` only when every check passed.

When the user gives direction in the session at any point, record it as a decision before acting on it.

## The PR description

The user reads the PR without opening the issue, so the description carries the why. It follows this order:

```
Closes #<n> · Part of #<feature>: <feature title>

**Feature:** <one line from the feature's Why>
**Why this ticket:** <the ticket's Why>

**Decision for you:** <rule changes and open calls; leave this line out when there are none>

**Decisions so far:**
- <the decision lines from the issue; "None." when there are none>

**Acceptance criteria:**
- <how each one is met>
```

Whenever a decision is added to the issue, update "Decisions so far" with `gh pr edit <pr> --body-file <file>`.

## Surprising changes

A change is surprising when a reader who has only read the issue would stop at it:

- it touches a file or symbol that the issue's scope doesn't name;
- it is a rule change;
- it keeps something the issue asked to remove, or skips part of a criterion;
- it is there because of a review round.

The build agent returns each one as the path, the line in the new file, and the reason in one or two sentences. A change made in a review round says which round and what was asked. Mechanical changes, such as moved code, renames and formatting, get no comment. A comment on every hunk would bury the ones that matter.

Post the list as one review:

```
gh api repos/{owner}/{repo}/pulls/<pr>/reviews --input <file>
```

where the file holds `{"event": "COMMENT", "body": "Why the surprising changes are here.", "comments": [{"path": "...", "line": 12, "side": "RIGHT", "body": "..."}]}`. Comments read as plain explanations from the author and never mention an agent or a tool. Leave comments on lines that a later fix moved or deleted. GitHub marks them outdated.

## Writing a new ticket

When the user describes work, find its feature first (`gh issue list --label feature`). If none fits, ask the user. Don't invent a feature.

Create the issue with the **Task** form's sections: Goal, Why, Context, Acceptance criteria, Out of scope, Decisions. Why says in one or two sentences why the ticket matters to its feature, in the words the user used in the session. Size it for one pull request of about 1000 added lines. Split it into several issues only when parts can land on their own.

Create it as a sub-issue with `gh issue create --parent <feature>`, and add it to the board. Add `ready` only when the acceptance criteria are checkable, and `needs-user` with `--add-assignee @me` when only the user can do it.

A new feature is the user's call. Create one only when they ask, with the **Feature** form's sections: Why, Done when, Decisions.
