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
   - a **connector**: bbbudget as an MCP server inside the user's own Claude or
     ChatGPT app, which is the only way to use what those apps already know about
     the person.
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
- AGPL-3.0-or-later. Self-hosting stays free.
- No real personal data in fixtures, screenshots or store listings.
- iOS builds need Xcode on macOS. The maintainer works on Arch Linux, so iOS
  builds and signing run on GitHub Actions macOS runners.
- iOS blocks plain-HTTP connections to non-local hosts by default (App
  Transport Security), so a self-hosted server reached over the internet needs
  HTTPS. The docs already point to Tailscale serve, Caddy or nginx.
- Apps in the stores lag behind servers. Any server may meet an older app, and
  the reverse.
- Apple and Google require in-app purchase for subscriptions bought inside the
  app.

## Starting point

- **Server:** the single-user Go binary. All maths lives in `internal/budget`, and
  the JSON API is in `internal/httpapi`. The assistant runs on the server and
  streams over `POST /api/budgets/{id}/chat` (SSE). Its provider is a server
  setting: `anthropic` or `openai-compatible`, with the key never sent to clients.
  It listens on localhost only, with no authentication.
- **Web UI:** it already has a phone layout. It stays as it is.
- **PR #18** (open, stale since Oct 15) adds `internal/accounts`: one `App`,
  data directory and settings file per account. Nothing calls it yet.
- **PR #19** (open, stacked on #18) adds `serve -remote`, bearer tokens for
  paired devices, `bbbudget pair` (one-time 50-bit code, 10 minutes, single
  use) and `bbbudget devices`. Devices live in one `devices.json` per server,
  not per account.
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
- Phones only for now: no tablet layout. *(My call.)*
- Tabs: **Overview** (nudge card, the two key numbers, where the money goes,
  insights), **Ask** (chat with suggestion chips and the tools each answer
  used), **Plan** (debts, savings pots, nudge toggle).

## How it fits together

```
Flutter app ──HTTPS + bearer token──▶ bbbudget server ──▶ AI provider
                                       (self-hosted or      (included, BYOK,
                                        hosted, per account)  or Ollama)
Claude / ChatGPT app ──MCP + OAuth──▶ /mcp on the same server
```

### Connecting

- **First screen:** two choices, "Use bbbudget hosting" and "Connect to my own server".
- **Hosted:** sign in. That creates the account (if needed) and a device token
  in one step. The first budget starts from a template, and the assistant is
  ready with no setup.
- **Self-hosted:** `bbbudget pair` prints the one-time code **and a QR code**
  holding the server URL plus the code. The app scans the QR code, or the user
  types the URL and code. No account.
- The app keeps the token in the platform's secure storage (Keychain or Keystore).
- Native apps don't need CORS. The browser UI on a remote server needs the
  sign-in from #19's "Not in this PR" list, but that isn't required for the apps.

### Server changes

- Rebase #18 and #19 onto main. Neither is merged or released, so their
  designs may change freely in review and nothing needs migrating.
- One code path for both modes. Hosted is the same binary serving many
  accounts through `internal/accounts`. A self-hosted `serve -remote` is the
  same code with one implicit account. Hosted launch scale is small (hundreds
  of accounts), so #18's keep-opened-accounts-in-memory design is fine for
  now; idle eviction can come later. *(My call.)*
- Devices and tokens belong to an account. *(My call.)*
- The identity store (Apple, Google or email identity to account id) and
  subscription state live in the hosted server's own data directory, next to
  the accounts, and only exist when it runs in hosted mode. *(My call.)*
- Every request picks its account from its token. Nothing below the doors
  learns that accounts exist (ARCHITECTURE.md layering).
- `/api/meta` reports an API version, an integer starting at 1. Changes are
  additive; only a breaking change bumps it. No `/api/v1` prefixes. The app
  refuses to talk to a server that is too old, and says so plainly. The server
  keeps the previous version working for at least one app release. *(My call.)*
- Option lists the app needs (rate sources, debt strategies, providers) come
  from `/api/meta`, the same rule as the web UI.

### Assistant

- **Notes:** the assistant keeps short notes about the person, such as "works as
  a freelance designer" or "going through a divorce, prioritise stability". It
  adds them through tools, and they're stored per account next to the budgets.
  The user can see and delete every note from the app. Notes go into the
  assistant's prompt alongside the budget.
- **BYOK:** an app settings screen sets provider, model, URL and key on the
  server. This is the existing settings API, per account. The key is write-only
  and never returned. Providers: Anthropic, any OpenAI-compatible endpoint
  (which covers OpenAI, Gemini's compatible endpoint, Ollama and vLLM), and
  others as the provider list grows. On hosted, BYOK lifts the usage cap.
- **Which provider answers:** the account's own BYOK settings win. Otherwise a
  hosted server uses the included provider from its server config, which no
  account can read. A self-hosted server has only its one account's settings,
  as today, and the assistant is off until they're set. *(My call.)*
- **Included AI (hosted):** one provider and model chosen by us, whose API
  terms say no training on customer data and minimal retention. It's named on a
  plain privacy page. It can change later as long as the replacement meets the
  same terms.
- **Connector:** `/mcp` accepts OAuth sign-in for hosted accounts, following
  the MCP authorization spec, so Claude or ChatGPT can add bbbudget as a
  connector. The first ticket of milestone 3 is a spike that checks which
  consumer apps support custom remote connectors and whether they accept a
  bearer token or require OAuth. If they require OAuth, self-hosted servers get
  a minimal OAuth flow that approves with a pairing code. If no consumer app
  supports custom connectors, the connector goal waits. *(My call.)*

### Nudges

- Once a day, the server checks each budget against deterministic triggers
  built from the existing insights, for example: the coming month goes
  negative, a category is on course to overspend, or a goal date slips. AI runs
  only when a trigger fires, to word the nudge using the notes. That keeps cost
  near zero for quiet months. Without a configured provider, the nudge uses
  fixed template text. Hosted nudges don't count against the usage cap.
  *(My call.)*
- Nudges appear as a card on Overview, with an action ("Plan presents") that
  opens Ask with context, and a dismiss.
- **Push:** the payload carries no amounts or text from the budget, only "bbbudget
  has a note for you". Push needs the app publisher's APNs and FCM credentials,
  so self-hosted servers send through a small relay we run, which holds only
  device push tokens. Push for self-hosters is opt-in. Without it, nudges show
  when the app opens.
- The relay is a separate small command in this repo, and the hosted server
  uses the same code in-process. A self-hosted server can only push to devices
  that registered through it, and the relay rate-limits each device. It is the
  last ticket of milestone 3. *(My call.)*
- One toggle turns nudges off entirely, and push can be off while in-app nudges
  stay on.

### Hosted service

- Sign-up and sign-in without passwords: Sign in with Apple, Sign in with
  Google, and an email one-time code. Apple requires Sign in with Apple when
  other third-party logins are offered.
- **Price:** $4.99/month or $39/year with the assistant included. It's cheaper
  than YNAB, Monarch, Copilot and Lunch Money ($60–$199/year), which charge for
  bank sync that we don't have.
- **Metering:** the server logs tokens per message per account. The fair-use cap
  is set only after real usage data exists. Until then it's a config value that
  only logs and doesn't block. *(My call.)*
- App review needs a demo account with sample data. *(My call.)* A rough estimate is about $0.015 per
  message on a Haiku-class model, so about $0.60 for 40 messages a month.
- Data at rest on encrypted disks, with backups. An account can export
  everything and delete itself.

## Milestones (GitHub Project order)

1. **Remote server** (self-hosting from a phone works): rebase #18 and #19,
   devices per account, QR pairing, API version in `/api/meta`, HTTPS docs.
2. **Flutter app MVP**:
   - `mobile/` in this repo, the brand theme, and the connect screen (pair only);
   - the Overview, Ask (SSE chat) and Plan tabs, plus editing an item's amount;
   - CI for Android and iOS, and an Android internal-testing build.
3. **Assistant**: notes, BYOK settings in the app, nudges (server job and
   Overview card), the push relay, and the connector over MCP with tokens.
4. **Hosted service**: sign-in, account provisioning, metering and the cap,
   billing, the privacy page, operations (backups, export, delete), and OAuth
   for the connector.
5. **Store release**: signing, store listings, review. Some of it only the
   maintainer can do: developer accounts, tax and banking forms.

## Left to the builder

- Flutter packages: state management, HTTP and SSE client, secure storage, QR scanning.
- The push relay's protocol and where it runs.
- The exact nudge triggers and thresholds, as long as they're built from
  `internal/budget` insights.
- The notes storage format and the assistant tool names, following the naming
  rules.
- How `mobile/` joins `make check`, for example a `make mobile` target that CI runs.
- Hosting provider and region for the hosted service.
- The Flutter version pin and the minimum Android and iOS versions.

## Billing (decided)

- **In-app purchase only:** StoreKit on iOS, Play Billing on Android. No web
  checkout. The server validates receipts and listens to store notifications
  for renewals and cancellations, and that is the only source of entitlement.
- **Free trial** through the stores' own introductory offer, with the assistant
  included. The trial length is set in the store listing (7 or 14 days).
