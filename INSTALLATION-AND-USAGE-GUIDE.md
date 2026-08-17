# Installation and usage

Posthouse is a personal switchboard, not a hosted product. You run it on your
laptop or on a machine you control. A shorter first-run path is in
[GETTING-STARTED.md](./GETTING-STARTED.md).

## Install

**Go 1.26.6** (CLI and TUI):

```sh
curl -fsSL https://raw.githubusercontent.com/timborovkov/posthouse/main/scripts/install.sh | sh
```

Or:

```sh
go install github.com/timborovkov/posthouse/cmd/posthouse@latest
posthouse help
```

Put `$(go env GOPATH)/bin` on your `PATH` if needed.

**From a clone:**

```sh
make build
./bin/posthouse help
```

**Docker** (MCP + REST HTTP, not the TUI): see [Docker](#docker).

You need IMAP/SMTP and/or CalDAV (or an ICS feed URL). Use an app password,
not your normal login. OAuth is not in v0.2; the static [website/](./website/)
privacy page is prepared for future Google/Microsoft verification.

## Secrets

```sh
posthouse setup
```

Prints two values (or pass `--write-env PATH` for a mode-`0600` file):

- `POSTHOUSE_CACHE_KEY` encrypts local Posthouse state. Desktop use can skip
  this and let Posthouse store a key in the OS keychain; headless and Docker
  **must** set it.
- `POSTHOUSE_ACCESS_KEY` protects HTTP (MCP + REST). Stdio MCP on your machine
  does not need it. `POSTHOUSE_MCP_TOKEN` is accepted as an alias; if both are
  set they must match. The access key must be at least 16 characters.

Do not commit env files that contain these values.

## Add a connection

Copy [examples/connection.json](./examples/connection.json), fill in hosts and
your address, keep the password in the environment:

```sh
export ACME_MAIL_PASSWORD='app password'
export ACME_CALENDAR_PASSWORD='app password'
posthouse connection add --file examples/connection.json
posthouse connection discover acme
posthouse connection doctor acme
```

`discover` saves folders and calendar collections. The printed JSON is
redacted — do not feed it to `connection update`.

Read-only ICS feed: [examples/feed-connection.json](./examples/feed-connection.json).

Config path: `posthouse config path` (or `--config` / `POSTHOUSE_CONFIG`).
State is `posthouse.db` next to it.

Secret in the JSON is one of:

```json
{"secret":{"env":"ACME_MAIL_PASSWORD"}}
```

```json
{"secret":{"keychain":"acme-mail"}}
```

```sh
printf '%s' "$ACME_MAIL_PASSWORD" | posthouse connection secret set acme-mail --file -
```

Remote servers need real TLS or STARTTLS. Cleartext auth is loopback-only.
Config v1 is migrated atomically to v2 on load and backed up as `*.v1.bak`.

## Terminal app

The TUI is optional. Config files plus env/keychain secrets, CLI, and MCP
already cover every operation; use the TUI for by-hand setup and tweaking.
Recipients are raw addresses. There is no contacts registry and no WYSIWYG
HTML editor.

```sh
posthouse tui
```

`Tab` / `Shift+Tab` cycle areas and form fields, arrows or `j`/`k` to move,
`/` search, `r` refresh the current page, `c` compose or create, `a` actions,
`d` discover the selected connection, `s` save a loaded attachment (0600,
never overwrite), `n` / `PageDown` and `p` / `PageUp` page inbox (25) and
agenda (100) with opaque cursors, `Enter` open/confirm/prepare, `Esc` back,
`?` help, `q` quit.

Compose includes To, optional CC/BCC, subject, body type (`text` default, or
`html`), a body textarea, and attachment paths. Choosing `html` sends the
textarea as HTML; paste markup. Agenda times are RFC3339 with a visible
example and live valid/invalid markers. Writes still go through a preview
before anything hits the provider.

## Writes

CLI, TUI, MCP, and REST all work the same: prepare → inspect preview → execute.
Nothing is sent or saved until `operation execute TOKEN` (CLI),
`operation_execute` (MCP), or `POST /v1/operations/execute` (REST). Tokens last
ten minutes.

CLI output is JSON, except `calendar ics`.

```sh
posthouse mail list --category work --label primary --unread
posthouse mail search --query renewal --page-size 25
posthouse calendar list --collection team --start 2026-08-01T00:00:00Z

posthouse mail get --connection work --folder INBOX --uid 42
posthouse mail send --connection work --to teammate@example.test --subject Status --body-file status.txt
posthouse mail send --connection work --to teammate@example.test --subject Status --html-file status.html
posthouse operation show 'TOKEN'
posthouse operation execute 'TOKEN'

posthouse mail reply --connection work --folder INBOX --uid 42 --body 'Thanks'
posthouse mail archive --connection work --folder INBOX --uid 42

posthouse calendar create --connection work --file event.json
posthouse calendar ics --title Planning --start 2026-08-17T09:00:00+03:00 --end 2026-08-17T10:00:00+03:00 --output planning.ics
posthouse sync
posthouse cache status
```

`--offline` uses only the local cache. `--refresh` requires a live read.
`posthouse <command> -h` for flags. Attachments on send/draft: 25 MiB max.
HTML send uses `--html` / `--html-file` (and MCP `html`); text-only stays
`text/plain`, HTML-only gets a derived plain fallback, both become
`multipart/alternative`.

## MCP and REST

Local stdio (no access key):

```json
{
  "mcpServers": {
    "posthouse": {
      "command": "/absolute/path/to/posthouse",
      "args": ["mcp", "stdio"],
      "env": {
        "POSTHOUSE_CACHE_KEY": "...",
        "ACME_MAIL_PASSWORD": "..."
      }
    }
  }
}
```

HTTP serves Streamable MCP at `/mcp` and REST at `/v1` from one process.
An access key is required, including on loopback:

```sh
export POSTHOUSE_ACCESS_KEY='a-long-random-token'
export POSTHOUSE_CACHE_KEY='a-base64-or-hex-encoded-32-byte-key'
posthouse serve --address 127.0.0.1:8791
```

`posthouse mcp http` is the same listener. `/healthz` is liveness; `/readyz` is
config and cache key, not provider connectivity. `GET /v1` lists REST routes.
Request bodies are capped at 36 MiB.

Failed bearer attempts are counted per client address; eight failures in
fifteen minutes return `429` with `Retry-After`. Set `POSTHOUSE_TRUST_PROXY=1`
only when a TLS reverse proxy is the connecting peer (loopback or private
network) and supplies `X-Real-IP` or `X-Forwarded-For`. Lockout then keys off
`X-Real-IP` when present, otherwise the rightmost `X-Forwarded-For` hop.
Forwarded headers from a public connecting address are ignored.

The direct server is restricted to loopback. Expose it remotely only through a
TLS-terminating reverse proxy, and keep bearer auth. `--allow-container-listener`
is for a container whose published port is already loopback- or TLS-constrained.
Hosted platforms that inject `PORT` still need that flag (or
`POSTHOUSE_ALLOW_CONTAINER_LISTENER=1`) before Posthouse will listen on a
non-loopback address. To bind a specific address, set `--address` or
`POSTHOUSE_HTTP_ADDRESS`.

## Agent skills

```sh
posthouse skill list
posthouse skill install --agent claude connections mail calendar
posthouse skill install --agent claude --all
posthouse skill install --dir ./.agents/skills connections mail calendar
```

`--agent` accepts `claude`, `cursor`, `codex`, or `hermes`. `--agent codex` installs into `~/.agents/skills` and also refreshes `~/.codex/skills`. Prefer the CLI trio locally; add `mcp` / `rest` (or `--all`) only when the agent uses those transports. Re-running install refreshes files and removes retired skill folders. Writes always prepare, then execute after confirmation.

## Docker

```sh
# Either copy the example and fill in keys:
cp .env.example .env
# Or generate a new file (refuses to overwrite an existing .env unless you pass --force):
# posthouse setup --write-env .env
# Then set POSTHOUSE_CACHE_KEY and POSTHOUSE_ACCESS_KEY if you copied the example.
docker compose up --build
```

Listens on `127.0.0.1:8791`. Data is the `posthouse-data` volume (`/data`).

To publish on all host interfaces — only on a private network or behind TLS:

```sh
docker compose -f docker-compose.yml -f docker-compose.private.yml up --build
```

[railway.json](./railway.json) builds the Dockerfile, health-checks `/healthz`,
and expects a volume at `/data`. Set `POSTHOUSE_CACHE_KEY`,
`POSTHOUSE_ACCESS_KEY`, `POSTHOUSE_TRUST_PROXY=1`, and provider secrets.
Railway injects `PORT`; [railway.json](./railway.json) also passes
`--allow-container-listener` so the process listens on `0.0.0.0:$PORT`. This is
still a personal process, not a Posthouse-hosted service.

## Cache

Encrypted local SQLite, not a backup of your mailbox. `cache clear` drops
local cache, not provider data. Headless rekey:

```sh
export POSTHOUSE_CACHE_KEY_NEW='new-base64-or-hex-encoded-32-byte-key'
posthouse cache rekey --key-env POSTHOUSE_CACHE_KEY_NEW
export POSTHOUSE_CACHE_KEY="$POSTHOUSE_CACHE_KEY_NEW"
unset POSTHOUSE_CACHE_KEY_NEW
```

Then restart anything still running with the old key.

## License

[MIT](./LICENSE). Built by [Tim Borovkov](https://timb.dev).
