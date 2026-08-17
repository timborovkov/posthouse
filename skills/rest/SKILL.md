---
name: posthouse-rest
description: Call the user's self-hosted Posthouse REST API. Use when posthouse serve is running. The user must already have connected their mailbox; do not collect OAuth tokens over HTTP.
---

# Posthouse REST API

Personal deployment. REST at `/v1`, MCP at `/mcp`. Not a multi-tenant SaaS.

If `GET /v1/connections` is empty, tell the user to connect Gmail or Microsoft in Posthouse (`connection auth`, with `--device` on a server). Do not accept refresh tokens in REST bodies.

## Auth

```
Authorization: Bearer $POSTHOUSE_ACCESS_KEY
```

`X-Posthouse-Key` is equivalent. After several failures the server returns 429. `/healthz` and `/readyz` need no key.

## Base URL

- This machine: `http://127.0.0.1:8791`
- Their server: `https://YOUR-HOST` (Railway or reverse proxy). Never disable TLS on a public address.

`GET /v1` lists routes. JSON bodies match MCP tool inputs. Writes return a prepared token. Only `POST /v1/operations/execute` with `{"token":"..."}` sends mail. Show the preview first.

Use base64 `data` for attachments, not filesystem `path`. 25 MiB total per mail/draft.

```sh
curl -sS -H "Authorization: Bearer $POSTHOUSE_ACCESS_KEY" "$POSTHOUSE_URL/v1/connections"
```
