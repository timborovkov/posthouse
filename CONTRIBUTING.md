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
4. Update `README.md` and `TODO.md` in the same change when capabilities or limitations move.
5. Run `make validate` before opening a pull request.
6. Explain externally visible side effects and security implications in the PR description.

CI runs formatting, vet, race-enabled tests, and a build. Review responses should either change the code or explain why the current behavior is intentional. Releases should be tagged with semantic versions and include binaries plus an OCI image.
