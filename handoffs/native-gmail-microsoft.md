# Native Gmail API and Microsoft Graph backends

Temporary. Delete this file and `handoffs/` when native Gmail and Microsoft connections ship.

## Context

Posthouse v0.2 Daily-Ops MVP is on `main` (`bc54d98`): generic IMAP/SMTP, ICS feeds, CalDAV, encrypted cache, prepare-before-execute, CLI JSON, MCP, TUI. That protocol surface stays. This follow-up adds **native Gmail API and Microsoft Graph** as additional connection backends so a selector can fan out across iCloud (IMAP) + two Gmails + one Microsoft connection and return one `MessagePage` / `EventPage`.

An MCP consumer (Hermes or similar) must not see provider differences. Same tools, same selector, same prepared-write flow. The backend behind each connection is an implementation detail.

Suggested branch: `tim/native-gmail-microsoft`.

## What Tim specified

These are product requirements, not suggestions. Build this shape.

**Now that generic IMAP/SMTP/CalDAV works, add native API OAuth for Gmail and Microsoft.** Not IMAP-with-a-Google-password, not XOAUTH2 against `imap.gmail.com`. Real Gmail API and Microsoft Graph, properly, including calendar on those backends. Without that, Posthouse is not useful to most people.

**A person can connect several Gmails** (and a Microsoft connection, and generic ones) as separate connections in one Posthouse.

**Unified search is the point.** Example Tim gave: one iCloud connection on IMAP/SMTP, two different Gmail connections, one Microsoft connection. Search runs generic IMAP search on iCloud, Gmail API search on each Gmail, Graph search on Microsoft, then **composes one response**. Same idea for other reads that already fan out (calendar list once those backends exist).

**Hermes (MCP) must not care which vendor it is.** Gmail, Microsoft, iCloud, and Protonmail look like the same tools and the same message/event shapes. Protonmail is in that list as “same MCP surface,” which today means Proton Bridge as an IMAP connection, not a native Proton API.

**Posthouse is not a SaaS.** No Posthouse server, no mailbox proxy, no hosted OAuth callback service. It is a local CLI / MCP / TUI.

**Non-technical operators never open Google Cloud or Entra.** They must not register their own OAuth apps. They click Allow in a browser. Tim registers Posthouse once per vendor.

**Do not put OAuth secrets in the git repo.** People still have to use the binary: ship/inject a public client ID (and Google’s Desktop token-exchange value only via env or release CI). Refresh tokens stay in the OS keychain (desktop) or an env ref (headless), never `config.json`, never MCP tool arguments.

**No branded protocol presets** (`fastmail` / `icloud` hostname catalogs). Generic connections stay generic. Native Gmail/Microsoft connections also do not ask for `imap.gmail.com` hostnames; OAuth is the onboard.

Publisher registration, privacy-policy website, and Google/Microsoft verification are Tim’s work (`handoffs/publisher-gmail-microsoft.md`). Client IDs may be absent for the whole implementation; fixtures and fail-closed `connection auth` are enough.

## Read first

1. `CONTEXT.md` — vocabulary. Connection, identity, capability, selector, cursor, message, event, prepared operation. Do not say account/inbox/integration in public names.
2. `AGENTS.md` — `make validate` after changes; tests never need live provider credentials; no secrets in git/logs/fixtures.
3. `design.md` — local-first switchboard; reads fan out; writes resolve to exactly one connection; cache is not canonical.
4. `internal/model/model.go` — `Message` is IMAP-shaped (`Folder` + `UID uint32`). That is the blocking public-model change.
5. `internal/service/service.go` — `SearchMessagesContext` already fans out per connection, merges pages, and returns `errors[]`. Reuse this. Do not build a second aggregator.
6. `internal/config/store.go` — `SecretRef` is env or keychain only; `ResolveSecret` / `SetKeychainSecret`. Refresh tokens use this, not `config.json`.
7. `internal/mcpserver/server.go` — tool contracts Hermes already calls (`messages_search`, `messages_get`, prepare/execute).
8. `internal/mail/imap.go` and `internal/calendar/caldav.go` — keep as the generic backends.
9. `TODO.md` — this work is after v0.2, not the v0.2.0 release gate.
10. Conversation decisions in this file’s **Decisions** section. Those override any impulse to do IMAP XOAUTH2 for Gmail/Microsoft.

## Decisions (already settled)

1. **Native APIs, not IMAP-on-OAuth.** Gmail = Gmail API + Calendar API. Microsoft = Graph mail + calendar. Do not implement SASL XOAUTH2 against `imap.gmail.com` or Outlook IMAP. Google’s verification FAQ treats IMAP as the restricted `https://mail.google.com/` scope and will push a mail client onto the Gmail API anyway.
2. **One public language.** `messages_search` / `events_list` stay generic. Translate `--query`, unread, time bounds per backend. Do not add `gmail_q` or Graph `$search` to CLI/MCP.
3. **Multiple Gmails are multiple connections.** Each has its own identity, secret ref, and capabilities. Selectors already express “all of them.”
4. **Posthouse is the publisher of one desktop/public OAuth client per vendor.** Users click Allow. Users never create a Google Cloud or Entra project. Optional env override of client IDs is an escape hatch for dogfood, not the product.
5. **Public client + PKCE.** Loopback (`127.0.0.1`) for desktop; device-code for headless/SSH/MCP hosts with no browser. Microsoft: public client, **no client secret**. Google Desktop: client ID is public; if Google’s token endpoint still demands the Desktop “client secret”, inject it at build/runtime from env — it is not a real secret, but it still must not be committed.
6. **No Posthouse OAuth proxy.** No hosted redirect, no token-exchange SaaS. Tokens never transit a Posthouse server (there isn’t one).
7. **Refresh tokens are the credential.** Store via existing keychain/env `SecretRef`. Config holds `{"keychain":"gmail-work"}` or `{"env":"POSTHOUSE_GMAIL_WORK_REFRESH"}`. Access tokens are short-lived and memory/cache only.
8. **Generic IMAP/SMTP/CalDAV/ICS stay first-class.** iCloud, Fastmail, Proton Bridge keep using them. Do not add branded presets (`fastmail`, `icloud` host catalogs).
9. **Proton native API is out of scope.** Proton Bridge is IMAP and already fits.
10. **Dogfood against live Google/Microsoft is the publisher’s job.** Implementation tests use `httptest` fixtures. `make test` stays Docker-free and credential-free.

## Deliverables

Work in this order. Each chunk should be committable and `make validate`-clean.

### 1. Provider-neutral message identity

Public message identity becomes `connection_id` + opaque `id` (string). `folder` remains display/mailbox metadata, not the primary key. RFC `message_id` stays the header.

IMAP implementation may encode UIDVALIDITY+UID (and folder) inside `id` internally. Gmail uses Gmail API message id. Graph uses Graph message id.

Update `model.Message`, `GetMessage*`, `PrepareMailAction`, `PrepareDraft`, CLI `mail get` / mark / move / archive / trash, MCP `messages_get`, `messages_attachment_get`, `messages_action_prepare`, `messages_draft_prepare`, TUI open/action, cache keys, and tests.

Completion: an IMAP round-trip still works through the new `id`; no public API requires `--uid` as the only identifier. Keep `--uid` as a deprecated IMAP-only alias only if it is a thin wrapper around `id` and tests cover both.

### 2. Connection mail/calendar backend kind + OAuth secret wiring

Extend config so a connection can be:

- mail: generic IMAP/SMTP (today) **or** `gmail` **or** `microsoft`
- calendar: `feed` / `caldav` (today) **or** `gmail` **or** `microsoft`

Auth method: password secret (today) or OAuth refresh token in the same `SecretRef` shape.

Add `posthouse connection auth <id>` as **CLI first**. TUI auth is optional in this branch. There is no MCP tool that opens a browser or accepts a refresh token; Hermes uses connections the operator already authed.

Flows:

- Desktop: loopback + PKCE, open system browser, persist refresh token to keychain, write keychain ref on the connection.
- Headless: device-code; print URL + user code on stderr; persist the same way.
- Resolve client IDs from, in order: env override, then build-time injected values. Missing client ID is a clear error telling the operator to set `POSTHOUSE_GOOGLE_CLIENT_ID` / `POSTHOUSE_MICROSOFT_CLIENT_ID` (publisher has not shipped verified IDs yet).

Google Desktop secret, if required: `POSTHOUSE_GOOGLE_CLIENT_SECRET` env or ldflags. Never write it into `config.json` or git.

Completion: a fixture OAuth token endpoint plus a fake loopback/device-code exchange stores a refresh token via `SetKeychainSecret` or env, and `connection doctor` reports oauth token refresh without printing the token.

### 3. Gmail API mail backend

Implement list/search, get (text + sanitized HTML + attachments), send, reply/forward, mark/flag, archive, trash, drafts against Gmail API. Map Posthouse actions onto Gmail labels (INBOX, TRASH, etc.). Use scopes no broader than:

- `https://www.googleapis.com/auth/gmail.readonly`
- `https://www.googleapis.com/auth/gmail.send`
- `https://www.googleapis.com/auth/gmail.compose` if drafts need it
- `https://www.googleapis.com/auth/gmail.modify` only if archive/trash/labels cannot be done otherwise

Do not request `https://mail.google.com/`.

Refresh the access token before calls. Treat 401 as refresh-once-then-fail.

Gmail API send already places the message in Sent; do not run IMAP `sent_copy` / APPEND on a Gmail connection.

Completion: httptest Gmail fixtures cover search, get, send prepare/execute, and a label action; live Gmail is not required.

### 4. Microsoft Graph mail backend

Same Posthouse mail capabilities against Graph. Scopes (delegated):

- `User.Read`
- `Mail.Read`
- `Mail.Send`
- `Mail.ReadWrite` if modify/drafts/trash need it
- `offline_access`

Public client, no Microsoft client secret. Same `connection auth` flows.

Completion: same fixture bar as Gmail.

### 5. Fan-in through existing search/list

`SearchMessagesContext` already iterates matched connections. Each connection’s backend (IMAP / Gmail / Graph) returns `[]model.Message` plus optional `SourceError`. Merge, cursor, partial errors stay as today.

Do the same for `ListEventsMode` once calendar backends exist.

Completion: a unit test matching Tim’s example — one IMAP connection (iCloud/Proton-Bridge shaped) + two Gmail connections + one Microsoft connection — returns one page, stable sort, per-source errors, and a query-bound cursor that resumes without mixing provider paging tokens into the public JSON.

### 6. Gmail Calendar API + Graph calendar

After mail works: `calendar.read` / `calendar.write` for those connections. Keep iCalendar UID on `model.Event` where the provider has one; Graph/Google ids map into existing `Event.ID` / `Href`/`ETag` equivalents without leaking Graph JSON to MCP.

Completion: list + create/update/delete prepare/execute against fixtures; ICS generation stays provider-independent.

### 7. Surfaces

CLI, MCP tool descriptions, TUI onboarding/auth, README, examples, `CONTEXT.md` if identity language changes. MCP tools stay generic — no `gmail_*` tools.

Completion: `make validate`; MCP descriptions mention opaque message `id` and OAuth connections without naming provider query languages.

## Code traps in this repo

- `config.Validate` today requires IMAP or SMTP `address` plus a password `SecretRef` whenever `mail` is set (`internal/config/store.go`). Native connections have neither hostnames nor a password. Validation and `capabilities()` must key off mail/calendar **kind**, or Gmail/Graph connections cannot be saved.
- `connection discover` persists IMAP special-use folders and CalDAV collections. Native connections skip that path (Gmail labels / Graph well-known folders are backend metadata, not discover output the operator pastes).
- `connection doctor` is IMAP/SMTP/CalDAV checks. Native doctor is: secret resolves, access token refreshes, one cheap API ping. Still redacted.
- `SearchMessagesContext` still threads IMAP UID cursors (`CursorUID`, `UIDVALIDITY`). Native backends need their own per-connection cursor state inside the existing opaque page cursor, not a second pagination protocol.
- `golang.org/x/oauth2` is already an indirect module. Promote it (or the current Google/Microsoft client libs) only after fetching current docs.

## Invariants

- Reads may fan out. Writes never do. Prepared operations still bind one connection, identity, payload digest, preview; execute is idempotent.
- `config.json` mode `0600`; never persist refresh tokens, access tokens, or Google desktop secrets there.
- Public JSON/MCP/CLI never return secret env names that are not already the SecretRef contract, and never return token material. Discover/doctor stay redacted.
- Cache namespaces stay bound to canonical provider config + non-reversible secret digests (`internal/service` provider identity). Token refresh that rotates the refresh token must invalidate unexecuted prepares the same way a password rotation does today.
- Cleartext IMAP/SMTP and `insecure` CalDAV remain loopback-only.
- Tests: no live Google/Microsoft credentials. GreenMail/Radicale stay the generic protocol fixtures.
- Fetch current Gmail API, Calendar API, Graph, and OAuth docs (ctx7 / official URLs) before wiring request shapes.
- Preserve generic protocol support. A Gmail backend must not become the only mail path.

## Client ID injection

```
POSTHOUSE_GOOGLE_CLIENT_ID
POSTHOUSE_GOOGLE_CLIENT_SECRET    # Desktop token exchange only; not committed
POSTHOUSE_MICROSOFT_CLIENT_ID
```

Optional later: `-ldflags` for release binaries. Until the publisher verifies apps, env-only is correct. A missing ID fails closed with an operator-facing message.

## Ask the user first

Do not block chunks 1–5 on these. Defaults: CLI `connection auth` only; calendar is chunk 6 after mail is green; no client IDs in git.

- Real client IDs, if they now exist, go into env — never into a commit the agent creates.

## Verify

```
make validate
```

Add focused `httptest` coverage for each new backend and for mixed-connection search. Do not add Docker live-Google jobs.

## Out of scope

- v0.2.0 git tag, binaries, OCI image
- IMAP XOAUTH2
- Google/Microsoft Cloud Console registration and verification
- Posthouse-hosted OAuth redirect or token proxy
- Branded IMAP/CalDAV presets
- HTML composition, contacts, permanent IMAP expunge, IDLE/push, CalDAV scheduling
- Native Proton API
- Asking operators to create their own OAuth apps as the default path
- Committing `client_secret` or refresh tokens

## First action

Read `CONTEXT.md`, `internal/model/model.go` (`Message`), and `SearchMessagesContext` in `internal/service/service.go`. Write a short plan of the `id` migration (every CLI flag, MCP field, cache key, and TUI path that still says `uid`). Then implement chunk 1 before any Gmail HTTP client.
