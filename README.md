# Posthouse

Posthouse is a local-first Go CLI, MCP server, and full-screen terminal app for operating multiple generic mail and calendar connections through one safe interface. v0.2 covers IMAP/SMTP, read-only ICS feeds, mutable CalDAV calendars, encrypted offline state, and prepare-before-execute writes.

> **Release target:** v0.2.0. OAuth, native provider APIs, HTML composition, permanent mail deletion, CalDAV scheduling/free-busy, and live-provider certification are intentionally outside this release.

## What works

- Aggregate and paginate two or more mail, CalDAV, and ICS-feed connections with structured partial-source errors.
- Fetch complete MIME messages, decoded text, sanitized HTML, threading headers, and bounded attachment chunks.
- Offline full-text search uses the encrypted headers and bodies already cached; structured `offline_search_incomplete` source warnings identify queries that may omit uncached content.
- Attachment reads return a cursorless final chunk when the cache cannot retain them; multi-chunk reads require enough `cache.max_bytes` capacity for the encrypted attachment snapshot.
- Prepare and execute send, reply, forward, draft, mark, flag, move, archive, and trash operations without global IMAP expunge.
- Discover IMAP special-use folders, capabilities, CalDAV principals/homes, and multiple calendar collections.
- Expand recurring ICS events with exclusions, overrides, cancellations, all-day and timezone handling.
- Prepare and execute ETag-guarded CalDAV create, update, occurrence/series update, and delete operations.
- Generate portable `METHOD:REQUEST` and `METHOD:CANCEL` invitations, then send them as a separate prepared mail operation.
- Use live-first reads with stale encrypted-cache fallback, `--offline`, `--refresh`, explicit sync, LRU limits, clear, and rekey.
- Run the same contracts through CLI JSON, MCP stdio, authenticated Streamable HTTP, and a keyboard-complete Go-TUI.

Provider-side draft create/update requires IMAP `UIDPLUS` (or IMAP4rev2) so the appended draft always has an addressable UID; sent-copy APPEND remains compatible without it. Cleartext authenticated IMAP/SMTP and disabled CalDAV certificate verification are accepted only on loopback development endpoints; remote connections must use verified TLS or STARTTLS.

## Safety model

Reads may fan out across selectors. Writes never do: every provider mutation resolves to exactly one connection and returns a ten-minute opaque prepared token. The preview includes the connection, acting identity, recipients or calendar, changed fields, attachments, and side effects. Only `operation execute TOKEN` performs the write. Repeated execution returns the original result; changed or expired operations must be prepared again, and an uncertain SMTP result after `DATA` is never retried automatically.

Provider secrets use either environment or OS-keychain references. The SQLite state is CGo-free; message/event content, bodies, attendees, drafts, operation payloads, and attachment chunks are encrypted with XChaCha20-Poly1305. Plain indexing data is limited to connection IDs, opaque provider IDs, timestamps, flags, sizes, and sync state.

## Install

Posthouse pins Go 1.26.6.

```sh
go install github.com/timborovkov/posthouse/cmd/posthouse@latest
```

From a clone:

```sh
make build
./bin/posthouse help
```

## Configure

Copy [examples/connection.json](./examples/connection.json), set its endpoints, and add it:

```sh
export ACME_MAIL_PASSWORD='disposable or provider app password'
export ACME_CALENDAR_PASSWORD='disposable or provider app password'
posthouse connection add --file examples/connection.json
posthouse connection discover acme
posthouse connection doctor acme
```

`connection discover` persists discovered special-use folders and CalDAV collections automatically; its displayed connection is redacted for safe inspection and should not be passed to `connection update`. A read-only feed example is in [examples/feed-connection.json](./examples/feed-connection.json). Config v2 accepts exactly one secret source:

```json
{"secret":{"env":"ACME_MAIL_PASSWORD"}}
```

or:

```json
{"secret":{"keychain":"acme-mail"}}
```

Store a keychain value without putting it on a command line:

```sh
printf '%s' "$ACME_MAIL_PASSWORD" | posthouse connection secret set acme-mail --file -
```

Config v1 is migrated atomically to v2 on load and backed up beside the config as `*.v1.bak`. Headless MCP and Docker deployments must use environment references and set `POSTHOUSE_CACHE_KEY` to a base64- or hex-encoded 32-byte key. Desktop use creates a path-scoped cache master key in an isolated OS-credential namespace, so rekeying one configured SQLite database cannot strand another; existing shared desktop keys migrate to the path-scoped slot when opened. Plaintext fallback is never used. State opening verifies an encrypted key marker, so `/readyz` fails instead of accepting a wrong key.

## CLI workflows

Data commands emit JSON except `calendar ics`, which emits `text/calendar` unless `--output` is supplied.

```sh
# Live-first aggregate reads; add --offline or --refresh when needed
posthouse mail list --category work --label primary --unread
posthouse mail search --query renewal --page-size 25
posthouse calendar list --collection team --start 2026-08-01T00:00:00Z

# Fetch one body or attachment
posthouse mail get --connection work --folder INBOX --uid 42
posthouse mail attachment --connection work --folder INBOX --uid 42 --id 'ATTACHMENT_ID_FROM_MAIL_GET' --output report.pdf

# Prepare, inspect, and execute a send
posthouse mail send --connection work --to teammate@example.test --subject Status --body-file status.txt --attachment report.pdf
posthouse operation show 'TOKEN_FROM_PREVIOUS_COMMAND'
posthouse operation execute 'TOKEN_FROM_PREVIOUS_COMMAND'

# Other mail writes use the same flow
posthouse mail reply --connection work --folder INBOX --uid 42 --body 'Thanks'
posthouse mail mark --connection work --folder INBOX --uid 42 --read --flagged
posthouse mail archive --connection work --folder INBOX --uid 42

# Prepare mutable CalDAV operations from event JSON
posthouse calendar create --connection work --file event.json
posthouse calendar update --connection work --file event-with-current-etag.json
posthouse calendar delete --connection work --collection team --href /work/team/item.ics --etag '"etag"'

# Portable ICS generation and explicit cache operations
posthouse calendar ics --title Planning --start 2026-08-17T09:00:00+03:00 --end 2026-08-17T10:00:00+03:00 --output planning.ics
posthouse calendar ics --method cancel --id planning-uid --sequence 3 --title Planning --start 2026-08-17T09:00:00+03:00 --end 2026-08-17T10:00:00+03:00 --output planning-cancel.ics
posthouse sync
posthouse cache status
posthouse cache rekey --key-env POSTHOUSE_CACHE_KEY_NEW

# Full-screen keyboard interface
posthouse tui
```

Outbound attachment payloads are limited to 25 MiB total per prepared mail or draft operation. Path-backed attachments must be regular files; directories, devices, pipes, and files that grow past the limit are rejected before provider I/O.

For a headless rekey, the command cannot modify its parent shell or deployment secret. Keep both values available until the command succeeds, then replace the active key before starting any other Posthouse process:

```sh
export POSTHOUSE_CACHE_KEY_NEW='new-base64-or-hex-encoded-32-byte-key'
posthouse cache rekey --key-env POSTHOUSE_CACHE_KEY_NEW
export POSTHOUSE_CACHE_KEY="$POSTHOUSE_CACHE_KEY_NEW"
unset POSTHOUSE_CACHE_KEY_NEW
```

The command returns a `required_action` field in headless mode. An already-running process that still holds the old key is prevented from writing and must be restarted. Desktop keychain rekeys keep an encrypted recovery record in the same SQLite transaction; if keychain activation is interrupted after commit, the next startup recovers and promotes the committed key automatically.

Selectors intersect exact connection IDs/names, category, labels, capability, and calendar collections. List cursors are opaque, query-bound, and source-snapshot-bound: new or recovered sources join only a fresh traversal. IMAP cursors also bind UIDVALIDITY and the initial UID boundary.

## Go-TUI

The TUI has five responsive views: connection onboarding/doctor, unified inbox, message detail/attachments, unified agenda/event editor, and operations/cache. It uses `Tab`/`Shift+Tab` for areas, arrows or `j/k` to move, `/` search, `r` refresh, `c` compose/create, `a` actions, `Enter` open/confirm, `Esc` back/cancel, `?` help, and `q` quit. Mail and event editors prepare writes; a separate exact preview modal is required before execution.

The `.gsx` source and generated `_gsx.go` are both committed. Run `make generate`; CI runs `make generate-check` and fails on a diff.

## MCP

Stdio client configuration:

```json
{
  "mcpServers": {
    "posthouse": {
      "command": "/absolute/path/to/posthouse",
      "args": ["mcp", "stdio"],
      "env": {
        "POSTHOUSE_CACHE_KEY": "...",
        "ACME_MAIL_PASSWORD": "...",
        "ACME_CALENDAR_PASSWORD": "..."
      }
    }
  }
}
```

Streamable HTTP:

```sh
export POSTHOUSE_MCP_TOKEN='a-long-random-token'
export POSTHOUSE_CACHE_KEY='a-base64-or-hex-encoded-32-byte-key'
posthouse mcp http --address 127.0.0.1:8791
```

`POSTHOUSE_MCP_TOKEN` is mandatory for every Streamable HTTP listener, including loopback. Stdio is the only transport with implicit local-process authentication. Streamable HTTP request bodies are capped at 36 MiB, which accommodates one operation's base64-encoded 25 MiB attachment allowance plus its JSON envelope.

The endpoint is `/mcp`. `/healthz` reports process liveness; `/readyz` checks configuration, cache migration/key availability, and initialized internal services. Provider connectivity belongs to `connection_doctor` and `sync`, not readiness. The direct server is restricted to loopback because it serves HTTP; expose it remotely only through a TLS-terminating reverse proxy forwarding to the loopback listener, and retain bearer-token authentication. `--allow-container-listener` exists only for a container whose published port is externally constrained to loopback or protected by TLS; the supplied Compose file uses it with a `127.0.0.1` host publication.

The typed tool surface includes connection listing/doctor; message search/body/attachment reads; send, reply, forward, draft, and message-action preparation; event listing/ICS/CRUD preparation; operation show/execute; sync; and cache status. Tool errors are for invalid requests or total failure; successful multi-source reads carry structured partial errors and stale/cache timestamps in their result.

## Docker and deterministic tests

Production-style local container:

```sh
cp .env.example .env
# Replace every placeholder, especially POSTHOUSE_CACHE_KEY and POSTHOUSE_MCP_TOKEN.
docker compose up --build
```

The service binds `127.0.0.1:8791`, mounts the Docker-managed `posthouse-data` volume at `/data`, and uses `/data/config.json` plus `/data/posthouse.db` by default. The named volume remains writable by the image's non-root Posthouse user; inspect or back it up with standard Docker volume commands rather than replacing it with an unowned host bind mount.

Development needs no real provider accounts. [docker-compose.test.yml](./docker-compose.test.yml) pins GreenMail `2.1.11` and Radicale `3.7.3`, binds them only to loopback, seeds isolated `work` and `personal` principals, and discards state after each suite:

```sh
make test              # race-enabled unit tests, no Docker
make test-container    # production image plus Compose topology
make test-integration  # SMTP/IMAP and CalDAV protocol suites
make test-e2e          # built-binary CLI and MCP workflows
make validate          # Docker-free local gate
make validate-all      # complete release gate
```

The Docker suites exercise two mail identities, concurrent cross-process execution, SMTP→IMAP attachments, reply/forward/drafts/folder actions/sent copies, real MCP stdio and authenticated HTTP writes, and real CalDAV discovery, REPORT, PUT, DELETE, ETags, conflicts, invitations, recurrence, and multiple collections. TUI state tests cover navigation, cancellation, attachment access, and the exact prepared-write preview. HTTP fixtures cover feeds, malformed data, redirects, limits, timeouts, and cancellation. Every run tears down containers and volumes first and again on exit.

## Cache policy and boundaries

- Defaults: 90 days of message metadata, 30 days of bodies, events from 90 days past through 365 days future, and attachments only after explicit access.
- Default encrypted-state limit: 2 GiB, accounting for both cache data and prepared-operation ciphertext, with LRU attachment eviction before message bodies. Old expired operation records are purged during preparation.
- Live-first is the default; stale-cache fallback is explicit in result metadata. `--offline` never contacts providers and `--refresh` refuses stale fallback.
- Cache namespaces are bound to canonical provider configuration plus non-reversible digests of resolved endpoint and credential secrets; unresolved identities cannot read or populate provider cache.
- Private content never belongs in logs, connection listings, fixtures, or error bodies. Configuration files are atomically written with mode `0600`.
- No cache is a provider backup. Clearing it removes local cached content, not provider data or the separate prepared-operation ledger.
- Generic protocol compatibility is covered by deterministic local servers; real Fastmail/iCloud or other provider checks may be added later but do not gate v0.2.

See [CONTEXT.md](./CONTEXT.md) for domain language, [design.md](./design.md) for boundaries, [CONTRIBUTING.md](./CONTRIBUTING.md) for gates, and [TODO.md](./TODO.md) for deliberately deferred work.
