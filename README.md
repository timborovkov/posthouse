# Posthouse

[![CI](https://github.com/timborovkov/posthouse/actions/workflows/ci.yml/badge.svg)](https://github.com/timborovkov/posthouse/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**One local switchboard for mail, calendars, and agents.**

You juggle work and personal mailboxes, a few calendars, and maybe an agent
that needs to read them. Providers do not speak the same language. Posthouse
does: search across connections, then preview and execute every write on
exactly one. It is personal software you run on a machine you control — CLI,
MCP, REST, and an optional terminal UI. Free and open source under MIT.

![Posthouse TUI unified inbox with demo work and personal messages](docs/images/tui-inbox.png)

IMAP/SMTP, CalDAV, and read-only ICS feeds today. Native Gmail and Microsoft
sign-in are on the roadmap. Recipients are raw addresses; there is no contacts
registry. The TUI is optional — config, CLI, and MCP cover every operation.

Built by [Tim Borovkov](https://timb.dev). Site and privacy pages for OAuth
verification live in [`website/`](./website/).

v0.2. No OAuth yet. See [TODO.md](./TODO.md).

## Install

```sh
go install github.com/timborovkov/posthouse/cmd/posthouse@latest
posthouse tui
```

Non-technical path (first connection, agents, private server):
[GETTING-STARTED.md](./GETTING-STARTED.md).

Full CLI, MCP, REST, Docker, and Railway:
[INSTALLATION-AND-USAGE-GUIDE.md](./INSTALLATION-AND-USAGE-GUIDE.md).

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
