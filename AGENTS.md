# Agent instructions

- Read `CONTEXT.md` before changing domain terms or public command/tool names.
- Run `make validate` after code changes; keep unit tests independent of live provider credentials.
- Fetch current primary documentation before using or changing third-party APIs.
- Preserve the local-first boundary: do not persist message or event content without an explicit architectural decision.
- Never log, fixture, return, or commit secrets. MCP and CLI write operations must resolve to exactly one connection.
- Keep `README.md`, `GETTING-STARTED.md`, `INSTALLATION-AND-USAGE-GUIDE.md`, `TODO.md`, examples, and MCP tool descriptions aligned with shipped behavior.

## Cursor Cloud specific instructions

Standard commands live in `README.md` (Develop) and the `Makefile`. Notes below cover only non-obvious environment caveats.

- Go 1.26.6 is fetched automatically via the `toolchain` directive in `go.mod`; no version manager is needed. The startup update script runs `go mod download`.
- `make validate` (vet + race unit tests + gofmt + build) needs no Docker and no live provider credentials — it is the default gate to run after code changes.
- Docker is required for `make test-integration`, `make test-e2e`, `make test-container`, and `make validate-all`. Docker is pre-installed in the environment, but the daemon is NOT started automatically (no systemd). Start it once per session, e.g. `sudo dockerd &` (or in a tmux session), and wait a few seconds before running Docker targets.
- The `ubuntu` user is in the `docker` group, but a shell opened without a fresh login may not have picked it up. Either run Docker commands in a new login shell or wrap them: `sg docker -c 'make test-integration'`.
- The Docker-backed make targets start/stop GreenMail (IMAP/SMTP) and Radicale (CalDAV) themselves via `docker-compose.test.yml`; you do not start those containers manually for tests.
- For manual local connections against loopback servers (e.g. GreenMail), the IMAP/SMTP config must set `"insecure": true` — cleartext auth is loopback-only. GreenMail test users are `work:work-pass@work.test` and `personal:personal-pass@personal.test` (login by email).
- `posthouse serve` / `posthouse mcp http` require `POSTHOUSE_ACCESS_KEY` (≥16 chars) even on loopback, and `POSTHOUSE_CACHE_KEY` in headless environments (no OS keychain).
