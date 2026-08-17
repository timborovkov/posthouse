# Installation and usage

## Install

**Go 1.26.6** (CLI and TUI):

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

**Docker** (MCP HTTP, not the TUI): see [Docker](#docker).

You need IMAP/SMTP and/or CalDAV (or an ICS feed URL). Use an app password,
not your normal login. OAuth is not in v0.2.

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

## Terminal app

```sh
posthouse tui
```

`Tab` between areas, arrows or `j`/`k` to move, `/` search, `r` refresh,
`c` compose, `a` actions, `Enter` confirm, `Esc` back, `?` help, `q` quit.
Writes still go through a preview before anything hits the provider.

## Writes

CLI, TUI, and MCP all work the same: prepare → inspect preview → execute.
Nothing is sent or saved until `operation execute TOKEN` (or the TUI
confirm). Tokens last ten minutes.

CLI output is JSON, except `calendar ics`.

```sh
posthouse mail list --category work --label primary --unread
posthouse mail search --query renewal --page-size 25
posthouse calendar list --collection team --start 2026-08-01T00:00:00Z

posthouse mail get --connection work --id MESSAGE_ID
posthouse mail send --connection work --to teammate@example.test --subject Status --body-file status.txt
posthouse operation show 'TOKEN'
posthouse operation execute 'TOKEN'

posthouse mail reply --connection work --id MESSAGE_ID --body 'Thanks'
posthouse mail archive --connection work --id MESSAGE_ID

posthouse calendar create --connection work --file event.json
posthouse calendar ics --title Planning --start 2026-08-17T09:00:00+03:00 --end 2026-08-17T10:00:00+03:00 --output planning.ics
posthouse sync
posthouse cache status
```

`--offline` uses only the local cache. `--refresh` requires a live read.
`posthouse <command> -h` for flags. Attachments on send/draft: 25 MiB max.

## MCP

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

HTTP (token required, loopback only):

```sh
export POSTHOUSE_MCP_TOKEN='a-long-random-token'
export POSTHOUSE_CACHE_KEY='a-base64-or-hex-encoded-32-byte-key'
posthouse mcp http --address 127.0.0.1:8791
```

`/mcp` is the API. `/healthz` is liveness; `/readyz` is config and cache key,
not provider connectivity. Headless/Docker must set `POSTHOUSE_CACHE_KEY`.

## Docker

```sh
cp .env.example .env   # set POSTHOUSE_CACHE_KEY and POSTHOUSE_MCP_TOKEN
docker compose up --build
```

Listens on `127.0.0.1:8791`. Data is the `posthouse-data` volume (`/data`).

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
