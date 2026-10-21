# Mobile apps, remote servers and hosted bbbudget

## Why

bbbudget is for people who struggle to keep their money in check and have
given up on budgeting apps because logging every purchase is a chore. Some of
them care about privacy and want to host it themselves.

They pick up their phone with questions, not data entry:

- "Can I afford a 500 purchase this month?"
- "What can I cut to save up for this?"
- "Why am I in such a hole?"
- "What's the fastest way out of my debt?"

The assistant is the heart of the phone app. It answers with exact numbers
from the budget engine *and* with judgment: "buying this would really help
with your job", "this isn't great for money, but you need it right now",
"you're tight this year, cut Christmas presents back a little; want help
picking some?". It also speaks up on its own when a month looks tight.

Later the project sells a subscription: we host the server, people install the
app, sign in, and everything works.

## Goals

1. Native Android and iOS apps, built with **Flutter** from one codebase.
2. The apps are thin clients. They work against either:
   - **bbbudget hosting**: sign in, nothing else to set up;
   - **the user's own server**: pair with a one-time code, no account.
3. The in-app assistant works from the phone in every mode, with the provider
   the user chooses:
   - **included** with the hosted subscription, under a fair-use cap;
   - **bring your own key (BYOK)** for any provider, hosted or self-hosted;
   - a **connector** on hosting: bbbudget as an MCP server inside the user's
     own Claude or ChatGPT app, which is the only way to use what those apps
     already know about the person.
4. The assistant remembers the person's situation (job, hard times, upcoming
   events) and sends **nudges** that the user can turn off.
5. A hosted service with sign-up, billing, metering and the usage cap.

## Non-goals

- Running the budget engine on the phone. Every number comes from a server.
- Bank sync.
- Training or self-hosting our own model. Cheap hosted models fit the margin.
- A wrapped web view (Capacitor, TWA). Wrapped apps feel off, and we rejected them.
- Fully separate Apple-style screens on iOS. See "Look and feel".

## Anti-goals: failure even if it works

- **Onboarding friction for subscribers.** If a hosted user has to type a URL,
  create an API key or pick a model before their first question, we failed.
- **Forced sign-in.** Only the hosted service has accounts. Self-hosters never
  create one.
- **Money maths in the app.** Dart, like JS today, only formats what the server
  computed (CONTRIBUTING.md rule, extended to `mobile/`).
- **Overclaiming privacy.** The hosted server must read budgets to do the
  maths, so it is not end-to-end encrypted. Never say or imply it is.
- **Budget amounts in push notifications.** Push text passes through Apple's
  and Google's servers.

## Constraints

- The repo rules apply to new code in every language: CLAUDE.md, CONTRIBUTING.md,
  `make check`, and dependencies pointing inward.
- AGPL-3.0-or-later for the server. Self-hosting stays free. The licence for
  `mobile/` is open; see "Open".
- No real personal data in fixtures, screenshots or store listings.
- No telemetry, as the README promises. That rules out Firebase and analytics
  SDKs in the app.
- iOS builds need Xcode on macOS. The maintainer works on Arch Linux, so iOS
  builds and signing run on GitHub Actions macOS runners.
- Both platforms block plain HTTP to non-local hosts by default: iOS through App
  Transport Security, Android 9+ through its network security config. A
  self-hosted server reached from a phone needs HTTPS. The docs already point
  to Tailscale serve, Caddy or nginx.
- Apps in the stores lag behind servers. Any server may meet an older app, and
  the reverse.
- Store publishing rules that shape the schedule:
  - A Google Play personal account needs a closed test with 12 testers opted
    in for 14 days in a row before it can publish to production.
  - EU law (DSA) makes anyone who earns money from an app a "trader", who must
    publish an address, phone number and email on the store page.
  - Apple requires in-app account deletion (guideline 5.1.1(v)).
  - Apple requires a privacy-preserving login option, such as Sign in with
    Apple, when other third-party logins are offered (guideline 4.8).
  - Google Play needs the Financial features declaration, and Play Billing
    Library 8 for every update from 31 August 2026.

## Starting point

- **Server:** the single-user Go binary. All maths lives in `internal/budget`, and
  the JSON API is in `internal/httpapi`. The assistant runs on the server and
  streams over `POST /api/budgets/{id}/chat` (SSE). Its provider is a server
  setting: `anthropic` (default model `claude-opus-5-5`) or `openai-compatible`,
  with the key never sent to clients. It listens on localhost only, with no
  authentication. Conversations live in server memory only.
- **Web UI:** it already has a phone layout. It stays as it is.
- **PR #18** (open, stale since Oct 15) adds `internal/accounts`: one `App`,
  data directory and settings file per account, nested as
  `<root>/<account>/budgets/`. Nothing calls it yet.
- **PR #19** (open, stacked on #18) adds `serve -remote`, bearer tokens for
  paired devices on `/api/` and `/mcp`, `bbbudget pair` (one-time 50-bit code,
  10 minutes, single use) and `bbbudget devices`. Devices live in one
  `devices.json` per server, not per account. Tokens never expire.
- **The mockup:** a throwaway Flutter mockup showed the direction, and the
  maintainer approved it: connect screen, Overview, Ask and Plan. It isn't in
  the repo. The next section describes its look.

## Look and feel (decided)

- A brand style of our own, neither stock Material nor stock iOS:
  - flat cards with hairline borders on an off-white page (near-black in dark mode);
  - dark ink text with the yellow `#ffd60a` accent;
  - large bold page titles.
- **Adaptive where people notice the platform:**
  - **iOS:** the Cupertino tab bar and iOS icons, no ink ripple, bouncing scroll,
    swipe-back, iOS switches and dialogs.
  - **Android:** the Material navigation bar and switches.
- Light and dark follow the system.
- Phones only for now: no tablet layout. Hosted is phone-only at launch, and
  the store listing says so. *(My call.)*
- Tabs: **Overview** (nudge card, the two key numbers, where the money goes,
  insights), **Ask** (chat with suggestion chips and the tools each answer
  used), **Plan** (debts, savings pots, nudge toggle).

## How it fits together

```
Flutter app ──HTTPS + bearer token──▶ bbbudget server ──▶ AI provider
                                       (self-hosted or      (included, BYOK,
                                        hosted, per account)  or Ollama)
Claude / ChatGPT app ──MCP + OAuth──▶ /mcp on the hosted server only
```

### Connecting

- **First screen:** two choices, "Use bbbudget hosting" and "Connect to my own server".
- **Hosted:** sign in. That creates the account (if needed) and a device token
  in one step. The first budget starts from a template, and the assistant is
  ready with no setup.
- **Self-hosted:** `bbbudget pair` prints the one-time code **and a QR code**
  holding the server's public URL plus the code. The server can't know its
  public URL when it sits behind Tailscale or a reverse proxy, so it comes from
  a `public_url` setting or a `pair -url` flag, and `pair` refuses to print a QR
  code without one. The app scans the QR code, or the user types the URL and
  code. No account.
- The connect screen refuses `http://` URLs with a message that links to the
  HTTPS docs.
- `POST /api/pair` is rate-limited per client address.
- The app keeps the token in the platform's secure storage (Keychain or Keystore).
- Native apps don't need CORS. The browser UI on a remote server needs the
  sign-in from #19's "Not in this PR" list, but that isn't required for the apps.

### Server changes

- Rebase #18 and #19 onto main. Neither is merged or released, so their
  designs may change freely in review.
- One code path for both modes. Hosted is the same binary serving many
  accounts through `internal/accounts`. A self-hosted `serve -remote` is the
  same code with one implicit account. Hosted launch scale is small (hundreds
  of accounts), so #18's keep-opened-accounts-in-memory design is fine for
  now; idle eviction can come later. *(My call.)*
- **Existing installs keep working.** The implicit account uses today's data
  directory with today's flat layout. Only hosted mode nests accounts as
  `<root>/<account>/`. *(My call.)*
- Devices and tokens belong to an account. *(My call.)*
- The identity store and subscription state live in the hosted server's own
  data directory, next to the accounts, and only exist in hosted mode. One
  account can have several identities (Apple, Google, email). *(My call.)*
- Every request picks its account from its token. Nothing below the doors
  learns that accounts exist (ARCHITECTURE.md layering). Each account gets its
  own `Assistant`, so chat ids can't cross accounts, and the assistant returns
  token usage to the door that meters it.
- `/api/meta` reports an API version, an integer starting at 1. Changes are
  additive; only a breaking change bumps it. No `/api/v1` prefixes. The app
  refuses to talk to a server that is too old, and says so plainly. The server
  keeps the previous version working for at least one app release. *(My call.)*
- Option lists the app needs (rate sources, debt strategies, providers) come
  from `/api/meta`, the same rule as the web UI.
- The app edits through the existing operations endpoint with a revision, not
  by replacing the whole budget.

### Assistant

- **Notes:** the assistant keeps short notes about the person, such as "works as
  a freelance designer" or "going through a divorce, prioritise stability". It
  adds them through tools, and they're stored per account next to the budgets.
  Notes go into the assistant's prompt alongside the budget. How a note is
  saved depends on where it came from; see "Untrusted text".
- **BYOK:** an app settings screen sets provider, model, URL and key on the
  server. This is the existing settings API, per account. The key is write-only
  and never returned. Providers: Anthropic, any OpenAI-compatible endpoint
  (which covers OpenAI, Gemini's compatible endpoint, Ollama and vLLM), and
  others as the provider list grows. On hosted, BYOK lifts the usage cap.
- **Which provider answers:** the account's own BYOK settings win. Otherwise a
  hosted server uses the included provider from its server config, which no
  account can read. A self-hosted server has only its one account's settings,
  as today, and the assistant is off until they're set. *(My call.)*
- **Included AI (hosted):** one cheap model (Haiku or Flash class), pinned in
  the hosted server's config, never the `claude-opus-5-5` default. Its provider's
  API terms must say no training on customer data, and the privacy page names
  the provider and its retention period (30 days by default at Anthropic).
  Prompt caching on the system prompt and tool definitions is on from the
  start. The model can change later if the replacement meets the same terms.
- **Connector, hosted only:** `/mcp` on the hosted server accepts OAuth sign-in
  following the MCP authorization spec, so Claude or ChatGPT can add bbbudget as
  a connector. Those apps call the server from their own backends and require
  OAuth, so a self-hosted server on a tailnet can't serve them. Self-hosters
  keep what #19 already gives them: `/mcp` with a device token, for local
  clients such as Claude Desktop and Claude Code. *(My call.)*

### Untrusted text

The assistant reads text the person didn't write: pasted bank statements, item
and category names, tool results, and, in connector mode, whatever the other
app sends. Any of it can carry instructions. These rules limit the damage and
make it visible; they don't make the model immune.

- **Where a note comes from decides how it's saved.**
  - A note drawn from the person's own chat message saves straight away, and
    the chat shows "Saved: … (undo)".
  - A note drawn from imported or pasted text, or arriving through the
    connector, is a proposal. The chat shows it with Save and Dismiss, and
    nothing is stored until the person taps Save.
- Every note records its source and date ("from chat, 21 Oct", "from statement
  import, 21 Oct"), shown in the notes list, where any note can be deleted.
- At most 50 notes of at most 200 characters each.
- The nudge job runs unattended, so it reads notes but never writes notes or
  changes the budget.
- The system prompt puts imported text, names and tool output in clearly marked
  sections and says they are data, never instructions.
- Budget changes go only through the normal tools, even after an import. Each
  one shows in the chat and can be undone, as today.

### Nudges

- Once a day, `serve` checks each budget against deterministic triggers built
  from the existing insights, for example: the coming month goes negative, a
  category is on course to overspend, or a goal date slips. Each budget keeps
  a record of which triggers it has already shown, so the same one doesn't fire
  every day. AI runs only when a trigger fires, to word the nudge using the
  notes. That keeps cost near zero for quiet months. Without a configured
  provider, the nudge uses fixed template text. Hosted nudges don't count
  against the usage cap. *(My call.)*
- Nudges appear as a card on Overview, with an action ("Plan presents") that
  opens Ask with context, and a dismiss. They show when the app opens.
- A toggle turns nudges off entirely.
- **Push is deferred** until after the hosted launch. It needs the publisher's
  APNs key and Firebase on Android (which conflicts with "no telemetry" and
  F-Droid), and the relay that self-hosted servers would need is the most
  security-sensitive piece of the plan. When it returns, it gets its own design.
  Push payloads will never carry amounts or budget text. *(My call.)*

### Hosted service

- **Sign-up and sign-in without passwords:** Sign in with Apple, Sign in with
  Google, and an email one-time code.
  - Email codes are at least 8 characters, valid for 10 minutes, and allow 5
    attempts, with limits per email address and per client address.
  - Sign in with Apple can hide the email address, so identities are linked to
    an existing account only on a verified matching email, or through an
    explicit "link account" step while signed in.
  - Settings has "sign out everywhere", which revokes all of the account's
    device tokens. Hosted device tokens expire after 90 days without use.
- **Price:** $4.99/month or $39/year with the assistant included. It's cheaper
  than YNAB, Monarch, Copilot and Lunch Money ($95–$109/year), which charge for
  bank sync that we don't have.
- **Margin:** $39/year is $3.25 a month. That leaves about $2.75 after the
  stores' 15% small-business commission, and about $2.30 after EU VAT. One
  assistant message is a tool loop of roughly 15–30k input tokens, which costs
  about $0.02–0.04 on Haiku 4.5 without caching. A heavy user can cost more than
  they pay, so the cap is real from day one.
- **Metering and cap:** the server logs tokens per message per account. A hard
  monthly cap applies from the first hosted account: 150 messages for
  subscribers and 50 during the trial, as starting values in server config.
  At the cap the assistant stops and offers BYOK. Real usage data then tunes
  the numbers. *(My call.)*
- App review gets a demo account with sample data and an entitlement override,
  so reviewers can use it without buying. *(My call.)*
- **Privacy page:** lists exactly what is stored (budgets, notes, device names,
  sign-in email, token logs), that the operator can read it, the AI provider
  and its retention period, and the hosting region.
- Data at rest on encrypted disks, with backups. Export and account deletion
  are in the app.

## Billing (decided)

- **In-app purchase only:** StoreKit on iOS, Play Billing on Android. No web
  checkout. US App Store link-outs and Google's external billing exist now, but
  in-app purchase is the simplest and the safest in review. The server
  validates receipts and listens to store notifications for renewals and
  cancellations, and that is the only source of entitlement.
- **Free trial** through the stores' own introductory offer, with the assistant
  included under the trial cap. The trial length is set in the store listing
  (7 or 14 days).

## Milestones (GitHub Project order)

1. **Remote server** (self-hosting from a phone works):
   - rebase #18 and #19, devices per account, the implicit account on today's layout;
   - QR pairing with `public_url`, rate-limited pairing, API version in `/api/meta`;
   - HTTPS docs.
2. **Flutter app MVP**:
   - `mobile/` in this repo, the brand theme, and the connect screen (pair only);
   - the Overview, Ask (SSE chat) and Plan tabs, plus editing an item's amount;
   - CI for Android and iOS;
   - a Google Play closed test with 12 testers, so the 14-day clock starts early.
3. **Assistant**: notes with the untrusted-text rules, BYOK settings in the
   app, and nudges (the server job and the Overview card).
4. **Hosted service**:
   - sign-in, account provisioning, metering and the cap;
   - billing, the privacy page;
   - operations: backups, export, in-app deletion;
   - the OAuth connector.
5. **Store release**: signing, store listings, review. Some of it only the
   maintainer can do: developer accounts, tax and banking forms, and trader
   details.
6. **Later**: push and its relay, web sign-in for the hosted UI.

## Left to the builder

- Flutter packages: state management, HTTP and SSE client, secure storage, QR scanning.
- The exact nudge triggers and thresholds, as long as they're built from
  `internal/budget` insights.
- The notes storage format and the assistant tool names, following the naming
  rules.
- How `mobile/` joins `make check`, for example a `make mobile` target that CI runs.
- Hosting provider and region for the hosted service, stated on the privacy page.
- The Flutter version pin and the minimum Android and iOS versions.

## Open

- **Licence for `mobile/`.** The FSF considers App Store terms incompatible
  with GPL-family licences. With no CLA, the first outside contribution to
  `mobile/` could block publishing the app. Options: MIT or Apache-2.0 for
  `mobile/`, or AGPL with an explicit App Store exception, stated in
  CONTRIBUTING.md. Needed before milestone 2.
- **Publishing as an individual or a company.** As an individual, the EU trader
  rules put the maintainer's own address and phone number on the public store
  page. Needed before the milestone 2 closed test.
