---
name: posthouse-mcp
description: Configure Posthouse MCP over stdio or HTTP /mcp. Use when the agent calls MCP tools instead of the local CLI.
---

# Posthouse MCP

Typed MCP tools for mail and calendar. Prefer CLI skills (`posthouse-connections`, `posthouse-mail`, `posthouse-calendar`) when `posthouse` runs on the same machine.

## Local stdio

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
    },
    "posthouse-readonly": {
      "command": "/absolute/path/to/posthouse",
      "args": ["mcp", "stdio", "--profile", "readonly"],
      "env": {
        "POSTHOUSE_CACHE_KEY": "..."
      }
    }
  }
}
```

Headless/Docker must set `POSTHOUSE_CACHE_KEY`. Desktop may use the OS keychain.

`--profile` beats config `policy.mcp_profile` (including an explicit `full`), which beats `POSTHOUSE_MCP_PROFILE`, which defaults to `full`.

## HTTP

`POSTHOUSE_ACCESS_KEY` (alias `POSTHOUSE_MCP_TOKEN`) required. Endpoint `/mcp`. `Authorization: Bearer <key>`.

```sh
posthouse serve --address 127.0.0.1:8791
# optional: --profile readonly
# same listener: posthouse mcp http --address 127.0.0.1:8791 --profile readonly
```

Failed auth is rate-limited. `/healthz` = liveness; `/readyz` = config/cache key, not providers.

## Tools

Read: `connections_list`, `connection_doctor`, `messages_search`, `messages_triage`, `messages_unread_counts`, `messages_get`, `messages_attachment_get`, `events_list`, `event_ics_generate`, `operation_show`, `cache_status`.

Prepare (no side effect; `full` only): `messages_send_prepare`, `messages_reply_prepare`, `messages_forward_prepare`, `messages_draft_prepare`, `messages_action_prepare`, `event_create_prepare`, `event_update_prepare`, `event_delete_prepare`.

Execute (`full` only): `operation_execute`. Cache refresh: `sync`.

`readonly` omits prepare/execute tools and **keeps** `operation_show`. Policy deny classes still apply under `full`: listed tools error if the class is denied.

`messages_attachment_get` with `extract_text` returns `text` plus UTF-8 `data_base64` for PDFs. Writes resolve to one connection. Prepare → show preview → execute after confirmation. Pass `next_cursor` unchanged with identical filters.
