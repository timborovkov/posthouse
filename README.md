# Posthouse

Posthouse is a local-first Go CLI and MCP server that gives people and agents one interface across multiple email connections and read-only calendar feeds. Connections have stable names, one category, and any number of labels, so an operation can target `work + acme`, `personal + primary`, or an exact connection without exposing provider details to the agent.

> **Status:** v0.1 proof of concept implementing the intended v1 boundary: IMAP/SMTP mail, read-only ICS feeds, and portable ICS generation. OAuth mail authentication, message body/attachment retrieval, provider calendar mutation, and a full-screen TUI are later milestones.

## What works

- Add, list, select, update, and remove connections from the CLI.
- List and search multiple IMAP mailboxes with bounded message previews.
- Send plain-text email over SMTP, including CC, BCC, and Reply-To.
- List and search events from private or public ICS subscription feeds.
- Generate standards-compliant `.ics` files to stdout, a local file, or an embedded MCP resource without modifying a provider.
- Select connections by exact ID/name, category, and intersected labels.
- Use the same operations as typed MCP tools over stdio or modern Streamable HTTP.
- Run as one Go binary or a small Docker container.
- Keep passwords out of configuration by storing only environment-variable references.

## Mental model

```text
selector: category=work + label=acme + capability=calendar
                              │
                              ▼
                  one or more connections
                    ┌─────────┴─────────┐
                    │                   │
               IMAP / SMTP          ICS feed
                    │                   │
                    └─────────┬─────────┘
                              ▼
                    CLI JSON or MCP tools
```

A **connection** is one authenticated provider endpoint. An **identity** is the name/address presented to recipients. A connection may offer mail, calendar, or both. See [CONTEXT.md](./CONTEXT.md) for the full project language.

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

The GitHub organization in `go.mod` is a proposed home for the working title; change it before publishing if a different organization or final name is chosen.

## Configure a connection

Copy [examples/connection.json](./examples/connection.json), adjust the endpoints, and add it:

```sh
export ACME_MAIL_PASSWORD='an app password or provider password'
export ACME_CALENDAR_ICS_URL='a private https://.../calendar.ics URL'
posthouse connection add --file examples/connection.json
posthouse connection list
posthouse config path
```

Use `--replace` to update an existing ID. Put `--config /path/to/config.json` before the command to use another config file, or set `POSTHOUSE_CONFIG`.

The config stores environment-variable names (for example `ACME_MAIL_PASSWORD` and `ACME_CALENDAR_ICS_URL`), never their values. Treat a private ICS URL as a password: anyone holding it may be able to read the calendar. Prefer provider app passwords for mail where allowed and a local secret manager that injects environment variables. Many Google Workspace and Microsoft 365 mail tenants require OAuth, which is not implemented yet.

## CLI

Data commands emit JSON for scripting and agents. `calendar ics` is the deliberate exception: it emits the actual `text/calendar` file to stdout unless `--output PATH` is supplied, in which case it writes the file securely and reports JSON metadata.

```sh
# Unread messages across all primary work connections
posthouse mail list --category work --label primary --unread --page-size 20

# Search one company, with selectors intersected
posthouse mail search --category work --label acme --query 'renewal'

# Send through exactly one connection
posthouse mail send \
  --connection acme \
  --to teammate@example.com \
  --subject 'Status' \
  --body-file ./status.txt

# Next 30 days is the default calendar window
posthouse calendar list --category work --label primary

posthouse calendar ics \
  --title 'Planning' \
  --start '2026-08-17T09:00:00+03:00' \
  --end '2026-08-17T10:00:00+03:00' \
  --attendee teammate@example.com \
  --output planning.ics

# Stream the actual ICS file, suitable for piping or attaching
posthouse calendar ics \
  --title 'Planning' \
  --start '2026-08-17T09:00:00+03:00' \
  --end '2026-08-17T10:00:00+03:00' > planning.ics

# Lightweight connection dashboard
posthouse tui
```

Repeat `--connection`, `--label`, recipient, or attendee flags, or pass comma-separated values. Categories and labels are case-insensitive. When multiple selector fields are present, all must match.

### Pagination

Every list/search response is an object containing its items and, when more results exist, `next_cursor`:

```json
{
  "messages": [],
  "next_cursor": "opaque-token"
}
```

Pass that value back with `--cursor` and keep every selector and search filter unchanged:

```sh
posthouse mail search --query renewal --page-size 25
posthouse mail search --query renewal --page-size 25 --cursor 'opaque-token'
```

Defaults and maximums are 50/200 connections, 25/100 messages, and 100/500 events. Cursors are opaque and query-bound; changing a category, label, folder, query, time range, or resolved connection set invalidates them. Connection and ICS-event cursors also bind the ordered source-key snapshot, so adding/removing a connection or changing the feed's event set requires restarting that listing. IMAP cursors bind each mailbox's `UIDVALIDITY` and initial `UIDNEXT` boundary: new arrivals wait for the next traversal, while a provider UID reset produces an explicit “restart pagination” error rather than an incorrect page.

Posthouse intentionally has no offset or page-number API. Connections and events use stable keyset continuation. Cross-account mail uses one UID continuation point per mailbox and merges results by received time.

## MCP for agents

### Stdio

Configure any MCP client to spawn:

```json
{
  "mcpServers": {
    "posthouse": {
      "command": "/absolute/path/to/posthouse",
      "args": ["mcp", "stdio"],
      "env": {
        "ACME_MAIL_PASSWORD": "...",
        "ACME_CALENDAR_ICS_URL": "..."
      }
    }
  }
}
```

### Streamable HTTP

```sh
export POSTHOUSE_MCP_TOKEN='generate-a-long-random-token'
posthouse mcp http --address 127.0.0.1:8791
```

The endpoint is `http://127.0.0.1:8791/mcp`; send `Authorization: Bearer …` when a token is configured. A token is mandatory when binding outside a loopback address. `/healthz` is unauthenticated and contains no connection data.

### Tools

| Tool | Effect |
| --- | --- |
| `connections_list` | Read-only connection discovery with secret references redacted |
| `messages_search` | Read-only IMAP list/search across a selector |
| `messages_send` | External side effect: sends email through one connection |
| `events_list` | Read-only ICS feed list/search across a selector |
| `event_ics_generate` | Returns event JSON, raw ICS, a safe filename, and an embedded `text/calendar` resource |

Email writes deliberately require an exact connection. ICS generation is read-only with respect to external systems: the client decides whether to save, attach, or import the returned artifact.

The three list/search tools accept `page_size` and `cursor`, and return `next_cursor` when another page exists. Agents should treat cursors as opaque and copy them unchanged.

## Docker

```sh
docker build -t posthouse:local .
docker compose up --build
```

The Compose service mounts `./data/config.json` at `/data/config.json`, listens on port 8791, and requires `POSTHOUSE_MCP_TOKEN`. Add each connection's secret environment variable to your local `.env`; never commit it.

## Security boundaries

- Configuration is written atomically with mode `0600`.
- Secret values are read only from the process environment and are never returned by CLI/MCP connection listing.
- Non-local HTTP binding is refused without bearer authentication.
- HTTP request bodies are capped at 4 MiB.
- Calendar feed URLs are redacted from connection listings and cleartext HTTP is refused except for localhost.
- SMTP/IMAP should use implicit TLS or STARTTLS. Cleartext protocol support is intended only for explicitly trusted development servers.
- Posthouse queries providers live and does not create a central mailbox/calendar cache in v0.1.

An MCP server can read mail and calendars and send email: treat access to the process and its bearer token as access to every configured connection. ICS generation itself has no external side effect. Do not expose the HTTP port directly to the public internet; put production use behind a private network or authenticated reverse proxy.

## Project map

```text
cmd/posthouse/        executable
internal/cli/         command and terminal interface
internal/config/      atomic config store and validation
internal/mail/        IMAP search and SMTP send
internal/calendar/    ICS feed client, parser, and generator
internal/mcpserver/   typed MCP tools and transports
internal/service/     shared application operations
internal/selector/    connection selection semantics
examples/             safe configuration examples
```

## Development

```sh
make format
make test
make validate
```

See [CONTRIBUTING.md](./CONTRIBUTING.md), [TODO.md](./TODO.md), and [design.md](./design.md). The repository is MIT licensed.
