# Contributing

Thanks for being here. Posthouse is early, and the bar is intentionally low:
if something is missing, confusing, or broken, please say so.

## Issues are welcome

[Open an issue](https://github.com/timborovkov/posthouse/issues/new/choose)
when:

- something does not work the way you expected
- installation or docs are confusing
- a capability you expected is missing
- you have an idea, even a half-formed one

A blank issue is fine. Templates exist only to make reports easier, not to
block them.

Please do **not** include passwords, tokens, cache keys, message bodies,
calendar contents, or other private provider data. Redact hostnames if they
are sensitive. Security reports belong in [SECURITY.md](./SECURITY.md), not
in a public issue.

## Pull requests are welcome

You do not need permission to send a PR. Small, focused changes are easiest
to review, but incomplete work that starts a conversation is still useful.

A helpful PR usually:

1. Keeps the terms in [CONTEXT.md](./CONTEXT.md), or updates that glossary
   when the domain language genuinely changes.
2. Adds or adjusts a test when behavior changes. Live provider credentials
   are never required; use local servers or fixtures.
3. Updates [README.md](./README.md),
   [INSTALLATION-AND-USAGE-GUIDE.md](./INSTALLATION-AND-USAGE-GUIDE.md), or
   [TODO.md](./TODO.md) when user-facing behavior or limitations move.
4. Passes `make validate` before you ask for review.

That is it. Perfect formatting is not required to start a conversation.

By contributing, you agree that your work is licensed under the
[MIT License](./LICENSE). Please follow the
[Code of Conduct](./CODE_OF_CONDUCT.md).

## Development setup

Posthouse pins Go 1.26.6. Clone the repository, then:

```sh
go mod download
make validate
```

Useful commands:

```sh
make build             # CGo-free binary at ./bin/posthouse
make generate          # regenerate the Go-TUI from app.gsx
make test              # race-enabled unit tests; no Docker
make test-integration  # GreenMail and Radicale protocol tests
make test-e2e          # built-binary CLI and MCP workflows
make validate          # Docker-free format, vet, unit, and build gate
make validate-all      # complete release gate
```

The `.gsx` TUI source and generated `_gsx.go` are both committed. Run
`make generate` when you change the TUI; CI runs `make generate-check` and
fails on a diff.

Docker tests bind only to loopback, use disposable `work` and `personal`
principals, and tear down volumes after every run. Development never needs
real mail or calendar accounts.

## Review

I will look at the PR. Review responses should either change the code or
explain why the current behavior is intentional. There is no CLA and no
required commit-message format.
