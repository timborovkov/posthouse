---
name: posthouse-mcp
description: Configure and call Posthouse MCP tools over stdio (local) or Streamable HTTP (localhost or a private server). Use when an MCP client such as Claude, Cursor, Codex, or Hermes should operate mail and calendar connections through Posthouse.
---

# Posthouse MCP

Posthouse exposes typed MCP tools. It is not a hosted SaaS. Choose stdio on the same machine, or Streamable HTTP against a personal `posthouse serve` deployment.

## Local stdio

Stdio is the only transport with implicit local-process authentication. Point the client at the `posthouse` binary:

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

Headless and Docker deployments must set `POSTHOUSE_CACHE_KEY`. Desktop use can rely on the OS keychain for the cache key.

## Remote or localhost HTTP

`POSTHOUSE_ACCESS_KEY` (alias `POSTHOUSE_MCP_TOKEN`) is required. Endpoint is `/mcp`. Send `Authorization: Bearer <key>`.

```sh
posthouse serve --address 127.0.0.1:8791
```

Failed auth is rate-limited; do not spray tokens. `/healthz` is liveness; `/readyz` is config and cache-key readiness, not provider connectivity.

## Tools

Read-only: `connections_list`, `connection_doctor`, `messages_search`, `messages_get`, `messages_attachment_get`, `events_list`, `event_ics_generate`, `operation_show`, `cache_status`.

Prepare-only (no provider side effect): `messages_send_prepare`, `messages_reply_prepare`, `messages_forward_prepare`, `messages_draft_prepare`, `messages_action_prepare`, `event_create_prepare`, `event_update_prepare`, `event_delete_prepare`.

Destructive and idempotent: `operation_execute`. `sync` refreshes encrypted local cache.

## Rules

- Writes resolve to exactly one connection. Never fan out a send or CalDAV mutation.
- Call the matching `*_prepare` tool, show the preview (connection, identity, recipients or calendar, attachments), then `operation_execute` only after confirmation.
- Pass `next_cursor` back unchanged with identical filters.
- Attachment chunks need `cursor` when `offset > 0`. There is no `path` field for remote attachments.
- Tool errors are invalid requests or total failure. Multi-source reads can succeed with structured partial errors in the result.
