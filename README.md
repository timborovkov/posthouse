# Posthouse

[![CI](https://github.com/timborovkov/posthouse/actions/workflows/ci.yml/badge.svg)](https://github.com/timborovkov/posthouse/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Local-first CLI, MCP server, and terminal app for multiple mail and calendar
connections. Generic IMAP/SMTP, CalDAV, and read-only ICS feeds, plus native
Gmail API and Microsoft Graph backends. Reads can span connections; every
write is previewed, then executed on exactly one connection.

Send text or HTML (`--html` / `--html-file`, MCP `html`, TUI body type). The
TUI is optional: config, CLI, and MCP already cover every operation.
Recipients are raw addresses; there is no contacts registry.

v0.2 protocol surface stays. Native Gmail/Microsoft connections are additional
backends behind the same selector, message/event shapes, and prepare-before-execute
flow. See [TODO.md](./TODO.md).

Built by [Tim Borovkov](https://timb.dev). Free to use under the
[MIT License](LICENSE).

## Install

```sh
go install github.com/timborovkov/posthouse/cmd/posthouse@latest
posthouse tui
```

Full setup, CLI, MCP, and Docker: [INSTALLATION-AND-USAGE-GUIDE.md](./INSTALLATION-AND-USAGE-GUIDE.md).

## Develop

Go 1.26.6. Clone, then:

```sh
go mod download
make validate
```

```sh
make build             # ./bin/posthouse
make generate          # regenerate TUI from internal/tui/app.gsx
make test              # unit tests, no Docker
make test-integration  # GreenMail + Radicale
make test-e2e          # CLI and MCP against local servers
make validate-all      # full release gate
```

No live provider accounts needed. Generated `_gsx.go` is committed; CI fails
if `make generate` would change it.

## Contributing

Open an [issue](https://github.com/timborovkov/posthouse/issues/new) if
something is missing or broken. PRs welcome — see
[CONTRIBUTING.md](./CONTRIBUTING.md). Do not paste secrets or message/event
content. Security: [SECURITY.md](./SECURITY.md).

## License

[MIT](LICENSE) — free to use, copy, modify, and share, including commercially.
Copyright (c) 2026 [Tim Borovkov](https://timb.dev) and Posthouse contributors.

Domain language: [CONTEXT.md](./CONTEXT.md).
