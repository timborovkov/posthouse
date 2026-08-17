# Installation and usage

This guide is for people who want to run Posthouse, and for agents that need
the same interface. You do not need to be a Go developer. If you can paste a
command into a terminal and create an app password at your mail provider, you
can get through the first sections.

Development setup lives in the [main README](./README.md#development) and in
[CONTRIBUTING.md](./CONTRIBUTING.md).

If something here is confusing, missing, or wrong,
[open an issue](https://github.com/timborovkov/posthouse/issues/new/choose).

## What you need

Posthouse talks to providers over ordinary protocols:

- **Mail:** IMAP and SMTP, usually with an [app password](#app-passwords)
- **Calendar:** CalDAV for read/write, or a public/private ICS URL for
  read-only feeds

It does **not** yet speak OAuth or native Gmail/Microsoft APIs. Many
providers still expose IMAP/SMTP and CalDAV; use those endpoints.

On your machine you need one of:

- [Go 1.26.6](https://go.dev/dl/) (simplest for the CLI and TUI), or
- [Docker](https://docs.docker.com/get-docker/) (headless MCP server)

Desktop use stores an encrypted cache key in the OS keychain. Headless and
Docker deployments set `POSTHOUSE_CACHE_KEY` instead.

## Install

### Option A: `go install`

```sh
go install github.com/timborovkov/posthouse/cmd/posthouse@latest
posthouse version
posthouse help
```

Put Go's `bin` directory on your `PATH` (often `$(go env GOPATH)/bin`).

### Option B: from a clone

```sh
git clone https://github.com/timborovkov/posthouse.git
cd posthouse
make build
./bin/posthouse help
```

### Option C: Docker

See [Docker](#docker) below. The Compose file runs the MCP HTTP server, not
the full-screen TUI.

## First connection

A **connection** is one authenticated provider endpoint (mail, calendar, or
both), plus the identity shown to recipients and attendees. Copy
[examples/connection.json](./examples/connection.json), fill in your
endpoints and address, and keep the secret in the environment — never in the
JSON file.

```sh
export ACME_MAIL_PASSWORD='disposable or provider app password'
export ACME_CALENDAR_PASSWORD='disposable or provider app password'
posthouse connection add --file examples/connection.json
posthouse connection discover acme
posthouse connection doctor acme
```

`connection discover` saves IMAP special-use folders and CalDAV collections.
The JSON it prints is redacted; do not pipe that display back into
`connection update`.

A read-only holiday feed looks like
[examples/feed-connection.json](./examples/feed-connection.json).

Find the config file with:

```sh
posthouse config path
```

By default that is `posthouse/config.json` under your user config directory
(`~/.config` on Linux, `~/Library/Application Support` on macOS). Override
with `--config PATH` or `POSTHOUSE_CONFIG`. The encrypted SQLite state lives
beside the config as `posthouse.db`.

### App passwords

Use a provider **app password** or other disposable credential, not your
normal login password. Put it in the environment or the OS keychain. Config
v2 accepts exactly one secret source per credential:

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

Config v1 is migrated atomically to v2 on load and backed up beside the
config as `*.v1.bak`.

### Cleartext and TLS

Remote connections must use verified TLS or STARTTLS. Cleartext
authenticated IMAP/SMTP and disabled CalDAV certificate verification are
accepted only on loopback development endpoints.

## Terminal app (TUI)

The TUI is the friendliest way to use Posthouse if you are not wiring it
into scripts or an agent.

```sh
posthouse tui
```

There are five views: connection onboarding/doctor, unified inbox, message
detail/attachments, unified agenda/event editor, and operations/cache.

| Key | Action |
| --- | --- |
| `Tab` / `Shift+Tab` | Move between areas |
| Arrows or `j` / `k` | Move |
| `/` | Search |
| `r` | Refresh |
| `c` | Compose or create |
| `a` | Actions |
| `Enter` | Open or confirm |
| `Esc` | Back or cancel |
| `?` | Help |
| `q` | Quit |

Mail and event editors **prepare** writes. A separate preview modal must be
confirmed before anything is sent or saved at the provider.

## How writes work

This is the same rule for the TUI, CLI, and MCP:

1. A send, reply, calendar create, or other mutation **prepares** an
   operation. Nothing has happened at the provider yet.
2. Inspect the preview (connection, identity, recipients or calendar,
   attachments, side effects).
3. Only `operation execute TOKEN` (or the TUI confirm) performs the write.

Tokens last ten minutes. Repeating execute returns the original result.
Changed or expired operations must be prepared again. An uncertain SMTP
result after `DATA` is never retried automatically.

CLI data commands print JSON, except `calendar ics`, which prints
`text/calendar` unless you pass `--output`.

## CLI

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
```

Selectors intersect exact connection IDs/names, category, labels, capability,
and calendar collections. List cursors are opaque and bound to the query;
new or recovered sources join only a fresh traversal. IMAP cursors also bind
UIDVALIDITY and the initial UID boundary.

Outbound attachment payloads are limited to 25 MiB total per prepared mail
or draft. Path-backed attachments must be regular files; directories,
devices, pipes, and files that grow past the limit are rejected before
provider I/O.

Provider-side draft create/update requires IMAP `UIDPLUS` (or IMAP4rev2) so
the appended draft always has an addressable UID. Sent-copy APPEND remains
compatible without it.

Run `posthouse <command> -h` for flags.

## MCP (for agents)

Stdio is the usual local-process transport. Example client config:

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

`POSTHOUSE_MCP_TOKEN` is mandatory for every Streamable HTTP listener,
including loopback. Stdio is the only transport with implicit local-process
authentication. Request bodies are capped at 36 MiB (one operation's
base64-encoded 25 MiB attachment allowance plus JSON envelope).

The endpoint is `/mcp`. `/healthz` is process liveness. `/readyz` checks
configuration, cache key/migration, and internal services — not live
provider connectivity. Use `connection_doctor` and `sync` for that.

The direct server is loopback-only because it serves HTTP. Expose it
remotely only through a TLS-terminating reverse proxy to the loopback
listener, and keep bearer-token authentication. `--allow-container-listener`
exists only for a container whose published port is constrained to loopback
or protected by TLS; the supplied Compose file uses it with a `127.0.0.1`
host publication.

Typed tools cover connection listing/doctor; message search/body/attachment
reads; send, reply, forward, draft, and message-action preparation; event
listing/ICS/CRUD preparation; operation show/execute; sync; and cache
status. Tool errors are for invalid requests or total failure. Successful
multi-source reads carry structured partial errors in the result.

Headless MCP and Docker deployments must use environment secret references
and set `POSTHOUSE_CACHE_KEY` to a base64- or hex-encoded 32-byte key.
Desktop use creates a path-scoped cache master key in an isolated
OS-credential namespace. Plaintext fallback is never used. A wrong cache key
makes `/readyz` fail.

## Docker

```sh
cp .env.example .env
# Replace every placeholder, especially POSTHOUSE_CACHE_KEY and POSTHOUSE_MCP_TOKEN.
docker compose up --build
```

The service binds `127.0.0.1:8791`, mounts the Docker-managed
`posthouse-data` volume at `/data`, and uses `/data/config.json` plus
`/data/posthouse.db` by default. Keep that named volume writable by the
image's non-root Posthouse user; inspect or back it up with ordinary Docker
volume commands rather than replacing it with an unowned host bind mount.

## Cache, offline, and rekey

- Defaults: 90 days of message metadata, 30 days of bodies, events from 90
  days past through 365 days future, and attachments only after explicit
  access.
- Default encrypted-state limit: 2 GiB, with LRU attachment eviction before
  message bodies.
- Live-first is the default; stale-cache fallback is explicit in result
  metadata. `--offline` never contacts providers. `--refresh` refuses stale
  fallback.
- Offline full-text search uses encrypted headers and bodies already cached.
  An `offline_search_incomplete` warning means uncached content may be
  omitted.
- Attachment reads return a cursorless final chunk when the cache cannot
  retain them; multi-chunk reads need enough `cache.max_bytes` for the
  encrypted snapshot.
- The cache is not a provider backup. Clearing it removes local cached
  content, not provider data or the prepared-operation ledger.

Headless rekey cannot modify its parent shell. Keep both keys available
until the command succeeds, then replace the active key before starting any
other Posthouse process:

```sh
export POSTHOUSE_CACHE_KEY_NEW='new-base64-or-hex-encoded-32-byte-key'
posthouse cache rekey --key-env POSTHOUSE_CACHE_KEY_NEW
export POSTHOUSE_CACHE_KEY="$POSTHOUSE_CACHE_KEY_NEW"
unset POSTHOUSE_CACHE_KEY_NEW
```

The command returns a `required_action` field in headless mode. A process
that still holds the old key is prevented from writing and must be
restarted. Desktop keychain rekeys keep an encrypted recovery record in the
same SQLite transaction; if keychain activation is interrupted after commit,
the next startup recovers automatically.

## Troubleshooting

| Symptom | What to try |
| --- | --- |
| `posthouse: command not found` | Put `$(go env GOPATH)/bin` on `PATH`, or run `./bin/posthouse` from a clone |
| Doctor or discover fails | Confirm IMAP/SMTP/CalDAV host:port, TLS flags, and that the env/keychain secret is set |
| Writes seem to do nothing | Check that you ran `operation execute` (or confirmed the TUI preview) before the ten-minute token expired |
| `/readyz` fails in Docker | `POSTHOUSE_CACHE_KEY` must be a 32-byte key, base64 or hex encoded |
| Draft create fails | The server needs IMAP `UIDPLUS` or IMAP4rev2 |
| Want a missing capability | [Open an issue](https://github.com/timborovkov/posthouse/issues/new/choose) — that is expected |

Do not paste live passwords, tokens, or message contents into issues.

## What is not in v0.2

OAuth and native Gmail/Microsoft APIs, live-provider certification, HTML
composition, contacts, permanent IMAP expunge, CalDAV scheduling/free-busy,
background-daemon sync, and external push notifications. Details are in
[TODO.md](./TODO.md).
