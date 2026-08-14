# Agent instructions

- Read `CONTEXT.md` before changing domain terms or public command/tool names.
- Run `make validate` after code changes; keep unit tests independent of live provider credentials.
- Fetch current primary documentation before using or changing third-party APIs.
- Preserve the local-first boundary: do not persist message or event content without an explicit architectural decision.
- Never log, fixture, return, or commit secrets. MCP and CLI write operations must resolve to exactly one connection.
- Keep `README.md`, `TODO.md`, examples, and MCP tool descriptions aligned with shipped behavior.
