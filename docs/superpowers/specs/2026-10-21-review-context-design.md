# Review context: the why travels with the work

## Intent

Development happens through the `/ticket` skill: a Sonnet agent builds an issue into a pull
request, an Opus agent reviews it, and the user merges. The user is the only human reviewer.

The user loses the thread when reviewing. Issues were planned in earlier sessions, and course
changes happen mid-ticket. Both kinds of decision live only in session chat, which is gone by the
time the PR is reviewed. GitHub keeps what was decided, never why. So the user opens an issue,
clicks through to the PR, tries to rebuild the reasoning from memory, finds changes that look
odd, and ends up merging on trust.

The fix is to write every decision down where the user already looks, at the moment it is made,
without the user having to remember to do it. The user reviews on GitHub and wants to keep doing
so.

Success: the user opens any PR cold and understands it in a minute or two. They know which
feature it serves, why this ticket exists, and why each surprising change is there, without
leaving the PR page and without remembering anything.

## Goals

- Every ticket belongs to a feature, and the feature says why it exists.
- Every decision made mid-ticket is recorded on the issue, with its reason and a link.
- Every PR opens with the feature, the ticket's why and its decisions, so the user never has to
  click back to the issue.
- Every surprising change in a PR has an inline comment on that line explaining it.
- One board shows all work grouped by feature, the way a Jira board groups by epic.
- Existing open issues and PRs are backfilled to the same standard.

## Non-goals

- Reducing the number of open PRs. The user said 8 waiting PRs feels fine once each PR carries
  its context and the feature is visible. No limit on work in progress.
- A separate review tool or page. Review stays on GitHub (option A of the mockups).
- Changing how the build and review agents write code.

## Anti-goals

- Noise. Comments on mechanical changes (moved code, renames, formatting) bury the ones that
  matter. If every hunk gets a comment, the comments stop meaning anything.
- Asking the user to do the bookkeeping. If recording a decision depends on the user remembering
  to, it will not happen.
- A second source of truth that drifts. Labels stay the queue that `/ticket` reads; anything
  else is kept in sync by the skill, not by hand.
- Personal data. The repo is public, so issues, decision logs and PR comments are public too.

## The design

### Features

A feature is a parent issue. Its tickets are GitHub sub-issues of it, so the parent shows
progress ("9 of 24 done") and each ticket links up to it.

The features are the two goals the project has today: code readability, and the plan in
`docs/superpowers/specs/2026-10-21-mobile-apps-design.md` (on the `user/planner` branch). That
plan already splits its work into ordered steps, and each step becomes a feature. Their Why and
Done when are in [Feature Whys](#feature-whys).

| Feature | Takes over |
|---|---|
| Code readability | Everything in the "Code review fixes" milestone, and #64 to #67. |
| Remote server | PRs #18 and #19. |
| Flutter app | Nothing yet. |
| Assistant | Nothing yet. |
| Hosted service | Nothing yet. |
| Store release | Nothing yet. |

The mobile plan's sixth step, "Later" (push and web sign-in), isn't a feature yet. It becomes one
when it gets its own design.

Prompt injection security is part of the Assistant feature, as the plan's "Untrusted text"
rules, and not a feature of its own. *My call.*

The mobile plan calls these steps "Milestones". When that plan lands on `main`, its heading
becomes "Features" so that one concept has one name. *My call.*

A feature issue carries the label `feature` and has three sections:

- **Why**: the problem it solves and why now, in a short paragraph.
- **Done when**: what is true when the feature is finished.
- **Decisions**: feature-level decisions, in the same line format as tickets (below).

Feature issues carry only the `feature` label, never a status label, so the `ready` queue never
lists them. They are never handed to `/ticket`; only their sub-issues are. *My call.*

### Tickets

The Task issue form (`.github/ISSUE_TEMPLATE/task.yml`) gains:

- **Why**: one or two sentences on why this ticket matters to its feature. This is separate from
  Context, which keeps the facts and where to look.
- **Decisions**: empty at creation. It is filled in as the work goes.

Sub-issue linking can't be done from an issue form, so `/ticket` sets the parent when it writes
the ticket. A `ready` ticket must have a parent feature, a Why, and checkable acceptance
criteria. Otherwise `/ticket` refuses to start it, the same way it refuses today without
acceptance criteria.

The gate applies once the backfill is done; the skill change lands after it. A ticket the user
leaves without a feature can't be started until it is placed. *My call.*

New tickets get no milestone, and the skill's "Milestones group issues by goal" line is replaced
by the feature rule.

### Decision lines

A decision is one line in the issue body's Decisions section:

```
- 2026-10-19: Descriptions stay in struct tags, and CLAUDE.md exempts lines that can't be split. Moving them out made the code worse. (#86, 806030d)
```

It holds the date, what was decided, why, and a link to where it was carried out. When the line
is written before the change exists, it links the PR (or the issue, before a PR exists), and the
commit hash is added once the change lands. *My call.* The section is
in the body rather than in comments, so it reads top to bottom and the PR description can copy
it whole.

Decisions are recorded:

- **By `/ticket`, in the session, when the user changes course.** Any instruction from the user
  that changes scope, overrides an acceptance criterion, or changes a rule counts. The skill
  appends the line before passing the change on to the build agent.
- **By the build agent**, when it makes a judgement call that the acceptance criteria don't
  cover, such as keeping an import or touching a file outside the stated scope.
- **When a ticket is written** in a planning session, the reasoning from that conversation goes
  into Why, not into chat.

### Rule changes are their own decision

A change to `CLAUDE.md`, `CONTRIBUTING.md`, CI config or the skills is a rule change. It is never
folded silently into an unrelated PR. It goes in its own commit, gets a decision line, and is
listed first in the PR description under **Decision for you**. The `CLAUDE.md` exemption made in
#86 is the case this prevents.

### The PR description

The build agent writes the body in this order:

```
Closes #51 · Part of #<feature>: Code readability

**Feature:** <one line from the feature's Why>
**Why this ticket:** <the ticket's Why>

**Decision for you:** <rule changes or open calls; omitted when there are none>

**Decisions so far:**
- <decision lines copied from the issue>

**Acceptance criteria:**
- <how each one is met, as today>
```

`/ticket` refreshes the "Decisions so far" block whenever it adds a decision during a fix round.

### Inline comments on surprising changes

After opening the PR, the build agent posts inline review comments on the lines that would
surprise a reader who has only read the issue. A change counts as surprising when it:

- touches a file or symbol the issue's scope doesn't name;
- is a rule change;
- keeps something the issue asked to remove, or skips part of a criterion;
- is there because of a review round (the comment says which round and what was asked).

Mechanical changes get no comment. Each comment states the reason in one or two sentences. They
are posted as a single review with the `COMMENT` event, so they show in the Files tab beside the
line. All comments come from the user's own account, so they read as plain explanations and
don't mention any tool.

The orchestrator posts them, from a list of `path`, `line` and reason that the build agent returns,
so the agent never needs to know review ids. Each fix round that adds a surprising change returns a
new list and gets a new review. Comments on lines a fix moved or deleted are left; GitHub marks them
outdated. *My call.*

### Review

The Opus reviewer gains two checks, and a gap in either is a blocking finding:

- every surprising change (by the list above) has an inline comment, from any review on the PR
  (the builder's and the reviewer's share one account, so the author doesn't tell them apart);
- the PR description's feature, Why and decisions match the issue.

### Board

One GitHub Project, "bbbudget", shows every ticket grouped by parent feature, with a column per
status: `ready`, `in-progress`, `in-review`, `needs-user`, `blocked`, plus `done` for closed
tickets, hidden from the default view. Feature issues and PRs are not cards; a ticket's PR is
reached from the ticket. Labels remain the source of truth. Whenever `/ticket` changes a label,
it also sets the Project status. The builder creates the Status options if the reused project
lacks them. The `gh` token already has the `project` scope, so this runs unattended.

The existing project that already holds #45, #47 and #50 is renamed and reused. The other
untitled project is left alone for the user to delete or keep.

### Milestones

Features replace milestones as the grouping. After the backfill:

- "Code review fixes" is closed, once all its issues sit under Code readability.
- "AI-first stack" has no open issues and is closed.
- "Cleanup" keeps #32 until that issue is done, then closes.

## Backfill

This is a one-time pass, run as its own `/ticket`-style job, not by hand:

1. Create the six feature issues with the Why and Done when given in [Feature Whys](#feature-whys).
2. Attach every open ticket as a sub-issue of its feature. Code review fixes and the
   test-assertion tickets (#64 to #67) go under Code readability. Issues that fit no feature
   (#32, #90, and anything else unclear) stay unattached and show in a "No feature" group on the
   board for the user to place.
3. For each open ticket, write its Why and rebuild its Decisions from the PR review comments,
   the issue comments and the commit messages. A Why or decision that can't be traced to a
   written source is marked `(reconstructed)` so the user knows to check it.
4. For each open PR (#82 to #88, #91), rewrite the description into the new shape and post the
   inline comments on surprising changes. In #86, list the `CLAUDE.md` change under Decision
   for you.
5. Set up the board and close the milestones as above.

The old PRs #18 (accounts) and #19 (remote mode) predate the ticket flow and have no issue. The
backfill opens one ticket for each, under Remote server, using the mobile plan's "Server changes"
section as the Why. Each PR then gets a `Closes` line and the new description. Otherwise, PRs get
their feature through the issue they close.

## Feature Whys

The feature issues are created with these texts. The mobile features link to the matching
section of the mobile plan, which holds the detail.

**Code readability.** Why: a stranger can read any file on the first pass without comments to lean
on, so reviewing agent-written PRs stays fast and the code stays safe to change. Done when: every
file passes the `CLAUDE.md` checklist, and new PRs are held to it in review.

**Remote server.** Why: the phone apps are thin clients, so they need a server they can reach. Self
hosting from a phone has to work before anything else is built on it. Done when: the mobile plan's
step 1 is done. That covers #18 and #19 rebased, devices per account, QR pairing with
`public_url`, rate-limited pairing, the API version in `/api/meta`, and the HTTPS docs.

**Flutter app.** Why: people pick up their phone with questions, not data entry. A native app is
where the assistant becomes the heart of bbbudget. Done when: the mobile plan's step 2 is done,
including the Play closed test running, so the 14-day clock has started.

**Assistant.** Why: the assistant answers with exact numbers and with judgment, remembers the
person's situation and speaks up when a month looks tight. It reads text the person didn't write,
so it must resist prompt injection. Done when: the mobile plan's step 3 is done, with the
"Untrusted text" rules in place.

**Hosted service.** Why: subscribers should sign in and have everything work, with no URL, key or
model to set up. That is also how the project pays for itself. Done when: the mobile plan's step 4
is done, legal footing included.

**Store release.** Why: people install apps from the stores, and the store rules shape the
schedule. Done when: the mobile plan's step 5 is done and both apps are live.

## Constraints

- The repo is public. Nothing personal goes into issues, decisions, comments or the board.
- No mention of Claude in commits, PR descriptions or PR comments.
- One concept, one name: "feature", "ticket", "decision" and "Why" mean the same thing in the
  skill, the issue form, the PR description and the board.
- The skill keeps its current rules: Sonnet builds, Opus reviews, two fix rounds, CI checked by
  the orchestrator.

## Left to the builder

- How the Project status is set from the skill (`gh project item-edit` or GraphQL), and how
  sub-issues are linked (`gh` support or the REST sub-issues endpoint).
- The wording of the new skill steps, and whether the build agent's inline comments are posted
  by the agent or by the orchestrator from a list the agent returns.
- How the backfill is split into runs, and in what order.
- Whether the Decisions section is edited with `gh issue edit --body-file` or another way, as
  long as the rest of the body is left unchanged.
