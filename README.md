# Posthouse

[![CI](https://github.com/timborovkov/posthouse/actions/workflows/ci.yml/badge.svg)](https://github.com/timborovkov/posthouse/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Posthouse is a local-first Go CLI, MCP server, and full-screen terminal app
for operating multiple mail and calendar connections through one safe
interface.

It speaks generic IMAP/SMTP, read-only ICS feeds, and mutable CalDAV. People
and agents get the same contracts: JSON on the CLI, typed MCP tools, and a
keyboard-complete TUI. Writes never go out until you inspect a preview and
execute a short-lived prepared token.

> **v0.2.0.** OAuth, native Gmail/Microsoft APIs, HTML composition, permanent
> mail deletion, CalDAV scheduling/free-busy, and live-provider certification
> are intentionally outside this release.

## Install

```sh
go install github.com/timborovkov/posthouse/cmd/posthouse@latest
posthouse tui
```

Configuration, the terminal app, CLI, MCP, and Docker are in
[INSTALLATION-AND-USAGE-GUIDE.md](./INSTALLATION-AND-USAGE-GUIDE.md). That
guide is written for both technical and non-technical users.

## Why Posthouse

Mail and calendar providers each have their own apps, APIs, and agent
plugins. Posthouse is a switchboard in front of them: one language for
connections, identities, selectors, messages, events, and prepared writes.

- **Local-first.** Config and encrypted SQLite state stay on your machine.
  Message and event content is not persisted as a canonical mailbox.
- **Safe under automation.** Reads may fan out across connections. Every
  provider mutation resolves to exactly one connection and must be executed
  explicitly.
- **Generic protocols.** No vendor SDK required for v0.2. App passwords and
  CalDAV/IMAP endpoints are enough.

## What works

- Aggregate mail, CalDAV, and ICS-feed connections, including partial-source
  errors when one connection fails.
- Fetch complete MIME messages, decoded text, sanitized HTML, threading
  headers, and bounded attachments.
- Offline full-text search over encrypted cached headers and bodies.
- Prepare and execute send, reply, forward, draft, mark, flag, move, archive,
  and trash without global IMAP expunge.
- Discover IMAP special-use folders and CalDAV collections; expand recurring
  events; ETag-guarded CalDAV create/update/delete.
- Generate portable `METHOD:REQUEST` and `METHOD:CANCEL` invitations, then
  send them as a separate prepared mail operation.
- Live-first reads with stale encrypted-cache fallback, `--offline`,
  `--refresh`, explicit sync, LRU limits, clear, and rekey.
- The same contracts over CLI JSON, MCP stdio, authenticated Streamable HTTP,
  and the Go-TUI.

See [TODO.md](./TODO.md) for work that is deliberately deferred.

## Safety in one paragraph

Reads may span a selector. Writes never do. Every mutation returns a
ten-minute opaque prepared token whose preview names the connection, acting
identity, recipients or calendar, changed fields, attachments, and side
effects. Only `operation execute TOKEN` performs the write. Provider secrets
use environment or OS-keychain references. Cached content is encrypted with
XChaCha20-Poly1305. Private content does not belong in logs, listings,
fixtures, or error bodies.

## Development

Install [Go 1.26.6](https://go.dev/dl/), clone this repository, and run:

```sh
go mod download
make validate
```

```sh
make build             # ./bin/posthouse
make generate          # regenerate the Go-TUI from internal/tui/app.gsx
make test              # race-enabled unit tests; no Docker
make test-integration  # GreenMail 2.1.11 and Radicale 3.7.3
make test-e2e          # built-binary CLI and MCP workflows
make validate          # Docker-free local gate
make validate-all      # complete release gate
```

Live provider credentials are never required. Docker protocol suites bind
only to loopback, seed disposable `work` and `personal` principals, and
discard state after each run.

The `.gsx` TUI source and generated `_gsx.go` are both committed. CI runs
`make generate-check` and fails on a diff.

## Contributing

Issues and pull requests are welcome, including "this is missing" and "this
is broken" reports. There is no CLA and no strict review ritual.

Please read [CONTRIBUTING.md](./CONTRIBUTING.md). Do not put secrets or
provider message/event content in public tickets. Security reports go to
[SECURITY.md](./SECURITY.md).

## Docs

- [INSTALLATION-AND-USAGE-GUIDE.md](./INSTALLATION-AND-USAGE-GUIDE.md) —
  install, first connection, TUI, CLI, MCP, and Docker
- [CONTEXT.md](./CONTEXT.md) — domain language
- [design.md](./design.md) — product boundaries
- [TODO.md](./TODO.md) — roadmap and deferred work
- [CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md)

## License

[MIT](./LICENSE)
