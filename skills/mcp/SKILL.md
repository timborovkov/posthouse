---
name: posthouse-mcp
description: Call Posthouse MCP for mail and calendar. Local stdio or HTTP to the user's own server. The user must already have connected Gmail/Microsoft; do not run OAuth or accept refresh tokens.
---

# Posthouse MCP

Posthouse exposes typed MCP tools. It is not a hosted SaaS.

If `connections_list` is empty, stop and tell the user to connect a mailbox in Posthouse first (`GETTING-STARTED.md`: `connection add --kind gmail` then `connection auth`). Never ask for a Google password, refresh token, or Cloud project.

## Local (this computer)

```json
{
  "mcpServers": {
    "posthouse": {
      "command": "/absolute/path/to/posthouse",
      "args": ["mcp", "stdio"]
    }
  }
}
```

Stdio is local-process auth. Headless/Docker must set `POSTHOUSE_CACHE_KEY`.

## Server (Railway, Docker, VPS)

URL `https://YOUR-HOST/mcp` with `Authorization: Bearer $POSTHOUSE_ACCESS_KEY`. Failed auth is rate-limited. `/healthz` is liveness; `/readyz` is config readiness.

## Tools

Read-only: `connections_list`, `connection_doctor`, `messages_search`, `messages_get`, `messages_attachment_get`, `events_list`, `event_ics_generate`, `operation_show`, `cache_status`.

Prepare-only: `messages_send_prepare`, `messages_reply_prepare`, `messages_forward_prepare`, `messages_draft_prepare`, `messages_action_prepare`, `event_create_prepare`, `event_update_prepare`, `event_delete_prepare`. Optional `html` alongside `text` on send/reply/forward/draft.

Then `operation_execute` after the user confirms the preview. `sync` refreshes the encrypted local cache.

## Rules

- Writes use exactly one connection.
- Show the prepare preview (connection, identity, recipients or calendar) and wait.
- Pass `next_cursor` back unchanged with identical filters.
- There is no MCP tool that opens a browser or stores a refresh token.
