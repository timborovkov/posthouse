---
name: posthouse-rest
description: Call a self-hosted Posthouse REST /v1 API. Use when posthouse serve is up and the agent should use HTTP instead of the CLI.
---

# Posthouse REST API

Personal `posthouse serve` deployment: REST at `/v1`, MCP at `/mcp`. Prefer CLI skills when `posthouse` is local.

## Auth

```
Authorization: Bearer $POSTHOUSE_ACCESS_KEY
```

`X-Posthouse-Key` is equivalent. `/healthz` and `/readyz` are unauthenticated. Failed auth returns 429 with `Retry-After` after several attempts — do not brute-force.

Base URL: `http://127.0.0.1:8791` or the user's HTTPS origin. Discover routes with `GET /v1`.

## Contract

JSON bodies match MCP tool inputs. Writes need exact `connection` and return a prepared operation. Only `POST /v1/operations/execute` with `{"token":"..."}` performs the side effect. Show the preview and wait for confirmation.

Use base64 `data` on attachments (no filesystem `path`). ≤ 25 MiB total per mail/draft operation.

```sh
curl -sS -H "Authorization: Bearer $POSTHOUSE_ACCESS_KEY" "$POSTHOUSE_URL/v1/connections"
curl -sS -H "Authorization: Bearer $POSTHOUSE_ACCESS_KEY" -H "Content-Type: application/json" \
  -d '{"connection":"work","to":["teammate@example.test"],"subject":"Status","text":"Update"}' \
  "$POSTHOUSE_URL/v1/mail/send"
```

Useful routes: `POST /v1/mail/search`, `/mail/get`, `/mail/send`, `/calendar/events`, `/calendar/create`, `/operations/show`, `/operations/execute`, `/sync`, `GET /v1/cache`.
