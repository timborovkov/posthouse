# Contributing

Posthouse is an early-stage Go project. Small vertical changes with protocol fixtures and tests are preferred over broad connector abstractions without a working provider.

## Setup

Install Go 1.26.6, clone the repository, and run:

```sh
go mod download
make validate
```

Live provider credentials are never required for the unit suite. Use local test servers or fixture transports. Keep real addresses, calendar URLs, tokens, passwords, and message contents out of tests, issues, logs, and commits.

## Change flow

1. Create a focused branch.
2. Preserve the terms in `CONTEXT.md`; update the glossary when the domain language genuinely changes.
3. Add protocol or selector tests for behavior changes.
4. Update `README.md`, `GETTING-STARTED.md`, and `TODO.md` in the same change when capabilities or limitations move.
5. Run `make validate` before opening a pull request.
6. Explain externally visible side effects and security implications in the PR description.

The local and CI gates are:

```sh
make test              # fast race-enabled unit suite; no Docker
make test-integration  # real GreenMail and Radicale protocol tests
make test-e2e          # built-binary CLI and MCP workflows
make validate          # Docker-free format, vet, unit, and build gate
make validate-all      # complete release gate, including generation and Docker
```

Docker tests bind only to loopback, use disposable `work` and `personal` principals, and tear down volumes after every run. CI splits `unit-build` from `docker-e2e`; both must pass before a release. Review responses should either change the code or explain why the current behavior is intentional. Releases should be tagged with semantic versions and include binaries plus an OCI image.
